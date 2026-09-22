package engine

import (
	"github.com/google/uuid"

	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/review"
)

// Claude runs the review in an interactive `claude` session. docket generates the
// session id before launching, so resuming needs nothing captured afterwards.
type Claude struct{}

func (Claude) Name() string   { return "claude" }
func (Claude) Binary() string { return "claude" }

func (Claude) NewSessionID() string { return uuid.NewString() }

func (Claude) Start(rec review.Record, _ Paths) exec.CommandSpec {
	return claudeSpec(rec.Dir, rec.SessionID, "/review-code "+reviewArgs(rec))
}

func (Claude) Ask(rec review.Record, _ Paths) exec.CommandSpec {
	return claudeSpec(rec.Dir, rec.AskSessionID, askPrompt(rec))
}

func claudeSpec(dir, sessionID, prompt string) exec.CommandSpec {
	args := []string{}
	if sessionID != "" {
		args = append(args, "--session-id", sessionID)
	}
	args = append(args, prompt)
	return exec.CommandSpec{Path: "claude", Args: args, Dir: dir}
}

func (Claude) Resume(rec review.Record, _ Paths) (exec.CommandSpec, bool) {
	if rec.SessionID == "" {
		return exec.CommandSpec{}, false
	}
	return exec.CommandSpec{
		Path: "claude",
		Args: []string{"--resume", rec.SessionID},
		Dir:  rec.Dir,
	}, true
}

// CaptureSessionID has nothing to find. NewSessionID already minted the id and
// Start passed it to claude.
func (Claude) CaptureSessionID(review.Record, Paths) (string, error) { return "", nil }
