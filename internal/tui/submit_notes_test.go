package tui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/haacked/docket/internal/core/config"
	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/core/session"
	"github.com/haacked/docket/internal/core/tier"
	"github.com/haacked/docket/internal/tui/msg"
)

// draftedRecord is a review waiting to be submitted: review-code left a pending
// review on GitHub and the session is over.
func draftedRecord() review.Record {
	return review.Record{
		ID:        "rec-1",
		Ref:       pr.Ref{Org: "haacked", Repo: "docket", Number: 7},
		URL:       "https://github.com/haacked/docket/pull/7",
		Title:     "Add a thing",
		Author:    "someone",
		State:     review.StateDrafted,
		Tier:      tier.Tier2,
		Dir:       "/tmp/clones/haacked/docket/pr-7",
		ReviewID:  4321,
		NotesPath: "/opt/review-code/.reviews/haacked/docket/pr-7.md",
	}
}

// liveApp holds a real service so the root reads the login the way production
// does. Service.Login caches into Cfg on the first GitHub read, and the startup
// copy the root also holds stays empty on an install whose config.toml names no
// user, so the service's copy is the one that is current. None of these tests
// runs a command, so a service with nothing but its config is enough.
func liveApp(records ...review.Record) App {
	cfg := config.Config{DefaultEngine: "claude", GitHubUser: "haacked"}
	a := New(&session.Service{Cfg: cfg}, config.Config{DefaultEngine: "claude"}, "", false)
	a.dash = a.dash.SetRecords(records)
	return a
}

func TestOpeningSubmitAimsTheScreenAtTheRecord(t *testing.T) {
	rec := draftedRecord()

	next, cmd := liveApp(rec).Update(msg.OpenSubmit{ID: rec.ID})
	a := next.(App)

	if cmd != nil {
		t.Errorf("opening the submit screen ran %#v", cmd())
	}
	if a.screen != msg.Submit {
		t.Fatalf("screen = %v, want the submit screen", a.screen)
	}
	if content := a.View().Content; !strings.Contains(content, "haacked/docket#7") {
		t.Errorf("the submit screen does not name the pull request:\n%s", content)
	}
}

// The events endpoint answers a record with no pending review with a 404. The
// dashboard already knows, so it says so instead of asking GitHub.
func TestOpeningSubmitNeedsAPendingReview(t *testing.T) {
	rec := draftedRecord()
	rec.State = review.StateReviewing

	next, _ := liveApp(rec).Update(msg.OpenSubmit{ID: rec.ID})
	a := next.(App)

	if a.screen == msg.Submit {
		t.Error("the submit screen opened on a record with nothing to submit")
	}
	if !strings.Contains(a.status, "drafted") {
		t.Errorf("status = %q, want it to say only a drafted review can be submitted", a.status)
	}
}

// Approving your own pull request is a 422, so the choice never reaches the
// screen.
func TestOpeningSubmitOnMyOwnPullRequestOffersNoApproval(t *testing.T) {
	rec := draftedRecord()
	rec.Author = "haacked"

	next, _ := liveApp(rec).Update(msg.OpenSubmit{ID: rec.ID})
	a := next.(App)

	if slices.Contains(a.sub.Events, review.EventApprove) {
		t.Errorf("events = %v, want no approval of my own pull request", a.sub.Events)
	}
}

func TestADryRunSubmitsNothingAndOpensNoEditor(t *testing.T) {
	rec := draftedRecord()

	tests := []struct {
		name    string
		message tea.Msg
		want    []string
	}{
		{
			name:    "submit",
			message: msg.SubmitReview{ID: rec.ID, Event: review.EventComment},
			want:    []string{"Would submit", "haacked/docket#7", review.EventComment},
		},
		{
			name:    "edit the notes",
			message: msg.EditNotes{ID: rec.ID},
			want:    []string{"Would open", rec.NotesPath},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			next, cmd := dryRunApp(rec).Update(tc.message)
			if cmd != nil {
				t.Errorf("%s ran a command in a dry run: %#v", tc.name, cmd())
			}
			for _, want := range tc.want {
				if got := next.(App).status; !strings.Contains(got, want) {
					t.Errorf("status = %q, want %q in it", got, want)
				}
			}
		})
	}
}

// Pressing v opens the pane on the record before the file has been read, so the
// header is this record's rather than the last one's while the load is in
// flight. The markdown arrives separately.
func TestOpeningTheNotesAimsThePaneThenShowsTheFile(t *testing.T) {
	rec := draftedRecord()
	a := liveApp(rec)

	next, cmd := a.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	next, cmd = next.(App).Update(msg.OpenNotes{ID: rec.ID})
	a = next.(App)

	if cmd == nil {
		t.Fatal("opening the notes read nothing")
	}
	if a.screen != msg.Notes {
		t.Fatalf("screen = %v, want the notes screen", a.screen)
	}
	content := a.View().Content
	if !strings.Contains(content, "haacked/docket#7") || !strings.Contains(content, rec.NotesPath) {
		t.Errorf("the pane does not name the record before the file lands:\n%s", content)
	}

	next, _ = a.Update(notesLoadedMsg{record: rec, markdown: "The compaction drops its lock partway through.\n"})

	if content := next.(App).View().Content; !strings.Contains(content, "compaction") {
		t.Errorf("the loaded notes are not shown:\n%s", content)
	}
}

