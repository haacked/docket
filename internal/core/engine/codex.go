package engine

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/review"
)

// Codex runs the review in an interactive `codex` session.
type Codex struct{}

func (Codex) Name() string   { return "codex" }
func (Codex) Binary() string { return "codex" }

// NewSessionID mints nothing. Interactive codex takes no session id on the
// command line, so the id exists only after the session has run and
// CaptureSessionID reads it back off disk.
func (Codex) NewSessionID() string { return "" }

// scrubbed are the variables a claude session exports that would tell codex it
// is running inside one. docket may be launched from a claude session itself.
var scrubbed = []string{"CLAUDECODE", "CLAUDE_CONFIG_DIR"}

func (Codex) Start(rec review.Record, paths Paths) exec.CommandSpec {
	return codexSpec(rec.Dir, paths, "$review-code "+reviewArgs(rec))
}

func (Codex) Ask(rec review.Record, paths Paths) exec.CommandSpec {
	return codexSpec(rec.Dir, paths, askPrompt(rec))
}

func codexSpec(dir string, paths Paths, prompt string) exec.CommandSpec {
	args := append(codexDirs(dir, paths), prompt)
	return exec.CommandSpec{Path: "codex", Args: args, Dir: dir, Unset: scrubbed}
}

func (Codex) Resume(rec review.Record, paths Paths) (exec.CommandSpec, bool) {
	if rec.SessionID == "" {
		return exec.CommandSpec{}, false
	}
	args := append([]string{"resume", rec.SessionID}, codexDirs(rec.Dir, paths)...)
	return exec.CommandSpec{
		Path:  "codex",
		Args:  args,
		Dir:   rec.Dir,
		Unset: scrubbed,
	}, true
}

// codexDirs is the working root plus the directories the session writes to
// outside it. A relative grant would name a directory under codex's own
// workspace, so only absolute ones are passed.
//
// The sandbox is named alongside them because codex ignores every --add-dir
// under its default permissions, reporting that the effective permissions allow
// no additional writable roots. review-code writes its notes and its worktrees
// outside the working root, so the grants are inert without this and each of
// those writes stops for approval.
func codexDirs(dir string, paths Paths) []string {
	grants := make([]string, 0, 2*len(paths.Grant))
	for _, grant := range paths.Grant {
		if filepath.IsAbs(grant) {
			grants = append(grants, "--add-dir", grant)
		}
	}
	if len(grants) == 0 {
		return []string{"-C", dir}
	}
	return append([]string{"-C", dir, "--sandbox", "workspace-write"}, grants...)
}

// sessionMeta is the first line of a codex rollout file.
type sessionMeta struct {
	Type    string `json:"type"`
	Payload struct {
		ID        string    `json:"id"`
		CWD       string    `json:"cwd"`
		Timestamp time.Time `json:"timestamp"`
	} `json:"payload"`
}

// startTolerance forgives the truncation between docket's own clock and the
// timestamp codex writes. Both clocks are this machine's, so the window only has
// to cover rounding. A narrow window is what keeps an earlier session in the
// same directory from matching.
const startTolerance = 2 * time.Second

// How codex names a session's directory and its file, both in local time.
const (
	dayLayout  = "2006/01/02"
	fileLayout = "2006-01-02T15-04-05"
)

// CaptureSessionID finds the session codex just recorded for this record and
// returns its id. A session matches when it ran in the record's directory and
// began no earlier than the record did. The oldest match wins.
//
// Oldest rather than newest, because docket's session is not the only codex
// process in that directory. review-code dispatches each of its reviewers
// through `codex exec` from inside the running session and passes no working
// directory, so every reviewer inherits this one and writes its own rollout
// carrying the same cwd and a later timestamp. Those rollouts outnumber the real
// one. codex also refuses outright to resume a sub-agent thread through its
// parent. A resume is still captured, because launch stamps StartedAt again and
// the cutoff moves past the earlier session with it.
//
// Nothing found is not an error. The record keeps no id, and the next launch
// starts a fresh session rather than resuming.
func (Codex) CaptureSessionID(rec review.Record, paths Paths) (string, error) {
	if paths.CodexSessions == "" {
		return "", fmt.Errorf("no codex sessions directory configured")
	}
	if rec.Dir == "" {
		return "", fmt.Errorf("no directory to match a codex session against")
	}

	dir := Resolve(rec.Dir)
	cutoff := rec.StartedAt.Add(-startTolerance)
	// A rollout's name carries its local start time, so this is the name the
	// oldest session worth opening would have.
	floor := "rollout-" + cutoff.Local().Format(fileLayout)

	for _, day := range scanDays(rec.StartedAt) {
		matches, err := filepath.Glob(filepath.Join(paths.CodexSessions, day, "rollout-*.jsonl"))
		if err != nil {
			return "", fmt.Errorf("scan codex sessions in %s: %w", day, err)
		}
		for _, path := range matches {
			// Glob sorts lexically. This layout is zero-padded from the year down,
			// so that order is chronological. A name below the floor belongs to a
			// session that started before this one. Comparing the name is what keeps
			// the hundreds of older files in a real sessions directory unopened.
			if filepath.Base(path) < floor {
				continue
			}
			meta, ok := readSessionMeta(path)
			if !ok || meta.Payload.ID == "" || meta.Payload.Timestamp.Before(cutoff) {
				continue
			}
			if Resolve(meta.Payload.CWD) == dir {
				return meta.Payload.ID, nil
			}
		}
	}
	return "", nil
}

// scanDays lists the directories a session started at startedAt could be filed
// under, oldest first. codex files a rollout under the local date the session
// began, and launch stamps StartedAt immediately before handing over the
// terminal, so only that day and the one after it can hold it. The second day
// covers a launch that crosses midnight.
//
// codex names the directories for the local date while the timestamps inside
// them are UTC, which is why these are built in local time.
func scanDays(startedAt time.Time) []string {
	day := midnight(startedAt.Local())
	return []string{day.Format(dayLayout), day.AddDate(0, 0, 1).Format(dayLayout)}
}

func midnight(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// readSessionMeta reads a rollout's first line. A file docket cannot read or
// parse is one it does not recognize, not a failure: the directory belongs to
// codex and may hold anything.
func readSessionMeta(path string) (sessionMeta, bool) {
	f, err := os.Open(path)
	if err != nil {
		return sessionMeta{}, false
	}
	defer f.Close()

	// The file grows for as long as the session runs.
	line, err := bufio.NewReader(f).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return sessionMeta{}, false
	}

	var meta sessionMeta
	if err := json.Unmarshal(line, &meta); err != nil || meta.Type != "session_meta" {
		return sessionMeta{}, false
	}
	return meta, true
}

// Resolve follows symlinks so two spellings of one directory compare equal.
// codex records the resolved path. os.Getwd may return either spelling.
// docket's own directories can sit behind a link. /tmp is one on macOS.
// DOCKET_HOME may be another. A path that will not resolve is compared as
// written, which is all a deleted directory leaves to go on.
func Resolve(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return resolved
}
