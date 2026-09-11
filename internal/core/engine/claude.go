package engine

import (
	"fmt"

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

func (Claude) Start(rec review.Record, _ string) exec.CommandSpec {
	args := []string{}
	if rec.SessionID != "" {
		args = append(args, "--session-id", rec.SessionID)
	}
	args = append(args, fmt.Sprintf("/review-code %s --draft", rec.URL))
	return exec.CommandSpec{Path: "claude", Args: args, Dir: rec.Dir}
}

func (Claude) Resume(rec review.Record, _ string) (exec.CommandSpec, bool) {
	if rec.SessionID == "" {
		return exec.CommandSpec{}, false
	}
	return exec.CommandSpec{
		Path: "claude",
		Args: []string{"--resume", rec.SessionID},
		Dir:  rec.Dir,
	}, true
}
