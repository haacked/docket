package review_test

import (
	"testing"
	"time"

	"github.com/haacked/docket/internal/core/review"
)

func TestAReviewedRecordIsOpen(t *testing.T) {
	if !review.StateReviewed.Open() {
		t.Error("a reviewed record is closed, so the dashboard hides a review the user adopted to ask about")
	}
}

// A reviewed row has no pending review behind it. Submitting would post against
// a review id that is not a draft of mine.
func TestAReviewedRecordIsNotSubmittable(t *testing.T) {
	rec := review.Record{ID: "rec-1", State: review.StateReviewed, ReviewID: 9}

	if rec.Submittable() {
		t.Error("a reviewed record is submittable, want only a drafted one to be")
	}
}

func TestTheReviewedEventFoldsToTheReviewedState(t *testing.T) {
	events := []review.Event{
		event("rec-1", foldBase, "prepared", map[string]any{"title": "Fix the retry loop"}),
		event("rec-1", foldBase.Add(time.Minute), "reviewed", nil),
	}

	got := review.Fold(events)

	if len(got) != 1 {
		t.Fatalf("Fold returned %d records (%+v), want 1", len(got), got)
	}
	if got[0].State != review.StateReviewed {
		t.Errorf("state = %q, want %q", got[0].State, review.StateReviewed)
	}
}

// Compaction rewrites the log from what Fold produced, and it refuses to when an
// event is unknown. The reviewed state has to round-trip through both maps or a
// compaction drops the row.
func TestTheReviewedStateHasAnEventThatRoundTrips(t *testing.T) {
	eventType, ok := review.EventForState(review.StateReviewed)
	if !ok {
		t.Fatal("the reviewed state has no event type, so appending a reviewed record fails")
	}
	if eventType != review.EventReviewed {
		t.Errorf("event type = %q, want %q", eventType, review.EventReviewed)
	}
	if !review.KnownEvent(review.Event{ID: "rec-1", Type: eventType}) {
		t.Errorf("event %q is unknown to Fold, so compaction stops", eventType)
	}
}

func TestIntentUsesTheSpelledOutValues(t *testing.T) {
	tests := []struct {
		intent review.Intent
		want   string
	}{
		{intent: review.IntentReview, want: "review"},
		{intent: review.IntentAppend, want: "append"},
		{intent: review.IntentOverwrite, want: "overwrite"},
		{intent: review.IntentAsk, want: "ask"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if string(tt.intent) != tt.want {
				t.Errorf("intent = %q, want %q", tt.intent, tt.want)
			}
		})
	}
}

func TestFoldReadsTheIntentAnEventCarries(t *testing.T) {
	events := []review.Event{
		event("rec-1", foldBase, "prepared", map[string]any{"intent": "append"}),
	}

	got := review.Fold(events)

	if len(got) != 1 {
		t.Fatalf("Fold returned %d records (%+v), want 1", len(got), got)
	}
	if got[0].Intent != review.IntentAppend {
		t.Errorf("intent = %q, want %q: the JSON key is not \"intent\"", got[0].Intent, review.IntentAppend)
	}
}
