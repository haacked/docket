package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/haacked/docket/internal/core/clone"
	"github.com/haacked/docket/internal/core/config"
	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/gh"
	"github.com/haacked/docket/internal/core/git"
	"github.com/haacked/docket/internal/core/index"
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

// A dry run reports what each review would run and records nothing. Prepare is
// what appends to the index, so an empty index is what says it never ran.
func TestADryRunBatchExplainsEachPullRequestWithoutPreparing(t *testing.T) {
	svc, runner := batchService(t)
	a := New(svc, config.Config{DefaultEngine: "claude"}, "", true)
	urls := []string{"https://github.com/haacked/docket/pull/7", "https://github.com/haacked/docket/pull/8"}

	_, cmd := a.Update(msg.StartBatch{URLs: urls, Engine: "claude"})

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
