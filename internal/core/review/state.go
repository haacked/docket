// Package review holds the review record, the append-only event log it folds
// from, and the decision that reads a review's state off GitHub.
package review

import (
	"time"

	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/tier"
)

// State is where one review stands.
type State string

const (
	StatePreparing  State = "preparing"
	StateReviewing  State = "reviewing"
	StateDrafted    State = "drafted"
	StateSubmitted  State = "submitted"
	StateArchived   State = "archived"
	StateAbandoned  State = "abandoned"
	StateUnreviewed State = "unreviewed"
)

// Submittable reports whether the record has a pending review to submit. The
// dashboard gates the submit key on it and the service refuses anything else, so
// the rule is stated once.
func (r Record) Submittable() bool {
	return r.State == StateDrafted && r.ReviewID != 0
}

// Open reports whether the record still wants the user's attention.
func (s State) Open() bool {
	switch s {
	case StateArchived, StateAbandoned:
		return false
	default:
		return true
	}
}

// Mode is how the session runs. Background mode arrives in M3.
type Mode string

const (
	ModeInteractive Mode = "interactive"
	ModeBackground  Mode = "background"
)

// Record is one pull request docket is handling.
//
// Every event in the index carries a complete record, so no field here may use
// `omitempty`. Fold applies an event's fields over the record it has so far, and
// an omitted key leaves a stale value where the writer meant to clear one.
// TestFoldClearsFieldsASnapshotZeroes holds the line.
type Record struct {
	ID             string     `json:"id"`
	Ref            pr.Ref     `json:"ref"`
	URL            string     `json:"url"`
	Title          string     `json:"title"`
	Author         string     `json:"author"`
	Engine         string     `json:"engine"`
	Tier           tier.Tier  `json:"tier"`
	Mode           Mode       `json:"mode"`
	Dir            string     `json:"dir"`
	SessionID      string     `json:"session_id"`
	BGID           string     `json:"bg_id"`
	StartedAt      time.Time  `json:"started_at"`
	SubmittedAt    *time.Time `json:"submitted_at"`
	ArchivedAt     *time.Time `json:"archived_at"`
	State          State      `json:"state"`
	ReviewID       int64      `json:"review_id"`
	NotesPath      string     `json:"notes_path"`
	PriorReviewIDs []int64    `json:"prior_review_ids"`
	Err            string     `json:"err"`
}
