package review_test

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/haacked/docket/internal/core/review"
)

var foldBase = time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC)

// event is the single place the fold tests name Event's fields.
func event(id string, at time.Time, eventType string, fields map[string]any) review.Event {
	return review.Event{ID: id, At: at, Type: eventType, Fields: rawFields(fields)}
}

func rawFields(fields map[string]any) json.RawMessage {
	if len(fields) == 0 {
		return nil
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	return raw
}

func recordByID(t *testing.T, records []review.Record, id string) review.Record {
	t.Helper()
	for _, rec := range records {
		if rec.ID == id {
			return rec
		}
	}
	t.Fatalf("Fold returned no record with id %q, got %+v", id, records)
	return review.Record{}
}

func recordIDs(records []review.Record) []string {
	ids := make([]string, 0, len(records))
	for _, rec := range records {
		ids = append(ids, rec.ID)
	}
	return ids
}

func TestFoldWithoutEventsReturnsNoRecords(t *testing.T) {
	tests := []struct {
		name   string
		events []review.Event
	}{
		{name: "nil events", events: nil},
		{name: "empty events", events: []review.Event{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := review.Fold(tt.events)

			if len(got) != 0 {
				t.Errorf("Fold(%+v) returned %d records (%+v), want 0", tt.events, len(got), got)
			}
		})
	}
}

func TestFoldStartedEventCreatesARecord(t *testing.T) {
	events := []review.Event{
		event("rec-1", foldBase, "started", map[string]any{
			"title":      "Fix the retry loop",
			"dir":        "/home/me/.docket/scratch",
			"session_id": "0b7d2c4e-2f1a-4c3b-9a7e-5d6f8a9b0c1d",
		}),
	}

	got := review.Fold(events)

	if len(got) != 1 {
		t.Fatalf("Fold returned %d records (%+v), want 1", len(got), got)
	}
	rec := got[0]
	if rec.ID != "rec-1" {
		t.Errorf("record id = %q, want %q", rec.ID, "rec-1")
	}
	if rec.State != review.StateReviewing {
		t.Errorf("record state = %v, want %v", rec.State, review.StateReviewing)
	}
	if rec.Title != "Fix the retry loop" {
		t.Errorf("record title = %q, want %q", rec.Title, "Fix the retry loop")
	}
	if rec.Dir != "/home/me/.docket/scratch" {
		t.Errorf("record dir = %q, want %q", rec.Dir, "/home/me/.docket/scratch")
	}
	if rec.SessionID != "0b7d2c4e-2f1a-4c3b-9a7e-5d6f8a9b0c1d" {
		t.Errorf("record session id = %q, want %q", rec.SessionID, "0b7d2c4e-2f1a-4c3b-9a7e-5d6f8a9b0c1d")
	}
}

func TestFoldAdvancesStateOnLaterEvents(t *testing.T) {
	tests := []struct {
		eventType string
		want      review.State
	}{
		{eventType: "drafted", want: review.StateDrafted},
		{eventType: "submitted", want: review.StateSubmitted},
		{eventType: "archived", want: review.StateArchived},
		{eventType: "abandoned", want: review.StateAbandoned},
	}

	for _, tt := range tests {
		t.Run(tt.eventType, func(t *testing.T) {
			events := []review.Event{
				event("rec-1", foldBase, "started", map[string]any{"title": "Fix the retry loop"}),
				event("rec-1", foldBase.Add(20*time.Minute), tt.eventType, nil),
			}

			got := review.Fold(events)

			if len(got) != 1 {
				t.Fatalf("Fold returned %d records (%+v), want 1", len(got), got)
			}
			if got[0].State != tt.want {
				t.Errorf("state after a %q event = %v, want %v", tt.eventType, got[0].State, tt.want)
			}
		})
	}
}

func TestFoldKeepsFieldsLaterEventsDoNotCarry(t *testing.T) {
	events := []review.Event{
		event("rec-1", foldBase, "started", map[string]any{
			"title":      "Fix the retry loop",
			"dir":        "/home/me/.docket/clones/acme/tool/pr-123",
			"session_id": "0b7d2c4e-2f1a-4c3b-9a7e-5d6f8a9b0c1d",
		}),
		event("rec-1", foldBase.Add(20*time.Minute), "drafted", map[string]any{
			"notes_path": "/home/me/.agents/skills/review-code/.reviews/acme/tool/pr-123.md",
		}),
	}

	got := review.Fold(events)

	rec := recordByID(t, got, "rec-1")
	if rec.Title != "Fix the retry loop" {
		t.Errorf("title = %q, want the value the started event carried", rec.Title)
	}
	if rec.Dir != "/home/me/.docket/clones/acme/tool/pr-123" {
		t.Errorf("dir = %q, want the value the started event carried", rec.Dir)
	}
	if rec.SessionID != "0b7d2c4e-2f1a-4c3b-9a7e-5d6f8a9b0c1d" {
		t.Errorf("session id = %q, want the value the started event carried", rec.SessionID)
	}
	if rec.NotesPath != "/home/me/.agents/skills/review-code/.reviews/acme/tool/pr-123.md" {
		t.Errorf("notes path = %q, want the value the drafted event carried", rec.NotesPath)
	}
}

func TestFoldOverwritesFieldsLaterEventsCarry(t *testing.T) {
	events := []review.Event{
		event("rec-1", foldBase, "started", map[string]any{"title": "WIP"}),
		event("rec-1", foldBase.Add(time.Minute), "submitted", map[string]any{"title": "Fix the retry loop"}),
	}

	got := review.Fold(events)

	rec := recordByID(t, got, "rec-1")
	if rec.Title != "Fix the retry loop" {
		t.Errorf("title = %q, want %q", rec.Title, "Fix the retry loop")
	}
}

func TestFoldAppliesTheReviewIDAnEventCarries(t *testing.T) {
	events := []review.Event{
		event("rec-1", foldBase, "started", map[string]any{"title": "Fix the retry loop"}),
		event("rec-1", foldBase.Add(time.Minute), "drafted", map[string]any{"review_id": int64(99)}),
	}

	got := review.Fold(events)

	rec := recordByID(t, got, "rec-1")
	if rec.ReviewID != 99 {
		t.Errorf("review id = %d, want 99", rec.ReviewID)
	}
}

func TestFoldIgnoresUnknownEventTypesWithoutDroppingTheRecord(t *testing.T) {
	events := []review.Event{
		event("rec-1", foldBase, "started", map[string]any{"title": "Fix the retry loop"}),
		event("rec-1", foldBase.Add(time.Minute), "telemetry-ping", nil),
	}

	got := review.Fold(events)

	if len(got) != 1 {
		t.Fatalf("Fold returned %d records (%+v), want 1", len(got), got)
	}
	rec := got[0]
	if rec.ID != "rec-1" {
		t.Errorf("record id = %q, want %q", rec.ID, "rec-1")
	}
	if rec.State != review.StateReviewing {
		t.Errorf("state after an unknown event = %v, want the state the started event set (%v)", rec.State, review.StateReviewing)
	}
	if rec.Title != "Fix the retry loop" {
		t.Errorf("title after an unknown event = %q, want %q", rec.Title, "Fix the retry loop")
	}
}

func TestFoldKeepsAdvancingPastAnUnknownEventType(t *testing.T) {
	events := []review.Event{
		event("rec-1", foldBase, "started", map[string]any{"title": "Fix the retry loop"}),
		event("rec-1", foldBase.Add(time.Minute), "telemetry-ping", nil),
		event("rec-1", foldBase.Add(2*time.Minute), "drafted", nil),
	}

	got := review.Fold(events)

	rec := recordByID(t, got, "rec-1")
	if rec.State != review.StateDrafted {
		t.Errorf("state = %v, want %v", rec.State, review.StateDrafted)
	}
}

func TestFoldOrdersRecordsByFirstAppearance(t *testing.T) {
	events := []review.Event{
		event("rec-2", foldBase, "started", nil),
		event("rec-1", foldBase.Add(time.Minute), "started", nil),
		event("rec-2", foldBase.Add(2*time.Minute), "drafted", nil),
		event("rec-3", foldBase.Add(3*time.Minute), "started", nil),
		event("rec-1", foldBase.Add(4*time.Minute), "submitted", nil),
	}
	want := []string{"rec-2", "rec-1", "rec-3"}

	got := recordIDs(review.Fold(events))

	if !slices.Equal(got, want) {
		t.Errorf("Fold returned record ids %q, want %q", got, want)
	}
}

func TestFoldTracksRecordsIndependently(t *testing.T) {
	events := []review.Event{
		event("rec-1", foldBase, "started", map[string]any{"title": "Fix the retry loop"}),
		event("rec-2", foldBase.Add(time.Minute), "started", map[string]any{"title": "Add the index watcher"}),
		event("rec-1", foldBase.Add(2*time.Minute), "archived", nil),
	}

	got := review.Fold(events)

	if len(got) != 2 {
		t.Fatalf("Fold returned %d records (%+v), want 2", len(got), got)
	}
	first := recordByID(t, got, "rec-1")
	second := recordByID(t, got, "rec-2")
	if first.State != review.StateArchived {
		t.Errorf("rec-1 state = %v, want %v", first.State, review.StateArchived)
	}
	if second.State != review.StateReviewing {
		t.Errorf("rec-2 state = %v, want %v", second.State, review.StateReviewing)
	}
	if second.Title != "Add the index watcher" {
		t.Errorf("rec-2 title = %q, want %q", second.Title, "Add the index watcher")
	}
}
