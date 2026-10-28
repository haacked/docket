package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/haacked/docket/internal/core/config"
	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/review"
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
		found:  review.Found{NotesAt: notesAt, PendingID: 12},
	})
	got := next.(App)

	if got.screen != msg.NewReview {
		t.Errorf("screen = %v, want the new review screen", got.screen)
	}
	found := got.newrev.Existing
	if found == nil {
		t.Fatal("the screen is not asking what to do with the review")
	}
	if found.Ref != "haacked/docket#4" || found.Engine != "codex" || !found.NotesAt.Equal(notesAt) || found.PendingID != 12 || found.Submitted {
		t.Errorf("existing = %+v", *found)
	}
	if got.newrev.Busy != "" {
		t.Errorf("busy = %q, want the screen ready for a choice", got.newrev.Busy)
	}
	if view := ansi.Strip(got.View().Content); strings.Contains(view, "enter start") || strings.Contains(view, "tab engine") {
		t.Errorf("the footer offers keys the choice ignores:\n%s", view)
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

func TestARecordInProgressRefusesReReview(t *testing.T) {
	for _, state := range []review.State{review.StateReviewing, review.StatePreparing} {
		rec := backgroundRecord(state)

		next, cmd := withRecords(rec).Update(msg.OpenRereview{ID: rec.ID})
		got := next.(App)
		if cmd != nil {
			t.Errorf("%s: re-review ran a command against a record still in progress", state)
		}
		if got.screen != msg.Dashboard || !strings.Contains(got.status, "is still "+string(state)) {
			t.Errorf("%s: screen = %v, status = %q", state, got.screen, got.status)
		}
	}
}

// withNotes is an adopted record whose notes exist, so enter opens a session
// about them.
func withNotes(t *testing.T) review.Record {
	t.Helper()
	rec := adopted()
	rec.Dir = t.TempDir()
	rec.NotesPath = filepath.Join(rec.Dir, "pr-4.md")
	if err := os.WriteFile(rec.NotesPath, []byte("# Review\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return rec
}

// A record with no review session has nothing for enter to resume, so enter
// opens a session about its notes.
func TestEnterOnARecordWithNoReviewSessionOpensTheNotes(t *testing.T) {
	svc, _ := serviceOverIndex(t)
	rec := withNotes(t)
	a := New(svc, config.Config{DefaultEngine: "claude"}, "", false)
	a.dash = a.dash.SetRecords([]review.Record{rec})

	next, cmd := a.Update(msg.Resume{ID: rec.ID})
	if note := next.(App).dash.Busy[rec.ID]; note != "opening" {
		t.Errorf("busy = %q, want opening", note)
	}
	got := drain(cmd)
	if len(got) != 1 {
		t.Fatalf("enter sent %#v, want one launch", got)
	}
	launch, ok := got[0].(launchMsg)
	if !ok {
		t.Fatalf("enter sent %#v, want a launch", got[0])
	}
	if launch.kind != launchAsk {
		t.Errorf("launch kind = %v, want the Q&A session", launch.kind)
	}
}

// The dashboard stays live while the session's command builds. A second enter
// in that window would start a second session and overwrite the first one's id.
func TestASecondEnterWhileTheNotesSessionOpensIsIgnored(t *testing.T) {
	rec := withNotes(t)
	a := withRecords(rec)
	a.dash.Busy[rec.ID] = "opening"

	next, cmd := a.Update(msg.Resume{ID: rec.ID})

	if len(drain(cmd)) != 0 {
		t.Error("a second enter ran a command while the first was opening")
	}
	if want := rec.Ref.String() + " is still opening"; next.(App).status != want {
		t.Errorf("status = %q, want %q", next.(App).status, want)
	}
}

// An ask session's exit captures the Q&A session before it reads GitHub, and a
// review session's exit reads GitHub only. Each needs its own message.
func TestAnAskSessionExitTakesItsOwnPath(t *testing.T) {
	rec := adopted()

	if _, ok := exited(launchMsg{record: rec, kind: launchAsk}, nil).(askExitedMsg); !ok {
		t.Error("an ask session's exit went down the review's path")
	}
	if _, ok := exited(launchMsg{record: rec}, nil).(childExitedMsg); !ok {
		t.Error("a review session's exit would not read GitHub")
	}
	if _, ok := exited(launchMsg{record: rec, kind: launchEditor}, nil).(editorExitedMsg); !ok {
		t.Error("an editor's exit would not re-read the notes")
	}

	next, _ := withRecords(rec).Update(askExitedMsg{record: rec})
	if note := next.(App).dash.Busy[rec.ID]; note != "reading GitHub" {
		t.Errorf("busy = %q, want reading GitHub", note)
	}
}

// View and ask is prepared the way a review is. A review launch would pass
// review-code no --append or --overwrite, so the root has to open the Q&A
// session instead.
func TestAPreparedAskOpensTheQuestionSession(t *testing.T) {
	svc, _ := serviceOverIndex(t)
	rec := withNotes(t)
	a := New(svc, config.Config{DefaultEngine: "claude"}, "", false)

	_, cmd := a.Update(preparedMsg{record: rec})
	if cmd == nil {
		t.Fatal("a prepared ask ran nothing")
	}
	launch, ok := cmd().(launchMsg)
	if !ok {
		t.Fatal("a prepared ask did not launch")
	}
	if launch.kind != launchAsk {
		t.Errorf("launch kind = %v, want the Q&A session", launch.kind)
	}
}

// A row with no review session gets a hint that matches what enter opens.
func TestDescribeARecordWithNoReviewSession(t *testing.T) {
	rec := withNotes(t)
	rec.State = review.StateDrafted
	if got := describe(rec); !strings.Contains(got, "enter to ask") {
		t.Errorf("describe(drafted) = %q", got)
	}

	rec.Intent = review.IntentReview
	rec.Engine = "codex"
	rec.State = review.StateUnreviewed
	if got := describe(rec); !strings.Contains(got, "enter to ask about the notes") {
		t.Errorf("describe(unreviewed) = %q", got)
	}

	rec.SessionID = "019a"
	if got := describe(rec); !strings.Contains(got, "enter to resume it") {
		t.Errorf("describe(unreviewed with a session) = %q", got)
	}
}

// A dry run reports the command enter would run and records nothing.
func TestADryRunEnterReportsTheNotesSession(t *testing.T) {
	svc, paths := serviceOverIndex(t)
	rec := withNotes(t)
	a := New(svc, config.Config{DefaultEngine: "claude"}, "", true)
	a.dash = a.dash.SetRecords([]review.Record{rec})

	_, cmd := a.Update(msg.Resume{ID: rec.ID})
	got := drain(cmd)
	if len(got) != 1 {
		t.Fatalf("enter sent %#v, want one status", got)
	}
	status, ok := got[0].(statusMsg)
	if !ok || !strings.Contains(status.text, rec.NotesPath) {
		t.Errorf("enter sent %#v, want the command that reads the notes", got[0])
	}
	if info, err := os.Stat(paths.Index); err == nil && info.Size() > 0 {
		t.Error("a dry run wrote to the index")
	}
}

func TestDescribeAnAdoptedRecord(t *testing.T) {
	rec := adopted()
	if got := describe(rec); !strings.Contains(got, "enter to ask") {
		t.Errorf("describe(reviewed) = %q", got)
	}
	rec.State = review.StateDrafted
	if got := describe(rec); !strings.Contains(got, "s to submit") {
		t.Errorf("describe(adopted draft) = %q", got)
	}
}
