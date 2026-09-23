package session

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/haacked/docket/internal/core/review"
)

func TestAfterExitArchivesAnUnreviewedRowOnAMergedPullRequest(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := launched(t, svc, unlisted)
	ghc.info.State = review.PRMerged

	done, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}

	if done.State != review.StateArchived || done.PRState != review.PRMerged {
		t.Errorf("record is %q on a %q pull request, want archived on a merged one", done.State, done.PRState)
	}
	if done.SubmittedAt != nil {
		t.Errorf("submitted at %v, want nothing: no review went in", done.SubmittedAt)
	}
	if _, err := os.Stat(rec.Dir); !os.IsNotExist(err) {
		t.Errorf("the clone at %s survived the archive", rec.Dir)
	}
	if got := storedByID(t, svc, rec.ID); got.State != review.StateArchived {
		t.Errorf("stored state = %q, want archived", got.State)
	}
}

// GitHub accepts a review on a merged pull request. The draft stays for the
// user to submit or drop.
func TestAfterExitKeepsADraftOnAMergedPullRequest(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := launched(t, svc, unlisted)
	ghc.info.State = review.PRMerged
	ghc.reviews = []review.GHReview{myPending()}

	done, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}

	if done.State != review.StateDrafted || done.PRState != review.PRMerged {
		t.Errorf("record is %q on a %q pull request, want drafted on a merged one", done.State, done.PRState)
	}
	if !done.Submittable() {
		t.Error("the draft on a merged pull request cannot be submitted")
	}
	if _, err := os.Stat(rec.Dir); err != nil {
		t.Errorf("the clone is gone, but the review is still a draft: %v", err)
	}
}

func TestAfterExitRecordsThatThePullRequestIsOpen(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := launched(t, svc, unlisted)
	ghc.info.State = review.PROpen

	done, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}

	if done.State != review.StateUnreviewed || done.PRState != review.PROpen {
		t.Errorf("record is %q on a %q pull request, want unreviewed on an open one", done.State, done.PRState)
	}
}

// Decide cannot produce reviewed. It reads an adopted row with nothing new on
// GitHub as unreviewed. A refresh must not take away the state adopt gave it.
func TestRefreshKeepsAReviewedRowWhenNothingNewIsOnGitHub(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo(), reviews: []review.GHReview{mySubmitted()}}
	svc, _ := newService(t, ghc, newFakeGit())
	notesFor(t, svc, unlisted)
	rec := prepareWith(t, svc, unlisted, "claude", review.IntentAsk)

	done, err := svc.Refresh(context.Background(), rec)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	if done.State != review.StateReviewed {
		t.Errorf("state = %q, want reviewed", done.State)
	}
	if got := storedByID(t, svc, rec.ID).State; got != review.StateReviewed {
		t.Errorf("stored state = %q, want reviewed", got)
	}
}

func TestRefreshAllArchivesUnreviewedAndReviewedRowsOnAMergedPullRequest(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())

	sequentialIDs(svc, "rec-1", "rec-2")
	unreviewed := launched(t, svc, unlisted)
	if _, err := svc.AfterExit(context.Background(), unreviewed, nil); err != nil {
		t.Fatal(err)
	}
	ghc.reviews = []review.GHReview{mySubmitted()}
	notesFor(t, svc, listed)
	reviewed := prepareWith(t, svc, listed, "claude", review.IntentAsk)
	ghc.info.State = review.PRMerged

	records, err := svc.RefreshAll(context.Background())
	if err != nil {
		t.Fatalf("RefreshAll: %v", err)
	}

	for _, rec := range records {
		if rec.State != review.StateArchived {
			t.Errorf("%s is %q, want archived", rec.ID, rec.State)
		}
	}
	for _, id := range []string{unreviewed.ID, reviewed.ID} {
		if got := storedByID(t, svc, id).State; got != review.StateArchived {
			t.Errorf("stored %s is %q, want archived", id, got)
		}
	}
	if _, err := os.Stat(unreviewed.Dir); !os.IsNotExist(err) {
		t.Errorf("the clone at %s survived the archive", unreviewed.Dir)
	}
	if _, err := os.Stat(reviewed.NotesPath); err != nil {
		t.Errorf("the archive removed the notes: %v", err)
	}
}

