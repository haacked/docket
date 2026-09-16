package session

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/review"
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
			name:  "still reviewing",
			spoil: func(rec review.Record) review.Record { rec.State = review.StateReviewing; return rec },
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
