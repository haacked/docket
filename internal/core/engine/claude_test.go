package engine

import (
	"strings"
	"testing"

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
	spec := Claude{}.Start(record(), "/opt/review-code")

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
	if len(spec.Unset) != 0 {
		t.Errorf("claude needs nothing unset, got %v", spec.Unset)
	}
}

func TestStartWithoutAnIdStillRunsTheReview(t *testing.T) {
	rec := record()
	rec.SessionID = ""

	line := Claude{}.Start(rec, "").String()
	if strings.Contains(line, "--session-id") {
		t.Errorf("command = %s, want no empty session id", line)
	}
	if !strings.Contains(line, "--draft") {
		t.Errorf("command = %s, want the draft review", line)
	}
}

func TestResumeReopensTheStoredSession(t *testing.T) {
	spec, ok := Claude{}.Resume(record(), "")
	if !ok {
		t.Fatal("Resume refused a record with a session id")
	}
	if want := "[/tmp/clone] claude --resume 1ce5f0ad-0000-4000-8000-000000000001"; spec.String() != want {
		t.Errorf("got  %s\nwant %s", spec, want)
	}
}

func TestResumeRefusesARecordWithNoSession(t *testing.T) {
	rec := record()
	rec.SessionID = ""

	eng := Claude{}
	if _, ok := eng.Resume(rec, ""); ok {
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

func TestForKnowsClaudeAndSaysWhatItDoesNot(t *testing.T) {
	if _, err := For("claude"); err != nil {
		t.Errorf("For(claude): %v", err)
	}
	for _, name := range []string{"", "codex", "aider"} {
		if _, err := For(name); err == nil {
			t.Errorf("For(%q) succeeded, want an error", name)
		}
	}
	if names := Names(); len(names) != 1 || names[0] != "claude" {
		t.Errorf("Names() = %v, want just claude while codex is unwired", names)
	}
}