func TestAfterAskArchivesAReviewedRowOnAMergedPullRequest(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo(), reviews: []review.GHReview{mySubmitted()}}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := adopted(t, svc, unlisted)
	ghc.info.State = review.PRMerged

	done, err := svc.AfterAsk(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterAsk: %v", err)
	}

	if done.State != review.StateArchived {
		t.Errorf("state = %q, want archived", done.State)
	}
	if got := storedByID(t, svc, rec.ID); got.AskSessionID != rec.AskSessionID {
		t.Errorf("stored ask session = %q, want %q", got.AskSessionID, rec.AskSessionID)
	}
}

func TestPrepareRefusesAMergedPullRequest(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	ghc.info.State = review.PRMerged
	gitc := newFakeGit()
	svc, _ := newService(t, ghc, gitc)

	_, _, err := svc.Prepare(context.Background(), unlisted, "claude", review.ModeInteractive, review.IntentReview)
	if err == nil || !strings.Contains(err.Error(), "merged") {
		t.Fatalf("Prepare err = %v, want a refusal that names the merge", err)
	}

	if records := stored(t, svc); len(records) != 0 {
		t.Errorf("records = %d, want none for a refused pull request", len(records))
	}
	if len(gitc.calls) != 0 {
		t.Errorf("git ran %v for a refused pull request", gitc.calls)
	}
}

func TestPrepareRecordsThatThePullRequestIsOpen(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	ghc.info.State = review.PROpen
	svc, _ := newService(t, ghc, newFakeGit())

	rec, _, err := svc.Prepare(context.Background(), unlisted, "claude", review.ModeInteractive, review.IntentReview)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	if rec.PRState != review.PROpen {
		t.Errorf("pr state = %q, want open", rec.PRState)
	}
}

// The new review screen asks how to handle an existing review before it
// prepares. Refusing here spares the user a choice that Prepare then refuses.
func TestExistingRefusesAMergedPullRequest(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo(), reviews: []review.GHReview{myPending()}}
	ghc.info.State = review.PRMerged
	svc, _ := newService(t, ghc, newFakeGit())

	if _, err := svc.Existing(context.Background(), unlisted); err == nil || !strings.Contains(err.Error(), "merged") {
		t.Errorf("Existing err = %v, want a refusal that names the merge", err)
	}
}

// The stored state is only as fresh as the last detection. A row that read open
// last week may have merged since.
func TestRereviewRefusesAPullRequestThatMergedSinceTheLastRead(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	ghc.info.State = review.PROpen
	svc, _ := newService(t, ghc, newFakeGit())
	rec := drafted(t, svc, ghc, unlisted)
	ghc.info.State = review.PRMerged

	if _, err := svc.Rereview(context.Background(), rec, review.IntentAppend, review.ModeInteractive); err == nil || !strings.Contains(err.Error(), "merged") {
		t.Errorf("Rereview err = %v, want a refusal that names the merge", err)
	}
	if got := storedByID(t, svc, rec.ID).State; got != review.StateDrafted {
		t.Errorf("stored state = %q, want the draft left alone", got)
	}
}

// The poll owns a background review that is still running. Reading GitHub for it
// mid-session finds no draft yet, and on a merged pull request the archive would
// stop the session and delete its clone.
func TestRefreshLeavesARunningBackgroundReviewToThePoll(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	runner := bgRunner(bgListing("6d681a76", bgSession, "working", true))
	rec := startedBackground(t, svc, runner)
	ghc.info.State = review.PRMerged

	if _, err := svc.Refresh(context.Background(), rec); err == nil {
		t.Error("Refresh read GitHub for a background review that is still running")
	}
	records, err := svc.RefreshAll(context.Background())
	if err != nil {
		t.Fatalf("RefreshAll: %v", err)
	}

	if records[0].State != review.StateReviewing {
		t.Errorf("state = %q, want the running review left to the poll", records[0].State)
	}
	if slices.ContainsFunc(runner.Lines(), func(l string) bool { return strings.Contains(l, "claude stop") }) {
		t.Errorf("a refresh stopped the running session: %v", runner.Lines())
	}
	if _, err := os.Stat(rec.Dir); err != nil {
		t.Errorf("a refresh deleted the clone under the running session: %v", err)
	}
}

