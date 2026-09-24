package exec

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestStringShowsDirectoryScrubbedEnvironmentAndQuoting(t *testing.T) {
	spec := CommandSpec{
		Path:  "codex",
		Args:  []string{"-C", "/tmp/x", "$review-code https://example.com/pull/1 --draft"},
		Dir:   "/tmp/x",
		Unset: []string{"CLAUDECODE", "CLAUDE_CONFIG_DIR"},
	}

	got := spec.String()
	want := "[/tmp/x] env -u CLAUDECODE -u CLAUDE_CONFIG_DIR codex -C /tmp/x '$review-code https://example.com/pull/1 --draft'"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestEnvRemovesOnlyTheNamedVariables(t *testing.T) {
	spec := CommandSpec{Unset: []string{"CLAUDECODE"}}

	got := spec.Env([]string{"PATH=/bin", "CLAUDECODE=1", "CLAUDECODE_EXTRA=1"})

	if len(got) != 2 {
		t.Fatalf("env = %v, want two entries", got)
	}
	for _, kv := range got {
		if kv == "CLAUDECODE=1" {
			t.Error("CLAUDECODE survived")
		}
	}
}

func TestEnvLeavesTheEnvironmentAloneWithNothingToUnset(t *testing.T) {
	environ := []string{"PATH=/bin"}
	if got := (CommandSpec{}).Env(environ); len(got) != 1 || got[0] != "PATH=/bin" {
		t.Errorf("env = %v, want it unchanged", got)
	}
}

func TestRealRunsACommand(t *testing.T) {
	res, err := Real{}.Run(context.Background(), CommandSpec{Path: "echo", Args: []string{"hello"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(res.Stdout) != "hello" {
		t.Errorf("stdout = %q", res.Stdout)
	}
}

func TestRealStartDoesNotWaitForTheCommand(t *testing.T) {
	began := time.Now()

	if err := (Real{}).Start(CommandSpec{Path: "sleep", Args: []string{"5"}}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if waited := time.Since(began); waited > 2*time.Second {
		t.Errorf("Start took %s, want it to return before the command ends", waited)
	}
}

func TestRealStartReportsAMissingCommand(t *testing.T) {
	if err := (Real{}).Start(CommandSpec{Path: "docket-no-such-command"}); err == nil {
		t.Error("Start succeeded, want the missing command reported")
	}
}

func TestRealReportsTheExitCode(t *testing.T) {
	res, err := Real{}.Run(context.Background(), CommandSpec{Path: "false"})
	if err == nil {
		t.Fatal("Run succeeded, want a failure")
	}
	if res.ExitCode != 1 {
		t.Errorf("exit code = %d, want 1", res.ExitCode)
	}
}

func TestFakeMatchesOnTheRenderedCommand(t *testing.T) {
	fake := &Fake{Results: map[string]Result{"pr view": {Stdout: "{}"}}, Default: Result{Stdout: "fallback"}}

	res, _ := fake.Run(context.Background(), CommandSpec{Path: "gh", Args: []string{"pr", "view", "7"}})
	if res.Stdout != "{}" {
		t.Errorf("stdout = %q, want the canned result", res.Stdout)
	}

	res, _ = fake.Run(context.Background(), CommandSpec{Path: "gh", Args: []string{"issue", "list"}})
	if res.Stdout != "fallback" {
		t.Errorf("stdout = %q, want the default", res.Stdout)
	}
	if len(fake.Lines()) != 2 {
		t.Errorf("recorded %d calls, want 2", len(fake.Lines()))
	}
}
