package worktree_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/haacked/docket/internal/core/config"
	"github.com/haacked/docket/internal/core/git"
	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/worktree"
)

// lockingGit records the git commands a worktree would run, and whether
// review-code's lock was held when each one ran. It keeps just enough state for
// the checks Ensure makes after adding the worktree.
type lockingGit struct {
	mu sync.Mutex
	// lock is the path the lock should be at. Each call records whether it was.
	lock     string
	calls    []string
	locked   []bool
	failAt   string
	remotes  []git.Remote
	current  map[string]string
	branches map[string]bool
	empty    bool
	// landOn makes WorktreeAdd leave the worktree on another branch.
	landOn string
	// detached makes WorktreeAdd leave the worktree on no branch.
	detached bool
}

func newLockingGit(lock string) *lockingGit {
	return &lockingGit{
		lock:     lock,
		remotes:  []git.Remote{{Name: "origin", URL: "https://github.com/PostHog/posthog.git"}},
		current:  map[string]string{},
		branches: map[string]bool{},
	}
}

func (f *lockingGit) record(call string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	info, err := os.Stat(f.lock)
	f.calls = append(f.calls, call)
	f.locked = append(f.locked, err == nil && info.IsDir())
	if strings.HasPrefix(call, f.failAt+" ") || call == f.failAt {
		return errors.New("git failed: " + call)
	}
	return nil
}

// names is the first word of each call.
func (f *lockingGit) names() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		name, _, _ := strings.Cut(c, " ")
		out = append(out, name)
	}
	return out
}

func (f *lockingGit) callsNamed(name string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if c == name || strings.HasPrefix(c, name+" ") {
			out = append(out, c)
		}
	}
	return out
}

func (f *lockingGit) IsRepo(context.Context, string) bool { return true }

func (f *lockingGit) Init(context.Context, string) error { return f.record("init") }

func (f *lockingGit) RemoteAdd(context.Context, string, string, string) error {
	return f.record("remote-add")
}

func (f *lockingGit) SymbolicRef(context.Context, string, string, string) error {
	return f.record("symbolic-ref")
}

func (f *lockingGit) FetchPR(context.Context, string, string, int, string, int) error {
	return f.record("fetch-pr")
}

func (f *lockingGit) Checkout(context.Context, string, string) error { return f.record("checkout") }

func (f *lockingGit) ResetHard(_ context.Context, dir, ref string) error {
	return f.record("reset " + dir + " " + ref)
}

func (f *lockingGit) CurrentBranch(_ context.Context, dir string) (string, error) {
	if err := f.record("current-branch " + dir); err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.current[dir], nil
}

func (f *lockingGit) WorkTreeEmpty(_ context.Context, dir string) (bool, error) {
	if err := f.record("ls-files " + dir); err != nil {
		return false, err
	}
	return f.empty, nil
}

func (f *lockingGit) Head(_ context.Context, dir string) (string, error) {
	return "base-sha", f.record("head " + dir)
}

func (f *lockingGit) Dirty(_ context.Context, dir string) (bool, error) {
	return false, f.record("status " + dir)
}

func (f *lockingGit) FetchBranch(_ context.Context, dir, remote, branch string) error {
	return f.record("fetch-branch " + dir + " " + remote + " " + branch)
}

func (f *lockingGit) Ahead(_ context.Context, dir, upstream string) (int, error) {
	return 0, f.record("ahead " + dir + " " + upstream)
}

func (f *lockingGit) SetUpstream(_ context.Context, dir, branch, upstream string) error {
	return f.record("upstream " + dir + " " + branch + " " + upstream)
}

func (f *lockingGit) SetCredentialHelper(_ context.Context, dir string) error {
	return f.record("credential " + dir)
}

func (f *lockingGit) Remotes(_ context.Context, dir string) ([]git.Remote, error) {
	if err := f.record("remotes " + dir); err != nil {
		return nil, err
	}
	return f.remotes, nil
}

func (f *lockingGit) BranchExists(_ context.Context, dir, branch string) (bool, error) {
	if err := f.record("branch-exists " + dir + " " + branch); err != nil {
		return false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.branches[dir+" "+branch], nil
}

func (f *lockingGit) WorktreeAdd(_ context.Context, base, dir, branch, start string) error {
	if err := f.record("worktree-add " + base + " " + dir + " " + branch + " " + start); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case f.detached:
		f.current[dir] = ""
	case f.landOn != "":
		f.current[dir] = f.landOn
	default:
		f.current[dir] = branch
	}
	f.branches[base+" "+branch] = true
	return nil
}

func (f *lockingGit) WorktreeRemove(_ context.Context, base, dir string) error {
	if err := f.record("worktree-remove " + base + " " + dir); err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

func (f *lockingGit) BranchDelete(_ context.Context, base, branch string) error {
	if err := f.record("branch-delete " + base + " " + branch); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.branches, base+" "+branch)
	return nil
}

