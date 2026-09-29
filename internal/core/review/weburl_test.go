package review_test

import (
	"testing"

	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/review"
)

func TestWebURLOpensADraftAtItsReview(t *testing.T) {
	ref := pr.Ref{Org: "haacked", Repo: "docket", Number: 7}

	tests := []struct {
		name string
		rec  review.Record
		want string
	}{
		{
			name: "a drafted review",
			rec:  review.Record{Ref: ref, State: review.StateDrafted, ReviewID: 4321},
			want: "https://github.com/haacked/docket/pull/7#pullrequestreview-4321",
		},
		{
			name: "a submitted review",
			rec:  review.Record{Ref: ref, State: review.StateReviewed, ReviewID: 4321},
			want: "https://github.com/haacked/docket/pull/7",
		},
		{
			name: "a review still running",
			rec:  review.Record{Ref: ref, State: review.StateReviewing},
			want: "https://github.com/haacked/docket/pull/7",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.rec.WebURL(); got != tt.want {
				t.Errorf("WebURL() = %q, want %q", got, tt.want)
			}
		})
	}
}
