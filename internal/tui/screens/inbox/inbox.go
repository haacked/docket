// Package inbox lists the pull requests waiting on the user's review and turns
// keys into intents. It holds no service and runs no commands, so its Update is
// testable with synthetic key messages.
package inbox

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/haacked/docket/internal/core/requests"
	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/tui/format"
	"github.com/haacked/docket/internal/tui/msg"
)

type Styles struct {
	Group    lipgloss.Style
	Assignee lipgloss.Style
	Row      lipgloss.Style
	Selected lipgloss.Style
	Dim      lipgloss.Style
}

type Model struct {
	// Sections is what the screen draws: every section SetList was given, less
	// the draft pull requests while ShowDrafts is false. The covered section
	// keeps its rows while ShowCovered is false, so its heading can count them.
	Sections []requests.Section
	all      requests.List
	// ShowDrafts is false until the user presses d, so the list starts with only
	// the pull requests that are ready for review.
	ShowDrafts bool
	// ShowCovered is false until the user presses c, so the list starts without
	// the pull requests that a member of the team other than the owner is
	// assigned to.
	ShowCovered bool
	Cursor      int
	// Marked holds the URLs the user marked with space. A key on the URL rather
	// than the cursor position keeps each mark on its pull request when a
	// refresh reorders the rows.
	Marked map[string]bool
	// Engine is the engine a batch runs under. It is empty when no engine has a
	// background mode, and marking is then refused.
	Engine  string
	Loading bool
	// Busy is true from the enter that sends a batch until the root answers.
	// A second enter in that time does nothing.
	Busy bool
	// Unassigned is how many marked pull requests you are not assigned to
	// while the screen asks whether to assign you. It is zero otherwise.
	Unassigned int
	// Existing lists the marked pull requests that already have a review of
	// yours while the screen asks what to do with them. It is empty otherwise.
	Existing []Existing
	// Others is how many marked pull requests start whatever the answer.
	Others  int
	Spinner format.Spinner
	Styles  Styles
	Width   int
	// Height is the rows the list may use. Zero draws every row.
	Height int
	Now    func() time.Time
}

// Existing is a marked pull request that already has a review of yours.
type Existing struct {
	Ref string
	review.Found
}

func New(styles Styles, engine string) Model {
	return Model{Styles: styles, Engine: engine, Marked: map[string]bool{}}
}

// SetList replaces the list and keeps the cursor on the pull request it was on.
// A mark is dropped when its pull request has left the list or now has an open
// record, because a batch would skip it and the mark would claim otherwise.
func (m Model) SetList(list requests.List) Model {
	selected, had := m.Selected()
	m.all, m.Sections = list, list.Sections
	if !m.ShowDrafts {
		m.Sections = withoutDrafts(list.Sections)
	}

	rows := m.rows()
	for url := range m.Marked {
		if !slices.ContainsFunc(rows, func(r requests.Row) bool { return r.Ref.URL() == url && r.State == "" }) {
			delete(m.Marked, url)
		}
	}
	// A reload can drop marks while the screen asks whether to assign you. The
	// question then counts the marks that are left. It closes when no marked
	// pull request is left that you are not assigned to.
	if m.Unassigned > 0 {
		m.Unassigned = m.unassigned()
	}
	if had {
		if i := slices.IndexFunc(rows, func(r requests.Row) bool { return r.Ref.Equal(selected.Ref) }); i >= 0 {
			m.Cursor = i
			return m
		}
	}
	m.Cursor = min(max(m.Cursor, 0), max(0, len(rows)-1))
	return m
}

// withoutDrafts drops the draft pull requests from sections. It keeps every
// section, so a section's index is the same in both lists.
func withoutDrafts(sections []requests.Section) []requests.Section {
	out := make([]requests.Section, 0, len(sections))
	for _, s := range sections {
		s.Rows = slices.DeleteFunc(slices.Clone(s.Rows), func(r requests.Row) bool { return r.IsDraft })
		out = append(out, s)
	}
	return out
}

// hiddenIn counts the drafts that withoutDrafts dropped from section i.
func (m Model) hiddenIn(i int) int {
	if m.ShowDrafts || i >= len(m.all.Sections) {
		return 0
	}
	return len(m.all.Sections[i].Rows) - len(m.Sections[i].Rows)
}

// shows reports whether the screen draws the rows of s.
func (m Model) shows(s requests.Section) bool {
	return s.Kind != requests.Covered || m.ShowCovered
}

