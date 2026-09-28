package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/review"
)

// bgListing is `claude agents --json --all`. The helpers below set the state,
// the status, and the process in it, which is all the poll reads. bgListing
// reports a working session busy and any other idle. idleListing covers a
// working session that has ended its turn.
func bgListing(id, sessionID, state string, live bool) string {
	status := "idle"
	if state == "working" {
		status = "busy"
	}
	return bgEntry(id, sessionID, state, status, live)
}

// idleListing is a session that ended its turn without asking anything. claude
// keeps its working state and reports it idle.
func idleListing(id, sessionID string) string {
	return bgEntry(id, sessionID, "working", "idle", true)
}

func bgEntry(id, sessionID, state, status string, live bool) string {
	pid := ""
	if live {
		pid = `"pid": 4242,`
	}
	return `[{` + pid + `"id": "` + id + `", "kind": "background",
		"sessionId": "` + sessionID + `", "state": "` + state + `", "status": "` + status + `"}]`
}

const bgSession = "6d681a76-7638-4a56-a4cb-bc0353e547d5"

// bgRunner answers the two commands a background review drives: the start, which
// reports the short id, and the listing, which says how the session is doing.
func bgRunner(listing string) *exec.Fake {
	return &exec.Fake{Results: map[string]exec.Result{
		"--bg":   {Stdout: "backgrounded · 6d681a76\n"},
		"agents": {Stdout: listing},
	}}
}

// startedBackground is a tier-2 review already running in the background.
func startedBackground(t *testing.T, svc *Service, runner *exec.Fake) review.Record {
	t.Helper()
	rec, err := launchBackground(t, svc, runner)
	if err != nil {
		t.Fatalf("StartBackground: %v", err)
	}
	return rec
}

// launchBackground prepares a tier-2 background review and starts it against
// runner, returning what the start returned.
func launchBackground(t *testing.T, svc *Service, runner *exec.Fake) (review.Record, error) {
	t.Helper()
	svc.Runner = runner
	rec, _, err := svc.Prepare(context.Background(), unlisted, "claude", review.ModeBackground, review.IntentReview)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	return svc.StartBackground(context.Background(), rec)
}

// claude refuses the --session-id a background start passes and mints its own,
// so a record that carried one would name a session that never existed.
func TestPrepareMintsNoSessionIdForABackgroundReview(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())

	rec, _, err := svc.Prepare(context.Background(), unlisted, "claude", review.ModeBackground, review.IntentReview)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if rec.SessionID != "" {
		t.Errorf("session id = %q, want none until claude reports one", rec.SessionID)
	}
	if rec.Mode != review.ModeBackground {
		t.Errorf("mode = %q", rec.Mode)
	}
}

func TestStartBackgroundRecordsTheIdClaudeReported(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := startedBackground(t, svc, bgRunner(bgListing("6d681a76", bgSession, "working", true)))

	if rec.BGID != "6d681a76" {
		t.Errorf("background id = %q", rec.BGID)
	}
	if rec.State != review.StateReviewing {
		t.Errorf("state = %q, want the review running", rec.State)
	}

	stored, err := svc.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].BGID != "6d681a76" {
		t.Errorf("the index did not keep the background id: %+v", stored)
	}
}

// The record has to exist before the agent does. A docket that dies in between
// otherwise leaves a session running that nothing knows about.
func TestStartBackgroundRecordsTheReviewBeforeLaunchingIt(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	svc.Runner = &exec.Fake{Errs: map[string]error{"--bg": errors.New("claude is not logged in")}}

	rec, _, err := svc.Prepare(context.Background(), unlisted, "claude", review.ModeBackground, review.IntentReview)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if _, err := svc.StartBackground(context.Background(), rec); err == nil {
		t.Fatal("StartBackground hid a failing launch")
	}

	stored, err := svc.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 {
		t.Fatalf("got %d records, want the one that failed to launch", len(stored))
	}
	if !strings.Contains(stored[0].Err, "not logged in") {
		t.Errorf("the row does not say why it failed: %q", stored[0].Err)
	}
}

