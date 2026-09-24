package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNewPathsPrefersTheArgumentThenTheEnvironment(t *testing.T) {
	t.Setenv("DOCKET_HOME", "/tmp/from-env")

	paths, err := NewPaths("/tmp/from-arg")
	if err != nil {
		t.Fatal(err)
	}
	if paths.Home != "/tmp/from-arg" {
		t.Errorf("home = %q, want the argument", paths.Home)
	}

	paths, err = NewPaths("")
	if err != nil {
		t.Fatal(err)
	}
	if paths.Home != "/tmp/from-env" {
		t.Errorf("home = %q, want the environment", paths.Home)
	}
}

func TestNewPathsFallsBackToTheHomeDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("DOCKET_HOME", "")
	t.Setenv("HOME", home)

	paths, err := NewPaths("")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".docket"); paths.Home != want {
		t.Errorf("home = %q, want %q", paths.Home, want)
	}
}

func TestPathsLayout(t *testing.T) {
	paths, err := NewPaths("/tmp/docket")
	if err != nil {
		t.Fatal(err)
	}

	for name, got := range map[string]string{
		"index":   paths.Index,
		"lock":    paths.Lock,
		"clones":  paths.Clones,
		"scratch": paths.Scratch,
		"config":  paths.Config,
	} {
		if !strings.HasPrefix(got, "/tmp/docket/") {
			t.Errorf("%s = %q, want it under the home directory", name, got)
		}
	}
	if want := "/tmp/docket/clones/haacked/docket/pr-7"; paths.CloneDir("haacked", "docket", 7) != want {
		t.Errorf("clone dir = %q, want %q", paths.CloneDir("haacked", "docket", 7), want)
	}
}

func TestLoadAMissingFileGivesDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.DefaultEngine != EngineClaude {
		t.Errorf("default engine = %q, want claude", cfg.DefaultEngine)
	}
	if strings.HasPrefix(cfg.ReviewCodeDir, "~") {
		t.Errorf("review_code_dir = %q, want the tilde expanded", cfg.ReviewCodeDir)
	}
	if !strings.HasSuffix(cfg.ReviewCodeDir, filepath.Join(".agents", "skills", "review-code")) {
		t.Errorf("review_code_dir = %q, want review-code's installed path", cfg.ReviewCodeDir)
	}
}

func TestLoadReadsTheFileAndFillsTheGaps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	body := "review_code_dir = \"/opt/review-code\"\ngithub_user = \"haacked\"\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.ReviewCodeDir != "/opt/review-code" {
		t.Errorf("review_code_dir = %q", cfg.ReviewCodeDir)
	}
	if cfg.GitHubUser != "haacked" {
		t.Errorf("github_user = %q", cfg.GitHubUser)
	}
	if cfg.DefaultEngine != EngineClaude {
		t.Errorf("default engine = %q, want the default to fill in", cfg.DefaultEngine)
	}
}

func TestSaveThenLoadRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	want := Config{
		ReviewCodeDir:    "/opt/review-code",
		CodexSessionsDir: "/opt/codex/sessions",
		ClaudeJobsDir:    "/opt/claude/jobs",
		DefaultEngine:    "codex",
		GitHubUser:       "haacked",
		DefaultRepo:      "haacked/docket",
		Teams:            []string{"PostHog/team-feature-flags"},
	}

	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestReviewCodePaths(t *testing.T) {
	cfg := Config{ReviewCodeDir: "/opt/review-code"}

	if want := "/opt/review-code/repos.conf"; cfg.ReposConfPath() != want {
		t.Errorf("repos.conf = %q, want %q", cfg.ReposConfPath(), want)
	}
	if want := "/opt/review-code/.reviews/haacked/docket/pr-7.md"; cfg.NotesPath("haacked", "docket", 7) != want {
		t.Errorf("notes = %q, want %q", cfg.NotesPath("haacked", "docket", 7), want)
	}
	if want := "/opt/review-code/.worktrees/haacked/docket/pr-7"; cfg.WorktreeDir("haacked", "docket", 7) != want {
		t.Errorf("worktree = %q, want %q", cfg.WorktreeDir("haacked", "docket", 7), want)
	}
}

func TestNewPathsMakesARelativeHomeAbsolute(t *testing.T) {
	paths, err := NewPaths("relative/dir")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(paths.Home) {
		t.Errorf("home = %q, want an absolute path: the index stores clone paths, and a relative home resolves against whatever directory docket started in", paths.Home)
	}
	if !strings.HasSuffix(paths.Home, filepath.Join("relative", "dir")) {
		t.Errorf("home = %q, want it to end in the directory that was asked for", paths.Home)
	}
}

func TestNewPathsExpandsALeadingTilde(t *testing.T) {
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}

	paths, err := NewPaths("~/docket-home")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(userHome, "docket-home"); paths.Home != want {
		t.Errorf("home = %q, want %q", paths.Home, want)
	}
}

// Each of these directories belongs to an agent, not to docket, so each is
// configuration with a default rather than a path docket knows. docket reads the
// codex sessions directory to recover the id of a session it just ran, and
// claude's jobs directory to show what a background review is doing.
func TestAgentDirectoriesAreConfiguration(t *testing.T) {
	for _, tc := range []struct {
		key    string
		field  func(Config) string
		suffix string
	}{
		{key: "codex_sessions_dir", field: func(c Config) string { return c.CodexSessionsDir }, suffix: filepath.Join(".codex", "sessions")},
		{key: "claude_jobs_dir", field: func(c Config) string { return c.ClaudeJobsDir }, suffix: filepath.Join(".claude", "jobs")},
	} {
		t.Run(tc.key+" defaults to the agent's installed path", func(t *testing.T) {
			cfg, err := Load(filepath.Join(t.TempDir(), "config.toml"))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}

			got := tc.field(cfg)
			if strings.HasPrefix(got, "~") {
				t.Errorf("%s = %q, want the tilde expanded", tc.key, got)
			}
			if !strings.HasSuffix(got, tc.suffix) {
				t.Errorf("%s = %q, want the agent's installed path", tc.key, got)
			}
		})

		t.Run(tc.key+" from the file wins", func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(tc.key+" = \"/opt/agent/dir\"\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}

			if got := tc.field(cfg); got != "/opt/agent/dir" {
				t.Errorf("%s = %q, want the configured path", tc.key, got)
			}
		})
	}
}
