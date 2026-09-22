package help

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/haacked/docket/internal/tui/msg"
)

func key(s string) tea.KeyPressMsg { return tea.KeyPressMsg{Code: rune(s[0]), Text: s} }

// sized is the screen with room to show every line without the viewport
// clipping any of it, for tests that check what the full text contains
// rather than how the viewport scrolls.
func sized() Model { return New(Styles{}).SetSize(100, 50) }

func TestForSetsWhereEscAndQuestionMarkReturnTo(t *testing.T) {
	m := New(Styles{}).For(msg.Submit)

	if m.Return != msg.Submit {
		t.Errorf("Return = %v, want msg.Submit", m.Return)
	}
}

func TestEscAndQuestionMarkCloseToReturn(t *testing.T) {
	for _, press := range []tea.KeyPressMsg{{Code: tea.KeyEscape}, key("?")} {
		m := New(Styles{}).For(msg.Notes)

		_, cmd := m.Update(press)

		if cmd == nil {
			t.Fatalf("%s produced no command", press)
		}
		if got, want := cmd(), (msg.Goto{Screen: msg.Notes}); got != want {
			t.Errorf("%s gave %#v, want %#v", press, got, want)
		}
	}
}

// A key help does not recognize is swallowed rather than passed through, so
// the screen underneath does not act on it while help is on top.
func TestOtherKeysAreSwallowed(t *testing.T) {
	m := New(Styles{})

	_, cmd := m.Update(key("n"))

	if cmd != nil {
		t.Errorf("n on the help screen produced a command: %#v", cmd())
	}
}

func TestViewListsEveryScreensKeys(t *testing.T) {
	view := sized().View()

	for _, want := range []string{"Dashboard", "New review", "Submit", "Notes", "Everywhere", "ctrl+c", "quit"} {
		if !strings.Contains(view, want) {
			t.Errorf("view is missing %q:\n%s", want, view)
		}
	}
}

// Footer skips an entry with no Short, which is how Dashboard's movement
// keys and its dynamic archived toggle stay out of the footer.
func TestFooterSkipsEntriesWithNoShortLabel(t *testing.T) {
	got := Footer([]Entry{
		{Key: "n", Short: "new"},
		{Key: "j/k", Long: "move the selection"},
		{Key: "q", Short: "quit"},
	})

	want := []string{"n new", "q quit"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("Footer() = %v, want %v", got, want)
	}
}

// A short terminal cannot show every line at once, so content past the
// bottom of the pane has to be reachable by scrolling rather than silently
// cut off.
func TestAShortViewportStillReachesTheLastSectionByScrolling(t *testing.T) {
	m := New(Styles{}).SetSize(100, 10)

	if strings.Contains(m.View(), "quit") {
		t.Fatal("the Everywhere section is already visible without scrolling; this test needs a shorter viewport to be meaningful")
	}

	m.Viewport.GotoBottom()

	if !strings.Contains(m.View(), "quit") {
		t.Error("scrolling to the bottom still does not reach the Everywhere section")
	}
}

// Update persists the Viewport it returns, which is what makes a real key
// press scroll. Content set only on a value-receiver View's own copy would
// never reach the model Update and the next View both operate on.
func TestDownArrowScrollsThroughUpdate(t *testing.T) {
	m := New(Styles{}).SetSize(100, 10)
	before := m.View()

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})

	if after := m.View(); after == before {
		t.Error("pressing down did not change what the pane shows")
	}
}

// The whole point of a shared table is that the footer and the full help
// screen cannot drift the way they did before it existed: every key Footer
// shows has to come from the same Long text View renders.
func TestFooterKeysAllAppearInTheFullHelpScreen(t *testing.T) {
	view := sized().View()

	for _, table := range [][]Entry{Dashboard, NewReview, Submit, Notes} {
		for _, part := range Footer(table) {
			key := strings.SplitN(part, " ", 2)[0]
			if !strings.Contains(view, key) {
				t.Errorf("footer key %q from %q is not shown anywhere in the full help screen", key, part)
			}
		}
	}
}
