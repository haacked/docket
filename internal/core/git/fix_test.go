package git

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/haacked/docket/internal/core/exec"
)

// Each test gets a Fake of its own. exec.Fake matches its keys as substrings in
// sorted order, so a "rev-parse" key would also answer "rev-parse HEAD".

func TestHeadReadsTheCommitTheCheckoutIsOn(t *testing.T) {
	fake := &exec.Fake{Results: map[string]exec.Result{"rev-parse HEAD": {Stdout: "0123abcd\n"}}}

	head, err := New(fake).Head(context.Background(), "/tmp/wt")
	if err != nil {
		t.Fatalf("Head: %v", err)
	}

	if head != "0123abcd" {
		t.Errorf("head = %q, want 0123abcd", head)
	}
	if line := fake.Lines()[0]; !strings.Contains(line, "-C /tmp/wt") {
		t.Errorf("command %s does not run in the checkout", line)
	}
}

func TestHeadReportsAFailure(t *testing.T) {
	fake := &exec.Fake{Errs: map[string]error{"rev-parse": errors.New("not a git repository")}}

	if _, err := New(fake).Head(context.Background(), "/tmp/wt"); err == nil {
		t.Error("Head succeeded when git failed")
	}
}

// review-code's fix pass can add a file as well as edit one. An untracked file
// is local work that deleting the checkout would lose.
func TestDirtyReadsTheShortStatus(t *testing.T) {
	tests := []struct {
		name   string
		status string
		want   bool
	}{
		{name: "nothing changed", status: "", want: false},
		{name: "only a newline", status: "\n", want: false},
		{name: "an edited file", status: " M src/app.go\n", want: true},
		{name: "an untracked file", status: "?? src/new_test.go\n", want: true},
		{name: "a staged file", status: "A  src/added.go\n", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &exec.Fake{Results: map[string]exec.Result{"status --porcelain": {Stdout: tt.status}}}

			dirty, err := New(fake).Dirty(context.Background(), "/tmp/wt")
			if err != nil {
				t.Fatalf("Dirty: %v", err)
			}
			if dirty != tt.want {
				t.Errorf("Dirty with status %q = %v, want %v", tt.status, dirty, tt.want)
			}
		})
	}
}

func TestDirtyReportsAFailure(t *testing.T) {
	fake := &exec.Fake{Errs: map[string]error{"status": errors.New("not a git repository")}}

	if _, err := New(fake).Dirty(context.Background(), "/tmp/wt"); err == nil {
		t.Error("Dirty succeeded when git failed, which reads a checkout it could not inspect as clean")
	}
}

// The branch lands in its remote-tracking ref, never in the local branch. The
// local branch is checked out in the worktree, and git refuses to fetch into a
// checked-out branch.
func TestFetchBranchUpdatesTheRemoteTrackingRef(t *testing.T) {
	fake := &exec.Fake{}

	if err := New(fake).FetchBranch(context.Background(), "/tmp/clone", "upstream", "posthog/fix-thing"); err != nil {
		t.Fatalf("FetchBranch: %v", err)
	}

	line := fake.Lines()[0]
	for _, want := range []string{
		"-C /tmp/clone",
		"fetch",
		"upstream",
		"+refs/heads/posthog/fix-thing:refs/remotes/upstream/posthog/fix-thing",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("command %s is missing %q", line, want)
		}
	}
	if strings.Contains(line, ":refs/heads/") {
		t.Errorf("command %s fetches into a local branch", line)
	}
}

// docket's fetch authenticates through gh the way the tier-2 clone's fetch does.
func TestFetchBranchResetsTheCredentialHelperBeforeSettingIt(t *testing.T) {
	fake := &exec.Fake{}

	if err := New(fake).FetchBranch(context.Background(), "/tmp/clone", "origin", "topic"); err != nil {
		t.Fatalf("FetchBranch: %v", err)
	}

	line := fake.Lines()[0]
	reset := strings.Index(line, "credential.helper= ")
	helper := strings.Index(line, "credential.helper=!gh")
	if reset < 0 || helper < 0 || reset > helper {
		t.Errorf("command %s does not reset credential.helper before setting it to gh", line)
	}
}

// PostHog deletes a head branch once its pull request merges, so this fetch
// fails from then on. The caller has to see the failure to fall back.
func TestFetchBranchReportsAFailure(t *testing.T) {
	fake := &exec.Fake{Errs: map[string]error{"fetch": errors.New("couldn't find remote ref refs/heads/topic")}}

	if err := New(fake).FetchBranch(context.Background(), "/tmp/clone", "origin", "topic"); err == nil {
		t.Error("FetchBranch succeeded when git failed")
	}
}

