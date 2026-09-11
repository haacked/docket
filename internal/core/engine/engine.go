// Package engine builds the command lines that launch a review session in one of
// the CLI agents. It returns specs. internal/tui runs them on the terminal.
package engine

import (
	"fmt"

	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/review"
)

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
	Start(rec review.Record, reviewCodeDir string) exec.CommandSpec
	// Resume reopens the conversation Start left behind. It reports false when
	// the record carries no id to resume, so the caller starts fresh instead.
	Resume(rec review.Record, reviewCodeDir string) (exec.CommandSpec, bool)
}

// For returns the engine with the given name.
func For(name string) (Engine, error) {
	switch name {
	case Claude{}.Name():
		return Claude{}, nil
	case "codex":
		return nil, fmt.Errorf("the codex engine is not wired up yet")
	case "":
		return nil, fmt.Errorf("no engine given")
	default:
		return nil, fmt.Errorf("unknown engine %q", name)
	}
}

// Names lists the engines docket can launch.
func Names() []string { return []string{Claude{}.Name()} }
