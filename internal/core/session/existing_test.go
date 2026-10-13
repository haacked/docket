package session

import (
	"context"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/review"
)

// myPendingID and mySubmittedID are reviews of mine already on GitHub before
// docket sees the pull request.
const (
	myPendingID   = 8801
	mySubmittedID = 8802
)

func myPending() review.GHReview {
	return review.GHReview{ID: myPendingID, User: review.GHUser{Login: "haacked"}, State: review.StatePending}
}

func mySubmitted() review.GHReview {
	at := start.Add(-48 * time.Hour)
	return review.GHReview{ID: mySubmittedID, User: review.GHUser{Login: "haacked"}, State: "COMMENTED", SubmittedAt: &at}
}

func theirSubmitted() review.GHReview {
	at := start.Add(-48 * time.Hour)
	return review.GHReview{ID: 8803, User: review.GHUser{Login: "someone-else"}, State: "APPROVED", SubmittedAt: &at}
}

// notesFor writes review-code's notes for a pull request where review-code
// keeps them, which is where Existing looks.
func notesFor(t *testing.T, svc *Service, ref pr.Ref) string {
	t.Helper()
	path := svc.Cfg.NotesPath(ref.Org, ref.Repo, ref.Number)
	writeNotes(t, path, "# Review\n\nThe lock is dropped before the rewrite.\n")
	return path
}

func stored(t *testing.T, svc *Service) []review.Record {
	t.Helper()
	records, err := svc.Records()
	if err != nil {
		t.Fatal(err)
	}
	return records
}

func storedByID(t *testing.T, svc *Service, id string) review.Record {
	t.Helper()
	for _, rec := range stored(t, svc) {
		if rec.ID == id {
			return rec
		}
	}
	t.Fatalf("no stored record %q", id)
	return review.Record{}
}

func prepareWith(t *testing.T, svc *Service, ref pr.Ref, engineName string, intent review.Intent) review.Record {
	t.Helper()
	rec, _, err := svc.Prepare(context.Background(), ref, engineName, review.ModeInteractive, intent)
	if err != nil {
		t.Fatalf("Prepare(%s): %v", intent, err)
	}
	return rec
}

// adopted is a record prepared to ask about an existing review, whose first Q&A
// session has launched. Notes have to exist first: an ask is about them.
func adopted(t *testing.T, svc *Service, ref pr.Ref) review.Record {
	t.Helper()
	notesFor(t, svc, ref)
	rec := prepareWith(t, svc, ref, "claude", review.IntentAsk)
	rec, _, err := svc.AskSpec(rec)
	if err != nil {
		t.Fatalf("AskSpec: %v", err)
	}
	return rec
}

func TestExistingFindsNothingOnAPullRequestNeverReviewed(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())

	found, err := svc.Existing(context.Background(), unlisted)
	if err != nil {
		t.Fatalf("Existing: %v", err)
	}

	if found.Any() {
		t.Errorf("found %+v, want nothing", found)
	}
}

func TestExistingReportsTheNotesAndWhenTheyWereWritten(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	path := notesFor(t, svc, unlisted)
	written := time.Date(2026, 9, 18, 15, 30, 0, 0, time.UTC)
	if err := os.Chtimes(path, written, written); err != nil {
		t.Fatal(err)
	}

	found, err := svc.Existing(context.Background(), unlisted)
	if err != nil {
		t.Fatalf("Existing: %v", err)
	}

	if !found.NotesAt.Equal(written) {
		t.Errorf("notes at %v, want the file's mtime %v", found.NotesAt, written)
	}
	if !found.Any() {
		t.Error("notes alone do not count as an existing review, so the choice step is skipped and review-code asks instead")
	}
}

func TestExistingReportsMyPendingReview(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo(), reviews: []review.GHReview{myPending()}}
	svc, _ := newService(t, ghc, newFakeGit())

	found, err := svc.Existing(context.Background(), unlisted)
	if err != nil {
		t.Fatalf("Existing: %v", err)
	}

	if found.PendingID != myPendingID {
		t.Errorf("pending review = %d, want %d", found.PendingID, myPendingID)
	}
	if found.Submitted {
		t.Error("a pending review counted as a submitted one")
	}
}

// With no notes, a review of mine on GitHub still offers the choice: append and
// overwrite decide whether review-code keeps or replaces that review.
func TestExistingReportsMySubmittedReview(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo(), reviews: []review.GHReview{mySubmitted()}}
	svc, _ := newService(t, ghc, newFakeGit())

	found, err := svc.Existing(context.Background(), unlisted)
	if err != nil {
		t.Fatalf("Existing: %v", err)
	}

	if !found.Submitted {
		t.Error("Existing missed my submitted review")
	}
	if found.PendingID != 0 {
		t.Errorf("pending review = %d, want none", found.PendingID)
	}
	if !found.Any() {
		t.Error("a submitted review of mine does not count as an existing review")
	}
}

