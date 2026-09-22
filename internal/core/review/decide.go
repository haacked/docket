package review

import (
	"slices"
	"strings"
	"time"
)

// GHReview is the part of GitHub's pull request review payload docket reads.
type GHReview struct {
	ID          int64      `json:"id"`
	User        GHUser     `json:"user"`
	State       string     `json:"state"`
	SubmittedAt *time.Time `json:"submitted_at"`
}

type GHUser struct {
	Login string `json:"login"`
}

// StatePending is the state GitHub reports for a review that was created
// without an event, which is what review-code's draft does.
const StatePending = "PENDING"

// SubmitTolerance absorbs clock skew between this machine and GitHub when
// comparing submitted_at against the session's start.
const SubmitTolerance = 2 * time.Minute

// Decide reads where a review stands from GitHub's review list for the PR. It
// returns the state and the id of the review that settled it, or 0 when no review of
// mine exists.
//
// A review of mine that is no longer pending counts as this session's
// submission when docket had not already seen it before the session started, or
// when GitHub submitted it no earlier than the session did. Both tests matter.
// Prior ids alone miss a re-submission of a review docket already knew about.
// The timestamp alone trips over clock skew.
func Decide(reviews []GHReview, me string, startedAt time.Time, priorIDs []int64) (State, int64) {
	var draftedID int64
	cutoff := startedAt.Add(-SubmitTolerance)

	for _, r := range reviews {
		if !strings.EqualFold(r.User.Login, me) {
			continue
		}
		if r.pending() {
			draftedID = r.ID
			continue
		}
		if !slices.Contains(priorIDs, r.ID) {
			return StateSubmitted, r.ID
		}
		if r.SubmittedAt != nil && !r.SubmittedAt.Before(cutoff) {
			return StateSubmitted, r.ID
		}
	}

	if draftedID != 0 {
		return StateDrafted, draftedID
	}
	return StateUnreviewed, 0
}

// PriorSubmittedIDs lists my already-submitted reviews, which Prepare snapshots
// so an older review cannot masquerade as this session's. It leaves out a pending
// review on purpose. Submitting a pending review keeps its id, so listing it here
// would make this session's own submission look prior.
func PriorSubmittedIDs(reviews []GHReview, me string) []int64 {
	var ids []int64
	for _, r := range reviews {
		if strings.EqualFold(r.User.Login, me) && !r.pending() {
			ids = append(ids, r.ID)
		}
	}
	return ids
}

// PendingReviewID is my pending review on the pull request, or 0 when there is
// none. Adopting an existing review asks for it directly rather than through
// Decide, which reports a submitted review ahead of a pending one. The last
// match wins, as it does in Decide.
func PendingReviewID(reviews []GHReview, me string) int64 {
	var id int64
	for _, r := range reviews {
		if strings.EqualFold(r.User.Login, me) && r.pending() {
			id = r.ID
		}
	}
	return id
}

func (r GHReview) pending() bool { return strings.EqualFold(r.State, StatePending) }
