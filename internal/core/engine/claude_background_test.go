package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/haacked/docket/internal/core/exec"
)

// backgrounded is what `claude --bg` prints on stdout. The lines under the id
// are the commands that take it, and they carry the id too, which is what makes
// taking the first matching line rather than the last one matter.
const backgrounded = `backgrounded · 6d681a76
  claude agents             list sessions
  claude attach 6d681a76    open in this terminal
`

func TestStartBackgroundForcesTheReviewPastItsPrompts(t *testing.T) {
	spec := Claude{}.StartBackground(record(), Paths{Grant: grants})

	if spec.Dir != "/tmp/clone" {
		t.Errorf("dir = %q, want the record's directory", spec.Dir)
	}
	line := spec.String()
	for _, want := range []string{"--bg", "--force", "--draft"} {
		if !strings.Contains(line, want) {
			t.Errorf("command %s is missing %q", line, want)
		}
	}
	// claude refuses --session-id here and mints its own, so passing the
	// record's would store an id the session never had.
	if strings.Contains(line, "--session-id") {
		t.Errorf("command %s passes a session id claude ignores", line)
	}
}

func TestParseBackgroundIDTakesTheIdClaudeReported(t *testing.T) {
	id, err := Claude{}.ParseBackgroundID(exec.Result{
		Stdout: backgrounded,
		Stderr: "Starting background service…\n",
	})
	if err != nil {
		t.Fatalf("ParseBackgroundID: %v", err)
	}
	if id != "6d681a76" {
		t.Errorf("id = %q, want 6d681a76", id)
	}
}

func TestParseBackgroundIDSaysWhatClaudePrintedInstead(t *testing.T) {
	_, err := Claude{}.ParseBackgroundID(exec.Result{Stderr: "not logged in\n"})
	if err == nil {
		t.Fatal("ParseBackgroundID accepted output carrying no id")
	}
	if !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("error %q drops what claude said", err)
	}
}

// listing is `claude agents --json --all`. It holds an interactive session, a
// finished background session claude still holds, and one that was stopped.
const listing = `[
  {"pid": 32107, "cwd": "/tmp/other", "kind": "interactive",
   "startedAt": 1787787516695, "sessionId": "a3ed6486-1655-4d5a-a26e-518e9a3841f7", "status": "idle"},
  {"pid": 87280, "id": "6d681a76", "cwd": "/tmp/clone", "kind": "background",
   "startedAt": 1789586633888, "sessionId": "6d681a76-7638-4a56-a4cb-bc0353e547d5",
   "status": "idle", "state": "done"},
  {"id": "ebdb2938", "cwd": "/tmp/clone", "kind": "background",
   "startedAt": 1789586795052, "sessionId": "ebdb2938-8705-436f-8fdb-87237cec3130", "state": "done"},
  {"pid": 16250, "id": "1c56cc66", "cwd": "/tmp/clone", "kind": "background",
   "startedAt": 1789586795052, "sessionId": "1c56cc66-0000-4000-8000-000000000001",
   "status": "busy", "state": "working"}
]`

func TestParseStatusKeepsOnlyTheSessionsDocketCouldHaveStarted(t *testing.T) {
	statuses, err := Claude{}.ParseStatus(exec.Result{Stdout: listing})
	if err != nil {
		t.Fatalf("ParseStatus: %v", err)
	}

	if len(statuses) != 3 {
		t.Fatalf("got %d statuses, want the 3 background ones", len(statuses))
	}
	// An interactive session carries no short id, so there is nothing for a
	// record to match it on.
	for id := range statuses {
		if id == "" {
			t.Error("an entry with no id reached the status map")
		}
	}

	done := statuses["6d681a76"]
	if !done.Done {
		t.Errorf("state %q should read as finished", done.State)
	}
	if !done.Live {
		t.Error("claude still holds a finished session, so it reads as live")
	}
	if done.SessionID != "6d681a76-7638-4a56-a4cb-bc0353e547d5" {
		t.Errorf("session id = %q", done.SessionID)
	}

	if statuses["ebdb2938"].Live {
		t.Error("a stopped session keeps no process, so it is not live")
	}

	working := statuses["1c56cc66"]
	if working.Done {
		t.Errorf("state %q should not read as finished", working.State)
	}
	if working.Activity != "busy" {
		t.Errorf("activity = %q, want what the session is doing", working.Activity)
	}
}

func TestParseStatusRefusesOutputThatIsNotTheListing(t *testing.T) {
	if _, err := (Claude{}).ParseStatus(exec.Result{Stdout: "command not found"}); err == nil {
		t.Error("ParseStatus accepted output that is not the session list")
	}
}

// An unknown state means the session is still going. Reading one as finished
// would send docket to GitHub for a review still being written.
func TestParseStatusTreatsAnUnfamiliarStateAsStillRunning(t *testing.T) {
	statuses, err := Claude{}.ParseStatus(exec.Result{
		Stdout: `[{"id": "aaaaaaaa", "kind": "background", "state": "compacting", "pid": 5}]`,
	})
	if err != nil {
		t.Fatalf("ParseStatus: %v", err)
	}
	if statuses["aaaaaaaa"].Done {
		t.Error("an unfamiliar state read as finished")
	}
}

