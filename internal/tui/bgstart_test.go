package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/review"
)

// A launch that fails after StartBackground recorded StateReviewing may have
// started a session with no id recorded. r and R refuse that record. The poll has
// to run now to adopt the session or close the record, not at the next restart.
func TestAFailedLaunchThatRecordedReviewingPollsRightAway(t *testing.T) {
	onPath(t, "claude")
	svc, runner := batchService(t)
	svc.Cfg.GitHubUser = "haacked"
	runner.Results["branch --show-current"] = exec.Result{Stdout: "haacked/a-thing\n"}
	runner.Results["ls-files"] = exec.Result{Stdout: "README.md\n"}
	runner.Results["agents --json"] = exec.Result{Stdout: "[]"}
	runner.Results["/reviews"] = exec.Result{Stdout: "[]"}
	runner.Errs = map[string]error{"--bg": errors.New("claude exited 1")}

	ref, err := pr.ParseRef("https://github.com/haacked/docket/pull/7", "")
	if err != nil {
		t.Fatalf("ParseRef: %v", err)
	}
	rec, _, err := svc.Prepare(context.Background(), ref, "claude", review.ModeBackground, review.IntentReview)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	a := New(svc, svc.Cfg, "", false)
	got := a.startBackground(rec)()
	failed, ok := got.(bgStartFailedMsg)
	if !ok {
		t.Fatalf("a launch that failed after recording StateReviewing reported %T, want bgStartFailedMsg", got)
	}
	if !failed.record.InBackgroundSession() {
		t.Fatalf("the failed record is not InBackgroundSession: %+v", failed.record)
	}

	next, cmd := a.Update(failed)
	a = next.(App)
	if a.err == nil {
		t.Error("the failure did not reach the status line")
	}

	var poll *bgPolledMsg
	for _, m := range drain(cmd) {
		if p, ok := m.(bgPolledMsg); ok {
			poll = &p
		}
	}
	if poll == nil {
		t.Fatal("the failure did not poll for a session it may have started anyway")
	}
	if len(poll.records) != 1 || poll.records[0].State == review.StateReviewing {
		t.Errorf("the poll left the record waiting on a session nobody holds: %+v", poll.records)
	}

	// A poll that works clears a.err. The launch failure still has to be on screen.
	next, _ = a.Update(*poll)
	a = next.(App)
	if a.err == nil && !strings.Contains(a.status, "claude exited 1") {
		t.Errorf("the poll wiped the launch failure off the status line: status %q", a.status)
	}
}

// A launch that fails before recording StateReviewing has started nothing for
// the poll to find. It reports the error and reloads the records the way errMsg
// does. It runs no poll.
func TestAFailedLaunchThatNeverStartedDoesNotPoll(t *testing.T) {
	svc, _ := batchService(t)
	svc.Cfg.GitHubUser = "haacked"

	rec := review.Record{ID: "rec-1", Ref: pr.Ref{Org: "haacked", Repo: "docket", Number: 7}, Engine: "not-a-real-engine"}

	a := New(svc, svc.Cfg, "", false)
	got := a.startBackground(rec)()
	failed, ok := got.(bgStartFailedMsg)
	if !ok {
		t.Fatalf("a launch that never started reported %T, want bgStartFailedMsg", got)
	}
	if failed.record.InBackgroundSession() {
		t.Fatalf("a launch refused before recording StateReviewing reports InBackgroundSession: %+v", failed.record)
	}

	_, cmd := a.Update(failed)
	for _, m := range drain(cmd) {
		if _, ok := m.(bgPolledMsg); ok {
			t.Error("a launch that started nothing still polled")
		}
	}
}
