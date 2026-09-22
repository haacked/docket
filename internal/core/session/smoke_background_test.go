package session

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/haacked/docket/internal/core/engine"
	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/review"
)

// TestSmokeBackgroundAgainstARealPullRequest runs a real background review end
// to end, which is the one thing the fakes cannot cover: that claude accepts the
// command docket builds, that the id it prints is the id `claude agents` lists it
// under, and that the review finishes into a draft docket can find on GitHub.
//
// It launches a real agent and spends real tokens, so it is skipped unless you
// name a pull request. It creates a pending review on that pull request, which
// is what review-code's --draft does, and it never submits one.
//
// Your own pull request is the one worth naming: Prepare marks it and the launch
// passes --self, which is what makes review-code create the draft this test
// detects. Name a small one. A review of a large diff dispatches a dozen
// reviewer agents and runs well past half an hour:
//
//	DOCKET_SMOKE_BG_PR=haacked/docket#4 go test -count=1 -v -timeout 40m -run SmokeBackground ./internal/core/session/
func TestSmokeBackgroundAgainstARealPullRequest(t *testing.T) {
	input := os.Getenv("DOCKET_SMOKE_BG_PR")
	if input == "" {
		t.Skip("set DOCKET_SMOKE_BG_PR=<org/repo#N> to run a real background review")
	}
	ref, err := pr.ParseRef(input, "")
	if err != nil {
		t.Fatalf("DOCKET_SMOKE_BG_PR: %v", err)
	}

	svc := realService(t)
	ctx := context.Background()

	rec, plan, err := svc.Prepare(ctx, ref, "claude", review.ModeBackground)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Logf("%s is %s, running in %s", ref, plan.Tier, rec.Dir)
	if rec.SessionID != "" {
		t.Errorf("session id = %q, want none until claude reports one", rec.SessionID)
	}

	rec, err = svc.StartBackground(ctx, rec)
	if err != nil {
		t.Fatalf("StartBackground: %v", err)
	}
	if rec.BGID == "" {
		t.Fatal("claude reported no background id")
	}
	t.Logf("backgrounded as %s", rec.BGID)

	// Abandoning stops the session and deletes the clone, whatever the review
	// did, so a failure part way through does not leave an agent running.
	defer func() {
		done, err := svc.Abandon(context.Background(), rec)
		if err != nil {
			t.Errorf("Abandon: %v", err)
		}
		t.Logf("abandoned: state=%s err=%q", done.State, done.Err)
		assertSessionReleased(t, rec.BGID)
	}()

	rec = pollUntilDone(t, svc, rec)
	t.Logf("finished as %s, session %s, review %d", rec.State, rec.SessionID, rec.ReviewID)

	if rec.State == review.StateReviewing {
		t.Fatal("the review never finished")
	}
	if rec.SessionID == "" {
		t.Error("no session id was captured, so the review could not be reopened")
	}
	if rec.State == review.StateDrafted && rec.ReviewID == 0 {
		t.Error("a drafted review carries no review id")
	}
	if rec.State == review.StateUnreviewed {
		t.Errorf("the session left no review of yours on %s", ref)
	}

	// The session is finished and claude still holds it, which is the case that
	// takes an attach rather than a resume.
	spec, err := svc.OpenBackgroundSpec(ctx, rec)
	if err != nil {
		t.Fatalf("OpenBackgroundSpec: %v", err)
	}
	t.Logf("open would run: %s", spec)
	if !strings.Contains(spec.String(), "attach "+rec.BGID) {
		t.Errorf("command %s should attach to the session claude is holding", spec)
	}

	if _, err := os.Stat(rec.NotesPath); err != nil {
		t.Errorf("review-code wrote no notes at %s: %v", rec.NotesPath, err)
	}
}

// pollUntilDone drives PollBackground the way the dashboard's tick does, until
// the record leaves the running state.
func pollUntilDone(t *testing.T, svc *Service, rec review.Record) review.Record {
	t.Helper()

	// A review of a large diff dispatches a dozen reviewer agents and runs for
	// well over half an hour, so point this at a small pull request rather than
	// raising the limit and waiting.
	const (
		every = 15 * time.Second
		limit = 45 * time.Minute
	)
	deadline := time.Now().Add(limit)

	for time.Now().Before(deadline) {
		time.Sleep(every)
		records, statuses, err := svc.PollBackground(context.Background())
		if err != nil {
			t.Fatalf("PollBackground: %v", err)
		}
		current, ok := find(records, rec.ID)
		if !ok {
			t.Fatalf("record %s vanished from the index", rec.ID)
		}
		// Carried forward, so a run that times out still reports what the polls
		// had learned rather than the record as it was before the first one.
		rec = current
		if status, running := statuses[rec.ID]; running {
			t.Logf("  %s %s/%s session=%s", time.Now().Format("15:04:05"), status.State, status.Activity, rec.SessionID)
			continue
		}
		return rec
	}
	t.Errorf("the review was still running after %s", limit)
	return rec
}

func find(records []review.Record, id string) (review.Record, bool) {
	for _, rec := range records {
		if rec.ID == id {
			return rec, true
		}
	}
	return review.Record{}, false
}

// assertSessionReleased checks that abandoning really let the session go. claude
// keeps a stopped session listed and keeps its conversation, so what changes is
// that it no longer holds a process for it.
func assertSessionReleased(t *testing.T, bgid string) {
	t.Helper()

	res, err := exec.Real{}.Run(context.Background(), engine.Claude{}.StatusSpec(engine.Paths{}))
	if err != nil {
		t.Errorf("read the session list: %v", err)
		return
	}
	statuses, err := engine.Claude{}.ParseStatus(res)
	if err != nil {
		t.Errorf("parse the session list: %v", err)
		return
	}
	if statuses[bgid].Live {
		t.Errorf("claude still holds session %s after the abandon", bgid)
	}
}
