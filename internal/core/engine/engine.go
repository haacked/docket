// Package engine builds the command lines that launch a review session in one of
// the CLI agents. It returns specs. internal/tui runs them on the terminal.
package engine

import (
	"fmt"

	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/review"
)

// Paths are the installed locations an engine needs to build a command. They are
// configuration with defaults, so no engine names one of them itself. config
// owns the layout behind them.
type Paths struct {
	// Grant are the directories the agent must be able to write to, beyond the
	// one it runs in.
	Grant []string
	// CodexSessions is where codex records a session.
	CodexSessions string
}

// Engine launches and resumes a review session.
type Engine interface {
	// Name is the value stored on the record.
	Name() string
	// Binary is the executable docket checks for at startup.
	Binary() string
	// NewSessionID is the id docket stores before launching, or "" when the
	// agent assigns its own.
	NewSessionID() string
	// Start begins a review of rec's pull request.
	Start(rec review.Record, paths Paths) exec.CommandSpec
	// Resume reopens the conversation Start left behind. It reports false when
	// the record carries no id to resume, so the caller starts fresh instead.
	Resume(rec review.Record, paths Paths) (exec.CommandSpec, bool)
	// CaptureSessionID recovers the id of the session the record just ran, for
	// an agent that assigns its own rather than taking one on the command line.
	// It returns "" when there is nothing to capture, which is not an error: an
	// engine that mints its own ids up front never has anything to find.
	CaptureSessionID(rec review.Record, paths Paths) (string, error)
}

// For returns the engine with the given name.
func For(name string) (Engine, error) {
	switch name {
	case Claude{}.Name():
		return Claude{}, nil
	case Codex{}.Name():
		return Codex{}, nil
	case "":
		return nil, fmt.Errorf("no engine given")
	default:
		return nil, fmt.Errorf("unknown engine %q", name)
	}
}

// Names lists the engines docket can launch.
func Names() []string { return []string{Claude{}.Name(), Codex{}.Name()} }

// reviewArgs is the review-code invocation for a record, after the prefix each
// agent puts in front of it.
//
// --self is what lets a review of your own pull request create its draft.
// review-code leaves the draft out otherwise, and the Suggested Comments with
// it, so docket would find nothing on GitHub and report the review as
// unreviewed however well the session went.
func reviewArgs(rec review.Record) string {
	args := rec.URL + " --draft"
	if rec.OwnPR {
		args += " --self"
	}
	return args
}
