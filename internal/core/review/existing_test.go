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

// Leaving a drafted row's background session while it works puts the row back
// to reviewing with its draft's id, and the draft is still pending on GitHub. A
// review that has not posted a draft yet carries no id. An interactive session
// that is reviewing is open in a terminal, and a submit would archive the row
// and delete the clone under it.
func TestAReviewingRecordIsSubmittableOnceItHoldsADraft(t *testing.T) {
	tests := []struct {
		name string
		rec  review.Record
		want bool
	}{
		{
			name: "background session holding a draft",
			rec:  review.Record{State: review.StateReviewing, Mode: review.ModeBackground, ReviewID: 9},
			want: true,
		},
		{
			name: "background session with no draft yet",
			rec:  review.Record{State: review.StateReviewing, Mode: review.ModeBackground},
			want: false,
		},
		{
			name: "interactive session holding a draft",
			rec:  review.Record{State: review.StateReviewing, Mode: review.ModeInteractive, ReviewID: 9},
			want: false,
		},
		{
			name: "abandoned background session holding a draft",
			rec:  review.Record{State: review.StateAbandoned, Mode: review.ModeBackground, ReviewID: 9},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.rec.Submittable(); got != tt.want {
				t.Errorf("Submittable() = %v, want %v", got, tt.want)
			}
		})
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
