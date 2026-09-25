package tui

import (
	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/index"
	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/requests"
	"github.com/haacked/docket/internal/core/review"
)

// These messages are produced by the root's own commands and consumed by the
// root. They stay here rather than in internal/tui/msg so that the screens, which
// hold no service, do not depend on the service packages these name.

// recordsLoadedMsg carries the index as it now stands. stamp is set only by
// loadRecords, which stats the index right before reading it. reconcile and
// refreshAll leave it zero, because their own writes during the read would
// make a stamp taken there misrepresent what they actually saw.
type recordsLoadedMsg struct {
	records []review.Record
	stamp   index.StatMark
}

// refreshedMsg carries the index after every record whose session is over was
// read from GitHub again. Like reconcile's load, it carries no stamp.
type refreshedMsg struct {
	records []review.Record
}

// preparedMsg means the record is ready to launch: the pull request resolved and
// the working directory is ready, or a record is set to be reviewed again. status
// is the footer line that says what was prepared.
type preparedMsg struct {
	record review.Record
	status string
}

// existingMsg means the pull request already has a review, so the new review
// screen asks what to do with it before anything is prepared.
type existingMsg struct {
	ref    pr.Ref
	engine string
	found  review.Found
}

// launchMsg hands the root the command to run on the terminal.
type launchMsg struct {
	record review.Record
	spec   exec.CommandSpec
	kind   launchKind
	// waiting are the launches a trust prompt answers for. Only launchTrust
	// sets it.
	waiting []review.Record
}

// launchKind is what a launch hands the terminal to, which decides what its exit
// runs.
type launchKind int

const (
	// launchReview is a review session. Its exit reads GitHub.
	launchReview launchKind = iota
	// launchEditor is $EDITOR on the notes. Its exit re-reads them.
	launchEditor
	// launchAsk is a question-and-answer session. Its exit records the session
	// and then reads GitHub for anything posted during it.
	launchAsk
	// launchTrust is the agent's trust prompt for a directory. Its exit starts
	// the launches that were waiting on it.
	launchTrust
)

// askExitedMsg reports that a question-and-answer session ended.
type askExitedMsg struct {
	record review.Record
	err    error
}

// childExitedMsg reports that the agent session ended, whatever its exit status.
type childExitedMsg struct {
	record review.Record
	err    error
}

// detectedMsg carries the record after docket read GitHub for it. submitted
// marks the detection that follows a submission. The submit screen is still
// showing when that one arrives, so the root switches to the dashboard and
// clears the screen's busy marker.
type detectedMsg struct {
	record    review.Record
	submitted bool
}

// notesLoadedMsg carries the review review-code wrote. A missing file is not an
// error, so it travels as a flag rather than an errMsg.
type notesLoadedMsg struct {
	record   review.Record
	markdown string
	missing  bool
}

// draftLoadedMsg carries the body of the record's pending review, read for the
// submit screen. A failed read travels here rather than as an errMsg, because
// the screen still submits without it.
type draftLoadedMsg struct {
	record review.Record
	body   string
	err    error
}

// editorExitedMsg reports that $EDITOR closed, so the notes are worth re-reading.
type editorExitedMsg struct {
	record review.Record
	err    error
}

// bgTickMsg asks for a poll of the running background sessions. The root
// re-arms it from its own handler, and only while something is running, so an
// idle docket makes no subprocess calls.
type bgTickMsg struct{}

// bgPolledMsg carries the index after a poll, with what each running background
// session is doing. The dashboard holds no engine to ask.
type bgPolledMsg struct {
	records  []review.Record
	progress map[string]review.Progress
	err      error
}

// indexTickMsg asks the root to check whether another docket process appended
// to the index since the last check.
type indexTickMsg struct{}

// indexChangedMsg reports that the index changed. It carries the StatMark to
// compare against next time.
type indexChangedMsg struct{ stamp index.StatMark }

// requestsLoadedMsg carries what one search of GitHub for review requests found.
type requestsLoadedMsg struct{ fetched requests.Fetched }

// batchCheckedMsg reports what review each pull request in a batch already has,
// in the order the rows are drawn. failed holds one line per pull request the
// check could not read, which the batch leaves out. err is a failure that stops
// the whole batch before the check reads anything.
type batchCheckedMsg struct {
	engine string
	items  []batchItem
	failed []string
	err    error
}

type batchItem struct {
	ref    pr.Ref
	found  review.Found
	intent review.Intent
}

// resolve gives each item the intent it starts with: answer for an item that
// already has a review, and IntentReview for the rest. An empty answer leaves
// the items that have a review out.
func (b batchCheckedMsg) resolve(answer review.Intent) batchCheckedMsg {
	var items []batchItem
	for _, item := range b.items {
		item.intent = review.IntentReview
		if item.found.Any() {
			if answer == "" {
				continue
			}
			item.intent = answer
		}
		items = append(items, item)
	}
	b.items = items
	return b
}

// batchStartedMsg reports a batch of background reviews. failed holds one line
// per pull request that did not start. untrusted holds the records the agent
// refused because it does not trust their directories yet. retry marks the
// report of the launches that follow a trust prompt.
type batchStartedMsg struct {
	started   int
	failed    []string
	untrusted []review.Record
	retry     bool
}

// trustNeededMsg names background launches the agent refused because it does
// not trust their directories yet.
type trustNeededMsg struct{ records []review.Record }

// trustExitedMsg reports that the agent's trust prompt for the first record's
// directory closed. A nil err means the user trusted that directory. records are
// every launch still waiting on a trust prompt, including the ones in other
// directories.
type trustExitedMsg struct {
	records []review.Record
	err     error
}

// statusMsg is a line for the footer.
type statusMsg struct{ text string }

// errMsg is a failure to show the user. endsWork marks the failure of a refresh
// of every record. It is the one errMsg that clears the line of work in flight.
// A batch clears that line through its own messages, and any other failure
// leaves the work running.
type errMsg struct {
	err      error
	endsWork bool
}

// bgStartFailedMsg reports a background launch that failed, with the record
// StartBackground returned. A failure after the record reached StateReviewing
// may have started a session with no id recorded. The handler polls for that
// case only.
type bgStartFailedMsg struct {
	record review.Record
	err    error
}
