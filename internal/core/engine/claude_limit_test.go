package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// limitSession is the id of a session whose transcript the tests write.
const limitSession = "823735c4-c8ff-4e82-9dc4-82e0e1356c92"

// scratchProject is the directory claude names after docket's scratch
// directory.
const scratchProject = "-Users-haacked--docket-scratch"

// refusedTurn is the line Claude Code 2.1.308 wrote when an account's weekly
// limit refused a session's turn.
const refusedTurn = `{"parentUuid":"f9351c2e-5b1a-4c4e-9d1e-0c1b2a3d4e5f","isSidechain":false,"type":"assistant","uuid":"29c8d0a1-7e6f-4b3a-8c2d-1e0f9a8b7c6d","timestamp":"2026-10-22T16:04:08.722Z","message":{"model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"You've hit your individual spend limit · run /usage-credits to ask your admin for a higher limit · your weekly limit resets Oct 24 at 4am (America/Los_Angeles)"}]},"apiError":"usage_limit_reached","apiErrorParams":{"rate_limit_info":{"status":"rejected","resetsAt":1792839600,"rateLimitType":"seven_day"}},"error":"rate_limit","isApiErrorMessage":true,"apiErrorStatus":429,"sessionId":"823735c4-c8ff-4e82-9dc4-82e0e1356c92"}`

// refusedAt and refusalResets are the times refusedTurn records.
var (
	refusedAt     = time.Date(2026, 10, 22, 16, 4, 8, 722e6, time.UTC)
	refusalResets = time.Date(2026, 10, 24, 11, 0, 0, 0, time.UTC)
)

const userTurn = `{"type":"user","isSidechain":false,"timestamp":"2026-10-22T16:10:00.000Z","message":{"role":"user","content":"continue"}}`

// queuedTurn is a message a person sent while a turn ran.
const queuedTurn = `{"type":"user","isSidechain":false,"promptSource":"queued","origin":{"kind":"human"},"timestamp":"2026-10-22T16:10:00.000Z","message":{"role":"user","content":"continue"}}`

const promptTurn = `{"type":"user","isSidechain":false,"timestamp":"2026-10-22T16:00:00.000Z","message":{"role":"user","content":"/review-code https://github.com/PostHog/posthog/pull/121510 --draft --append --force"}}`

const replyTurn = `{"parentUuid":"3a4b5c6d-7e8f-4a0b-9c1d-2e3f4a5b6c7d","isSidechain":false,"type":"assistant","uuid":"4b5c6d7e-8f9a-4b1c-8d2e-3f4a5b6c7d8e","timestamp":"2026-10-22T16:20:00.000Z","message":{"model":"claude-opus-4-1","role":"assistant","content":[{"type":"text","text":"Posted the draft review."}]},"sessionId":"823735c4-c8ff-4e82-9dc4-82e0e1356c92"}`

// taskNotification is the line Claude Code 2.1.310 wrote 30 ms after a refused
// turn, when a background sub-agent finished. claude queued it and ran no turn
// for it.
const taskNotification = `{"isSidechain":false,"type":"user","timestamp":"2026-10-22T16:04:08.752Z","promptSource":"system","origin":{"kind":"task-notification","producer":"session-task"},"queueTranscriptOnly":true,"message":{"role":"user","content":"<task-notification>\n<task-id>a2f57c85855be31c5</task-id>\n<status>completed</status>\n<summary>Agent \"Gather architectural context\" finished</summary>\n</task-notification>"}}`

// bookkeeping are the lines claude writes after a turn that are not part of
// the conversation.
var bookkeeping = []string{
	`{"type":"system","subtype":"turn_duration","isMeta":false}`,
	`{"type":"bridge-session"}`,
	`{"type":"cost-state","sessionId":"823735c4-c8ff-4e82-9dc4-82e0e1356c92","totalCostUSD":0}`,
	`{"type":"last-prompt","lastPrompt":"/review-code https://github.com/PostHog/posthog/pull/121510 --draft --append --force"}`,
	`{"type":"attachment"}`,
	`{"type":"mode"}`,
}

// refusal is refusedTurn with one string replaced, for the shapes a refusal can
// take.
func refusal(old, replacement string) string {
	return strings.Replace(refusedTurn, old, replacement, 1)
}

// notification is taskNotification with one string replaced.
func notification(old, replacement string) string {
	return strings.Replace(taskNotification, old, replacement, 1)
}

// sidechain marks a conversation line as a sub-agent's.
func sidechain(line string) string {
	return strings.Replace(line, `"isSidechain":false`, `"isSidechain":true`, 1)
}