// A start that never reported an id leaves no session to wait on. The record
// must not read as a running one, or the tick would spin over it for ever.
func TestAStartThatReportedNoIdIsNotWaitedOn(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	runner := &exec.Fake{
		Errs:    map[string]error{"--bg": errors.New("claude is not logged in")},
		Results: map[string]exec.Result{"agents": {Stdout: "[]"}},
	}
	svc.Runner = runner

	rec, _, _ := svc.Prepare(context.Background(), unlisted, "claude", review.ModeBackground, review.IntentReview)
	rec, _ = svc.StartBackground(context.Background(), rec)

	if rec.BackgroundRunning() {
		t.Error("a record with no background id is being waited on")
	}
	records, statuses, err := svc.PollBackground(context.Background())
	if err != nil {
		t.Fatalf("PollBackground: %v", err)
	}
	if records[0].State == review.StateReviewing {
		t.Error("a start that launched nothing is still waiting to finish")
	}
	if len(statuses) != 0 {
		t.Errorf("a session that was never started reported a status: %+v", statuses)
	}
}

func TestPollLearnsTheSessionIdAndKeepsWaiting(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := startedBackground(t, svc, bgRunner(bgListing("6d681a76", bgSession, "working", true)))
	ghc.reviews = []review.GHReview{myPending()}
	before := ghc.reads

	records, statuses, err := svc.PollBackground(context.Background())
	if err != nil {
		t.Fatalf("PollBackground: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records", len(records))
	}
	if records[0].State != review.StateReviewing {
		t.Errorf("state = %q, want the review still running", records[0].State)
	}
	// A busy session has nothing on GitHub to find yet.
	if got := ghc.reads - before; got != 0 {
		t.Errorf("read GitHub %d times for a busy session, want none", got)
	}
	if records[0].SessionID != bgSession {
		t.Errorf("session id = %q, want the one claude reported", records[0].SessionID)
	}
	if status, ok := statuses[rec.ID]; !ok || status.State != "working" {
		t.Errorf("status = %+v, want the state claude reported", status)
	}
}

// The status is for the screen, not the log. A poll every few seconds that
// appended each time would grow the index without recording anything new.
func TestPollWritesOnlyWhenSomethingChanged(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	startedBackground(t, svc, bgRunner(bgListing("6d681a76", bgSession, "working", true)))

	events := func() int {
		t.Helper()
		loaded, err := svc.Store.Events()
		if err != nil {
			t.Fatal(err)
		}
		return len(loaded)
	}

	if _, _, err := svc.PollBackground(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := events()
	for range 3 {
		if _, _, err := svc.PollBackground(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if events() != after {
		t.Errorf("polling an unchanged session wrote %d more events", events()-after)
	}
}

func TestPollDetectsTheReviewOnceTheSessionIsDone(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo(), reviews: []review.GHReview{
		{ID: 55, State: "PENDING", User: struct {
			Login string `json:"login"`
		}{Login: "haacked"}},
	}}
	svc, _ := newService(t, ghc, newFakeGit())
	startedBackground(t, svc, bgRunner(bgListing("6d681a76", bgSession, "done", true)))

	records, statuses, err := svc.PollBackground(context.Background())
	if err != nil {
		t.Fatalf("PollBackground: %v", err)
	}
	if records[0].State != review.StateDrafted {
		t.Errorf("state = %q, want the pending review detected", records[0].State)
	}
	if records[0].ReviewID != 55 {
		t.Errorf("review id = %d", records[0].ReviewID)
	}
	// A finished session is no longer running, so the screen shows no status and
	// the tick has nothing left to wait on.
	if len(statuses) != 0 {
		t.Errorf("a finished session still reported a status: %+v", statuses)
	}
}

// A session the user deleted from outside docket is gone from the listing. A
// record that waited for it would wait forever.
func TestPollDetectsASessionTheAgentNoLongerKnows(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	runner := bgRunner(bgListing("6d681a76", bgSession, "working", true))
	startedBackground(t, svc, runner)

	runner.Results["agents"] = exec.Result{Stdout: "[]"}
	records, _, err := svc.PollBackground(context.Background())
	if err != nil {
		t.Fatalf("PollBackground: %v", err)
	}
	if records[0].State != review.StateUnreviewed {
		t.Errorf("state = %q, want the record moved off running", records[0].State)
	}
}

// The agent is the only thing that can say how its sessions are doing, so a
// listing that fails leaves the records alone and the next tick asks again.
func TestAFailedListingLeavesTheRecordsRunning(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	runner := bgRunner(bgListing("6d681a76", bgSession, "working", true))
	startedBackground(t, svc, runner)

	runner.Errs = map[string]error{"agents": errors.New("claude is not on PATH")}
	records, _, err := svc.PollBackground(context.Background())
	if err == nil {
		t.Fatal("PollBackground hid a failing listing")
	}
	if records[0].State != review.StateReviewing {
		t.Errorf("state = %q, want the record left running", records[0].State)
	}
}

// claude holds a background session after its turn ends and refuses a plain
// resume while it does, saying to attach instead.
func TestOpenAttachesWhileTheAgentHoldsTheSession(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := startedBackground(t, svc, bgRunner(bgListing("6d681a76", bgSession, "done", true)))

	spec, err := svc.OpenBackgroundSpec(context.Background(), rec)
	if err != nil {
		t.Fatalf("OpenBackgroundSpec: %v", err)
	}
	if !strings.Contains(spec.String(), "attach 6d681a76") {
		t.Errorf("command %s should attach to the held session", spec)
	}
}

func TestOpenResumesOnceTheAgentHasLetGo(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	runner := bgRunner(bgListing("6d681a76", bgSession, "working", true))
	startedBackground(t, svc, runner)

	// The first poll is what teaches the record the id a resume needs.
	records, _, err := svc.PollBackground(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runner.Results["agents"] = exec.Result{Stdout: bgListing("6d681a76", bgSession, "done", false)}

	spec, err := svc.OpenBackgroundSpec(context.Background(), records[0])
	if err != nil {
		t.Fatalf("OpenBackgroundSpec: %v", err)
	}
	if !strings.Contains(spec.String(), "--resume "+bgSession) {
		t.Errorf("command %s should resume the captured session", spec)
	}
}

// Closing a session the user had attached to is not the end of the review.
// Detecting here would read GitHub for a review still being written, land on
// unreviewed, and take the record out of the poll.
func TestLeavingAWorkingSessionKeepsItRunning(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := startedBackground(t, svc, bgRunner(bgListing("6d681a76", bgSession, "working", true)))
	// Preparing the review read GitHub once, for the snapshot detection measures
	// against. Only what happens after this point is under test.
	reads := ghc.reads

	after, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}
	if after.State != review.StateReviewing {
		t.Errorf("state = %q, want the review still running", after.State)
	}
	if ghc.reads != reads {
		t.Errorf("AfterExit read GitHub for a session that is still working")
	}
}

func TestLeavingAFinishedSessionDetectsTheReview(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo(), reviews: []review.GHReview{
		{ID: 55, State: "PENDING", User: struct {
			Login string `json:"login"`
		}{Login: "haacked"}},
	}}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := startedBackground(t, svc, bgRunner(bgListing("6d681a76", bgSession, "done", true)))

	after, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}
	if after.State != review.StateDrafted {
		t.Errorf("state = %q, want the pending review detected", after.State)
	}
}