// A launch records StateReviewing before the background id arrives, so a record
// can carry no BGID while a session is genuinely running behind it. recoverLost
// treats this shape as needing recovery from the agent's own listing. Refuse it
// the same as a record that already has its id, because stopBackground cannot
// stop a session it has no id for.
func TestRefreshLeavesABackgroundReviewWithNoIDYetToThePoll(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := startedBackground(t, svc, bgRunner(bgListing("6d681a76", bgSession, "working", true)))
	rec.BGID = ""
	ghc.info.State = review.PRMerged

	if _, err := svc.Refresh(context.Background(), rec); err == nil {
		t.Error("Refresh read GitHub for a background review with no id yet")
	}
}

func TestPollArchivesAFinishedBackgroundReviewOnAMergedPullRequest(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	runner := bgRunner(bgListing("6d681a76", bgSession, "done", true))
	rec := startedBackground(t, svc, runner)
	ghc.info.State = review.PRMerged

	records, _, err := svc.PollBackground(context.Background())
	if err != nil {
		t.Fatalf("PollBackground: %v", err)
	}

	if records[0].State != review.StateArchived {
		t.Errorf("state = %q, want archived", records[0].State)
	}
	if !slices.ContainsFunc(runner.Lines(), func(l string) bool { return strings.Contains(l, "claude stop 6d681a76") }) {
		t.Errorf("archiving left the agent holding the session: %v", runner.Lines())
	}
	if _, err := os.Stat(rec.Dir); !os.IsNotExist(err) {
		t.Errorf("the clone at %s survived the archive", rec.Dir)
	}
}

func TestAfterAskKeepsADraftOnAMergedPullRequest(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo(), reviews: []review.GHReview{myPending()}}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := adopted(t, svc, unlisted)
	ghc.info.State = review.PRMerged

	done, err := svc.AfterAsk(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterAsk: %v", err)
	}

	if done.State != review.StateDrafted || done.PRState != review.PRMerged || !done.Submittable() {
		t.Errorf("record is %q on a %q pull request, want a submittable draft on a merged one", done.State, done.PRState)
	}
}

// The reviews alone cannot say whether the pull request merged, so a detection
// that cannot read the pull request fails rather than guessing.
func TestADetectionThatCannotReadThePullRequestKeepsTheRecord(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := launched(t, svc, unlisted)
	ghc.infoErr = errors.New("dial tcp: lookup api.github.com: no such host")

	if _, err := svc.AfterExit(context.Background(), rec, nil); err == nil {
		t.Fatal("AfterExit succeeded without the pull request's state")
	}

	got := storedByID(t, svc, rec.ID)
	if got.State != review.StateReviewing || !strings.Contains(got.Err, "no such host") {
		t.Errorf("stored record is %q with err %q, want it still reviewing with the failure", got.State, got.Err)
	}
	if _, err := os.Stat(rec.Dir); err != nil {
		t.Errorf("a failed detection deleted the clone: %v", err)
	}
}

func TestExplainRereviewRefusesAPullRequestThatMergedSinceTheLastRead(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	ghc.info.State = review.PROpen
	svc, _ := newService(t, ghc, newFakeGit())
	rec := drafted(t, svc, ghc, unlisted)
	ghc.info.State = review.PRMerged

	if _, err := svc.ExplainRereview(context.Background(), rec, review.IntentAppend, review.ModeInteractive); err == nil || !strings.Contains(err.Error(), "merged") {
		t.Errorf("ExplainRereview err = %v, want a refusal that names the merge", err)
	}
}
