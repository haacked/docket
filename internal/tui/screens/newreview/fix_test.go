package newreview

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/tui/msg"
)

// ctrlF is the key that chooses between fixing and drafting a review.
var ctrlF = tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl}

func TestTheFixChoiceStartsOnAuto(t *testing.T) {
	if got := model().Fix; got != review.FixAuto {
		t.Errorf("fix = %q, want auto so fix_authors decides", got)
	}
}

func TestCtrlFCyclesAutoThenFixThenDraft(t *testing.T) {
	m := typed(model(), typedURL)

	for _, want := range []review.FixChoice{review.FixOn, review.FixOff, review.FixAuto} {
		m, _ = m.Update(ctrlF)
		if m.Fix != want {
			t.Fatalf("fix after ctrl+f = %q, want %q", m.Fix, want)
		}
	}
}

// The field takes arbitrary text, so the screen takes ctrl+f away from it the
// way it takes ctrl+b.
func TestCtrlFDoesNotReachTheField(t *testing.T) {
	m := typed(model(), typedURL)

	m, cmd := m.Update(ctrlF)

	if got := m.Input.Value(); got != typedURL {
		t.Errorf("field = %q, want %q unchanged", got, typedURL)
	}
	if cmd != nil {
		t.Errorf("ctrl+f produced %#v, want only the choice to change", cmd())
	}
}

func TestEnterCarriesTheFixChoice(t *testing.T) {
	tests := []struct {
		presses int
		want    string
	}{
		{presses: 0, want: ""},
		{presses: 1, want: "fix"},
		{presses: 2, want: "draft"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			m := typed(model(), typedURL)
			for range tt.presses {
				m, _ = m.Update(ctrlF)
			}

			_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

			want := msg.StartReview{Input: typedURL, Engine: "claude", Fix: tt.want}
			if got := sent(t, cmd); got != want {
				t.Errorf("got %#v, want %#v", got, want)
			}
		})
	}
}

// The fix choice and the background choice are independent. A fix review runs
// in the background and in the terminal alike.
func TestTheFixChoiceTravelsWithTheBackgroundChoice(t *testing.T) {
	m := typed(model(), typedURL)
	m, _ = m.Update(ctrlB)
	m, _ = m.Update(ctrlF)

	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	got := sent(t, cmd)
	if !got.Background || got.Fix != string(review.FixOn) {
		t.Errorf("got %#v, want a background fix review", got)
	}
}

// The first enter asks what is already there. The answer resends the review,
// and the choice made before enter has to survive that round trip.
func TestTheChoiceStepKeepsTheFixChoice(t *testing.T) {
	m := typed(model(), typedURL)
	m, _ = m.Update(ctrlF)
	m = m.SetExisting(Existing{Ref: "haacked/docket#4", Found: review.Found{NotesAt: notesWritten, PendingID: 12}})

	for _, key := range []string{"a", "o", "v"} {
		t.Run(key, func(t *testing.T) {
			_, cmd := m.Update(press(key))

			if got := sent(t, cmd); got.Fix != string(review.FixOn) {
				t.Errorf("%s sent %#v, want the fix choice kept", key, got)
			}
		})
	}
}

// The choice step shows no text field, so ctrl+f can still change the choice
// there for a typed pull request.
func TestCtrlFChangesTheChoiceOnTheChoiceStep(t *testing.T) {
	m, _ := choosing().Update(ctrlF)

	_, cmd := m.Update(press("a"))

	got := sent(t, cmd)
	if got.Fix != string(review.FixOn) || got.Intent != string(review.IntentAppend) {
		t.Errorf("got %#v, want an append that fixes", got)
	}
}

func TestCtrlFIsIgnoredWhileTheScreenIsBusy(t *testing.T) {
	m, _ := typed(model(), typedURL).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.Busy == "" {
		t.Fatal("the screen is not busy after enter")
	}

	m, _ = m.Update(ctrlF)

	if m.Fix != review.FixAuto {
		t.Errorf("fix = %q, want the choice left alone once the review was sent", m.Fix)
	}
}

// fixLine is the line of the view that says whether the review fixes.
func fixLine(t *testing.T, view string) (string, int) {
	t.Helper()
	lines := strings.Split(ansi.Strip(view), "\n")
	run := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "Run") {
			run = i
		}
	}
	if run < 0 || run+1 >= len(lines) {
		t.Fatalf("the view has no line under the run line:\n%s", view)
	}
	return lines[run+1], run + 1
}

// A line under the run line says which applies, so the user knows before enter
// whether the review edits files or drafts a review.
func TestTheViewSaysWhetherTheReviewFixes(t *testing.T) {
	tests := []struct {
		presses int
		want    string
	}{
		{presses: 0, want: "fixes pull requests by fix_authors"},
		{presses: 1, want: "fixes the code for you to push"},
		{presses: 2, want: "drafts a review"},
	}

	seen := map[string]bool{}
	for _, tt := range tests {
		m := typed(model(), typedURL)
		for range tt.presses {
			m, _ = m.Update(ctrlF)
		}

		line, _ := fixLine(t, m.View())

		// Every label mentions both fixing and drafting, so only the opening
		// words tell them apart.
		if !strings.HasPrefix(fixText(line), tt.want) {
			t.Errorf("after %d ctrl+f the line under the run line is %q, want it to start with %q", tt.presses, line, tt.want)
		}
		seen[line] = true
	}
	if len(seen) != len(tests) {
		t.Errorf("the three choices read the same: %v", seen)
	}
}

func TestTheChoiceStepSaysWhetherTheReviewFixes(t *testing.T) {
	m, _ := choosing().Update(ctrlF)

	line, _ := fixLine(t, m.View())

	if want := "fixes the code for you to push"; !strings.HasPrefix(fixText(line), want) {
		t.Errorf("the line under the run line is %q, want it to start with %q", line, want)
	}
}

// fixText is the fix line without its label.
func fixText(line string) string {
	return strings.TrimSpace(strings.TrimPrefix(line, "Fix"))
}

// n opens the screen through Reset. A ctrl+f chosen for one pull request must
// not carry into the next, which would then edit a checkout and post no draft.
func TestResetPutsTheFixChoiceBackToAuto(t *testing.T) {
	m, _ := typed(model(), typedURL).Update(ctrlF)
	if m.Fix == review.FixAuto {
		t.Fatal("ctrl+f left the choice on auto")
	}

	if got := m.Reset().Fix; got != review.FixAuto {
		t.Errorf("fix after Reset = %q, want auto", got)
	}
}
