package submit

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/tui/msg"
)

func record() review.Record {
	return review.Record{
		ID:       "rec-1",
		Ref:      pr.Ref{Org: "haacked", Repo: "docket", Number: 7},
		URL:      "https://github.com/haacked/docket/pull/7",
		Title:    "Add a thing",
		Author:   "someone",
		State:    review.StateDrafted,
		ReviewID: 4321,
	}
}

// model is the screen as the root opens it, for a pull request that is not mine.
func model() Model {
	return New(Styles{}).For(record(), review.SubmitEventsFor("someone", "haacked"))
}

func key(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code} }

func typed(s string) tea.KeyPressMsg { return tea.KeyPressMsg{Code: rune(s[0]), Text: s} }

var ctrlS = tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}

func TestTheChoiceStartsOnComment(t *testing.T) {
	if got := model().Event; got != review.EventComment {
		t.Errorf("event = %q, want comment: it is the choice that always applies", got)
	}
}

func TestTabCyclesTheEvents(t *testing.T) {
	m := model()
	seen := []string{m.Event}

	for range len(m.Events) {
		m, _ = m.Update(key(tea.KeyTab))
		seen = append(seen, m.Event)
	}

	for _, want := range m.Events {
		if !slices.Contains(seen, want) {
			t.Errorf("cycling visited %v, want %s among them", seen, want)
		}
	}
	if seen[len(seen)-1] != review.EventComment {
		t.Errorf("a full cycle ended on %q, want it back at comment", seen[len(seen)-1])
	}
}

// GitHub answers 422 to approving your own pull request, so the choice is never
// reachable rather than offered and refused.
func TestApprovingMyOwnPullRequestIsNotOffered(t *testing.T) {
	rec := record()
	rec.Author = "haacked"
	m := New(Styles{}).For(rec, review.SubmitEventsFor(rec.Author, "haacked"))

	if slices.Contains(m.Events, review.EventApprove) {
		t.Fatalf("events = %v, want no approval of my own pull request", m.Events)
	}
	for range len(m.Events) + 1 {
		m, _ = m.Update(key(tea.KeyTab))
		if m.Event == review.EventApprove {
			t.Fatalf("cycling reached %q on my own pull request", m.Event)
		}
	}
	if !strings.Contains(m.View(), "own pull request") {
		t.Errorf("the view does not say why approval is missing:\n%s", m.View())
	}
}

func TestSubmittingSendsTheEventAndTheBodyUp(t *testing.T) {
	m := model()
	m.Body.SetValue("  Two things worth changing.\n")

	m, _ = m.Update(key(tea.KeyTab))
	m, cmd := m.Update(ctrlS)
	if cmd == nil {
		t.Fatal("submitting produced no command")
	}

	got := cmd()
	want := msg.SubmitReview{ID: "rec-1", ReviewID: 4321, Event: m.Event, Body: "Two things worth changing."}
	if got != want {
		t.Errorf("got %#v, want %#v", got, want)
	}
	if m.Busy == "" {
		t.Error("the screen should show that it is working")
	}
}

func TestSubmittingWithNoBodyIsAllowed(t *testing.T) {
	_, cmd := model().Update(ctrlS)
	if cmd == nil {
		t.Fatal("submitting an empty body produced no command")
	}

	if got := cmd().(msg.SubmitReview).Body; got != "" {
		t.Errorf("body = %q, want none", got)
	}
}

// A second press while the first is in flight would submit the review twice.
func TestSubmittingTwiceSendsOneReview(t *testing.T) {
	m, _ := model().Update(ctrlS)

	if _, cmd := m.Update(ctrlS); cmd != nil {
		t.Errorf("a second press produced %#v", cmd())
	}
}

// The body is a text area, so enter belongs to it.
func TestEnterDoesNotSubmit(t *testing.T) {
	m := model()

	m, cmd := m.Update(key(tea.KeyEnter))
	if cmd != nil {
		if _, sent := cmd().(msg.SubmitReview); sent {
			t.Error("enter submitted the review instead of ending the line")
		}
	}
	if m.Busy != "" {
		t.Error("enter marked the screen busy")
	}
}

func TestTypingGoesIntoTheBody(t *testing.T) {
	m := model()

	for _, s := range []string{"h", "i"} {
		m, _ = m.Update(typed(s))
	}

	if got := m.Body.Value(); got != "hi" {
		t.Errorf("body = %q, want what was typed", got)
	}
}

// ? is not bound here the way it is on Dashboard and Notes. A review body is
// free text, and writing "is this intentional?" in one is reasonable.
func TestQuestionMarkGoesIntoTheBodyRatherThanOpeningHelp(t *testing.T) {
	m, _ := model().Update(typed("?"))

	if got := m.Body.Value(); got != "?" {
		t.Errorf("body = %q, want the ? typed into it", got)
	}
}

