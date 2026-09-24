package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/core/tier"
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

// A background review has nobody to answer review-code's prompts. --force
// answers the pre-flight one, and the record's intent answers the one about a
// notes file that already exists.
func TestABackgroundReviewAnswersEveryPrompt(t *testing.T) {
	for _, tc := range intentFlags {
		line := (Claude{}).StartBackground(intentRecord(tc.intent), Paths{}).String()
		if !strings.Contains(line, "--force") {
			t.Errorf("%s: command %s dropped the pre-flight answer", tc.intent, line)
		}
		if tc.want != "" && !strings.Contains(line, tc.want) {
			t.Errorf("%s: command %s is missing %s", tc.intent, line, tc.want)
		}
		for _, flag := range tc.refused {
			if strings.Contains(line, flag) {
				t.Errorf("%s: command %s carries %s", tc.intent, line, flag)
			}
		}
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

// lostListing holds the session a record launched moments before docket died,
// a later one dispatched from inside it, one in another directory, and one that
// started before the record did.
// launchedAt is when the record started, as claude and docket both count it:
// milliseconds since the epoch, on this machine's clock.
const launchedAt = 1789606668709

const lostListing = `[
  {"id": "11111111", "kind": "background", "cwd": "/tmp/clone", "startedAt": 1789606669000, "state": "working"},
  {"id": "22222222", "kind": "background", "cwd": "/tmp/clone", "startedAt": 1789606700000, "state": "working"},
  {"id": "33333333", "kind": "background", "cwd": "/tmp/other", "startedAt": 1789606669000, "state": "working"},
  {"id": "44444444", "kind": "background", "cwd": "/tmp/clone", "startedAt": 1789606000000, "state": "working"}
]`

func lostRecord() review.Record {
	rec := record()
	rec.Tier = tier.Tier2
	rec.StartedAt = time.UnixMilli(launchedAt).UTC()
	return rec
}

// A launch records its id in a second step, so a docket killed in between
// leaves a record naming no session and an agent nobody is watching.
func TestRecoverFindsTheSessionALaunchNeverRecorded(t *testing.T) {
	id, ok := (Claude{}).RecoverBackgroundID(lostRecord(), exec.Result{Stdout: lostListing})
	if !ok {
		t.Fatal("RecoverBackgroundID found nothing to adopt")
	}
	// The oldest match at or after the launch. A review dispatches further
	// sessions from inside the one docket started, and they carry the same
	// directory and a later time, while a session from before the launch is not
	// this record's at all.
	if id != "11111111" {
		t.Errorf("id = %q, want the session the record started", id)
	}
}

func TestRecoverIgnoresSessionsThatCannotBeThisRecords(t *testing.T) {
	rec := lostRecord()

	// A tier-1 review shares one scratch directory with every other one, so a
	// match there could belong to another record.
	tier1 := rec
	tier1.Tier = tier.Tier1
	if _, ok := (Claude{}).RecoverBackgroundID(tier1, exec.Result{Stdout: lostListing}); ok {
		t.Error("a tier-1 record adopted a session from a shared directory")
	}

	// A record that never launched has no start time to measure against.
	unstarted := rec
	unstarted.StartedAt = time.Time{}
	if _, ok := (Claude{}).RecoverBackgroundID(unstarted, exec.Result{Stdout: lostListing}); ok {
		t.Error("a record that never launched adopted a session")
	}

	elsewhere := rec
	elsewhere.Dir = "/tmp/nowhere"
	if _, ok := (Claude{}).RecoverBackgroundID(elsewhere, exec.Result{Stdout: lostListing}); ok {
		t.Error("a record adopted a session from another directory")
	}

	if _, ok := (Claude{}).RecoverBackgroundID(rec, exec.Result{Stdout: "[]"}); ok {
		t.Error("a record adopted a session out of an empty listing")
	}
}

// A session that ends its turn with a question keeps its process and reports
// blocked. It is not done, and docket has to know it is waiting.
func TestParseStatusReportsABlockedSession(t *testing.T) {
	statuses, err := Claude{}.ParseStatus(exec.Result{
		Stdout: `[{"id": "aaaaaaaa", "kind": "background", "state": "blocked", "status": "idle", "pid": 5}]`,
	})
	if err != nil {
		t.Fatalf("ParseStatus: %v", err)
	}
	got := statuses["aaaaaaaa"]
	if !got.Blocked {
		t.Error("a blocked session did not read as blocked")
	}
	if got.Done {
		t.Error("a blocked session read as finished")
	}
	if !got.Idle {
		t.Error("a blocked session ended its turn but did not read as idle")
	}
}

// A session that ends its turn without asking anything keeps its process and
// its working state, and only the status says the turn is over. review-code's
// background runs end this way when the last message tells the user what to do
// next rather than asking.
func TestParseStatusReportsASessionThatEndedItsTurnAsIdle(t *testing.T) {
	statuses, err := Claude{}.ParseStatus(exec.Result{Stdout: `[
		{"id": "aaaaaaaa", "kind": "background", "state": "working", "status": "idle", "pid": 5},
		{"id": "bbbbbbbb", "kind": "background", "state": "working", "status": "busy", "pid": 6}
	]`})
	if err != nil {
		t.Fatalf("ParseStatus: %v", err)
	}
	idle := statuses["aaaaaaaa"]
	if !idle.Idle {
		t.Error("a session that ended its turn did not read as idle")
	}
	if idle.Done || idle.Blocked {
		t.Errorf("done %v, blocked %v, want neither: claude still holds the session and it asked nothing", idle.Done, idle.Blocked)
	}
	if statuses["bbbbbbbb"].Idle {
		t.Error("a busy session read as idle")
	}
}
