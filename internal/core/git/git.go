// Package git wraps the git commands docket's checkouts need: the tier-2 clone,
// and the tier-3 worktree a fix review edits.
package git

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"slices"
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
	Head(ctx context.Context, dir string) (string, error)
	Dirty(ctx context.Context, dir string) (bool, error)
	FetchBranch(ctx context.Context, dir, remote, branch string) error
	Ahead(ctx context.Context, dir, upstream string) (int, error)
	SetUpstream(ctx context.Context, dir, branch, upstream string) error
	SetCredentialHelper(ctx context.Context, dir string) error
	Remotes(ctx context.Context, dir string) ([]Remote, error)
	BranchExists(ctx context.Context, dir, branch string) (bool, error)
	WorktreeAdd(ctx context.Context, base, dir, branch, start string) error
	WorktreeRemove(ctx context.Context, base, dir string) error
	BranchDelete(ctx context.Context, base, branch string) error
}

// ghCredential makes git ask gh for GitHub credentials. The empty
// credential.helper resets the list, because `-c` appends to a multi-valued
// config rather than replacing it, and git does not try the next helper after
// one fails.
var ghCredential = []string{"-c", "credential.helper=", "-c", "credential.helper=" + ghHelper}

// ghHelper is the credential helper that asks gh for GitHub credentials.
const ghHelper = "!gh auth git-credential"

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

