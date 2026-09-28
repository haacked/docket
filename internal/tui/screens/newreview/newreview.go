// Package newreview takes a pull request reference and an engine choice. It
// parses what the user types as they type it, which needs nothing from the
// network.
package newreview

import (
	"slices"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/tui/format"
	"github.com/haacked/docket/internal/tui/msg"
	"github.com/haacked/docket/internal/tui/screens/choice"
)

type Styles struct {
	Label lipgloss.Style
	Dim   lipgloss.Style
	Err   lipgloss.Style
}

type Model struct {
	Input   textinput.Model
	Engine  string
	Engines []string
	// BackgroundEngines are the engines that can run a review without the
	// terminal. It is a list of names rather than anything richer because a
	// screen holds no engine: it turns keys into intents and nothing else.
	BackgroundEngines []string
	// Background is the user's choice. An engine with no background mode runs
	// in the terminal and leaves it set. A move back to an engine that has a
	// background mode runs there again.
	Background  bool
	DefaultRepo string
	Styles      Styles
	Busy        string
	Spinner     format.Spinner
	// Existing holds what was found while the screen asks what to do with a
	// review that is already there. It is nil while the screen takes a pull
	// request.
	Existing *Existing
}

// Existing is what the root found before reviewing a pull request. The screen
// holds plain values rather than the service's own type, because a screen holds
// no service.
type Existing struct {
	Ref    string
	Engine string
	// RecordID names a record already on the dashboard, which offers only a
	// re-review. It is empty for a typed pull request.
	RecordID string
	review.Found
}

// CanAsk reports whether view and ask is on offer. It needs notes to ask about.
// A record already on the dashboard has its own key for it.
func (e Existing) CanAsk() bool {
	return e.RecordID == "" && !e.NotesAt.IsZero()
}

// fieldWidth is the widest the pull request field grows, in columns of text.
const fieldWidth = 60

// SetWidth fits the pull request field to width, the room the page gives the
// screen, up to fieldWidth. The prompt and one column for the cursor sit beside
// the text. A width of zero is unknown and leaves the field as it is.
func (m Model) SetWidth(width int) Model {
	if width <= 0 {
		return m
	}
	beside := format.Columns(m.Input.Prompt) + 1
	if w := max(min(fieldWidth, width-beside), 1); w != m.Input.Width() {
		pos := m.Input.Position()
		m.Input.SetWidth(w)
		// The field picks a new slice of the value only when the cursor moves
		// outside the slice it shows. Moving to the end and back makes it pick
		// the slice for the new width wherever the cursor was.
		m.Input.CursorEnd()
		m.Input.SetCursor(pos)
	}
	return m
}

func New(styles Styles, engines, backgroundEngines []string, engine, defaultRepo string, background bool) Model {
	input := textinput.New()
	input.Placeholder = "https://github.com/org/repo/pull/123"
	input.Prompt = "› "
	input.SetWidth(fieldWidth)
	// A virtual cursor renders inside the field's own string, so the root does
	// not have to work out where on the screen the real cursor belongs.
	input.SetVirtualCursor(true)
	input.Focus()
	return Model{
		Input:             input,
		Engine:            engine,
		Engines:           engines,
		BackgroundEngines: backgroundEngines,
		Background:        background,
		DefaultRepo:       defaultRepo,
		Styles:            styles,
	}
}

// CanBackground reports whether the chosen engine runs a review without the
// terminal. The toggle is refused for one that cannot, rather than failing after
// the pull request has already been resolved and cloned.
func (m Model) CanBackground() bool {
	return slices.Contains(m.BackgroundEngines, m.engine())
}

// background reports whether the review would run in the background.
func (m Model) background() bool {
	return m.Background && m.CanBackground()
}

// engine is the engine the review would run under. A re-review keeps the one
// its record was started with.
func (m Model) engine() string {
	if m.Existing != nil && m.Existing.Engine != "" {
		return m.Existing.Engine
	}
	return m.Engine
}

// SetExisting switches the screen to asking what to do with the review found.
func (m Model) SetExisting(found Existing) Model {
	m.Existing = &found
	m.Busy = ""
	return m
}

