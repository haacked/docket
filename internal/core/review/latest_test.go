package review_test

import (
	"testing"
	"time"

	"github.com/haacked/docket/internal/core/review"
)

// mineOn is a review of mine submitted at offset from base on commit, with a
// summary, so that it counts as a real review whatever its state.
func mineOn(id int64, state, commit string, offset time.Duration) review.GHReview {
	return withBody(onCommit(ghReview(id, me, state, base.Add(offset)), commit), "Some thoughts.")
}

// bareReply is how GitHub records a reply in a review thread: a COMMENTED
// review with no body, on whatever the head was when the reply went up.
func bareReply(id int64, commit string, offset time.Duration) review.GHReview {
	return onCommit(ghReview(id, me, review.StateCommented, base.Add(offset)), commit)
}

func pendingOn(id int64, commit string) review.GHReview {
	return onCommit(ghReview(id, me, review.StatePending, time.Time{}), commit)
}

func TestLatestReview(t *testing.T) {
	tests := []struct {
		name    string
		reviews []review.GHReview
		wantID  int64
		wantOK  bool
	}{
		{
			name: "no reviews",
		},
		{
			name:    "only replies in review threads",
			reviews: []review.GHReview{bareReply(1, older, -2*time.Hour), bareReply(2, head, -time.Hour)},
		},
		{
			name:    "only my pending review",
			reviews: []review.GHReview{pendingOn(1, head)},
		},
		{
			name:    "my pending review is newer than my real review",
			reviews: []review.GHReview{mineOn(1, review.StateCommented, older, -time.Hour), pendingOn(2, head)},
			wantID:  1,
			wantOK:  true,
		},
		{
			name:    "a reply in a thread is newer than my real review",
			reviews: []review.GHReview{mineOn(1, review.StateChangesRequested, older, -time.Hour), bareReply(2, head, time.Hour)},
			wantID:  1,
			wantOK:  true,
		},
		{
			// PostHog/posthog#137945.
			name:    "an approval after a request for changes",
			reviews: []review.GHReview{mineOn(1, review.StateChangesRequested, older, -time.Hour), mineOn(2, review.StateApproved, head, 0)},
			wantID:  2,
			wantOK:  true,
		},
		{
			name:    "a comment with a summary after an approval",
			reviews: []review.GHReview{mineOn(1, review.StateApproved, older, -time.Hour), mineOn(2, review.StateCommented, head, 0)},
			wantID:  2,
			wantOK:  true,
		},
		{
			name:    "a dismissed review is the latest",
			reviews: []review.GHReview{mineOn(1, review.StateCommented, older, -time.Hour), mineOn(2, review.StateDismissed, head, 0)},
			wantID:  2,
			wantOK:  true,
		},
		{
			name:    "only other people's reviews",
			reviews: []review.GHReview{withBody(onCommit(otherSubmittedFresh, head), "LGTM")},
		},
		{
			name:    "another user's review is newer than mine",
			reviews: []review.GHReview{mineOn(1, review.StateCommented, older, -time.Hour), withBody(onCommit(ghReview(2, other, review.StateApproved, base), head), "LGTM")},
			wantID:  1,
			wantOK:  true,
		},
		{
			name:    "my login in other letters",
			reviews: []review.GHReview{withBody(onCommit(ghReview(1, "HAACKED", review.StateCommented, base), head), "Some thoughts.")},
			wantID:  1,
			wantOK:  true,
		},
		{
			// An empty commit would match a head docket could not read.
			name:    "my real review with no commit",
			reviews: []review.GHReview{withBody(ghReview(1, me, review.StateCommented, base), "Some thoughts.")},
		},
		{
			name:    "my submitted review with no submitted time",
			reviews: []review.GHReview{withBody(onCommit(review.GHReview{ID: 1, User: review.GHUser{Login: me}, State: review.StateCommented}, head), "Some thoughts.")},
		},
		{
			name: "a review with no submitted time beside one that has one",
			reviews: []review.GHReview{
				mineOn(1, review.StateCommented, older, -time.Hour),
				withBody(onCommit(review.GHReview{ID: 2, User: review.GHUser{Login: me}, State: review.StateApproved}, head), "LGTM"),
			},
			wantID: 1,
			wantOK: true,
		},
		{
			// GitHub lists reviews oldest first, but nothing promises it, so the
			// time decides rather than the position.
			name:    "reviews listed newest first",
			reviews: []review.GHReview{mineOn(2, review.StateApproved, head, 0), mineOn(1, review.StateCommented, older, -time.Hour)},
			wantID:  2,
			wantOK:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := review.LatestReview(tt.reviews, me)

			if ok != tt.wantOK || got.ID != tt.wantID {
				t.Errorf("LatestReview = review %d (ok=%v), want review %d (ok=%v)", got.ID, ok, tt.wantID, tt.wantOK)
			}
		})
	}
}

