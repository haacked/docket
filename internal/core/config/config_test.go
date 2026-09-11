package config

import (
	"os"
	"path/filepath"
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
	want := Config{ReviewCodeDir: "/opt/review-code", DefaultEngine: "codex", GitHubUser: "haacked", DefaultRepo: "haacked/docket"}

	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != want {
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
