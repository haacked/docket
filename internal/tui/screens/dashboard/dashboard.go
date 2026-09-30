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
	"github.com/haacked/docket/internal/tui/format"
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
	{Title: "Did not start", States: []review.State{review.StateNotStarted}},
	{Title: "Drafted", States: []review.State{review.StateDrafted}},
	{Title: "Reviewed", States: []review.State{review.StateReviewed}},
	// A submitted record is one whose archiving did not finish. Listing it keeps
	// it selectable, so a refresh retries the archive.
	{Title: "Submitted", States: []review.State{review.StateSubmitted}},
	// Earlier reviews of mine may still be on GitHub. The title therefore does not
	// say "unreviewed".
	{Title: "No review posted", States: []review.State{review.StateUnreviewed}},
	{Title: "Archived", States: []review.State{review.StateArchived, review.StateAbandoned}, Closed: true},
}

type Model struct {
	Records      []review.Record
	Cursor       int
	ShowArchived bool
	Busy         map[string]string
	Spinner      format.Spinner
	// Background is what each running background session is doing, keyed by
	// record. The root fills it in, because a screen holds no engine to ask.
	Background map[string]review.Progress
	Styles     Styles
	Width      int
	Now        func() time.Time
}

// quietAfter is how long a session may go without the agent updating its
// account before the row says so. A review's agents can run for several
// minutes with no update, so a shorter wait would flag healthy sessions.
const quietAfter = 10 * time.Minute

func New(styles Styles) Model {
	return Model{Styles: styles, Busy: map[string]string{}, Background: map[string]review.Progress{}}
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
	case "i":
		return m, msg.Send(msg.OpenRequests{})
	case "enter":
		if rec, ok := m.Selected(); ok {
			// An adopted record has no review session to resume, so enter offers
			// to review it again instead.
			if rec.Adopted() {
				return m, msg.Send(msg.OpenRereview{ID: rec.ID})
			}
			return m, msg.Send(msg.Resume{ID: rec.ID})
		}
	case "c":
		if rec, ok := m.Selected(); ok {
			return m, msg.Send(msg.Ask{ID: rec.ID})
		}
	case "u":
		if rec, ok := m.Selected(); ok {
			return m, msg.Send(msg.OpenRereview{ID: rec.ID})
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
	case "o":
		if rec, ok := m.Selected(); ok {
			return m, msg.Send(msg.OpenOnGitHub{ID: rec.ID})
		}
	case "r":
		if rec, ok := m.Selected(); ok {
			return m, msg.Send(msg.RefreshRecords{ID: rec.ID})
		}
	case "R":
		return m, msg.Send(msg.RefreshRecords{})
	case "?":
		return m, msg.Send(msg.OpenHelp{})
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

	head := fmt.Sprintf("%s%s  ", marker, rec.Ref)
	left, meta := format.Row(head, cmp.Or(rec.Title, rec.URL), m.meta(rec), m.width())
	rendered := style.Render(left)
	if meta != "" {
		rendered += " " + meta
	}

	if line, waiting := m.activity(rec); line != "" {
		lineStyle := m.Styles.Dim
		if waiting {
			lineStyle = m.Styles.Err
		}
		rendered += "\n" + lineStyle.Render(format.Truncate("    "+line, m.width()))
	}
	if rec.Err != "" {
		rendered += "\n" + m.Styles.Err.Render(format.Truncate("    "+rec.Err, m.width()))
	}
	return rendered
}

// meta is the row's metadata, dim, led by any busy text in the busy style. A
// narrow terminal cuts the metadata from the end, so the busy text goes first.
// format.Row measures and cuts styled text by its columns.
func (m Model) meta(rec review.Record) string {
	parts := []string{rec.Author, rec.Engine, rec.Tier.String(), m.age(rec)}
	if rec.PRState.Closed() {
		parts = append(parts, rec.PRState.Label())
	}
	if rec.Mode == review.ModeBackground {
		parts = append(parts, "background")
	}
	meta := m.Styles.Dim.Render("· " + strings.Join(parts, " · "))
	note, busy := m.Busy[rec.ID]
	if !busy {
		return meta
	}
	return m.Styles.Dim.Render("· ") + m.Spinner.Render(note) + " " + meta
}

// activity is the line under a running background row that says what its
// session is doing, and whether the session is waiting for the user. It is
// empty for any other row, and for a session the last poll said nothing about.
func (m Model) activity(rec review.Record) (string, bool) {
	act, ok := m.Background[rec.ID]
	if !ok || !rec.BackgroundRunning() {
		return "", false
	}
	if act.Needs != "" {
		parts := []string{"waiting for you: " + act.Needs, "enter opens it"}
		if act.Detail != "" {
			parts = append(parts, act.Detail)
		}
		return strings.Join(parts, " · "), true
	}

	var parts []string
	if act.Detail != "" {
		parts = append(parts, act.Detail)
	}
	if act.Agents > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", act.Agents, format.Plural(act.Agents, "agent")))
	}
	if quiet := m.now().Sub(act.UpdatedAt); !act.UpdatedAt.IsZero() && quiet >= quietAfter {
		parts = append(parts, "no update for "+format.Duration(quiet))
	}
	return strings.Join(parts, " · "), false
}

func (m Model) age(rec review.Record) string {
	if rec.StartedAt.IsZero() {
		return "not started"
	}
	return format.Ago(m.now().Sub(rec.StartedAt))
}

func (m Model) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m Model) width() int { return format.Width(m.Width) }
