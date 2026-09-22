package tui

import (
	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/index"
	"github.com/haacked/docket/internal/core/requests"
	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/core/session"
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

// preparedMsg means the pull request resolved and the working directory is ready.
type preparedMsg struct {
	record review.Record
	plan   session.Plan
}

// launchMsg hands the root the command to run on the terminal. An editor is the
// other thing docket gives the terminal to. Its exit ends in a re-read of the
// notes rather than in detection.
type launchMsg struct {
	record review.Record
	spec   exec.CommandSpec
	editor bool
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
// session is doing. The note is already formatted: the dashboard holds no engine
// to ask.
type bgPolledMsg struct {
	records []review.Record
	notes   map[string]string
	err     error
}

// indexTickMsg asks the root to check whether another docket process appended
// to the index since the last check.
type indexTickMsg struct{}

// indexChangedMsg reports that the index changed. It carries the StatMark to
// compare against next time.
type indexChangedMsg struct{ stamp index.StatMark }

// requestsLoadedMsg carries what one search of GitHub for review requests found.
type requestsLoadedMsg struct{ fetched requests.Fetched }

// batchStartedMsg reports a batch of background reviews. failed holds one line
// per pull request that did not start.
type batchStartedMsg struct {
	started int
	failed  []string
}

// statusMsg is a line for the footer.
type statusMsg struct{ text string }

// errMsg is a failure to show the user.
type errMsg struct{ err error }