func TestExistingIgnoresSomebodyElsesReviews(t *testing.T) {
	theirs := myPending()
	theirs.User.Login = "someone-else"
	ghc := &fakeGH{login: "haacked", info: prInfo(), reviews: []review.GHReview{theirs, theirSubmitted()}}
	svc, _ := newService(t, ghc, newFakeGit())

	found, err := svc.Existing(context.Background(), unlisted)
	if err != nil {
		t.Fatalf("Existing: %v", err)
	}

	if found.Any() {
		t.Errorf("found %+v, want another reviewer's reviews left out", found)
	}
}

func TestExistingMatchesMyLoginWithoutCase(t *testing.T) {
	mine := myPending()
	mine.User.Login = "HAACKED"
	ghc := &fakeGH{login: "haacked", info: prInfo(), reviews: []review.GHReview{mine}}
	svc, _ := newService(t, ghc, newFakeGit())

	found, err := svc.Existing(context.Background(), unlisted)
	if err != nil {
		t.Fatalf("Existing: %v", err)
	}

	if found.PendingID != myPendingID {
		t.Errorf("pending review = %d, want %d: GitHub logins compare without case", found.PendingID, myPendingID)
	}
}

// An open record refuses the new review before GitHub is asked anything, so an
// unreachable GitHub cannot hide the refusal behind its own error.
func TestExistingRefusesAnOpenRecordWithoutAskingGitHub(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	prepareWith(t, svc, unlisted, "claude", review.IntentReview)

	ghc.infoErr = errors.New("dial tcp: lookup api.github.com: no such host")
	ghc.reviewErr = ghc.infoErr
	before := ghc.reads
	shouted := pr.Ref{Org: strings.ToUpper(unlisted.Org), Repo: strings.ToUpper(unlisted.Repo), Number: unlisted.Number}

	_, err := svc.Existing(context.Background(), shouted)

	if err == nil || !strings.Contains(err.Error(), "already open") {
		t.Errorf("err = %v, want the already-open refusal rather than GitHub's failure", err)
	}
	if ghc.reads != before {
		t.Errorf("Existing read GitHub's reviews %d times with a record already open, want none", ghc.reads-before)
	}
}

func TestExistingIgnoresAClosedRecord(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := prepareWith(t, svc, unlisted, "claude", review.IntentReview)
	if _, err := svc.Abandon(context.Background(), rec); err != nil {
		t.Fatalf("Abandon: %v", err)
	}

	if _, err := svc.Existing(context.Background(), unlisted); err != nil {
		t.Errorf("Existing refused a pull request whose earlier record was abandoned: %v", err)
	}
}

// A dry run runs Existing too, so Existing must write nothing a dry run could
// leave behind.
func TestExistingRecordsNothing(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo(), reviews: []review.GHReview{myPending()}}
	svc, _ := newService(t, ghc, newFakeGit())
	notesFor(t, svc, unlisted)

	if _, err := svc.Existing(context.Background(), unlisted); err != nil {
		t.Fatalf("Existing: %v", err)
	}

	if records := stored(t, svc); len(records) != 0 {
		t.Errorf("Existing recorded %d records, want none", len(records))
	}
}

func TestPrepareStoresTheIntent(t *testing.T) {
	for _, intent := range []review.Intent{review.IntentReview, review.IntentAppend, review.IntentOverwrite} {
		t.Run(string(intent), func(t *testing.T) {
			ghc := &fakeGH{login: "haacked", info: prInfo()}
			svc, _ := newService(t, ghc, newFakeGit())

			rec := prepareWith(t, svc, unlisted, "claude", intent)

			if rec.Intent != intent {
				t.Errorf("record intent = %q, want %q", rec.Intent, intent)
			}
			if got := storedByID(t, svc, rec.ID).Intent; got != intent {
				t.Errorf("stored intent = %q, want %q", got, intent)
			}
			if rec.State != review.StatePreparing {
				t.Errorf("state = %q, want preparing: a review intent waits for its launch", rec.State)
			}
		})
	}
}

func TestLaunchingAnAppendPassesTheFlag(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := prepareWith(t, svc, unlisted, "claude", review.IntentAppend)

	_, spec, err := svc.LaunchSpec(context.Background(), rec)
	if err != nil {
		t.Fatalf("LaunchSpec: %v", err)
	}

	if !strings.Contains(spec.String(), "/review-code "+rec.URL+" --draft") || !strings.Contains(spec.String(), "--append") {
		t.Errorf("spec = %s, want the review with --append", spec)
	}
}

