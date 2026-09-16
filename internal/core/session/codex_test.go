package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/review"
)

// codexService is a service whose sessions land in a directory the test owns,
// because the real one is the user's own ~/.codex/sessions.
func codexService(t *testing.T, ghc *fakeGH) (*Service, string) {
	t.Helper()

	svc, _ := newService(t, ghc, newFakeGit())
	sessions := t.TempDir()
	svc.Cfg.CodexSessionsDir = sessions
	return svc, sessions
}

// writeRollout lays out a codex session file: the date directories are local
// time, and the timestamps inside are UTC.
func writeRollout(t *testing.T, sessionsDir, id, cwd string, at time.Time) {
	t.Helper()

	dir := filepath.Join(sessionsDir, at.Local().Format("2006/01/02"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stamp := at.UTC().Format(time.RFC3339)
	line := fmt.Sprintf(
		`{"timestamp":%q,"type":"session_meta","payload":{"id":%q,"cwd":%q,"timestamp":%q}}`+"\n",
		stamp, id, cwd, stamp,
	)
	name := fmt.Sprintf("rollout-%s-%s.jsonl", at.Local().Format("2006-01-02T15-04-05"), id)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
}

func launchedInCodex(t *testing.T, svc *Service, ref pr.Ref) review.Record {
	t.Helper()

	rec, _, err := svc.Prepare(context.Background(), ref, "codex")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if rec.SessionID != "" {
		t.Fatalf("session id = %q, want none before the session ran: interactive codex takes no id", rec.SessionID)
	}
	rec, _, err = svc.LaunchSpec(context.Background(), rec)
	if err != nil {
		t.Fatalf("LaunchSpec: %v", err)
	}
	return rec
}

// Interactive codex names its own session, so the id exists only on disk once
// the session has run. Without it a later resume has no conversation to reopen.
func TestAfterExitCapturesTheCodexSessionID(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, sessions := codexService(t, ghc)
	rec := launchedInCodex(t, svc, unlisted)
	writeRollout(t, sessions, "0199a1b2-0000-7000-8000-00000000beef", rec.Dir, rec.StartedAt.Add(time.Minute))

	done, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}

	if done.SessionID != "0199a1b2-0000-7000-8000-00000000beef" {
		t.Errorf("session id = %q, want the one codex recorded", done.SessionID)
	}

	stored, err := svc.Records()
	if err != nil {
		t.Fatal(err)
	}
	if stored[0].SessionID != done.SessionID {
		t.Errorf("the stored record names session %q, want %q: the id has to survive the exit to be resumed", stored[0].SessionID, done.SessionID)
	}
}

func TestACapturedCodexSessionIsWhatResumeReopens(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, sessions := codexService(t, ghc)
	rec := launchedInCodex(t, svc, unlisted)
	writeRollout(t, sessions, "0199a1b2-0000-7000-8000-00000000beef", rec.Dir, rec.StartedAt.Add(time.Minute))

	rec, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}

	_, spec, err := svc.ResumeSpec(context.Background(), rec)
	if err != nil {
		t.Fatalf("ResumeSpec: %v", err)
	}
	// The id leads the flags. codex takes it as a positional, and an argument
	// sitting after --add-dir <DIR> is in a flag's value position.
	if !strings.Contains(spec.String(), "codex resume 0199a1b2-0000-7000-8000-00000000beef") {
		t.Errorf("spec = %s, want it to reopen the captured session", spec)
	}
}

// A record with nothing captured has no conversation to reopen, so the resume
// key starts a review instead of running `codex resume` with no id.
func TestResumeStartsFreshWhenNoCodexSessionWasCaptured(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := codexService(t, ghc)
	rec := launchedInCodex(t, svc, unlisted)

	_, spec, err := svc.ResumeSpec(context.Background(), rec)
	if err != nil {
		t.Fatalf("ResumeSpec: %v", err)
	}

	line := spec.String()
	if strings.Contains(line, "resume") {
		t.Errorf("spec = %s, want a fresh review: there is no session to reopen", spec)
	}
	if !strings.Contains(line, "$review-code "+rec.URL+" --draft") {
		t.Errorf("spec = %s, want a fresh review", spec)
	}
}

// A session docket cannot find costs a resume, which starts fresh instead. It
// is not worth failing the detection that reads GitHub.
func TestAfterExitStillReadsGitHubWhenNoCodexSessionMatches(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, sessions := codexService(t, ghc)
	rec := launchedInCodex(t, svc, unlisted)
	writeRollout(t, sessions, "somebody-elses-session", t.TempDir(), rec.StartedAt.Add(time.Minute))
	ghc.reviews = []review.GHReview{
		{ID: pendingID, User: review.GHUser{Login: "haacked"}, State: review.StatePending},
	}

	done, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}

	if done.State != review.StateDrafted {
		t.Errorf("state = %q, want the draft detected even with no session captured", done.State)
	}
	if done.SessionID != "" {
		t.Errorf("session id = %q, want none: no session ran in this record's directory", done.SessionID)
	}
}

// Resuming writes a further rollout, so the record has to name the newest one or
// the resume after that reopens a conversation two sessions old.
func TestAfterExitFollowsTheCodexSessionAcrossAResume(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, sessions := codexService(t, ghc)
	rec := launchedInCodex(t, svc, unlisted)
	writeRollout(t, sessions, "first-session", rec.Dir, rec.StartedAt.Add(time.Minute))

	rec, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}

	svc.Now = func() time.Time { return start.Add(time.Hour) }
	rec, _, err = svc.ResumeSpec(context.Background(), rec)
	if err != nil {
		t.Fatalf("ResumeSpec: %v", err)
	}
	writeRollout(t, sessions, "second-session", rec.Dir, rec.StartedAt.Add(time.Minute))

	done, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}

	if done.SessionID != "second-session" {
		t.Errorf("session id = %q, want the session the resume left behind", done.SessionID)
	}
}