// Nothing may still be writing when the clone under it is deleted.
func TestAbandonStopsTheSessionBeforeDeletingItsClone(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	runner := bgRunner(bgListing("6d681a76", bgSession, "working", true))
	rec := startedBackground(t, svc, runner)

	done, err := svc.Abandon(context.Background(), rec)
	if err != nil {
		t.Fatalf("Abandon: %v", err)
	}
	if done.State != review.StateAbandoned {
		t.Errorf("state = %q", done.State)
	}

	lines := runner.Lines()
	if !slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, "claude stop 6d681a76") }) {
		t.Fatalf("the session was never stopped: %v", lines)
	}
	// rm is the command that deletes the conversation, and docket never runs it.
	for _, line := range lines {
		if strings.Contains(line, "claude rm") {
			t.Errorf("abandon deleted the conversation: %s", line)
		}
	}
}

// A row that cannot be abandoned because its agent will not answer is worse
// than an agent left running, so the failure is reported and the record closes.
func TestAbandonClosesTheRecordEvenWhenTheStopFails(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	runner := bgRunner(bgListing("6d681a76", bgSession, "working", true))
	rec := startedBackground(t, svc, runner)
	runner.Errs = map[string]error{"stop": errors.New("no such session")}

	done, err := svc.Abandon(context.Background(), rec)
	if err != nil {
		t.Fatalf("Abandon: %v", err)
	}
	if done.State != review.StateAbandoned {
		t.Errorf("state = %q, want the record closed anyway", done.State)
	}
	if !strings.Contains(done.Err, "no such session") {
		t.Errorf("the row does not say the stop failed: %q", done.Err)
	}
}

