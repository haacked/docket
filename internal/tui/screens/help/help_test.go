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
func sized() Model { return New(Styles{}).SetSize(100, 100) }

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

	want := []string{"n", "q"}
	if len(got) != len(want) || got[0].Key != want[0] || got[1].Key != want[1] {
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
		for _, e := range Footer(table) {
			if !strings.Contains(view, e.Key) {
				t.Errorf("footer key %q (%q) is not shown anywhere in the full help screen", e.Key, e.Short)
			}
		}
	}
}

// A pane that grows while scrolled to the bottom stays on the last line rather
// than showing blank rows past the end.
func TestGrowingThePaneDoesNotScrollPastTheEnd(t *testing.T) {
	m := New(Styles{}).SetSize(100, 10)
	m.Viewport.GotoBottom()

	m = m.SetSize(100, 20)

	if m.Viewport.PastBottom() {
		t.Error("the pane is scrolled past its last line after it grew")
	}
}

// section is the trimmed lines under a section's title, up to the blank line
// that ends it.
func section(t *testing.T, view, title string) []string {
	t.Helper()
	var out []string
	in := false
	for _, line := range strings.Split(view, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case !in && line == title:
			in = true
		case in && line == "":
			return out
		case in:
			out = append(out, line)
		}
	}
	if !in {
		t.Fatalf("the view has no %q section:\n%s", title, view)
	}
	return out
}

// hasKey reports whether a section lists key. keyLine pads each key and ends it
// with a space, so a prefix of key and a space names the key alone.
func hasKey(lines []string, key string) bool {
	for _, line := range lines {
		if strings.HasPrefix(line, key+" ") {
			return true
		}
	}
	return false
}

func TestViewListsTheTeamsScreensKeys(t *testing.T) {
	lines := section(t, sized().View(), "Teams")

	for _, want := range []string{"space", "enter", "esc"} {
		if !hasKey(lines, want) {
			t.Errorf("the Teams section does not list %q:\n%s", want, strings.Join(lines, "\n"))
		}
	}
}

func TestViewListsTUnderReviewRequests(t *testing.T) {
	lines := section(t, sized().View(), "Review requests")

	if !hasKey(lines, "t") {
		t.Errorf("the Review requests section does not list t:\n%s", strings.Join(lines, "\n"))
	}
}
