package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/core/tier"
)

// pendingID is the review review-code left on GitHub: created with no event, so
// GitHub holds it as PENDING with no submitted_at.
const pendingID = 4321

// drafted runs a session that ends with a pending review, which is the state a
// record has to be in before it can be submitted.
func drafted(t *testing.T, svc *Service, ghc *fakeGH, ref pr.Ref) review.Record {
	t.Helper()

	rec := launched(t, svc, ref)
	ghc.reviews = []review.GHReview{
		{ID: pendingID, User: review.GHUser{Login: "haacked"}, State: review.StatePending},
	}
	rec, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}
	if rec.State != review.StateDrafted || rec.ReviewID != pendingID {
		t.Fatalf("record is %q with review %d, want a drafted record holding review %d", rec.State, rec.ReviewID, pendingID)
	}
	return rec
}

func TestSubmitPostsTheEventAndArchivesTheRecord(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := drafted(t, svc, ghc, unlisted)

	done, err := svc.Submit(context.Background(), rec, review.EventRequestChanges, "one blocking comment")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	want := submitCall{ref: rec.Ref, id: pendingID, event: review.EventRequestChanges, body: "one blocking comment"}
	if len(ghc.submitted) != 1 || ghc.submitted[0] != want {
		t.Errorf("submitted %+v, want one call of %+v", ghc.submitted, want)
	}
	if done.State != review.StateArchived {
		t.Errorf("state = %q, want archived: a submitted review is no longer pending, which is what closes the record", done.State)
	}
	if done.SubmittedAt == nil {
		t.Error("the record carries no submission time")
	}

	stored, err := svc.Records()
	if err != nil {
		t.Fatal(err)
	}
	if stored[0].State != review.StateArchived {
		t.Errorf("the stored record is %q, want archived", stored[0].State)
	}
	if _, err := os.Stat(rec.Dir); !os.IsNotExist(err) {
		t.Errorf("the clone at %s survived a submitted review", rec.Dir)
	}
}

func TestSubmitSendsAnEmptyBodyAsEmpty(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := drafted(t, svc, ghc, unlisted)

	if _, err := svc.Submit(context.Background(), rec, review.EventComment, ""); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	if len(ghc.submitted) != 1 || ghc.submitted[0].body != "" {
		t.Errorf("submitted %+v, want one call with no body", ghc.submitted)
	}
}

// Only a record holding a pending review has anything to submit. Posting to the
// events endpoint for anything else is a 404 or a 422 from GitHub.
func TestSubmitRefusesARecordWithNothingPending(t *testing.T) {
	tests := []struct {
		name  string
		spoil func(review.Record) review.Record
	}{
		{
			name: "reviewing with no draft yet",
			spoil: func(rec review.Record) review.Record {
				rec.State = review.StateReviewing
				rec.ReviewID = 0
				return rec
			},
		},
		{
			name:  "no review of mine on GitHub",
			spoil: func(rec review.Record) review.Record { rec.State = review.StateUnreviewed; return rec },
		},
		{
			name:  "already archived",
			spoil: func(rec review.Record) review.Record { rec.State = review.StateArchived; return rec },
		},
		{
			name:  "drafted with no review id",
			spoil: func(rec review.Record) review.Record { rec.ReviewID = 0; return rec },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ghc := &fakeGH{login: "haacked", info: prInfo()}
			svc, _ := newService(t, ghc, newFakeGit())
			rec := tt.spoil(drafted(t, svc, ghc, unlisted))

			if _, err := svc.Submit(context.Background(), rec, review.EventComment, ""); err == nil {
				t.Fatal("Submit accepted a record with no pending review")
			}

			if len(ghc.submitted) != 0 {
				t.Errorf("Submit posted %+v to GitHub", ghc.submitted)
			}
			stored, err := svc.Records()
			if err != nil {
				t.Fatal(err)
			}
			if stored[0].State != review.StateDrafted {
				t.Errorf("the stored record is %q, want the drafted row left as it was", stored[0].State)
			}
		})
	}
}

