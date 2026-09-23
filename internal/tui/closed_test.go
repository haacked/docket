package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/tui/msg"
)

func TestAMergedPullRequestRefusesReReview(t *testing.T) {
	rec := draftedRecord()
	rec.PRState = review.PRMerged

	next, cmd := withRecords(rec).Update(msg.OpenRereview{ID: rec.ID})
	got := next.(App)

	if cmd != nil {
		t.Error("re-review of a merged pull request ran a command")
	}
	if got.screen != msg.Dashboard || !strings.Contains(got.status, "is merged") {
		t.Errorf("screen = %v, status = %q, want the dashboard saying the pull request merged", got.screen, got.status)
	}
}

func TestDescribeARowArchivedBecauseItsPullRequestMerged(t *testing.T) {
	rec := draftedRecord()
	rec.State = review.StateArchived
	rec.PRState = review.PRMerged

	got := describe(rec)
	if !strings.Contains(got, "merged") || strings.Contains(got, "submitted") {
		t.Errorf("describe(archived on merge) = %q, want it to name the merge and not a submission", got)
	}
}

func TestDescribeASubmittedReviewOnAMergedPullRequest(t *testing.T) {
	at := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	rec := draftedRecord()
	rec.State = review.StateArchived
	rec.PRState = review.PRMerged
	rec.SubmittedAt = &at

	if got := describe(rec); !strings.Contains(got, "submitted") {
		t.Errorf("describe(submitted on a merged pull request) = %q, want the submission", got)
	}
}

// A draft on a merged pull request stays open for the user to submit or drop.
// The status line has to say why the row did not archive.
func TestDescribeADraftOnAMergedPullRequest(t *testing.T) {
	rec := draftedRecord()
	rec.PRState = review.PRMerged

	if got := describe(rec); !strings.Contains(got, "merged") || !strings.Contains(got, "s to submit") {
		t.Errorf("describe(draft on a merged pull request) = %q", got)
	}
}

// Five reviews of mine can sit on a pull request whose last session posted
// nothing. The status line must not say that there is no review of mine.
func TestDescribeAnUnreviewedRowSaysTheSessionPostedNothing(t *testing.T) {
	rec := draftedRecord()
	rec.State = review.StateUnreviewed
	rec.ReviewID = 0

	got := describe(rec)
	if strings.Contains(got, "no review of yours") {
		t.Errorf("describe(unreviewed) = %q, which denies reviews that may be there", got)
	}
	if !strings.Contains(got, "without posting a review") {
		t.Errorf("describe(unreviewed) = %q, want it to say the session posted nothing", got)
	}
}
