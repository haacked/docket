// Package help shows the full key reference across every screen. Like every
// screen it holds no service and runs no commands, so its Update is testable
// with synthetic key messages.
package help

import (
	"fmt"
	"strings"

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

// Dashboard, NewReview, Submit, and Notes are the per-screen key tables.
// NewReview and Submit have no entry for ?. Each holds a free-text field, so
// neither binds it.
var (
	Dashboard = []Entry{
		{Key: "n", Short: "new", Long: "start a new review"},
		{Key: "enter", Short: "resume", Long: "resume or open the selected record"},
		{Key: "s", Short: "submit", Long: "submit a drafted review"},
		{Key: "v", Short: "notes", Long: "view the review notes"},
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
	Return msg.Screen
	Styles Styles
}

func New(styles Styles) Model {
	return Model{Styles: styles}
}

// For aims a close back at whatever screen opened this one.
func (m Model) For(from msg.Screen) Model {
	m.Return = from
	return m
}

// Update closes on esc or ? and swallows every other key. That keeps a key
// the screen underneath would act on, such as n for a new review, from
// leaking through while help is on top.
func (m Model) Update(message tea.Msg) (Model, tea.Cmd) {
	if key, ok := message.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "esc", "?":
			return m, msg.Send(msg.Goto{Screen: m.Return})
		}
		return m, nil
	}
	return m, nil
}

func (m Model) View() string {
	var b strings.Builder
	section := func(title string, entries []Entry) {
		b.WriteString(m.Styles.Group.Render(title) + "\n")
		for _, e := range entries {
			b.WriteString(keyLine(m.Styles, e.Key, e.Long))
		}
		b.WriteString("\n")
	}

	section("Dashboard", Dashboard)
	section("New review", NewReview)
	section("Submit", Submit)
	section("Notes", Notes)
	section("Everywhere", everywhere)

	return strings.TrimRight(b.String(), "\n")
}

// keyLine is one row: a key or chord, padded so the descriptions that follow
// it line up.
func keyLine(s Styles, key, desc string) string {
	return fmt.Sprintf("  %s %s\n", s.Label.Render(fmt.Sprintf("%-10s", key)), desc)
}