// With my pending review there, the adopted row is that draft, and s submits it
// through the submit screen like any other.
func TestPrepareToAskAdoptsMyPendingReviewAsADraft(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo(), reviews: []review.GHReview{myPending()}}
	svc, _ := newService(t, ghc, newFakeGit())
	notesFor(t, svc, unlisted)

	rec := prepareWith(t, svc, unlisted, "claude", review.IntentAsk)

	if rec.State != review.StateDrafted || rec.ReviewID != myPendingID {
		t.Fatalf("record is %q with review %d, want drafted holding review %d", rec.State, rec.ReviewID, myPendingID)
	}
	if !rec.Submittable() {
		t.Error("the adopted draft is not submittable")
	}
	if rec.Intent != review.IntentAsk {
		t.Errorf("intent = %q, want ask", rec.Intent)
	}
	if got := storedByID(t, svc, rec.ID); got.State != review.StateDrafted || got.ReviewID != myPendingID {
		t.Errorf("stored record is %q with review %d, want the adopted draft", got.State, got.ReviewID)
	}
}

// A pending draft and an older submitted review can both be mine. The draft is
// what s submits, so it wins over the submission Decide would report first.
func TestPrepareToAskPrefersMyPendingReviewOverASubmittedOne(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo(), reviews: []review.GHReview{mySubmitted(), myPending()}}
	svc, _ := newService(t, ghc, newFakeGit())
	notesFor(t, svc, unlisted)

	rec := prepareWith(t, svc, unlisted, "claude", review.IntentAsk)

	if rec.State != review.StateDrafted || rec.ReviewID != myPendingID {
		t.Errorf("record is %q with review %d, want drafted holding review %d", rec.State, rec.ReviewID, myPendingID)
	}
}

func TestPrepareToAskWithOnlyNotesMarksTheRecordReviewed(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	notesFor(t, svc, unlisted)

	rec := prepareWith(t, svc, unlisted, "claude", review.IntentAsk)

	if rec.State != review.StateReviewed {
		t.Errorf("state = %q, want reviewed", rec.State)
	}
	if rec.ReviewID != 0 {
		t.Errorf("review id = %d, want none: there is no draft to submit", rec.ReviewID)
	}
	if got := storedByID(t, svc, rec.ID).State; got != review.StateReviewed {
		t.Errorf("stored state = %q, want reviewed", got)
	}
}

// Decide would read my submitted review as this row's own submission unless it
// is in the snapshot, and a submitted row archives and deletes the clone. The
// snapshot is what lets the next append on this row start clean.
func TestPrepareToAskOverMySubmittedReviewSnapshotsIt(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo(), reviews: []review.GHReview{mySubmitted()}}
	svc, _ := newService(t, ghc, newFakeGit())

	rec := prepareWith(t, svc, unlisted, "claude", review.IntentAsk)

	if rec.State != review.StateReviewed {
		t.Errorf("state = %q, want reviewed rather than an archived submission", rec.State)
	}
	if !slices.Contains(rec.PriorReviewIDs, int64(mySubmittedID)) {
		t.Errorf("prior review ids = %v, want my submitted review %d", rec.PriorReviewIDs, mySubmittedID)
	}
	if _, err := os.Stat(rec.Dir); err != nil {
		t.Errorf("the clone is gone: %v", err)
	}
}

// Nothing provisions a row after Prepare, so an ask gets its clone here too. The
// clone is also what lets the agent read the files the notes cite.
func TestPrepareToAskStillClonesATier2PullRequest(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	gitc := newFakeGit()
	svc, paths := newService(t, ghc, gitc)
	notesFor(t, svc, unlisted)

	rec := prepareWith(t, svc, unlisted, "claude", review.IntentAsk)

	if want := paths.CloneDir("haacked", "docket", 7); rec.Dir != want {
		t.Errorf("dir = %q, want the clone %q", rec.Dir, want)
	}
	if gitc.checkedOut != "haacked/a-thing" {
		t.Errorf("clone is on %q, want the head branch", gitc.checkedOut)
	}
}

func TestPrepareToAskRefusesAPullRequestAlreadyOpen(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	notesFor(t, svc, unlisted)
	prepareWith(t, svc, unlisted, "claude", review.IntentReview)

	if _, _, err := svc.Prepare(context.Background(), unlisted, "claude", review.ModeInteractive, review.IntentAsk); err == nil {
		t.Fatal("Prepare adopted a pull request that already has an open record")
	}
}

// An adopted record has no review session. Starting review-code fresh would pass
// neither --append nor --overwrite, and it would stop at the prompt the user
// already answered.
func TestResumeSpecRefusesAnAdoptedRecordWithNoReviewSession(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	notesFor(t, svc, unlisted)
	rec := prepareWith(t, svc, unlisted, "claude", review.IntentAsk)

	if _, spec, err := svc.ResumeSpec(context.Background(), rec); err == nil {
		t.Errorf("ResumeSpec returned %s, want a refusal", spec)
	}
	if _, err := svc.ExplainResume(rec); err == nil {
		t.Error("ExplainResume reported a command a real resume refuses")
	}
}

