// Package dashboard lists docket's records and turns keys into intents. It holds
// no service and runs no commands, so its Update is testable with synthetic key
// messages.
package dashboard

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/tui/msg"
)

// Styles are the lipgloss styles the root hands down.
type Styles struct {
	Group    lipgloss.Style
	Row      lipgloss.Style
	Selected lipgloss.Style
	Dim      lipgloss.Style
	Err      lipgloss.Style
}

// groups is the order states appear in. Archived and abandoned records are in
// the last group, which stays hidden until the user asks for it.
var groups = []struct {
	Title  string
	States []review.State
	Closed bool
}{
	{Title: "Reviewing", States: []review.State{review.StateReviewing, review.StatePreparing}},
	{Title: "Drafted", States: []review.State{review.StateDrafted}},
	// A submitted record is one whose archiving did not finish. Listing it keeps
	// it selectable, so a refresh retries the archive.
	{Title: "Submitted", States: []review.State{review.StateSubmitted}},
	{Title: "Unreviewed", States: []review.State{review.StateUnreviewed}},
	{Title: "Archived", States: []review.State{review.StateArchived, review.StateAbandoned}, Closed: true},
}

type Model struct {
	Records      []review.Record
	Cursor       int
	ShowArchived bool
	Busy         map[string]string
	Styles       Styles
	Width        int
	Now          func() time.Time
}

func New(styles Styles) Model {
	return Model{Styles: styles, Busy: map[string]string{}}
}

// SetRecords replaces the list and keeps the cursor on the record it was on. The
// cursor ranges over the visible rows, which is fewer than the records whenever
// archived ones are hidden, and detection reorders those rows by moving a record
// between groups. Following the record rather than the position is what stops a
// background refresh from sliding a different review under an unconfirmed x.
func (m Model) SetRecords(records []review.Record) Model {
	selected, had := m.Selected()
	m.Records = records
	if had {
		if i := slices.IndexFunc(m.rows(), func(r review.Record) bool { return r.ID == selected.ID }); i >= 0 {
			m.Cursor = i
			return m
		}
	}
	return m.clampCursor()
}

func (m Model) clampCursor() Model {
	m.Cursor = min(max(m.Cursor, 0), max(0, len(m.rows())-1))
	return m
}

// Selected is the record under the cursor.
func (m Model) Selected() (review.Record, bool) {
	rows := m.rows()
	if m.Cursor < 0 || m.Cursor >= len(rows) {
		return review.Record{}, false
	}
	return rows[m.Cursor], true
}

func (m Model) Update(message tea.Msg) (Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.KeyPressMsg:
		return m.handleKey(message)
	}
	return m, nil
}

func (m Model) handleKey(key tea.KeyPressMsg) (Model, tea.Cmd) {
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
	case "a":
		m.ShowArchived = !m.ShowArchived
		m = m.clampCursor()
	case "n":
		return m, msg.Send(msg.Goto{Screen: msg.NewReview})
	case "enter":
		if rec, ok := m.Selected(); ok {
			return m, msg.Send(msg.Resume{ID: rec.ID})
		}
	case "x":
		if rec, ok := m.Selected(); ok {
			return m, msg.Send(msg.Abandon{ID: rec.ID})
		}
	case "s":
		if rec, ok := m.Selected(); ok {
			return m, msg.Send(msg.OpenSubmit{ID: rec.ID})
		}
	case "v":
		if rec, ok := m.Selected(); ok {
			return m, msg.Send(msg.OpenNotes{ID: rec.ID})
		}
	case "r":
		if rec, ok := m.Selected(); ok {
			return m, msg.Send(msg.RefreshRecords{ID: rec.ID})
		}
	case "R":
		return m, msg.Send(msg.RefreshRecords{})
	}
	return m, nil
}

// rows is the records the cursor moves over, in the order they are drawn.
func (m Model) rows() []review.Record {
	var rows []review.Record
	for _, group := range groups {
		if group.Closed && !m.ShowArchived {
			continue
		}
		rows = append(rows, m.inGroup(group.States)...)
	}
	return rows
}

func (m Model) inGroup(states []review.State) []review.Record {
	var out []review.Record
	for _, rec := range m.Records {
		if slices.Contains(states, rec.State) {
			out = append(out, rec)
		}
	}
	return out
}

func (m Model) View() string {
	if len(m.Records) == 0 {
		return m.Styles.Dim.Render("No reviews yet. Press n to start one.")
	}

	var b strings.Builder
	index := 0
	for _, group := range groups {
		if group.Closed && !m.ShowArchived {
			continue
		}
		records := m.inGroup(group.States)
		if len(records) == 0 {
			continue
		}
		fmt.Fprintf(&b, "%s\n", m.Styles.Group.Render(fmt.Sprintf("%s (%d)", group.Title, len(records))))
		for _, rec := range records {
			b.WriteString(m.row(rec, index == m.Cursor))
			b.WriteByte('\n')
			index++
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m Model) row(rec review.Record, selected bool) string {
	marker := "  "
	style := m.Styles.Row
	if selected {
		marker = "> "
		style = m.Styles.Selected
	}

	title := cmp.Or(rec.Title, rec.URL)
	line := fmt.Sprintf("%s%s  %s", marker, rec.Ref, title)

	meta := []string{rec.Engine, rec.Tier.String(), m.age(rec)}
	if note, busy := m.Busy[rec.ID]; busy {
		meta = append(meta, note)
	}
	rendered := style.Render(truncate(line, m.titleWidth())) + " " + m.Styles.Dim.Render("· "+strings.Join(meta, " · "))
	if rec.Err != "" {
		rendered += "\n" + m.Styles.Err.Render("    "+truncate(rec.Err, m.width()-4))
	}
	return rendered
}

func (m Model) age(rec review.Record) string {
	if rec.StartedAt.IsZero() {
		return "not started"
	}
	now := time.Now()
	if m.Now != nil {
		now = m.Now()
	}
	d := now.Sub(rec.StartedAt)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func (m Model) width() int {
	if m.Width <= 0 {
		return 80
	}
	return m.Width
}

func (m Model) titleWidth() int { return max(20, m.width()-34) }

// truncate counts runes, not bytes. width is a column budget, and a pull request
// title carrying an accent or an emoji otherwise gets cut inside a character.
func truncate(s string, width int) string {
	if width <= 1 {
		return s
	}
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	return string(r[:width-1]) + "…"
}
