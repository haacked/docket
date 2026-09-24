// Package engine builds the command lines that launch a review session in one of
// the CLI agents. It returns specs. internal/tui runs them on the terminal.
package engine

import (
	"fmt"

	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/core/tier"
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
	// ClaudeJobs is where claude's background service keeps a status file for
	// each background session.
	ClaudeJobs string
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
	// Ask starts a session that answers questions about the record's notes. It
	// runs no review, so it passes none of the review-code flags. The session id
	// it runs under is rec.AskSessionID.
	Ask(rec review.Record, paths Paths) exec.CommandSpec
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
//
// --append and --overwrite answer review-code's prompt about a notes file that
// already exists. docket asks the user that question before it launches, so the
// session never stops there, in the terminal or in the background.
func reviewArgs(rec review.Record) string {
	args := rec.URL + " --draft"
	if rec.OwnPR {
		args += " --self"
	}
	switch rec.Intent {
	case review.IntentAppend:
		args += " --append"
	case review.IntentOverwrite:
		args += " --overwrite"
	}
	return args
}

// askPrompt is the first message of a session about an existing review. It names
// the notes file rather than invoking review-code, because `/review-code find`
// only prints the notes and cannot resolve a pull request from docket's scratch
// directory.
//
// A tier-1 ask runs in that scratch directory with none of the pull request's
// files. review-code removes the worktree it read them from when the review
// ends. The prompt therefore names the GitHub commands that read the change and
// its files at the pull request's head.
func askPrompt(rec review.Record) string {
	prompt := fmt.Sprintf("Read the review notes at %s. They are my review of %s. I have questions about this review. Do not post anything to GitHub unless I ask.", rec.NotesPath, rec.URL)
	if rec.Tier == tier.Tier1 {
		prompt += fmt.Sprintf(" The pull request's files are not checked out here. Run `gh pr diff %s` to see the change, and `gh api -H 'Accept: application/vnd.github.raw' 'repos/%s/%s/contents/<path>?ref=refs/pull/%d/head'` to read a whole file.", rec.URL, rec.Ref.Org, rec.Ref.Repo, rec.Ref.Number)
	}
	return prompt
}
