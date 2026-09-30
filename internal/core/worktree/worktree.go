// Package worktree adds and removes the tier-3 working directory: a worktree of
// the repos.conf clone on a branch named exactly like the pull request's head
// branch. review-code edits files for --fix only on that branch. review-code's
// own worktree is detached, and review-code tears it down when its session ends.
package worktree

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/haacked/docket/internal/core/config"
	"github.com/haacked/docket/internal/core/git"
	"github.com/haacked/docket/internal/core/pr"
)

// DefaultWait is how long Adder waits for the lock. review-code's
// REVIEW_CODE_LOCK_TIMEOUT defaults to the same.
const DefaultWait = 30 * time.Second

// fetchWait bounds the wait for the lock before a fetch. review-code holds the
// lock across a fetch and a checkout. A caller whose Fetch fails counts against
// the ref from the last fetch that worked, so a long wait would only hold up a
// refresh.
const fetchWait = 2 * time.Second

type Adder struct {
	Git   git.Git
	Paths config.Paths
	// LockRoot is review-code's worktree root. review-code's pr-worktree.sh runs
	// fetch and `worktree add/remove` against the same clone, and git is not
	// safe to run there concurrently, so docket takes the same per-repository
	// lock.
	LockRoot string
	// Wait bounds the wait for the lock. Zero means DefaultWait.
	Wait time.Duration
}

func New(g git.Git, paths config.Paths, lockRoot string) *Adder {
	return &Adder{Git: g, Paths: paths, LockRoot: lockRoot}
}

// Dir is where the worktree for this pull request belongs.
func (a *Adder) Dir(ref pr.Ref) string {
	return a.Paths.FixWorktreeDir(ref.Org, ref.Repo, ref.Number)
}

// LockPath is the lock review-code's worktree_lock_for names for a repository.
// review-code lowercases the org and the repo, so a mixed-case ref names the
// same lock.
func LockPath(root, org, repo string) string {
	return filepath.Join(root, strings.ToLower(org), strings.ToLower(repo)+".lock")
}

// Ensure adds the worktree of localClone for ref on branch and returns its
// directory and the remote its branch tracks. The branch starts at the remote
// branch and tracks it, so a push from the worktree needs no arguments.
func (a *Adder) Ensure(ctx context.Context, localClone string, ref pr.Ref, branch string) (string, string, error) {
	if err := git.CheckBranch(branch); err != nil {
		return "", "", fmt.Errorf("validate the head branch: %w", err)
	}
	unlock, err := a.lock(ctx, ref, a.wait())
	if err != nil {
		return "", "", err
	}
	defer unlock()

	// A failed add removes the directory and the branch, so both have to be new.
	// The branch check also covers a branch made since resolve looked for it.
	dir := a.Dir(ref)
	if _, err := os.Stat(dir); err == nil {
		return "", "", fmt.Errorf("%s already exists; remove it and try again", dir)
	}
	exists, err := a.Git.BranchExists(ctx, localClone, branch)
	if err != nil {
		return "", "", err
	}
	if exists {
		return "", "", fmt.Errorf("%s already has a branch named %s", localClone, branch)
	}

	remotes, err := a.Git.Remotes(ctx, localClone)
	if err != nil {
		return "", "", err
	}
	remote, ok := git.RemoteFor(remotes, ref.Org, ref.Repo)
	if !ok {
		return "", "", fmt.Errorf("no remote of %s points at github.com/%s", localClone, ref.Slug())
	}
	if git.ShadowsRemote(remotes, branch) {
		return "", "", fmt.Errorf("a branch named %s in %s would hide the remote-tracking ref of that name", branch, localClone)
	}
	if err := a.Git.FetchBranch(ctx, localClone, remote, branch); err != nil {
		return "", "", err
	}

	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", "", fmt.Errorf("create the parent of %s: %w", dir, err)
	}
	// git creates the branch before it adds the worktree. git also keeps a
	// worktree whose post-checkout hook failed.
	if err := a.Git.WorktreeAdd(ctx, localClone, dir, branch, remote+"/"+branch); err != nil {
		return "", "", errors.Join(err, a.remove(ctx, localClone, dir, branch))
	}
	if err := git.VerifyCheckout(ctx, a.Git, dir, branch); err != nil {
		return "", "", errors.Join(fmt.Errorf("verify the worktree: %w", err), a.remove(ctx, localClone, dir, branch))
	}
	return dir, remote, nil
}

// Fetch updates the remote-tracking ref of the worktree's branch. The fetch
// writes to the clone's shared refs, so it takes the lock too, but it waits only
// briefly for it.
func (a *Adder) Fetch(ctx context.Context, ref pr.Ref, dir, remote, branch string) error {
	unlock, err := a.lock(ctx, ref, min(fetchWait, a.wait()))
	if err != nil {
		return err
	}
	defer unlock()
	return a.Git.FetchBranch(ctx, dir, remote, branch)
}

// Remove removes the worktree at dir from localClone and deletes its branch,
// whatever the worktree holds. The caller checks it for work first. It refuses
// a directory outside docket's worktrees directory, because the only other
// directory a record can point at is the user's own clone.
func (a *Adder) Remove(ctx context.Context, localClone string, ref pr.Ref, dir, branch string) error {
	if _, err := config.Inside(a.Paths.Worktrees, dir); err != nil {
		return fmt.Errorf("refusing to remove: %w", err)
	}
	unlock, err := a.lock(ctx, ref, a.wait())
	if err != nil {
		return err
	}
	defer unlock()
	return a.remove(ctx, localClone, dir, branch)
}

// remove removes the worktree at dir and deletes its branch. It tries the branch
// even when the worktree removal fails, because a failed add can leave the
// branch with no worktree on it.
func (a *Adder) remove(ctx context.Context, localClone, dir, branch string) error {
	var rmErr error
	if _, err := os.Stat(dir); err == nil {
		rmErr = a.Git.WorktreeRemove(ctx, localClone, dir)
	}
	exists, err := a.Git.BranchExists(ctx, localClone, branch)
	if err == nil && exists {
		err = a.Git.BranchDelete(ctx, localClone, branch)
	}
	return errors.Join(rmErr, err)
}

func (a *Adder) wait() time.Duration {
	return cmp.Or(a.Wait, DefaultWait)
}

// lock takes review-code's mkdir lock for the repository, waiting up to wait,
// and returns the function that releases it.
func (a *Adder) lock(ctx context.Context, ref pr.Ref, wait time.Duration) (func(), error) {
	path := LockPath(a.LockRoot, ref.Org, ref.Repo)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create the parent of %s: %w", path, err)
	}
	deadline := time.Now().Add(wait)
	for {
		err := os.Mkdir(path, 0o755)
		if err == nil {
			return func() { os.Remove(path) }, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%s is still locked. review-code may be setting up a worktree of %s; if nothing is, remove the directory", path, ref.Slug())
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}