// longReply is an ordinary reply far longer than the part of a transcript that
// UsageLimit reads.
func longReply() string {
	return fmt.Sprintf(`{"isSidechain":false,"type":"assistant","timestamp":"2026-10-22T16:30:00.000Z","message":{"role":"assistant","content":[{"type":"text","text":%q}]}}`, strings.Repeat("a", 200<<10))
}

// lines joins transcript lines the way claude writes them, each one ended by a
// newline.
func lines(entries ...string) string {
	var b strings.Builder
	for _, entry := range entries {
		b.WriteString(entry)
		b.WriteString("\n")
	}
	return b.String()
}

func with(entries []string, more ...string) []string {
	return append(append([]string{}, entries...), more...)
}

// writeTranscript lays a transcript out the way claude does, at
// <projects>/<project>/<session id>.jsonl.
func writeTranscript(t *testing.T, projects, project, session, content string) {
	t.Helper()
	dir := filepath.Join(projects, project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, session+".jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// usageLimit writes content as limitSession's transcript and reads it back.
func usageLimit(t *testing.T, content string) (UsageLimit, bool) {
	t.Helper()
	projects := t.TempDir()
	writeTranscript(t, projects, scratchProject, limitSession, content)
	return Claude{}.UsageLimit(limitSession, Paths{ClaudeProjects: projects})
}

func TestUsageLimitReadsClaudesRefusal(t *testing.T) {
	got, ok := usageLimit(t, lines(with([]string{promptTurn, refusedTurn}, bookkeeping...)...))

	if !ok {
		t.Fatal("the refused turn was not recognized")
	}
	if !got.At.Equal(refusedAt) {
		t.Errorf("at = %v, want the refused turn's timestamp %v", got.At, refusedAt)
	}
	if !got.Resets.Equal(refusalResets) {
		t.Errorf("resets = %v, want %v", got.Resets, refusalResets)
	}
}

// claude names the transcript's directory after the session's working
// directory, so a tier-2 clone's session lives somewhere other than the
// scratch directory's.
func TestUsageLimitFindsTheTranscriptUnderAnyProjectDirectory(t *testing.T) {
	projects := t.TempDir()
	writeTranscript(t, projects, scratchProject, "6d681a76-0000-4000-8000-000000000000", lines(promptTurn, replyTurn))
	writeTranscript(t, projects, "-Users-haacked--docket-clones-PostHog-posthog-pr-121510", limitSession, lines(promptTurn, refusedTurn))

	if _, ok := (Claude{}).UsageLimit(limitSession, Paths{ClaudeProjects: projects}); !ok {
		t.Error("the refusal in the clone's project directory was not found")
	}
}

// Another session's refusal says nothing about this one's account.
func TestUsageLimitReadsOnlyTheSessionsOwnTranscript(t *testing.T) {
	projects := t.TempDir()
	writeTranscript(t, projects, scratchProject, "6d681a76-0000-4000-8000-000000000000", lines(promptTurn, refusedTurn))
	writeTranscript(t, projects, scratchProject, limitSession, lines(promptTurn, replyTurn))

	if _, ok := (Claude{}).UsageLimit(limitSession, Paths{ClaudeProjects: projects}); ok {
		t.Error("another session's refusal was reported for this one")
	}
}

func TestUsageLimitReportsNoResetWhenClaudeGaveNone(t *testing.T) {
	for name, line := range map[string]string{
		"no error parameters": refusal(`"apiErrorParams":{"rate_limit_info":{"status":"rejected","resetsAt":1792839600,"rateLimitType":"seven_day"}},`, ""),
		"no rate limit info":  refusal(`{"rate_limit_info":{"status":"rejected","resetsAt":1792839600,"rateLimitType":"seven_day"}}`, `{}`),
		"no reset":            refusal(`"resetsAt":1792839600,`, ""),
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := usageLimit(t, lines(promptTurn, line))

			if !ok {
				t.Fatal("a refusal without a reset was not recognized")
			}
			if !got.Resets.IsZero() {
				t.Errorf("resets = %v, want zero", got.Resets)
			}
			if !got.At.Equal(refusedAt) {
				t.Errorf("at = %v, want %v", got.At, refusedAt)
			}
		})
	}
}

// A line without isSidechain is the session's own.
func TestUsageLimitReadsARefusalWithNoSidechainField(t *testing.T) {
	if _, ok := usageLimit(t, lines(promptTurn, refusal(`"isSidechain":false,`, ""))); !ok {
		t.Error("a refusal with no isSidechain field was not recognized")
	}
}