// The PostHog bot's pull requests are the case this exists for, and PostHog
// spells its org and repo in mixed case in a pull request URL.
var ref = pr.Ref{Org: "PostHog", Repo: "PostHog", Number: 105921}

const branch = "posthog/fix-thing"

type fixture struct {
	adder *worktree.Adder
	git   *lockingGit
	paths config.Paths
	// root is review-code's worktree root, where its lock lives.
	root string
	// lock is the path review-code's worktree_lock_for builds for ref.
	lock string
	// clone is the user's repos.conf clone.
	clone string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	paths, err := config.NewPaths(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := paths.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "review-code", ".worktrees")
	// review-code's worktree_lock_for lowercases both halves:
	// <worktree_root>/<org lower>/<repo lower>.lock.
	lock := filepath.Join(root, "posthog", "posthog.lock")
	g := newLockingGit(lock)
	return fixture{
		adder: worktree.New(g, paths, root),
		git:   g,
		paths: paths,
		root:  root,
		lock:  lock,
		clone: t.TempDir(),
	}
}

func (f fixture) ensure(t *testing.T) (string, string) {
	t.Helper()
	dir, remote, err := f.adder.Ensure(context.Background(), f.clone, ref, branch)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	return dir, remote
}

func TestLockPathMatchesReviewCodesLockForAMixedCaseRef(t *testing.T) {
	got := worktree.LockPath("/opt/review-code/.worktrees", "PostHog", "PostHog")

	if want := "/opt/review-code/.worktrees/posthog/posthog.lock"; got != want {
		t.Errorf("LockPath = %q, want %q", got, want)
	}
}

func TestEnsureAddsTheWorktreeUnderDocketsWorktreesDirectory(t *testing.T) {
	f := newFixture(t)

	dir, _ := f.ensure(t)

	if want := f.paths.FixWorktreeDir(ref.Org, ref.Repo, ref.Number); dir != want {
		t.Errorf("dir = %q, want %q", dir, want)
	}
	if want := f.adder.Dir(ref); dir != want {
		t.Errorf("dir = %q, want Dir's %q", dir, want)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Errorf("%s is not a directory after Ensure: %v", dir, err)
	}
}

// Fetching into the remote-tracking ref and starting the branch there with
// --track is what lets a plain `git push` in the session reach the head branch.
func TestEnsureStartsATrackingBranchAtTheFetchedRemoteBranch(t *testing.T) {
	f := newFixture(t)

	dir, remote := f.ensure(t)

	if remote != "origin" {
		t.Errorf("remote = %q, want origin", remote)
	}
	if want := []string{"remotes", "fetch-branch", "worktree-add"}; !slices.Equal(f.git.names()[:3], want) {
		t.Errorf("calls = %v, want %v first", f.git.calls, want)
	}
	if got, want := f.git.callsNamed("fetch-branch"), []string{"fetch-branch " + f.clone + " origin " + branch}; !slices.Equal(got, want) {
		t.Errorf("fetches = %v, want %v", got, want)
	}
	if got, want := f.git.callsNamed("worktree-add"), []string{"worktree-add " + f.clone + " " + dir + " " + branch + " origin/" + branch}; !slices.Equal(got, want) {
		t.Errorf("worktree adds = %v, want %v", got, want)
	}
}

// A clone whose origin is the user's fork keeps the base repository under
// another name. The fix is pushed to the base repository's head branch.
func TestEnsureUsesTheRemoteThatPointsAtThePullRequestsRepository(t *testing.T) {
	f := newFixture(t)
	f.git.remotes = []git.Remote{
		{Name: "origin", URL: "git@github.com:haacked/posthog.git"},
		{Name: "upstream", URL: "git@github.com:PostHog/posthog.git"},
	}

	_, remote := f.ensure(t)

	if remote != "upstream" {
		t.Errorf("remote = %q, want upstream", remote)
	}
	add := f.git.callsNamed("worktree-add")
	if len(add) != 1 || !strings.HasSuffix(add[0], " upstream/"+branch) {
		t.Errorf("worktree adds = %v, want one starting at upstream/%s", add, branch)
	}
}

// With no remote for the pull request's repository there is nowhere to fetch
// the head branch from or to push the fixes to.
func TestEnsureRefusesACloneWithNoRemoteForTheRepository(t *testing.T) {
	f := newFixture(t)
	f.git.remotes = []git.Remote{{Name: "origin", URL: "git@github.com:haacked/posthog.git"}}

	_, _, err := f.adder.Ensure(context.Background(), f.clone, ref, branch)

	if err == nil {
		t.Fatal("Ensure succeeded with no remote for PostHog/PostHog")
	}
	if !strings.Contains(err.Error(), f.clone) {
		t.Errorf("error = %v, want it to name the clone", err)
	}
	for _, name := range []string{"fetch-branch", "worktree-add"} {
		if got := f.git.callsNamed(name); len(got) != 0 {
			t.Errorf("Ensure ran %v after finding no remote", got)
		}
	}
	if _, err := os.Stat(f.adder.Dir(ref)); !os.IsNotExist(err) {
		t.Errorf("%s exists after a refusal", f.adder.Dir(ref))
	}
}

