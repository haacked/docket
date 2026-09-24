package review_test

import (
	"testing"

	"github.com/haacked/docket/internal/core/review"
)

// A record has an empty state when docket wrote it before reading the pull
// request's state, or when a fake never set one. Reading it as closed would
// archive every such row on its next detection.
func TestOnlyMergedAndClosedPullRequestsAreClosed(t *testing.T) {
	tests := []struct {
		state review.PRState
		want  bool
	}{
		{state: review.PRMerged, want: true},
		{state: review.PRClosed, want: true},
		{state: review.PROpen, want: false},
		{state: "", want: false},
	}

	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			if got := tt.state.Closed(); got != tt.want {
				t.Errorf("%q.Closed() = %v, want %v", tt.state, got, tt.want)
			}
		})
	}
}

func TestARecordIsFinishedWhenItsPullRequestClosedWithNothingPending(t *testing.T) {
	tests := []struct {
		name  string
		state review.State
		pr    review.PRState
		want  bool
	}{
		{name: "unreviewed on a merged pull request", state: review.StateUnreviewed, pr: review.PRMerged, want: true},
		{name: "unreviewed on a closed pull request", state: review.StateUnreviewed, pr: review.PRClosed, want: true},
		{name: "reviewed on a merged pull request", state: review.StateReviewed, pr: review.PRMerged, want: true},
		{name: "unreviewed on an open pull request", state: review.StateUnreviewed, pr: review.PROpen, want: false},
		{name: "unreviewed with an unknown state", state: review.StateUnreviewed, pr: "", want: false},
		// GitHub accepts a review on a merged pull request. Only the user can
		// choose between submitting the draft and dropping it.
		{name: "drafted on a merged pull request", state: review.StateDrafted, pr: review.PRMerged, want: false},
		{name: "reviewing on a merged pull request", state: review.StateReviewing, pr: review.PRMerged, want: false},
		{name: "preparing on a merged pull request", state: review.StatePreparing, pr: review.PRMerged, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := review.Record{State: tt.state, PRState: tt.pr}
			if got := rec.Finished(); got != tt.want {
				t.Errorf("Finished() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTheUnreviewedStateReadsAsNoReviewPosted(t *testing.T) {
	if got := review.StateUnreviewed.Label(); got != "no review posted" {
		t.Errorf("label = %q, want no review posted", got)
	}
	if got := review.StateDrafted.Label(); got != "drafted" {
		t.Errorf("label = %q, want drafted", got)
	}
}

func TestAPullRequestStateReadsAsALowercaseWord(t *testing.T) {
	if got := review.PRMerged.Label(); got != "merged" {
		t.Errorf("label = %q, want merged", got)
	}
}
