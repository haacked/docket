package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

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

// The screen opens before the draft's summary is read, and the summary fills the
// body when it lands.
func TestOpeningSubmitAimsTheScreenThenFillsTheSummary(t *testing.T) {
	rec := draftedRecord()

	next, cmd := liveApp(rec).Update(msg.OpenSubmit{ID: rec.ID})
	a := next.(App)

	if cmd == nil {
		t.Error("opening the submit screen did not read the draft's summary")
	}
	if a.screen != msg.Submit {
		t.Fatalf("screen = %v, want the submit screen", a.screen)
	}
	if content := a.View().Content; !strings.Contains(content, "haacked/docket#7") {
		t.Errorf("the submit screen does not name the pull request:\n%s", content)
	}

	next, _ = a.Update(draftLoadedMsg{record: rec, body: "Nice fix! No blockers."})

	if got := next.(App).sub.Body.Value(); got != "Nice fix! No blockers." {
		t.Errorf("body = %q, want the draft's summary", got)
	}
}

// The read runs in a command, so it can land after the user left the screen or
// opened another record's.
func TestADraftSummaryForAnotherScreenIsIgnored(t *testing.T) {
	left := draftedRecord()
	open := draftedRecord()
	open.ID = "rec-2"
	open.Ref = pr.Ref{Org: "haacked", Repo: "docket", Number: 9}

	tests := []struct {
		name    string
		message tea.Msg
	}{
		{name: "another record's submit screen", message: msg.OpenSubmit{ID: open.ID}},
		{name: "back on the dashboard", message: msg.Goto{Screen: msg.Dashboard}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			next, _ := liveApp(left, open).Update(msg.OpenSubmit{ID: left.ID})
			next, _ = next.(App).Update(tc.message)

			next, _ = next.(App).Update(draftLoadedMsg{record: left, body: "The summary of the record left behind."})

			if got := next.(App).sub.Body.Value(); got != "" {
				t.Errorf("body = %q, want the late summary dropped", got)
			}
		})
	}
}

// A new draft keeps the record's ID. A read of the old draft that lands after the
// screen reopens would otherwise fill in the old draft's summary.
func TestADraftSummaryForAnOlderDraftOfTheSameRecordIsIgnored(t *testing.T) {
	old := draftedRecord()
	newer := old
	newer.ReviewID = old.ReviewID + 1

	next, _ := liveApp(old).Update(msg.OpenSubmit{ID: old.ID})
	a := next.(App)
	a.dash = a.dash.SetRecords([]review.Record{newer})
	next, _ = a.Update(msg.OpenSubmit{ID: newer.ID})

	next, _ = next.(App).Update(draftLoadedMsg{record: old, body: "The summary of the older draft."})

	if got := next.(App).sub.Body.Value(); got != "" {
		t.Errorf("body = %q, want the older draft's summary dropped", got)
	}
}

// The screen still submits without the summary, because an empty body keeps
// whatever the draft holds. A failed read therefore goes to the status line. It
// does not go through the error path, which clears the dashboard's busy markers.
func TestAFailedReadOfTheSummaryLeavesTheScreenUsable(t *testing.T) {
	rec := draftedRecord()
	next, _ := liveApp(rec).Update(msg.OpenSubmit{ID: rec.ID})
	a := next.(App)
	a.dash.Busy["rec-other"] = "abandoning"

	next, _ = a.Update(draftLoadedMsg{record: rec, err: errors.New("HTTP 502")})
	a = next.(App)

	if a.screen != msg.Submit {
		t.Errorf("screen = %v, want the submit screen still open", a.screen)
	}
	if !strings.Contains(a.status, "HTTP 502") {
		t.Errorf("status = %q, want the failure in it", a.status)
	}
	if a.dash.Busy["rec-other"] == "" {
		t.Error("a failed read of the summary cleared another row's busy marker")
	}
}

