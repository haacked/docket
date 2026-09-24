package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/review"
)

// review-code ends a background review by asking whether to submit the draft it
// posted. claude reports that session as blocked, not done, so without this the
// row stays under Reviewing and the user cannot submit it from docket.
func TestPollMovesABlockedSessionWithADraftToDrafted(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := startedBackground(t, svc, bgRunner(bgListing("6d681a76", bgSession, "blocked", true)))
	ghc.reviews = []review.GHReview{myPending()}

	records, statuses, err := svc.PollBackground(context.Background())
	if err != nil {
		t.Fatalf("PollBackground: %v", err)
	}
	if records[0].State != review.StateDrafted || records[0].ReviewID != myPendingID {
		t.Errorf("state %q with review %d, want drafted with the pending review", records[0].State, records[0].ReviewID)
	}
	if _, polled := statuses[rec.ID]; polled {
		t.Error("a drafted record is still reported as running")
	}
	// claude still holds the session, so enter has to attach to it.
	if got := storedByID(t, svc, rec.ID); got.State != review.StateDrafted || !got.HasBackgroundSession() {
		t.Errorf("stored %q, background session %v, want drafted with the session kept", got.State, got.HasBackgroundSession())
	}
}

// A review-code session can also end its turn with a statement, telling the user
// how to amend the draft rather than asking. claude then keeps its working state
// and reports it idle, so the draft has to be found the same way.
func TestPollMovesAnIdleSessionWithADraftToDrafted(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := startedBackground(t, svc, bgRunner(idleListing("6d681a76", bgSession)))
	ghc.reviews = []review.GHReview{myPending()}

	records, statuses, err := svc.PollBackground(context.Background())
	if err != nil {
		t.Fatalf("PollBackground: %v", err)
	}
	if records[0].State != review.StateDrafted || records[0].ReviewID != myPendingID {
		t.Errorf("state %q with review %d, want drafted with the pending review", records[0].State, records[0].ReviewID)
	}
	if _, polled := statuses[rec.ID]; polled {
		t.Error("a drafted record is still reported as running")
	}
	if got := storedByID(t, svc, rec.ID); got.State != review.StateDrafted || !got.HasBackgroundSession() {
		t.Errorf("stored %q, background session %v, want drafted with the session kept", got.State, got.HasBackgroundSession())
	}
}

// A session that is still working has nothing to find on GitHub yet, so the
// poll does not read it.
func TestPollDoesNotReadGitHubForABusySession(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := startedBackground(t, svc, bgRunner(bgListing("6d681a76", bgSession, "working", true)))
	ghc.reviews = []review.GHReview{myPending()}
	before := ghc.reads

	records, statuses, err := svc.PollBackground(context.Background())
	if err != nil {
		t.Fatalf("PollBackground: %v", err)
	}
	if got := ghc.reads - before; got != 0 {
		t.Errorf("read GitHub %d times for a busy session, want none", got)
	}
	if records[0].State != review.StateReviewing {
		t.Errorf("state = %q, want the review still running", records[0].State)
	}
	if _, polled := statuses[rec.ID]; !polled {
		t.Error("the busy session is no longer watched")
	}
}

// A session can block before it posts anything, at a permission prompt for
// example. Reading that as unreviewed would stop watching a review in progress.
func TestPollKeepsABlockedSessionWithNothingPostedRunning(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := startedBackground(t, svc, bgRunner(bgListing("6d681a76", bgSession, "blocked", true)))

	records, statuses, err := svc.PollBackground(context.Background())
	if err != nil {
		t.Fatalf("PollBackground: %v", err)
	}
	if records[0].State != review.StateReviewing {
		t.Errorf("state = %q, want the review still running", records[0].State)
	}
	if _, polled := statuses[rec.ID]; !polled {
		t.Error("the blocked session is no longer watched")
	}
}

