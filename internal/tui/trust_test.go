package tui

import (
	"errors"
	"os"
	osexec "os/exec"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/tui/msg"
)

const untrustedStderr = "Workspace not trusted. Run `claude` in /tmp/x once and accept the trust prompt, then retry.\n"

// untrustedService is a service whose claude refuses every background launch
// because nobody has trusted the directory.
func untrustedService(t *testing.T) (*App, *exec.Fake) {
	t.Helper()
	svc, runner := cloningService(t)
	runner.Results["--bg"] = exec.Result{Stderr: untrustedStderr, ExitCode: 1}
	runner.Errs = map[string]error{"--bg": errors.New("claude exited 1: Workspace not trusted.")}
	a := New(svc, svc.Cfg, "", false)
	return &a, runner
}

// trustLater lets claude launch again, as it does once the directory is trusted.
func trustLater(runner *exec.Fake) {
	runner.Errs = nil
	runner.Results["--bg"] = exec.Result{Stdout: "backgrounded · 0a1b2c3d\n"}
}

// refused prepares a background review of number and lets claude refuse it.
func refused(t *testing.T, a *App, number int) review.Record {
	t.Helper()
	got := a.startBackground(prepared(t, a, number))()
	needed, ok := got.(trustNeededMsg)
	if !ok {
		t.Fatalf("a launch refused for trust reported %T, want trustNeededMsg", got)
	}
	return needed.records[0]
}

func prepared(t *testing.T, a *App, number int) review.Record {
	t.Helper()
	ref := pr.Ref{Org: "haacked", Repo: "docket", Number: number}
	rec, _, err := a.svc.Prepare(t.Context(), ref, "claude", review.ModeBackground, review.IntentReview)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	return rec
}

// isTrustPrompt reports whether a message hands the terminal to the trust
// prompt.
func isTrustPrompt(m tea.Msg) bool {
	launch, ok := m.(launchMsg)
	return ok && launch.kind == launchTrust
}

// declined is the error claude's exit status 1 hands tea.ExecProcess when the
// user declines the trust prompt.
func declined(t *testing.T) error {
	t.Helper()
	err := osexec.Command("/usr/bin/false").Run()
	if err == nil {
		t.Fatal("/usr/bin/false exited zero")
	}
	return err
}

func bgLaunches(runner *exec.Fake) int {
	return len(slices.DeleteFunc(runner.Lines(), func(line string) bool { return !strings.Contains(line, "--bg") }))
}

func TestALaunchRefusedForTrustAsksForTheTrustPrompt(t *testing.T) {
	a, _ := untrustedService(t)

	rec := refused(t, a, 7)

	if rec.State != review.StateNotStarted {
		t.Errorf("state = %q, want the refused launch recorded as not started", rec.State)
	}
	_, cmd := a.Update(trustNeededMsg{records: []review.Record{rec}})
	if !slices.ContainsFunc(drain(cmd), isTrustPrompt) {
		t.Error("the refusal did not hand the terminal to the trust prompt")
	}
}

// Every review in a batch that claude refused goes to the trust prompt, rather
// than into the list of failures the user has to fix by hand.
func TestABatchSendsItsRefusedLaunchesToTheTrustPrompt(t *testing.T) {
	a, _ := untrustedService(t)

	got, ok := a.startBatch([]string{"https://github.com/haacked/docket/pull/7"}, "claude")().(batchStartedMsg)
	if !ok {
		t.Fatal("the batch did not report a batchStartedMsg")
	}
	if len(got.untrusted) != 1 || len(got.failed) != 0 {
		t.Fatalf("untrusted = %d, failed = %q, want the refusal waiting on the trust prompt", len(got.untrusted), got.failed)
	}
	_, cmd := a.Update(got)
	if !slices.ContainsFunc(drain(cmd), isTrustPrompt) {
		t.Error("the batch did not hand the terminal to the trust prompt")
	}
}

// One prompt answers for every review in the same directory, which is every
// tier-1 review. A review in another directory gets a prompt of its own.
func TestTrustingADirectoryStartsItsReviewsAndAsksAboutTheNext(t *testing.T) {
	a, runner := untrustedService(t)
	first, second, other := refused(t, a, 7), refused(t, a, 8), refused(t, a, 9)
	second.Dir = first.Dir
	trustLater(runner)

	_, cmd := a.Update(trustExitedMsg{records: []review.Record{first, second, other}})
	msgs := drain(cmd)

	var started batchStartedMsg
	for _, m := range msgs {
		if done, ok := m.(batchStartedMsg); ok {
			started = done
		}
	}
	if started.started != 2 {
		t.Errorf("started = %d, want both reviews in the trusted directory", started.started)
	}
	if !slices.ContainsFunc(msgs, isTrustPrompt) {
		t.Error("the review in another directory got no trust prompt")
	}
}

