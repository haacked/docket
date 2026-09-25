package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/haacked/docket/internal/core/clone"
	"github.com/haacked/docket/internal/core/config"
	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/gh"
	"github.com/haacked/docket/internal/core/git"
	"github.com/haacked/docket/internal/core/index"
	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/core/session"
	"github.com/haacked/docket/internal/tui/msg"
)

// The handler's commands are not run here, because they reach the service, which
// app() leaves nil. That the handler returns work at all is what says it reloads
// the records the batch started.
func TestABatchReportsWhatStartedAndWhatFailed(t *testing.T) {
	next, cmd := app().Update(batchStartedMsg{
		started: 2,
		failed:  []string{"haacked/docket#9: haacked/docket#9 is already open; abandon it first"},
	})
	a := next.(App)

	if cmd == nil {
		t.Fatal("the batch did not reload the records it started")
	}
	view := a.View().Content
	for _, want := range []string{"Started 2", "1 failed", "haacked/docket#9 is already open"} {
		if !strings.Contains(view, want) {
			t.Errorf("the view does not say %q:\n%s", want, view)
		}
	}
}

func TestABatchWithNoFailuresReportsNoFailure(t *testing.T) {
	next, _ := app().Update(batchStartedMsg{started: 1})
	a := next.(App)

	if !strings.Contains(a.View().Content, "Started 1") {
		t.Errorf("the view does not say what started:\n%s", a.View().Content)
	}
	if strings.Contains(a.View().Content, "failed") {
		t.Errorf("the view reports a failure when none happened:\n%s", a.View().Content)
	}
}

// batchService is a real service over a temporary index, with gh and git
// answering through a fake runner. Explain reads the pull request from gh and
// finds no repos.conf entry, so it plans tier 1 and needs nothing from git.
func batchService(t *testing.T) (*session.Service, *exec.Fake) {
	t.Helper()
	paths, err := config.NewPaths(t.TempDir())
	if err != nil {
		t.Fatalf("config.NewPaths: %v", err)
	}
	if err := paths.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	runner := &exec.Fake{Results: map[string]exec.Result{
		"pr view": {Stdout: `{"number":7,"title":"Add a thing","headRefName":"haacked/a-thing","author":{"login":"someone"}}`},
	}}
	gitc := git.New(runner)
	return &session.Service{
		Cfg:    config.Config{ReviewCodeDir: t.TempDir(), DefaultEngine: "claude"},
		Paths:  paths,
		Store:  index.New(paths.Index, paths.Lock),
		GH:     gh.New(runner),
		Git:    gitc,
		Cloner: clone.New(gitc, paths),
		Runner: runner,
	}, runner
}

// cloningService is batchService set up to start background reviews under
// claude. No repos.conf entry makes each review a tier-2 clone, which checks
// that the clone landed on the head branch with files in it. claude reports an
// id for every start.
func cloningService(t *testing.T) (*session.Service, *exec.Fake) {
	t.Helper()
	onPath(t, "claude")
	svc, runner := batchService(t)
	svc.Cfg.GitHubUser = "haacked"
	runner.Results["branch --show-current"] = exec.Result{Stdout: "haacked/a-thing\n"}
	runner.Results["ls-files"] = exec.Result{Stdout: "README.md\n"}
	runner.Results["/reviews"] = exec.Result{Stdout: "[]"}
	runner.Results["--bg"] = exec.Result{Stdout: "backgrounded · 0a1b2c3d\n"}
	return svc, runner
}

// drain runs a command and every command a batch holds, and returns the
// messages they produced.
func drain(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	out := cmd()
	if batch, ok := out.(tea.BatchMsg); ok {
		var msgs []tea.Msg
		for _, c := range batch {
			msgs = append(msgs, drain(c)...)
		}
		return msgs
	}
	return []tea.Msg{out}
}

