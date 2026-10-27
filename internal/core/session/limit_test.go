package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/haacked/docket/internal/core/account"
	"github.com/haacked/docket/internal/core/engine"
	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/review"
)

// refusedTurn is the line claude writes to a session's transcript when the
// account's usage limit refuses the turn (2.1.308).
func refusedTurn(at, resets time.Time) string {
	return fmt.Sprintf(`{"type":"assistant","isSidechain":false,"timestamp":%q,"isApiErrorMessage":true,"error":"rate_limit","apiErrorParams":{"rate_limit_info":{"status":"rejected","resetsAt":%d}}}`, at.Format(time.RFC3339Nano), resets.Unix())
}

// writeTranscript writes a session's transcript where claude keeps it under the
// account whose projects directory is projects.
func writeTranscript(t *testing.T, projects, sessionID string, lines ...string) {
	t.Helper()
	dir := filepath.Join(projects, "-tmp-clone")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sessionID+".jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func underAccount(dir, command string) string {
	return "CLAUDE_CONFIG_DIR=" + dir + " claude " + command
}

// rotation is a service configured with two claude accounts and a background
// review whose session claude refused for the first account's usage limit. Each
// account starts and lists its own session.
type rotation struct {
	svc               *Service
	ghc               *fakeGH
	runner            *exec.Fake
	rec               review.Record
	primary, fallback string
	resets            time.Time
}

func refusedForUsage(t *testing.T) rotation {
	t.Helper()
	r := rotation{ghc: &fakeGH{login: "haacked", info: prInfo()}, primary: t.TempDir(), fallback: t.TempDir()}
	r.svc, _ = newService(t, r.ghc, newFakeGit())
	r.svc.Cfg.ClaudeConfigDir = r.primary
	r.svc.Cfg.ClaudeFallbackConfigDirs = []string{r.fallback}
	r.runner = &exec.Fake{Results: map[string]exec.Result{
		underAccount(r.primary, "--bg"):    {Stdout: "backgrounded · 6d681a76\n"},
		underAccount(r.fallback, "--bg"):   {Stdout: "backgrounded · bbbbbbbb\n"},
		underAccount(r.primary, "agents"):  {Stdout: bgListing("6d681a76", bgSession, "blocked", true)},
		underAccount(r.fallback, "agents"): {Stdout: bgListing("bbbbbbbb", "bbbbbbbb-0000-4000-8000-000000000002", "working", true)},
	}}
	r.rec = startedBackground(t, r.svc, r.runner)
	r.resets = r.svc.now().Add(48 * time.Hour)
	writeTranscript(t, r.svc.Cfg.ClaudeProjectsDir(r.primary), bgSession, refusedTurn(r.svc.now(), r.resets), `{"type":"system","subtype":"turn_duration"}`)
	return r
}

func (r rotation) poll(t *testing.T) (review.Record, map[string]engine.BGStatus) {
	t.Helper()
	records, statuses, err := r.svc.PollBackground(context.Background())
	if err != nil {
		t.Fatalf("PollBackground: %v", err)
	}
	got, ok := find(records, r.rec.ID)
	if !ok {
		t.Fatalf("the poll returned no record %s", r.rec.ID)
	}
	return got, statuses
}

// launches lists the background starts runner ran under the account dir
// names.
func launches(runner *exec.Fake, dir string) []exec.CommandSpec {
	var out []exec.CommandSpec
	for _, spec := range claudeCalls(runner.Calls, "--bg") {
		if accountOf(spec) == dir {
			out = append(out, spec)
		}
	}
	return out
}

