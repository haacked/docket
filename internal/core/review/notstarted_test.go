package review_test

import (
	"testing"
	"time"

	"github.com/haacked/docket/internal/core/review"
)

// Compaction rewrites the log from what Fold produced, and it refuses to when an
// event is unknown. A launch the agent refused has to round-trip through both
// maps or a compaction drops the row.
func TestTheNotStartedStateHasAnEventThatRoundTrips(t *testing.T) {
	eventType, ok := review.EventForState(review.StateNotStarted)
	if !ok {
		t.Fatal("the not started state has no event type, so appending a refused launch fails")
	}
	got := review.Fold([]review.Event{
		event("rec-1", foldBase, "prepared", nil),
		event("rec-1", foldBase.Add(time.Minute), eventType, map[string]any{"err": "claude does not trust /tmp/x"}),
	})
	if len(got) != 1 || got[0].State != review.StateNotStarted {
		t.Fatalf("Fold = %+v, want one record that did not start", got)
	}
	if got[0].Err == "" {
		t.Error("the fold dropped the reason the launch was refused")
	}
}

// A launch that never ran still wants the user's attention, and no session is
// holding the record, so the user may start it again or review it again.
func TestANotStartedRecordIsOpenAndSettled(t *testing.T) {
	rec := review.Record{State: review.StateNotStarted, Mode: review.ModeBackground}

	if !rec.State.Open() {
		t.Error("a launch that never ran is closed, so the dashboard hides it")
	}
	if rec.InProgress() {
		t.Error("a launch that never ran reads as in progress, so u and c refuse it")
	}
	if rec.InBackgroundSession() {
		t.Error("a launch that never ran reads as a running session, so r and R refuse it as running")
	}
}

func TestTheNotStartedLabelSaysTheReviewDidNotStart(t *testing.T) {
	if got := review.StateNotStarted.Label(); got != "did not start" {
		t.Errorf("label = %q", got)
	}
}