func TestAskSpecRunsTheQuestionsNotAReview(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	notesFor(t, svc, unlisted)
	rec := prepareWith(t, svc, unlisted, "claude", review.IntentAsk)
	svc.Now = func() time.Time { return start.Add(time.Hour) }

	asked, spec, err := svc.AskSpec(rec)
	if err != nil {
		t.Fatalf("AskSpec: %v", err)
	}

	line := spec.String()
	if strings.Contains(line, "/review-code ") || strings.Contains(line, "--draft") {
		t.Errorf("spec = %s, want the Q&A session, not a review", line)
	}
	if spec.Dir != rec.Dir {
		t.Errorf("dir = %q, want the record's %q", spec.Dir, rec.Dir)
	}
	if !strings.Contains(line, rec.NotesPath) {
		t.Errorf("spec = %s, want the prompt to name the notes at %s", line, rec.NotesPath)
	}
	if asked.AskSessionID == "" || !strings.Contains(line, asked.AskSessionID) {
		t.Errorf("spec = %s, want it to carry the recorded session id %q", line, asked.AskSessionID)
	}
	if !asked.AskStartedAt.Equal(start.Add(time.Hour)) {
		t.Errorf("ask started at %v, want %v", asked.AskStartedAt, start.Add(time.Hour))
	}
	if asked.State != review.StateReviewed {
		t.Errorf("state = %q, want reviewed: an ask reviews nothing", asked.State)
	}
	got := storedByID(t, svc, rec.ID)
	if got.AskSessionID != asked.AskSessionID {
		t.Errorf("stored ask session id = %q, want %q recorded before the session runs", got.AskSessionID, asked.AskSessionID)
	}
	if got.State != review.StateReviewed {
		t.Errorf("stored state = %q, want reviewed", got.State)
	}
}

func TestAskSpecOnADraftedRowKeepsTheDraftSubmittable(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo(), reviews: []review.GHReview{myPending()}}
	svc, _ := newService(t, ghc, newFakeGit())
	notesFor(t, svc, unlisted)
	rec := prepareWith(t, svc, unlisted, "claude", review.IntentAsk)

	asked, spec, err := svc.AskSpec(rec)
	if err != nil {
		t.Fatalf("AskSpec: %v", err)
	}

	if asked.State != review.StateDrafted || asked.ReviewID != myPendingID || !asked.Submittable() {
		t.Errorf("record is %q with review %d, want the draft left submittable", asked.State, asked.ReviewID)
	}
	if strings.Contains(spec.String(), "--draft") {
		t.Errorf("spec = %s, want no review", spec)
	}
}

// c works on any open row with notes, not only an adopted one. The review
// session on that row stays where enter can resume it.
func TestAskSpecOnAReviewedRowLeavesTheReviewSessionAlone(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := prepareWith(t, svc, unlisted, "claude", review.IntentReview)
	rec, _, err := svc.LaunchSpec(context.Background(), rec)
	if err != nil {
		t.Fatalf("LaunchSpec: %v", err)
	}
	ghc.reviews = []review.GHReview{myPending()}
	rec, err = svc.AfterExit(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}
	notesFor(t, svc, unlisted)
	reviewSession := rec.SessionID

	asked, spec, err := svc.AskSpec(rec)
	if err != nil {
		t.Fatalf("AskSpec: %v", err)
	}

	if strings.Contains(spec.String(), "--resume") {
		t.Errorf("spec = %s, want a new Q&A session, not the review's conversation", spec)
	}
	if asked.AskSessionID == "" || asked.AskSessionID == reviewSession {
		t.Errorf("ask session id = %q, want a fresh one distinct from the review's %q", asked.AskSessionID, reviewSession)
	}
	if asked.SessionID != reviewSession {
		t.Errorf("review session id = %q, want %q kept for enter to resume", asked.SessionID, reviewSession)
	}
	if asked.State != review.StateDrafted || asked.ReviewID != myPendingID {
		t.Errorf("record is %q with review %d, want the draft left as it was", asked.State, asked.ReviewID)
	}
}

func TestAskSpecResumesTheQuestionSessionItStartedBefore(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := adopted(t, svc, unlisted)
	rec, err := svc.AfterAsk(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterAsk: %v", err)
	}

	again, spec, err := svc.AskSpec(rec)
	if err != nil {
		t.Fatalf("AskSpec: %v", err)
	}

	if !strings.Contains(spec.String(), "--resume "+rec.AskSessionID) {
		t.Errorf("spec = %s, want it to resume the Q&A session %q", spec, rec.AskSessionID)
	}
	if again.AskSessionID != rec.AskSessionID {
		t.Errorf("ask session id = %q, want %q kept", again.AskSessionID, rec.AskSessionID)
	}
}