func TestAPollStartsAReviewRefusedForUsageAgainUnderTheFallbackAccount(t *testing.T) {
	r := refusedForUsage(t)

	got, statuses := r.poll(t)

	if got.State != review.StateReviewing || got.BGID != "bbbbbbbb" || got.ClaudeConfigDir != r.fallback {
		t.Errorf("record is %q with session %q under %q, want reviewing with the new session under the fallback", got.State, got.BGID, got.ClaudeConfigDir)
	}
	if stored := storedByID(t, r.svc, r.rec.ID); stored.BGID != "bbbbbbbb" || stored.ClaudeConfigDir != r.fallback {
		t.Errorf("stored session %q under %q, want the new session under the fallback", stored.BGID, stored.ClaudeConfigDir)
	}
	stops := claudeCalls(r.runner.Calls, "stop")
	if len(stops) != 1 || accountOf(stops[0]) != r.primary {
		t.Errorf("stops = %v, want the refused session stopped under its own account", r.runner.Lines())
	}
	if n := len(launches(r.runner, r.fallback)); n != 1 {
		t.Errorf("%d starts under the fallback, want 1:\n%s", n, strings.Join(r.runner.Lines(), "\n"))
	}
	// The status the poll read belongs to the stopped session.
	if _, ok := statuses[r.rec.ID]; ok {
		t.Error("the poll reported the stopped session's status for the new one")
	}
}

// docket stays on the fallback after the poll moves a review there, until the
// fallback hits a limit of its own.
func TestANewReviewRunsUnderTheAccountTheRotationMovedTo(t *testing.T) {
	r := refusedForUsage(t)
	r.poll(t)
	r.svc.Now = func() time.Time { return r.resets.Add(time.Hour) }

	rec := prepareWith(t, r.svc, pr.Ref{Org: "haacked", Repo: "docket", Number: 8}, "claude", review.IntentReview)

	if rec.ClaudeConfigDir != r.fallback {
		t.Errorf("account = %q, want the fallback %q, which has not hit a limit", rec.ClaudeConfigDir, r.fallback)
	}
}