func TestOpenAttachesWhileClaudeStillHoldsTheSession(t *testing.T) {
	rec := record()
	rec.BGID = "6d681a76"

	spec, ok := Claude{}.OpenSpec(rec, BGStatus{State: "done", Live: true}, Paths{})
	if !ok {
		t.Fatal("OpenSpec refused a live background session")
	}
	if want := "[/tmp/clone] claude attach 6d681a76"; spec.String() != want {
		t.Errorf("got  %s\nwant %s", spec, want)
	}
}

// Once the session is stopped claude lets go of it, and a plain resume is what
// reopens it. While claude holds it, resume refuses and says to attach.
func TestOpenResumesOnceClaudeHasLetGo(t *testing.T) {
	rec := record()
	rec.BGID = "6d681a76"

	spec, ok := Claude{}.OpenSpec(rec, BGStatus{State: "done"}, Paths{})
	if !ok {
		t.Fatal("OpenSpec refused a stopped background session")
	}
	if !strings.Contains(spec.String(), "--resume "+rec.SessionID) {
		t.Errorf("command %s should resume the captured session", spec)
	}
}

func TestStopEndsTheSessionWithoutDeletingIt(t *testing.T) {
	rec := record()
	rec.BGID = "6d681a76"

	// rm is the command that deletes the conversation. docket never runs it.
	if line := (Claude{}).StopSpec(rec, Paths{}).String(); line != "claude stop 6d681a76" {
		t.Errorf("command = %s", line)
	}
}

// docket keeps the notes of every review it runs, so a second review of a pull
// request finds that file already there. review-code then asks what to do with
// it, and --force does not answer that prompt.
func TestAReReviewAnswersTheExistingNotesPrompt(t *testing.T) {
	rec := record()
	rec.NotesPath = filepath.Join(t.TempDir(), "pr-7.md")

	if line := (Claude{}).StartBackground(rec, Paths{}).String(); strings.Contains(line, "--append") {
		t.Errorf("command %s appends to notes that are not there", line)
	}

	if err := os.WriteFile(rec.NotesPath, []byte("# an earlier review\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	line := (Claude{}).StartBackground(rec, Paths{}).String()
	if !strings.Contains(line, "--append") {
		t.Errorf("command %s would stop at the existing-review prompt with nobody to answer", line)
	}
	if !strings.Contains(line, "--force") {
		t.Errorf("command %s dropped the pre-flight answer", line)
	}
}

func TestOnlyClaudeRunsReviewsInTheBackground(t *testing.T) {
	if _, ok := Background("claude"); !ok {
		t.Error("claude should run background reviews")
	}
	// codex 0.150.1 has no background primitive: a review would be a child of
	// docket and die with it.
	if _, ok := Background("codex"); ok {
		t.Error("codex has no background mode to offer")
	}
	if _, ok := Background("aider"); ok {
		t.Error("an unknown engine has no background mode")
	}
	if names := BackgroundNames(); len(names) != 1 || names[0] != "claude" {
		t.Errorf("BackgroundNames() = %v", names)
	}
}

// `claude stop` on a session that was still working leaves it as "stopped",
// which is not "done". A user can stop one from outside docket, and a record
// waiting for that session to finish would wait for ever.
func TestASessionClaudeNoLongerHoldsIsOver(t *testing.T) {
	statuses, err := (Claude{}).ParseStatus(exec.Result{
		Stdout: `[{"id": "bbbbbbbb", "kind": "background", "state": "stopped"}]`,
	})
	if err != nil {
		t.Fatalf("ParseStatus: %v", err)
	}
	stopped := statuses["bbbbbbbb"]
	if !stopped.Done {
		t.Error("a stopped session reads as still running")
	}
	if stopped.Live {
		t.Error("a stopped session keeps no process, so it is not live")
	}
}

// review-code leaves the draft review out of a review of your own pull request
// unless it is told otherwise, so docket would find nothing on GitHub and call
// the review unreviewed however well the session went.
func TestReviewingYourOwnPullRequestAsksForTheDraft(t *testing.T) {
	rec := record()
	if line := (Claude{}).Start(rec, Paths{}).String(); strings.Contains(line, "--self") {
		t.Errorf("command %s passes --self on somebody else's pull request", line)
	}

	rec.OwnPR = true
	for name, line := range map[string]string{
		"start":      (Claude{}).Start(rec, Paths{}).String(),
		"background": (Claude{}).StartBackground(rec, Paths{}).String(),
		"codex":      (Codex{}).Start(rec, Paths{}).String(),
	} {
		if !strings.Contains(line, "--self") {
			t.Errorf("%s command %s would produce no draft review", name, line)
		}
	}
}
