package tui

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/haacked/docket/internal/core/config"
	"github.com/haacked/docket/internal/core/review"
)

func TestCheckConfigReportsAnEditOnceAndNothingAfter(t *testing.T) {
	svc, paths := serviceOverIndex(t)
	a := New(svc, svc.Cfg, "", false)
	if err := os.WriteFile(paths.Config, []byte("default_repo = \"PostHog/posthog\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok := a.checkConfig()().(configReloadedMsg)
	if !ok || got.err != nil {
		t.Fatalf("checkConfig() = %#v, want a reload that worked", got)
	}
	if again := a.checkConfig()(); again != nil {
		t.Errorf("the next checkConfig() = %#v, want nil for a file that has not changed", again)
	}
}

func TestCheckConfigReportsAFileThatDoesNotLoad(t *testing.T) {
	svc, paths := serviceOverIndex(t)
	a := New(svc, svc.Cfg, "", false)
	if err := os.WriteFile(paths.Config, []byte("claude_config_dir = \"relative\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok := a.checkConfig()().(configReloadedMsg)

	if !ok || got.err == nil {
		t.Fatalf("checkConfig() = %#v, want the load's error", got)
	}
}

func TestAReloadPointsTheNewReviewScreenAtTheNewRepoAndReadsTheAccount(t *testing.T) {
	svc, _ := serviceOverIndex(t)
	a := New(svc, svc.Cfg, "", false)
	svc.Cfg.DefaultRepo = "PostHog/posthog"

	model, cmd := a.Update(configReloadedMsg{})

	next := model.(App)
	if next.newrev.DefaultRepo != "PostHog/posthog" {
		t.Errorf("the new review screen's repo = %q, want the reloaded one", next.newrev.DefaultRepo)
	}
	if countCmds(cmd) != 1 {
		t.Errorf("the reload ran %d commands, want the account read", countCmds(cmd))
	}
}

// A failed reload must not clear a marker of work still in flight, which is
// what errMsg would do.
func TestAFailedReloadKeepsTheBusyMarkersAndSaysTheOldSettingsStay(t *testing.T) {
	a := app()
	a.dash.Busy["rec-1"] = "submitting"

	model, _ := a.Update(configReloadedMsg{err: errors.New("config.toml: default_run is \"sometimes\". docket keeps the settings it read before")})

	next := model.(App)
	if next.dash.Busy["rec-1"] != "submitting" {
		t.Errorf("busy = %v, want the submit's marker kept", next.dash.Busy)
	}
	if !strings.Contains(next.status, "sometimes") || !strings.Contains(next.status, "keeps the settings it read before") {
		t.Errorf("status = %q, want the error and that the old settings stay", next.status)
	}
}

// The status line can hold a batch's failures, which show nowhere else, and a
// reload arrives on a timer with no keystroke behind it.
func TestAReloadLeavesTheStatusLineAlone(t *testing.T) {
	svc, _ := serviceOverIndex(t)
	a := New(svc, svc.Cfg, "", false)
	a.status = "Started 2 of 3. PostHog/posthog#1 failed: HTTP 502"

	model, _ := a.Update(configReloadedMsg{})

	if got := model.(App).status; got != a.status {
		t.Errorf("status = %q, want %q kept", got, a.status)
	}
}

func TestAReloadAfterAFailedOneReplacesTheError(t *testing.T) {
	svc, _ := serviceOverIndex(t)
	a := New(svc, svc.Cfg, "", false)
	model, _ := a.Update(configReloadedMsg{err: errors.New("config.toml: bad")})

	model, _ = model.(App).Update(configReloadedMsg{})

	next := model.(App)
	if next.status != "Reloaded config.toml" {
		t.Errorf("status = %q, want the error replaced", next.status)
	}
	if next.configErr != "" {
		t.Errorf("configErr = %q after a reload that worked, want it cleared", next.configErr)
	}
}

const batchReport = "Started 2 of 3. PostHog/posthog#1 failed: HTTP 502"

func TestAFailedReloadAddsItsErrorAfterABatchsFailures(t *testing.T) {
	a := app()
	a.status = batchReport

	model, _ := a.Update(configReloadedMsg{err: errors.New("config.toml: bad")})

	if got, want := model.(App).status, batchReport+"\nconfig.toml: bad"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
}

func TestAReloadTakesOnlyItsOwnErrorOffABatchsFailures(t *testing.T) {
	svc, _ := serviceOverIndex(t)
	a := New(svc, svc.Cfg, "", false)
	a.status = batchReport
	model, _ := a.Update(configReloadedMsg{err: errors.New("config.toml: bad")})

	model, _ = model.(App).Update(configReloadedMsg{})

	if got := model.(App).status; got != batchReport {
		t.Errorf("status = %q, want %q", got, batchReport)
	}
}

func TestASecondFailedReloadReplacesTheFirstError(t *testing.T) {
	a := app()
	a.status = batchReport
	model, _ := a.Update(configReloadedMsg{err: errors.New("config.toml: bad")})

	model, _ = model.(App).Update(configReloadedMsg{err: errors.New("config.toml: worse")})

	if got, want := model.(App).status, batchReport+"\nconfig.toml: worse"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
}

// A batch can report between the failed reload and the one that works, and its
// failures show nowhere else.
func TestAReloadLeavesAStatusWrittenAfterItsError(t *testing.T) {
	svc, _ := serviceOverIndex(t)
	a := New(svc, svc.Cfg, "", false)
	model, _ := a.Update(configReloadedMsg{err: errors.New("config.toml: bad")})
	failed := model.(App)
	failed.status = batchReport

	model, _ = failed.Update(configReloadedMsg{})

	next := model.(App)
	if next.status != batchReport {
		t.Errorf("status = %q, want %q kept", next.status, batchReport)
	}
	if next.configErr != "" {
		t.Errorf("configErr = %q after a reload that worked, want it cleared", next.configErr)
	}
}

// The new review screen accepts a bare number against the reloaded
// default_repo, so prepare has to parse it against the same one.
func TestPrepareParsesABareNumberAgainstTheReloadedRepo(t *testing.T) {
	svc, _ := batchService(t)
	svc.Cfg.GitHubUser = "haacked"
	a := New(svc, config.Config{DefaultEngine: "claude"}, "", true)
	svc.Cfg.DefaultRepo = "haacked/docket"

	got := a.prepare("7", "claude", review.ModeBackground, review.IntentReview)()

	status, ok := got.(statusMsg)
	if !ok || !strings.Contains(status.text, "Would run") {
		t.Errorf("prepare = %#v, want the dry run's command for haacked/docket#7", got)
	}
}