// Sub-agents write to the session's transcript. Their lines can follow the
// session's own refusal.
func TestUsageLimitSkipsASubAgentsLinesAfterTheRefusal(t *testing.T) {
	if _, ok := usageLimit(t, lines(promptTurn, refusedTurn, sidechain(replyTurn), sidechain(userTurn))); !ok {
		t.Error("a sub-agent's lines hid the session's refusal")
	}
}

// claude writes messages of its own into the conversation, such as a notice that
// a background sub-agent finished. One can follow the refusal with no turn of its
// own.
func TestUsageLimitSkipsAMessageClaudeQueuesWithoutATurn(t *testing.T) {
	got, ok := usageLimit(t, lines(promptTurn, refusedTurn, taskNotification))

	if !ok {
		t.Fatal("claude's own message hid the session's refusal")
	}
	if !got.At.Equal(refusedAt) {
		t.Errorf("at = %v, want %v", got.At, refusedAt)
	}
}

// Only a refusal that ended the session's last turn moves the review to another
// account. Each of these sessions is waiting for some other reason.
func TestUsageLimitIsNoneUnlessTheLastTurnIsARefusal(t *testing.T) {
	for name, content := range map[string]string{
		"a normal reply":                        lines(with([]string{promptTurn, replyTurn}, bookkeeping...)...),
		"the user continued after a refusal":    lines(with([]string{promptTurn, refusedTurn}, with(bookkeeping, userTurn)...)...),
		"a reply after a refusal":               lines(promptTurn, refusedTurn, userTurn, replyTurn),
		"a queued message after a refusal":      lines(promptTurn, refusedTurn, queuedTurn),
		"a notification after a queued message": lines(promptTurn, refusedTurn, queuedTurn, taskNotification),
		"a notification claude runs a turn for": lines(promptTurn, refusedTurn, notification(`"queueTranscriptOnly":true,`, "")),
		"a person's message with no turn":       lines(promptTurn, refusedTurn, notification(`"promptSource":"system"`, `"promptSource":"queued"`)),
		"an assistant line with no turn":        lines(promptTurn, refusedTurn, notification(`"type":"user"`, `"type":"assistant"`)),
		"another API error":                     lines(promptTurn, refusal(`"error":"rate_limit"`, `"error":"authentication_failed"`)),
		"a rate limit with no API error flag":   lines(promptTurn, refusal(`"isApiErrorMessage":true`, `"isApiErrorMessage":false`)),
		"a rate limit on a user entry":          lines(promptTurn, refusal(`"type":"assistant"`, `"type":"user"`)),
		"a sub-agent's refusal after a reply":   lines(promptTurn, replyTurn, sidechain(refusedTurn)),
		"a sub-agent's refusal and bookkeeping": lines(with([]string{promptTurn, replyTurn, sidechain(refusedTurn)}, bookkeeping...)...),
		"bookkeeping only":                      lines(bookkeeping...),
		"an empty transcript":                   "",
	} {
		t.Run(name, func(t *testing.T) {
			if got, ok := usageLimit(t, content); ok {
				t.Errorf("reported a usage limit %+v", got)
			}
		})
	}
}

// docket stops a session it reads as refused and starts the review again under
// another account. A turn after the refusal that docket cannot read may be the
// user continuing, so it must not let the earlier refusal answer for it.
func TestUsageLimitIsNoneWhenALaterTurnDoesNotDecode(t *testing.T) {
	for name, line := range map[string]string{
		"a timestamp that is a number":                           `{"type":"user","isSidechain":false,"timestamp":1792839600000,"message":{"role":"user","content":"continue"}}`,
		"an error that is an object":                             `{"type":"assistant","isSidechain":false,"timestamp":"2026-10-22T16:20:00.000Z","error":{"type":"overloaded"},"message":{"role":"assistant","content":[]}}`,
		"a source that is an object":                             strings.Replace(userTurn, `"isSidechain":false,`, `"isSidechain":false,"promptSource":{"kind":"system"},`, 1),
		"claude's own message with a timestamp that is a number": notification(`"timestamp":"2026-10-22T16:04:08.752Z"`, `"timestamp":1792857848752`),
	} {
		t.Run(name, func(t *testing.T) {
			if got, ok := usageLimit(t, lines(promptTurn, refusedTurn, line)); ok {
				t.Errorf("reported a usage limit %+v from before the last turn", got)
			}
		})
	}
}

