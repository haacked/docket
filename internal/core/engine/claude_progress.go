package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/review"
)

// jobState is the part of claude's per-session status file that docket reads.
// claude's background service keeps the file at <jobs>/<short id>/state.json and
// rewrites it as the session goes (verified on 2.1.281). The format is claude's
// own and undocumented. A field claude drops or renames leaves that part of the
// progress empty and does not fail the poll.
type jobState struct {
	Detail string `json:"detail"`
	// Tempo is claude's own reading of the session: "active", "idle", or
	// "blocked". docket reads only whether it is "active".
	Tempo string `json:"tempo"`
	Needs string `json:"needs"`
	// Fan lists the session's sub-agents. It keeps a finished one, with its
	// doneAt set, for a while after it ends.
	Fan []struct {
		DoneAt *int64 `json:"doneAt"`
	} `json:"fan"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Progress reads claude's status file for the session.
func (Claude) Progress(id string, paths Paths) review.Progress {
	// The id is what `claude --bg` printed. filepath.IsLocal keeps an id that
	// climbs out of the jobs directory from naming a file somewhere else.
	if paths.ClaudeJobs == "" || id == "" || !filepath.IsLocal(id) {
		return review.Progress{}
	}
	data, err := os.ReadFile(filepath.Join(paths.ClaudeJobs, id, "state.json"))
	if err != nil {
		return review.Progress{}
	}
	var state jobState
	if err := json.Unmarshal(data, &state); err != nil {
		return review.Progress{}
	}
	running := 0
	for _, agent := range state.Fan {
		if agent.DoneAt == nil {
			running++
		}
	}
	return review.Progress{
		Detail:    state.Detail,
		Needs:     state.Needs,
		Active:    state.Tempo == "active",
		Agents:    running,
		UpdatedAt: state.UpdatedAt,
	}
}

// untrustedPrefix starts the message `claude --bg` prints when it refuses a
// directory that nobody has trusted (verified on 2.1.281).
const untrustedPrefix = "Workspace not trusted"

func (Claude) Untrusted(res exec.Result) bool {
	return strings.Contains(res.Stderr, untrustedPrefix) || strings.Contains(res.Stdout, untrustedPrefix)
}

// TrustSpec starts an interactive session whose first message is /exit. claude
// shows its trust prompt before the session starts. When the user accepts, /exit
// runs and claude exits 0. When the user declines, claude exits 1 and trusts
// nothing (verified on 2.1.281). claude's trust check walks up from the
// directory and stops at its git root (read from 2.1.281's code). Every tier-2
// clone is its own git root, so a trusted parent does not cover it. Each clone
// needs this prompt once.
func (Claude) TrustSpec(dir string) exec.CommandSpec {
	return exec.CommandSpec{Path: "claude", Args: []string{"/exit"}, Dir: dir}
}
