package engine

import (
	"slices"
	"strings"
	"testing"

	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/review"
)

func record() review.Record {
	return review.Record{
		Ref:       pr.Ref{Org: "haacked", Repo: "docket", Number: 7},
		URL:       "https://github.com/haacked/docket/pull/7",
		Dir:       "/tmp/clone",
		SessionID: "1ce5f0ad-0000-4000-8000-000000000001",
		Engine:    "claude",
	}
}

func TestStartRunsTheReviewAsADraftInTheRecordsDirectory(t *testing.T) {
	spec := Claude{}.Start(record(), Paths{Grant: grants})

	if spec.Path != "claude" {
		t.Errorf("path = %q", spec.Path)
	}
	if spec.Dir != "/tmp/clone" {
		t.Errorf("dir = %q", spec.Dir)
	}
	line := spec.String()
	for _, want := range []string{
		"--session-id 1ce5f0ad-0000-4000-8000-000000000001",
		"/review-code https://github.com/haacked/docket/pull/7 --draft",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("command %s is missing %q", line, want)
		}
	}
}

func TestStartWithoutAnIdStillRunsTheReview(t *testing.T) {
	rec := record()
	rec.SessionID = ""

	line := Claude{}.Start(rec, Paths{}).String()
	if strings.Contains(line, "--session-id") {
		t.Errorf("command = %s, want no empty session id", line)
	}
	if !strings.Contains(line, "--draft") {
		t.Errorf("command = %s, want the draft review", line)
	}
}

func TestResumeReopensTheStoredSession(t *testing.T) {
	spec, ok := Claude{}.Resume(record(), Paths{})
	if !ok {
		t.Fatal("Resume refused a record with a session id")
	}
	if want := "[/tmp/clone] env -u CLAUDE_CONFIG_DIR claude --resume 1ce5f0ad-0000-4000-8000-000000000001 --setting-sources user"; spec.String() != want {
		t.Errorf("got  %s\nwant %s", spec, want)
	}
}

// A tier-2 clone is the pull request's own repository. Its settings would run
// the hooks its author committed. attach joins a session that already loaded
// its settings, so it carries no flag.
func TestEverySessionLoadsOnlyTheUsersSettings(t *testing.T) {
	rec := record()
	resume, _ := Claude{}.Resume(rec, Paths{})
	for name, spec := range map[string]exec.CommandSpec{
		"start":      Claude{}.Start(rec, Paths{}),
		"ask":        Claude{}.Ask(rec, Paths{}),
		"resume":     resume,
		"background": Claude{}.StartBackground(rec, Paths{}),
		"trust":      Claude{}.TrustSpec(rec.Dir, Paths{}),
	} {
		i := slices.Index(spec.Args, "--setting-sources")
		if i < 0 || i+1 >= len(spec.Args) || spec.Args[i+1] != "user" {
			t.Errorf("%s command %s loads the working directory's settings", name, spec)
		}
	}
}

func TestResumeRefusesARecordWithNoSession(t *testing.T) {
	rec := record()
	rec.SessionID = ""

	eng := Claude{}
	if _, ok := eng.Resume(rec, Paths{}); ok {
		t.Error("Resume accepted a record with no session to resume")
	}
}

func TestNewSessionIDIsFreshEveryTime(t *testing.T) {
	eng := Claude{}
	first, second := eng.NewSessionID(), eng.NewSessionID()
	if first == "" || first == second {
		t.Errorf("session ids %q and %q should both be set and differ", first, second)
	}
}

func TestForKnowsTheEnginesAndSaysWhatItDoesNot(t *testing.T) {
	for _, name := range []string{"claude", "codex"} {
		if _, err := For(name); err != nil {
			t.Errorf("For(%q): %v", name, err)
		}
	}
	for _, name := range []string{"", "aider"} {
		if _, err := For(name); err == nil {
			t.Errorf("For(%q) succeeded, want an error", name)
		}
	}
	if names := Names(); !slices.Equal(names, []string{"claude", "codex"}) {
		t.Errorf("Names() = %v", names)
	}
}