// The events endpoint answers a record with no pending review with a 404. The
// dashboard already knows, so it says so instead of asking GitHub.
func TestOpeningSubmitNeedsAPendingReview(t *testing.T) {
	rec := draftedRecord()
	rec.State = review.StateReviewing
	rec.ReviewID = 0

	next, _ := liveApp(rec).Update(msg.OpenSubmit{ID: rec.ID})
	a := next.(App)

	if a.screen == msg.Submit {
		t.Error("the submit screen opened on a record with nothing to submit")
	}
	if !strings.Contains(a.status, "no pending review") {
		t.Errorf("status = %q, want it to say there is no pending review to submit", a.status)
	}
}

// Leaving a drafted row's background session while it works puts the row back to
// reviewing, and its draft is still pending on GitHub.
func TestOpeningSubmitOnAReviewingRowWithADraft(t *testing.T) {
	rec := draftedRecord()
	rec.Mode = review.ModeBackground
	rec.State = review.StateReviewing

	next, _ := liveApp(rec).Update(msg.OpenSubmit{ID: rec.ID})

	if a := next.(App); a.screen != msg.Submit {
		t.Errorf("screen = %v, want the submit screen for the pending review; status = %q", a.screen, a.status)
	}
}

// An interactive reviewing row with a draft id is refused too, because its
// session runs in a terminal and a submit would archive the row out from under
// it. The row does have a pending review, so the message must not say it has
// none.
func TestOpeningSubmitOnAnInteractiveReviewingRowNamesTheOpenSession(t *testing.T) {
	rec := draftedRecord()
	rec.Mode = review.ModeInteractive
	rec.State = review.StateReviewing

	next, _ := liveApp(rec).Update(msg.OpenSubmit{ID: rec.ID})
	a := next.(App)

	if a.screen == msg.Submit {
		t.Error("the submit screen opened on a row whose interactive session is open")
	}
	if strings.Contains(a.status, "no pending review") {
		t.Errorf("status = %q, want it to say the pending review exists but its session is open", a.status)
	}
	if !strings.Contains(a.status, "session") {
		t.Errorf("status = %q, want it to mention the open session", a.status)
	}
}

// The poll keeps reading GitHub while the submit screen is open. When the
// session posts a newer draft, ctrl+s must not send the user's event to a draft
// they have not read.
func TestSubmittingADraftThePollReplacedIsRefused(t *testing.T) {
	rec := draftedRecord()
	rec.Mode = review.ModeBackground
	rec.State = review.StateReviewing
	next, _ := liveApp(rec).Update(msg.OpenSubmit{ID: rec.ID})
	a := next.(App)
	replaced := rec
	replaced.State = review.StateDrafted
	replaced.ReviewID = rec.ReviewID + 1
	a.dash = a.dash.SetRecords([]review.Record{replaced})

	next, cmd := a.Update(msg.SubmitReview{ID: rec.ID, ReviewID: rec.ReviewID, Event: review.EventComment})
	a = next.(App)

	if cmd != nil {
		t.Error("the submit went ahead for a draft the user has not read")
	}
	if a.dash.Busy[rec.ID] != "" {
		t.Errorf("busy = %q, want the row left alone", a.dash.Busy[rec.ID])
	}
	if a.screen != msg.Dashboard {
		t.Errorf("screen = %v, want the dashboard, where s opens the newer draft", a.screen)
	}
	if !strings.Contains(a.status, "changed") {
		t.Errorf("status = %q, want it to say the pending review changed", a.status)
	}
}

