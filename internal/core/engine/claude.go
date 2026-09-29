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

func (Claude) Start(rec review.Record, paths Paths) exec.CommandSpec {
	return claudeSpec(paths, rec.Dir, rec.SessionID, "/review-code "+reviewArgs(rec))
}

func (Claude) Ask(rec review.Record, paths Paths) exec.CommandSpec {
	return claudeSpec(paths, rec.Dir, rec.AskSessionID, askPrompt(rec))
}

// userSettings keeps a session from loading the settings in its working
// directory, which include the hooks the session runs. A tier-2 clone is the
// pull request's own repository, so its author controls those settings.
var userSettings = []string{"--setting-sources", "user"}

func claudeSpec(paths Paths, dir, sessionID, prompt string) exec.CommandSpec {
	args := slices.Clone(userSettings)
	if sessionID != "" {
		args = append(args, "--session-id", sessionID)
	}
	return claudeCommand(paths, dir, append(args, prompt)...)
}

func (Claude) Resume(rec review.Record, paths Paths) (exec.CommandSpec, bool) {
	if rec.SessionID == "" {
		return exec.CommandSpec{}, false
	}
	return claudeCommand(paths, rec.Dir, append([]string{"--resume", rec.SessionID}, userSettings...)...), true
}

// claudeConfigDir is the variable that picks claude's account.
const claudeConfigDir = "CLAUDE_CONFIG_DIR"

// claudeCommand runs claude under the account paths names. For the default
// account the spec unsets the variable rather than inheriting it, because docket
// itself may run with another account's CLAUDE_CONFIG_DIR exported.
func claudeCommand(paths Paths, dir string, args ...string) exec.CommandSpec {
	spec := exec.CommandSpec{Path: "claude", Args: args, Dir: dir}
	if paths.ClaudeConfig == "" {
		spec.Unset = []string{claudeConfigDir}
	} else {
		spec.Set = []string{claudeConfigDir + "=" + paths.ClaudeConfig}
	}
	return spec
}

// CaptureSessionID has nothing to find. NewSessionID already minted the id and
// Start passed it to claude.
func (Claude) CaptureSessionID(review.Record, Paths) (string, error) { return "", nil }
