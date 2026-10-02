package submit

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/tui/msg"
)

func pushedRecord() review.Record {
	rec := record()
	rec.State = review.StatePushed
	rec.ReviewID = 0
	rec.Fix = true
	rec.FixBase = "0000000aaaaaaa"
	rec.FixHead = "1234567deadbeef"
	return rec
}

// The user fixed what the review found, so approving is the likely answer.
func TestAPushedRowStartsOnApprove(t *testing.T) {
	m := New(Styles{}).For(pushedRecord(), review.SubmitEventsFor("someone", "haacked"))

	if m.Event != review.EventApprove {
		t.Errorf("event = %q, want approve first for a pushed fix review", m.Event)
	}
	if len(m.Events) != len(review.SubmitEvents) {
		t.Errorf("events = %v, want every event still offered", m.Events)
	}
}

// Approving your own pull request is a 422, so the screen still leaves it out.
func TestAPushedRowOfMyOwnPullRequestStartsOnWhatIsLeft(t *testing.T) {
	m := New(Styles{}).For(pushedRecord(), review.SubmitEventsFor("haacked", "haacked"))

	for _, e := range m.Events {
		if e == review.EventApprove {
			t.Fatalf("events = %v, want no approve on my own pull request", m.Events)
		}
	}
	if m.Event == "" {
		t.Error("no event is chosen")
	}
}

// A draft review keeps comment first.
func TestADraftedRowStillStartsOnComment(t *testing.T) {
	if got := model().Event; got != review.EventComment {
		t.Errorf("event = %q, want comment", got)
	}
}

// The review goes on the commit the fixes ended at, which is not necessarily
// the head the user sees on GitHub by the time they read this.
func TestThePushedRowsScreenSaysItPostsANewReviewOnTheFixHead(t *testing.T) {
	m := New(Styles{}).For(pushedRecord(), review.SubmitEventsFor("someone", "haacked"))

	view := ansi.Strip(m.View())

	if !strings.Contains(view, "1234567") {
		t.Errorf("the view does not name the commit the review goes on:\n%s", view)
	}
	if !strings.Contains(strings.ToLower(view), "new review") {
		t.Errorf("the view does not say it posts a new review:\n%s", view)
	}
}

// A pushed row has no pending review, so the body starts empty and ctrl+s
// sends what the user typed.
func TestAPushedRowStartsWithAnEmptyBodyAndSendsWhatWasTyped(t *testing.T) {
	m := New(Styles{}).For(pushedRecord(), review.SubmitEventsFor("someone", "haacked"))
	if got := m.Body.Value(); got != "" {
		t.Fatalf("body = %q, want empty", got)
	}
	for _, r := range "LGTM" {
		m, _ = m.Update(typed(string(r)))
	}

	_, cmd := m.Update(ctrlS)
	if cmd == nil {
		t.Fatal("ctrl+s produced no command")
	}

	want := msg.SubmitReview{ID: "rec-1", ReviewID: 0, Event: review.EventApprove, Body: "LGTM"}
	if got := cmd(); got != want {
		t.Errorf("got %#v, want %#v", got, want)
	}
}
