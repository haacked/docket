package clone

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/haacked/docket/internal/core/config"
	"github.com/haacked/docket/internal/core/gh"
	"github.com/haacked/docket/internal/core/pr"
)

// fakeGit records the commands a clone would run and keeps just enough state for
// the checks the clone makes afterwards.
type fakeGit struct {
	calls      []string
	branch     map[string]string
	files      map[string]bool
	failAt     string
	repos      map[string]bool
	emptyDir   bool
	checkedOut string
}

func newFakeGit() *fakeGit {
	return &fakeGit{branch: map[string]string{}, files: map[string]bool{}, repos: map[string]bool{}}
}

func (f *fakeGit) record(name string) error {
	f.calls = append(f.calls, name)
	if f.failAt == name {
		return errors.New("git failed: " + name)
	}
	return nil
}

func (f *fakeGit) IsRepo(_ context.Context, dir string) bool { return f.repos[dir] }

func (f *fakeGit) Init(_ context.Context, dir string) error {
	if err := f.record("init"); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f.repos[dir] = true
	return nil
}

func (f *fakeGit) RemoteAdd(_ context.Context, _, _, _ string) error { return f.record("remote") }

func (f *fakeGit) SymbolicRef(_ context.Context, _, _, _ string) error {
	return f.record("symbolic-ref")
}

func (f *fakeGit) FetchPR(_ context.Context, dir, _ string, _ int, branch string, _ int) error {
	if err := f.record("fetch"); err != nil {
		return err
	}
	f.files[dir] = true
	_ = branch
	return nil
}

func (f *fakeGit) Checkout(_ context.Context, dir, branch string) error {
	if err := f.record("checkout"); err != nil {
		return err
	}
	f.branch[dir] = branch
	f.checkedOut = branch
	return nil
}

func (f *fakeGit) ResetHard(_ context.Context, _, _ string) error { return f.record("reset") }

func (f *fakeGit) CurrentBranch(_ context.Context, dir string) (string, error) {
	if err := f.record("current-branch"); err != nil {
		return "", err
	}
	return f.branch[dir], nil
}

func (f *fakeGit) WorkTreeEmpty(_ context.Context, dir string) (bool, error) {
	if err := f.record("ls-files"); err != nil {
		return false, err
	}
	if f.emptyDir {
		return true, nil
	}
	return !f.files[dir], nil
}

func setup(t *testing.T) (*Cloner, *fakeGit, config.Paths) {
	t.Helper()
	home := t.TempDir()
	paths, err := config.NewPaths(home)
	if err != nil {
		t.Fatalf("paths: %v", err)
	}
	if err := paths.EnsureDirs(); err != nil {
		t.Fatalf("dirs: %v", err)
	}
	g := newFakeGit()
	return New(g, paths), g, paths
}

var ref = pr.Ref{Org: "haacked", Repo: "docket", Number: 7}

func info(branch string) gh.PRInfo {
	return gh.PRInfo{Number: 7, Title: "A change", HeadRefName: branch}
}

func TestEnsureLandsOnTheHeadBranch(t *testing.T) {
	cloner, g, paths := setup(t)

	dir, err := cloner.Ensure(context.Background(), ref, info("haacked/some-change"))
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	want := paths.CloneDir("haacked", "docket", 7)
	if dir != want {
		t.Errorf("dir = %q, want %q", dir, want)
	}
	if g.checkedOut != "haacked/some-change" {
		t.Errorf("checked out %q, want the head branch", g.checkedOut)
	}
	if _, err := os.Stat(want + ".tmp"); !os.IsNotExist(err) {
		t.Error("the temporary directory is still there")
	}
}

func TestEnsureParksHeadBeforeFetching(t *testing.T) {
	cloner, g, _ := setup(t)

	if _, err := cloner.Ensure(context.Background(), ref, info("main")); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	order := strings.Join(g.calls, ",")
	parked := strings.Index(order, "symbolic-ref")
	fetched := strings.Index(order, "fetch")
	if parked < 0 || fetched < 0 || parked > fetched {
		t.Errorf("HEAD must be parked before the fetch, got %v", g.calls)
	}
}

func TestAFailedCloneLeavesNothingAtTheFinalPath(t *testing.T) {
	for _, step := range []string{"init", "remote", "symbolic-ref", "fetch", "checkout"} {
		t.Run(step, func(t *testing.T) {
			cloner, g, paths := setup(t)
			g.failAt = step

			if _, err := cloner.Ensure(context.Background(), ref, info("topic")); err == nil {
				t.Fatal("Ensure succeeded, want an error")
			}

			final := paths.CloneDir("haacked", "docket", 7)
			if _, err := os.Stat(final); !os.IsNotExist(err) {
				t.Errorf("%s exists after a failed clone", final)
			}
			if _, err := os.Stat(final + ".tmp"); !os.IsNotExist(err) {
				t.Errorf("%s.tmp survived a failed clone", final)
			}
		})
	}
}

func TestEnsureRejectsAnEmptyWorkingTree(t *testing.T) {
	cloner, g, _ := setup(t)
	g.emptyDir = true

	_, err := cloner.Ensure(context.Background(), ref, info("topic"))
	if err == nil || !strings.Contains(err.Error(), "empty working tree") {
		t.Errorf("error = %v, want one about an empty working tree", err)
	}
}

func TestEnsureRejectsADangerousHeadBranch(t *testing.T) {
	for _, branch := range []string{"", "-rf", "a/../b", "has space"} {
		t.Run(branch, func(t *testing.T) {
			cloner, _, _ := setup(t)
			if _, err := cloner.Ensure(context.Background(), ref, info(branch)); err == nil {
				t.Errorf("Ensure accepted head branch %q", branch)
			}
		})
	}
}

func TestEnsureRefreshesACloneAlreadyOnTheHeadBranch(t *testing.T) {
	cloner, g, paths := setup(t)
	final := paths.CloneDir("haacked", "docket", 7)
	if err := os.MkdirAll(final, 0o755); err != nil {
		t.Fatal(err)
	}
	g.repos[final] = true
	g.branch[final] = "topic"
	g.files[final] = true

	if _, err := cloner.Ensure(context.Background(), ref, info("topic")); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	if strings.Contains(strings.Join(g.calls, ","), "init") {
		t.Errorf("an existing clone was rebuilt: %v", g.calls)
	}
	if !strings.Contains(strings.Join(g.calls, ","), "reset") {
		t.Errorf("an existing clone was not reset: %v", g.calls)
	}
}

func TestRemoveRefusesAnythingOutsideTheClonesDirectory(t *testing.T) {
	cloner, _, paths := setup(t)

	outside := filepath.Join(t.TempDir(), "someones-repo")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := cloner.Remove(outside); err == nil {
		t.Error("Remove accepted a path outside the clones directory")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("Remove deleted %s anyway", outside)
	}
	if err := cloner.Remove(paths.Clones); err == nil {
		t.Error("Remove accepted the clones directory itself")
	}
}

func TestRemoveDeletesAClone(t *testing.T) {
	cloner, _, paths := setup(t)
	dir := paths.CloneDir("haacked", "docket", 7)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := cloner.Remove(dir); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("%s is still there", dir)
	}
}