// review-code's pr-worktree.sh fetches and adds worktrees in the same clone,
// and git is not safe to run there concurrently. docket takes review-code's own
// lock, at the path review-code builds, or the two would not exclude each
// other.
func TestEnsureHoldsReviewCodesLockForEveryGitCommand(t *testing.T) {
	f := newFixture(t)

	f.ensure(t)

	if len(f.git.calls) == 0 {
		t.Fatal("Ensure ran no git commands")
	}
	for i, held := range f.git.locked {
		if !held {
			t.Errorf("%s ran without %s held", f.git.calls[i], f.lock)
		}
	}
	if _, err := os.Stat(f.lock); !os.IsNotExist(err) {
		t.Errorf("%s is still there after Ensure returned", f.lock)
	}
	if info, err := os.Stat(filepath.Dir(f.lock)); err != nil || !info.IsDir() {
		t.Errorf("the lock's parent %s was not created: %v", filepath.Dir(f.lock), err)
	}
}

func TestEnsureReleasesTheLockWhenAStepFails(t *testing.T) {
	for _, step := range []string{"remotes", "fetch-branch", "worktree-add"} {
		t.Run(step, func(t *testing.T) {
			f := newFixture(t)
			f.git.failAt = step

			if _, _, err := f.adder.Ensure(context.Background(), f.clone, ref, branch); err == nil {
				t.Fatal("Ensure succeeded, want the failure")
			}

			if _, err := os.Stat(f.lock); !os.IsNotExist(err) {
				t.Errorf("%s is still there after a failed %s, so review-code waits on it", f.lock, step)
			}
		})
	}
}

// review-code holds the lock while it sets up its own worktree of the clone.
func TestEnsureWaitsForALockReviewCodeHolds(t *testing.T) {
	f := newFixture(t)
	f.adder.Wait = 10 * time.Second
	if err := os.MkdirAll(f.lock, 0o755); err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(300 * time.Millisecond)
		f.git.mu.Lock()
		ran := len(f.git.calls)
		f.git.mu.Unlock()
		if ran != 0 {
			t.Errorf("git ran %d commands while review-code held the lock", ran)
		}
		os.Remove(f.lock)
		close(released)
	}()

	f.ensure(t)
	<-released

	if len(f.git.calls) == 0 {
		t.Error("Ensure ran nothing once the lock was released")
	}
}

// A lock left behind by a crash never goes away on its own. Ensure gives up
// rather than hang, and it leaves the lock alone, because it cannot tell a
// crashed holder from a slow one.
func TestEnsureGivesUpOnALockThatStaysHeldAndLeavesItThere(t *testing.T) {
	f := newFixture(t)
	f.adder.Wait = 100 * time.Millisecond
	if err := os.MkdirAll(f.lock, 0o755); err != nil {
		t.Fatal(err)
	}

	_, _, err := f.adder.Ensure(context.Background(), f.clone, ref, branch)

	if err == nil {
		t.Fatal("Ensure succeeded while the lock was held")
	}
	if !strings.Contains(err.Error(), f.lock) {
		t.Errorf("error = %v, want it to name the lock so the user can remove a stale one", err)
	}
	if len(f.git.calls) != 0 {
		t.Errorf("Ensure ran %v without the lock", f.git.calls)
	}
	if _, err := os.Stat(f.lock); err != nil {
		t.Errorf("Ensure removed a lock it did not take: %v", err)
	}
}

// review-code edits files for --fix only on a branch named exactly like the
// head branch, and a worktree with no files would be reviewed as a diff alone.
func TestEnsureRefusesAWorktreeThatDidNotLandOnTheHeadBranch(t *testing.T) {
	tests := []struct {
		name  string
		spoil func(*lockingGit)
	}{
		{name: "another branch", spoil: func(g *lockingGit) { g.landOn = "main" }},
		{name: "detached", spoil: func(g *lockingGit) { g.detached = true }},
		{name: "empty working tree", spoil: func(g *lockingGit) { g.empty = true }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			tt.spoil(f.git)

			if _, _, err := f.adder.Ensure(context.Background(), f.clone, ref, branch); err == nil {
				t.Fatal("Ensure accepted the worktree")
			}
		})
	}
}

