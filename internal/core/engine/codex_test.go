package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/review"
)

// grants are the directories an engine is handed, in the shape config.AgentDirs
// builds them.
var grants = []string{
	"/opt/review-code/.sessions",
	"/opt/review-code/.worktrees",
	"/opt/review-code/.reviews",
}

func codexRecord(dir string, startedAt time.Time) review.Record {
	rec := record()
	rec.Engine = "codex"
	rec.Dir = dir
	rec.SessionID = ""
	rec.StartedAt = startedAt
	return rec
}

// writeRollout lays a session file out the way codex does: under the local date
// the session started, with the metadata on the first line. The date directory
// and the name are local time. The timestamps inside are UTC.
func writeRollout(t *testing.T, sessionsDir, id, cwd string, at time.Time) {
	t.Helper()

	stamp := at.UTC().Format(time.RFC3339)
	line := fmt.Sprintf(
		`{"timestamp":%q,"type":"session_meta","payload":{"id":%q,"session_id":%q,"cwd":%q,"timestamp":%q,"originator":"codex_cli_rs"}}`,
		stamp, id, id, cwd, stamp,
	)
	writeRolloutLine(t, sessionsDir, id, at, line+"\n")
}

func writeRolloutLine(t *testing.T, sessionsDir, id string, at time.Time, content string) {
	t.Helper()

	dir := filepath.Join(sessionsDir, at.Local().Format("2006/01/02"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("rollout-%s-%s.jsonl", at.Local().Format("2006-01-02T15-04-05"), id)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func capture(t *testing.T, sessionsDir string, rec review.Record) string {
	t.Helper()

	id, err := Codex{}.CaptureSessionID(rec, Paths{CodexSessions: sessionsDir})
	if err != nil {
		t.Fatalf("CaptureSessionID: %v", err)
	}
	return id
}

func TestCodexStartRunsTheReviewAsADraftInTheRecordsDirectory(t *testing.T) {
	rec := codexRecord("/tmp/clone", time.Now())

	spec := Codex{}.Start(rec, Paths{Grant: grants})

	if spec.Path != "codex" {
		t.Errorf("path = %q", spec.Path)
	}
	if spec.Dir != "/tmp/clone" {
		t.Errorf("dir = %q", spec.Dir)
	}
	line := spec.String()
	for _, want := range []string{
		"-C /tmp/clone",
		"'$review-code https://github.com/haacked/docket/pull/7 --draft'",
		"--add-dir /opt/review-code/.reviews",
		"--add-dir /opt/review-code/.worktrees",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("command %s is missing %q", line, want)
		}
	}
}

// docket may be launched from a claude session, and the variables that session
// exports tell codex it is running inside one. A claude account's directory
// means nothing to codex. Codex drops it too.
func TestCodexUnsetsTheClaudeEnvironment(t *testing.T) {
	rec := codexRecord("/tmp/clone", time.Now())
	rec.SessionID = "01998e2c-0000-7000-8000-000000000001"
	paths := Paths{Grant: grants, ClaudeConfig: automation}
	resume, ok := Codex{}.Resume(rec, paths)
	if !ok {
		t.Fatal("Resume refused a record with a session id")
	}

	for name, spec := range map[string]exec.CommandSpec{
		"start":  Codex{}.Start(rec, paths),
		"resume": resume,
		"ask":    Codex{}.Ask(rec, paths),
	} {
		got := spec.Env([]string{"CLAUDECODE=1", "CLAUDE_CONFIG_DIR=/elsewhere", "HOME=/h"})
		if !slices.Equal(got, []string{"HOME=/h"}) {
			t.Errorf("%s runs with %v, want only HOME", name, got)
		}
	}
}

// Interactive codex takes no session id on the command line, so docket has none
// to mint and none to pass.
func TestCodexMintsNoSessionID(t *testing.T) {
	if got := (Codex{}).NewSessionID(); got != "" {
		t.Errorf("NewSessionID() = %q, want empty: codex names its own sessions", got)
	}

	rec := codexRecord("/tmp/clone", time.Now())
	rec.SessionID = "01998e2c-0000-7000-8000-000000000001"

	if line := (Codex{}).Start(rec, Paths{}).String(); strings.Contains(line, rec.SessionID) {
		t.Errorf("command = %s, want no session id: interactive codex has no flag for one", line)
	}
}

func TestCodexResumeReopensTheCapturedSession(t *testing.T) {
	rec := codexRecord("/tmp/clone", time.Now())
	rec.SessionID = "01998e2c-0000-7000-8000-000000000001"

	spec, ok := Codex{}.Resume(rec, Paths{Grant: grants})
	if !ok {
		t.Fatal("Resume refused a record with a session id")
	}

	line := spec.String()
	for _, want := range []string{"codex resume", rec.SessionID, "-C /tmp/clone"} {
		if !strings.Contains(line, want) {
			t.Errorf("command %s is missing %q", line, want)
		}
	}
	if spec.Dir != "/tmp/clone" {
		t.Errorf("dir = %q", spec.Dir)
	}
}

func TestCodexResumeRefusesARecordWithNoSession(t *testing.T) {
	rec := codexRecord("/tmp/clone", time.Now())

	if _, ok := (Codex{}).Resume(rec, Paths{}); ok {
		t.Error("Resume accepted a record with no captured session, which would start codex with no conversation to reopen")
	}
}

func TestCodexStartGrantsNothingWhenThereIsNothingToGrant(t *testing.T) {
	rec := codexRecord("/tmp/clone", time.Now())

	line := Codex{}.Start(rec, Paths{}).String()

	if strings.Contains(line, "--add-dir") {
		t.Errorf("command = %s, want no directory granted when none is configured", line)
	}
}

// codex ignores every --add-dir under its default permissions, so a grant
// without the sandbox that admits it is inert and review-code's notes writes
// stop for approval instead.
func TestCodexGrantsComeWithTheSandboxThatAdmitsThem(t *testing.T) {
	rec := codexRecord("/tmp/clone", time.Now())

	for name, line := range map[string]string{
		"start":  Codex{}.Start(rec, Paths{Grant: grants}).String(),
		"resume": resumeLine(t, rec, Paths{Grant: grants}),
	} {
		if !strings.Contains(line, "--sandbox workspace-write") {
			t.Errorf("%s = %s, want the sandbox that lets --add-dir take effect", name, line)
		}
	}
}

// Naming the sandbox widens what codex may write, so it is only named when
// there is a grant that needs it.
func TestCodexNamesNoSandboxWithNothingToGrant(t *testing.T) {
	rec := codexRecord("/tmp/clone", time.Now())

	if line := (Codex{}).Start(rec, Paths{}).String(); strings.Contains(line, "--sandbox") {
		t.Errorf("command = %s, want no sandbox widened when nothing is granted", line)
	}
}

func resumeLine(t *testing.T, rec review.Record, paths Paths) string {
	t.Helper()

	rec.SessionID = "01998e2c-0000-7000-8000-000000000001"
	spec, ok := Codex{}.Resume(rec, paths)
	if !ok {
		t.Fatal("Resume refused a record with a session id")
	}
	return spec.String()
}

// A relative grant names a directory inside codex's own workspace rather than
// the one meant, which is what an unset review-code directory would produce.
func TestCodexStartGrantsOnlyAbsoluteDirectories(t *testing.T) {
	rec := codexRecord("/tmp/clone", time.Now())

	line := Codex{}.Start(rec, Paths{Grant: []string{".reviews", "/opt/review-code/.reviews"}}).String()

	if strings.Contains(line, "--add-dir .reviews") {
		t.Errorf("command = %s, want the relative directory left out", line)
	}
	if !strings.Contains(line, "--add-dir /opt/review-code/.reviews") {
		t.Errorf("command = %s, want the absolute directory granted", line)
	}
}

// codex records the directory it resolved, while the record carries the spelling
// docket built. On macOS the two differ whenever the clone or DOCKET_HOME sits
// under /tmp or /var. A capture that misses is silent. The next resume then
// opens a fresh codex session instead of the conversation.
func TestCaptureSessionIDMatchesThroughASymlinkedDirectory(t *testing.T) {
	sessions, real := t.TempDir(), t.TempDir()
	link := filepath.Join(t.TempDir(), "clone")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("this filesystem has no symlinks: %v", err)
	}

	startedAt := time.Now().Add(-10 * time.Minute)
	// codex writes the resolved path; the record holds the link.
	writeRollout(t, sessions, "01998e2c-0000-7000-8000-0000000000aa", real, startedAt.Add(time.Second))

	if got := capture(t, sessions, codexRecord(link, startedAt)); got != "01998e2c-0000-7000-8000-0000000000aa" {
		t.Errorf("id = %q, want the session found through the link", got)
	}
}

func TestCaptureSessionIDFindsTheSessionThatRanInTheRecordsDirectory(t *testing.T) {
	sessions, dir := t.TempDir(), t.TempDir()
	startedAt := time.Now().Add(-10 * time.Minute)
	writeRollout(t, sessions, "01998e2c-0000-7000-8000-00000000cafe", dir, startedAt.Add(time.Second))

	if got := capture(t, sessions, codexRecord(dir, startedAt)); got != "01998e2c-0000-7000-8000-00000000cafe" {
		t.Errorf("id = %q, want the session recorded for this directory", got)
	}
}

func TestCaptureSessionIDIgnoresSessionsThatAreNotThisReview(t *testing.T) {
	sessions, dir, elsewhere := t.TempDir(), t.TempDir(), t.TempDir()
	startedAt := time.Now().Add(-10 * time.Minute)

	writeRollout(t, sessions, "other-directory", elsewhere, startedAt.Add(time.Second))
	writeRollout(t, sessions, "before-this-record", dir, startedAt.Add(-time.Hour))

	if got := capture(t, sessions, codexRecord(dir, startedAt)); got != "" {
		t.Errorf("id = %q, want none: neither session belongs to this record", got)
	}
}

// Resuming writes a further rollout for the same directory, so the newest match
// is the conversation as it now stands.
// review-code runs each of its reviewers through `codex exec` from inside the
// session, passing no working directory, so every reviewer writes a rollout in
// this same directory with a later timestamp. docket's own session is the first
// one after the launch. Taking the newest would hand back a reviewer's thread,
// which codex refuses to resume through its parent.
func TestCaptureSessionIDTakesTheFirstSessionAfterTheLaunch(t *testing.T) {
	sessions, dir := t.TempDir(), t.TempDir()
	startedAt := time.Now().Add(-time.Hour)

	writeRollout(t, sessions, "docket-session", dir, startedAt.Add(time.Minute))
	writeRollout(t, sessions, "reviewer-subagent", dir, startedAt.Add(30*time.Minute))

	if got := capture(t, sessions, codexRecord(dir, startedAt)); got != "docket-session" {
		t.Errorf("id = %q, want the session docket launched rather than a reviewer's", got)
	}
}

// The date directories and the name are local time, while the timestamp inside
// is UTC. A session started late yesterday is filed under yesterday whichever
// side of midnight UTC it fell on, so a scan of today alone misses it.
func TestCaptureSessionIDFindsASessionFiledUnderAnEarlierDay(t *testing.T) {
	sessions, dir := t.TempDir(), t.TempDir()
	startedAt := time.Now().Add(-26 * time.Hour)

	writeRollout(t, sessions, "yesterday", dir, startedAt.Add(time.Minute))

	if got := capture(t, sessions, codexRecord(dir, startedAt)); got != "yesterday" {
		t.Errorf("id = %q, want the session recorded the day the record started", got)
	}
}

// codex files a rollout under the local date the session began, and the record
// is stamped immediately before the launch, so those are the only two days that
// can hold it. The second covers a launch that crosses midnight.
func TestScanDaysCoversTheStartDayAndTheOneAfterIt(t *testing.T) {
	const layout = "2006/01/02"
	startedAt := time.Now().Add(-48 * time.Hour)

	days := scanDays(startedAt)

	// The paths are named for local dates, so the list has to be built in local
	// time or it names a directory codex never wrote.
	want := []string{
		startedAt.Local().Format(layout),
		startedAt.Local().AddDate(0, 0, 1).Format(layout),
	}
	if !slices.Equal(days, want) {
		t.Errorf("days = %v, want %v", days, want)
	}
}

// The directory belongs to codex and may hold anything. A file docket cannot
// read is one it does not recognize, not a failure.
func TestCaptureSessionIDToleratesFilesItCannotRead(t *testing.T) {
	sessions, dir := t.TempDir(), t.TempDir()
	startedAt := time.Now().Add(-10 * time.Minute)
	at := startedAt.Add(time.Second)

	writeRolloutLine(t, sessions, "empty", at, "")
	writeRolloutLine(t, sessions, "not-json", at, "this is not json at all\n")
	writeRolloutLine(t, sessions, "another-type", at, `{"type":"response_item","payload":{}}`+"\n")
	writeRolloutLine(t, sessions, "truncated-json", at, `{"type":"session_meta","payload":{"id":`+"\n")

	// A directory where a rollout file should be: opening it works and reading
	// it does not.
	day := filepath.Join(sessions, at.Local().Format("2006/01/02"))
	if err := os.MkdirAll(filepath.Join(day, "rollout-a-directory.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}

	writeRollout(t, sessions, "the-real-one", dir, at)

	if got := capture(t, sessions, codexRecord(dir, startedAt)); got != "the-real-one" {
		t.Errorf("id = %q, want the one session docket could read", got)
	}
}

// codex flushes the metadata before the session has written anything else, so
// the file can hold one complete JSON object and no newline yet.
func TestCaptureSessionIDReadsAMetaLineWithNoTrailingNewline(t *testing.T) {
	sessions, dir := t.TempDir(), t.TempDir()
	startedAt := time.Now().Add(-10 * time.Minute)
	at := startedAt.Add(time.Second)
	stamp := at.UTC().Format(time.RFC3339)

	writeRolloutLine(t, sessions, "unterminated", at, fmt.Sprintf(
		`{"timestamp":%q,"type":"session_meta","payload":{"id":"unterminated","cwd":%q,"timestamp":%q}}`,
		stamp, dir, stamp,
	))

	if got := capture(t, sessions, codexRecord(dir, startedAt)); got != "unterminated" {
		t.Errorf("id = %q, want the session whose first line was not newline-terminated", got)
	}
}

func TestCaptureSessionIDFindsNothingInAnEmptyDirectory(t *testing.T) {
	sessions, dir := t.TempDir(), t.TempDir()

	id, err := Codex{}.CaptureSessionID(codexRecord(dir, time.Now()), Paths{CodexSessions: sessions})
	if err != nil {
		t.Fatalf("CaptureSessionID: %v", err)
	}
	if id != "" {
		t.Errorf("id = %q, want none", id)
	}
}

// The payload names the session twice. id is what `codex resume` takes.
func TestCaptureSessionIDPrefersTheIDOverTheSessionID(t *testing.T) {
	sessions, dir := t.TempDir(), t.TempDir()
	startedAt := time.Now().Add(-10 * time.Minute)
	at := startedAt.Add(time.Second)
	stamp := at.UTC().Format(time.RFC3339)

	writeRolloutLine(t, sessions, "both", at, fmt.Sprintf(
		`{"type":"session_meta","payload":{"id":"the-id","session_id":"the-session-id","cwd":%q,"timestamp":%q}}`+"\n",
		dir, stamp,
	))

	if got := capture(t, sessions, codexRecord(dir, startedAt)); got != "the-id" {
		t.Errorf("id = %q, want the id `codex resume` takes", got)
	}
}

func TestCaptureSessionIDNeedsSomewhereToLook(t *testing.T) {
	dir := t.TempDir()

	if _, err := (Codex{}).CaptureSessionID(codexRecord(dir, time.Now()), Paths{}); err == nil {
		t.Error("CaptureSessionID searched with no sessions directory configured")
	}
}

func TestCaptureSessionIDNeedsADirectoryToMatchAgainst(t *testing.T) {
	sessions := t.TempDir()

	if _, err := (Codex{}).CaptureSessionID(codexRecord("", time.Now()), Paths{CodexSessions: sessions}); err == nil {
		t.Error("CaptureSessionID matched a record with no directory, which any session would answer")
	}
}

// claude takes its id on the command line, so the record already carries it.
func TestClaudeCapturesNothing(t *testing.T) {
	id, err := Claude{}.CaptureSessionID(record(), Paths{})
	if err != nil {
		t.Fatalf("CaptureSessionID: %v", err)
	}
	if id != "" {
		t.Errorf("id = %q, want none: Start already passed the id claude used", id)
	}
}
