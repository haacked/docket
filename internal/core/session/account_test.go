package session

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/review"
)

// automation and personal are the config directories of two Claude Code
// accounts. A record keeps the one in effect when it was created. The tests
// below then switch docket's configuration to the other.
const (
	automation = "/Users/me/.claude-automation"
	personal   = "/Users/me/.claude-personal"
)

// accountOf is the CLAUDE_CONFIG_DIR a spec gives claude when docket itself runs
// with another account's directory exported. It is "" when the spec removes the
// variable, which leaves claude on its default account.
func accountOf(spec exec.CommandSpec) string {
	for _, kv := range spec.Env([]string{"CLAUDE_CONFIG_DIR=/elsewhere", "HOME=/h"}) {
		if value, ok := strings.CutPrefix(kv, "CLAUDE_CONFIG_DIR="); ok {
			return value
		}
	}
	return ""
}

// claudeCalls returns the claude commands among calls whose first argument is
// verb.
func claudeCalls(calls []exec.CommandSpec, verb string) []exec.CommandSpec {
	var out []exec.CommandSpec
	for _, spec := range calls {
		if spec.Path == "claude" && len(spec.Args) > 0 && spec.Args[0] == verb {
			out = append(out, spec)
		}
	}
	return out
}

// startedUnderAutomation is a tier-2 background review started under
// automation, whose session claude lists in state. docket's configuration has
// switched to personal since.
func startedUnderAutomation(t *testing.T, ghc *fakeGH, state string) (*Service, *exec.Fake, review.Record) {
	t.Helper()
	svc, _ := newService(t, ghc, newFakeGit())
	svc.Cfg.ClaudeConfigDir = automation
	runner := bgRunner(bgListing("6d681a76", bgSession, state, true))
	rec := startedBackground(t, svc, runner)
	svc.Cfg.ClaudeConfigDir = personal
	return svc, runner, rec
}

// wantOneUnderAutomation checks that runner ran claude's verb once, under
// automation.
func wantOneUnderAutomation(t *testing.T, runner *exec.Fake, verb string) {
	t.Helper()
	calls := claudeCalls(runner.Calls, verb)
	if len(calls) != 1 || accountOf(calls[0]) != automation {
		t.Errorf("claude %s calls = %v, want one under %q", verb, runner.Lines(), automation)
	}
}

func TestPrepareStampsTheRecordWithTheConfiguredAccount(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	svc.Cfg.ClaudeConfigDir = automation

	rec, _, err := svc.Prepare(context.Background(), unlisted, "claude", review.ModeBackground, review.IntentReview, review.FixAuto)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	if rec.ClaudeConfigDir != automation {
		t.Errorf("account = %q, want %q", rec.ClaudeConfigDir, automation)
	}
	if got := storedByID(t, svc, rec.ID).ClaudeConfigDir; got != automation {
		t.Errorf("stored account = %q, want %q", got, automation)
	}
}

func TestExplainShowsTheAccountTheReviewWouldRunUnder(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	svc.Cfg.ClaudeConfigDir = automation

	_, spec, err := svc.Explain(context.Background(), unlisted, "claude", review.ModeBackground, review.IntentReview, review.FixAuto)
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}

	if got := accountOf(spec); got != automation {
		t.Errorf("%s runs under %q, want %q", spec, got, automation)
	}
}

// A review of a record that did not start yet still belongs to the account the
// record was created under.
func TestStartBackgroundRunsUnderTheRecordsAccount(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	runner := bgRunner(bgListing("6d681a76", bgSession, "working", true))
	svc.Runner = runner
	svc.Cfg.ClaudeConfigDir = automation
	rec, _, err := svc.Prepare(context.Background(), unlisted, "claude", review.ModeBackground, review.IntentReview, review.FixAuto)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	svc.Cfg.ClaudeConfigDir = personal

	if _, err := svc.StartBackground(context.Background(), rec); err != nil {
		t.Fatalf("StartBackground: %v", err)
	}

	wantOneUnderAutomation(t, runner, "--bg")
}

