package tui

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
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

const batchReport = "Started 2 of 3. PostHog/posthog#1 failed: HTTP 502"

// A failed reload must not clear a marker of work still in flight, which is
// what errMsg would do.
func TestAFailedReloadKeepsTheBusyMarkers(t *testing.T) {
	a := app()
	a.dash.Busy["rec-1"] = "submitting"

	model, _ := a.Update(configReloadedMsg{err: errors.New("config.toml: bad")})

	if busy := model.(App).dash.Busy; busy["rec-1"] != "submitting" {
		t.Errorf("busy = %v, want the submit's marker kept", busy)
	}
}

// A reload arrives on a timer with no keystroke behind it, and the status can
// hold a batch's failures, which show nowhere else. So the reload's error has a
// part of the status line to itself and leaves the status alone.
func TestAReloadShowsItsErrorBesideTheStatus(t *testing.T) {
	tests := []struct {
		name    string
		reloads []string
		want    string
	}{
		{name: "a reload that works", reloads: []string{""}, want: ""},
		{name: "a failed reload", reloads: []string{"config.toml: bad"}, want: "config.toml: bad"},
		{name: "a fix after a failure", reloads: []string{"config.toml: bad", ""}, want: ""},
		{name: "a second failure", reloads: []string{"config.toml: bad", "config.toml: worse"}, want: "config.toml: worse"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := app()
			a.status = batchReport

			for _, failure := range tc.reloads {
				var err error
				if failure != "" {
					err = errors.New(failure)
				}
				model, _ := a.Update(configReloadedMsg{err: err})
				a = model.(App)
			}

			if a.status != batchReport {
				t.Errorf("status = %q, want %q kept", a.status, batchReport)
			}
			if a.configErr != tc.want {
				t.Errorf("configErr = %q, want %q", a.configErr, tc.want)
			}
			if line := ansi.Strip(a.statusLine()); !strings.Contains(line, batchReport) || !strings.Contains(line, tc.want) {
				t.Errorf("status line = %q, want the batch's report and %q", line, tc.want)
			}
		})
	}
}