// check sends a batch the way the requests screen does and hands the root the
// result of its check. The root then either asks about the pull requests that
// already have a review or starts the batch.
func check(t *testing.T, a App, urls ...string) (App, tea.Cmd) {
	t.Helper()
	next, cmd := a.Update(msg.StartBatch{URLs: urls, Engine: "claude"})
	next, cmd = next.(App).Update(only[batchCheckedMsg](t, cmd))
	return next.(App), cmd
}

// only runs cmd and returns the one message it produced.
func only[T tea.Msg](t *testing.T, cmd tea.Cmd) T {
	t.Helper()
	msgs := drain(cmd)
	if len(msgs) != 1 {
		t.Fatalf("the command produced %d messages, want one", len(msgs))
	}
	got, ok := msgs[0].(T)
	if !ok {
		t.Fatalf("the command sent %#v, want a %T", msgs[0], got)
	}
	return got
}

func recordsByNumber(t *testing.T, svc *session.Service) map[int]review.Record {
	t.Helper()
	records, err := svc.Store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	byNumber := map[int]review.Record{}
	for _, rec := range records {
		byNumber[rec.Ref.Number] = rec
	}
	return byNumber
}

// A dry run reports what each review would run and records nothing. Prepare is
// what appends to the index, so an empty index is what says it never ran.
func TestADryRunBatchExplainsEachPullRequestWithoutPreparing(t *testing.T) {
	svc, runner := batchService(t)
	svc.Cfg.GitHubUser = "haacked"
	a := New(svc, config.Config{DefaultEngine: "claude"}, "", true)
	urls := []string{"https://github.com/haacked/docket/pull/7", "https://github.com/haacked/docket/pull/8"}

	_, cmd := check(t, a, urls...)

	var report strings.Builder
	for _, m := range drain(cmd) {
		switch m := m.(type) {
		case statusMsg:
			report.WriteString(m.text + "\n")
		case errMsg:
			t.Fatalf("the dry run failed: %v", m.err)
		}
	}
	if got := strings.Count(report.String(), "Would run"); got != len(urls) {
		t.Errorf("the report explains %d reviews, want %d:\n%s", got, len(urls), report.String())
	}
	records, err := svc.Store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("a dry run recorded %+v", records)
	}
	for _, line := range runner.Lines() {
		if strings.Contains(line, "claude") {
			t.Errorf("a dry run ran %s", line)
		}
	}
}

// onPath puts an executable of each name on PATH, because a batch checks the
// engine's binary before it prepares anything. The fake runner is what answers
// the commands, so the files only have to exist.
func onPath(t *testing.T, names ...string) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
}

// A pull request docket already has open fails the check. The batch reports it
// and still starts the pull request after it.
func TestABatchStartsThePullRequestsAfterOneThatFails(t *testing.T) {
	svc, _ := cloningService(t)
	open := pr.Ref{Org: "haacked", Repo: "docket", Number: 7}
	if _, err := startOne(context.Background(), svc, open, "claude", review.IntentReview); err != nil {
		t.Fatalf("starting the first review: %v", err)
	}

	a, cmd := check(t, New(svc, svc.Cfg, "", false), open.URL(), freshURL)
	got := only[batchStartedMsg](t, cmd)

	if a.screen != msg.Dashboard {
		t.Errorf("screen = %v, want the dashboard while the batch starts", a.screen)
	}
	if got.started != 1 {
		t.Errorf("started = %d, want the pull request after the failure", got.started)
	}
	if len(got.failed) != 1 || !strings.Contains(got.failed[0], "haacked/docket#7") {
		t.Errorf("failed = %q, want one line naming #7", got.failed)
	}
	if records := recordsByNumber(t, svc); len(records) != 2 {
		t.Errorf("records = %d, want #7 once and #8", len(records))
	}
}

