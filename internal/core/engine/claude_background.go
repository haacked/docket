package engine

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/core/tier"
)

// StartBackground launches the review as a detached session.
//
// No --session-id. claude refuses one here ("--bg manages the session id") and
// mints its own, so a background record carries no id until ParseBackgroundID
// reads the short one back and the first poll fills in the full one.
func (Claude) StartBackground(rec review.Record, _ Paths) exec.CommandSpec {
	return exec.CommandSpec{
		Path: "claude",
		Args: []string{"--bg", "/review-code " + reviewArgs(rec) + unattended},
		Dir:  rec.Dir,
	}
}

// unattended answers review-code's pre-flight context clear, because nobody is at
// the terminal to answer it. reviewArgs answers the prompt about a notes file that already
// exists, with the --append or --overwrite it takes from the record's intent.
const unattended = " --force"

// bgPrefix is what claude prints on stdout for a session it backgrounded. The
// id follows it on the same line.
const bgPrefix = "backgrounded"

// ParseBackgroundID reads the short id out of what `claude --bg` printed. The
// line is scanned for rather than taken by position: claude prints "Starting
// background service…" first when the service was not already up, and the lines
// after the id list the commands that take it.
func (Claude) ParseBackgroundID(res exec.Result) (string, error) {
	for line := range strings.Lines(res.Stdout) {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != bgPrefix {
			continue
		}
		// The id is last because what sits between it and the word is decoration.
		return fields[len(fields)-1], nil
	}
	return "", fmt.Errorf("claude started no background session: %s", firstLine(res))
}

// firstLine is what claude said when it did not report an id, for the error that
// carries it. claude writes its progress to stderr and the id to stdout, so a
// failure is usually on stderr with stdout empty.
func firstLine(res exec.Result) string {
	for _, stream := range []string{res.Stderr, res.Stdout} {
		if line := strings.TrimSpace(stream); line != "" {
			before, _, _ := strings.Cut(line, "\n")
			return before
		}
	}
	return "it printed nothing"
}

// StatusSpec lists every session claude is holding. --all includes the finished
// ones, which are the sessions docket is waiting for.
func (Claude) StatusSpec(_ Paths) exec.CommandSpec {
	return exec.CommandSpec{Path: "claude", Args: []string{"agents", "--json", "--all"}}
}

// bgStateDone is the state claude reports once a background session has
// finished its turn. It is not the only way a session ends, so the process
// rather than the state is what ParseStatus reads: claude holds one for every
// session it is still working on, and drops it for every session it is not.
// A state docket has not seen therefore needs no entry here.
const bgStateDone = "done"

// agentEntry is one element of `claude agents --json`. Interactive sessions
// appear too and carry no ID.
type agentEntry struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionId"`
	Kind      string `json:"kind"`
	State     string `json:"state"`
	Status    string `json:"status"`
	PID       int    `json:"pid"`
	CWD       string `json:"cwd"`
	StartedAt int64  `json:"startedAt"`
}

// ParseStatus keys the listing by the short id docket stored at launch. An
// interactive session has no short id and is skipped, so only sessions docket
// could have started are matched.
func (Claude) ParseStatus(res exec.Result) (map[string]BGStatus, error) {
	var entries []agentEntry
	if err := json.Unmarshal([]byte(res.Stdout), &entries); err != nil {
		return nil, fmt.Errorf("read the session list claude printed: %w", err)
	}
	out := make(map[string]BGStatus, len(entries))
	for _, entry := range entries {
		if entry.ID == "" {
			continue
		}
		out[entry.ID] = BGStatus{
			SessionID: entry.SessionID,
			State:     entry.State,
			Activity:  entry.Status,
			// A session claude is no longer holding is over whatever its state
			// says. `claude stop` on a session still working leaves it as
			// "stopped", which is not "done" and would otherwise be polled for
			// ever, and the user can stop one from outside docket.
			Done: entry.State == bgStateDone || entry.PID == 0,
			// claude keeps the process after the session's turn ends, and drops
			// it once the session is stopped. That is the difference between a
			// session that has to be attached and one a plain resume reopens.
			Live: entry.PID != 0,
		}
	}
	return out, nil
}

// RecoverBackgroundID finds the session a record launched but never recorded.
//
// The record's own directory is what identifies it. docket gives every tier-2
// review a directory of its own, and the launch stamps StartedAt immediately
// before running the command, so a session in that directory that began no
// earlier is the one the record lost. The oldest match wins, for the same reason
// the codex engine takes the oldest: a review dispatches further sessions from
// inside the one docket started.
//
// A tier-1 review shares one scratch directory with every other tier-1 review,
// so a match there could belong to another record. Only a directory this record
// has to itself can answer.
func (Claude) RecoverBackgroundID(rec review.Record, res exec.Result) (string, bool) {
	if rec.Dir == "" || rec.Tier != tier.Tier2 || rec.StartedAt.IsZero() {
		return "", false
	}
	var entries []agentEntry
	if err := json.Unmarshal([]byte(res.Stdout), &entries); err != nil {
		return "", false
	}

	dir := resolve(rec.Dir)
	cutoff := rec.StartedAt.Add(-startTolerance).UnixMilli()
	best, bestAt := "", int64(0)
	for _, entry := range entries {
		if entry.ID == "" || entry.StartedAt < cutoff || resolve(entry.CWD) != dir {
			continue
		}
		if best == "" || entry.StartedAt < bestAt {
			best, bestAt = entry.ID, entry.StartedAt
		}
	}
	return best, best != ""
}

// OpenSpec puts the background session on the terminal. attach is the verb for
// a session claude still holds, which a finished one is: `claude --resume`
// refuses it and says so. Once the session is stopped claude lets go of it, and
// Resume reopens it like any other.
func (Claude) OpenSpec(rec review.Record, status BGStatus, paths Paths) (exec.CommandSpec, bool) {
	if rec.BGID == "" || !status.Live {
		return Claude{}.Resume(rec, paths)
	}
	return exec.CommandSpec{
		Path: "claude",
		Args: []string{"attach", rec.BGID},
		Dir:  rec.Dir,
	}, true
}

// StopSpec ends the session. The conversation survives, which is what makes this
// safe to run before abandoning a record: `claude rm` is the one that deletes it,
// and docket never runs it.
func (Claude) StopSpec(rec review.Record, _ Paths) exec.CommandSpec {
	return exec.CommandSpec{Path: "claude", Args: []string{"stop", rec.BGID}}
}