// A re-review starts a new session. claude would otherwise hold the finished one
// with no record naming it.
func TestRereviewStopsTheHeldBackgroundSession(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	runner := bgRunner(bgListing("6d681a76", bgSession, "done", true))
	rec := startedBackground(t, svc, runner)
	rec.State = review.StateDrafted

	again, err := svc.Rereview(context.Background(), rec, review.IntentAppend, review.ModeInteractive)
	if err != nil {
		t.Fatalf("Rereview: %v", err)
	}
	if lines := runner.Lines(); !slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, "claude stop 6d681a76") }) {
		t.Fatalf("the held session was never stopped: %v", lines)
	}
	if again.BGID != "" {
		t.Errorf("background id = %q, want none after the stop", again.BGID)
	}
	if got := storedByID(t, svc, rec.ID); got.BGID != "" || got.Mode != review.ModeInteractive {
		t.Errorf("stored record has background id %q in mode %q, want none in interactive", got.BGID, got.Mode)
	}
}

// A session that would not stop is still held, so the record keeps the id that
// names it and the mode that says how to reach it.
func TestRereviewLeavesTheRecordWhenTheStopFails(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	runner := bgRunner(bgListing("6d681a76", bgSession, "done", true))
	rec := startedBackground(t, svc, runner)
	rec.State = review.StateDrafted
	runner.Errs = map[string]error{"stop": errors.New("no such session")}

	if _, err := svc.Rereview(context.Background(), rec, review.IntentAppend, review.ModeInteractive); err == nil {
		t.Fatal("Rereview went ahead although the held session did not stop")
	}
	got := storedByID(t, svc, rec.ID)
	if got.BGID != "6d681a76" || got.Mode != review.ModeBackground || got.Intent != review.IntentReview {
		t.Errorf("stored record has background id %q, mode %q, intent %q, want it unchanged", got.BGID, got.Mode, got.Intent)
	}
	if !strings.Contains(got.Err, "no such session") {
		t.Errorf("the row does not say the stop failed: %q", got.Err)
	}
}

// A background session outlives the docket that started it, so the startup pass
// that re-detects abandoned interactive sessions must leave it alone.
func TestReconcileLeavesABackgroundSessionToThePoll(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	startedBackground(t, svc, bgRunner(bgListing("6d681a76", bgSession, "working", true)))
	reads := ghc.reads

	records, err := svc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if records[0].State != review.StateReviewing {
		t.Errorf("state = %q, want the running session untouched", records[0].State)
	}
	if ghc.reads != reads {
		t.Error("Reconcile read GitHub for a session that is still running")
	}
}

func TestExplainBackgroundRecordsNothing(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	svc.Runner = bgRunner("[]")

	_, spec, err := svc.Explain(context.Background(), unlisted, "claude", review.ModeBackground, review.IntentReview)
	if err != nil {
		t.Fatalf("ExplainBackground: %v", err)
	}
	if !strings.Contains(spec.String(), "--bg") {
		t.Errorf("command %s is not a background start", spec)
	}

	records, err := svc.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Errorf("a dry run recorded %d records", len(records))
	}
}

func TestBackgroundIsRefusedForAnEngineThatHasNone(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	svc.Runner = bgRunner("[]")

	rec, _, err := svc.Prepare(context.Background(), unlisted, "codex", review.ModeBackground, review.IntentReview)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if _, err := svc.StartBackground(context.Background(), rec); err == nil {
		t.Fatal("StartBackground accepted an engine with no background mode")
	}
}

// The agent goes on holding a session after the review it ran has finished, so
// a record abandoned once it was drafted still has one to end. Asking whether
// the review is running rather than whether a session exists would leave that
// agent holding the clone docket is about to delete.
func TestAbandonStopsASessionWhoseReviewIsAlreadyDone(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo(), reviews: []review.GHReview{
		{ID: 55, State: "PENDING", User: struct {
			Login string `json:"login"`
		}{Login: "haacked"}},
	}}
	svc, _ := newService(t, ghc, newFakeGit())
	runner := bgRunner(bgListing("6d681a76", bgSession, "done", true))
	startedBackground(t, svc, runner)

	records, _, err := svc.PollBackground(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if records[0].State != review.StateDrafted {
		t.Fatalf("state = %q, want the review drafted before it is abandoned", records[0].State)
	}

	if _, err := svc.Abandon(context.Background(), records[0]); err != nil {
		t.Fatalf("Abandon: %v", err)
	}
	lines := runner.Lines()
	if !slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, "claude stop 6d681a76") }) {
		t.Errorf("a drafted background review was abandoned without stopping its session: %v", lines)
	}
}

