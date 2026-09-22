// Package notes shows the review review-code wrote. It holds no service and runs
// no commands: the root reads the file and hands the text down, and the key that
// opens an editor leaves as an intent.
package notes

import (
	"cmp"
	"fmt"
	"os"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/glamour/v2/styles"
	"charm.land/lipgloss/v2"

	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/tui/msg"
)

type Styles struct {
	Label lipgloss.Style
	Dim   lipgloss.Style
}

type Model struct {
	Record   review.Record
	Markdown string
	// Missing means review-code wrote no notes for this record yet, which is not
	// an error.
	Missing  bool
	Viewport viewport.Model
	Styles   Styles
	// renderedAt is the width the current content was wrapped to, so a resize
	// that does not change the width re-renders nothing. It is a record of what
	// was drawn, not a cached terminal size.
	renderedAt int
	dark       bool
}

// New starts the pane on the dark palette. A terminal that never answers Bubble
// Tea's background-color request keeps it, which is the assumption a terminal
// tool is safest making.
func New(styles Styles) Model {
	return Model{Styles: styles, Viewport: viewport.New(), dark: true}
}

// SetDark picks the palette the notes render in, from the background color the
// terminal reported.
func (m Model) SetDark(dark bool) Model {
	if dark == m.dark {
		return m
	}
	m.dark = dark
	return m.render()
}

// SetNotes aims the screen at one record's notes. missing says review-code has
// not written the file, which the pane reports rather than treating as an error.
func (m Model) SetNotes(rec review.Record, markdown string, missing bool) Model {
	m.Record = rec
	m.Markdown = markdown
	m.Missing = missing
	m.Viewport.GotoTop()
	return m.render()
}

// SetSize refits the pane. A width change re-renders, because glamour wraps to a
// fixed width and moving the viewport alone would leave the old line breaks
// behind. A height change does not: the same lines are simply clipped
// differently, and re-wrapping a long review costs tens of milliseconds.
func (m Model) SetSize(width, height int) Model {
	m.Viewport.SetWidth(width)
	m.Viewport.SetHeight(max(height, 1))
	if width == m.renderedAt {
		return m
	}
	return m.render()
}

// render turns the markdown into styled text. A renderer that fails falls back
// to the raw markdown, because unstyled notes still read.
func (m Model) render() Model {
	m.renderedAt = m.Viewport.Width()
	switch {
	case m.Missing:
		m.Viewport.SetContent(m.Styles.Dim.Render("No notes at " + m.Record.NotesPath))
	case m.Markdown == "":
		// The root aims the pane at a record before the file is read, so this is
		// the frame between the keypress and the load. Nothing to wrap yet.
		m.Viewport.SetContent("")
	default:
		m.Viewport.SetContent(renderMarkdown(m.Markdown, m.Viewport.Width(), m.dark))
	}
	return m
}

func renderMarkdown(markdown string, width int, dark bool) string {
	if width <= 0 {
		return markdown
	}

	// GLAMOUR_STYLE wins, so the notes follow whatever the user already set for
	// every other glamour-rendered tool. glamour v2 has no style that follows the
	// terminal, and its own fallback is dark whatever the terminal is, so docket
	// picks from the background color Bubble Tea reported instead.
	style := glamour.WithStandardStyle(styles.LightStyle)
	switch {
	case os.Getenv("GLAMOUR_STYLE") != "":
		style = glamour.WithEnvironmentConfig()
	case dark:
		style = glamour.WithStandardStyle(styles.DarkStyle)
	}

	renderer, err := glamour.NewTermRenderer(style, glamour.WithWordWrap(width))
	if err != nil {
		return markdown
	}
	out, err := renderer.Render(markdown)
	if err != nil {
		return markdown
	}
	return out
}

func (m Model) Update(message tea.Msg) (Model, tea.Cmd) {
	if key, ok := message.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "esc", "q":
			return m, msg.Send(msg.Goto{Screen: msg.Dashboard})
		case "e":
			return m, msg.Send(msg.EditNotes{ID: m.Record.ID})
		case "?":
			return m, msg.Send(msg.OpenHelp{})
		}
	}

	var cmd tea.Cmd
	m.Viewport, cmd = m.Viewport.Update(message)
	return m, cmd
}

func (m Model) View() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s  %s\n", m.Styles.Label.Render("Notes"), m.Record.Ref, cmp.Or(m.Record.Title, m.Record.URL))
	b.WriteString(m.Styles.Dim.Render(m.Record.NotesPath) + "\n\n")
	b.WriteString(m.Viewport.View())
	return b.String()
}
