// Package review holds the review record, the append-only event log it folds
// from, and the decision that reads a review's state off GitHub.
package review

import (
	"strings"
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
	// StateReviewed is a record adopted from notes docket did not produce, with
	// no pending review of mine on GitHub. Detection never produces it. Decide
	// reads such a pull request as unreviewed, which misstates a review the user
	// already did.
	StateReviewed State = "reviewed"
)

// Intent is what the user asked for when a pull request already had a review.
type Intent string

const (
	// IntentReview is a review of a pull request with nothing to choose about.
	IntentReview Intent = "review"
	// IntentAppend re-reviews and keeps the earlier notes and draft comments.
	IntentAppend Intent = "append"
	// IntentOverwrite re-reviews from scratch.
	IntentOverwrite Intent = "overwrite"
	// IntentAsk adopts the existing review without running review-code.
	IntentAsk Intent = "ask"
)

// Submittable reports whether the record has a pending review to submit. The
// dashboard gates the submit key on it and the service refuses anything else, so
// the rule is stated once.
func (r Record) Submittable() bool {
	return r.State == StateDrafted && r.ReviewID != 0
}

// BackgroundRunning reports whether the record is a background session docket
// should still be asking the agent about. The service polls on it and the UI
// keeps its tick alive on it, so the rule is stated once: if the two disagreed,
// docket would either poll forever over nothing or stop watching a live review.
//
// The id has to be there. A start that failed before reporting one leaves a
// record that reads as running with no session behind it.
func (r Record) BackgroundRunning() bool {
	return r.Mode == ModeBackground && r.State == StateReviewing && r.BGID != ""
}

// InBackgroundSession reports whether the record's review is running as a
// background session, whether or not its id has been captured yet. A launch
// records StateReviewing before the id arrives, so a record still carries no
// BGID while it is genuinely running or while recoverLost is still looking for
// it. Refresh and RefreshAll refuse such a record: the poll owns it until the
// session ends. Archiving it here could stop a session still writing to its
// clone with no id to stop it by.
func (r Record) InBackgroundSession() bool {
	return r.Mode == ModeBackground && r.State == StateReviewing
}

// HasBackgroundSession reports whether an agent is holding a session for this
// record, whatever state the review reached. Stopping asks this rather than
// BackgroundRunning: the agent keeps holding a session after the review it ran
// is finished, so a record cleaned up once it was drafted still has one to end.
func (r Record) HasBackgroundSession() bool {
	return r.Mode == ModeBackground && r.BGID != ""
}

// InProgress reports whether another step may still be writing to the record: a
// clone or scratch setup while it is preparing, or a review session while it is
// reviewing. Another docket instance can be doing either, so asking about the
// record or reviewing it again waits for it to settle.
func (r Record) InProgress() bool {
	return r.State == StatePreparing || r.State == StateReviewing
}

// Adopted reports whether the record took over an existing review and has run no
// review session of its own. The dashboard opens the re-review choice for it and
// the service refuses to resume it, so the rule is stated once.
func (r Record) Adopted() bool {
	return r.Intent == IntentAsk
}

// Finished reports whether the record's pull request merged or closed and left
// the user nothing to do. A pending review is not finished, because GitHub still
// accepts a review on a merged pull request. Only the user can choose to submit it
// or drop it. A record in progress is not finished until its session ends.
func (r Record) Finished() bool {
	if !r.PRState.Closed() {
		return false
	}
	return r.State == StateUnreviewed || r.State == StateReviewed
}

// PRState is the pull request's state as `gh pr view --json state` reports it.
// An empty PRState is unknown. docket wrote such a record before it read the
// state.
type PRState string

const (
	PROpen   PRState = "OPEN"
	PRMerged PRState = "MERGED"
	PRClosed PRState = "CLOSED"
)

// Closed reports whether the pull request merged or closed without merging.
func (s PRState) Closed() bool {
	return s == PRMerged || s == PRClosed
}

// Label is the state as the user reads it.
func (s PRState) Label() string {
	return strings.ToLower(string(s))
}

// Label is the state as the screens name it. The unreviewed state means that the
// last session posted nothing new of mine. It does not mean that the user never
// reviewed the pull request, so its label is "no review posted".
func (s State) Label() string {
	if s == StateUnreviewed {
		return "no review posted"
	}
	return string(s)
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

// Mode is how the session runs.
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
	OwnPR          bool       `json:"own_pr"`
	PriorReviewIDs []int64    `json:"prior_review_ids"`
	Err            string     `json:"err"`
	Intent         Intent     `json:"intent"`
	// AskSessionID names the question-and-answer session about the notes. docket
	// keeps it apart from SessionID, so asking about a review never replaces the
	// review session that enter resumes.
	AskSessionID string    `json:"ask_session_id"`
	AskStartedAt time.Time `json:"ask_started_at"`
	// PRState is the pull request's state when docket last read GitHub.
	PRState PRState `json:"pr_state"`
}
