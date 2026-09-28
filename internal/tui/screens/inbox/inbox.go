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
	Row      lipgloss.Style
	Selected lipgloss.Style
	Dim      lipgloss.Style
}

type Model struct {
	Sections []requests.Section
	Cursor   int
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

// SetSections replaces the list and keeps the cursor on the pull request it was
// on. A mark is dropped when its pull request has left the list or now has an
// open record, because a batch would skip it and the mark would claim otherwise.
func (m Model) SetSections(sections []requests.Section) Model {
	selected, had := m.Selected()
	m.Sections = sections

	rows := m.rows()
	for url := range m.Marked {
		if !slices.ContainsFunc(rows, func(r requests.Row) bool { return r.Ref.URL() == url && r.State == "" }) {
			delete(m.Marked, url)
		}
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

// SetExisting switches the screen to asking what to do with the marked pull
// requests that already have a review. others is how many marked pull requests
// have none. An empty found puts the list back.
func (m Model) SetExisting(found []Existing, others int) Model {
	m.Existing, m.Others, m.Busy = found, others, false
	return m
}

// Asking reports whether the screen is asking what to do with the marked pull
// requests that already have a review.
func (m Model) Asking() bool { return len(m.Existing) > 0 }

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
		rows = append(rows, s.Rows...)
	}
	return rows
}

func (m Model) Update(message tea.Msg) (Model, tea.Cmd) {
	key, ok := message.(tea.KeyPressMsg)
	if !ok {
		return m, nil
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
	case "r":
		if m.Loading {
			return m, nil
		}
		return m, msg.Send(msg.RefreshRequests{})
	case "t":
		return m, msg.Send(msg.OpenTeams{})
	case "esc":
		return m, msg.Send(msg.Goto{Screen: msg.Dashboard})
	case "?":
		return m, msg.Send(msg.OpenHelp{})
	}
	return m, nil
}

// choose handles the keys of the step that asks what to do with the marked pull
// requests that already have a review. The list is not on screen, so no key
// reaches it. A batch runs in the background, so view and ask, which needs the
// terminal, is not on offer.
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

// markedURLs lists the marks in the order the rows are drawn, so a batch starts
// the pull requests top to bottom.
func (m Model) markedURLs() []string {
	var urls []string
	for _, row := range m.rows() {
		if m.Marked[row.Ref.URL()] {
			urls = append(urls, row.Ref.URL())
		}
	}
	return urls
}

func (m Model) View() string {
	if m.Asking() {
		return m.existingView()
	}
	if m.Loading && len(m.Sections) == 0 {
		return m.Spinner.Render("Searching GitHub for review requests")
	}

	var lines []string
	cursorLine := 0
	index := 0
	for _, s := range m.Sections {
		title := "Requested of me"
		if s.Team != "" {
			title = s.Team
		}
		lines = append(lines, m.Styles.Group.Render(fmt.Sprintf("%s (%d)", title, len(s.Rows))))
		switch {
		case s.Err != nil:
			lines = append(lines, m.Styles.Dim.Render(format.Truncate("  "+format.OneLine(s.Err), format.Width(m.Width))))
		case len(s.Rows) == 0:
			lines = append(lines, m.Styles.Dim.Render("  none"))
		}
		for _, row := range s.Rows {
			if index == m.Cursor {
				cursorLine = len(lines)
			}
			lines = append(lines, m.row(row, index == m.Cursor))
			index++
		}
		lines = append(lines, "")
	}

	return format.Pane(m.header(), lines, cursorLine, m.Height)
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

func (m Model) row(row requests.Row, selected bool) string {
	cursor := "  "
	style := m.Styles.Row
	if selected {
		cursor = "> "
		style = m.Styles.Selected
	}
	mark := "[ ]"
	if m.Marked[row.Ref.URL()] {
		mark = "[x]"
	}
	if row.State != "" {
		mark = "   "
	}

	head := fmt.Sprintf("%s%s %s  ", cursor, mark, row.Ref)
	meta := []string{row.Author, m.age(row.UpdatedAt)}
	if row.IsDraft {
		meta = append(meta, "draft")
	}
	if row.State != "" {
		meta = append(meta, row.State.Label())
	}
	left, fitted := format.Row(head, row.Title, "· "+strings.Join(meta, " · "), format.Width(m.Width))
	if fitted == "" {
		return style.Render(left)
	}
	return style.Render(left) + " " + m.Styles.Dim.Render(fitted)
}

func (m Model) age(t time.Time) string {
	now := time.Now()
	if m.Now != nil {
		now = m.Now()
	}
	return format.Ago(now.Sub(t))
}