func TestAskSpecRefusesARecordWithNoNotes(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo(), reviews: []review.GHReview{myPending()}}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := prepareWith(t, svc, unlisted, "claude", review.IntentAsk)
	before := stored(t, svc)

	if _, _, err := svc.AskSpec(rec); err == nil {
		t.Error("AskSpec opened a session about notes that are not there")
	}
	if after := stored(t, svc); !reflect.DeepEqual(after, before) {
		t.Errorf("a refused ask changed the index:\nbefore %+v\nafter  %+v", before, after)
	}
}

func TestAskSpecRefusesAMissingDirectory(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	notesFor(t, svc, unlisted)
	rec := prepareWith(t, svc, unlisted, "claude", review.IntentAsk)
	if err := os.RemoveAll(rec.Dir); err != nil {
		t.Fatal(err)
	}

	if _, _, err := svc.AskSpec(rec); err == nil {
		t.Error("AskSpec accepted a directory that is gone")
	}
}

// Nothing posted in the session reads as unreviewed to Decide, which an adopted
// row must not become.
func TestAfterAskKeepsAReviewedRowWhenNothingWasPosted(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo(), reviews: []review.GHReview{mySubmitted()}}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := adopted(t, svc, unlisted)

	done, err := svc.AfterAsk(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterAsk: %v", err)
	}

	if done.State != review.StateReviewed {
		t.Errorf("state = %q, want reviewed", done.State)
	}
	if got := storedByID(t, svc, rec.ID).State; got != review.StateReviewed {
		t.Errorf("stored state = %q, want reviewed", got)
	}
	if _, err := os.Stat(rec.Dir); err != nil {
		t.Errorf("the clone is gone after an ask: %v", err)
	}
}

// The user can ask the agent to submit the draft once their questions are
// answered, and the review is then done.
func TestAfterAskArchivesAReviewSubmittedDuringTheSession(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo(), reviews: []review.GHReview{myPending()}}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := adopted(t, svc, unlisted)
	if rec.State != review.StateDrafted {
		t.Fatalf("adopted state = %q, want drafted", rec.State)
	}

	at := start.Add(3 * time.Minute)
	ghc.reviews = []review.GHReview{
		{ID: myPendingID, User: review.GHUser{Login: "haacked"}, State: "COMMENTED", SubmittedAt: &at},
	}

	done, err := svc.AfterAsk(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterAsk: %v", err)
	}

	if done.State != review.StateArchived {
		t.Errorf("state = %q, want the submitted review archived", done.State)
	}
	if got := storedByID(t, svc, rec.ID); got.State != review.StateArchived || got.AskSessionID != rec.AskSessionID {
		t.Errorf("stored record is %q with ask session %q, want archived with %q", got.State, got.AskSessionID, rec.AskSessionID)
	}
}

func TestAfterAskPicksUpADraftPostedDuringTheSession(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo(), reviews: []review.GHReview{mySubmitted()}}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := adopted(t, svc, unlisted)
	ghc.reviews = append(ghc.reviews, myPending())

	done, err := svc.AfterAsk(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterAsk: %v", err)
	}

	if done.State != review.StateDrafted || done.ReviewID != myPendingID {
		t.Errorf("record is %q with review %d, want drafted with %d", done.State, done.ReviewID, myPendingID)
	}
	if !done.Submittable() {
		t.Error("the draft posted in the session cannot be submitted from the dashboard")
	}
}

// The session is recorded even when GitHub cannot be read, so the next c still
// resumes it.
func TestAfterAskKeepsTheSessionWhenGitHubFails(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := adopted(t, svc, unlisted)
	ghc.reviewErr = errors.New("dial tcp: lookup api.github.com: no such host")

	done, err := svc.AfterAsk(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterAsk: %v", err)
	}

	got := storedByID(t, svc, rec.ID)
	if got.State != review.StateReviewed || got.AskSessionID != rec.AskSessionID {
		t.Errorf("stored record is %q with ask session %q, want reviewed with %q", got.State, got.AskSessionID, rec.AskSessionID)
	}
	if !strings.Contains(done.Err, "no such host") || !strings.Contains(got.Err, "no such host") {
		t.Errorf("err = %q (stored %q), want GitHub's failure on the row", done.Err, got.Err)
	}
}

func TestAfterAskRecordsTheChildsFailure(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := adopted(t, svc, unlisted)

	done, err := svc.AfterAsk(context.Background(), rec, errors.New("exit status 130"))
	if err != nil {
		t.Fatalf("AfterAsk: %v", err)
	}

	if done.Err == "" {
		t.Error("the child's failure is not on the record")
	}
	if got := storedByID(t, svc, rec.ID).Err; got == "" {
		t.Error("the child's failure is not in the index")
	}
}

