// Package newreview takes a pull request reference and an engine choice. It
// parses what the user types as they type it, which needs nothing from the
// network.
package newreview

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/tui/msg"
	"github.com/haacked/docket/internal/tui/screens/choice"
)

type Styles struct {
	Label lipgloss.Style
	Dim   lipgloss.Style
	Err   lipgloss.Style
}

type Model struct {
	Input       textinput.Model
	Engine      string
	Engines     []string
	DefaultRepo string
	Styles      Styles
	Busy        string
}

func New(styles Styles, engines []string, engine, defaultRepo string) Model {
	input := textinput.New()
	input.Placeholder = "https://github.com/org/repo/pull/123"
	input.Prompt = "› "
	input.SetWidth(60)
	// A virtual cursor renders inside the field's own string, so the root does
	// not have to work out where on the screen the real cursor belongs.
	input.SetVirtualCursor(true)
	input.Focus()
	return Model{
		Input:       input,
		Engine:      engine,
		Engines:     engines,
		DefaultRepo: defaultRepo,
		Styles:      styles,
	}
}

// Reset clears the field, so leaving and returning does not carry a stale URL.
func (m Model) Reset() Model {
	m.Input.SetValue("")
	m.Busy = ""
	return m
}

// ClearBusy makes the screen ready to try again and keeps what the user typed. A
// screen left busy after a failure ignores enter, which looks like a hang.
func (m Model) ClearBusy() Model {
	m.Busy = ""
	return m
}

// SetValue prefills the field, which is how the URL argument arrives.
func (m Model) SetValue(value string) Model {
	m.Input.SetValue(value)
	m.Input.CursorEnd()
	return m
}

func (m Model) Update(message tea.Msg) (Model, tea.Cmd) {
	if key, ok := message.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "esc":
			return m, msg.Send(msg.Goto{Screen: msg.Dashboard})
		case "tab":
			m.Engine = choice.Next(m.Engines, m.Engine)
			return m, nil
		case "enter":
			if m.Busy != "" {
				return m, nil
			}
			value := strings.TrimSpace(m.Input.Value())
			if _, err := m.ref(); err != nil {
				return m, nil
			}
			m.Busy = "resolving"
			return m, msg.Send(msg.StartReview{Input: value, Engine: m.Engine})
		}
	}

	var cmd tea.Cmd
	m.Input, cmd = m.Input.Update(message)
	return m, cmd
}

func (m Model) ref() (pr.Ref, error) {
	return pr.ParseRef(m.Input.Value(), m.DefaultRepo)
}

func (m Model) View() string {
	var b strings.Builder
	b.WriteString(m.Styles.Label.Render("Pull request") + "\n")
	b.WriteString(m.Input.View() + "\n\n")

	if value := strings.TrimSpace(m.Input.Value()); value != "" {
		ref, err := m.ref()
		if err != nil {
			b.WriteString(m.Styles.Err.Render(err.Error()) + "\n")
		} else {
			b.WriteString(m.Styles.Dim.Render("Will review "+ref.String()+" at "+ref.URL()) + "\n")
		}
	} else {
		b.WriteString(m.Styles.Dim.Render("A URL, org/repo#123, or a bare number with a default repo set.") + "\n")
	}

	b.WriteString("\n" + m.Styles.Label.Render("Engine") + " " + choice.Line(m.Engines, m.Engine, lipgloss.Style{}, m.Styles.Dim) + "\n")
	if m.Busy != "" {
		b.WriteString("\n" + m.Styles.Dim.Render(m.Busy+"…") + "\n")
	}
	return b.String()
}