// SetExisting switches the screen to asking what to do with the marked pull
// requests that already have a review. others is how many marked pull requests
// have none. An empty found puts the list back.
func (m Model) SetExisting(found []Existing, others int) Model {
	m.Existing, m.Others, m.Busy = found, others, false
	return m
}

// Asking reports whether the screen is asking a question about the marked pull
// requests: whether to assign you, or what to do with the ones that already have
// a review.
func (m Model) Asking() bool { return m.Unassigned > 0 || len(m.Existing) > 0 }

// Selected is the row under the cursor.
func (m Model) Selected() (requests.Row, bool) {
	rows := m.rows()
	if m.Cursor < 0 || m.Cursor >= len(rows) {
		return requests.Row{}, false
	}
	return rows[m.Cursor], true
}

func (m Model) rows() []requests.Row {
	var rows []requests.Row
	for _, s := range m.Sections {
		if m.shows(s) {
			rows = append(rows, s.Rows...)
		}
	}
	return rows
}

func (m Model) Update(message tea.Msg) (Model, tea.Cmd) {
	key, ok := message.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	if m.Unassigned > 0 {
		return m.confirmAssign(key)
	}
	if m.Asking() {
		return m.choose(key)
	}
	rows := m.rows()
	switch key.String() {
	case "j", "down":
		if m.Cursor < len(rows)-1 {
			m.Cursor++
		}
	case "k", "up":
		if m.Cursor > 0 {
			m.Cursor--
		}
	case "g", "home":
		m.Cursor = 0
	case "G", "end":
		m.Cursor = max(0, len(rows)-1)
	case "space":
		row, ok := m.Selected()
		if !ok || row.State != "" || m.Engine == "" || m.Busy {
			return m, nil
		}
		if m.Marked[row.Ref.URL()] {
			delete(m.Marked, row.Ref.URL())
		} else {
			m.Marked[row.Ref.URL()] = true
		}
	case "enter":
		if m.Busy {
			return m, nil
		}
		if urls := m.markedURLs(); len(urls) > 0 {
			if m.Unassigned = m.unassigned(); m.Unassigned > 0 {
				return m, nil
			}
			m.Busy = true
			return m, msg.Send(msg.StartBatch{URLs: urls, Engine: m.Engine})
		}
		row, ok := m.Selected()
		switch {
		case !ok:
		case row.RecordID != "":
			return m, msg.Send(msg.Resume{ID: row.RecordID})
		default:
			return m, msg.Send(msg.PrefillReview{URL: row.Ref.URL()})
		}
	case "o":
		row, ok := m.Selected()
		switch {
		case !ok:
		case row.RecordID != "":
			return m, msg.Send(msg.OpenOnGitHub{ID: row.RecordID})
		default:
			return m, msg.Send(msg.OpenPullRequest{URL: row.Ref.URL()})
		}
	case "r":
		if m.Loading {
			return m, nil
		}
		return m, msg.Send(msg.RefreshRequests{})
	case "d":
		m.ShowDrafts = !m.ShowDrafts
		return m.SetList(m.all), nil
	case "c":
		m.ShowCovered = !m.ShowCovered
		return m.SetList(m.all), nil
	case "t":
		return m, msg.Send(msg.OpenTeams{})
	case "esc":
		return m, msg.Send(msg.Goto{Screen: msg.Dashboard})
	case "?":
		return m, msg.Send(msg.OpenHelp{})
	}
	return m, nil
}

// confirmAssign handles the keys of the step that asks whether to assign you to
// the marked pull requests you are not assigned to. Either answer starts every
// marked pull request.
func (m Model) confirmAssign(key tea.KeyPressMsg) (Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.Unassigned = 0
	case "y", "n":
		m.Unassigned, m.Busy = 0, true
		return m, msg.Send(msg.StartBatch{URLs: m.markedURLs(), Engine: m.Engine, Assign: key.String() == "y"})
	}
	return m, nil
}

// choose handles the keys of the step that asks what to do with the marked pull
// requests that already have a review. The list is not on screen, so no key
// reaches it. A batch runs in the background. Open needs the terminal, so the
// screen does not offer it.
func (m Model) choose(key tea.KeyPressMsg) (Model, tea.Cmd) {
	if m.Busy {
		return m, nil
	}
	var intent review.Intent
	switch key.String() {
	case "esc":
		return m.SetExisting(nil, 0), nil
	case "a":
		intent = review.IntentAppend
	case "o":
		intent = review.IntentOverwrite
	case "s":
		if m.Others == 0 {
			return m, nil
		}
	default:
		return m, nil
	}
	m.Busy = true
	return m, msg.Send(msg.AnswerBatch{Intent: string(intent)})
}

