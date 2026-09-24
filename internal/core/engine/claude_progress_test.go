package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/review"
)

// untrustedStderr is what `claude --bg` 2.1.281 prints when it refuses a
// directory nobody has trusted.
const untrustedStderr = "Workspace not trusted. Run `claude` in /tmp/clone once and accept the trust prompt, then retry.\n"

func TestUntrustedRecognizesTheRefusal(t *testing.T) {
	if !(Claude{}).Untrusted(exec.Result{Stderr: untrustedStderr, ExitCode: 1}) {
		t.Error("the trust refusal was not recognized")
	}
}

// Any other failure has a cause the trust prompt does not fix, so offering the
// prompt would send the user through it for nothing.
func TestUntrustedIgnoresOtherFailures(t *testing.T) {
	if (Claude{}).Untrusted(exec.Result{Stderr: "Not logged in. Run /login\n", ExitCode: 1}) {
		t.Error("a login failure read as a trust refusal")
	}
}

// claude shows its trust prompt before a session starts. /exit ends the session
// as soon as the user answers the prompt.
func TestTrustRunsClaudeInTheDirectoryAndExits(t *testing.T) {
	spec := Claude{}.TrustSpec("/tmp/clone")

	if spec.Path != "claude" || spec.Dir != "/tmp/clone" {
		t.Errorf("spec = %s, want claude in the directory", spec)
	}
	if len(spec.Args) != 1 || spec.Args[0] != "/exit" {
		t.Errorf("args = %q, want only /exit", spec.Args)
	}
}

func writeJobState(t *testing.T, jobs, id, body string) {
	t.Helper()
	dir := filepath.Join(jobs, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestProgressReadsWhatClaudeSaysTheSessionIsDoing(t *testing.T) {
	jobs := t.TempDir()
	writeJobState(t, jobs, "6d681a76", `{
		"state": "working", "tempo": "active",
		"detail": "7 review agents dispatched; loading synthesis",
		"needs": null,
		"fan": [
			{"id": "a1", "kind": "agent", "label": "Security", "doneAt": null},
			{"id": "a2", "kind": "agent", "label": "Tests"},
			{"id": "a3", "kind": "agent", "label": "Context", "doneAt": 1790270649813}
		],
		"updatedAt": "2026-09-24T17:27:18.813Z"
	}`)

	got := Claude{}.Progress("6d681a76", Paths{ClaudeJobs: jobs})

	if got.Detail != "7 review agents dispatched; loading synthesis" {
		t.Errorf("detail = %q", got.Detail)
	}
	if got.Needs != "" {
		t.Errorf("needs = %q, want nothing from a session working on its own", got.Needs)
	}
	if !got.Active {
		t.Error("a session claude calls active does not read as active")
	}
	if got.Agents != 2 {
		t.Errorf("agents = %d, want the 2 that have not finished", got.Agents)
	}
	if want := time.Date(2026, 9, 24, 17, 27, 18, 813e6, time.UTC); !got.UpdatedAt.Equal(want) {
		t.Errorf("updated at = %v, want %v", got.UpdatedAt, want)
	}
}

// The need is claude's own words. Whether the session is waiting at all is the
// listing's to say, so a blocked session with no named need reports none here.
func TestProgressReportsWhatClaudeSaysASessionNeeds(t *testing.T) {
	jobs := t.TempDir()
	writeJobState(t, jobs, "named", `{"state": "working", "tempo": "blocked", "needs": "permission to run gh"}`)
	writeJobState(t, jobs, "unnamed", `{"state": "working", "tempo": "blocked", "needs": null}`)

	named := (Claude{}).Progress("named", Paths{ClaudeJobs: jobs})
	if named.Needs != "permission to run gh" {
		t.Errorf("needs = %q, want what claude named", named.Needs)
	}
	if named.Active {
		t.Error("a blocked session reads as active")
	}
	if got := (Claude{}).Progress("unnamed", Paths{ClaudeJobs: jobs}).Needs; got != "" {
		t.Errorf("needs = %q, want none when claude named none", got)
	}
}

// The file is claude's own and undocumented. When it is missing or unreadable,
// the poll still has the listing, so progress is empty rather than an error.
func TestProgressIsEmptyWhenThereIsNothingToRead(t *testing.T) {
	jobs := t.TempDir()
	writeJobState(t, jobs, "garbled", "not json")

	for name, tc := range map[string]struct {
		id    string
		paths Paths
	}{
		"no file":           {id: "missing", paths: Paths{ClaudeJobs: jobs}},
		"not json":          {id: "garbled", paths: Paths{ClaudeJobs: jobs}},
		"no jobs directory": {id: "garbled", paths: Paths{}},
		"no id":             {id: "", paths: Paths{ClaudeJobs: jobs}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := (Claude{}).Progress(tc.id, tc.paths); got != (review.Progress{}) {
				t.Errorf("progress = %+v, want none", got)
			}
		})
	}
}

// The id comes from what claude printed. One that climbs out of the jobs
// directory must not make docket read a file elsewhere.
func TestProgressReadsOnlyInsideTheJobsDirectory(t *testing.T) {
	root := t.TempDir()
	jobs := filepath.Join(root, "jobs")
	writeJobState(t, root, "outside", `{"detail": "not a job"}`)

	if got := (Claude{}).Progress("../outside", Paths{ClaudeJobs: jobs}); got.Detail != "" {
		t.Errorf("read %q from outside the jobs directory", got.Detail)
	}
}