// Resuming a drafted interactive row records it as reviewing with the draft's
// id. Another docket instance reads that row, and a submit there would archive it
// and delete the clone under the open session.
func TestSubmitRefusesADraftWhoseInteractiveSessionIsOpen(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec, _, err := svc.ResumeSpec(context.Background(), drafted(t, svc, ghc, unlisted))
	if err != nil {
		t.Fatalf("ResumeSpec: %v", err)
	}
	if rec.State != review.StateReviewing || rec.ReviewID != pendingID {
		t.Fatalf("record is %q with review %d, want reviewing and still holding review %d", rec.State, rec.ReviewID, pendingID)
	}

	if _, err := svc.Submit(context.Background(), rec, review.EventComment, ""); err == nil {
		t.Error("Submit accepted a draft whose interactive session is open")
	}
	if len(ghc.submitted) != 0 {
		t.Errorf("Submit posted %+v to GitHub", ghc.submitted)
	}
	if _, err := os.Stat(rec.Dir); err != nil {
		t.Errorf("the clone at %s was deleted under the open session: %v", rec.Dir, err)
	}
}

func TestSubmitRefusesAnEventGitHubDoesNotAccept(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := drafted(t, svc, ghc, unlisted)

	for _, event := range []string{"", "LGTM", "approve"} {
		if _, err := svc.Submit(context.Background(), rec, event, ""); err == nil {
			t.Errorf("Submit accepted the event %q", event)
		}
	}
	if len(ghc.submitted) != 0 {
		t.Errorf("Submit posted %+v to GitHub", ghc.submitted)
	}
}

// GitHub answers 422 to approving your own pull request. The draft is still
// there afterwards, so the record has to stay where the user can try again.
func TestSubmitKeepsTheDraftWhenGitHubRefuses(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := drafted(t, svc, ghc, unlisted)
	ghc.submitErr = errors.New("HTTP 422: Can not approve your own pull request")

	done, err := svc.Submit(context.Background(), rec, review.EventApprove, "")
	if err == nil {
		t.Fatal("Submit reported success on a 422")
	}

	if done.State != review.StateDrafted {
		t.Errorf("state = %q, want the record left drafted", done.State)
	}
	stored, err := svc.Records()
	if err != nil {
		t.Fatal(err)
	}
	if stored[0].State != review.StateDrafted {
		t.Errorf("the stored record is %q, want drafted", stored[0].State)
	}
	if stored[0].Err == "" {
		t.Error("the stored record carries no reason, so the dashboard cannot say why the submission failed")
	}
	if _, err := os.Stat(rec.Dir); err != nil {
		t.Errorf("the clone is gone after a failed submission, so there is nothing left to retry from: %v", err)
	}
}

// The submit screen leaves approve out on my own pull request, but it reads a
// login the service owns and a fresh install has not cached one yet. The refusal
// belongs here too, where the login is certain.
func TestSubmitRefusesApprovingMyOwnPullRequest(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := drafted(t, svc, ghc, unlisted)
	rec.Author = "Haacked"

	done, err := svc.Submit(context.Background(), rec, review.EventApprove, "")
	if err == nil {
		t.Fatal("Submit accepted an approval of my own pull request")
	}

	if len(ghc.submitted) != 0 {
		t.Errorf("Submit posted %+v, want the refusal to come before GitHub sees it", ghc.submitted)
	}
	if done.State != review.StateDrafted {
		t.Errorf("state = %q, want the draft left alone", done.State)
	}
}

func TestSubmitAllowsApprovingSomeoneElsesPullRequest(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := drafted(t, svc, ghc, unlisted)
	rec.Author = "someone-else"

	if _, err := svc.Submit(context.Background(), rec, review.EventApprove, ""); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if len(ghc.submitted) != 1 || ghc.submitted[0].event != review.EventApprove {
		t.Errorf("submitted %+v, want one approval", ghc.submitted)
	}
}