// A Q&A session leaves the row open to other instances, which may close it
// before the session ends.
func TestAfterAskKeepsWhatAnotherInstanceRecorded(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := adopted(t, svc, unlisted)
	elsewhere := rec
	elsewhere.State = review.StateArchived
	if err := svc.append(elsewhere); err != nil {
		t.Fatal(err)
	}
	before := ghc.reads

	done, err := svc.AfterAsk(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterAsk: %v", err)
	}
	if done.State != review.StateArchived {
		t.Errorf("state = %q, want the archive another instance recorded", done.State)
	}
	if got := storedByID(t, svc, rec.ID); got.State != review.StateArchived || got.AskSessionID != rec.AskSessionID {
		t.Errorf("stored record is %q with ask session %q, want archived with %q", got.State, got.AskSessionID, rec.AskSessionID)
	}
	if ghc.reads != before {
		t.Error("AfterAsk read GitHub for a row another instance had closed")
	}
}

// codex names its own session, and the Q&A session dispatches no reviewers, so
// the oldest rollout after the ask started is the one to resume.
func TestAfterAskCapturesTheCodexSessionID(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, sessions := codexService(t, ghc)
	notesFor(t, svc, unlisted)
	rec := prepareWith(t, svc, unlisted, "codex", review.IntentAsk)
	svc.Now = func() time.Time { return start.Add(time.Hour) }
	rec, _, err := svc.AskSpec(rec)
	if err != nil {
		t.Fatalf("AskSpec: %v", err)
	}
	// The review's own rollout is older than the ask's, so a capture that cut off
	// at the review's start would take it.
	writeRollout(t, sessions, "0199a1b2-0000-7000-8000-0000000be1e0", rec.Dir, rec.StartedAt.Add(time.Minute))
	writeRollout(t, sessions, "0199a1b2-0000-7000-8000-00000000a5c0", rec.Dir, rec.AskStartedAt.Add(time.Minute))

	done, err := svc.AfterAsk(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterAsk: %v", err)
	}

	if done.AskSessionID != "0199a1b2-0000-7000-8000-00000000a5c0" {
		t.Errorf("ask session id = %q, want the one codex recorded", done.AskSessionID)
	}
	if got := storedByID(t, svc, rec.ID).AskSessionID; got != done.AskSessionID {
		t.Errorf("stored ask session id = %q, want %q", got, done.AskSessionID)
	}
}

func rereviewed(t *testing.T, svc *Service, rec review.Record, intent review.Intent) (review.Record, string) {
	t.Helper()
	rec, err := svc.Rereview(context.Background(), rec, intent, review.ModeInteractive)
	if err != nil {
		t.Fatalf("Rereview: %v", err)
	}
	rec, spec, err := svc.LaunchSpec(context.Background(), rec)
	if err != nil {
		t.Fatalf("LaunchSpec: %v", err)
	}
	return rec, spec.String()
}

func TestRereviewLaunchesAFreshReviewWithTheChosenFlag(t *testing.T) {
	tests := []struct {
		intent review.Intent
		flag   string
	}{
		{intent: review.IntentAppend, flag: "--append"},
		{intent: review.IntentOverwrite, flag: "--overwrite"},
	}

	for _, tt := range tests {
		t.Run(string(tt.intent), func(t *testing.T) {
			ghc := &fakeGH{login: "haacked", info: prInfo()}
			svc, _ := newService(t, ghc, newFakeGit())
			rec := adopted(t, svc, unlisted)
			svc.Now = func() time.Time { return start.Add(2 * time.Hour) }

			again, line := rereviewed(t, svc, rec, tt.intent)

			if !strings.Contains(line, "/review-code "+rec.URL+" --draft") || !strings.Contains(line, tt.flag) {
				t.Errorf("spec = %s, want the review with %s", line, tt.flag)
			}
			if strings.Contains(line, "--resume") {
				t.Errorf("spec = %s, want a fresh start, not a resume", line)
			}
			if again.SessionID == "" || again.SessionID == rec.AskSessionID || !strings.Contains(line, again.SessionID) {
				t.Errorf("session id = %q (ask was %q), want a new one the spec carries", again.SessionID, rec.AskSessionID)
			}
			if again.Intent != tt.intent {
				t.Errorf("intent = %q, want %q", again.Intent, tt.intent)
			}
			if again.State != review.StateReviewing {
				t.Errorf("state = %q, want reviewing", again.State)
			}
			if !again.StartedAt.Equal(start.Add(2 * time.Hour)) {
				t.Errorf("started at %v, want the re-review's own clock", again.StartedAt)
			}
			if got := storedByID(t, svc, rec.ID); got.State != review.StateReviewing || got.Intent != tt.intent {
				t.Errorf("stored record is %q with intent %q, want it recorded before the command runs", got.State, got.Intent)
			}
		})
	}
}

