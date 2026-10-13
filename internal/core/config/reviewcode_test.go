package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// review-code keeps its worktrees under $REVIEW_CODE_WORKTREE_DIR when that is
// set. docket reports the tier-1 worktree there and grants codex the directory.
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
// docket reads the notes there, so it has to look where review-code writes them.
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
