// Package help shows the full key reference across every screen. Like every
// screen it holds no service and runs no commands, so its Update is testable
// with synthetic key messages.
package help

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/haacked/docket/internal/tui/msg"
)

type Styles struct {
	Group lipgloss.Style
	Label lipgloss.Style
}

// Entry is one key, shared by the footer (keymap.go's helpFor) and the full
// help screen (View, below), so the two cannot drift the way they did
// before this table existed. Short is the footer label, and an empty Short
// leaves the key out of the footer. Dashboard uses that for its movement
// keys, which have no room there. It also uses that for its archived
// toggle, whose label depends on state and is built by helpFor instead.
type Entry struct {
	Key   string
	Short string
	Long  string
}

// Dashboard, NewReview, Submit, Notes, and Requests are the per-screen key tables.
// NewReview and Submit have no entry for ?. Each holds a free-text field, so
// neither binds it.
var (
	Dashboard = []Entry{
		{Key: "n", Short: "new", Long: "start a new review"},
		{Key: "i", Short: "requests", Long: "list the pull requests waiting on your review"},
		{Key: "enter", Short: "resume", Long: "resume or open the selected record"},
		{Key: "s", Short: "submit", Long: "submit a drafted review"},
		{Key: "v", Short: "notes", Long: "view the review notes"},
		{Key: "c", Short: "ask", Long: "ask questions about the review notes"},
		{Key: "u", Short: "re-review", Long: "review again, appending to or overwriting the review"},
		{Key: "x", Short: "abandon", Long: "abandon the selected record"},
		{Key: "r", Short: "refresh", Long: "refresh the selected record from GitHub"},
		{Key: "R", Short: "refresh all", Long: "refresh every record from GitHub"},
		{Key: "a", Long: "show or hide archived records"},
		{Key: "j/k ↓/↑", Long: "move the selection"},
		{Key: "g/G", Long: "jump to the top or bottom"},
		{Key: "?", Short: "help", Long: "toggle this help"},
		{Key: "q", Short: "quit", Long: "quit"},
	}
	NewReview = []Entry{
		{Key: "enter", Short: "start", Long: "start the review"},
		{Key: "tab", Short: "engine", Long: "change the engine"},
		{Key: "ctrl+b", Short: "background", Long: "toggle background vs. this terminal"},
		{Key: "v/a/o", Long: "when a review exists: view and ask, append, or overwrite"},
		{Key: "esc", Short: "back", Long: "back to the dashboard"},
	}
	Submit = []Entry{
		{Key: "ctrl+s", Short: "submit", Long: "submit"},
		{Key: "tab", Short: "event", Long: "change the event"},
		{Key: "esc", Short: "back", Long: "back to the dashboard"},
	}
	Notes = []Entry{
		{Key: "e", Short: "edit", Long: "edit in $EDITOR"},
		{Key: "↑/↓", Short: "scroll", Long: "scroll"},
		{Key: "?", Short: "help", Long: "toggle this help"},
		{Key: "esc/q", Short: "back", Long: "back to the dashboard"},
	}
	Requests = []Entry{
		{Key: "space", Short: "mark", Long: "mark or unmark the selected pull request"},
		{Key: "enter", Short: "start", Long: "start the marked pull requests as background reviews, or open the selected pull request, or its review when one is already open"},
		{Key: "r", Short: "refresh", Long: "search GitHub again"},
		{Key: "j/k ↓/↑", Long: "move the selection"},
		{Key: "g/G", Long: "jump to the top or bottom"},
		{Key: "?", Short: "help", Long: "toggle this help"},
		{Key: "esc", Short: "back", Long: "back to the dashboard"},
	}
	everywhere = []Entry{{Key: "ctrl+c", Long: "quit"}}
)

// Footer returns each entry's footer label, in table order, skipping any
// entry whose Short is empty.
func Footer(entries []Entry) []string {
	var out []string
	for _, e := range entries {
		if e.Short == "" {
			continue
		}
		out = append(out, e.Key+" "+e.Short)
	}
	return out
}

type Model struct {
	// Return is the screen that was showing before help opened. Esc and ?
	// close back to it.
	Return   msg.Screen
	Viewport viewport.Model
	Styles   Styles
}

// New builds the pane and sets its content immediately, rather than on every
// View. The reference text depends only on Styles, fixed for the Model's
// life, so there is nothing later that would need to change it. Setting it
// once here, rather than from View, is also what makes Update's scrolling
// work. Update persists the Viewport it returns. A View that set content on
// its own value-receiver copy would never reach that persisted one.
func New(styles Styles) Model {
	m := Model{Styles: styles, Viewport: viewport.New()}
	m.Viewport.SetContent(content(styles))
	return m
}

// For records the screen to return to when help closes.
func (m Model) For(from msg.Screen) Model {
	m.Return = from
	return m
}

// SetSize refits the pane. The reference text is long enough to overflow an
// ordinary terminal on its own. The viewport is what makes the rest of it
// reachable by scrolling, instead of silently cut off.
func (m Model) SetSize(width, height int) Model {
	m.Viewport.SetWidth(width)
	m.Viewport.SetHeight(max(height, 1))
	return m
}

// Update closes on esc or ? and hands every other key to the viewport. The
// viewport only reacts to its own scroll keys and leaves the rest as a
// no-op, which keeps a key the screen underneath would act on, such as n
// for a new review, from leaking through while help is on top.
func (m Model) Update(message tea.Msg) (Model, tea.Cmd) {
	if key, ok := message.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "esc", "?":
			return m, msg.Send(msg.Goto{Screen: m.Return})
		}
	}

	var cmd tea.Cmd
	m.Viewport, cmd = m.Viewport.Update(message)
	return m, cmd
}

func (m Model) View() string {
	return m.Viewport.View()
}

// content is the full reference text, built once by New and never changed
// afterward.
func content(styles Styles) string {
	var b strings.Builder
	section := func(title string, entries []Entry) {
		b.WriteString(styles.Group.Render(title) + "\n")
		for _, e := range entries {
			b.WriteString(keyLine(styles, e.Key, e.Long))
		}
		b.WriteString("\n")
	}

	section("Dashboard", Dashboard)
	section("New review", NewReview)
	section("Submit", Submit)
	section("Notes", Notes)
	section("Review requests", Requests)
	section("Everywhere", everywhere)

	return strings.TrimRight(b.String(), "\n")
}

// keyLine is one row: a key or chord, padded so the descriptions that follow
// it line up.
func keyLine(s Styles, key, desc string) string {
	return fmt.Sprintf("  %s %s\n", s.Label.Render(fmt.Sprintf("%-10s", key)), desc)
}