// FetchPR fetches the pull request head into a local branch named branch. An
// empty branch leaves the head in FETCH_HEAD, which is what a caller wants when
// the destination branch is already checked out: git refuses to fetch into a
// checked-out branch.
func (c *CLI) FetchPR(ctx context.Context, dir, remote string, number int, branch string, depth int) error {
	args := slices.Concat([]string{"-C", dir}, ghCredential, []string{"fetch"})
	if depth > 0 {
		args = append(args, "--depth", strconv.Itoa(depth))
	}
	refspec := fmt.Sprintf("pull/%d/head", number)
	if branch != "" {
		refspec = fmt.Sprintf("+pull/%d/head:refs/heads/%s", number, branch)
	}
	args = append(args, remote, refspec)
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

// Head is the commit the checkout at dir is on.
func (c *CLI) Head(ctx context.Context, dir string) (string, error) {
	res, err := c.run(ctx, "-C", dir, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD in %s: %w", dir, err)
	}
	return strings.TrimSpace(res.Stdout), nil
}

// Dirty reports whether the checkout at dir has uncommitted changes, untracked
// files included.
func (c *CLI) Dirty(ctx context.Context, dir string) (bool, error) {
	res, err := c.run(ctx, "-C", dir, "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("git status in %s: %w", dir, err)
	}
	return strings.TrimSpace(res.Stdout) != "", nil
}

// FetchBranch fetches a branch of remote into its remote-tracking ref,
// refs/remotes/<remote>/<branch>. It passes no --depth. In a shallow clone a
// plain fetch stops at commits the clone already has, so the new commits stay
// connected to the local ones. A --depth fetch would cut them off, and counting
// the local commits the remote lacks would then count pushed ones too.
func (c *CLI) FetchBranch(ctx context.Context, dir, remote, branch string) error {
	args := slices.Concat([]string{"-C", dir}, ghCredential, []string{"fetch", "--quiet"})
	args = append(args, remote, fmt.Sprintf("+refs/heads/%s:refs/remotes/%s/%s", branch, remote, branch))
	if _, err := c.run(ctx, args...); err != nil {
		return fmt.Errorf("git fetch %s %s: %w", remote, branch, err)
	}
	return nil
}

// Ahead counts the commits HEAD has that upstream does not.
func (c *CLI) Ahead(ctx context.Context, dir, upstream string) (int, error) {
	res, err := c.run(ctx, "-C", dir, "rev-list", "--count", upstream+"..HEAD")
	if err != nil {
		return 0, fmt.Errorf("git rev-list %s..HEAD in %s: %w", upstream, dir, err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(res.Stdout))
	if err != nil {
		return 0, fmt.Errorf("git rev-list %s..HEAD in %s printed %q", upstream, dir, res.Stdout)
	}
	return n, nil
}

// SetUpstream makes upstream the branch that branch pushes to and pulls from.
func (c *CLI) SetUpstream(ctx context.Context, dir, branch, upstream string) error {
	return c.write(ctx, dir, "branch", "--quiet", "--set-upstream-to="+upstream, branch)
}

// SetCredentialHelper makes the repository at dir ask gh for GitHub
// credentials, so a push from it authenticates the way docket's fetches do. The
// first value empties the list the global config contributes.
func (c *CLI) SetCredentialHelper(ctx context.Context, dir string) error {
	if err := c.write(ctx, dir, "config", "--local", "--replace-all", "credential.helper", ""); err != nil {
		return err
	}
	return c.write(ctx, dir, "config", "--local", "--add", "credential.helper", ghHelper)
}

// Remote is one of a repository's remotes and the URL it fetches from.
type Remote struct {
	Name string
	URL  string
}

// Remotes lists the remotes of the repository at dir, with the URLs their
// config names. `git remote -v` prints URLs after url.<base>.insteadOf has
// rewritten them, which can hide that a remote is the GitHub repository. git
// config exits 1 when no key matches, which is a repository with no remotes.
func (c *CLI) Remotes(ctx context.Context, dir string) ([]Remote, error) {
	res, err := c.run(ctx, "-C", dir, "config", "--get-regexp", `^remote\..*\.url$`)
	if err != nil {
		if res.ExitCode == 1 && strings.TrimSpace(res.Stdout) == "" {
			return nil, nil
		}
		return nil, fmt.Errorf("git config remote urls in %s: %w", dir, err)
	}
	return parseRemotes(res.Stdout), nil
}

// parseRemotes reads `git config --get-regexp` lines of the form
// `remote.<name>.url <url>`. A remote's name can contain dots.
func parseRemotes(out string) []Remote {
	var remotes []Remote
	for line := range strings.Lines(out) {
		key, url, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		name, ok := strings.CutPrefix(key, "remote.")
		if !ok {
			continue
		}
		if name, ok = strings.CutSuffix(name, ".url"); ok {
			remotes = append(remotes, Remote{Name: name, URL: strings.TrimSpace(url)})
		}
	}
	return remotes
}

// RemoteFor names the remote whose URL is github.com/<org>/<repo>, over https
// or ssh, ignoring case and a trailing .git.
func RemoteFor(remotes []Remote, org, repo string) (string, bool) {
	want := strings.ToLower(org + "/" + repo)
	for _, r := range remotes {
		if path, ok := githubPath(r.URL); ok && strings.ToLower(path) == want {
			return r.Name, true
		}
	}
	return "", false
}

// ShadowsRemote reports whether a local branch named branch would hide a
// remote-tracking ref. git resolves a short name in refs/heads before
// refs/remotes, so a branch named origin/main in a clone with an origin remote
// takes over origin/main there.
func ShadowsRemote(remotes []Remote, branch string) bool {
	for _, r := range remotes {
		if branch == r.Name || strings.HasPrefix(branch, r.Name+"/") {
			return true
		}
	}
	return false
}

func githubPath(url string) (string, bool) {
	for _, prefix := range []string{"https://github.com/", "http://github.com/", "ssh://git@github.com/", "git@github.com:"} {
		if rest, ok := strings.CutPrefix(url, prefix); ok {
			return strings.TrimSuffix(strings.TrimSuffix(rest, "/"), ".git"), true
		}
	}
	return "", false
}

// BranchExists reports whether the repository at dir has a local branch named
// branch. for-each-ref prints nothing for a missing ref and still exits zero.
func (c *CLI) BranchExists(ctx context.Context, dir, branch string) (bool, error) {
	res, err := c.run(ctx, "-C", dir, "for-each-ref", "--format=%(refname)", "refs/heads/"+branch)
	if err != nil {
		return false, fmt.Errorf("git for-each-ref in %s: %w", dir, err)
	}
	return strings.TrimSpace(res.Stdout) != "", nil
}

// WorktreeAdd adds a worktree of base at dir on a new branch that starts at, and
// tracks, start.
func (c *CLI) WorktreeAdd(ctx context.Context, base, dir, branch, start string) error {
	return c.write(ctx, base, "worktree", "add", "--quiet", "--track", "-b", branch, dir, start)
}

// WorktreeRemove removes the worktree at dir from base, whatever it holds. The
// second --force removes a locked worktree: Supacode locks every worktree it
// finds. The caller checks the worktree for work first.
func (c *CLI) WorktreeRemove(ctx context.Context, base, dir string) error {
	return c.write(ctx, base, "worktree", "remove", "--force", "--force", dir)
}

// BranchDelete deletes a local branch of base whether or not it was merged.
func (c *CLI) BranchDelete(ctx context.Context, base, branch string) error {
	return c.write(ctx, base, "branch", "-D", branch)
}

// CheckBranch refuses a head branch name that git would read as an option or
// a range, or that a command line would split.
func CheckBranch(branch string) error {
	switch {
	case branch == "":
		return fmt.Errorf("empty head branch")
	case strings.HasPrefix(branch, "-"):
		return fmt.Errorf("head branch %q starts with a dash", branch)
	case strings.Contains(branch, ".."):
		return fmt.Errorf("head branch %q contains ..", branch)
	case strings.ContainsAny(branch, " \t\n"):
		return fmt.Errorf("head branch %q contains whitespace", branch)
	}
	return nil
}

// VerifyCheckout guards what review-code's fast path depends on. review-code
// compares the current branch against the pull request's head branch, so a
// detached checkout or a differently named branch silently costs full-file
// context, and a fix review edits nothing.
func VerifyCheckout(ctx context.Context, g Git, dir, branch string) error {
	current, err := g.CurrentBranch(ctx, dir)
	if err != nil {
		return err
	}
	if current != branch {
		return fmt.Errorf("checkout landed on %q, want %q", current, branch)
	}
	empty, err := g.WorkTreeEmpty(ctx, dir)
	if err != nil {
		return err
	}
	if empty {
		return fmt.Errorf("checkout of %s left an empty working tree", branch)
	}
	return nil
}
