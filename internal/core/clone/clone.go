// Package clone builds the tier-2 working directory, a shallow checkout of a
// pull request head. The local branch is named exactly like the head branch, so
// review-code takes its in-repo fast path instead of reviewing the diff alone.
package clone

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/haacked/docket/internal/core/config"
	"github.com/haacked/docket/internal/core/gh"
	"github.com/haacked/docket/internal/core/git"
	"github.com/haacked/docket/internal/core/pr"
)

// placeholderHead keeps HEAD on a branch nothing fetches into. A pull request
// opened from the fork's default branch otherwise collides with HEAD. git then
// either refuses the fetch or leaves an empty working tree.
const placeholderHead = "refs/heads/_docket"

// Depth is how much history the clone carries. The diff comes from gh, not from
// a local base branch, so one commit is enough. The cost is review-code's
// git-history metrics, which a fallback path can do without.
const Depth = 1

type Cloner struct {
	Git   git.Git
	Paths config.Paths
}

func New(g git.Git, paths config.Paths) *Cloner { return &Cloner{Git: g, Paths: paths} }

// Dir is where the clone for this pull request belongs.
func (c *Cloner) Dir(ref pr.Ref) string {
	return c.Paths.CloneDir(ref.Org, ref.Repo, ref.Number)
}

// Ensure returns a directory checked out at the pull request head. It builds in
// a temporary directory and renames it into place, so a failed clone never sits
// at the final path. An existing clone already on the head branch is refreshed
// rather than rebuilt.
func (c *Cloner) Ensure(ctx context.Context, ref pr.Ref, info gh.PRInfo) (string, error) {
	branch := info.HeadRefName
	if err := git.CheckBranch(branch); err != nil {
		return "", fmt.Errorf("validate the head branch: %w", err)
	}

	final := c.Dir(ref)
	if reused, err := c.refresh(ctx, final, ref, branch); err != nil {
		return "", err
	} else if reused {
		return final, nil
	}

	tmp := final + ".tmp"
	if err := os.RemoveAll(tmp); err != nil {
		return "", fmt.Errorf("clear %s: %w", tmp, err)
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return "", fmt.Errorf("create the parent of %s: %w", final, err)
	}

	if err := c.build(ctx, tmp, ref, branch); err != nil {
		os.RemoveAll(tmp)
		return "", err
	}

	if err := os.RemoveAll(final); err != nil {
		os.RemoveAll(tmp)
		return "", fmt.Errorf("clear %s: %w", final, err)
	}
	if err := os.Rename(tmp, final); err != nil {
		os.RemoveAll(tmp)
		return "", fmt.Errorf("move the clone into %s: %w", final, err)
	}
	return final, nil
}

func (c *Cloner) build(ctx context.Context, dir string, ref pr.Ref, branch string) error {
	if err := c.Git.Init(ctx, dir); err != nil {
		return fmt.Errorf("git init: %w", err)
	}
	remoteURL := fmt.Sprintf("https://github.com/%s/%s.git", ref.Org, ref.Repo)
	if err := c.Git.RemoteAdd(ctx, dir, "origin", remoteURL); err != nil {
		return fmt.Errorf("add the origin remote: %w", err)
	}
	if err := c.Git.SymbolicRef(ctx, dir, "HEAD", placeholderHead); err != nil {
		return fmt.Errorf("park HEAD: %w", err)
	}
	if err := c.Git.FetchPR(ctx, dir, "origin", ref.Number, branch, Depth); err != nil {
		return fmt.Errorf("fetch the pull request head: %w", err)
	}
	if err := c.Git.Checkout(ctx, dir, branch); err != nil {
		return fmt.Errorf("check out %s: %w", branch, err)
	}
	if err := git.VerifyCheckout(ctx, c.Git, dir, branch); err != nil {
		return fmt.Errorf("verify the checkout: %w", err)
	}
	return nil
}

// refresh reuses a clone already sitting on the head branch. Any failure returns
// false, so Ensure rebuilds from scratch. A cause that persists surfaces as the
// rebuild's own error.
func (c *Cloner) refresh(ctx context.Context, dir string, ref pr.Ref, branch string) (bool, error) {
	if !c.Git.IsRepo(ctx, dir) {
		return false, nil
	}
	current, err := c.Git.CurrentBranch(ctx, dir)
	if err != nil || current != branch {
		return false, nil
	}
	// The destination branch is checked out here, and git refuses to fetch into a
	// checked-out branch, so the head lands in FETCH_HEAD and the reset moves the
	// branch to it.
	if err := c.Git.FetchPR(ctx, dir, "origin", ref.Number, "", Depth); err != nil {
		return false, nil
	}
	if err := c.Git.ResetHard(ctx, dir, "FETCH_HEAD"); err != nil {
		return false, nil
	}
	if err := git.VerifyCheckout(ctx, c.Git, dir, branch); err != nil {
		return false, nil
	}
	return true, nil
}

// Remove deletes a tier-2 clone. It refuses any path outside docket's clones
// directory, because the only other directory a record can point at is the
// user's own local clone.
func (c *Cloner) Remove(dir string) error {
	if dir == "" {
		return nil
	}
	target, err := config.Inside(c.Paths.Clones, dir)
	if err != nil {
		return fmt.Errorf("refusing to delete: %w", err)
	}
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("delete %s: %w", target, err)
	}
	return nil
}
