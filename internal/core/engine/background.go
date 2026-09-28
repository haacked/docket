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
	// Live reports whether the agent still holds the process. An agent holding a
	// session refuses a plain resume, so this decides how the session reopens.
	Live bool
	// Done reports that the session has stopped working, which is when docket
	// reads GitHub for what it left behind. Each engine names its own states, so
	// its ParseStatus decides this rather than a word compared here.
	Done bool
	// Idle reports that the agent's listing says it holds the session and the
	// session is not working. The listing can say so just as a turn starts.
	// Waiting, not Idle, is the test for an ended turn.
	Idle bool
	// Progress is what the agent says the session is doing, beyond the listing.
	// The poll fills it in for a session that is still running.
	Progress review.Progress
}

// Waiting reports that the session has ended its turn. The listing can say idle
// while the progress file already says a turn is active. claude does this at
// launch, before the first turn. Waiting therefore requires both.
func (s BGStatus) Waiting() bool {
	return s.Idle && !s.Progress.Active
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
	// RecoverBackgroundID finds the session a record launched but never got to
	// record, by the directory it runs in and the time it started. A launch
	// writes the id in a second step, so a docket that dies in between leaves a
	// record naming no session and an agent nobody is watching.
	RecoverBackgroundID(rec review.Record, res exec.Result) (string, bool)
	// OpenSpec hands the session back to the terminal. It reports false when
	// there is nothing left to open.
	OpenSpec(rec review.Record, status BGStatus, paths Paths) (exec.CommandSpec, bool)
	// StopSpec ends the session while keeping its conversation. The caller has
	// already established that there is one to end.
	StopSpec(rec review.Record, paths Paths) exec.CommandSpec
	// Progress reads what the agent says the session with this background id is
	// doing. It is zero when the agent left nothing docket can read.
	Progress(id string, paths Paths) review.Progress
	// Untrusted reports whether a failed StartBackground failed because the
	// agent has not been told to trust the directory. The agent asks that
	// question only on a terminal, which a background start does not have.
	Untrusted(res exec.Result) bool
	// TrustSpec puts the agent's trust prompt for dir on the terminal and ends
	// once the user answers it. It exits zero only when the user trusted dir.
	TrustSpec(dir string) exec.CommandSpec
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