func TestLaunchingAnInteractiveReviewRunsUnderTheRecordsAccount(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	svc.Cfg.ClaudeConfigDir = automation
	rec := prepareWith(t, svc, unlisted, "claude", review.IntentReview)
	svc.Cfg.ClaudeConfigDir = personal

	_, spec, err := svc.LaunchSpec(context.Background(), rec)
	if err != nil {
		t.Fatalf("LaunchSpec: %v", err)
	}

	if got := accountOf(spec); got != automation {
		t.Errorf("%s runs under %q, want %q", spec, got, automation)
	}
}

// claude keeps the directories it trusts under its config directory. The
// prompt has to trust the directory for the account that launches there.
func TestTrustSpecRunsUnderTheRecordsAccount(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	svc.Cfg.ClaudeConfigDir = automation
	rec, _ := refusedLaunch(t, svc, untrustedStderr)
	svc.Cfg.ClaudeConfigDir = personal

	spec, err := svc.TrustSpec(storedByID(t, svc, rec.ID))
	if err != nil {
		t.Fatalf("TrustSpec: %v", err)
	}

	if got := accountOf(spec); got != automation {
		t.Errorf("%s runs under %q, want %q", spec, got, automation)
	}
}

// Only the account that started a session knows its short id.
func TestOpeningABackgroundSessionAsksAndAttachesUnderTheRecordsAccount(t *testing.T) {
	svc, runner, rec := startedUnderAutomation(t, &fakeGH{login: "haacked", info: prInfo()}, "done")

	spec, err := svc.OpenBackgroundSpec(context.Background(), rec)
	if err != nil {
		t.Fatalf("OpenBackgroundSpec: %v", err)
	}

	if got := accountOf(spec); got != automation {
		t.Errorf("%s runs under %q, want %q", spec, got, automation)
	}
	wantOneUnderAutomation(t, runner, "agents")
}

func TestAbandonStopsTheSessionUnderTheRecordsAccount(t *testing.T) {
	svc, runner, rec := startedUnderAutomation(t, &fakeGH{login: "haacked", info: prInfo()}, "working")

	if _, err := svc.Abandon(context.Background(), rec); err != nil {
		t.Fatalf("Abandon: %v", err)
	}

	wantOneUnderAutomation(t, runner, "stop")
}

func TestArchiveStopsTheSessionUnderTheRecordsAccount(t *testing.T) {
	submitted := start.Add(time.Minute)
	ghc := &fakeGH{login: "haacked", info: prInfo(), reviews: []review.GHReview{
		{ID: 55, State: "APPROVED", SubmittedAt: &submitted, User: review.GHUser{Login: "haacked"}},
	}}
	svc, runner, _ := startedUnderAutomation(t, ghc, "done")

	records, _, err := svc.PollBackground(context.Background())
	if err != nil {
		t.Fatalf("PollBackground: %v", err)
	}
	if records[0].State != review.StateArchived {
		t.Fatalf("state = %q, want the submitted review archived", records[0].State)
	}

	wantOneUnderAutomation(t, runner, "stop")
}

// A re-review replaces the record's session with a new one on the same pull
// request. It stops the old session and starts the new one under the same
// account.
func TestRereviewKeepsTheAccountTheRecordStartedUnder(t *testing.T) {
	svc, runner, rec := startedUnderAutomation(t, &fakeGH{login: "haacked", info: prInfo()}, "done")
	rec.State = review.StateDrafted

	again, err := svc.Rereview(context.Background(), rec, review.IntentAppend, review.ModeBackground)
	if err != nil {
		t.Fatalf("Rereview: %v", err)
	}
	if got := storedByID(t, svc, rec.ID).ClaudeConfigDir; got != automation {
		t.Errorf("stored account = %q, want %q", got, automation)
	}
	if _, err := svc.StartBackground(context.Background(), again); err != nil {
		t.Fatalf("StartBackground: %v", err)
	}

	wantOneUnderAutomation(t, runner, "stop")
	for _, launch := range claudeCalls(runner.Calls, "--bg") {
		if got := accountOf(launch); got != automation {
			t.Errorf("%s runs under %q, want %q", launch, got, automation)
		}
	}
}

