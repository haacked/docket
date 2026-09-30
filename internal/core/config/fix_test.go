package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestLoadReadsTheFixAuthors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	body := "fix_authors = [\"app/posthog\", \"renovate[bot]\"]\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if want := []string{"app/posthog", "renovate[bot]"}; !slices.Equal(cfg.FixAuthors, want) {
		t.Errorf("fix_authors = %q, want %q", cfg.FixAuthors, want)
	}
}

// Fix mode is something the user opts into for named authors. A config that
// names none fixes nobody's pull request under auto.
func TestLoadWithoutFixAuthorsListsNobody(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(cfg.FixAuthors) != 0 {
		t.Errorf("fix_authors = %q, want none by default", cfg.FixAuthors)
	}
}

// docket's worktrees live under its own home rather than under review-code's
// skill, because review-code tears down everything it finds in its own.
func TestTheFixWorktreesLiveUnderTheHomeDirectory(t *testing.T) {
	paths, err := NewPaths("/tmp/docket")
	if err != nil {
		t.Fatal(err)
	}

	if paths.Worktrees != "/tmp/docket/worktrees" {
		t.Errorf("worktrees = %q, want /tmp/docket/worktrees", paths.Worktrees)
	}
	if got, want := paths.FixWorktreeDir("PostHog", "posthog", 105921), "/tmp/docket/worktrees/PostHog/posthog/pr-105921"; got != want {
		t.Errorf("worktree dir = %q, want %q", got, want)
	}
	if strings.HasPrefix(paths.Worktrees, paths.Clones) {
		t.Errorf("worktrees %q sit inside clones %q, so Cloner.Remove would accept one", paths.Worktrees, paths.Clones)
	}
}

func TestEnsureDirsCreatesTheWorktreesDirectory(t *testing.T) {
	paths, err := NewPaths(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	if err := paths.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}

	if info, err := os.Stat(paths.Worktrees); err != nil || !info.IsDir() {
		t.Errorf("%s is not a directory after EnsureDirs: %v", paths.Worktrees, err)
	}
}
