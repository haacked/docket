package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/review"
)

const untrustedStderr = "Workspace not trusted. Run `claude` in /tmp/x once and accept the trust prompt, then retry.\n"

// refusedLaunch starts a background review that claude refuses with stderr.
func refusedLaunch(t *testing.T, svc *Service, stderr string) (review.Record, error) {
	t.Helper()
	return launchBackground(t, svc, &exec.Fake{
		Results: map[string]exec.Result{"--bg": {Stderr: stderr, ExitCode: 1}},
		Errs:    map[string]error{"--bg": errors.New("claude exited 1: " + strings.TrimSpace(stderr))},
	})
}

// A launch the agent refused ran no session, so nothing on GitHub can say
// anything about it. It must not read as a review that ran and posted nothing.
func TestALaunchTheAgentRefusedDidNotStart(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())

	rec, err := refusedLaunch(t, svc, "Not logged in. Run /login\n")
	if err == nil {
		t.Fatal("StartBackground hid a refused launch")
	}

	stored := storedByID(t, svc, rec.ID)
	if stored.State != review.StateNotStarted {
		t.Errorf("state = %q, want %q", stored.State, review.StateNotStarted)
	}
	if !strings.Contains(stored.Err, "Not logged in") {
		t.Errorf("err = %q, want the reason claude gave", stored.Err)
	}
}

// The trust refusal is the one the caller can fix by handing the terminal to the
// agent, so it names the directory the agent has to trust.
func TestALaunchRefusedForTrustNamesTheDirectory(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())

	rec, err := refusedLaunch(t, svc, untrustedStderr)

	if !errors.Is(err, ErrUntrusted) {
		t.Fatalf("err = %v, want ErrUntrusted", err)
	}
	if !strings.Contains(err.Error(), rec.Dir) {
		t.Errorf("err = %q, want it to name %q", err, rec.Dir)
	}
	if got := storedByID(t, svc, rec.ID); got.State != review.StateNotStarted || !strings.Contains(got.Err, rec.Dir) {
		t.Errorf("stored = %q with err %q, want not started and the directory named", got.State, got.Err)
	}
}

// A refresh reads GitHub, which knows nothing about a launch that never ran.
// Detecting it would move it to unreviewed and wipe the reason it never ran.
func TestRefreshLeavesALaunchThatDidNotStartAlone(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec, _ := refusedLaunch(t, svc, untrustedStderr)

	if _, err := svc.Refresh(context.Background(), storedByID(t, svc, rec.ID)); err == nil {
		t.Error("Refresh read GitHub for a launch that never ran")
	}
	if _, err := svc.RefreshAll(context.Background()); err != nil {
		t.Fatalf("RefreshAll: %v", err)
	}
	got := storedByID(t, svc, rec.ID)
	if got.State != review.StateNotStarted || got.Err == "" {
		t.Errorf("after a refresh: state %q, err %q, want it untouched", got.State, got.Err)
	}
}

// A launch that did not start is not running, so the poll has nothing to ask
// the agent about it.
func TestAPollLeavesALaunchThatDidNotStartAlone(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec, _ := refusedLaunch(t, svc, untrustedStderr)

	records, _, err := svc.PollBackground(context.Background())
	if err != nil {
		t.Fatalf("PollBackground: %v", err)
	}
	if records[0].State != review.StateNotStarted {
		t.Errorf("state = %q, want the poll to leave it alone", records[0].State)
	}
	if storedByID(t, svc, rec.ID).Err == "" {
		t.Error("the poll dropped the reason the launch was refused")
	}
}

// Starting again clears what the refused launch recorded, so the row stops
// saying why an earlier attempt failed.
func TestStartingAgainClearsTheRefusal(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec, _ := refusedLaunch(t, svc, untrustedStderr)

	svc.Runner = bgRunner(bgListing("6d681a76", bgSession, "working", true))
	started, err := svc.StartBackground(context.Background(), storedByID(t, svc, rec.ID))
	if err != nil {
		t.Fatalf("StartBackground: %v", err)
	}
	if started.State != review.StateReviewing || started.Err != "" {
		t.Errorf("state %q, err %q, want a running review with no error", started.State, started.Err)
	}
}

func TestTrustSpecRunsTheAgentInTheRecordsDirectory(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec, _ := refusedLaunch(t, svc, untrustedStderr)

	spec, err := svc.TrustSpec(storedByID(t, svc, rec.ID))
	if err != nil {
		t.Fatalf("TrustSpec: %v", err)
	}
	if spec.Dir != rec.Dir || spec.Path != "claude" {
		t.Errorf("spec = %s, want claude in %s", spec, rec.Dir)
	}
}

// The poll hands the screen the agent's own account of each running session,
// which is how the user tells a working review from one waiting on them.
func TestPollReportsWhatEachRunningSessionIsDoing(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	jobs := t.TempDir()
	svc.Cfg.ClaudeJobsDir = jobs
	if err := os.MkdirAll(filepath.Join(jobs, "6d681a76"), 0o755); err != nil {
		t.Fatal(err)
	}
	state := `{"detail": "7 review agents dispatched", "tempo": "blocked", "needs": "permission to run gh"}`
	if err := os.WriteFile(filepath.Join(jobs, "6d681a76", "state.json"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := startedBackground(t, svc, bgRunner(bgListing("6d681a76", bgSession, "working", true)))

	_, statuses, err := svc.PollBackground(context.Background())
	if err != nil {
		t.Fatalf("PollBackground: %v", err)
	}
	got := statuses[rec.ID].Progress
	if got.Detail != "7 review agents dispatched" || got.Needs != "permission to run gh" {
		t.Errorf("progress = %+v, want the agent's detail and what it needs", got)
	}
}

// The stored state is only as fresh as the last detection, and a refresh skips
// a launch that never ran. A pull request that merged while the row waited must
// not get a review.
func TestRestartRefusesAPullRequestThatMergedSince(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec, _ := refusedLaunch(t, svc, untrustedStderr)
	ghc.info.State = review.PRMerged
	svc.Runner = bgRunner(bgListing("6d681a76", bgSession, "working", true))

	if _, err := svc.Restart(context.Background(), storedByID(t, svc, rec.ID)); err == nil {
		t.Fatal("Restart started a review of a merged pull request")
	}
	if got := storedByID(t, svc, rec.ID); got.State != review.StateNotStarted {
		t.Errorf("state = %q, want the row left as it was", got.State)
	}
}

// A review the user submitted while the row waited is not this session's work.
// Without a fresh snapshot, detection would read it as this session's
// submission and archive the row.
func TestRestartTakesAFreshSnapshotOfPriorReviews(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec, _ := refusedLaunch(t, svc, untrustedStderr)
	ghc.reviews = []review.GHReview{mySubmitted()}
	svc.Runner = bgRunner(bgListing("6d681a76", bgSession, "working", true))

	started, err := svc.Restart(context.Background(), storedByID(t, svc, rec.ID))
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if !slices.Contains(started.PriorReviewIDs, mySubmittedID) {
		t.Errorf("prior reviews = %v, want the review submitted while the row waited", started.PriorReviewIDs)
	}
	if !started.BackgroundRunning() {
		t.Errorf("state = %q, want the review running in the background", started.State)
	}
}

// Only a launch that never ran has nothing to resume. Restarting any other
// record would start a second session beside the one it has.
func TestRestartRefusesARecordThatStarted(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := startedBackground(t, svc, bgRunner(bgListing("6d681a76", bgSession, "working", true)))

	if _, err := svc.Restart(context.Background(), rec); err == nil {
		t.Error("Restart started a second session for a running review")
	}
}
