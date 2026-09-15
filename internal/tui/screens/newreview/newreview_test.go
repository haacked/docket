package newreview

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/haacked/docket/internal/tui/msg"
)

func model() Model {
	return New(Styles{}, []string{"claude", "codex"}, "claude", "")
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
	m := typed(New(Styles{}, []string{"claude"}, "claude", "haacked/docket"), "123")

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