func TestEscapeGoesBack(t *testing.T) {
	_, cmd := model().Update(key(tea.KeyEscape))
	if cmd == nil {
		t.Fatal("escape produced no command")
	}

	if got, want := cmd(), (msg.Goto{Screen: msg.Dashboard}); got != want {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestViewNamesThePullRequestAndEveryChoice(t *testing.T) {
	view := model().View()

	if !strings.Contains(view, "haacked/docket#7") || !strings.Contains(view, "Add a thing") {
		t.Errorf("the view does not name the pull request:\n%s", view)
	}
	for _, event := range review.SubmitEvents {
		if !strings.Contains(view, event) {
			t.Errorf("the view does not offer %s:\n%s", event, view)
		}
	}
}

func TestForResetsTheBodyBetweenRecords(t *testing.T) {
	m := model().SetDraft("a summary for the first review")

	m = m.For(record(), review.SubmitEvents)

	if got := m.Body.Value(); got != "" {
		t.Errorf("body = %q, want it cleared: it would otherwise be posted on the next review", got)
	}
	if strings.Contains(m.View(), keepsSummary) {
		t.Errorf("the next record's screen still speaks of the last one's summary:\n%s", m.View())
	}
}

// keepsSummary is part of the hint the screen shows under an emptied summary.
const keepsSummary = "keeps the draft's summary"

// The read of GitHub runs in a command, so the user can start typing before it
// lands. What they typed wins.
func TestTheDraftsSummaryLeavesWhatTheUserTyped(t *testing.T) {
	m, _ := model().Update(typed("x"))

	m = m.SetDraft("Nice fix! No blockers.")

	if got := m.Body.Value(); got != "x" {
		t.Errorf("body = %q, want what the user typed", got)
	}
}

// The text area turns a tab into spaces. Sending an untouched summary back would
// therefore rewrite it. Sending nothing leaves the draft's own text on GitHub.
func TestAnUntouchedSummaryIsNotSentBack(t *testing.T) {
	m := model().SetDraft("Nice fix!\n\n\tindented")

	_, cmd := m.Update(ctrlS)
	if cmd == nil {
		t.Fatal("submitting produced no command")
	}

	if got := cmd().(msg.SubmitReview).Body; got != "" {
		t.Errorf("body = %q, want none so that GitHub keeps the draft's summary", got)
	}
}

// GitHub stores a body saved from the browser with \r\n line endings.
func TestASummaryWithWindowsLineEndingsKeepsItsLines(t *testing.T) {
	m := model().SetDraft("Nice fix!\r\n\r\nOne nit.")

	if got := m.Body.Value(); got != "Nice fix!\n\nOne nit." {
		t.Errorf("body = %q, want one line break for each \\r\\n", got)
	}
}

func TestAnEditedSummaryIsSent(t *testing.T) {
	m := model().SetDraft("Nice fix!")

	m, _ = m.Update(typed("x"))
	_, cmd := m.Update(ctrlS)
	if cmd == nil {
		t.Fatal("submitting produced no command")
	}

	if got := cmd().(msg.SubmitReview).Body; got != "xNice fix!" {
		t.Errorf("body = %q, want the edited summary", got)
	}
}

// GitHub keeps the draft's summary when the submit leaves out the body. An
// emptied body therefore cannot remove the summary. The screen says so.
func TestEmptyingTheSummarySaysGitHubKeepsIt(t *testing.T) {
	m := model().SetDraft("Nice fix!")
	if strings.Contains(m.View(), keepsSummary) {
		t.Errorf("the view warns about an empty body while the summary is there:\n%s", m.View())
	}

	m.Body.SetValue("  \n")

	if !strings.Contains(m.View(), keepsSummary) {
		t.Errorf("the view does not say an empty body keeps the summary:\n%s", m.View())
	}
}

// A change made after ctrl+s would never be sent. The screen would then show an
// event that the review did not go out with.
func TestTabDoesNotChangeTheEventWhileSubmitting(t *testing.T) {
	m, _ := model().Update(ctrlS)
	event := m.Event

	m, _ = m.Update(key(tea.KeyTab))

	if m.Event != event {
		t.Errorf("event = %q, want %q: tab changed it after the review went out", m.Event, event)
	}
}

func TestTypingDoesNotReachTheBodyWhileSubmitting(t *testing.T) {
	m, _ := model().Update(typed("a"))
	m, _ = m.Update(ctrlS)

	m, _ = m.Update(typed("b"))

	if got := m.Body.Value(); got != "a" {
		t.Errorf("body = %q, want what was sent: the typing came after the submit", got)
	}
}

// esc is the one key that works while the submit runs. The dashboard row says
// it is still submitting.
func TestEscapeGoesBackWhileSubmitting(t *testing.T) {
	m, _ := model().Update(ctrlS)

	_, cmd := m.Update(key(tea.KeyEscape))
	if cmd == nil {
		t.Fatal("escape produced no command while submitting")
	}

	if got, want := cmd(), (msg.Goto{Screen: msg.Dashboard}); got != want {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestSubmittingTakesTheFocusFromTheBody(t *testing.T) {
	m, _ := model().Update(ctrlS)

	if m.Body.Focused() {
		t.Error("the body still has the focus while the review is being submitted")
	}
}

// A failed submit leaves the user on this screen to retry. A retry may mean
// editing the body first.
func TestClearingBusyMakesTheBodyEditableAgain(t *testing.T) {
	m, _ := model().Update(ctrlS)

	m = m.ClearBusy()
	m, _ = m.Update(typed("x"))

	if !m.Body.Focused() {
		t.Error("the body did not get the focus back")
	}
	if got := m.Body.Value(); got != "x" {
		t.Errorf("body = %q, want what was typed after the failure", got)
	}
}

func TestTheBusyLineCarriesTheSpinnersFrame(t *testing.T) {
	m, _ := model().Update(ctrlS)
	m.Spinner.Frame = "⠙"

	if view := m.View(); !strings.Contains(view, "⠙ submitting…") {
		t.Errorf("the view does not put the frame before what is running:\n%s", view)
	}
}