func TestAPollMovesAReviewToDidNotStartWhenNoAccountHasUsageLeft(t *testing.T) {
	r := refusedForUsage(t)
	if err := account.Append(r.svc.Paths.Limits, account.Limit{Account: r.fallback, At: r.svc.now(), Until: r.resets.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	got, _ := r.poll(t)

	if got.State != review.StateNotStarted || !strings.Contains(got.Err, "no claude account has usage left") {
		t.Errorf("record is %q with %q, want it under Did not start with the reason", got.State, got.Err)
	}
	if stored := storedByID(t, r.svc, r.rec.ID); stored.State != review.StateNotStarted || stored.BGID != "" {
		t.Errorf("stored %q with session %q, want not started with the stopped session dropped", stored.State, stored.BGID)
	}
	if n := len(claudeCalls(r.runner.Calls, "--bg")); n != 1 {
		t.Errorf("%d starts, want only the one claude refused", n)
	}
	if len(claudeCalls(r.runner.Calls, "stop")) != 1 {
		t.Errorf("calls = %v, want the refused session stopped", r.runner.Lines())
	}
}

// review-code can post the draft and still have a later turn refused. The
// draft is the review, so nothing starts again.
func TestAPollKeepsADraftTheSessionPostedBeforeItsUsageRanOut(t *testing.T) {
	r := refusedForUsage(t)
	r.ghc.reviews = []review.GHReview{myPending()}

	got, _ := r.poll(t)

	if got.State != review.StateDrafted || got.ClaudeConfigDir != r.primary {
		t.Errorf("record is %q under %q, want drafted where it ran", got.State, got.ClaudeConfigDir)
	}
	if len(claudeCalls(r.runner.Calls, "stop")) != 0 || len(launches(r.runner, r.fallback)) != 0 {
		t.Errorf("calls = %v, want nothing stopped or started", r.runner.Lines())
	}
}

// A session that ends its turn for any other reason is waiting on the user.
func TestAPollLeavesASessionThatEndedItsTurnNormally(t *testing.T) {
	r := refusedForUsage(t)
	writeTranscript(t, r.svc.Cfg.ClaudeProjectsDir(r.primary), bgSession,
		refusedTurn(r.svc.now(), r.resets),
		`{"type":"user","isSidechain":false,"message":{"role":"user","content":"continue"}}`,
		`{"type":"assistant","isSidechain":false,"message":{"role":"assistant","content":[{"type":"text","text":"Submit the draft?"}]}}`)

	got, _ := r.poll(t)

	if got.State != review.StateReviewing || got.BGID != r.rec.BGID {
		t.Errorf("record is %q with session %q, want the session left running", got.State, got.BGID)
	}
	if n := len(claudeCalls(r.runner.Calls, "stop")); n != 0 {
		t.Errorf("%d stops, want none", n)
	}
	if _, err := os.Stat(r.svc.Paths.Limits); !os.IsNotExist(err) {
		t.Errorf("limits log stat = %v, want no limit recorded", err)
	}
}

// docket asked about existing notes before the first launch, so notes there now
// came from the refused session. A background session cannot answer
// review-code's question about them.
func TestAReviewStartedAgainOverwritesNotesTheRefusedSessionWrote(t *testing.T) {
	r := refusedForUsage(t)
	notesFor(t, r.svc, unlisted)

	r.poll(t)

	starts := launches(r.runner, r.fallback)
	if len(starts) != 1 || !strings.Contains(starts[0].String(), "--overwrite") {
		t.Errorf("starts under the fallback = %v, want one that overwrites the notes", starts)
	}
}

// With every account out of usage, the review starts again later through enter
// or start_review. That start has to overwrite the notes too.
func TestAReviewWaitingForUsageKeepsTheChoiceToOverwrite(t *testing.T) {
	r := refusedForUsage(t)
	notesFor(t, r.svc, unlisted)
	r.svc.Cfg.ClaudeFallbackConfigDirs = nil

	got, _ := r.poll(t)

	if got.State != review.StateNotStarted || storedByID(t, r.svc, r.rec.ID).Intent != review.IntentOverwrite {
		t.Errorf("record is %q with intent %q, want it waiting with the intent to overwrite", got.State, storedByID(t, r.svc, r.rec.ID).Intent)
	}
}

// claude keeps a conversation under the account that ran it. A record that
// moves cannot resume its question-and-answer session under the new account.
func TestARecordThatMovesAccountDropsItsAskSession(t *testing.T) {
	r := refusedForUsage(t)
	if err := account.Append(r.svc.Paths.Limits, account.Limit{Account: r.primary, At: r.svc.now(), Until: r.resets}); err != nil {
		t.Fatal(err)
	}
	rec := storedByID(t, r.svc, r.rec.ID)
	rec.State = review.StateDrafted
	rec.AskSessionID = "a5c0ffee-0000-4000-8000-000000000003"
	rec.AskStartedAt = r.svc.now()

	again, err := r.svc.Rereview(context.Background(), rec, review.IntentAppend, review.ModeBackground)
	if err != nil {
		t.Fatalf("Rereview: %v", err)
	}

	if again.AskSessionID != "" || !again.AskStartedAt.IsZero() {
		t.Errorf("ask session %q from %v, want none under the new account", again.AskSessionID, again.AskStartedAt)
	}
}

// Two docket instances poll the same index. Once one has started the review
// again, the other must not start a second session.
func TestARefusalAnotherInstanceAlreadyHandledIsLeftAlone(t *testing.T) {
	r := refusedForUsage(t)
	moved := storedByID(t, r.svc, r.rec.ID)
	moved.BGID = "cccccccc"
	moved.ClaudeConfigDir = r.fallback
	if err := r.svc.append(moved); err != nil {
		t.Fatal(err)
	}
	stale := r.rec
	stale.SessionID = bgSession

	got, err := r.svc.startElsewhere(context.Background(), stale, engine.UsageLimit{At: r.svc.now(), Resets: r.resets})
	if err != nil {
		t.Fatalf("startElsewhere: %v", err)
	}

	if got.BGID != "cccccccc" {
		t.Errorf("returned session %q, want the other instance's record", got.BGID)
	}
	if len(claudeCalls(r.runner.Calls, "stop")) != 0 || len(launches(r.runner, r.fallback)) != 0 {
		t.Errorf("calls = %v, want nothing stopped or started", r.runner.Lines())
	}
}

// A re-review starts a new session, so it leaves an account that is over its
// limit. The old session still stops under its own account.
func TestRereviewMovesOffAnAccountOverItsLimit(t *testing.T) {
	r := refusedForUsage(t)
	if err := account.Append(r.svc.Paths.Limits, account.Limit{Account: r.primary, At: r.svc.now(), Until: r.resets}); err != nil {
		t.Fatal(err)
	}
	rec := storedByID(t, r.svc, r.rec.ID)
	rec.State = review.StateDrafted

	again, err := r.svc.Rereview(context.Background(), rec, review.IntentAppend, review.ModeBackground)
	if err != nil {
		t.Fatalf("Rereview: %v", err)
	}

	if again.ClaudeConfigDir != r.fallback {
		t.Errorf("account = %q, want the fallback", again.ClaudeConfigDir)
	}
	stops := claudeCalls(r.runner.Calls, "stop")
	if len(stops) != 1 || accountOf(stops[0]) != r.primary {
		t.Errorf("stops = %v, want the old session stopped under its own account", r.runner.Lines())
	}
}

func TestRestartMovesOffAnAccountOverItsLimit(t *testing.T) {
	r := refusedForUsage(t)
	if err := account.Append(r.svc.Paths.Limits, account.Limit{Account: r.primary, At: r.svc.now(), Until: r.resets}); err != nil {
		t.Fatal(err)
	}
	rec := storedByID(t, r.svc, r.rec.ID)
	rec.State = review.StateNotStarted
	rec.BGID = ""

	started, err := r.svc.Restart(context.Background(), rec)
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}

	if started.ClaudeConfigDir != r.fallback || len(launches(r.runner, r.fallback)) != 1 {
		t.Errorf("started under %q, calls = %v, want one start under the fallback", started.ClaudeConfigDir, r.runner.Lines())
	}
}

// The user saw claude refuse an interactive session. docket records the limit
// so the next review moves to the fallback.
func TestAnInteractiveSessionRefusedForUsageMovesTheNextReview(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	primary, fallback := t.TempDir(), t.TempDir()
	svc.Cfg.ClaudeConfigDir = primary
	svc.Cfg.ClaudeFallbackConfigDirs = []string{fallback}
	rec := prepareWith(t, svc, unlisted, "claude", review.IntentReview)
	writeTranscript(t, svc.Cfg.ClaudeProjectsDir(primary), rec.SessionID, refusedTurn(svc.now(), svc.now().Add(time.Hour)))

	exited, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}

	if !strings.Contains(exited.Err, "over its usage limit") {
		t.Errorf("error = %q, want it to say the account ran out of usage", exited.Err)
	}
	next := prepareWith(t, svc, pr.Ref{Org: "haacked", Repo: "docket", Number: 8}, "claude", review.IntentReview)
	if next.ClaudeConfigDir != fallback {
		t.Errorf("next review runs under %q, want the fallback %q", next.ClaudeConfigDir, fallback)
	}
}