// A failed Ensure leaves nothing at the final path, as a failed clone does. A
// worktree left there would hold the head branch, and git refuses a second
// worktree on the same branch.
func TestAWorktreeThatFailsVerificationIsRemoved(t *testing.T) {
	f := newFixture(t)
	f.git.empty = true

	if _, _, err := f.adder.Ensure(context.Background(), f.clone, ref, branch); err == nil {
		t.Fatal("Ensure accepted an empty working tree")
	}

	if _, err := os.Stat(f.adder.Dir(ref)); !os.IsNotExist(err) {
		t.Errorf("%s is still there after a failed verification", f.adder.Dir(ref))
	}
	if f.git.branches[f.clone+" "+branch] {
		t.Errorf("branch %s is still in %s after a failed verification", branch, f.clone)
	}
}

// The branch name reaches git on the command line.
func TestEnsureRejectsADangerousHeadBranch(t *testing.T) {
	for _, bad := range []string{"", "-rf", "a/../b", "has space"} {
		t.Run(bad, func(t *testing.T) {
			f := newFixture(t)

			if _, _, err := f.adder.Ensure(context.Background(), f.clone, ref, bad); err == nil {
				t.Errorf("Ensure accepted head branch %q", bad)
			}
			if len(f.git.calls) != 0 {
				t.Errorf("Ensure ran %v for head branch %q", f.git.calls, bad)
			}
		})
	}
}

// Supacode locks every worktree it finds, so the removal forces twice, which
// the git wrapper does. The branch goes too, or the next fix review of the
// pull request would find it and fall back to a clone.
func TestRemoveRemovesTheWorktreeAndThenItsBranch(t *testing.T) {
	f := newFixture(t)
	dir, _ := f.ensure(t)
	f.git.calls, f.git.locked = nil, nil

	if err := f.adder.Remove(context.Background(), f.clone, ref, dir, branch); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	removed := slices.Index(f.git.names(), "worktree-remove")
	deleted := slices.Index(f.git.names(), "branch-delete")
	if removed < 0 || deleted < 0 || removed > deleted {
		t.Errorf("calls = %v, want the worktree removed before its branch is deleted", f.git.calls)
	}
	if got, want := f.git.callsNamed("worktree-remove"), []string{"worktree-remove " + f.clone + " " + dir}; !slices.Equal(got, want) {
		t.Errorf("removals = %v, want %v", got, want)
	}
	if got, want := f.git.callsNamed("branch-delete"), []string{"branch-delete " + f.clone + " " + branch}; !slices.Equal(got, want) {
		t.Errorf("branch deletes = %v, want %v", got, want)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("%s is still there after Remove", dir)
	}
}

func TestRemoveHoldsReviewCodesLock(t *testing.T) {
	f := newFixture(t)
	dir, _ := f.ensure(t)
	f.git.calls, f.git.locked = nil, nil

	if err := f.adder.Remove(context.Background(), f.clone, ref, dir, branch); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	for i, held := range f.git.locked {
		if !held {
			t.Errorf("%s ran without %s held", f.git.calls[i], f.lock)
		}
	}
	if _, err := os.Stat(f.lock); !os.IsNotExist(err) {
		t.Errorf("%s is still there after Remove returned", f.lock)
	}
}

// The only other directory a record can name is the user's own clone, and
// `git worktree remove --force --force` on it would be unrecoverable.
func TestRemoveRefusesADirectoryOutsideTheWorktreesDirectory(t *testing.T) {
	f := newFixture(t)

	for _, dir := range []string{f.clone, f.paths.Worktrees, f.paths.Home} {
		if err := f.adder.Remove(context.Background(), f.clone, ref, dir, branch); err == nil {
			t.Errorf("Remove accepted %s", dir)
		}
	}
	for _, name := range []string{"worktree-remove", "branch-delete"} {
		if got := f.git.callsNamed(name); len(got) != 0 {
			t.Errorf("Remove ran %v outside the worktrees directory", got)
		}
	}
	if _, err := os.Stat(f.clone); err != nil {
		t.Errorf("the user's clone is gone: %v", err)
	}
}

// localWork fetches the head branch into the clone's shared refs before it
// reads the checkout, and that write needs the same lock.
func TestFetchHoldsReviewCodesLock(t *testing.T) {
	f := newFixture(t)
	dir, remote := f.ensure(t)
	f.git.calls, f.git.locked = nil, nil

	if err := f.adder.Fetch(context.Background(), ref, dir, remote, branch); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if got := f.git.callsNamed("fetch-branch"); len(got) != 1 || !strings.Contains(got[0], " origin "+branch) {
		t.Errorf("fetches = %v, want one of origin %s", got, branch)
	}
	for i, held := range f.git.locked {
		if !held {
			t.Errorf("%s ran without %s held", f.git.calls[i], f.lock)
		}
	}
	if _, err := os.Stat(f.lock); !os.IsNotExist(err) {
		t.Errorf("%s is still there after Fetch returned", f.lock)
	}
}