// A re-review drops the old review session so the fresh start passes the new
// flag. Resuming the old conversation would never reach review-code's flags.
func TestRereviewDropsTheOldReviewSession(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := launched(t, svc, unlisted)
	rec.State = review.StateDrafted
	old := rec.SessionID

	again, line := rereviewed(t, svc, rec, review.IntentOverwrite)

	if again.SessionID == old || strings.Contains(line, old) {
		t.Errorf("spec = %s, want a session other than the old %q", line, old)
	}
}

func TestRereviewRefusesAnIntentThatIsNotAReReview(t *testing.T) {
	for _, intent := range []review.Intent{review.IntentAsk, review.IntentReview} {
		t.Run(string(intent), func(t *testing.T) {
			ghc := &fakeGH{login: "haacked", info: prInfo()}
			svc, _ := newService(t, ghc, newFakeGit())
			rec := adopted(t, svc, unlisted)

			if _, err := svc.Rereview(context.Background(), rec, intent, review.ModeInteractive); err == nil {
				t.Errorf("Rereview accepted %q, want only append or overwrite", intent)
			}
		})
	}
}

// The record may be days old. A review submitted since then has to be in the
// snapshot or the re-review reads it as its own submission.
func TestRereviewResnapshotsTheSubmittedReviews(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := adopted(t, svc, unlisted)

	between := start.Add(time.Hour)
	ghc.reviews = []review.GHReview{
		{ID: 77, User: review.GHUser{Login: "haacked"}, State: "APPROVED", SubmittedAt: &between},
	}
	svc.Now = func() time.Time { return start.Add(2 * time.Hour) }

	again, _ := rereviewed(t, svc, rec, review.IntentAppend)

	if !slices.Contains(again.PriorReviewIDs, int64(77)) {
		t.Errorf("prior review ids = %v, want the review submitted since the record was made", again.PriorReviewIDs)
	}
}

func TestRereviewRefusesWhenTheReviewSnapshotCannotRefresh(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := adopted(t, svc, unlisted)
	ghc.reviewErr = errors.New("dial tcp: lookup api.github.com: no such host")

	if _, err := svc.Rereview(context.Background(), rec, review.IntentAppend, review.ModeInteractive); err == nil {
		t.Fatal("Rereview went ahead with a stale review snapshot")
	}

	got := storedByID(t, svc, rec.ID)
	if got.Intent != review.IntentAsk || got.State != review.StateReviewed {
		t.Errorf("stored record is %q with intent %q, want the refused re-review to leave it reviewed with intent ask", got.State, got.Intent)
	}
}

func TestALaunchAfterRereviewRefusesAMissingDirectory(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := adopted(t, svc, unlisted)
	if err := os.RemoveAll(rec.Dir); err != nil {
		t.Fatal(err)
	}

	again, err := svc.Rereview(context.Background(), rec, review.IntentAppend, review.ModeInteractive)
	if err != nil {
		t.Fatalf("Rereview: %v", err)
	}
	if _, _, err := svc.LaunchSpec(context.Background(), again); err == nil {
		t.Error("a re-review launched in a directory that is gone")
	}
}

// Once a row is re-reviewed its session is meant to post again, so the exit
// reads GitHub the way any review's does.
func TestAfterExitOfARereviewDetectsAgain(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := adopted(t, svc, unlisted)
	rec, _ = rereviewed(t, svc, rec, review.IntentAppend)
	ghc.reviews = []review.GHReview{myPending()}

	done, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}

	if done.State != review.StateDrafted || done.ReviewID != myPendingID {
		t.Errorf("record is %q with review %d, want the new draft detected", done.State, done.ReviewID)
	}
}

func TestExplainWithAnAppendReportsTheFlag(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())

	_, spec, err := svc.Explain(context.Background(), unlisted, "claude", review.ModeInteractive, review.IntentAppend)
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}

	if !strings.Contains(spec.String(), "--append") {
		t.Errorf("spec = %s, want the --append a real start would pass", spec)
	}
	if records := stored(t, svc); len(records) != 0 {
		t.Errorf("Explain recorded %d records, want none", len(records))
	}
}

func TestExplainWithAnAskReportsTheQuestionSession(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	gitc := newFakeGit()
	svc, paths := newService(t, ghc, gitc)
	notes := notesFor(t, svc, unlisted)

	plan, spec, err := svc.Explain(context.Background(), unlisted, "claude", review.ModeInteractive, review.IntentAsk)
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}

	line := spec.String()
	if strings.Contains(line, "/review-code ") || strings.Contains(line, "--draft") {
		t.Errorf("spec = %s, want the Q&A session, not a review", line)
	}
	if !strings.Contains(line, notes) {
		t.Errorf("spec = %s, want the prompt to name %s", line, notes)
	}
	if plan.Dir == "" {
		t.Error("the plan names no directory, want the tier and directory the ask would run in")
	}
	if records := stored(t, svc); len(records) != 0 {
		t.Errorf("Explain recorded %d records, want none", len(records))
	}
	if _, err := os.Stat(paths.CloneDir("haacked", "docket", 7)); !os.IsNotExist(err) {
		t.Error("Explain provisioned a clone")
	}
}