// review-code posts its summary as the body of the pending review. The submit
// screen starts from it, so the user edits that summary rather than writing one.
func TestDraftBodyReadsThePendingReviewsSummary(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := drafted(t, svc, ghc, unlisted)
	ghc.reviews = []review.GHReview{
		{ID: 99, User: review.GHUser{Login: "someone"}, State: "COMMENTED", Body: "Somebody else's review."},
		{ID: pendingID, User: review.GHUser{Login: "haacked"}, State: review.StatePending, Body: "Nice fix! No blockers."},
	}

	body, err := svc.DraftBody(context.Background(), rec)
	if err != nil {
		t.Fatalf("DraftBody: %v", err)
	}
	if body != "Nice fix! No blockers." {
		t.Errorf("body = %q, want the summary of review %d", body, pendingID)
	}
}

// A new review of the pull request or the browser can delete the draft while
// the screen is open. Submit reports the missing review when the user tries it.
// The read therefore answers empty rather than failing.
func TestDraftBodyIsEmptyWhenTheReviewIsGone(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := drafted(t, svc, ghc, unlisted)
	ghc.reviews = nil

	body, err := svc.DraftBody(context.Background(), rec)
	if err != nil {
		t.Fatalf("DraftBody: %v", err)
	}
	if body != "" {
		t.Errorf("body = %q, want none", body)
	}
}

func TestDraftBodyReportsAFailedRead(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := drafted(t, svc, ghc, unlisted)
	ghc.reviewErr = errors.New("HTTP 502")

	if _, err := svc.DraftBody(context.Background(), rec); err == nil {
		t.Error("DraftBody hid a failed read of GitHub")
	}
}

// A review session that submits its own review through docket mcp runs inside
// the clone. Removing the clone would delete the directory it works in, so the
// archive waits until the session ends.
func TestSubmitFromInsideTheCloneWaitsForTheSessionToEnd(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := drafted(t, svc, ghc, unlisted)
	inside := filepath.Join(rec.Dir, "internal")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	svc.CallerDir = inside

	done, err := svc.Submit(context.Background(), rec, review.EventComment, "")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	if len(ghc.submitted) != 1 {
		t.Errorf("submitted %+v, want the review posted", ghc.submitted)
	}
	if done.State != review.StateSubmitted || done.Err == "" {
		t.Errorf("record is %q with error %q, want submitted with the reason the archive waits", done.State, done.Err)
	}
	if got := storedByID(t, svc, rec.ID); got.State != review.StateSubmitted {
		t.Errorf("the stored record is %q, want submitted", got.State)
	}
	if _, err := os.Stat(inside); err != nil {
		t.Errorf("the clone was removed under the session running in it: %v", err)
	}

	// The session ends in docket, which runs outside the clone.
	svc.CallerDir = ""
	after, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}
	if after.State != review.StateArchived {
		t.Errorf("state after the session = %q, want archived", after.State)
	}
	if _, err := os.Stat(rec.Dir); !os.IsNotExist(err) {
		t.Errorf("the clone at %s survived the session's end", rec.Dir)
	}
}

// R on the dashboard refreshes every row, and the dashboard runs outside the
// clone. Finishing the archive there would delete the directory the session
// that submitted the review still works in. r on the row finishes it.
func TestRefreshAllLeavesAnArchiveThatWaitsForASession(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := drafted(t, svc, ghc, unlisted)
	svc.CallerDir = rec.Dir
	if _, err := svc.Submit(context.Background(), rec, review.EventComment, ""); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	svc.CallerDir = ""

	if _, err := svc.RefreshAll(context.Background()); err != nil {
		t.Fatalf("RefreshAll: %v", err)
	}
	if got := storedByID(t, svc, rec.ID); got.State != review.StateSubmitted || !got.CloneInUse {
		t.Errorf("stored record is %q with clone in use %v, want it left submitted", got.State, got.CloneInUse)
	}
	if _, err := os.Stat(rec.Dir); err != nil {
		t.Errorf("R removed the clone under the session running in it: %v", err)
	}

	done, err := svc.Refresh(context.Background(), storedByID(t, svc, rec.ID))
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if done.State != review.StateArchived || done.CloneInUse {
		t.Errorf("record is %q with clone in use %v, want archived", done.State, done.CloneInUse)
	}
	if _, err := os.Stat(rec.Dir); !os.IsNotExist(err) {
		t.Errorf("the clone at %s survived r on its row", rec.Dir)
	}
}

