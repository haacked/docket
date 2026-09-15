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
	args := append(codexDirs(rec.Dir, paths), fmt.Sprintf("$review-code %s --draft", rec.URL))
	return exec.CommandSpec{Path: "codex", Args: args, Dir: rec.Dir, Unset: scrubbed}
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
func codexDirs(dir string, paths Paths) []string {
	args := []string{"-C", dir}
	for _, grant := range paths.Grant {
		if filepath.IsAbs(grant) {
			args = append(args, "--add-dir", grant)
		}
	}
	return args
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

// scanDayLimit caps how far CaptureSessionID walks. A record whose session
// spanned more days than this has nothing worth resuming.
const scanDayLimit = 7

// How codex names a session's directory and its file, both in local time.
const (
	dayLayout  = "2006/01/02"
	fileLayout = "2006-01-02T15-04-05"
)

// CaptureSessionID finds the session codex just recorded for this record and
// returns its id. A session matches when it ran in the record's directory and
// began no earlier than the record did. The newest match wins, because resuming
// writes a further rollout and the newest one is the conversation as it stands.
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

	dir := resolve(rec.Dir)
	cutoff := rec.StartedAt.Add(-startTolerance)
	// A rollout's name carries its local start time, so this is the name the
	// oldest session worth opening would have.
	floor := "rollout-" + cutoff.Local().Format(fileLayout)

	days := scanDays(rec.StartedAt)
	// Both loops run newest first. The session that just exited is the newest
	// rollout, so the first match is the answer. A real sessions directory holds
	// hundreds of older files, and none of them is opened.
	for i := len(days) - 1; i >= 0; i-- {
		matches, err := filepath.Glob(filepath.Join(paths.CodexSessions, days[i], "rollout-*.jsonl"))
		if err != nil {
			return "", fmt.Errorf("scan codex sessions in %s: %w", days[i], err)
		}
		for j := len(matches) - 1; j >= 0; j-- {
			// Glob sorts lexically. This layout is zero-padded from the year down,
			// so that order is chronological. One name below the floor means every
			// name left, here and in the older days, is below it too.
			if filepath.Base(matches[j]) < floor {
				return "", nil
			}
			meta, ok := readSessionMeta(matches[j])
			if !ok || meta.Payload.ID == "" || meta.Payload.Timestamp.Before(cutoff) {
				continue
			}
			if resolve(meta.Payload.CWD) == dir {
				return meta.Payload.ID, nil
			}
		}
	}
	return "", nil
}

// scanDays lists the directories a session started at startedAt could be filed
// under, oldest first. codex names them for the local date while the timestamps
// inside are UTC. A session started late in the evening is therefore filed a day
// before the one its own timestamp reads.
func scanDays(startedAt time.Time) []string {
	day := midnight(startedAt.Local())
	last := midnight(time.Now().Local())

	var days []string
	for range scanDayLimit {
		days = append(days, day.Format(dayLayout))
		if !day.Before(last) {
			break
		}
		day = day.AddDate(0, 0, 1)
	}
	return days
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

// resolve follows symlinks so two spellings of one directory compare equal.
// codex records the resolved path, and docket's own directories can sit behind a
// link. /tmp is one on macOS, and DOCKET_HOME may be another. A path that will
// not resolve is compared as written, which is all a deleted directory leaves to
// go on.
func resolve(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return resolved
}