// claude may be partway through writing the next line, which can be the user
// continuing the session.
func TestUsageLimitWaitsForALineStillBeingWritten(t *testing.T) {
	for name, content := range map[string]string{
		"the refusal itself":         promptTurn + "\n" + refusedTurn,
		"a line after the refusal":   lines(promptTurn, refusedTurn) + `{"type":"user","isSidechain":false,"timestamp":"2026-10-22T16:10:00.000Z","message":{"role":"us`,
		"bookkeeping after it":       lines(promptTurn, refusedTurn) + bookkeeping[0],
		"a long line after it":       lines(promptTurn, refusedTurn) + strings.TrimSuffix(longReply(), `"}]}}`),
		"a refusal after a long one": lines(longReply()) + refusedTurn,
	} {
		t.Run(name, func(t *testing.T) {
			if got, ok := usageLimit(t, content); ok {
				t.Errorf("reported a usage limit %+v while a line was unfinished", got)
			}
		})
	}
}

// UsageLimit reads only the end of a transcript. The window can start partway
// through a line. That line must not hide a refusal after it, and an earlier
// refusal must not answer for it.
func TestUsageLimitReadsTheEndOfALongTranscript(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		want    bool
	}{
		{name: "a long reply after a refusal", content: lines(promptTurn, refusedTurn, longReply()), want: false},
		{name: "a long reply and bookkeeping after a refusal", content: lines(with([]string{promptTurn, refusedTurn, longReply()}, bookkeeping...)...), want: false},
		{name: "a sub-agent's refusal after a long reply", content: lines(with([]string{promptTurn, longReply(), sidechain(refusedTurn)}, bookkeeping...)...), want: false},
		{name: "a refusal after a long reply", content: lines(with([]string{promptTurn, longReply(), refusedTurn}, bookkeeping...)...), want: true},
		{name: "a refusal after a long reply and a user turn", content: lines(promptTurn, longReply(), userTurn, refusedTurn), want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := usageLimit(t, tc.content)

			if ok != tc.want {
				t.Fatalf("reported a limit = %v, want %v", ok, tc.want)
			}
			if ok && !got.At.Equal(refusedAt) {
				t.Errorf("at = %v, want %v", got.At, refusedAt)
			}
		})
	}
}

// The session id comes from what claude printed. One that is not a UUID must
// not name a transcript outside the projects directory, or match every
// transcript in it.
func TestUsageLimitReadsOnlyATranscriptTheSessionIDNames(t *testing.T) {
	root := t.TempDir()
	projects := filepath.Join(root, "projects")
	writeTranscript(t, projects, scratchProject, limitSession, lines(promptTurn, refusedTurn))
	writeTranscript(t, projects, ".", "x", lines(promptTurn, refusedTurn))
	writeTranscript(t, root, "elsewhere", "x", lines(promptTurn, refusedTurn))

	for _, id := range []string{
		"",
		"*",
		"../x",
		"../../elsewhere/x",
		"823735c4",
		"823735c4-c8ff-4e82-9dc4-82e0e1356c9?",
		"823735c4-c8ff-4e82-9dc4-*",
		limitSession + "/../" + limitSession,
	} {
		t.Run(id, func(t *testing.T) {
			if got, ok := (Claude{}).UsageLimit(id, Paths{ClaudeProjects: projects}); ok {
				t.Errorf("session id %q reported a usage limit %+v", id, got)
			}
		})
	}
}

func TestUsageLimitFindsNothingWithoutATranscript(t *testing.T) {
	projects := t.TempDir()
	writeTranscript(t, projects, scratchProject, limitSession, lines(promptTurn, refusedTurn))

	for name, paths := range map[string]Paths{
		"no projects directory":          {},
		"a projects directory not there": {ClaudeProjects: filepath.Join(t.TempDir(), "missing")},
		"another account's projects":     {ClaudeProjects: t.TempDir()},
	} {
		t.Run(name, func(t *testing.T) {
			if got, ok := (Claude{}).UsageLimit(limitSession, paths); ok {
				t.Errorf("reported a usage limit %+v", got)
			}
		})
	}
}

// docket rotates claude accounts only.
func TestCodexFindsNoUsageLimit(t *testing.T) {
	projects := t.TempDir()
	writeTranscript(t, projects, scratchProject, limitSession, lines(promptTurn, refusedTurn))

	if got, ok := (Codex{}).UsageLimit(limitSession, Paths{ClaudeProjects: projects}); ok {
		t.Errorf("codex reported a usage limit %+v", got)
	}
}

// The tail can start exactly where the refusal's line starts. That line is
// whole, so it still answers.
func TestUsageLimitReadsARefusalThatStartsTheTail(t *testing.T) {
	refusal := refusedTurn + "\n"
	padding := `{"type":"cost-state","pad":""}` + "\n"
	padding = strings.Replace(padding, `""`, `"`+strings.Repeat("p", transcriptTail-len(refusal)-len(padding))+`"`, 1)
	content := lines(longReply()) + refusal + padding

	if _, ok := usageLimit(t, content); !ok {
		t.Error("missed the refusal at the start of the tail")
	}
}
