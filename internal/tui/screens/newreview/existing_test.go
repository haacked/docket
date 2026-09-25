package newreview

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/tui/msg"
)

const typedURL = "https://github.com/haacked/docket/pull/4"

var notesWritten = time.Date(2026, 9, 18, 15, 30, 0, 0, time.UTC)

func press(s string) tea.KeyPressMsg { return tea.KeyPressMsg{Code: rune(s[0]), Text: s} }

// choosing is the screen after the root found notes and a pending draft for a
// typed pull request.
func choosing() Model {
	return typed(model(), typedURL).SetExisting(Existing{
		Ref:   "haacked/docket#4",
		Found: review.Found{NotesAt: notesWritten, PendingID: 12},
	})
}

// rereviewing is the screen u opens for a record already on the dashboard.
func rereviewing() Model {
	return model().SetExisting(Existing{
		Ref:      "haacked/docket#4",
		Engine:   "claude",
		RecordID: "rec-4",
		Found:    review.Found{NotesAt: notesWritten},
	})
}

func sent(t *testing.T, cmd tea.Cmd) msg.StartReview {
	t.Helper()
	if cmd == nil {
		t.Fatal("the key produced no command")
	}
	got, ok := cmd().(msg.StartReview)
	if !ok {
		t.Fatalf("the key sent %#v, want a StartReview", cmd())
	}
	return got
}

func TestTheChoiceStepSaysWhatWasFound(t *testing.T) {
	view := choosing().View()

	for _, want := range []string{"Existing review of haacked/docket#4", "2026-09-18", "pending draft", "view and ask", "append", "overwrite"} {
		if !strings.Contains(view, want) {
			t.Errorf("view is missing %q:\n%s", want, view)
		}
	}
}

func TestEachChoiceResendsTheReviewWithItsIntent(t *testing.T) {
	tests := []struct {
		key    string
		intent string
	}{
		{key: "v", intent: "ask"},
		{key: "a", intent: "append"},
		{key: "o", intent: "overwrite"},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			m, cmd := choosing().Update(press(tt.key))

			want := msg.StartReview{Input: typedURL, Engine: "claude", Intent: tt.intent}
			if got := sent(t, cmd); got != want {
				t.Errorf("got %#v, want %#v", got, want)
			}
			if m.Busy == "" {
				t.Error("the screen does not show that it is working")
			}
		})
	}
}

// Busy is what stops a second key from preparing the same pull request twice
// while the first is still resolving.
func TestAChoiceIsIgnoredWhileTheFirstIsInFlight(t *testing.T) {
	m, _ := choosing().Update(press("a"))

	if _, cmd := m.Update(press("o")); cmd != nil {
		t.Errorf("a second choice produced %#v", cmd())
	}
}

func TestTheChoiceStepKeepsTheFieldOutOfReach(t *testing.T) {
	m, _ := choosing().Update(press("x"))

	if got := m.Input.Value(); got != typedURL {
		t.Errorf("field = %q, want the typed URL untouched by keys on the choice step", got)
	}
}

// esc on a typed pull request goes back to the field with the URL still in it,
// not all the way to the dashboard.
func TestEscLeavesTheChoiceStepWithoutStartingAReview(t *testing.T) {
	m, cmd := choosing().Update(tea.KeyPressMsg{Code: tea.KeyEscape})

	if cmd != nil {
		if _, ok := cmd().(msg.StartReview); ok {
			t.Fatal("esc started a review")
		}
	}
	if m.Existing != nil {
		t.Error("esc left the screen on the choice step")
	}
	if got := m.Input.Value(); got != typedURL {
		t.Errorf("field = %q, want the typed URL kept", got)
	}
}

// ctrl+b is refused for an ask the same way the screen refuses it for codex.
// There is no draft for a background Q&A session to finish.
func TestViewAndAskNeverRunsInTheBackground(t *testing.T) {
	m, _ := choosing().Update(ctrlB)

	_, cmd := m.Update(press("v"))

	got := sent(t, cmd)
	if got.Intent != "ask" {
		t.Fatalf("intent = %q, want ask", got.Intent)
	}
	if got.Background {
		t.Error("view and ask asked for the background")
	}
}

func TestAnAppendCanRunInTheBackground(t *testing.T) {
	m, _ := choosing().Update(ctrlB)
	if !m.Background {
		t.Fatal("ctrl+b did not choose the background on the choice step")
	}

	_, cmd := m.Update(press("a"))

	if got := sent(t, cmd); !got.Background || got.Intent != "append" {
		t.Errorf("got %#v, want a background append", got)
	}
}

func TestTheChoiceStepRefusesTheBackgroundForAnEngineThatHasNone(t *testing.T) {
	m := choosing()
	m.Engine = "codex"

	m, _ = m.Update(ctrlB)

	if m.Background {
		t.Error("ctrl+b chose a background run for codex on the choice step")
	}
}