func TestCaughtUp(t *testing.T) {
	const unreviewed = "0b57cb56a2f3e1d4c5b6a7980112233445566778"
	tests := []struct {
		name    string
		reviews []review.GHReview
		head    string
		want    bool
	}{
		{
			name:    "the head is the commit of my latest review",
			reviews: []review.GHReview{mineOn(1, review.StateCommented, older, -time.Hour), mineOn(2, review.StateCommented, head, 0)},
			head:    head,
			want:    true,
		},
		{
			// The author force-pushed back to a commit I reviewed earlier.
			name:    "the head is the commit of an older review of mine",
			reviews: []review.GHReview{mineOn(1, review.StateChangesRequested, older, -time.Hour), mineOn(2, review.StateCommented, head, 0)},
			head:    older,
			want:    true,
		},
		{
			name:    "the head is the commit of my pending review",
			reviews: []review.GHReview{mineOn(1, review.StateCommented, older, -time.Hour), pendingOn(2, head)},
			head:    head,
		},
		{
			name:    "the head is the commit of my reply in a review thread",
			reviews: []review.GHReview{mineOn(1, review.StateCommented, older, -time.Hour), bareReply(2, head, 0)},
			head:    head,
		},
		{
			name:    "the head is a commit nobody reviewed",
			reviews: []review.GHReview{mineOn(1, review.StateCommented, older, -time.Hour), mineOn(2, review.StateCommented, head, 0)},
			head:    unreviewed,
		},
		{
			name:    "the head is the commit of someone else's review",
			reviews: []review.GHReview{withBody(onCommit(ghReview(1, other, review.StateApproved, base), head), "LGTM")},
			head:    head,
		},
		{
			// ReviewedCommits counts a dismissed review, and CaughtUp is the
			// same expression for the team rows and the candidates.
			name:    "the head is the commit of my dismissed review",
			reviews: []review.GHReview{mineOn(1, review.StateDismissed, head, 0)},
			head:    head,
			want:    true,
		},
		{
			name:    "an empty head beside a review with no commit",
			reviews: []review.GHReview{withBody(ghReview(1, me, review.StateCommented, base), "Some thoughts.")},
			head:    "",
		},
		{
			name: "no reviews",
			head: head,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := review.CaughtUp(tt.reviews, me, tt.head); got != tt.want {
				t.Errorf("CaughtUp = %v, want %v", got, tt.want)
			}
		})
	}
}

// The user's rule is "changes requested or comment, not approved". GitHub does
// not say whether a dismissed review was an approval, so it does not count.
func TestRevisitable(t *testing.T) {
	tests := []struct {
		state string
		want  bool
	}{
		{review.StateCommented, true},
		{review.StateChangesRequested, true},
		{"commented", true},
		{"changes_requested", true},
		{review.StateApproved, false},
		{review.StateDismissed, false},
		{review.StatePending, false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.state, func(t *testing.T) {
			r := mineOn(1, tt.state, head, 0)

			if got := review.Revisitable(r); got != tt.want {
				t.Errorf("Revisitable(%s) = %v, want %v", tt.state, got, tt.want)
			}
		})
	}
}
