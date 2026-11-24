package session

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// writeConfig replaces config.toml and moves its mtime forward by a second
// each time. A reorder of the accounts keeps the file's size, so a test that
// relied on the filesystem's clock could miss it.
func writeConfig(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	bumpMtime(t, path)
}

func bumpMtime(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	later := info.ModTime().Add(time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
}

func accountsTOML(personal, max, automation string) string {
	return "github_user = \"haacked\"\n" +
		"claude_config_dir = \"" + personal + "\"\n" +
		"claude_fallback_config_dirs = [\"" + max + "\", \"" + automation + "\"]\n"
}

// mustReload calls ReloadConfig and fails the test unless it read the file.
func mustReload(t *testing.T, svc *Service) {
	t.Helper()
	if loaded, err := svc.ReloadConfig(); err != nil || !loaded {
		t.Fatalf("ReloadConfig = %v, %v, want the file read", loaded, err)
	}
}

// serviceWithAccounts is a service that has read a config.toml naming the
// accounts /opt/a, /opt/b, and /opt/c in that order.
func serviceWithAccounts(t *testing.T) (*Service, string) {
	t.Helper()
	svc, paths := newService(t, &fakeGH{login: "haacked"}, newFakeGit())
	writeConfig(t, paths.Config, accountsTOML("/opt/a", "/opt/b", "/opt/c"))
	mustReload(t, svc)
	return svc, paths.Config
}

func TestReloadConfigTakesANewRotationOrder(t *testing.T) {
	svc, paths := newService(t, &fakeGH{login: "haacked"}, newFakeGit())
	root := t.TempDir()
	personal, max, automation := filepath.Join(root, "a"), filepath.Join(root, "b"), filepath.Join(root, "c")
	writeConfig(t, paths.Config, accountsTOML(personal, max, automation))
	mustReload(t, svc)
	if choice, err := svc.NextAccount(context.Background()); err != nil || choice.Account != personal {
		t.Fatalf("NextAccount = %+v, %v, want %s before the edit", choice, err, personal)
	}

	writeConfig(t, paths.Config, accountsTOML(automation, max, personal))
	mustReload(t, svc)

	choice, err := svc.NextAccount(context.Background())
	if err != nil || choice.Account != automation {
		t.Errorf("NextAccount = %+v, %v, want %s after the edit", choice, err, automation)
	}
	if got, want := svc.Config().ClaudeAccounts(), []string{automation, max, personal}; !slices.Equal(got, want) {
		t.Errorf("accounts = %v, want %v", got, want)
	}
}

func TestReloadConfigReadsAnUnchangedFileOnce(t *testing.T) {
	svc, _ := serviceWithAccounts(t)

	loaded, err := svc.ReloadConfig()

	if err != nil || loaded {
		t.Errorf("ReloadConfig = %v, %v, want nothing read for a file whose mark is the same", loaded, err)
	}
}

func TestReloadConfigKeepsTheSettingsDocketReadsOnlyAtStartup(t *testing.T) {
	svc, paths := newService(t, &fakeGH{login: "haacked"}, newFakeGit())
	before := svc.Config()
	writeConfig(t, paths.Config, `review_code_dir = "/elsewhere"
codex_sessions_dir = "/elsewhere/codex"
default_engine = "codex"
default_run = "terminal"
default_repo = "PostHog/posthog"
github_user = "someone-else"
teams = ["PostHog/team-feature-flags"]
`)

	mustReload(t, svc)

	after := svc.Config()
	if after.ReviewCodeDir != before.ReviewCodeDir || after.CodexSessionsDir != before.CodexSessionsDir {
		t.Errorf("review-code dirs = %q, %q, want %q, %q kept", after.ReviewCodeDir, after.CodexSessionsDir, before.ReviewCodeDir, before.CodexSessionsDir)
	}
	if after.DefaultEngine != before.DefaultEngine || after.DefaultRun != before.DefaultRun {
		t.Errorf("engine and run = %q, %q, want %q, %q kept", after.DefaultEngine, after.DefaultRun, before.DefaultEngine, before.DefaultRun)
	}
	if after.DefaultRepo != "PostHog/posthog" {
		t.Errorf("default_repo = %q, want the file's", after.DefaultRepo)
	}
	if after.GitHubUser != "someone-else" || !slices.Equal(after.Teams, []string{"PostHog/team-feature-flags"}) {
		t.Errorf("github_user, teams = %q, %v, want the file's", after.GitHubUser, after.Teams)
	}
}

// The index tick reloads on its own goroutine while an account read runs on
// another. Run under -race, this test fails when either reads or writes Cfg
// without the lock.
func TestNextAccountDuringReloadConfig(t *testing.T) {
	svc, path := serviceWithAccounts(t)
	writeConfig(t, path, accountsTOML("/opt/c", "/opt/b", "/opt/a"))

	var wg sync.WaitGroup
	wg.Go(func() {
		if _, err := svc.ReloadConfig(); err != nil {
			t.Errorf("ReloadConfig: %v", err)
		}
	})
	wg.Go(func() {
		if _, err := svc.NextAccount(context.Background()); err != nil {
			t.Errorf("NextAccount: %v", err)
		}
	})
	wg.Wait()
}

func TestReloadConfigKeepsTheOldSettingsWhenTheFileDoesNotLoad(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "a parse error", body: "teams = [\n"},
		{name: "a relative account", body: "claude_config_dir = \"relative/dir\"\n"},
		{name: "an unknown default_run", body: "default_run = \"sometimes\"\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, path := serviceWithAccounts(t)
			before := svc.Config().ClaudeAccounts()

			writeConfig(t, path, tc.body)
			loaded, err := svc.ReloadConfig()

			if err == nil || loaded {
				t.Fatalf("ReloadConfig = %v, %v, want an error and nothing read", loaded, err)
			}
			if !strings.Contains(err.Error(), "keeps the settings it read before") {
				t.Errorf("error = %q, want it to say the old settings stay", err)
			}
			if got := svc.Config().ClaudeAccounts(); !slices.Equal(got, before) {
				t.Errorf("accounts = %v, want %v kept", got, before)
			}
			if loaded, err := svc.ReloadConfig(); err != nil || loaded {
				t.Errorf("the next ReloadConfig = %v, %v, want the error reported once", loaded, err)
			}
			// The fix restores the same keys. It still reads as a reload, so
			// the caller can replace the error it showed.
			writeConfig(t, path, accountsTOML("/opt/a", "/opt/b", "/opt/c"))
			mustReload(t, svc)
		})
	}
}