// claude may not say when a limit resets. The account still counts as limited
// for a while, or the rotation would hand the next review straight back to it.
func TestALimitWithNoResetTimeStillCounts(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	if err := svc.recordLimit(review.Record{ClaudeConfigDir: "/a"}, engine.UsageLimit{At: svc.now()}); err != nil {
		t.Fatal(err)
	}

	limits, err := account.Load(svc.Paths.Limits)
	if err != nil {
		t.Fatal(err)
	}
	if len(limits) != 1 || !account.Limited("/a", account.Login{}, limits, svc.now().Add(unknownReset/2)) {
		t.Errorf("limits = %+v, want /a limited for a while after the refusal", limits)
	}
}

// docket mcp's start_review and the dashboard's enter start a Did not start row
// again. Showing the row there while the poll starts it would let either one
// launch a second session that no record names.
func TestAReviewStartedAgainNeverShowsAsNotStarted(t *testing.T) {
	r := refusedForUsage(t)
	notStarted, _ := review.EventForState(review.StateNotStarted)

	r.poll(t)

	events, err := r.svc.Store.Events()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.ID == r.rec.ID && e.Type == notStarted {
			t.Fatalf("the index recorded %s as %s while the poll started it again", r.rec.Ref, e.Type)
		}
	}
}

// A refused session that will not stop keeps the record pointing at it. The
// next poll tries again rather than leaving a session no record names.
func TestAStopThatFailsLeavesTheRefusedSessionForTheNextPoll(t *testing.T) {
	r := refusedForUsage(t)
	r.runner.Errs = map[string]error{underAccount(r.primary, "stop"): errors.New("claude exited 1")}

	got, _ := r.poll(t)

	if got.State != review.StateReviewing || got.BGID != r.rec.BGID || got.Err == "" {
		t.Errorf("record is %q with session %q and error %q, want the refused session kept with the reason", got.State, got.BGID, got.Err)
	}
	if stored := storedByID(t, r.svc, r.rec.ID); stored.BGID != r.rec.BGID {
		t.Errorf("stored session %q, want the refused session %q", stored.BGID, r.rec.BGID)
	}
	if n := len(launches(r.runner, r.fallback)); n != 0 {
		t.Errorf("%d starts under the fallback, want none while the refused session runs", n)
	}

	delete(r.runner.Errs, underAccount(r.primary, "stop"))
	got, _ = r.poll(t)

	if got.BGID != "bbbbbbbb" || got.ClaudeConfigDir != r.fallback {
		t.Errorf("second poll left session %q under %q, want the review started again under the fallback", got.BGID, got.ClaudeConfigDir)
	}
}

