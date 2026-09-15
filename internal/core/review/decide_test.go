package review_test

import (
	"testing"
	"time"

	"github.com/haacked/docket/internal/core/review"
)

const (
	me    = "haacked"
	other = "octocat"
)

// base is the instant a docket session starts in these fixtures. Every other
// timestamp is an offset from it so the 2 minute tolerance has fixed edges.
var base = time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

// ghReview is the single place the tests name GHReview's fields. A zero
// submittedAt stands for GitHub's null, which is what a pending review carries.
func ghReview(id int64, login, state string, submittedAt time.Time) review.GHReview {
	r := review.GHReview{ID: id, User: review.GHUser{Login: login}, State: state}
	if !submittedAt.IsZero() {
		r.SubmittedAt = &submittedAt
	}
	return r
}

var (
	mineSubmittedFresh  = ghReview(101, me, "APPROVED", base.Add(30*time.Second))
	minePriorSubmitted  = ghReview(55, me, "APPROVED", base.Add(-time.Hour))
	minePending         = ghReview(202, me, "PENDING", time.Time{})
	otherSubmittedFresh = ghReview(303, other, "APPROVED", base.Add(30*time.Second))
	otherPending        = ghReview(404, other, "PENDING", time.Time{})
)

func TestDecide(t *testing.T) {
	tests := []struct {
		name      string
		reviews   []review.GHReview
		startedAt time.Time
		priorIDs  []int64
		wantState review.State
		wantID    int64
	}{
		{
			name:      "no reviews on the pull request",
			reviews:   nil,
			startedAt: base,
			wantState: review.StateUnreviewed,
			wantID:    0,
		},
		{
			name:      "only reviews by other people",
			reviews:   []review.GHReview{otherSubmittedFresh, otherPending},
			startedAt: base,
			wantState: review.StateUnreviewed,
			wantID:    0,
		},
		{
			name:      "my submitted review from this session",
			reviews:   []review.GHReview{mineSubmittedFresh},
			startedAt: base,
			wantState: review.StateSubmitted,
			wantID:    101,
		},
		{
			name:      "my submitted review among other peoples reviews",
			reviews:   []review.GHReview{otherPending, mineSubmittedFresh, otherSubmittedFresh},
			startedAt: base,
			wantState: review.StateSubmitted,
			wantID:    101,
		},
		{
			name:      "my submitted review outranks my pending review",
			reviews:   []review.GHReview{minePending, mineSubmittedFresh},
			startedAt: base,
			wantState: review.StateSubmitted,
			wantID:    101,
		},
		{
			name:      "my submitted review outranks my pending review regardless of order",
			reviews:   []review.GHReview{mineSubmittedFresh, minePending},
			startedAt: base,
			wantState: review.StateSubmitted,
			wantID:    101,
		},
		{
			name:      "my pending review alone is a draft",
			reviews:   []review.GHReview{minePending},
			startedAt: base,
			wantState: review.StateDrafted,
			wantID:    202,
		},
		{
			name:      "my pending review among other peoples reviews is a draft",
			reviews:   []review.GHReview{otherSubmittedFresh, minePending, otherPending},
			startedAt: base,
			wantState: review.StateDrafted,
			wantID:    202,
		},
		{
			name:      "my pending review is a draft even when its id was snapshotted before the session",
			reviews:   []review.GHReview{minePending},
			startedAt: base,
			priorIDs:  []int64{202},
			wantState: review.StateDrafted,
			wantID:    202,
		},
		{
			name:      "my submitted review from a previous session does not count as this session",
			reviews:   []review.GHReview{minePriorSubmitted},
			startedAt: base,
			priorIDs:  []int64{55},
			wantState: review.StateUnreviewed,
			wantID:    0,
		},
		{
			name:      "my submitted review from a previous session leaves a new draft visible",
			reviews:   []review.GHReview{minePriorSubmitted, minePending},
			startedAt: base,
			priorIDs:  []int64{55},
			wantState: review.StateDrafted,
			wantID:    202,
		},
		{
			name:      "my submitted review from a previous session does not mask this sessions submission",
			reviews:   []review.GHReview{minePriorSubmitted, mineSubmittedFresh},
			startedAt: base,
			priorIDs:  []int64{55},
			wantState: review.StateSubmitted,
			wantID:    101,
		},
		{
			name:      "a submitted review of mine missing from the snapshot counts even with an old timestamp",
			reviews:   []review.GHReview{minePriorSubmitted},
			startedAt: base,
			priorIDs:  nil,
			wantState: review.StateSubmitted,
			wantID:    55,
		},
		{
			name:      "prior ids for other reviews do not suppress my submission",
			reviews:   []review.GHReview{mineSubmittedFresh},
			startedAt: base,
			priorIDs:  []int64{55, 56, 57},
			wantState: review.StateSubmitted,
			wantID:    101,
		},
		{
			name:      "my snapshotted review submitted at the session start counts",
			reviews:   []review.GHReview{ghReview(55, me, "APPROVED", base)},
			startedAt: base,
			priorIDs:  []int64{55},
			wantState: review.StateSubmitted,
			wantID:    55,
		},
		{
			name:      "clock skew of 90 seconds is inside the tolerance",
			reviews:   []review.GHReview{ghReview(55, me, "APPROVED", base.Add(-90*time.Second))},
			startedAt: base,
			priorIDs:  []int64{55},
			wantState: review.StateSubmitted,
			wantID:    55,
		},
		{
			name:      "clock skew of exactly 2 minutes is inside the tolerance",
			reviews:   []review.GHReview{ghReview(55, me, "APPROVED", base.Add(-2*time.Minute))},
			startedAt: base,
			priorIDs:  []int64{55},
			wantState: review.StateSubmitted,
			wantID:    55,
		},
		{
			name:      "clock skew just past 2 minutes is outside the tolerance",
			reviews:   []review.GHReview{ghReview(55, me, "APPROVED", base.Add(-2*time.Minute-time.Second))},
			startedAt: base,
			priorIDs:  []int64{55},
			wantState: review.StateUnreviewed,
			wantID:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotState, gotID := review.Decide(tt.reviews, me, tt.startedAt, tt.priorIDs)

			if gotState != tt.wantState {
				t.Errorf("Decide(…) state = %v, want %v", gotState, tt.wantState)
			}
			if gotID != tt.wantID {
				t.Errorf("Decide(…) review id = %d, want %d", gotID, tt.wantID)
			}
		})
	}
}