func TestDecliningTheTrustPromptStartsNothing(t *testing.T) {
	a, runner := untrustedService(t)
	rec := refused(t, a, 7)
	trustLater(runner)
	before := bgLaunches(runner)

	next, cmd := a.Update(trustExitedMsg{records: []review.Record{rec}, err: declined(t)})
	drain(cmd)

	if got := bgLaunches(runner); got != before {
		t.Errorf("launched %d more times after the user declined", got-before)
	}
	if status := next.(App).status; !strings.Contains(status, "still not trusted") {
		t.Errorf("status = %q, want it to say the directory is still not trusted", status)
	}
}

// A prompt the user accepted that did not help would otherwise come back after
// every launch, and the user could never leave it.
func TestASecondRefusalIsAFailureNotAnotherPrompt(t *testing.T) {
	a, _ := untrustedService(t)
	rec := refused(t, a, 7)

	_, cmd := a.Update(trustExitedMsg{records: []review.Record{rec}})
	for _, m := range drain(cmd) {
		if isTrustPrompt(m) {
			t.Error("a second refusal asked for the trust prompt again")
		}
		if done, ok := m.(batchStartedMsg); ok && (len(done.failed) != 1 || len(done.untrusted) != 0) {
			t.Errorf("failed = %q, untrusted = %d, want the second refusal reported as a failure", done.failed, len(done.untrusted))
		}
	}
}

// No session ran, so there is nothing to resume. enter starts the review again
// in the background, the way it was first started.
func TestEnterStartsALaunchThatDidNotStartAgain(t *testing.T) {
	a, runner := untrustedService(t)
	rec := refused(t, a, 7)
	trustLater(runner)
	a.dash = a.dash.SetRecords([]review.Record{rec})

	_, cmd := a.Update(msg.Resume{ID: rec.ID})
	msgs := drain(cmd)

	if len(msgs) != 1 {
		t.Fatalf("enter produced %d messages, want the launch's result", len(msgs))
	}
	detected, ok := msgs[0].(detectedMsg)
	if !ok || !detected.record.BackgroundRunning() {
		t.Errorf("enter produced %#v, want the review running in the background", msgs[0])
	}
}

func TestADryRunEnterOnALaunchThatDidNotStartLaunchesNothing(t *testing.T) {
	a, runner := untrustedService(t)
	rec := refused(t, a, 7)
	a.dryRun = true
	a.dash = a.dash.SetRecords([]review.Record{rec})
	before := bgLaunches(runner)

	_, cmd := a.Update(msg.Resume{ID: rec.ID})
	msgs := drain(cmd)

	if got := bgLaunches(runner); got != before {
		t.Error("a dry run launched the review")
	}
	if len(msgs) != 1 {
		t.Fatalf("the dry run produced %d messages, want one status line", len(msgs))
	}
	if status, ok := msgs[0].(statusMsg); !ok || !strings.Contains(status.text, "Would run:") || !strings.Contains(status.text, "--bg") {
		t.Errorf("the dry run reported %#v, want the background command it would run", msgs[0])
	}
}

// A second enter before the launch returns would start a second session that
// nothing polls or stops.
func TestASecondEnterBeforeTheLaunchReturnsStartsNothing(t *testing.T) {
	a, runner := untrustedService(t)
	rec := refused(t, a, 7)
	trustLater(runner)
	a.dash = a.dash.SetRecords([]review.Record{rec})

	next, first := a.Update(msg.Resume{ID: rec.ID})
	_, second := next.(App).Update(msg.Resume{ID: rec.ID})

	if second != nil {
		t.Error("the second enter started another launch")
	}
	if first == nil {
		t.Error("the first enter started nothing")
	}
}

// A clone deleted while its review waited on the prompt must not cost the
// reviews in other directories their prompts.
func TestATrustPromptThatCannotRunStillAsksAboutTheOthers(t *testing.T) {
	a, _ := untrustedService(t)
	gone, other := refused(t, a, 7), refused(t, a, 8)
	if err := os.RemoveAll(gone.Dir); err != nil {
		t.Fatal(err)
	}

	exited, ok := a.trust([]review.Record{gone, other})().(trustExitedMsg)
	if !ok || exited.err == nil {
		t.Fatalf("a prompt for a missing directory reported %#v, want a trustExitedMsg with the error", exited)
	}
	next, cmd := a.Update(exited)

	if !slices.ContainsFunc(drain(cmd), isTrustPrompt) {
		t.Error("the review in the other directory got no trust prompt")
	}
	if status := next.(App).status; !strings.Contains(status, "did not run") {
		t.Errorf("status = %q, want it to say the prompt did not run", status)
	}
}

// Pull requests a batch skipped have no row on the dashboard. The status is the
// only place that names them, so the reports after a trust prompt add to it.
func TestTheReportAfterATrustPromptKeepsWhatTheBatchSkipped(t *testing.T) {
	next, _ := app().Update(batchStartedMsg{skipped: []string{"haacked/docket#9"}})
	next, _ = next.(App).Update(batchStartedMsg{started: 1, retry: true})

	status := next.(App).status
	for _, want := range []string{"haacked/docket#9", "Started 1 background review"} {
		if !strings.Contains(status, want) {
			t.Errorf("status = %q, want it to keep %q", status, want)
		}
	}
}
