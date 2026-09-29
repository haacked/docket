package engine

import (
	"slices"

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

// userSettings keeps a session from loading the settings in its working
// directory, which include the hooks the session runs. A tier-2 clone is the
// pull request's own repository, so its author controls those settings.
var userSettings = []string{"--setting-sources", "user"}

func claudeSpec(dir, sessionID, prompt string) exec.CommandSpec {
	args := slices.Clone(userSettings)
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
		Args: append([]string{"--resume", rec.SessionID}, userSettings...),
		Dir:  rec.Dir,
	}, true
}

// CaptureSessionID has nothing to find. NewSessionID already minted the id and
// Start passed it to claude.
func (Claude) CaptureSessionID(review.Record, Paths) (string, error) { return "", nil }