// A session stays blocked until the user answers it. Reading GitHub on every
// tick for that long would spend two API calls per session every 15 seconds.
func TestPollReadsGitHubOnceForEachStretchASessionIsBlocked(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	jobs := t.TempDir()
	svc.Cfg.ClaudeJobsDir = jobs
	writeState := func(updated string) {
		t.Helper()
		dir := filepath.Join(jobs, "6d681a76")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := `{"tempo": "blocked", "needs": "permission to run gh", "updatedAt": "` + updated + `"}`
		if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeState("2026-09-24T17:00:00Z")
	startedBackground(t, svc, bgRunner(bgListing("6d681a76", bgSession, "blocked", true)))
	before := ghc.reads

	for range 3 {
		if _, _, err := svc.PollBackground(context.Background()); err != nil {
			t.Fatalf("PollBackground: %v", err)
		}
	}
	if got := ghc.reads - before; got != 1 {
		t.Errorf("read GitHub %d times over three polls of one block, want 1", got)
	}

	// The user answered and the session blocked again.
	writeState("2026-09-24T17:20:00Z")
	if _, _, err := svc.PollBackground(context.Background()); err != nil {
		t.Fatalf("PollBackground: %v", err)
	}
	if got := ghc.reads - before; got != 2 {
		t.Errorf("read GitHub %d times, want a second read once the session blocked again", got)
	}
}

// Opening a drafted row's session and leaving it unanswered leaves the session
// blocked. The row keeps its draft rather than going back to reviewing, where s,
// r, c, and u all refuse it.
func TestLeavingABlockedSessionKeepsItsDraft(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	startedBackground(t, svc, bgRunner(bgListing("6d681a76", bgSession, "blocked", true)))
	ghc.reviews = []review.GHReview{myPending()}
	records, _, err := svc.PollBackground(context.Background())
	if err != nil {
		t.Fatalf("PollBackground: %v", err)
	}

	after, err := svc.AfterExit(context.Background(), records[0], nil)
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}
	if after.State != review.StateDrafted {
		t.Errorf("state = %q, want the draft kept", after.State)
	}
}

// A session that ended its turn without asking anything is left the same way
// when the user opens it before a poll has seen the draft.
func TestLeavingAnIdleSessionFindsItsDraft(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := startedBackground(t, svc, bgRunner(idleListing("6d681a76", bgSession)))
	ghc.reviews = []review.GHReview{myPending()}

	after, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}
	if after.State != review.StateDrafted {
		t.Errorf("state = %q, want the session's draft", after.State)
	}
}

// An append or overwrite re-review starts with my old pending draft still on
// GitHub, and review-code replaces it only at the end. A session that blocks
// before then has not posted anything of its own.
func TestAPendingDraftFromBeforeTheLaunchIsNotTheSessions(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo(), reviews: []review.GHReview{myPending()}}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := startedBackground(t, svc, bgRunner(bgListing("6d681a76", bgSession, "blocked", true)))
	if rec.PriorPendingID != myPendingID {
		t.Fatalf("prior pending id = %d, want the draft that was there at launch", rec.PriorPendingID)
	}

	records, _, err := svc.PollBackground(context.Background())
	if err != nil {
		t.Fatalf("PollBackground: %v", err)
	}
	if records[0].State != review.StateReviewing {
		t.Errorf("state = %q, want the review still running", records[0].State)
	}
}

// A session can block, go back to work once the user answers, and block again
// with its draft posted. The second block has to be read even when claude left
// no status file to tell the two apart.
func TestABlockedSessionIsReadAgainAfterItWorks(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	runner := bgRunner(bgListing("6d681a76", bgSession, "blocked", true))
	startedBackground(t, svc, runner)

	poll := func(state string) []review.Record {
		t.Helper()
		runner.Results["agents"] = exec.Result{Stdout: bgListing("6d681a76", bgSession, state, true)}
		records, _, err := svc.PollBackground(context.Background())
		if err != nil {
			t.Fatalf("PollBackground: %v", err)
		}
		return records
	}
	poll("blocked")
	poll("working")
	ghc.reviews = []review.GHReview{myPending()}

	if got := poll("blocked")[0].State; got != review.StateDrafted {
		t.Errorf("state = %q, want the second block read and the draft found", got)
	}
}

// A failed read of a blocked session would otherwise leave nothing on screen to
// say why the row has not moved. The error stays off the index, so the next poll
// that works clears it.
func TestAFailedReadOfABlockedSessionShowsOnTheRow(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := startedBackground(t, svc, bgRunner(bgListing("6d681a76", bgSession, "blocked", true)))
	ghc.reviewErr = errors.New("github is down")

	records, _, err := svc.PollBackground(context.Background())
	if err != nil {
		t.Fatalf("PollBackground: %v", err)
	}
	if records[0].State != review.StateReviewing || !strings.Contains(records[0].Err, "github is down") {
		t.Errorf("state %q, err %q, want the review running with the read error on the row", records[0].State, records[0].Err)
	}
	if got := storedByID(t, svc, rec.ID).Err; got != "" {
		t.Errorf("the index kept %q, want the error on screen only", got)
	}
}
