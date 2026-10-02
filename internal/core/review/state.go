// Package review holds the review record, the append-only event log it folds
// from, and the decision that reads a review's state off GitHub.
package review

import (
	"strconv"
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
	// StateNotStarted is a background review whose launch the agent refused, so
	// no session ran. Nothing on GitHub can say anything about it. Detection
	// would read it as unreviewed and drop the reason it never ran.
	StateNotStarted State = "not_started"
	// StateFixed is a fix review whose checkout holds changes that are not on
	// GitHub: uncommitted edits, or commits the remote branch does not have.
	// Detection never produces it from GitHub, which knows nothing of the
	// checkout.
	StateFixed State = "fixed"
	// StatePushed is a fix review whose checkout matches the remote branch. The
	// fixes are pushed, or the review made none, and the user can approve.
	StatePushed State = "pushed"
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

// HasPendingDraft reports whether ReviewID names a pending review of mine.
//
// A drafted record always holds one. A reviewing record holds one after the
// user reopens a drafted row's session. The draft stays pending on GitHub until
// that session replaces it. The id can therefore be stale. A review that has
// not posted a draft carries no id. Rereview clears the id before it starts.
func (r Record) HasPendingDraft() bool {
	return r.ReviewID != 0 && (r.State == StateDrafted || r.State == StateReviewing)
}

// Submittable reports whether the record has a pending review to submit. The
// dashboard gates the submit key on it and the service refuses anything else, so
// the rule is stated once.
//
// A reviewing interactive record is refused. Its session is open in a terminal,
// possibly in another docket instance, and the archive that follows a submit
// would delete the clone under it. Archive leaves only a background session
// running.
//
// A pushed fix review has no pending review. Submitting it posts a new one.
func (r Record) Submittable() bool {
	if r.State == StatePushed {
		return true
	}
	return r.HasPendingDraft() && (r.State == StateDrafted || r.Mode == ModeBackground)
}

// NeedsBody reports whether submitting the record as event needs a body. A
// pushed fix review posts a new review, and GitHub refuses a new comment or
// request for changes with no body. The submit screen says so and the service
// refuses an empty one.
func (r Record) NeedsBody(event string) bool {
	return r.State == StatePushed && event != EventApprove
}

// WebURL is the page that shows the record's review on GitHub. A pending draft
// opens on the Conversation tab at the review. GitHub shows a pending review to
// its author only.
func (r Record) WebURL() string {
	if r.HasPendingDraft() {
		return r.Ref.URL() + "#pullrequestreview-" + strconv.FormatInt(r.ReviewID, 10)
	}
	return r.Ref.URL()
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

// HasCheckout reports whether the record has a working tree of its own: a
// tier-2 clone or a tier-3 worktree. Every tier-1 record shares the scratch
// directory.
func (r Record) HasCheckout() bool {
	return (r.Tier == tier.Tier2 || r.Tier == tier.Tier3) && r.Dir != ""
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
// or drop it. A record in progress is not finished until its session ends. A
// fix review with changes that are not on GitHub is not finished either, because
// archiving it would delete them.
func (r Record) Finished() bool {
	if !r.PRState.Closed() {
		return false
	}
	return r.State == StateUnreviewed || r.State == StateReviewed || r.State == StatePushed
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
	switch s {
	case StateUnreviewed:
		return "no review posted"
	case StateNotStarted:
		return "did not start"
	case StateFixed:
		return "fixes to push"
	case StatePushed:
		return "ready to approve"
	default:
		return string(s)
	}
}

// OpenRecord is the open record for ref. The index holds one record per review.
// A pull request that was reviewed, archived, and asked for again has two
// records, and only the open one counts.
func OpenRecord(records []Record, ref pr.Ref) (Record, bool) {
	for _, rec := range records {
		if rec.State.Open() && rec.Ref.Equal(ref) {
			return rec, true
		}
	}
	return Record{}, false
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

// Progress is the agent's own account of a running background session.
type Progress struct {
	// Detail is the agent's one-line summary of what the session is doing.
	Detail string
	// Needs is what the agent says the session needs from the user. The agent
	// can name one while the session still works, so the listing, not this,
	// decides whether the session is waiting.
	Needs string
	// Active reports that the agent says the session is working. claude says so
	// from launch, while its listing still reports the session idle.
	Active bool
	// Agents is how many sub-agents the session is running.
	Agents int
	// UpdatedAt is when the agent last rewrote its account of the session.
	UpdatedAt time.Time
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
	// PriorPendingID is my pending review when the session launched, or 0.
	// review-code replaces it only when it posts its own draft, so a session
	// that is still running may leave it in place.
	PriorPendingID int64  `json:"prior_pending_id"`
	Err            string `json:"err"`
	Intent         Intent `json:"intent"`
	// CloneInUse records that Archive found the session that asked for it
	// running inside the clone. Archive then left the record open and the clone
	// in place.
	CloneInUse bool `json:"clone_in_use"`
	// AskSessionID names the question-and-answer session about the notes. docket
	// keeps it apart from SessionID, so asking about a review never replaces the
	// review session that enter resumes.
	AskSessionID string    `json:"ask_session_id"`
	AskStartedAt time.Time `json:"ask_started_at"`
	// PRState is the pull request's state when docket last read GitHub.
	PRState PRState `json:"pr_state"`
	// ClaudeConfigDir is the CLAUDE_CONFIG_DIR the record's claude sessions run
	// under, or "" for claude's default. claude keeps a session under that
	// directory. Every command about the session therefore names the same one.
	ClaudeConfigDir string `json:"claude_config_dir"`
	// Fix reports that the review runs review-code with --fix. The session edits
	// the checkout and posts nothing to GitHub.
	Fix bool `json:"fix"`
	// Branch is the head branch a fix review's checkout is on. review-code
	// edits files only on a branch named exactly like the head branch.
	Branch string `json:"branch"`
	// Remote is the checkout's remote for the pull request's repository.
	Remote string `json:"remote"`
	// WorktreeOf is the repos.conf clone that a tier-3 worktree belongs to.
	WorktreeOf string `json:"worktree_of"`
	// FixBase is the commit the checkout was on when docket provisioned or
	// refreshed it.
	FixBase string `json:"fix_base"`
	// FixBaseAt is when docket recorded FixBase. A resumed session stamps a new
	// StartedAt, so review-code's notes are measured against this instead.
	FixBaseAt time.Time `json:"fix_base_at"`
	// FixHead is the commit the checkout was on when docket last inspected it.
	FixHead string `json:"fix_head"`
}

// Upstream is the remote-tracking ref of a fix review's head branch.
func (r Record) Upstream() string {
	return r.Remote + "/" + r.Branch
}

// NoChanges reports whether a fix review's checkout is still on the commit it
// started from.
func (r Record) NoChanges() bool {
	return r.FixHead == r.FixBase
}
