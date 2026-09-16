// Package submit collects the event and the body for a pending review and sends
// the intent up. Like every screen it holds no service and runs no commands, so
// its Update is testable with synthetic key messages.
package submit

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/tui/msg"
	"github.com/haacked/docket/internal/tui/screens/choice"
)

type Styles struct {
	Label    lipgloss.Style
	Dim      lipgloss.Style
	Selected lipgloss.Style
}

type Model struct {
	Record review.Record
	Events []string
	Event  string
	Body   textarea.Model
	Styles Styles
	Busy   string
}

func New(styles Styles) Model {
	body := textarea.New()
	body.Placeholder = "Optional summary to post with the review"
	body.SetWidth(60)
	body.SetHeight(4)
	return Model{Styles: styles, Body: body}
}

// For aims the screen at a record. The events come from the caller because
// approving your own pull request is a 422 from GitHub, so that choice is left
// out rather than offered and refused.
func (m Model) For(rec review.Record, events []string) Model {
	m.Record = rec
	m.Events = events
	m.Event = ""
	if len(events) > 0 {
		m.Event = events[0]
	}
	m.Body.Reset()
	m.Body.Focus()
	m.Busy = ""
	return m
}

// ClearBusy makes the screen ready to try again and keeps what the user typed. A
// screen left busy after a failed submit ignores the key that retries it, which
// looks like a hang.
func (m Model) ClearBusy() Model {
	m.Busy = ""
	return m
}

func (m Model) Update(message tea.Msg) (Model, tea.Cmd) {
	if key, ok := message.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "esc":
			return m, msg.Send(msg.Goto{Screen: msg.Dashboard})
		case "tab":
			m.Event = choice.Next(m.Events, m.Event)
			return m, nil
		// The body is a textarea, where enter is a newline. Submitting is its own
		// key so a multi-line summary stays possible.
		case "ctrl+s":
			if m.Busy != "" || m.Event == "" {
				return m, nil
			}
			m.Busy = "submitting"
			return m, msg.Send(msg.SubmitReview{
				ID:    m.Record.ID,
				Event: m.Event,
				Body:  strings.TrimSpace(m.Body.Value()),
			})
		}
	}

	var cmd tea.Cmd
	m.Body, cmd = m.Body.Update(message)
	return m, cmd
}

func (m Model) View() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s  %s\n\n", m.Styles.Label.Render("Submit"), m.Record.Ref, cmp.Or(m.Record.Title, m.Record.URL))

	b.WriteString(m.Styles.Label.Render("Event") + " " + choice.Line(m.Events, m.Event, m.Styles.Selected, m.Styles.Dim) + "\n")
	if !slices.Contains(m.Events, review.EventApprove) {
		b.WriteString(m.Styles.Dim.Render("GitHub refuses an approval of your own pull request, so approve is not offered.") + "\n")
	}

	b.WriteString("\n" + m.Styles.Label.Render("Body") + "\n")
	b.WriteString(m.Body.View() + "\n")

	if m.Busy != "" {
		b.WriteString("\n" + m.Styles.Dim.Render(m.Busy+"…") + "\n")
	}
	return b.String()
}
