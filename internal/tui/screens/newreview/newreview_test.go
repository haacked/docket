package newreview

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/haacked/docket/internal/tui/msg"
)

func model() Model {
	return New(Styles{}, []string{"claude", "codex"}, []string{"claude"}, "claude", "", false)
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
	m := typed(New(Styles{}, []string{"claude"}, []string{"claude"}, "claude", "haacked/docket", false), "123")

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

// ? is not bound here. The field takes arbitrary text, and a pasted URL's
// query string can carry one.
func TestQuestionMarkGoesIntoTheFieldRatherThanOpeningHelp(t *testing.T) {
	m, _ := model().Update(tea.KeyPressMsg{Code: '?', Text: "?"})

	if got := m.Input.Value(); got != "?" {
		t.Errorf("field = %q, want the ? typed into it", got)
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

// The background choice belongs to the user. An engine with no background mode
// sends a terminal run and keeps the choice. A move back to an engine that has
// one runs in the background again.
func TestTheBackgroundChoiceWaitsOutAnEngineThatHasNone(t *testing.T) {
	m, _ := typed(model(), typedURL).Update(ctrlB)

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.Engine != "codex" {
		t.Fatalf("engine = %q, want the tab to have moved on", m.Engine)
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if sent(t, cmd).Background {
		t.Error("enter asked codex for a background run")
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.Engine != "claude" {
		t.Fatalf("engine = %q, want the tab to have come back to claude", m.Engine)
	}
	_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !sent(t, cmd).Background {
		t.Error("the background choice did not survive a stop at codex")
	}
}

func TestAScreenBuiltForTheBackgroundStartsThere(t *testing.T) {
	m := typed(New(Styles{}, []string{"claude", "codex"}, []string{"claude"}, "claude", "", true), typedURL)

	if view := m.View(); !strings.Contains(view, "in the background") {
		t.Errorf("the view does not say the review runs in the background:\n%s", view)
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !sent(t, cmd).Background {
		t.Error("enter did not ask for the background")
	}
}

// A codex default with the background preference set runs codex in the
// terminal. A tab to claude sends a background review again.
func TestABackgroundScreenThatStartsOnCodexRunsInTheTerminal(t *testing.T) {
	m := typed(New(Styles{}, []string{"claude", "codex"}, []string{"claude"}, "codex", "", true), typedURL)

	if view := m.View(); !strings.Contains(view, "codex has no background mode") {
		t.Errorf("the screen does not say why codex runs in the terminal:\n%s", view)
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if sent(t, cmd).Background {
		t.Error("enter asked codex for a background run")
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := sent(t, cmd); got.Engine != "claude" || !got.Background {
		t.Errorf("got %#v, want a background claude review", got)
	}
}

// resolving is the screen after enter, while the root resolves the pull request.
func resolving(t *testing.T) Model {
	t.Helper()
	m, _ := typed(model(), "https://github.com/haacked/docket/pull/7").Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.Busy == "" {
		t.Fatal("enter did not mark the screen busy")
	}
	return m
}

// A terminal paste arrives as its own message rather than as key presses.
func TestAPasteDoesNotReachTheFieldWhileResolving(t *testing.T) {
	before := resolving(t)

	after, _ := before.Update(tea.PasteMsg{Content: "8"})

	if got, want := after.Input.Value(), before.Input.Value(); got != want {
		t.Errorf("field = %q, want %q", got, want)
	}
}

// The screen already sent the pull request, the engine, and the run choice to
// the root. A key that changed any of them would show a review other than the
// one that is starting.
func TestKeysOtherThanEscapeDoNothingWhileResolving(t *testing.T) {
	tests := []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{"typing", tea.KeyPressMsg{Code: 'x', Text: "x"}},
		{"tab", tea.KeyPressMsg{Code: tea.KeyTab}},
		{"ctrl+b", ctrlB},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := resolving(t)

			after, _ := before.Update(tc.key)

			if got, want := after.Input.Value(), before.Input.Value(); got != want {
				t.Errorf("field = %q, want %q", got, want)
			}
			if after.Engine != before.Engine {
				t.Errorf("engine = %q, want %q", after.Engine, before.Engine)
			}
			if after.Background != before.Background {
				t.Errorf("background = %v, want %v", after.Background, before.Background)
			}
		})
	}
}

func TestEscapeGoesBackWhileResolving(t *testing.T) {
	_, cmd := resolving(t).Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("escape produced no command while resolving")
	}

	if got, want := cmd(), (msg.Goto{Screen: msg.Dashboard}); got != want {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestTheBusyLineCarriesTheSpinnersFrame(t *testing.T) {
	m := resolving(t)
	m.Frame = "⠙"

	if view := m.View(); !strings.Contains(view, "⠙ resolving…") {
		t.Errorf("the view does not put the frame before what is running:\n%s", view)
	}
}