// u on a row whose archive waited reviews it again. The new review has no
// session in the clone yet, so R must read it again.
func TestRereviewClearsTheCloneInUse(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := drafted(t, svc, ghc, unlisted)
	svc.CallerDir = rec.Dir
	if _, err := svc.Submit(context.Background(), rec, review.EventComment, ""); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	svc.CallerDir = ""

	if _, err := svc.Rereview(context.Background(), storedByID(t, svc, rec.ID), review.IntentAppend, review.ModeInteractive); err != nil {
		t.Fatalf("Rereview: %v", err)
	}

	if got := storedByID(t, svc, rec.ID); got.CloneInUse {
		t.Errorf("stored record is %q with clone in use, want the flag cleared", got.State)
	}
}

func TestCallerInCloneMatchesOnlyTheRecordsOwnClone(t *testing.T) {
	clone := filepath.Join(t.TempDir(), "clones", "haacked", "docket", "pr-7")
	if err := os.MkdirAll(filepath.Join(clone, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The sibling has to exist, or RealPath leaves it unresolved beside a clone
	// that resolves through a symlink such as macOS's /var.
	if err := os.MkdirAll(clone+"-other", 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(filepath.Dir(clone), link); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		callerDir string
		tier      tier.Tier
		want      bool
	}{
		{name: "the clone", callerDir: clone, tier: tier.Tier2, want: true},
		{name: "inside the clone", callerDir: filepath.Join(clone, "internal"), tier: tier.Tier2, want: true},
		{name: "the clone through a symlink", callerDir: filepath.Join(link, "pr-7"), tier: tier.Tier2, want: true},
		{name: "a sibling sharing the prefix", callerDir: clone + "-other", tier: tier.Tier2},
		{name: "the parent", callerDir: filepath.Dir(clone), tier: tier.Tier2},
		// Every tier-1 record shares the scratch directory.
		{name: "tier 1", callerDir: clone, tier: tier.Tier1},
		{name: "no caller", tier: tier.Tier2},
	}
	for _, tc := range tests {
		svc := &Service{CallerDir: tc.callerDir}
		if got := svc.callerInClone(review.Record{Tier: tc.tier, Dir: clone}); got != tc.want {
			t.Errorf("%s: callerInClone = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The user can have the agent in a question-and-answer session submit the
// review through docket mcp. That session runs in the clone too, and its exit
// finishes the archive.
func TestAfterAskFinishesAnArchiveThatWaitedForTheSession(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo(), reviews: []review.GHReview{myPending()}}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := adopted(t, svc, unlisted)
	if _, err := os.Stat(rec.Dir); err != nil {
		t.Fatalf("the adopted review has no clone: %v", err)
	}
	svc.CallerDir = rec.Dir
	if _, err := svc.Submit(context.Background(), storedByID(t, svc, rec.ID), review.EventComment, ""); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if got := storedByID(t, svc, rec.ID); got.State != review.StateSubmitted {
		t.Fatalf("the stored record is %q, want submitted while the session runs", got.State)
	}

	svc.CallerDir = ""
	done, err := svc.AfterAsk(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterAsk: %v", err)
	}

	if done.State != review.StateArchived || done.Err != "" {
		t.Errorf("record is %q with error %q, want archived with no error", done.State, done.Err)
	}
	if _, err := os.Stat(rec.Dir); !os.IsNotExist(err) {
		t.Errorf("the clone at %s survived the session's end", rec.Dir)
	}
}