// markedRows lists the marked rows in the order they are drawn, so a batch
// starts the pull requests top to bottom.
func (m Model) markedRows() []requests.Row {
	return slices.DeleteFunc(m.rows(), func(row requests.Row) bool { return !m.Marked[row.Ref.URL()] })
}

// unassigned counts the marked pull requests you are not assigned to.
func (m Model) unassigned() int {
	n := 0
	for _, row := range m.markedRows() {
		if !row.AssignedMe {
			n++
		}
	}
	return n
}

func (m Model) markedURLs() []string {
	var urls []string
	for _, row := range m.markedRows() {
		urls = append(urls, row.Ref.URL())
	}
	return urls
}

func (m Model) View() string {
	if m.Unassigned > 0 {
		return m.assignView()
	}
	if m.Asking() {
		return m.existingView()
	}
	if m.Loading && len(m.Sections) == 0 {
		return m.Spinner.Render("Searching GitHub for review requests")
	}

	var lines []string
	for _, err := range m.all.Errs {
		lines = append(lines, m.Styles.Dim.Render(format.Truncate(format.OneLine(err), format.Width(m.Width))))
	}
	if len(lines) > 0 {
		lines = append(lines, "")
	}
	cursorLine := 0
	index := 0
	for i, s := range m.Sections {
		count, hidden := len(s.Rows), m.hiddenIn(i)
		// Only a team's request can need a reviewer or be covered.
		noTeam := s.Kind != requests.Yours && m.all.Teams == 0
		emptyCovered := s.Kind == requests.Covered && count == 0 && hidden == 0
		if noTeam || emptyCovered {
			continue
		}
		lines = append(lines, m.heading(s, count, hidden))
		if count == 0 && hidden == 0 {
			lines = append(lines, m.Styles.Dim.Render("  none"))
		}
		if m.shows(s) {
			split := splitsByTeammate(s)
			for j, row := range s.Rows {
				if split && (j == 0 || row.Teammate != s.Rows[j-1].Teammate) {
					lines = append(lines, m.Styles.Assignee.Render(fmt.Sprintf("  %s (%d)", whose(row.Teammate), countTeammate(s.Rows, row.Teammate))))
				}
				if index == m.Cursor {
					cursorLine = len(lines)
				}
				lines = append(lines, m.row(s.Kind, row, index == m.Cursor))
				index++
			}
		}
		lines = append(lines, "")
	}

	return format.Pane(m.header(), lines, cursorLine, m.Height)
}

// heading is a section's title and count, with what the screen hides from it.
func (m Model) heading(s requests.Section, count, hidden int) string {
	heading := m.Styles.Group.Render(fmt.Sprintf("%s (%d)", title(s.Kind), count))
	if hidden > 0 {
		heading += m.Styles.Dim.Render(fmt.Sprintf(" · %d %s hidden", hidden, format.Plural(hidden, "draft")))
	}
	if !m.shows(s) && count > 0 {
		heading += m.Styles.Dim.Render(" · hidden")
	}
	return heading
}

// splitsByTeammate reports whether the section draws its rows under "Your team"
// and "Everyone else". It does not when docket could not read the members of a
// row's team, because it cannot tell whether that row's owner is a teammate.
func splitsByTeammate(s requests.Section) bool {
	return s.Kind == requests.NeedsReviewer && !slices.ContainsFunc(s.Rows, func(r requests.Row) bool { return r.MembersUnknown })
}

func title(kind requests.Kind) string {
	switch kind {
	case requests.Yours:
		return "Yours"
	case requests.NeedsReviewer:
		return "Needs a reviewer"
	}
	return "Covered by a teammate"
}

// whose names the group of pull requests that need a reviewer by whether a
// member of the team that asked owns them.
func whose(teammate bool) string {
	if teammate {
		return "Your team"
	}
	return "Everyone else"
}

func countTeammate(rows []requests.Row, teammate bool) int {
	n := 0
	for _, r := range rows {
		if r.Teammate == teammate {
			n++
		}
	}
	return n
}

