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
	"github.com/haacked/docket/internal/tui/format"
	"github.com/haacked/docket/internal/tui/msg"
	"github.com/haacked/docket/internal/tui/screens/choice"
)

type Styles struct {
	Label    lipgloss.Style
	Dim      lipgloss.Style
	Selected lipgloss.Style
}

type Model struct {
	Record  review.Record
	Events  []string
	Event   string
	Body    textarea.Model
	Styles  Styles
	Busy    string
	Spinner format.Spinner
	// draft is the pending review's body as the text area holds it after SetDraft.
	draft string
}

// fieldWidth is the widest the body field grows, in columns.
const fieldWidth = 60

func New(styles Styles) Model {
	body := textarea.New()
	body.Placeholder = "Optional summary to post with the review"
	body.SetWidth(fieldWidth)
	body.SetHeight(4)
	return Model{Styles: styles, Body: body}
}

// SetWidth fits the body field to width, the room the page gives the screen, up
// to fieldWidth. A width of zero is unknown and leaves the field as it is.
func (m Model) SetWidth(width int) Model {
	if width > 0 {
		m.Body.SetWidth(min(fieldWidth, width))
	}
	return m
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
	m.draft = ""
	return m
}

// SetDraft fills the body with the pending review's body. It leaves alone a body
// the user has typed in, because the read of GitHub runs in a command and can
// land after the user starts typing.
func (m Model) SetDraft(body string) Model {
	if m.Body.Value() != "" {
		return m
	}
	// A body saved from the browser ends its lines in \r\n. The text area turns
	// each \r into a line break of its own, which would double every line.
	m.Body.SetValue(strings.ReplaceAll(body, "\r\n", "\n"))
	m.Body.MoveToBegin()
	m.draft = m.Body.Value()
	return m
}

// ClearBusy makes the screen ready to try again and keeps what the user typed. A
// screen left busy after a failed submit ignores the key that retries it, which
// looks like a hang.
func (m Model) ClearBusy() Model {
	m.Busy = ""
	m.Body.Focus()
	return m
}

func (m Model) Update(message tea.Msg) (Model, tea.Cmd) {
	if key, ok := message.(tea.KeyPressMsg); ok {
		// A change made after ctrl+s would never be sent. Every key but esc
		// therefore does nothing until the submit answers.
		if m.Busy != "" && key.String() != "esc" {
			return m, nil
		}
		switch key.String() {
		case "esc":
			return m, msg.Send(msg.Goto{Screen: msg.Dashboard})
		case "tab":
			m.Event = choice.Next(m.Events, m.Event)
			return m, nil
		// The body is a textarea, where enter is a newline. Submitting is its own
		// key so a multi-line summary stays possible.
		case "ctrl+s":
			if m.Event == "" {
				return m, nil
			}
			body := m.Body.Value()
			// The text area turns a tab into spaces and drops control
			// characters. An untouched draft therefore goes unsent. GitHub then
			// keeps the draft's own text.
			if body == m.draft {
				body = ""
			}
			m.Busy = "submitting"
			m.Body.Blur()
			return m, msg.Send(msg.SubmitReview{
				ID:       m.Record.ID,
				ReviewID: m.Record.ReviewID,
				Event:    m.Event,
				Body:     strings.TrimSpace(body),
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
	if m.draft != "" && strings.TrimSpace(m.Body.Value()) == "" {
		b.WriteString(m.Styles.Dim.Render("GitHub keeps the draft's summary when the body is empty.") + "\n")
	}

	if m.Busy != "" {
		b.WriteString("\n" + m.Spinner.Render(m.Busy) + "\n")
	}
	return b.String()
}