// docket rotates claude accounts only. A codex record keeps its account, and
// with it the question-and-answer session codex can still resume.
func TestAFreshStartLeavesACodexRecordWhereItIs(t *testing.T) {
	r := refusedForUsage(t)
	if err := account.Append(r.svc.Paths.Limits, account.Limit{Account: r.primary, At: r.svc.now(), Until: r.resets}); err != nil {
		t.Fatal(err)
	}
	rec := review.Record{Engine: "codex", ClaudeConfigDir: r.primary, AskSessionID: "codex-thread"}

	got, _, err := r.svc.freshAccount(rec)
	if err != nil {
		t.Fatalf("freshAccount: %v", err)
	}

	if got.ClaudeConfigDir != r.primary || got.AskSessionID != "codex-thread" {
		t.Errorf("record moved to %q with ask session %q, want it left alone", got.ClaudeConfigDir, got.AskSessionID)
	}
}

// A launch writes over the record's session id. claude would then hold the old
// session with no record naming it.
func TestStartBackgroundRefusesARecordThatStillNamesASession(t *testing.T) {
	r := refusedForUsage(t)
	before := len(claudeCalls(r.runner.Calls, "--bg"))

	if _, err := r.svc.StartBackground(context.Background(), storedByID(t, r.svc, r.rec.ID)); err == nil {
		t.Fatal("StartBackground launched over a record that still names a session")
	}

	if n := len(claudeCalls(r.runner.Calls, "--bg")) - before; n != 0 {
		t.Errorf("%d launches, want none", n)
	}
}

// The poll reads the same refusal again after a stop that fails, and every exit
// from a refused interactive session reads it again too.
func TestTheSameRefusalIsRecordedOnce(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	limit := engine.UsageLimit{At: svc.now(), Resets: svc.now().Add(time.Hour)}

	for range 2 {
		if err := svc.recordLimit(review.Record{ClaudeConfigDir: "/a"}, limit); err != nil {
			t.Fatal(err)
		}
	}

	limits, err := account.Load(svc.Paths.Limits)
	if err != nil {
		t.Fatal(err)
	}
	if len(limits) != 1 {
		t.Errorf("limits = %+v, want the refusal once", limits)
	}
}

// docket rotates claude accounts only, so a codex review never reads the limits
// log and cannot fail on it.
func TestACodexReviewDoesNotReadTheLimitsLog(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	// A directory in the log's place fails every read of it.
	svc.Paths.Limits = t.TempDir()

	if _, _, err := svc.Prepare(context.Background(), unlisted, "codex", review.ModeInteractive, review.IntentReview); err != nil {
		t.Errorf("Prepare(codex): %v", err)
	}
	if _, _, err := svc.Prepare(context.Background(), pr.Ref{Org: "haacked", Repo: "docket", Number: 8}, "claude", review.ModeInteractive, review.IntentReview); err == nil {
		t.Error("Prepare(claude) ignored a limits log it could not read")
	}
}

var (
	// teamSeat is a login the user signs two config dirs in to.
	teamSeat = account.Login{AccountUUID: "4f6f73cc-a2c3-4aef-adfd-615b0d6ede86", OrganizationUUID: "b94d544b-7e01-43e9-9fa2-0f99539bc5d1"}
	// ownOrg is the same person's account in another organization.
	ownOrg = account.Login{AccountUUID: teamSeat.AccountUUID, OrganizationUUID: "2bb69dd2-45b3-40ef-80fe-d40827cc4cd5"}
)

