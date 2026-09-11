package git

import (
	"context"
	"strings"
	"testing"

	"github.com/haacked/docket/internal/core/exec"
)

func TestFetchPRAsksForJustThePullRequestHead(t *testing.T) {
	fake := &exec.Fake{}

	if err := New(fake).FetchPR(context.Background(), "/tmp/clone", "origin", 7, "haacked/a-thing", 1); err != nil {
		t.Fatalf("FetchPR: %v", err)
	}

	line := fake.Lines()[0]
	for _, want := range []string{
		"-C /tmp/clone",
		"credential.helper=!gh auth git-credential",
		"--depth 1",
		"+pull/7/head:refs/heads/haacked/a-thing",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("command %s is missing %q", line, want)
		}
	}
}

func TestFetchPRLeavesOutTheDepthWhenItIsNotLimited(t *testing.T) {
	fake := &exec.Fake{}

	if err := New(fake).FetchPR(context.Background(), "/tmp/clone", "origin", 7, "topic", 0); err != nil {
		t.Fatalf("FetchPR: %v", err)
	}
	if strings.Contains(fake.Lines()[0], "--depth") {
		t.Errorf("command = %s, want no depth limit", fake.Lines()[0])
	}
}

func TestReadsReportWhatGitSaid(t *testing.T) {
	fake := &exec.Fake{Results: map[string]exec.Result{
		"branch --show-current": {Stdout: "topic\n"},
		"ls-files":              {Stdout: "README.md\n"},
	}}
	g := New(fake)

	branch, err := g.CurrentBranch(context.Background(), "/tmp/clone")
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}
	if branch != "topic" {
		t.Errorf("branch = %q", branch)
	}

	empty, err := g.WorkTreeEmpty(context.Background(), "/tmp/clone")
	if err != nil {
		t.Fatalf("WorkTreeEmpty: %v", err)
	}
	if empty {
		t.Error("a working tree with a file is not empty")
	}
}

func TestWorkTreeEmptyOnACheckoutThatLandedNothing(t *testing.T) {
	fake := &exec.Fake{Results: map[string]exec.Result{"ls-files": {Stdout: "\n"}}}

	empty, err := New(fake).WorkTreeEmpty(context.Background(), "/tmp/clone")
	if err != nil {
		t.Fatalf("WorkTreeEmpty: %v", err)
	}
	if !empty {
		t.Error("no tracked files means an empty working tree")
	}
}

func TestIsRepoSaysNoForAPathThatIsNotThere(t *testing.T) {
	if New(&exec.Fake{}).IsRepo(context.Background(), "/no/such/directory") {
		t.Error("IsRepo accepted a path that does not exist")
	}
	if New(&exec.Fake{}).IsRepo(context.Background(), "") {
		t.Error("IsRepo accepted an empty path")
	}
}

func TestIsRepoSaysYesOnlyWhenGitAgrees(t *testing.T) {
	dir := t.TempDir()

	if New(&exec.Fake{}).IsRepo(context.Background(), dir) {
		t.Error("IsRepo accepted a directory git reported nothing for")
	}

	fake := &exec.Fake{Results: map[string]exec.Result{"rev-parse": {Stdout: ".git\n"}}}
	if !New(fake).IsRepo(context.Background(), dir) {
		t.Error("IsRepo rejected a directory git called a repository")
	}
}