// Reset clears the field, so leaving and returning does not carry a stale URL.
// The background choice is left alone: a user who runs reviews in the background
// runs the next one there too.
func (m Model) Reset() Model {
	m.Input.SetValue("")
	m.Busy = ""
	m.Existing = nil
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
	// A change made after enter would not reach the review that starts. Every
	// key but esc, and every paste, therefore does nothing until the root
	// answers.
	if m.Busy != "" {
		switch message := message.(type) {
		case tea.PasteMsg:
			return m, nil
		case tea.KeyPressMsg:
			if message.String() != "esc" {
				return m, nil
			}
		}
	}
	if m.Existing != nil {
		if key, ok := message.(tea.KeyPressMsg); ok {
			return m.choose(key)
		}
		return m, nil
	}
	if key, ok := message.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "esc":
			return m, msg.Send(msg.Goto{Screen: msg.Dashboard})
		case "tab":
			m.Engine = choice.Next(m.Engines, m.Engine)
			return m, nil
		case "ctrl+b":
			if m.CanBackground() {
				m.Background = !m.Background
			}
			return m, nil
		case "enter":
			value := strings.TrimSpace(m.Input.Value())
			if _, err := m.ref(); err != nil {
				return m, nil
			}
			m.Busy = "resolving"
			return m, msg.Send(msg.StartReview{Input: value, Engine: m.Engine, Background: m.background()})
		}
	}

	var cmd tea.Cmd
	m.Input, cmd = m.Input.Update(message)
	return m, cmd
}

// choose handles the keys of the step that asks what to do with an existing
// review. The text field is not on screen, so no key reaches it.
func (m Model) choose(key tea.KeyPressMsg) (Model, tea.Cmd) {
	found := *m.Existing
	var intent review.Intent
	switch key.String() {
	case "esc":
		if found.RecordID != "" {
			return m, msg.Send(msg.Goto{Screen: msg.Dashboard})
		}
		m.Existing = nil
		return m, nil
	case "ctrl+b":
		if m.CanBackground() {
			m.Background = !m.Background
		}
		return m, nil
	case "v":
		if !found.CanAsk() {
			return m, nil
		}
		intent = review.IntentAsk
	case "a":
		intent = review.IntentAppend
	case "o":
		intent = review.IntentOverwrite
	default:
		return m, nil
	}

	m.Busy = "preparing"
	return m, msg.Send(msg.StartReview{
		Input:  strings.TrimSpace(m.Input.Value()),
		Engine: m.engine(),
		// A question-and-answer session needs the terminal. There is no draft for
		// a background one to finish.
		Background: m.background() && intent != review.IntentAsk,
		Intent:     string(intent),
		RecordID:   found.RecordID,
	})
}

func (m Model) ref() (pr.Ref, error) {
	return pr.ParseRef(m.Input.Value(), m.DefaultRepo)
}

func (m Model) View() string {
	if m.Existing != nil {
		return m.existingView(*m.Existing)
	}
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
	b.WriteString(m.Styles.Label.Render("Run") + " " + m.runLine() + "\n")
	if m.Busy != "" {
		b.WriteString("\n" + m.Spinner.Render(m.Busy) + "\n")
	}
	return b.String()
}

func (m Model) existingView(found Existing) string {
	var b strings.Builder
	title := "Existing review of " + found.Ref
	if found.RecordID != "" {
		title = "Review " + found.Ref + " again"
	}
	b.WriteString(m.Styles.Label.Render(title) + "\n")
	if summary := format.Existing(found.Found); summary != "" {
		b.WriteString(m.Styles.Dim.Render(summary) + "\n")
	}

	choices := []string{"a append", "o overwrite", "esc back"}
	if found.CanAsk() {
		choices = slices.Insert(choices, 0, "v view and ask")
	}
	b.WriteString("\n" + strings.Join(choices, " · ") + "\n")
	b.WriteString("\n" + m.Styles.Label.Render("Engine") + " " + m.engine() + "\n")
	b.WriteString(m.Styles.Label.Render("Run") + " " + m.runLine() + "\n")
	if found.CanAsk() && m.background() {
		b.WriteString(m.Styles.Dim.Render("view and ask runs in this terminal") + "\n")
	}
	if m.Busy != "" {
		b.WriteString("\n" + m.Spinner.Render(m.Busy) + "\n")
	}
	return b.String()
}

// runLine says where the review will run, and why the choice is not on offer
// when the engine has no background mode.
func (m Model) runLine() string {
	if !m.CanBackground() {
		return m.Styles.Dim.Render("in this terminal · " + m.engine() + " has no background mode")
	}
	if m.Background {
		return "in the background" + " " + m.Styles.Dim.Render("· ctrl+b for this terminal")
	}
	return "in this terminal" + " " + m.Styles.Dim.Render("· ctrl+b for the background")
}