// writeLogin writes login where claude keeps it, in the .claude.json at path.
func writeLogin(t *testing.T, path string, login account.Login) {
	t.Helper()
	body := fmt.Sprintf(`{"oauthAccount":{"accountUuid":%q,"organizationUuid":%q}}`, login.AccountUUID, login.OrganizationUUID)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// twins is a service whose rotation runs primary, then twin, then other.
// primary and twin are signed in to one login, and other to another.
func twins(t *testing.T) (svc *Service, primary, twin, other string) {
	t.Helper()
	svc, _ = newService(t, &fakeGH{login: "haacked", info: prInfo()}, newFakeGit())
	primary, twin, other = t.TempDir(), t.TempDir(), t.TempDir()
	writeLogin(t, filepath.Join(primary, ".claude.json"), teamSeat)
	writeLogin(t, filepath.Join(twin, ".claude.json"), teamSeat)
	writeLogin(t, filepath.Join(other, ".claude.json"), ownOrg)
	svc.Cfg.ClaudeConfigDir = primary
	svc.Cfg.ClaudeFallbackConfigDirs = []string{twin, other}
	return svc, primary, twin, other
}

// The user can /login a dir to another login later. The log keeps the login
// the dir held when claude refused it.
func TestALimitRecordsTheLoginItsDirWasSignedInTo(t *testing.T) {
	svc, primary, _, _ := twins(t)

	if err := svc.recordLimit(review.Record{ClaudeConfigDir: primary}, engine.UsageLimit{At: svc.now(), Resets: svc.now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	limits, err := account.Load(svc.Paths.Limits)
	if err != nil {
		t.Fatal(err)
	}
	if len(limits) != 1 || limits[0].Login != teamSeat {
		t.Errorf("limits = %+v, want one on the team seat's login", limits)
	}
}

// claude refuses every dir signed in to a login that is over its limit. The
// next review skips them all.
func TestANewReviewSkipsEveryDirSignedInToALimitedLogin(t *testing.T) {
	svc, primary, _, other := twins(t)
	if err := svc.recordLimit(review.Record{ClaudeConfigDir: primary}, engine.UsageLimit{At: svc.now(), Resets: svc.now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	rec := prepareWith(t, svc, unlisted, "claude", review.IntentReview)

	if rec.ClaudeConfigDir != other {
		t.Errorf("account = %q, want %q, the one dir on another login", rec.ClaudeConfigDir, other)
	}
}

// A record under twin moves although claude refused primary, because both are
// signed in to the same login.
func TestAFreshStartMovesOffADirWhoseLoginIsLimitedUnderAnotherDir(t *testing.T) {
	svc, primary, twin, other := twins(t)
	if err := svc.recordLimit(review.Record{ClaudeConfigDir: primary}, engine.UsageLimit{At: svc.now(), Resets: svc.now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	got, choice, err := svc.freshAccount(review.Record{Engine: "claude", ClaudeConfigDir: twin})
	if err != nil {
		t.Fatalf("freshAccount: %v", err)
	}

	if got.ClaudeConfigDir != other || choice.Limited() {
		t.Errorf("record moved to %q with %+v, want %q under its limits", got.ClaudeConfigDir, choice, other)
	}
}

// claude keeps the default account's login in ~/.claude.json, not in
// ~/.claude.
func TestALimitOnTheDefaultAccountRecordsTheLoginInTheHomeDirectory(t *testing.T) {
	svc, _ := newService(t, &fakeGH{login: "haacked", info: prInfo()}, newFakeGit())
	home := t.TempDir()
	svc.Cfg.ClaudeDefaultDir = filepath.Join(home, ".claude")
	writeLogin(t, filepath.Join(home, ".claude.json"), teamSeat)

	if err := svc.recordLimit(review.Record{ClaudeConfigDir: ""}, engine.UsageLimit{At: svc.now(), Resets: svc.now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	limits, err := account.Load(svc.Paths.Limits)
	if err != nil {
		t.Fatal(err)
	}
	if len(limits) != 1 || limits[0].Login != teamSeat {
		t.Errorf("limits = %+v, want one on the team seat's login", limits)
	}
}