// An archived row keeps the id of the review it submitted. It has no session
// and no pending review. The refusal must therefore not send the user to close
// one.
func TestOpeningSubmitOnAnArchivedRowSaysThereIsNothingPending(t *testing.T) {
	rec := draftedRecord()
	rec.State = review.StateArchived

	next, _ := liveApp(rec).Update(msg.OpenSubmit{ID: rec.ID})
	a := next.(App)

	if a.screen == msg.Submit {
		t.Error("the submit screen opened on an archived row")
	}
	if strings.Contains(a.status, "session") {
		t.Errorf("status = %q, want it to say there is no pending review rather than name a session", a.status)
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
			message: msg.SubmitReview{ID: rec.ID, ReviewID: rec.ReviewID, Event: review.EventComment},
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

// A resize that lands while help is on top of the notes reaches the notes pane
// once help closes back to it, rather than leaving it wrapped to a stale width
// until the next resize.
func TestGotoBackToNotesRefitsAResizeThatLandedWhileHelpWasOnTop(t *testing.T) {
	rec := draftedRecord()
	a := liveApp(rec)
	next, _ := a.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	next, _ = next.(App).Update(msg.OpenNotes{ID: rec.ID})
	next, _ = next.(App).Update(msg.OpenHelp{})
	a = next.(App)
	if a.screen != msg.Help {
		t.Fatal("OpenHelp did not open help")
	}

	next, _ = a.Update(tea.WindowSizeMsg{Width: 60, Height: 40})
	next, _ = next.(App).Update(msg.Goto{Screen: msg.Notes})
	a = next.(App)

	if got, want := a.notes.Viewport.Width(), 60-2*marginX; got != want {
		t.Errorf("notes viewport width = %d, want %d after the resize that landed while help was open", got, want)
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

// The archived toggle's label is built at runtime and slotted into the
// table-derived footer between the static entries and the trailing ? / q,
// where "a" sits in help.Dashboard. This pins that position rather than
// just its presence.
func TestTheDashboardFooterPlacesTheArchivedToggleBeforeHelpAndQuit(t *testing.T) {
	want :=
		"n new · i requests · enter resume · s submit · v notes · o github · c ask · u re-review · x abandon · r refresh · R refresh all · a show archived · ? help · q quit"
	if got := footerOn(msg.Dashboard, false); got != want {
		t.Errorf("footer = %q, want %q", got, want)
	}

	want = strings.Replace(want, "a show archived", "a hide archived", 1)
	if got := footerOn(msg.Dashboard, true); got != want {
		t.Errorf("footer with archived shown = %q, want %q", got, want)
	}
}

// The root cuts the requests screen's header at the terminal's width. The
// footer wraps instead, so the drafts toggle goes there.
func TestTheRequestsFooterPlacesTheDraftsToggleBeforeHelpAndBack(t *testing.T) {
	a := app()
	a.screen, a.width = msg.Requests, 400
	want := "space mark · enter start · o github · r refresh · t teams · d show drafts · ? help · esc back · ctrl+c quit"
	if got := ansi.Strip(a.footer()); got != want {
		t.Errorf("footer = %q, want %q", got, want)
	}

	a.reqs.ShowDrafts = true
	want = strings.Replace(want, "d show drafts", "d hide drafts", 1)
	if got := ansi.Strip(a.footer()); got != want {
		t.Errorf("footer with drafts shown = %q, want %q", got, want)
	}
}

func TestTheFooterNamesTheKeysOfEachScreen(t *testing.T) {
	for screen, want := range map[msg.Screen][]string{
		msg.Dashboard: {"s submit", "notes", "? help"},
		msg.Submit:    {"ctrl+s submit", "tab event", "esc back"},
		msg.Notes:     {"e edit", "o github", "esc/q back", "? help"},
		msg.Help:      {"esc/? back", "ctrl+c quit"},
	} {
		got := footerOn(screen, false)

		for _, part := range want {
			if !strings.Contains(got, part) {
				t.Errorf("the footer for screen %v is %q, want %q in it", screen, got, part)
			}
		}
	}
}

// footerOn is the footer a screen draws on a terminal wide enough to hold it on
// one line, without the styles that set keys apart from their labels.
func footerOn(screen msg.Screen, showArchived bool) string {
	a := app()
	a.screen, a.width = screen, 400
	a.dash.ShowArchived = showArchived
	return ansi.Strip(a.footer())
}
