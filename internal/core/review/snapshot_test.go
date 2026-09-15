package review_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/review"
)

func snapshot(t *testing.T, rec review.Record, eventType string) review.Event {
	t.Helper()
	fields, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	return review.Event{ID: rec.ID, At: time.Now().UTC(), Type: eventType, Fields: fields}
}

// TestFoldClearsFieldsASnapshotZeroes is the guard on Record's JSON tags. docket
// writes a whole record per event, so a field tagged `omitempty` would drop out
// of the snapshot exactly when the writer meant to clear it, and Fold would keep
// the stale value instead. That is how a failed session's error text used to
// survive a clean resume.
func TestFoldClearsFieldsASnapshotZeroes(t *testing.T) {
	submitted := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	full := review.Record{
		ID:             "rec-1",
		Ref:            pr.Ref{Org: "haacked", Repo: "docket", Number: 7},
		URL:            "https://github.com/haacked/docket/pull/7",
		Title:          "Add a thing",
		Author:         "haacked",
		Engine:         "claude",
		Mode:           review.ModeInteractive,
		Dir:            "/tmp/clone",
		SessionID:      "session-1",
		BGID:           "bg-1",
		StartedAt:      submitted.Add(-time.Hour),
		SubmittedAt:    &submitted,
		ArchivedAt:     &submitted,
		State:          review.StateDrafted,
		ReviewID:       99,
		NotesPath:      "/tmp/notes.md",
		PriorReviewIDs: []int64{1, 2},
		Err:            "the session died",
	}

	cleared := review.Record{ID: "rec-1", State: review.StateReviewing}
	got := review.Fold([]review.Event{
		snapshot(t, full, review.EventDrafted),
		snapshot(t, cleared, review.EventStarted),
	})

	if len(got) != 1 {
		t.Fatalf("records = %d, want 1", len(got))
	}
	if !reflect.DeepEqual(got[0], cleared) {
		t.Errorf("a snapshot that zeroes every field folded to %+v, want %+v", got[0], cleared)
	}
}

// TestRecordFieldsAreNeverOmitEmpty asserts the tags directly, so a field added
// later is covered whether or not someone remembers to set it in the literal
// above. Tier is the field that showed why: it is absent from that literal, so
// tagging it omitempty left TestFoldClearsFieldsASnapshotZeroes green while a
// snapshot meant to clear it kept the stale value.
func TestRecordFieldsAreNeverOmitEmpty(t *testing.T) {
	typ := reflect.TypeOf(review.Record{})
	for i := range typ.NumField() {
		field := typ.Field(i)
		tag := field.Tag.Get("json")
		if slices.Contains(strings.Split(tag, ",")[1:], "omitempty") {
			t.Errorf("Record.%s is tagged omitempty, so a snapshot that clears it drops the key and Fold keeps the stale value", field.Name)
		}
	}
}
