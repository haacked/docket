package newreview

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/haacked/docket/internal/tui/msg"
)

func model() Model {
	return New(Styles{}, []string{"claude", "codex"}, []string{"claude"}, "claude", "")
}

func typed(m Model, text string) Model {
	m = m.SetValue(text)
	return m
}

func TestEnterStartsAReviewOfWhatWasTyped(t *testing.T) {
	m := typed(model(), "https://github.com/haacked/docket/pull/7")

	m, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter produced no command")
	}
	want := msg.StartReview{Input: "https://github.com/haacked/docket/pull/7", Engine: "claude"}
	if got := cmd(); got != want {
		t.Errorf("got %#v, want %#v", got, want)
	}
	if m.Busy == "" {
		t.Error("screen should show that it is working")
	}
}

func TestEnterIgnoresSomethingThatIsNotAPullRequest(t *testing.T) {
	m := typed(model(), "not a pull request")

	if _, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		t.Errorf("enter produced %#v", cmd())
	}
}

func TestEnterIgnoresABareNumberWithNoDefaultRepo(t *testing.T) {
	m := typed(model(), "123")

	if _, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		t.Errorf("enter produced %#v", cmd())
	}
}

func TestABareNumberWorksWithADefaultRepo(t *testing.T) {
	m := typed(New(Styles{}, []string{"claude"}, []string{"claude"}, "claude", "haacked/docket"), "123")

	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter produced no command")
	}
	if got := cmd().(msg.StartReview).Input; got != "123" {
		t.Errorf("input = %q, want %q", got, "123")
	}
}

func TestTabCyclesEngines(t *testing.T) {
	m := model()

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.Engine != "codex" {
		t.Fatalf("engine after tab = %q, want codex", m.Engine)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.Engine != "claude" {
		t.Errorf("engine after two tabs = %q, want claude", m.Engine)
	}
}

func TestEscapeGoesBack(t *testing.T) {
	_, cmd := model().Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("escape produced no command")
	}
	if got, want := cmd(), (msg.Goto{Screen: msg.Dashboard}); got != want {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestViewShowsWhatWillHappen(t *testing.T) {
	view := typed(model(), "haacked/docket#7").View()
	if !strings.Contains(view, "haacked/docket#7") {
		t.Errorf("view does not name the pull request:\n%s", view)
	}

	view = typed(model(), "nonsense").View()
	if !strings.Contains(view, "cannot read") {
		t.Errorf("view does not explain the problem:\n%s", view)
	}
}

// ctrlB is the key that moves a review off the terminal.
var ctrlB = tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl}

func TestBackgroundTogglesAndReachesTheIntent(t *testing.T) {
	m, _ := typed(model(), "https://github.com/haacked/docket/pull/7").Update(ctrlB)
	if !m.Background {
		t.Fatal("ctrl+b did not choose the background")
	}

	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter produced no command")
	}
	want := msg.StartReview{
		Input:      "https://github.com/haacked/docket/pull/7",
		Engine:     "claude",
		Background: true,
	}
	if got := cmd(); got != want {
		t.Errorf("got %#v, want %#v", got, want)
	}

	if m, _ = m.Update(ctrlB); m.Background {
		t.Error("ctrl+b did not toggle back to the terminal")
	}
}

// codex has no background mode, so the choice is refused here rather than after
// the pull request has been resolved and cloned.
func TestBackgroundIsRefusedForAnEngineThatHasNone(t *testing.T) {
	m := model()
	m.Engine = "codex"

	if m.CanBackground() {
		t.Fatal("codex was offered a background mode")
	}
	if m, _ = m.Update(ctrlB); m.Background {
		t.Error("ctrl+b chose a background run for an engine that has none")
	}
	if view := m.View(); !strings.Contains(view, "no background mode") {
		t.Errorf("the screen does not say why the choice is missing:\n%s", view)
	}
}

// Choosing an engine with no background mode has to drop a background choice
// already made, or enter would start a review the engine refuses.
func TestSwitchingToAnEngineWithNoBackgroundDropsTheChoice(t *testing.T) {
	m, _ := model().Update(ctrlB)
	if !m.Background {
		t.Fatal("ctrl+b did not choose the background")
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.Engine != "codex" {
		t.Fatalf("engine = %q, want the tab to have moved on", m.Engine)
	}
	if m.Background {
		t.Error("the background choice survived a move to an engine that has none")
	}
}