// `claude agents` lists only the sessions of the account it runs under. One
// listing therefore cannot answer for records of two accounts. A was started under the
// default account and B under automation. The default account's listing names
// B's short id as a stopped session, so a poll that read B from it would close
// B.
func TestPollAsksEachAccountAboutItsOwnSessions(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	sequentialIDs(svc, "rec-a", "rec-b")
	runner := &exec.Fake{Results: map[string]exec.Result{
		"-u CLAUDE_CONFIG_DIR claude agents":                 {Stdout: bgListing("bbbbbbbb", "bbbbbbbb-0000-4000-8000-000000000002", "stopped", false)},
		"CLAUDE_CONFIG_DIR=" + automation + " claude agents": {Stdout: bgListing("bbbbbbbb", "bbbbbbbb-0000-4000-8000-000000000002", "working", true)},
	}}
	runner.Results["--bg"] = exec.Result{Stdout: "backgrounded · aaaaaaaa\n"}
	a, err := launchBackground(t, svc, runner, unlisted)
	if err != nil {
		t.Fatalf("StartBackground(A): %v", err)
	}
	svc.Cfg.ClaudeConfigDir = automation
	runner.Results["--bg"] = exec.Result{Stdout: "backgrounded · bbbbbbbb\n"}
	b, err := launchBackground(t, svc, runner, pr.Ref{Org: "haacked", Repo: "docket", Number: 8})
	if err != nil {
		t.Fatalf("StartBackground(B): %v", err)
	}
	before := len(runner.Calls)

	records, statuses, err := svc.PollBackground(context.Background())
	if err != nil {
		t.Fatalf("PollBackground: %v", err)
	}

	var accounts []string
	for _, listing := range claudeCalls(runner.Calls[before:], "agents") {
		accounts = append(accounts, accountOf(listing))
	}
	slices.Sort(accounts)
	if want := []string{"", automation}; !slices.Equal(accounts, want) {
		t.Errorf("listed under %q, want one listing for each of %q", accounts, want)
	}
	if got, _ := find(records, a.ID); got.State != review.StateUnreviewed {
		t.Errorf("A is %q, want its session read as finished from the default account's listing", got.State)
	}
	if got, _ := find(records, b.ID); got.State != review.StateReviewing {
		t.Errorf("B is %q, want its session read as running from its own account's listing", got.State)
	}
	if _, running := statuses[b.ID]; !running {
		t.Error("B reports no status")
	}
}

// A record whose launch never got its id written is found in the listing of
// the account it launched under.
func TestAPollRecoversALostSessionFromItsRecordsAccount(t *testing.T) {
	svc, runner, rec := startedUnderAutomation(t, &fakeGH{login: "haacked", info: prInfo()}, "working")
	lostLaunch(t, svc, rec)
	delete(runner.Results, "agents")
	runner.Results["CLAUDE_CONFIG_DIR="+automation+" claude agents"] = exec.Result{Stdout: lostListing(rec)}

	records, _, err := svc.PollBackground(context.Background())
	if err != nil {
		t.Fatalf("PollBackground: %v", err)
	}

	if records[0].BGID != "6d681a76" {
		t.Errorf("background id = %q, want the session adopted from %s's listing", records[0].BGID, automation)
	}
}

// A record's status file is under the jobs directory of the account it runs
// under, not of the account docket is configured with now.
func TestPollReadsProgressFromTheRecordsAccount(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	account := t.TempDir()
	svc.Cfg.ClaudeConfigDir = account
	writeJobState(t, svc.Cfg.ClaudeJobsDir(account), "6d681a76", `{"detail": "7 review agents dispatched", "tempo": "active"}`)
	rec := startedBackground(t, svc, bgRunner(bgListing("6d681a76", bgSession, "working", true)))
	svc.Cfg.ClaudeConfigDir = t.TempDir()

	_, statuses, err := svc.PollBackground(context.Background())
	if err != nil {
		t.Fatalf("PollBackground: %v", err)
	}

	if got := statuses[rec.ID].Progress.Detail; got != "7 review agents dispatched" {
		t.Errorf("detail = %q, want what the record's account says", got)
	}
}
