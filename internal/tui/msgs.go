package tui

import (
	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/core/session"
)

// These messages are produced by the root's own commands and consumed by the
// root. They stay here rather than in internal/tui/msg so that the screens, which
// hold no service, do not depend on the service packages these name.

// recordsLoadedMsg carries the index as it now stands.
type recordsLoadedMsg struct{ records []review.Record }

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
// marks the one that followed a submission, which is the only detection that
// leaves a screen behind.
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

// statusMsg is a line for the footer.
type statusMsg struct{ text string }

// errMsg is a failure to show the user.
type errMsg struct{ err error }