func TestAheadCountsTheCommitsTheUpstreamDoesNotHave(t *testing.T) {
	fake := &exec.Fake{Results: map[string]exec.Result{"rev-list --count": {Stdout: "3\n"}}}

	ahead, err := New(fake).Ahead(context.Background(), "/tmp/wt", "origin/topic")
	if err != nil {
		t.Fatalf("Ahead: %v", err)
	}

	if ahead != 3 {
		t.Errorf("ahead = %d, want 3", ahead)
	}
	line := fake.Lines()[0]
	for _, want := range []string{"-C /tmp/wt", "rev-list --count", "origin/topic..HEAD"} {
		if !strings.Contains(line, want) {
			t.Errorf("command %s is missing %q", line, want)
		}
	}
}

func TestAheadOfNothingIsZero(t *testing.T) {
	fake := &exec.Fake{Results: map[string]exec.Result{"rev-list --count": {Stdout: "0\n"}}}

	ahead, err := New(fake).Ahead(context.Background(), "/tmp/wt", "origin/topic")
	if err != nil {
		t.Fatalf("Ahead: %v", err)
	}
	if ahead != 0 {
		t.Errorf("ahead = %d, want 0", ahead)
	}
}

// A count docket cannot read must not pass for zero, because zero lets cleanup
// delete the checkout.
func TestAheadRefusesOutputThatIsNotACount(t *testing.T) {
	for name, fake := range map[string]*exec.Fake{
		"empty output":  {Results: map[string]exec.Result{"rev-list": {Stdout: ""}}},
		"garbage":       {Results: map[string]exec.Result{"rev-list": {Stdout: "fatal: bad revision\n"}}},
		"a failed call": {Errs: map[string]error{"rev-list": errors.New("unknown revision origin/topic")}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := New(fake).Ahead(context.Background(), "/tmp/wt", "origin/topic"); err == nil {
				t.Error("Ahead succeeded, want an error")
			}
		})
	}
}

// for-each-ref exits zero whether or not the ref is there, so only the output
// says which.
func TestBranchExistsReadsTheOutputNotTheExitCode(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
		want   bool
	}{
		{name: "missing branch", stdout: "", want: false},
		{name: "missing branch with a newline", stdout: "\n", want: false},
		{name: "present branch", stdout: "refs/heads/posthog/fix-thing\n", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &exec.Fake{Results: map[string]exec.Result{"for-each-ref": {Stdout: tt.stdout}}}

			exists, err := New(fake).BranchExists(context.Background(), "/home/me/dev/posthog", "posthog/fix-thing")
			if err != nil {
				t.Fatalf("BranchExists: %v", err)
			}
			if exists != tt.want {
				t.Errorf("BranchExists with output %q = %v, want %v", tt.stdout, exists, tt.want)
			}
		})
	}
}

// A bare for-each-ref pattern matches by prefix, so the ref has to be spelled
// out in full for a branch named like another's parent directory.
func TestBranchExistsAsksForTheLocalBranchByItsFullRef(t *testing.T) {
	fake := &exec.Fake{}

	if _, err := New(fake).BranchExists(context.Background(), "/home/me/dev/posthog", "posthog/fix-thing"); err != nil {
		t.Fatalf("BranchExists: %v", err)
	}

	line := fake.Lines()[0]
	for _, want := range []string{"-C /home/me/dev/posthog", "for-each-ref", "refs/heads/posthog/fix-thing"} {
		if !strings.Contains(line, want) {
			t.Errorf("command %s is missing %q", line, want)
		}
	}
}

func TestSetUpstreamPointsTheBranchAtTheRemoteTrackingRef(t *testing.T) {
	fake := &exec.Fake{}

	if err := New(fake).SetUpstream(context.Background(), "/tmp/clone", "topic", "origin/topic"); err != nil {
		t.Fatalf("SetUpstream: %v", err)
	}

	line := fake.Lines()[0]
	for _, want := range []string{"-C /tmp/clone", "origin/topic", "topic"} {
		if !strings.Contains(line, want) {
			t.Errorf("command %s is missing %q", line, want)
		}
	}
}

// A push from the tier-2 clone runs in the user's session with no -c flags, so
// the helper has to be in the clone's own config. The first value empties the
// list the global config contributes, because git does not try the next helper
// after one fails.
func TestSetCredentialHelperWritesTheGhHelperToTheLocalConfig(t *testing.T) {
	fake := &exec.Fake{}

	if err := New(fake).SetCredentialHelper(context.Background(), "/tmp/clone"); err != nil {
		t.Fatalf("SetCredentialHelper: %v", err)
	}

	lines := fake.Lines()
	joined := strings.Join(lines, "\n")
	for _, line := range lines {
		if !strings.Contains(line, "-C /tmp/clone") {
			t.Errorf("command %s does not run in the clone", line)
		}
		if strings.Contains(line, "--global") || strings.Contains(line, "--system") {
			t.Errorf("command %s writes beyond the clone's own config", line)
		}
	}
	gh := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, "!gh auth git-credential") })
	if gh < 0 {
		t.Fatalf("no command sets the gh helper:\n%s", joined)
	}
	reset := slices.IndexFunc(lines, func(l string) bool {
		return strings.Contains(l, "credential.helper") && !strings.Contains(l, "!gh auth git-credential")
	})
	if reset < 0 || reset > gh {
		t.Errorf("the helper list is not emptied before gh is added:\n%s", joined)
	}
}

