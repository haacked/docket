package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/core/session"
	"github.com/haacked/docket/internal/tui/msg"
)

func adopted() review.Record {
	return review.Record{
		ID:     "rec-1",
		Ref:    pr.Ref{Org: "haacked", Repo: "docket", Number: 4},
		Engine: "claude",
		State:  review.StateReviewed,
		Intent: review.IntentAsk,
	}
}

func withRecords(records ...review.Record) App {
	a := app()
	a.dash = a.dash.SetRecords(records)
	return a
}

func TestAnExistingReviewSwitchesTheScreenToItsChoice(t *testing.T) {
	a := app()
	a.screen = msg.NewReview
	a.newrev.Busy = "resolving"
	notesAt := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)

	next, _ := a.Update(existingMsg{
		ref:    pr.Ref{Org: "haacked", Repo: "docket", Number: 4},
		engine: "codex",
		found:  session.Found{NotesAt: notesAt, PendingID: 12},
	})
	got := next.(App)

	if got.screen != msg.NewReview {
		t.Errorf("screen = %v, want the new review screen", got.screen)
	}
	found := got.newrev.Existing
	if found == nil {
		t.Fatal("the screen is not asking what to do with the review")
	}
	if found.Ref != "haacked/docket#4" || found.Engine != "codex" || !found.NotesAt.Equal(notesAt) || !found.Pending || found.Submitted {
		t.Errorf("existing = %+v", *found)
	}
	if got.newrev.Busy != "" {
		t.Errorf("busy = %q, want the screen ready for a choice", got.newrev.Busy)
	}
}

func TestReReviewOpensTheChoiceForTheRecord(t *testing.T) {
	rec := adopted()
	rec.Engine = "codex"

	next, _ := withRecords(rec).Update(msg.OpenRereview{ID: rec.ID})
	got := next.(App)

	if got.screen != msg.NewReview {
		t.Fatalf("screen = %v, want the new review screen", got.screen)
	}
	found := got.newrev.Existing
	if found == nil || found.RecordID != rec.ID || found.Engine != "codex" || found.CanAsk() {
		t.Errorf("existing = %+v, want a re-review of the record under its own engine", found)
	}
}

func TestARunningBackgroundReviewRefusesAskAndReReview(t *testing.T) {
	rec := backgroundRecord(review.StateReviewing)

	for _, message := range []any{msg.Ask{ID: rec.ID}, msg.OpenRereview{ID: rec.ID}} {
		next, cmd := withRecords(rec).Update(message)
		got := next.(App)
		if cmd != nil {
			t.Errorf("%T ran a command against a review still running", message)
		}
		if got.screen != msg.Dashboard || !strings.Contains(got.status, "still being reviewed") {
			t.Errorf("%T: screen = %v, status = %q", message, got.screen, got.status)
		}
	}
}

// The dashboard stays live while the ask command builds. A second c in that
// window would start a second session and overwrite the first one's id.
func TestASecondAskWhileTheFirstOpensIsIgnored(t *testing.T) {
	rec := adopted()
	a := withRecords(rec)
	a.dash.Busy[rec.ID] = "opening"

	if _, cmd := a.Update(msg.Ask{ID: rec.ID}); cmd != nil {
		t.Error("a second ask ran a command while the first was opening")
	}
}

func TestAnAskSessionExitSkipsDetection(t *testing.T) {
	rec := adopted()

	if _, ok := exited(launchMsg{record: rec, kind: launchAsk}, nil).(askExitedMsg); !ok {
		t.Error("an ask session's exit would read GitHub")
	}
	if _, ok := exited(launchMsg{record: rec}, nil).(childExitedMsg); !ok {
		t.Error("a review session's exit would not read GitHub")
	}
	if _, ok := exited(launchMsg{record: rec, kind: launchEditor}, nil).(editorExitedMsg); !ok {
		t.Error("an editor's exit would not re-read the notes")
	}

	next, _ := withRecords(rec).Update(askExitedMsg{record: rec})
	if note := next.(App).dash.Busy[rec.ID]; note != "saving" {
		t.Errorf("busy = %q, want saving rather than reading GitHub", note)
	}
}

func TestDescribeAnAdoptedRecord(t *testing.T) {
	rec := adopted()
	if got := describe(rec); !strings.Contains(got, "c to ask") {
		t.Errorf("describe(reviewed) = %q", got)
	}
	rec.State = review.StateDrafted
	if got := describe(rec); !strings.Contains(got, "s to submit") {
		t.Errorf("describe(adopted draft) = %q", got)
	}
}