// The read runs in a command, so a slow one can land after the user opened
// another record. The pane keeps showing the record they asked for.
func TestNotesLandingForAnotherRecordAreIgnored(t *testing.T) {
	left := draftedRecord()
	open := draftedRecord()
	open.ID = "rec-2"
	open.Ref = pr.Ref{Org: "haacked", Repo: "docket", Number: 9}
	open.NotesPath = "/opt/review-code/.reviews/haacked/docket/pr-9.md"

	a := liveApp(left, open)
	next, _ := a.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	next, _ = next.(App).Update(msg.OpenNotes{ID: open.ID})
	a = next.(App)

	next, _ = a.Update(notesLoadedMsg{record: left, markdown: "Departed, the notes of the record left behind.\n"})
	a = next.(App)

	if a.notes.Record.ID != open.ID {
		t.Errorf("the pane is aimed at %q, want the record the user opened", a.notes.Record.ID)
	}
	if content := a.View().Content; strings.Contains(content, "Departed") {
		t.Errorf("the other record's notes are shown:\n%s", content)
	}

	next, _ = a.Update(notesLoadedMsg{record: open, markdown: "Arrived, the notes of the record on screen.\n"})

	if content := next.(App).View().Content; !strings.Contains(content, "Arrived") {
		t.Errorf("the notes the user asked for are not shown:\n%s", content)
	}
}

// The pane draws nothing until it knows how much room it has, and a
// WindowSizeMsg follows every return from a child process.
func TestAResizeReachesTheNotesPane(t *testing.T) {
	rec := draftedRecord()
	a := liveApp(rec)
	a.screen = msg.Notes
	a.notes = a.notes.SetNotes(rec, "The compaction drops its lock partway through.\n", false)

	next, _ := a.Update(tea.WindowSizeMsg{Width: 100, Height: 40})

	if content := next.(App).View().Content; !strings.Contains(content, "compaction") {
		t.Errorf("the notes are not shown after the window was sized:\n%s", content)
	}
}

// An editor is not a review session. Reading GitHub after it would mark the row
// busy and measure a detection window the editor never moved.
func TestAnEditorExitRereadsTheNotesInsteadOfGitHub(t *testing.T) {
	rec := draftedRecord()
	a := liveApp(rec)
	a.screen = msg.Notes

	next, cmd := a.Update(editorExitedMsg{record: rec})
	a = next.(App)

	if cmd == nil {
		t.Fatal("the editor exit re-read nothing, so an edit is invisible until the screen is reopened")
	}
	if a.screen != msg.Notes {
		t.Errorf("screen = %v, want the notes still open", a.screen)
	}
	if len(a.dash.Busy) != 0 {
		t.Errorf("the editor exit marked rows busy: %v", a.dash.Busy)
	}
}

// A submitted review closes the record, so its screen has nothing left on it.
func TestASubmittedReviewLeavesTheSubmitScreen(t *testing.T) {
	rec := draftedRecord()
	a := liveApp(rec)
	a.screen = msg.Submit
	a.sub = a.sub.For(rec, review.SubmitEvents)

	archived := rec
	archived.State = review.StateArchived
	next, _ := a.Update(detectedMsg{record: archived, submitted: true})
	a = next.(App)

	if a.screen != msg.Dashboard {
		t.Errorf("screen = %v, want the dashboard", a.screen)
	}
	if a.sub.Busy != "" {
		t.Errorf("the submit screen is still busy (%q), so the next submission is dead", a.sub.Busy)
	}
}

// A failed submission leaves the user on the screen they can retry from.
func TestAFailedSubmissionLeavesTheSubmitScreenUsable(t *testing.T) {
	rec := draftedRecord()
	a := liveApp(rec)
	a.screen = msg.Submit
	a.sub = a.sub.For(rec, review.SubmitEvents)
	a.sub, _ = a.sub.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if a.sub.Busy == "" {
		t.Fatal("the submit screen did not mark itself busy")
	}

	next, _ := a.Update(errMsg{err: errNotFound})
	a = next.(App)

	if a.sub.Busy != "" {
		t.Error("the submit screen is left busy after a failure, which ignores the key that retries it")
	}
}

func TestTheFooterNamesTheKeysOfEachScreen(t *testing.T) {
	for screen, want := range map[msg.Screen][]string{
		msg.Dashboard: {"s submit", "notes"},
		msg.Submit:    {"ctrl+s submit", "tab event", "esc back"},
		msg.Notes:     {"e edit", "esc back"},
	} {
		got := helpFor(screen, false)

		for _, part := range want {
			if !strings.Contains(got, part) {
				t.Errorf("the footer for screen %v is %q, want %q in it", screen, got, part)
			}
		}
	}
}
