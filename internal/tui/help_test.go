package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/haacked/docket/internal/tui/msg"
)

func TestOpenHelpShowsItOverWhateverScreenSentIt(t *testing.T) {
	next, _ := app().Update(msg.Goto{Screen: msg.Notes})
	a := next.(App)

	next, cmd := a.Update(msg.OpenHelp{})
	a = next.(App)

	if a.screen != msg.Help {
		t.Errorf("screen = %v, want msg.Help", a.screen)
	}
	if cmd != nil {
		t.Error("opening help ran a command")
	}
}

func TestHelpClosesBackToWhereItOpened(t *testing.T) {
	next, _ := app().Update(msg.Goto{Screen: msg.Notes})
	next, _ = next.(App).Update(msg.OpenHelp{})
	a := next.(App)

	next, cmd := a.Update(press("?"))
	if cmd == nil {
		t.Fatal("? on the help screen produced no command")
	}
	a = next.(App).apply(t, cmd)

	if a.screen != msg.Notes {
		t.Errorf("screen = %v, want back on msg.Notes", a.screen)
	}
}

func TestEscClosesHelpBackToWhereItOpened(t *testing.T) {
	next, _ := app().Update(msg.OpenHelp{})
	a := next.(App)
	if a.screen != msg.Help {
		t.Fatal("OpenHelp did not open help")
	}

	next, cmd := a.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("esc on the help screen produced no command")
	}
	a = next.(App).apply(t, cmd)

	if a.screen != msg.Dashboard {
		t.Errorf("screen = %v, want back on msg.Dashboard", a.screen)
	}
}

// apply runs cmd and feeds its message back into Update, the two-hop pattern
// every screen's intents go through (a key produces a msg.Goto or similar,
// which the root then acts on).
func (a App) apply(t *testing.T, cmd tea.Cmd) App {
	t.Helper()
	next, _ := a.Update(cmd())
	return next.(App)
}

// A key help does not recognize must not leak through to the screen
// underneath. Pressing n while help is open must not start a new review
// behind it.
func TestHelpSwallowsKeysTheDashboardWouldOtherwiseAct(t *testing.T) {
	next, _ := app().Update(msg.OpenHelp{})
	a := next.(App)

	next, cmd := a.Update(press("n"))
	a = next.(App)

	if a.screen != msg.Help {
		t.Errorf("screen = %v, want it to stay on msg.Help", a.screen)
	}
	if cmd != nil {
		t.Error("a key help does not recognize ran a command behind the overlay")
	}
}

func TestHelpScreenListsEveryScreensKeys(t *testing.T) {
	view := app().help.View()
	for _, want := range []string{"Dashboard", "New review", "Submit", "Notes", "ctrl+c", "quit"} {
		if !strings.Contains(view, want) {
			t.Errorf("help screen is missing %q", want)
		}
	}
}
