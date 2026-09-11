// Package git wraps the git commands docket's tier-2 clone needs.
package git

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/haacked/docket/internal/core/exec"
)

// Git is the git access docket needs. Tests substitute a fake.
type Git interface {
	IsRepo(ctx context.Context, dir string) bool
	Init(ctx context.Context, dir string) error
	RemoteAdd(ctx context.Context, dir, name, url string) error
	SymbolicRef(ctx context.Context, dir, name, target string) error
	FetchPR(ctx context.Context, dir, remote string, number int, branch string, depth int) error
	Checkout(ctx context.Context, dir, branch string) error
	ResetHard(ctx context.Context, dir, ref string) error
	CurrentBranch(ctx context.Context, dir string) (string, error)
	WorkTreeEmpty(ctx context.Context, dir string) (bool, error)
}

// CLI runs the git command.
type CLI struct {
	Runner exec.Runner
	Path   string
}

func New(runner exec.Runner) *CLI { return &CLI{Runner: runner, Path: "git"} }

func (c *CLI) run(ctx context.Context, args ...string) (exec.Result, error) {
	return c.Runner.Run(ctx, exec.CommandSpec{Path: cmp.Or(c.Path, "git"), Args: args})
}

// write runs a git command that changes the repository at dir.
func (c *CLI) write(ctx context.Context, dir string, args ...string) error {
	if _, err := c.run(ctx, append([]string{"-C", dir}, args...)...); err != nil {
		return fmt.Errorf("git %s in %s: %w", strings.Join(args, " "), dir, err)
	}
	return nil
}

func (c *CLI) IsRepo(ctx context.Context, dir string) bool {
	if dir == "" {
		return false
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return false
	}
	res, err := c.run(ctx, "-C", dir, "rev-parse", "--git-dir")
	return err == nil && strings.TrimSpace(res.Stdout) != ""
}

func (c *CLI) Init(ctx context.Context, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if _, err := c.run(ctx, "init", "--quiet", dir); err != nil {
		return fmt.Errorf("git init %s: %w", dir, err)
	}
	return nil
}

func (c *CLI) RemoteAdd(ctx context.Context, dir, name, url string) error {
	return c.write(ctx, dir, "remote", "add", name, url)
}

func (c *CLI) SymbolicRef(ctx context.Context, dir, name, target string) error {
	return c.write(ctx, dir, "symbolic-ref", name, target)
}

// FetchPR fetches the pull request head into a local branch named branch. The
// credential helper covers private repositories without a git credential setup
// of its own.
func (c *CLI) FetchPR(ctx context.Context, dir, remote string, number int, branch string, depth int) error {
	args := []string{
		"-C", dir,
		"-c", "credential.helper=!gh auth git-credential",
		"fetch",
	}
	if depth > 0 {
		args = append(args, "--depth", strconv.Itoa(depth))
	}
	args = append(args, remote, fmt.Sprintf("+pull/%d/head:refs/heads/%s", number, branch))
	if _, err := c.run(ctx, args...); err != nil {
		return fmt.Errorf("git fetch pull/%d/head: %w", number, err)
	}
	return nil
}

func (c *CLI) Checkout(ctx context.Context, dir, branch string) error {
	return c.write(ctx, dir, "checkout", "--quiet", branch)
}

func (c *CLI) ResetHard(ctx context.Context, dir, ref string) error {
	return c.write(ctx, dir, "reset", "--hard", "--quiet", ref)
}

func (c *CLI) CurrentBranch(ctx context.Context, dir string) (string, error) {
	res, err := c.run(ctx, "-C", dir, "branch", "--show-current")
	if err != nil {
		return "", fmt.Errorf("git branch --show-current in %s: %w", dir, err)
	}
	return strings.TrimSpace(res.Stdout), nil
}

// WorkTreeEmpty reports whether the checkout produced no tracked files, which is
// how a fetch that did not land shows up.
func (c *CLI) WorkTreeEmpty(ctx context.Context, dir string) (bool, error) {
	res, err := c.run(ctx, "-C", dir, "ls-files", "--cached")
	if err != nil {
		return false, fmt.Errorf("git ls-files in %s: %w", dir, err)
	}
	return strings.TrimSpace(res.Stdout) == "", nil
}
