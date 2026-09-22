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

type Model struct {
	// Return is the screen that was showing before help opened. Esc and ?
	// close back to it.
	Return msg.Screen
	Styles Styles
}

func New(styles Styles, from msg.Screen) Model {
	return Model{Styles: styles, Return: from}
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
	section := func(title string, entries [][2]string) {
		b.WriteString(m.Styles.Group.Render(title) + "\n")
		for _, e := range entries {
			b.WriteString(keyLine(m.Styles, e[0], e[1]))
		}
		b.WriteString("\n")
	}

	section("Dashboard", [][2]string{
		{"n", "start a new review"},
		{"enter", "resume or open the selected record"},
		{"s", "submit a drafted review"},
		{"v", "view the review notes"},
		{"x", "abandon the selected record"},
		{"r", "refresh the selected record from GitHub"},
		{"R", "refresh every record from GitHub"},
		{"a", "show or hide archived records"},
		{"j/k ↓/↑", "move the selection"},
		{"g/G", "jump to the top or bottom"},
		{"q", "quit"},
	})
	section("New review", [][2]string{
		{"enter", "start the review"},
		{"tab", "change the engine"},
		{"ctrl+b", "toggle background vs. this terminal"},
		{"esc", "back to the dashboard"},
	})
	section("Submit", [][2]string{
		{"tab", "change the event"},
		{"ctrl+s", "submit"},
		{"esc", "back to the dashboard"},
	})
	section("Notes", [][2]string{
		{"e", "edit in $EDITOR"},
		{"↑/↓", "scroll"},
		{"esc", "back to the dashboard"},
	})
	section("Everywhere", [][2]string{
		{"?", "toggle this help"},
		{"ctrl+c", "quit"},
	})

	return strings.TrimRight(b.String(), "\n")
}

// keyLine is one row: a key or chord, padded so the descriptions that follow
// it line up.
func keyLine(s Styles, key, desc string) string {
	return fmt.Sprintf("  %s %s\n", s.Label.Render(fmt.Sprintf("%-10s", key)), desc)
}
