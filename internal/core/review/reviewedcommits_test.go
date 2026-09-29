package review_test

import (
	"slices"
	"testing"
	"time"

	"github.com/haacked/docket/internal/core/review"
)

const (
	head  = "db95d58da7848652151cfe8c35ffd849eb303492"
	older = "7c18bfd69b9151e50a913ed948fef9bbc5ba2848"
)

func onCommit(r review.GHReview, commit string) review.GHReview {
	r.CommitID = commit
	return r
}

func withBody(r review.GHReview, body string) review.GHReview {
	r.Body = body
	return r
}

func TestReviewedCommits(t *testing.T) {
	tests := []struct {
		name    string
		reviews []review.GHReview
		want    []string
	}{
		{
			name: "no reviews",
		},
		{
			name:    "my submitted review",
			reviews: []review.GHReview{onCommit(minePriorSubmitted, head)},
			want:    []string{head},
		},
		{
			name:    "two reviews of mine on different commits",
			reviews: []review.GHReview{onCommit(minePriorSubmitted, older), onCommit(mineSubmittedFresh, head)},
			want:    []string{older, head},
		},
		{
			name:    "only my pending review",
			reviews: []review.GHReview{onCommit(minePending, head)},
		},
		{
			name:    "only someone else's review",
			reviews: []review.GHReview{onCommit(otherSubmittedFresh, head)},
		},
		{
			name:    "my login in other letters",
			reviews: []review.GHReview{onCommit(ghReview(1, "Haacked", "APPROVED", base), head)},
			want:    []string{head},
		},
		{
			name:    "my comment review with a summary",
			reviews: []review.GHReview{withBody(onCommit(ghReview(1, me, "COMMENTED", base), head), "Looks close.")},
			want:    []string{head},
		},
		{
			// GitHub records a reply in a review thread as a COMMENTED review with
			// no body, on whatever the head was when the reply went up.
			name:    "my reply in a review thread",
			reviews: []review.GHReview{onCommit(ghReview(1, me, "COMMENTED", base), head)},
		},
		{
			name:    "my dismissed review",
			reviews: []review.GHReview{onCommit(ghReview(1, me, "DISMISSED", base.Add(-time.Hour)), head)},
			want:    []string{head},
		},
		{
			// An empty commit would match a head docket could not read.
			name:    "my review with no commit",
			reviews: []review.GHReview{minePriorSubmitted},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := review.ReviewedCommits(tt.reviews, me); !slices.Equal(got, tt.want) {
				t.Errorf("ReviewedCommits = %v, want %v", got, tt.want)
			}
		})
	}
}