// An interactive review has no agent session to end, so abandoning one runs no
// stop at all.
func TestAbandoningAnInteractiveReviewStopsNothing(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	runner := bgRunner("[]")
	svc.Runner = runner

	rec, _, err := svc.Prepare(context.Background(), unlisted, "claude", review.ModeInteractive, review.IntentReview)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Abandon(context.Background(), rec); err != nil {
		t.Fatalf("Abandon: %v", err)
	}
	for _, line := range runner.Lines() {
		if strings.Contains(line, "stop") {
			t.Errorf("abandoning an interactive review ran %s", line)
		}
	}
}

// Submitting is the ordinary end of a background review, and the agent goes on
// holding the session it ran until something stops it. Archiving without that
// would hold one session per review and, for tier 2, delete the directory the
// held session is working in.
func TestArchivingABackgroundReviewStopsItsSession(t *testing.T) {
	submitted := start.Add(time.Minute)
	ghc := &fakeGH{login: "haacked", info: prInfo(), reviews: []review.GHReview{
		{ID: 55, State: "APPROVED", SubmittedAt: &submitted, User: struct {
			Login string `json:"login"`
		}{Login: "haacked"}},
	}}
	svc, _ := newService(t, ghc, newFakeGit())
	runner := bgRunner(bgListing("6d681a76", bgSession, "done", true))
	rec := startedBackground(t, svc, runner)
	dir := rec.Dir

	records, _, err := svc.PollBackground(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if records[0].State != review.StateArchived {
		t.Fatalf("state = %q, want the submitted review archived", records[0].State)
	}

	lines := runner.Lines()
	if !slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, "claude stop 6d681a76") }) {
		t.Errorf("archiving left the agent holding the session: %v", lines)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the tier-2 clone at %s survived the archive", dir)
	}
}

// Prepare is where docket learns whose pull request this is, and it has to know
// before the session launches: review-code creates no draft review on your own
// unless it is asked to.
func TestPrepareMarksYourOwnPullRequest(t *testing.T) {
	mine := prInfo()
	mine.Author.Login = "haacked"
	ghc := &fakeGH{login: "haacked", info: mine}
	svc, _ := newService(t, ghc, newFakeGit())

	rec, _, err := svc.Prepare(context.Background(), unlisted, "claude", review.ModeInteractive, review.IntentReview)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if !rec.OwnPR {
		t.Error("a pull request the signed-in user wrote was not marked as their own")
	}

	theirs := &fakeGH{login: "haacked", info: prInfo()}
	other, _ := newService(t, theirs, newFakeGit())
	rec, _, err = other.Prepare(context.Background(), unlisted, "claude", review.ModeInteractive, review.IntentReview)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if rec.OwnPR {
		t.Errorf("%s wrote this pull request, not the signed-in user", rec.Author)
	}
}

// A launch records its id in a second append, so a docket killed in between
// leaves a record naming no session. BackgroundRunning excludes it from every
// poll, so without recovery the agent runs on with nobody watching it.
func TestAPollAdoptsASessionTheLaunchNeverRecorded(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	runner := bgRunner("[]")
	rec := startedBackground(t, svc, runner)

	// The record as a docket killed between the launch and the second append
	// would have left it.
	lost := rec
	lost.BGID = ""
	if err := svc.append(lost); err != nil {
		t.Fatal(err)
	}

	runner.Results["agents"] = exec.Result{Stdout: `[{"id": "6d681a76", "kind": "background",
		"cwd": "` + rec.Dir + `", "startedAt": ` + msOf(rec.StartedAt) + `,
		"sessionId": "` + bgSession + `", "state": "working", "status": "busy", "pid": 7}]`}

	records, statuses, err := svc.PollBackground(context.Background())
	if err != nil {
		t.Fatalf("PollBackground: %v", err)
	}
	if records[0].BGID != "6d681a76" {
		t.Errorf("background id = %q, want the session adopted", records[0].BGID)
	}
	if records[0].State != review.StateReviewing {
		t.Errorf("state = %q, want the adopted review still running", records[0].State)
	}
	if _, running := statuses[records[0].ID]; !running {
		t.Error("the adopted session reports no status")
	}
}

