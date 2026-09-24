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
	Styles  Styles
	Width   int
	// Height is the rows the list may use. Zero draws every row.
	Height int
	Now    func() time.Time
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
		if !ok || row.State != "" || m.Engine == "" {
			return m, nil
		}
		if m.Marked[row.Ref.URL()] {
			delete(m.Marked, row.Ref.URL())
		} else {
			m.Marked[row.Ref.URL()] = true
		}
	case "enter":
		if urls := m.markedURLs(); len(urls) > 0 {
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
	case "esc":
		return m, msg.Send(msg.Goto{Screen: msg.Dashboard})
	case "?":
		return m, msg.Send(msg.OpenHelp{})
	}
	return m, nil
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
	if m.Loading && len(m.Sections) == 0 {
		return m.Styles.Dim.Render("Searching GitHub for review requests…")
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
			// gh's error carries its stderr, which can run over several lines.
			reason := strings.Join(strings.Fields(s.Err.Error()), " ")
			lines = append(lines, m.Styles.Dim.Render(format.Truncate("  "+reason, format.Width(m.Width))))
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

	header := m.header()
	if m.Height > 1 {
		lines = window(lines, cursorLine, m.Height-1)
	}
	return header + "\n" + strings.TrimRight(strings.Join(lines, "\n"), "\n")
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
	if m.Loading {
		text += " · refreshing…"
	}
	return m.Styles.Dim.Render(text)
}

// window keeps at most height lines, placed so the cursor's line is visible.
func window(lines []string, cursor, height int) []string {
	if len(lines) <= height {
		return lines
	}
	start := min(max(0, cursor-height/2), len(lines)-height)
	return lines[start : start+height]
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