func TestReloadConfigAfterSavingTheTeamsKeepsThem(t *testing.T) {
	svc, path := serviceWithAccounts(t)
	teams := []string{"PostHog/team-feature-flags"}

	if err := svc.SaveTeams(teams); err != nil {
		t.Fatalf("SaveTeams: %v", err)
	}
	bumpMtime(t, path)
	mustReload(t, svc)

	if got := svc.Config().Teams; !slices.Equal(got, teams) {
		t.Errorf("teams = %v, want %v", got, teams)
	}
}

// A hand edit that lands before the save survives both, because SaveKey
// rewrites only its own key.
func TestReloadConfigAfterAHandEditAndASaveKeepsBoth(t *testing.T) {
	svc, path := serviceWithAccounts(t)
	writeConfig(t, path, accountsTOML("/opt/c", "/opt/b", "/opt/a"))
	teams := []string{"PostHog/team-feature-flags"}

	if err := svc.SaveTeams(teams); err != nil {
		t.Fatalf("SaveTeams: %v", err)
	}
	bumpMtime(t, path)
	mustReload(t, svc)

	cfg := svc.Config()
	if !slices.Equal(cfg.Teams, teams) || cfg.ClaudeConfigDir != "/opt/c" {
		t.Errorf("teams = %v and account = %q, want %v and /opt/c", cfg.Teams, cfg.ClaudeConfigDir, teams)
	}
}

func TestReloadConfigLeavesTheSettingsWhileTheFileIsEmptyOrMissing(t *testing.T) {
	tests := []struct {
		name  string
		empty func(t *testing.T, path string)
	}{
		{name: "truncated", empty: func(t *testing.T, path string) { writeConfig(t, path, "") }},
		{name: "removed", empty: func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, path := serviceWithAccounts(t)

			tc.empty(t, path)
			loaded, err := svc.ReloadConfig()

			if err != nil || loaded {
				t.Errorf("ReloadConfig = %v, %v, want nothing read", loaded, err)
			}
			if got := svc.Config().ClaudeAccounts(); !slices.Equal(got, []string{"/opt/a", "/opt/b", "/opt/c"}) {
				t.Errorf("accounts = %v, want the old rotation kept", got)
			}

			writeConfig(t, path, accountsTOML("/opt/c", "/opt/b", "/opt/a"))
			mustReload(t, svc)
		})
	}
}

func TestReloadConfigWritesNothing(t *testing.T) {
	svc, paths := newService(t, &fakeGH{login: "haacked"}, newFakeGit())
	body := accountsTOML("/opt/a", "/opt/b", "/opt/c")
	writeConfig(t, paths.Config, body)

	mustReload(t, svc)

	data, err := os.ReadFile(paths.Config)
	if err != nil || string(data) != body {
		t.Errorf("config.toml = %q, %v, want it untouched", data, err)
	}
	if _, err := os.Stat(paths.Limits); !os.IsNotExist(err) {
		t.Errorf("limits.jsonl exists after a reload: %v", err)
	}
}

func TestReloadConfigReportsAStatFailureOnceAndItsRecovery(t *testing.T) {
	svc, path := serviceWithAccounts(t)
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	loaded, err := svc.ReloadConfig()
	if err == nil || loaded || !strings.Contains(err.Error(), "keeps the settings it read before") {
		t.Fatalf("ReloadConfig = %v, %v, want the stat's failure", loaded, err)
	}
	if loaded, err := svc.ReloadConfig(); err != nil || loaded {
		t.Errorf("the next ReloadConfig = %v, %v, want the failure reported once", loaded, err)
	}

	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustReload(t, svc)
	if got := svc.Config().ClaudeAccounts(); !slices.Equal(got, []string{"/opt/a", "/opt/b", "/opt/c"}) {
		t.Errorf("accounts = %v, want the file's", got)
	}
}

// A stat failure stays on show until docket reads the file again, even when
// the file was missing for a moment after the failure passed.
func TestReloadConfigClearsAStatFailureOnceTheFileIsBack(t *testing.T) {
	svc, path := serviceWithAccounts(t)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if _, err := svc.ReloadConfig(); err == nil {
		t.Fatal("ReloadConfig returned no error for a stat that failed")
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	aside := path + ".aside"
	if err := os.Rename(path, aside); err != nil {
		t.Fatal(err)
	}
	if loaded, err := svc.ReloadConfig(); err != nil || loaded {
		t.Fatalf("ReloadConfig of a missing file = %v, %v, want nothing read", loaded, err)
	}

	if err := os.Rename(aside, path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}

	mustReload(t, svc)
}