func TestExplainAskReportsWithoutRecording(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	notesFor(t, svc, unlisted)
	rec := launched(t, svc, unlisted)
	rec.State = review.StateDrafted
	before := stored(t, svc)

	spec, err := svc.ExplainAsk(rec)
	if err != nil {
		t.Fatalf("ExplainAsk: %v", err)
	}

	if !strings.Contains(spec.String(), rec.NotesPath) || strings.Contains(spec.String(), "--draft") {
		t.Errorf("spec = %s, want the Q&A session over %s", spec, rec.NotesPath)
	}
	if after := stored(t, svc); !reflect.DeepEqual(after, before) {
		t.Errorf("ExplainAsk changed the index:\nbefore %+v\nafter  %+v", before, after)
	}
}

func TestExplainRereviewReportsWithoutRecording(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := adopted(t, svc, unlisted)
	before := stored(t, svc)
	readsBefore := ghc.reads

	spec, err := svc.ExplainRereview(context.Background(), rec, review.IntentOverwrite, review.ModeInteractive)
	if err != nil {
		t.Fatalf("ExplainRereview: %v", err)
	}

	line := spec.String()
	if !strings.Contains(line, "/review-code "+rec.URL+" --draft") || !strings.Contains(line, "--overwrite") {
		t.Errorf("spec = %s, want the review with --overwrite", line)
	}
	if strings.Contains(line, "--resume") {
		t.Errorf("spec = %s, want a fresh start", line)
	}
	if after := stored(t, svc); !reflect.DeepEqual(after, before) {
		t.Errorf("ExplainRereview changed the index:\nbefore %+v\nafter  %+v", before, after)
	}
	if ghc.reads != readsBefore {
		t.Errorf("ExplainRereview read GitHub's reviews %d times, want none", ghc.reads-readsBefore)
	}
}

// Another docket instance can hold an interactive review of the same record. A
// second session would write the same notes while that one is still writing
// them.
func TestAReviewStillRunningRefusesAskAndReReview(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	notesFor(t, svc, unlisted)
	rec := launched(t, svc, unlisted)

	if _, _, err := svc.AskSpec(rec); err == nil || !strings.Contains(err.Error(), "is still reviewing") {
		t.Errorf("AskSpec on a running review: err = %v", err)
	}
	if _, err := svc.Rereview(context.Background(), rec, review.IntentAppend, review.ModeInteractive); err == nil || !strings.Contains(err.Error(), "is still reviewing") {
		t.Errorf("Rereview on a running review: err = %v", err)
	}
}

// Another instance may still be provisioning the clone of a preparing record.
func TestARecordStillPreparingRefusesAskAndReReview(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := adopted(t, svc, unlisted)
	rec.State = review.StatePreparing
	if err := svc.append(rec); err != nil {
		t.Fatal(err)
	}
	before := stored(t, svc)

	if _, _, err := svc.AskSpec(rec); err == nil || !strings.Contains(err.Error(), "is still preparing") {
		t.Errorf("AskSpec on a preparing record: err = %v", err)
	}
	if _, err := svc.Rereview(context.Background(), rec, review.IntentAppend, review.ModeInteractive); err == nil || !strings.Contains(err.Error(), "is still preparing") {
		t.Errorf("Rereview on a preparing record: err = %v", err)
	}
	if after := stored(t, svc); !reflect.DeepEqual(after, before) {
		t.Errorf("a refused ask or re-review changed the index:\nbefore %+v\nafter  %+v", before, after)
	}
}

// Rereview writes the record before the launch marks it reviewing. In that gap
// the old draft must not look submittable to another instance, because the new
// review is about to replace it.
func TestRereviewRecordsNoSubmittableDraftBeforeTheLaunch(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo(), reviews: []review.GHReview{myPending()}}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := adopted(t, svc, unlisted)
	if !rec.Submittable() {
		t.Fatalf("adopted record is %q with review %d, want a submittable draft", rec.State, rec.ReviewID)
	}

	if _, err := svc.Rereview(context.Background(), rec, review.IntentAppend, review.ModeInteractive); err != nil {
		t.Fatalf("Rereview: %v", err)
	}

	got := storedByID(t, svc, rec.ID)
	if got.Submittable() || got.ReviewID != 0 {
		t.Errorf("stored record is %q with review %d, want no draft to submit", got.State, got.ReviewID)
	}
	if !got.InProgress() {
		t.Errorf("stored state = %q, want it in progress so ask and re-review wait", got.State)
	}
}