func TestDecideTreatsEveryNonPendingStateAsSubmitted(t *testing.T) {
	states := []string{"APPROVED", "COMMENTED", "CHANGES_REQUESTED"}

	for _, state := range states {
		t.Run(state, func(t *testing.T) {
			reviews := []review.GHReview{ghReview(101, me, state, base.Add(time.Minute))}

			gotState, gotID := review.Decide(reviews, me, base, nil)

			if gotState != review.StateSubmitted {
				t.Errorf("Decide with my %s review state = %v, want %v", state, gotState, review.StateSubmitted)
			}
			if gotID != 101 {
				t.Errorf("Decide with my %s review id = %d, want 101", state, gotID)
			}
		})
	}
}

func TestDecideIgnoresReviewsByOtherLogins(t *testing.T) {
	reviews := []review.GHReview{
		ghReview(301, other, "APPROVED", base.Add(time.Minute)),
		ghReview(302, other, "CHANGES_REQUESTED", base.Add(2*time.Minute)),
		ghReview(303, other, "PENDING", time.Time{}),
	}

	gotState, gotID := review.Decide(reviews, me, base, nil)

	if gotState != review.StateUnreviewed {
		t.Errorf("Decide over only other logins state = %v, want %v", gotState, review.StateUnreviewed)
	}
	if gotID != 0 {
		t.Errorf("Decide over only other logins review id = %d, want 0", gotID)
	}
}

// TestDecideCrossesFixturesWithStartedAt walks the same review fixtures past a
// range of session start times. A review whose id docket snapshotted before the
// session only counts as this session's submission while its timestamp sits
// within 2 minutes of the start; a review docket never saw before always counts.
func TestDecideCrossesFixturesWithStartedAt(t *testing.T) {
	submitted := ghReview(70, me, "APPROVED", base)
	pending := ghReview(71, me, "PENDING", time.Time{})

	starts := []struct {
		name            string
		startedAt       time.Time
		withinTolerance bool
	}{
		{"session started an hour before my review was submitted", base.Add(-time.Hour), true},
		{"session started one second before my review was submitted", base.Add(-time.Second), true},
		{"session started the instant my review was submitted", base, true},
		{"my review was submitted 90 seconds before the session started", base.Add(90 * time.Second), true},
		{"my review was submitted exactly 2 minutes before the session started", base.Add(2 * time.Minute), true},
		{"my review was submitted just over 2 minutes before the session started", base.Add(2*time.Minute + time.Second), false},
		{"my review was submitted an hour before the session started", base.Add(time.Hour), false},
	}

	fixtures := []struct {
		name       string
		reviews    []review.GHReview
		staleState review.State
		staleID    int64
	}{
		{
			name:       "my submitted review alone",
			reviews:    []review.GHReview{submitted},
			staleState: review.StateUnreviewed,
			staleID:    0,
		},
		{
			name:       "my submitted review beside my pending review",
			reviews:    []review.GHReview{submitted, pending},
			staleState: review.StateDrafted,
			staleID:    71,
		},
	}

	for _, fixture := range fixtures {
		for _, start := range starts {
			t.Run(fixture.name+", "+start.name+", id snapshotted before the session", func(t *testing.T) {
				wantState, wantID := fixture.staleState, fixture.staleID
				if start.withinTolerance {
					wantState, wantID = review.StateSubmitted, submitted.ID
				}

				gotState, gotID := review.Decide(fixture.reviews, me, start.startedAt, []int64{submitted.ID})

				if gotState != wantState {
					t.Errorf("Decide(…, startedAt=%s, priorIDs=[%d]) state = %v, want %v", start.startedAt, submitted.ID, gotState, wantState)
				}
				if gotID != wantID {
					t.Errorf("Decide(…, startedAt=%s, priorIDs=[%d]) review id = %d, want %d", start.startedAt, submitted.ID, gotID, wantID)
				}
			})

			t.Run(fixture.name+", "+start.name+", id not snapshotted", func(t *testing.T) {
				gotState, gotID := review.Decide(fixture.reviews, me, start.startedAt, nil)

				if gotState != review.StateSubmitted {
					t.Errorf("Decide(…, startedAt=%s, priorIDs=nil) state = %v, want %v", start.startedAt, gotState, review.StateSubmitted)
				}
				if gotID != submitted.ID {
					t.Errorf("Decide(…, startedAt=%s, priorIDs=nil) review id = %d, want %d", start.startedAt, gotID, submitted.ID)
				}
			})
		}
	}
}
