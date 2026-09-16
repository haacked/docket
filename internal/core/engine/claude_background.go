package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/review"
)

// StartBackground launches the review as a detached session.
//
// No --session-id. claude refuses one here ("--bg manages the session id") and
// mints its own, so a background record carries no id until ParseBackgroundID
// reads the short one back and the first poll fills in the full one.
func (Claude) StartBackground(rec review.Record, _ Paths) exec.CommandSpec {
	return exec.CommandSpec{
		Path: "claude",
		Args: []string{"--bg", "/review-code " + rec.URL + " --draft" + unattended(rec)},
		Dir:  rec.Dir,
	}
}

// unattended are the flags that answer review-code's prompts, because nobody is
// at the terminal to.
//
// --force covers the pre-flight context clear. It does not cover the second
// prompt: a review file that already exists asks whether to overwrite or append,
// and only --overwrite or --append answers that one. docket keeps the notes of
// every review it has run, so a pull request reviewed once already has that file
// and a background re-review would stop there with nobody to answer, showing as
// a session that runs and never finishes.
//
// --append is the answer rather than --overwrite because it is what a re-review
// means: review-code reviews what changed since the recorded commit and resolves
// the threads the author has since addressed.
func unattended(rec review.Record) string {
	flags := " --force"
	if rec.NotesPath == "" {
		return flags
	}
	if _, err := os.Stat(rec.NotesPath); err == nil {
		flags += " --append"
	}
	return flags
}

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
// finished its turn. Every other state means it is still going, including the
// ones docket has not seen: polling a session that is really finished costs one
// listing per tick, while reading an unknown state as finished would send docket
// to GitHub for a review still being written.
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
			Done:      entry.State == bgStateDone,
			// claude keeps the process after the session's turn ends, and drops
			// it once the session is stopped. That is the difference between a
			// session that has to be attached and one a plain resume reopens.
			Live: entry.PID != 0,
		}
	}
	return out, nil
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
