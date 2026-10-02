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

// review-code keeps its worktrees, and the per-repository locks docket shares
// with it, under $REVIEW_CODE_WORKTREE_DIR when that is set.
func TestTheWorktreesDirFollowsReviewCodesOverride(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "config.toml")

	t.Setenv("REVIEW_CODE_WORKTREE_DIR", "/srv/review-worktrees")
	cfg, err := Load(missing)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.WorktreesDir(); got != "/srv/review-worktrees" {
		t.Errorf("WorktreesDir = %q, want the override", got)
	}

	t.Setenv("REVIEW_CODE_WORKTREE_DIR", "")
	cfg, err = Load(missing)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := filepath.Join(cfg.ReviewCodeDir, ".worktrees"); cfg.WorktreesDir() != want {
		t.Errorf("WorktreesDir = %q, want %q", cfg.WorktreesDir(), want)
	}
}

// review-code writes its notes under $REVIEW_CODE_REVIEW_DIR when that is set.
// A background fix session counts as done once its notes appear, so docket has
// to look where review-code writes them.
func TestTheNotesFollowReviewCodesOverride(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "config.toml")

	t.Setenv("REVIEW_CODE_REVIEW_DIR", "/srv/reviews")
	cfg, err := Load(missing)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.NotesPath("PostHog", "posthog", 7), "/srv/reviews/PostHog/posthog/pr-7.md"; got != want {
		t.Errorf("NotesPath = %q, want %q", got, want)
	}

	t.Setenv("REVIEW_CODE_REVIEW_DIR", "")
	cfg, err = Load(missing)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := filepath.Join(cfg.ReviewCodeDir, ".reviews"); cfg.ReviewsDir() != want {
		t.Errorf("ReviewsDir = %q, want %q", cfg.ReviewsDir(), want)
	}
}

// docket and review-code run from different directories. bash does not expand
// a tilde in a variable's value. A relative override, or one that starts with
// ~, would therefore name two places.
func TestLoadRefusesARelativeReviewCodeOverride(t *testing.T) {
	for _, name := range []string{"REVIEW_CODE_WORKTREE_DIR", "REVIEW_CODE_REVIEW_DIR"} {
		for _, value := range []string{"relative/dir", "~/reviews"} {
			t.Run(name+" "+value, func(t *testing.T) {
				t.Setenv(name, value)
				if _, err := Load(filepath.Join(t.TempDir(), "config.toml")); err == nil || !strings.Contains(err.Error(), name) {
					t.Errorf("Load err = %v, want a refusal that names %s", err, name)
				}
			})
		}
	}
}
