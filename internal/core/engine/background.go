package engine

import (
	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/review"
)

// BGStatus is what an agent reports about one of its background sessions.
type BGStatus struct {
	// SessionID names the conversation. A background session is the one case
	// where the agent mints the id, so the record stores this once the first
	// poll sees it.
	SessionID string
	// State is how far the session got, in the agent's own words. It is for the
	// screen. Done is what docket reads.
	State string
	// Activity is what the session is doing now. A session stopped at a
	// permission prompt still reports a live state. This is the only sign the
	// user gets that it is waiting for them.
	Activity string
	// Live reports whether the agent still holds the process. An agent holding a
	// session refuses a plain resume, so this decides how the session reopens.
	Live bool
	// Done reports that the session has stopped working, which is when docket
	// reads GitHub for what it left behind. Each engine names its own states, so
	// its ParseStatus decides this rather than a word compared here.
	Done bool
}

// BackgroundEngine is an engine that can run a review with nobody at the
// terminal. Codex does not implement it: 0.150.1 has no background primitive,
// so a review would run as a child of docket and die when docket quits.
//
// The methods return specs and parse their output rather than running anything,
// which is what keeps os/exec out of this package.
type BackgroundEngine interface {
	Engine
	// StartBackground launches the review detached from the terminal.
	StartBackground(rec review.Record, paths Paths) exec.CommandSpec
	// ParseBackgroundID reads the id of the session StartBackground launched out
	// of what it printed.
	ParseBackgroundID(res exec.Result) (string, error)
	// StatusSpec lists every background session the agent knows about. It takes
	// no record. One listing answers for every record docket is watching.
	StatusSpec(paths Paths) exec.CommandSpec
	// ParseStatus turns that listing into a status per background id. An id that
	// is absent from the result has no entry, which the caller reads as a
	// session the agent no longer knows about.
	ParseStatus(res exec.Result) (map[string]BGStatus, error)
	// OpenSpec hands the session back to the terminal. It reports false when
	// there is nothing left to open.
	OpenSpec(rec review.Record, status BGStatus, paths Paths) (exec.CommandSpec, bool)
	// StopSpec ends the session while keeping its conversation. The caller has
	// already established that there is one to end.
	StopSpec(rec review.Record, paths Paths) exec.CommandSpec
}

// Background returns the engine's background support, or false when it has
// none. The new review screen asks so it can refuse the mode before the user
// starts a review that cannot run.
func Background(name string) (BackgroundEngine, bool) {
	eng, err := For(name)
	if err != nil {
		return nil, false
	}
	bg, ok := eng.(BackgroundEngine)
	return bg, ok
}

// BackgroundNames lists the engines that can run a review in the background.
func BackgroundNames() []string {
	var out []string
	for _, name := range Names() {
		if _, ok := Background(name); ok {
			out = append(out, name)
		}
	}
	return out
}