// Remotes reads the remote URLs from the repository's config, one line per
// remote, rather than from `git remote -v`, which prints each remote twice.
func TestRemotesReportsEachRemoteWithItsURL(t *testing.T) {
	out := "remote.origin.url https://github.com/PostHog/posthog.git\n" +
		"remote.fork.url git@github.com:haacked/posthog.git\n"
	fake := &exec.Fake{Results: map[string]exec.Result{"--get-regexp": {Stdout: out}}}

	remotes, err := New(fake).Remotes(context.Background(), "/home/me/dev/posthog")
	if err != nil {
		t.Fatalf("Remotes: %v", err)
	}

	want := []Remote{
		{Name: "origin", URL: "https://github.com/PostHog/posthog.git"},
		{Name: "fork", URL: "git@github.com:haacked/posthog.git"},
	}
	if !slices.Equal(remotes, want) {
		t.Errorf("remotes = %+v, want %+v", remotes, want)
	}
	if line := fake.Lines()[0]; !strings.Contains(line, "-C /home/me/dev/posthog") {
		t.Errorf("command %s does not run in the clone", line)
	}
}

// A remote name can hold a dot, and only the last .url ends the key.
func TestRemotesKeepsADottedRemoteName(t *testing.T) {
	fake := &exec.Fake{Results: map[string]exec.Result{"--get-regexp": {Stdout: "remote.my.fork.url https://github.com/haacked/posthog\n"}}}

	remotes, err := New(fake).Remotes(context.Background(), "/home/me/dev/posthog")
	if err != nil {
		t.Fatalf("Remotes: %v", err)
	}

	if want := []Remote{{Name: "my.fork", URL: "https://github.com/haacked/posthog"}}; !slices.Equal(remotes, want) {
		t.Errorf("remotes = %+v, want %+v", remotes, want)
	}
}

// git config exits 1 when no key matches, which is how a clone with no remotes
// answers. That is an empty list, not a failure.
func TestRemotesOfARepositoryWithNone(t *testing.T) {
	fake := &exec.Fake{
		Results: map[string]exec.Result{"--get-regexp": {ExitCode: 1}},
		Errs:    map[string]error{"--get-regexp": errors.New("git exited 1")},
	}

	remotes, err := New(fake).Remotes(context.Background(), "/home/me/dev/posthog")
	if err != nil {
		t.Fatalf("Remotes: %v", err)
	}
	if len(remotes) != 0 {
		t.Errorf("remotes = %+v, want none", remotes)
	}
}

func TestRemotesReportsAFailure(t *testing.T) {
	fake := &exec.Fake{
		Results: map[string]exec.Result{"--get-regexp": {ExitCode: 128, Stderr: "not a git repository"}},
		Errs:    map[string]error{"--get-regexp": errors.New("git exited 128")},
	}

	if _, err := New(fake).Remotes(context.Background(), "/home/me/dev/posthog"); err == nil {
		t.Error("Remotes succeeded when git failed, which reads as a clone with no remote")
	}
}