// header says what a mark does, because space and enter do something different
// here than anywhere else in docket.
func (m Model) header() string {
	text := "space marks · enter starts the marked pull requests as " + m.Engine + " background reviews"
	switch {
	case m.Engine == "":
		text = "no engine has a background mode, so enter opens the selected in a new review"
	case len(m.Marked) > 0:
		text = fmt.Sprintf("%d marked · enter starts them as %s background reviews", len(m.Marked), m.Engine)
	}
	header := m.Styles.Dim.Render(text)
	if m.Loading {
		header += m.Styles.Dim.Render(" · ") + m.Spinner.Render("refreshing")
	}
	return header
}

func (m Model) assignView() string {
	width := format.Width(m.Width)
	n, marked := m.Unassigned, len(m.Marked)
	question := fmt.Sprintf("Assign yourself on GitHub to the %d marked pull requests?", n)
	switch {
	case n < marked:
		question = fmt.Sprintf("Assign yourself on GitHub to %d of the %d marked pull requests?", n, marked)
	case n == 1:
		question = "Assign yourself on GitHub to the marked pull request?"
	}
	lines := []string{m.Styles.Group.Render(format.Truncate(question, width))}
	if n < marked {
		lines = append(lines, m.Styles.Dim.Render(format.Truncate(fmt.Sprintf("You are already assigned to the other %d.", marked-n), width)))
	}
	return strings.Join(append(lines,
		m.Styles.Dim.Render(format.Truncate("Your teammates' lists then show that you took the review.", width)),
		"",
		"y  yes: assign you once each review starts",
		"n  no: start the reviews without assigning you",
		"esc  back to the list",
	), "\n")
}

func (m Model) existingView() string {
	width := format.Width(m.Width)
	title := fmt.Sprintf("%d marked pull requests already have a review of yours", len(m.Existing))
	if len(m.Existing) == 1 {
		title = "1 marked pull request already has a review of yours"
	}
	lines := []string{m.Styles.Group.Render(format.Truncate(title, width))}
	for _, found := range m.Existing {
		line := "  " + found.Ref + "  " + format.Existing(found.Found)
		lines = append(lines, m.Styles.Dim.Render(format.Truncate(line, width)))
	}

	lines = append(lines, "",
		"a  append: review what changed since your last review",
		"o  overwrite: review the whole pull request from scratch",
	)
	if m.Others > 0 {
		lines = append(lines, fmt.Sprintf("s  skip, and start the other %d", m.Others))
	}
	lines = append(lines, "esc  back to the list")
	return strings.Join(lines, "\n")
}

func (m Model) row(kind requests.Kind, row requests.Row, selected bool) string {
	cursor := "  "
	style := m.Styles.Row
	if selected {
		cursor = "> "
		style = m.Styles.Selected
	}
	if row.IsDraft {
		style = style.Faint(true)
	}
	mark := "[ ]"
	if m.Marked[row.Ref.URL()] {
		mark = "[x]"
	}
	if row.State != "" {
		mark = "   "
	}

	head := fmt.Sprintf("%s%s %s  ", cursor, mark, row.Ref)
	author := row.Author
	if row.StartedBy != "" {
		author += " for " + row.StartedBy
	}
	meta := []string{author, m.age(row.UpdatedAt)}
	if row.IsDraft {
		meta = append(meta, "draft")
	}
	if why := why(kind, row); why != "" {
		meta = append(meta, why)
	}
	if m.all.Teams > 1 && !row.AskedMe() {
		_, team, _ := strings.Cut(row.Team, "/")
		meta = append(meta, team)
	}
	left, fitted := format.Row(head, row.Title, "· "+strings.Join(meta, " · "), format.Width(m.Width))
	if fitted == "" {
		return style.Render(left)
	}
	return style.Render(left) + " " + m.Styles.Dim.Render(fitted)
}

// why says what put the row in its section, beyond what the section's heading
// says.
func why(kind requests.Kind, row requests.Row) string {
	switch {
	case row.State != "":
		return row.State.Label()
	case kind == requests.Covered:
		return "assigned to " + row.Reviewer
	case kind == requests.NeedsReviewer && row.Reviewer != "":
		if row.MembersUnknown {
			return row.Reviewer + " assigned"
		}
		return row.Reviewer + " assigned, not on the team"
	case kind != requests.Yours:
		return ""
	case row.AskedMe():
		return "asked you"
	case row.AssignedMe:
		return "assigned to you"
	}
	return ""
}

func (m Model) age(t time.Time) string {
	now := time.Now()
	if m.Now != nil {
		now = m.Now()
	}
	return format.Ago(now.Sub(t))
}