// Notes are what an ask reads. A pull request with only a review of mine on
// GitHub offers append and overwrite, which decide what review-code does with
// that review.
func TestViewAndAskNeedsNotes(t *testing.T) {
	m := typed(model(), typedURL).SetExisting(Existing{Ref: "haacked/docket#4", Found: review.Found{Submitted: true}})

	if _, cmd := m.Update(press("v")); cmd != nil {
		t.Errorf("v with no notes produced %#v", cmd())
	}
	if view := m.View(); strings.Contains(view, "view and ask") {
		t.Errorf("the view offers view and ask with no notes:\n%s", view)
	}
	if _, cmd := m.Update(press("a")); cmd == nil {
		t.Error("append is refused when a review of mine is on GitHub")
	}
}

func TestARereviewNamesTheRecordAndItsEngine(t *testing.T) {
	m := rereviewing()
	m.Engine = "codex"

	_, cmd := m.Update(press("o"))

	got := sent(t, cmd)
	if got.RecordID != "rec-4" || got.Intent != "overwrite" {
		t.Errorf("got %#v, want an overwrite of rec-4", got)
	}
	if got.Engine != "claude" {
		t.Errorf("engine = %q, want the record's own claude, not the screen's choice", got.Engine)
	}
}

// A codex record runs its re-review in the terminal. The next review typed on
// the screen still runs in the background that the user chose.
func TestARereviewOfACodexRecordKeepsTheBackgroundChoice(t *testing.T) {
	m := New(Styles{}, []string{"claude", "codex"}, []string{"claude"}, "claude", "", true).SetExisting(Existing{
		Ref:      "haacked/docket#4",
		Engine:   "codex",
		RecordID: "rec-4",
		Found:    review.Found{NotesAt: notesWritten},
	})

	m, cmd := m.Update(press("a"))
	if got := sent(t, cmd); got.Background {
		t.Errorf("got %#v, want the codex re-review in the terminal", got)
	}

	m = typed(m.Reset(), typedURL)
	_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := sent(t, cmd); !got.Background {
		t.Errorf("got %#v, want the next review in the background", got)
	}
}

// A re-review offers append and overwrite. The dashboard's c is how a record
// already there is asked about.
func TestARereviewOffersNoAsk(t *testing.T) {
	m := rereviewing()

	if _, cmd := m.Update(press("v")); cmd != nil {
		t.Errorf("v on a re-review produced %#v", cmd())
	}
	if view := m.View(); strings.Contains(view, "view and ask") {
		t.Errorf("the re-review view offers view and ask:\n%s", view)
	}
}

// u came from the dashboard and there is no field to go back to.
func TestEscOnARereviewGoesBackToTheDashboard(t *testing.T) {
	_, cmd := rereviewing().Update(tea.KeyPressMsg{Code: tea.KeyEscape})

	if cmd == nil {
		t.Fatal("esc produced no command")
	}
	if got, want := cmd(), (msg.Goto{Screen: msg.Dashboard}); got != want {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

// Reset runs every time the screen is opened with n, so it must not carry a
// choice step from an earlier pull request.
func TestResetLeavesTheChoiceStep(t *testing.T) {
	m := choosing().Reset()

	if m.Existing != nil {
		t.Error("Reset kept the choice step")
	}
	if !strings.Contains(m.View(), "Pull request") {
		t.Errorf("view after Reset is not the field:\n%s", m.View())
	}
}

// Plain enter still carries no intent. That empty intent is what tells the root
// to look for an existing review before preparing.
func TestEnterSendsNoIntent(t *testing.T) {
	_, cmd := typed(model(), typedURL).Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if got := sent(t, cmd); got.Intent != "" || got.RecordID != "" {
		t.Errorf("got %#v, want no intent and no record", got)
	}
}

// The screen already sent the choice to the root with its background setting. A
// ctrl+b now would show the review running somewhere it does not.
func TestCtrlBDoesNotMoveAReviewThatIsPreparing(t *testing.T) {
	m, _ := choosing().Update(press("a"))
	if m.Busy == "" {
		t.Fatal("the choice did not mark the screen busy")
	}

	m, _ = m.Update(ctrlB)

	if m.Background {
		t.Error("ctrl+b moved a review that was already preparing in this terminal")
	}
}

func TestEscOnARereviewGoesBackWhilePreparing(t *testing.T) {
	m, _ := rereviewing().Update(press("a"))
	if m.Busy == "" {
		t.Fatal("the choice did not mark the screen busy")
	}

	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("esc produced no command while preparing")
	}

	if got, want := cmd(), (msg.Goto{Screen: msg.Dashboard}); got != want {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestTheChoiceStepsBusyLineCarriesTheSpinnersFrame(t *testing.T) {
	m, _ := choosing().Update(press("a"))
	m.Spinner.Frame = "⠙"

	if view := m.View(); !strings.Contains(view, "⠙ preparing…") {
		t.Errorf("the view does not put the frame before what is running:\n%s", view)
	}
}