// writeNotes leaves review-code notes for a pull request, which is what makes
// Existing report a review.
func writeNotes(t *testing.T, svc *session.Service, number int) {
	t.Helper()
	path := svc.Cfg.NotesPath("haacked", "docket", number)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# Review\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

const (
	reviewedURL = "https://github.com/haacked/docket/pull/7"
	freshURL    = "https://github.com/haacked/docket/pull/8"
)

// askingApp has notes for #7 and nothing for #8, and the root has checked a
// batch of both.
func askingApp(t *testing.T, dryRun bool) (App, *session.Service, *exec.Fake) {
	t.Helper()
	svc, runner := cloningService(t)
	writeNotes(t, svc, 7)
	a, cmd := check(t, New(svc, svc.Cfg, "", dryRun), reviewedURL, freshURL)
	if cmd != nil {
		t.Fatalf("the batch ran %#v before the user answered", drain(cmd))
	}
	return a, svc, runner
}

// Starting a pull request that already has a review without an answer would
// leave review-code asking whether to append or overwrite in a session nobody
// watches. The batch starts nothing until the user answers.
func TestABatchAsksBeforeStartingAPullRequestThatAlreadyHasAReview(t *testing.T) {
	a, svc, _ := askingApp(t, false)

	if a.screen != msg.Requests {
		t.Errorf("screen = %v, want the requests screen asking", a.screen)
	}
	if len(a.reqs.Existing) != 1 || a.reqs.Existing[0].Ref != "haacked/docket#7" {
		t.Errorf("asking about %+v, want #7 alone", a.reqs.Existing)
	}
	if a.reqs.Others != 1 {
		t.Errorf("others = %d, want #8", a.reqs.Others)
	}
	if view := a.View().Content; strings.Contains(view, "space mark") || strings.Contains(view, "enter start") {
		t.Errorf("the footer offers the list's keys while the question is on screen:\n%s", view)
	}
	if records := recordsByNumber(t, svc); len(records) != 0 {
		t.Errorf("records = %+v before the answer, want none", records)
	}
}

func TestAnAnsweredBatchStartsEachPullRequestWithItsIntent(t *testing.T) {
	a, svc, runner := askingApp(t, false)

	next, cmd := a.Update(msg.AnswerBatch{Intent: string(review.IntentAppend)})
	got := only[batchStartedMsg](t, cmd)

	if next.(App).screen != msg.Dashboard {
		t.Errorf("screen = %v, want the dashboard while the batch starts", next.(App).screen)
	}
	if got.started != 2 || len(got.failed) != 0 {
		t.Errorf("started = %d, failed = %q, want both started", got.started, got.failed)
	}
	records := recordsByNumber(t, svc)
	if records[7].Intent != review.IntentAppend || records[8].Intent != review.IntentReview {
		t.Errorf("intents = #7 %q, #8 %q, want append and review", records[7].Intent, records[8].Intent)
	}
	appended := slices.ContainsFunc(runner.Lines(), func(line string) bool {
		return strings.Contains(line, "--bg") && strings.Contains(line, reviewedURL+" --draft --append")
	})
	if !appended {
		t.Errorf("no background launch appended to #7's review:\n%s", strings.Join(runner.Lines(), "\n"))
	}
}

func TestSkippingStartsOnlyThePullRequestsWithNoReview(t *testing.T) {
	a, svc, _ := askingApp(t, false)

	_, cmd := a.Update(msg.AnswerBatch{})
	got := only[batchStartedMsg](t, cmd)

	if got.started != 1 {
		t.Errorf("started = %d, want #8 alone", got.started)
	}
	records := recordsByNumber(t, svc)
	if _, ok := records[8]; !ok || len(records) != 1 {
		t.Errorf("records = %+v, want #8 alone", records)
	}
}

// The check reads GitHub, which a dry run may do, so a dry run asks the same
// question and then explains the commands the answer would run.
func TestADryRunBatchAsksAndExplainsTheAnswer(t *testing.T) {
	a, svc, runner := askingApp(t, true)

	_, cmd := a.Update(msg.AnswerBatch{Intent: string(review.IntentOverwrite)})
	status := only[statusMsg](t, cmd)

	if strings.Count(status.text, "Would run") != 2 || !strings.Contains(status.text, "--overwrite") {
		t.Errorf("the dry run reported %q, want both commands with #7's --overwrite", status.text)
	}
	if records := recordsByNumber(t, svc); len(records) != 0 {
		t.Errorf("a dry run recorded %+v", records)
	}
	if bgLaunches(runner) != 0 {
		t.Error("a dry run launched a review")
	}
}

// openRecord starts a review of number, so a later check of it fails because
// docket already has it open.
func openRecord(t *testing.T, svc *session.Service, number int) pr.Ref {
	t.Helper()
	ref := pr.Ref{Org: "haacked", Repo: "docket", Number: number}
	if _, err := startOne(context.Background(), svc, ref, "claude", review.IntentReview); err != nil {
		t.Fatalf("starting #%d: %v", number, err)
	}
	return ref
}

// A check that fails for every pull request leaves nothing to start. The user
// stays on the list with the marks, so a retry needs no marking again.
func TestABatchWhoseChecksAllFailKeepsTheList(t *testing.T) {
	svc, _ := cloningService(t)
	open := openRecord(t, svc, 7)
	a := New(svc, svc.Cfg, "", false)
	a.screen = msg.Requests
	a.reqs.Marked = map[string]bool{open.URL(): true}
	a.reqs.Busy = true

	a, cmd := check(t, a, open.URL())

	if cmd != nil {
		t.Errorf("a batch with nothing to start ran %#v", drain(cmd))
	}
	if a.screen != msg.Requests || !a.reqs.Marked[open.URL()] || a.reqs.Busy {
		t.Errorf("screen = %v, marked = %v, busy = %v, want the list with its mark, ready for enter", a.screen, a.reqs.Marked, a.reqs.Busy)
	}
	if !strings.Contains(a.status, "haacked/docket#7") {
		t.Errorf("status = %q, want the failure", a.status)
	}
}

// The question replaces the list, so the status line is the one place that
// says a marked pull request failed its check. esc leaves it there.
func TestTheQuestionShowsTheChecksThatFailed(t *testing.T) {
	svc, _ := cloningService(t)
	writeNotes(t, svc, 7)
	open := openRecord(t, svc, 9)

	a, _ := check(t, New(svc, svc.Cfg, "", false), reviewedURL, freshURL, open.URL())
	if !a.reqs.Asking() || !strings.Contains(a.View().Content, "haacked/docket#9") {
		t.Errorf("the question does not show #9's failure:\n%s", a.View().Content)
	}

	_, cmd := a.Update(msg.AnswerBatch{Intent: string(review.IntentAppend)})
	if got := only[batchStartedMsg](t, cmd); got.started != 2 || len(got.failed) != 1 {
		t.Errorf("started = %d, failed = %q, want #7 and #8 started and #9 reported", got.started, got.failed)
	}
}

// A missing binary stops the batch before it reads GitHub or asks anything.
func TestABatchWithNoEngineBinaryChecksNothing(t *testing.T) {
	svc, runner := batchService(t)
	onPath(t)
	a := New(svc, svc.Cfg, "", false)
	a.screen = msg.Requests
	a.reqs.Marked = map[string]bool{freshURL: true}
	a.reqs.Busy = true

	a, cmd := check(t, a, freshURL)

	if cmd != nil || a.err == nil || !strings.Contains(a.err.Error(), "claude") {
		t.Errorf("err = %v, want claude named as missing", a.err)
	}
	if a.reqs.Busy || !a.reqs.Marked[freshURL] {
		t.Errorf("busy = %v, marked = %v, want the list ready for another enter", a.reqs.Busy, a.reqs.Marked)
	}
	if lines := runner.Lines(); len(lines) != 0 {
		t.Errorf("the batch ran %q with no engine to start", lines)
	}
}

// Only the check's own answer may clear the flag that stops a second enter. An
// unrelated failure, such as a search GitHub refused, would otherwise let the
// same batch be checked and started twice.
func TestAnUnrelatedErrorDuringTheCheckKeepsTheBatchBusy(t *testing.T) {
	a := app()
	a.reqs.Busy = true

	next, _ := a.Update(errMsg{err: errors.New("search refused")})

	if !next.(App).reqs.Busy {
		t.Error("an unrelated error let a second enter send the batch again")
	}
}
