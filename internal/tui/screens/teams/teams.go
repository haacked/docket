// Package teams lists the teams the user belongs to on GitHub and lets them
// check the ones whose review requests the requests screen lists. It holds no
// service and runs no commands, so its Update is testable with synthetic key
// messages.
package teams

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/haacked/docket/internal/tui/format"
	"github.com/haacked/docket/internal/tui/msg"
)

type Styles struct {
	Row      lipgloss.Style
	Selected lipgloss.Style
	Dim      lipgloss.Style
	Err      lipgloss.Style
}

// Row is one team, named by its "org/team" slug.
type Row struct {
	Slug    string
	Checked bool
	// Member is false for a checked team that GitHub did not list among the
	// user's teams, and for every row until GitHub answers.
	Member bool
}

type Model struct {
	Rows   []Row
	Cursor int
	// Loading is true until GitHub answers with the user's teams.
	Loading bool
	// Err is why GitHub did not list the user's teams.
	Err error
	// Busy is true from the enter that saves the checked teams until the root
	// answers.
	Busy    bool
	Spinner format.Spinner
	Styles  Styles
	Width   int
	// Height is the rows the list may use. Zero draws every row.
	Height int
}

func New(styles Styles) Model { return Model{Styles: styles} }

// Load starts the screen over with the configured teams checked, while the root
// asks GitHub for the user's teams.
func (m Model) Load(configured []string) Model {
	m.Rows, m.Cursor, m.Loading, m.Err, m.Busy = nil, 0, true, nil, false
	for _, slug := range configured {
		if m.find(slug) < 0 {
			m.Rows = append(m.Rows, Row{Slug: slug, Checked: true})
		}
	}
	sortRows(m.Rows)
	return m
}

// SetMemberships adds GitHub's list of the user's teams to the rows. GitHub's
// slugs are case-insensitive. A slug therefore matches a configured team
// without regard to case. The row takes GitHub's spelling.
func (m Model) SetMemberships(teams []string, err error) Model {
	var selected string
	if m.Cursor < len(m.Rows) {
		selected = m.Rows[m.Cursor].Slug
	}
	m.Loading, m.Err = false, err
	for _, slug := range teams {
		if i := m.find(slug); i >= 0 {
			m.Rows[i].Slug, m.Rows[i].Member = slug, true
		} else {
			m.Rows = append(m.Rows, Row{Slug: slug, Member: true})
		}
	}
	sortRows(m.Rows)
	if i := m.find(selected); i >= 0 {
		m.Cursor = i
	}
	m.Cursor = min(m.Cursor, max(0, len(m.Rows)-1))
	return m
}

func (m Model) find(slug string) int {
	return slices.IndexFunc(m.Rows, func(r Row) bool { return strings.EqualFold(r.Slug, slug) })
}

func sortRows(rows []Row) {
	slices.SortFunc(rows, func(a, b Row) int {
		return strings.Compare(strings.ToLower(a.Slug), strings.ToLower(b.Slug))
	})
}

// Checked lists the checked slugs in the order the rows are drawn.
func (m Model) Checked() []string {
	var slugs []string
	for _, r := range m.Rows {
		if r.Checked {
			slugs = append(slugs, r.Slug)
		}
	}
	return slugs
}

func (m Model) Update(message tea.Msg) (Model, tea.Cmd) {
	key, ok := message.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "j", "down":
		if m.Cursor < len(m.Rows)-1 {
			m.Cursor++
		}
	case "k", "up":
		if m.Cursor > 0 {
			m.Cursor--
		}
	case "g", "home":
		m.Cursor = 0
	case "G", "end":
		m.Cursor = max(0, len(m.Rows)-1)
	case "space":
		if m.Busy || m.Cursor >= len(m.Rows) {
			return m, nil
		}
		m.Rows[m.Cursor].Checked = !m.Rows[m.Cursor].Checked
	case "enter":
		if m.Busy {
			return m, nil
		}
		m.Busy = true
		return m, msg.Send(msg.SaveTeams{Teams: m.Checked()})
	case "esc":
		return m, msg.Send(msg.Goto{Screen: msg.Requests})
	case "?":
		return m, msg.Send(msg.OpenHelp{})
	}
	return m, nil
}

func (m Model) View() string {
	if m.Loading && len(m.Rows) == 0 {
		return m.Spinner.Render("Reading your teams from GitHub")
	}

	width := format.Width(m.Width)
	var lines []string
	if m.Err != nil {
		lines = append(lines,
			m.Styles.Err.Render(format.Truncate("  "+format.OneLine(m.Err), width)),
			m.Styles.Dim.Render(format.Truncate("  If the token lacks the read:org scope, gh auth refresh -s read:org grants it", width)),
			"",
		)
	}
	if len(m.Rows) == 0 && m.Err == nil {
		lines = append(lines, m.Styles.Dim.Render("  GitHub lists no teams for you"))
	}
	cursorLine := 0
	for i, r := range m.Rows {
		if i == m.Cursor {
			cursorLine = len(lines)
		}
		lines = append(lines, m.row(r, i == m.Cursor, width))
	}
	return format.Pane(m.header(), lines, cursorLine, m.Height)
}

// header says what checking a team does, because the requests screen is where
// the choice shows. It cuts that text rather than the work in flight, so the
// spinner stays in view on a narrow terminal.
func (m Model) header() string {
	var work string
	switch {
	case m.Busy:
		work = m.Styles.Dim.Render(" · ") + m.Spinner.Render("saving")
	case m.Loading:
		work = m.Styles.Dim.Render(" · ") + m.Spinner.Render("reading your teams from GitHub")
	}
	text := format.Truncate("The requests screen lists review requests for the checked teams", format.Width(m.Width)-format.Columns(work))
	return m.Styles.Dim.Render(text) + work
}

func (m Model) row(r Row, selected bool, width int) string {
	cursor, style := "  ", m.Styles.Row
	if selected {
		cursor, style = "> ", m.Styles.Selected
	}
	mark := "[ ]"
	if r.Checked {
		mark = "[x]"
	}
	// Until GitHub lists the user's teams, nothing says whether a configured
	// team is one of them.
	var note string
	if !r.Member && !m.Loading && m.Err == nil {
		note = "not one of your teams"
	}
	left, fitted := format.Row(cursor+mark+" ", r.Slug, note, width)
	if fitted == "" {
		return style.Render(left)
	}
	return style.Render(left) + " " + m.Styles.Dim.Render(fitted)
}