// A record with no session to find is one whose launch failed before starting
// anything. Leaving it would read as running for ever.
func TestAPollClosesALaunchThatStartedNothing(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	runner := bgRunner("[]")
	rec := startedBackground(t, svc, runner)

	lost := rec
	lost.BGID = ""
	if err := svc.append(lost); err != nil {
		t.Fatal(err)
	}

	records, _, err := svc.PollBackground(context.Background())
	if err != nil {
		t.Fatalf("PollBackground: %v", err)
	}
	if records[0].State == review.StateReviewing {
		t.Error("a launch that started nothing is still waiting to finish")
	}
}

// Archiving happens on its own once a review goes in, so there is nobody to
// weigh an agent that may still be writing against a directory removed under it.
func TestArchiveKeepsTheCloneWhenTheStopFails(t *testing.T) {
	submitted := start.Add(time.Minute)
	ghc := &fakeGH{login: "haacked", info: prInfo(), reviews: []review.GHReview{
		{ID: 55, State: "APPROVED", SubmittedAt: &submitted, User: struct {
			Login string `json:"login"`
		}{Login: "haacked"}},
	}}
	svc, _ := newService(t, ghc, newFakeGit())
	runner := bgRunner(bgListing("6d681a76", bgSession, "done", true))
	rec := startedBackground(t, svc, runner)
	runner.Errs = map[string]error{"stop": errors.New("no such session")}

	records, _, err := svc.PollBackground(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if records[0].State != review.StateArchived {
		t.Errorf("state = %q, want the record closed anyway", records[0].State)
	}
	if !strings.Contains(records[0].Err, "no such session") {
		t.Errorf("the row does not say the stop failed: %q", records[0].Err)
	}
	if _, err := os.Stat(rec.Dir); err != nil {
		t.Errorf("the clone at %s was deleted under an agent that may still hold it", rec.Dir)
	}
}

// reopenedWhileWorking is a background review that posted its draft, whose
// session the user then opened, gave more work, and left while it was working.
// That puts the row back to reviewing, holding the draft's id.
func reopenedWhileWorking(t *testing.T, svc *Service, ghc *fakeGH, runner *exec.Fake) review.Record {
	t.Helper()
	startedBackground(t, svc, runner)
	ghc.reviews = []review.GHReview{{ID: pendingID, User: review.GHUser{Login: "haacked"}, State: review.StatePending}}
	runner.Results["agents"] = exec.Result{Stdout: idleListing("6d681a76", bgSession)}
	records, _, err := svc.PollBackground(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	runner.Results["agents"] = exec.Result{Stdout: bgListing("6d681a76", bgSession, "working", true)}
	rec, err := svc.AfterExit(context.Background(), records[0], nil)
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}
	if rec.State != review.StateReviewing || rec.ReviewID != pendingID {
		t.Fatalf("record is %q with review %d, want reviewing and still holding review %d", rec.State, rec.ReviewID, pendingID)
	}
	return rec
}

// Submitting the draft must not cut off the work the user gave the session.
func TestSubmittingWhileTheSessionWorksLeavesTheSessionRunning(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	runner := bgRunner("")
	rec := reopenedWhileWorking(t, svc, ghc, runner)

	done, err := svc.Submit(context.Background(), rec, review.EventComment, "")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	if len(ghc.submitted) != 1 || ghc.submitted[0].id != pendingID {
		t.Errorf("submitted %+v, want one call for review %d", ghc.submitted, pendingID)
	}
	if done.State != review.StateArchived {
		t.Errorf("state = %q, want the submitted review archived", done.State)
	}
	if slices.ContainsFunc(runner.Lines(), func(l string) bool { return strings.Contains(l, "claude stop") }) {
		t.Errorf("the submit stopped a session that was still working: %v", runner.Lines())
	}
	if _, err := os.Stat(rec.Dir); err != nil {
		t.Errorf("the clone at %s was deleted under a session that is still working: %v", rec.Dir, err)
	}
	if !strings.Contains(done.Err, "still working") {
		t.Errorf("the row does not say the session was left running: %q", done.Err)
	}
}

// review-code ends its turn once the draft is posted, and claude goes on holding
// the session. Submitting from the dashboard is the usual end of that review.
func TestSubmittingADraftWhoseSessionEndedItsTurnStopsTheSession(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	runner := bgRunner(idleListing("6d681a76", bgSession))
	rec := startedBackground(t, svc, runner)
	ghc.reviews = []review.GHReview{{ID: pendingID, User: review.GHUser{Login: "haacked"}, State: review.StatePending}}
	records, _, err := svc.PollBackground(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if records[0].State != review.StateDrafted {
		t.Fatalf("state = %q, want the draft detected", records[0].State)
	}

	done, err := svc.Submit(context.Background(), records[0], review.EventComment, "")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	if done.State != review.StateArchived {
		t.Errorf("state = %q, want the submitted review archived", done.State)
	}
	if !slices.ContainsFunc(runner.Lines(), func(l string) bool { return strings.Contains(l, "claude stop 6d681a76") }) {
		t.Errorf("the submit left the agent holding the session: %v", runner.Lines())
	}
	if _, err := os.Stat(rec.Dir); !os.IsNotExist(err) {
		t.Errorf("the clone at %s survived the submitted review", rec.Dir)
	}
}

// The listing can say idle for a moment before the per-session progress file
// catches up, the same gap progressFor reads Active for. Submitting then must
// not stop, or delete the clone under, a session that has just started a new
// turn.
func TestSubmittingWhenTheListingLagsTheProgressFileLeavesTheSessionRunning(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	runner := bgRunner(idleListing("6d681a76", bgSession))
	rec := startedBackground(t, svc, runner)
	ghc.reviews = []review.GHReview{{ID: pendingID, User: review.GHUser{Login: "haacked"}, State: review.StatePending}}
	records, _, err := svc.PollBackground(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if records[0].State != review.StateDrafted {
		t.Fatalf("state = %q, want the draft detected", records[0].State)
	}

	jobs := t.TempDir()
	svc.Cfg.ClaudeJobsDir = jobs
	dir := filepath.Join(jobs, "6d681a76")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"tempo": "active", "detail": "starting…"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	done, err := svc.Submit(context.Background(), records[0], review.EventComment, "")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	if slices.ContainsFunc(runner.Lines(), func(l string) bool { return strings.Contains(l, "claude stop") }) {
		t.Errorf("the submit stopped a session the progress file still reports active: %v", runner.Lines())
	}
	if _, err := os.Stat(rec.Dir); err != nil {
		t.Errorf("the clone at %s was deleted under a session the progress file still reports active: %v", rec.Dir, err)
	}
	if !strings.Contains(done.Err, "still working") {
		t.Errorf("the row does not say the session was left running: %q", done.Err)
	}
}

// The session can replace the draft with a new pending review after docket last
// read GitHub for this row, since the poll only rereads once the session goes
// idle. Submitting the stale id must not post to a review that is no longer
// pending, which could submit a review the session had already deleted, or
// archive the row and leave the session's replacement draft untracked.
func TestSubmitRefusesADraftTheSessionAlreadyReplaced(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	runner := bgRunner("")
	rec := reopenedWhileWorking(t, svc, ghc, runner)
	ghc.reviews = []review.GHReview{{ID: pendingID + 1, User: review.GHUser{Login: "haacked"}, State: review.StatePending}}

	if _, err := svc.Submit(context.Background(), rec, review.EventComment, ""); err == nil {
		t.Error("Submit accepted a draft the session had already replaced")
	}
	if len(ghc.submitted) != 0 {
		t.Errorf("Submit posted %+v to GitHub", ghc.submitted)
	}
}

// With no answer from the agent, archiving cannot tell a finished session from
// one still working, so it leaves the session and its clone alone.
func TestArchiveLeavesTheSessionWhenTheAgentDoesNotAnswer(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	runner := bgRunner("")
	rec := reopenedWhileWorking(t, svc, ghc, runner)
	runner.Errs = map[string]error{"agents": errors.New("daemon not running")}

	done, err := svc.Submit(context.Background(), rec, review.EventComment, "")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	if done.State != review.StateArchived {
		t.Errorf("state = %q, want the submitted review archived", done.State)
	}
	if !strings.Contains(done.Err, "daemon not running") {
		t.Errorf("the row does not say why the session was left: %q", done.Err)
	}
	if slices.ContainsFunc(runner.Lines(), func(l string) bool { return strings.Contains(l, "claude stop") }) {
		t.Errorf("the archive stopped a session it could not read: %v", runner.Lines())
	}
	if _, err := os.Stat(rec.Dir); err != nil {
		t.Errorf("the clone at %s was deleted under a session docket could not read: %v", rec.Dir, err)
	}
}

func msOf(t time.Time) string {
	return strconv.FormatInt(t.UnixMilli(), 10)
}