// The user's clone may fetch over https or ssh, with or without .git, and in
// whatever case the user typed when cloning. A fork remote named origin must
// not win over the base repository's remote.
func TestRemoteForFindsTheRemoteThatPointsAtThePullRequestsRepository(t *testing.T) {
	tests := []struct {
		name     string
		remotes  []Remote
		want     string
		wantFind bool
	}{
		{name: "https with .git", remotes: []Remote{{Name: "origin", URL: "https://github.com/PostHog/posthog.git"}}, want: "origin", wantFind: true},
		{name: "https without .git", remotes: []Remote{{Name: "origin", URL: "https://github.com/PostHog/posthog"}}, want: "origin", wantFind: true},
		{name: "scp-style ssh", remotes: []Remote{{Name: "origin", URL: "git@github.com:PostHog/posthog.git"}}, want: "origin", wantFind: true},
		{name: "ssh url", remotes: []Remote{{Name: "origin", URL: "ssh://git@github.com/PostHog/posthog.git"}}, want: "origin", wantFind: true},
		{name: "another case", remotes: []Remote{{Name: "origin", URL: "https://github.com/posthog/PostHog.git"}}, want: "origin", wantFind: true},
		{
			name: "fork as origin and the base as upstream",
			remotes: []Remote{
				{Name: "origin", URL: "git@github.com:haacked/posthog.git"},
				{Name: "upstream", URL: "git@github.com:PostHog/posthog.git"},
			},
			want:     "upstream",
			wantFind: true,
		},
		{name: "only a fork", remotes: []Remote{{Name: "origin", URL: "git@github.com:haacked/posthog.git"}}, wantFind: false},
		{name: "a repository whose name starts the same", remotes: []Remote{{Name: "origin", URL: "https://github.com/PostHog/posthog-js.git"}}, wantFind: false},
		{name: "another host", remotes: []Remote{{Name: "origin", URL: "https://gitlab.com/PostHog/posthog.git"}}, wantFind: false},
		{name: "no remotes", remotes: nil, wantFind: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := RemoteFor(tt.remotes, "PostHog", "posthog")

			if found != tt.wantFind {
				t.Fatalf("RemoteFor(%+v) found = %v, want %v", tt.remotes, found, tt.wantFind)
			}
			if got != tt.want {
				t.Errorf("RemoteFor(%+v) = %q, want %q", tt.remotes, got, tt.want)
			}
		})
	}
}

// --track makes the new branch's upstream the remote branch, so `git push` in
// the session needs no arguments.
func TestWorktreeAddStartsATrackingBranchAtTheRemoteBranch(t *testing.T) {
	fake := &exec.Fake{}

	err := New(fake).WorktreeAdd(context.Background(), "/home/me/dev/posthog", "/home/me/.docket/worktrees/PostHog/posthog/pr-9", "posthog/fix-thing", "origin/posthog/fix-thing")
	if err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}

	line := fake.Lines()[0]
	for _, want := range []string{
		"-C /home/me/dev/posthog",
		"worktree add",
		"--track",
		"-b posthog/fix-thing",
		"/home/me/.docket/worktrees/PostHog/posthog/pr-9",
		"origin/posthog/fix-thing",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("command %s is missing %q", line, want)
		}
	}
	dir := strings.Index(line, "/home/me/.docket/worktrees/PostHog/posthog/pr-9")
	start := strings.LastIndex(line, "origin/posthog/fix-thing")
	if dir < 0 || start < dir {
		t.Errorf("command %s does not name the directory before the start point", line)
	}
}

// Supacode locks every worktree it finds, and one --force does not remove a
// locked worktree.
func TestWorktreeRemoveForcesTwiceToRemoveALockedWorktree(t *testing.T) {
	fake := &exec.Fake{}

	if err := New(fake).WorktreeRemove(context.Background(), "/home/me/dev/posthog", "/home/me/.docket/worktrees/PostHog/posthog/pr-9"); err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}

	line := fake.Lines()[0]
	for _, want := range []string{"-C /home/me/dev/posthog", "worktree remove", "--force --force", "/home/me/.docket/worktrees/PostHog/posthog/pr-9"} {
		if !strings.Contains(line, want) {
			t.Errorf("command %s is missing %q", line, want)
		}
	}
}

// The branch was pushed or its checkout was found clean before the removal, so
// it may be unmerged locally and still be safe to delete.
func TestBranchDeleteDeletesAnUnmergedBranch(t *testing.T) {
	fake := &exec.Fake{}

	if err := New(fake).BranchDelete(context.Background(), "/home/me/dev/posthog", "posthog/fix-thing"); err != nil {
		t.Fatalf("BranchDelete: %v", err)
	}

	line := fake.Lines()[0]
	for _, want := range []string{"-C /home/me/dev/posthog", "branch -D posthog/fix-thing"} {
		if !strings.Contains(line, want) {
			t.Errorf("command %s is missing %q", line, want)
		}
	}
}

func TestTheWritesReportWhatGitRefused(t *testing.T) {
	g := New(&exec.Fake{Errs: map[string]error{"git": errors.New("exit status 128")}})
	ctx := context.Background()

	for name, err := range map[string]error{
		"WorktreeAdd":         g.WorktreeAdd(ctx, "/base", "/dir", "topic", "origin/topic"),
		"WorktreeRemove":      g.WorktreeRemove(ctx, "/base", "/dir"),
		"BranchDelete":        g.BranchDelete(ctx, "/base", "topic"),
		"SetUpstream":         g.SetUpstream(ctx, "/dir", "topic", "origin/topic"),
		"SetCredentialHelper": g.SetCredentialHelper(ctx, "/dir"),
	} {
		if err == nil {
			t.Errorf("%s succeeded when git failed", name)
		}
	}
}
