package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

// replaced is entries with every old string replaced by its new one, as
// strings.NewReplacer replaces them.
func replaced(entries []string, oldnew ...string) []string {
	r := strings.NewReplacer(oldnew...)
	out := make([]string, len(entries))
	for i, entry := range entries {
		out[i] = r.Replace(entry)
	}
	return out
}

// turnCompanion is the line Claude Code 2.1.312 wrote just before a task
// notification that followed a refused turn. claude ran no turn for it.
const turnCompanion = `{"parentUuid":"e4176aef-494a-4719-8930-2517755e1ffd","isSidechain":false,"promptId":"6f718fd8-a1ef-4ebd-a30c-83a6468a16f8","type":"user","message":{"role":"user","content":"<system-reminder>\n<tools-used-before-summary>TaskStop, ToolSearch</tools-used-before-summary>\n</system-reminder>"},"isMeta":true,"turnCompanion":true,"uuid":"755cf424-8ad0-4bf4-bfb4-00463ac57999","timestamp":"2026-10-28T17:47:09.951Z","queueSkipAttachments":true,"queueTranscriptOnly":true,"sessionKind":"bg","userType":"external","sessionId":"1b5209e0-f2cb-42b4-ab42-6cd849a9925a"}`

// companion is turnCompanion with one string replaced.
func companion(old, replacement string) string {
	return strings.Replace(turnCompanion, old, replacement, 1)
}

// wrapUpPromptID is the promptId of every user line of the turn in which
// session 85f79088 reached its account's usage limit.
const wrapUpPromptID = "4508d009-d69c-4b25-9d7f-3123cd3a91ae"

// wrapUpPrompt is the line Claude Code 2.1.312 wrote to open a turn for a
// sub-agent's report. The account reached its usage limit during that turn.
const wrapUpPrompt = `{"isSidechain":false,"promptId":"4508d009-d69c-4b25-9d7f-3123cd3a91ae","type":"user","message":{"role":"user","content":"Another Claude session sent a message:\n<agent-message from=\"aa02b58c8200606ea\">Report written.</agent-message>"},"isMeta":true,"timestamp":"2026-10-28T17:45:49.326Z","permissionMode":"auto","origin":{"kind":"peer","from":"aa02b58c8200606ea"},"promptSource":"system","sessionKind":"bg"}`

// wrapUpToolUse is a tool call Claude Code 2.1.312 wrote in that turn.
const wrapUpToolUse = `{"isSidechain":false,"type":"assistant","timestamp":"2026-10-28T17:45:58.327Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_01P9tGvgFztJMoSQxMj9vZrW","name":"Bash","input":{}}]},"sessionKind":"bg"}`

// wrapUpToolResult is the result Claude Code 2.1.312 wrote for that tool call.
const wrapUpToolResult = `{"isSidechain":false,"promptId":"4508d009-d69c-4b25-9d7f-3123cd3a91ae","type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_01P9tGvgFztJMoSQxMj9vZrW","type":"tool_result","content":"…","is_error":false}]},"timestamp":"2026-10-28T17:45:58.944Z","sessionKind":"bg"}`

// wrapUpNote is the notice Claude Code 2.1.312 added to that turn when the
// account reached its usage limit. claude wrote no API error for the turn.
const wrapUpNote = `{"parentUuid":"81e56ed6-66bd-4c80-af13-386f3e52b8cc","isSidechain":false,"promptId":"4508d009-d69c-4b25-9d7f-3123cd3a91ae","type":"user","message":{"role":"user","content":"[Usage limit reached; a short grace allowance remains, then this turn is cut off without warning. Judge what's left: if it's genuinely only a few steps, finish it properly, then stop and say so without extra polishing. Otherwise don't attempt it all: finish the piece in hand and at most two more like it, then stop. The allowance also pays for any subagents you have running: they don't get this message and will keep going, so it's up to you how to handle them. Either way, end by telling the user their usage limit was reached, what's done and what's left.]"},"isMeta":true,"turnCompanion":true,"usageLimitNote":"wrap_up","uuid":"2aa0aa04-eefc-467f-abba-0a4d5447e4ea","timestamp":"2026-10-28T17:45:58.952Z","sessionKind":"bg"}`

// wrapUpReply is the reply Claude Code 2.1.312 wrote to end that turn.
const wrapUpReply = `{"isSidechain":false,"type":"assistant","timestamp":"2026-10-28T17:46:12.580Z","message":{"role":"assistant","content":[{"type":"text","text":"Your usage limit was reached, so I stopped before posting anything."}]},"sessionKind":"bg"}`

// garbledToolResult is wrapUpToolResult with a timestamp that does not decode.
var garbledToolResult = strings.Replace(wrapUpToolResult, `"timestamp":"2026-10-28T17:45:58.944Z"`, `"timestamp":1793209558944`, 1)

// wrapUpAt is the time wrapUpNote records.
var wrapUpAt = time.Date(2026, 10, 28, 17, 45, 58, 952e6, time.UTC)

// wrapUpStart is the part of wrapUpTurn before the note.
var wrapUpStart = []string{wrapUpPrompt, wrapUpToolUse, wrapUpToolResult}

// wrapUpTurn is the whole turn in which session 85f79088 reached its account's
// usage limit.
var wrapUpTurn = with(wrapUpStart, wrapUpNote, wrapUpReply)

// note is wrapUpNote with one string replaced.
func note(old, replacement string) string {
	return strings.Replace(wrapUpNote, old, replacement, 1)
}

// releasePromptID is the promptId of the turn that releaseNote belongs to.
const releasePromptID = "85520f37-fc0f-48e5-8dbc-e8a3deaa5eaf"

// releaseNote is the notice Claude Code 2.1.304 added to a turn that a person's
// message opened. It tells the model that earlier notices no longer apply.
const releaseNote = `{"isSidechain":false,"promptId":"85520f37-fc0f-48e5-8dbc-e8a3deaa5eaf","type":"user","message":{"role":"user","content":"[Earlier usage-limit notes no longer apply. Continue working normally. Don't mention this to the user.]"},"isMeta":true,"turnCompanion":true,"usageLimitNote":"release","timestamp":"2026-10-20T17:17:44.164Z"}`

// personTurn is a person's message that opens the turn releaseNote belongs to.
var personTurn = strings.Replace(userTurn, `"type":"user",`, `"type":"user","promptId":"`+releasePromptID+`",`, 1)

// exitPromptID is the promptId claude gave each line of a person's /exit.
const exitPromptID = "88c6cdd5-cb2b-4003-9ed9-2038296d0995"

// exitCaveat, exitCommand, and exitOutput are the lines Claude Code 2.1.312
// wrote when a person attached to a background session and left it with
// /exit. claude ran no turn for them.
const (
	exitCaveat  = `{"parentUuid":"4aefe929-5358-4139-b8a1-4e0c8fa66bf6","isSidechain":false,"promptId":"88c6cdd5-cb2b-4003-9ed9-2038296d0995","type":"user","message":{"role":"user","content":"<local-command-caveat>The command below was run directly in Claude Code, not sent to you as a request, and its output goes straight to the user. It's recorded here as context for later messages.</local-command-caveat>"},"isMeta":true,"uuid":"e64e36f6-caad-4b4e-ae80-e5e979fb471a","timestamp":"2026-10-28T18:00:04.442Z","sessionKind":"bg"}`
	exitCommand = `{"parentUuid":"e64e36f6-caad-4b4e-ae80-e5e979fb471a","isSidechain":false,"promptId":"88c6cdd5-cb2b-4003-9ed9-2038296d0995","type":"user","message":{"role":"user","content":"<command-name>/exit</command-name>\n            <command-message>exit</command-message>\n            <command-args></command-args>"},"uuid":"9488b56e-6400-4fcf-8dec-c2651d2214bb","timestamp":"2026-10-28T18:00:04.441Z","sessionKind":"bg"}`
	exitOutput  = `{"parentUuid":"9488b56e-6400-4fcf-8dec-c2651d2214bb","isSidechain":false,"promptId":"88c6cdd5-cb2b-4003-9ed9-2038296d0995","type":"user","message":{"role":"user","content":"<local-command-stdout>(no content)</local-command-stdout>"},"uuid":"e32eeb7f-5cb5-4a36-8f29-a6f8f946d11c","timestamp":"2026-10-28T18:00:04.441Z","sessionKind":"bg"}`
)

// exitLines are a person's /exit in the order claude wrote them.
var exitLines = []string{exitCaveat, exitCommand, exitOutput}

// secondExit is the second /exit that the same session recorded, at 18:00:47.
var secondExit = replaced(exitLines, exitPromptID, "c3feb92c-78c7-430a-bda5-5de282163c17", "18:00:04.44", "18:00:47.44")

// exitTag is a message under releasePromptID that starts the way claude writes
// an /exit command. typedExitTag is the same message as a person types it.
var (
	exitTag      = strings.Replace(personTurn, `"content":"continue"`, `"content":"<command-name>/exit</command-name> is what I typed"`, 1)
	typedExitTag = strings.Replace(exitTag, `"type":"user",`, `"type":"user","promptSource":"typed",`, 1)
)

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

// claude can write a companion line before a message of its own. The companion
// is meta and queued without a turn. It carries no promptSource.
func TestUsageLimitSkipsTheCompanionOfAMessageClaudeQueuesWithoutATurn(t *testing.T) {
	got, ok := usageLimit(t, lines(promptTurn, refusedTurn, taskNotification, turnCompanion, taskNotification))

	if !ok {
		t.Fatal("claude's companion line hid the session's refusal")
	}
	if !got.At.Equal(refusedAt) {
		t.Errorf("at = %v, want %v", got.At, refusedAt)
	}
}

// claude also writes a meta companion for a prompt it runs a turn for. That
// companion has no queueTranscriptOnly. A queued line that is neither meta nor
// marked as claude's own may be a person's message. So may one that carries a
// person's source.
func TestUsageLimitIsNoneAfterACompanionThatMayStartATurn(t *testing.T) {
	for name, line := range map[string]string{
		"the companion of a prompt claude ran":      companion(`"queueTranscriptOnly":true,`, ""),
		"a queued line that is not meta":            companion(`"isMeta":true,`, ""),
		"a queued meta line with a person's source": companion(`"isMeta":true,`, `"isMeta":true,"promptSource":"queued",`),
	} {
		t.Run(name, func(t *testing.T) {
			if got, ok := usageLimit(t, lines(promptTurn, refusedTurn, line)); ok {
				t.Errorf("reported a usage limit %+v", got)
			}
		})
	}
}

// When an account reaches its usage limit during a turn, claude can add a
// notice to the turn instead of refusing it. The model then ends the turn with
// an ordinary reply. claude writes no API error for that turn.
func TestUsageLimitReadsAWrapUpNotice(t *testing.T) {
	for name, content := range map[string]string{
		"the reply after the notice":                               lines(wrapUpTurn...),
		"more work after the notice":                               lines(with(wrapUpStart, wrapUpNote, wrapUpToolUse, wrapUpToolResult, wrapUpReply)...),
		"bookkeeping and claude's own message after it":            lines(with(with(wrapUpTurn, bookkeeping...), taskNotification)...),
		"claude's companion and message after it":                  lines(with(wrapUpTurn, turnCompanion, taskNotification)...),
		"a released turn before it":                                lines(with([]string{personTurn, releaseNote, replyTurn}, wrapUpTurn...)...),
		"a person's two exits after it, as session 85f79088 ended": lines(slices.Concat(wrapUpTurn, exitLines, secondExit)...),
		"a notice with no promptId before the reply":               lines(with(wrapUpStart, note(`"promptId":"`+wrapUpPromptID+`",`, ""), wrapUpReply)...),
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := usageLimit(t, content)

			if !ok {
				t.Fatal("the wrap-up notice was not recognized")
			}
			if !got.At.Equal(wrapUpAt) {
				t.Errorf("at = %v, want the notice's timestamp %v", got.At, wrapUpAt)
			}
			if !got.Resets.IsZero() {
				t.Errorf("resets = %v, want zero", got.Resets)
			}
		})
	}
}

// The lines that run no turn can land inside the turn that reached the limit.
// They do not end that turn.
func TestUsageLimitReadsAWrapUpNoticePastLinesThatRunNoTurn(t *testing.T) {
	interrupted := func(interruption ...string) string {
		head := with(wrapUpStart, wrapUpNote, wrapUpToolUse)
		return lines(with(with(head, interruption...), wrapUpToolResult, wrapUpReply)...)
	}
	for name, content := range map[string]string{
		"claude's own message":               interrupted(taskNotification),
		"claude's companion and its message": interrupted(turnCompanion, taskNotification),
		"a person's /exit":                   interrupted(exitLines...),
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := usageLimit(t, content)

			if !ok {
				t.Fatal("a line that runs no turn hid the wrap-up notice")
			}
			if !got.At.Equal(wrapUpAt) {
				t.Errorf("at = %v, want the notice's timestamp %v", got.At, wrapUpAt)
			}
		})
	}
}

// A notice moves the review only when the model has replied in the turn that
// holds it and nothing has released it since. Each of these sessions is waiting
// for some other reason. In some of them docket cannot tell which turn the
// notice is in.
func TestUsageLimitIsNoneUnlessTheLastTurnEndedAfterAWrapUpNotice(t *testing.T) {
	sameTurnRelease := strings.Replace(releaseNote, releasePromptID, wrapUpPromptID, 1)
	unpromptedResult := strings.Replace(wrapUpToolResult, `"promptId":"`+wrapUpPromptID+`",`, "", 1)
	laterResult := strings.Replace(wrapUpToolResult, wrapUpPromptID, releasePromptID, 1)
	for name, content := range map[string]string{
		"no reply after the notice yet":                              lines(with(wrapUpStart, wrapUpNote)...),
		"a tool call after the notice with no result yet":            lines(with(wrapUpStart, wrapUpNote, wrapUpToolUse)...),
		"a person's message after the reply":                         lines(with(wrapUpTurn, personTurn)...),
		"a queued message after the reply":                           lines(with(wrapUpTurn, queuedTurn)...),
		"a later turn that released the notice":                      lines(with(wrapUpTurn, personTurn, releaseNote, replyTurn)...),
		"a release after the notice in the same turn":                lines(with(wrapUpStart, wrapUpNote, sameTurnRelease, wrapUpReply)...),
		"a notice of another kind":                                   lines(with(wrapUpStart, note(`"usageLimitNote":"wrap_up"`, `"usageLimitNote":"warn"`), wrapUpReply)...),
		"a sub-agent's notice":                                       lines(with(wrapUpStart, sidechain(wrapUpNote), wrapUpReply)...),
		"a later ordinary turn of another promptId":                  lines(with(wrapUpTurn, personTurn, replyTurn)...),
		"a later ordinary turn with no promptId":                     lines(with(wrapUpTurn, userTurn, replyTurn)...),
		"a tool result with no promptId after the notice":            lines(with(wrapUpStart, wrapUpNote, wrapUpToolUse, unpromptedResult, wrapUpReply)...),
		"a person's queued message after the notice":                 lines(with(wrapUpStart, wrapUpNote, queuedTurn, wrapUpReply)...),
		"a notice in a sub-agent's turn after a reply":               lines(promptTurn, replyTurn, sidechain(wrapUpNote), sidechain(wrapUpReply)),
		"a notice and reply that are both a sub-agent's":             lines(with(wrapUpStart, sidechain(wrapUpNote), sidechain(wrapUpReply))...),
		"an API error claude may retry after the notice":             lines(with(wrapUpStart, wrapUpNote, wrapUpToolUse, wrapUpToolResult, refusal(`"error":"rate_limit"`, `"error":"server_error"`))...),
		"claude's own message directly before the reply":             lines(with(wrapUpStart, wrapUpNote, taskNotification, wrapUpReply)...),
		"a later turn a person typed that starts with the /exit tag": lines(with(wrapUpTurn, typedExitTag, wrapUpToolUse, laterResult, wrapUpReply)...),
		"a later turn with no source that starts with the /exit tag": lines(with(wrapUpTurn, exitTag, wrapUpToolUse, laterResult, wrapUpReply)...),
	} {
		t.Run(name, func(t *testing.T) {
			if got, ok := usageLimit(t, content); ok {
				t.Errorf("reported a usage limit %+v", got)
			}
		})
	}
}

// docket cannot tell which turn a line it cannot read belongs to. A notice
// before such a line must not answer for the session.
func TestUsageLimitIsNoneWhenALineOfTheWrapUpTurnDoesNotDecode(t *testing.T) {
	afterNote := func(line string) string {
		return lines(with(wrapUpStart, wrapUpNote, wrapUpToolUse, line, wrapUpReply)...)
	}
	for name, content := range map[string]string{
		"a tool result whose timestamp is a number":                              afterNote(garbledToolResult),
		"a tool result whose promptId is a number":                               afterNote(strings.Replace(wrapUpToolResult, `"promptId":"`+wrapUpPromptID+`"`, `"promptId":4508`, 1)),
		"a notice whose kind is an object":                                       lines(with(wrapUpStart, note(`"usageLimitNote":"wrap_up"`, `"usageLimitNote":{"kind":"wrap_up"}`), wrapUpReply)...),
		"claude's own message after the reply with a timestamp that is a number": lines(with(wrapUpTurn, notification(`"timestamp":"2026-10-22T16:04:08.752Z"`, `"timestamp":1792857848752`))...),
	} {
		t.Run(name, func(t *testing.T) {
			if got, ok := usageLimit(t, content); ok {
				t.Errorf("reported a usage limit %+v from a turn docket could not read", got)
			}
		})
	}
}

// A line before the session's last message cannot be the person continuing. A
// line that does not decode ends the turn there, and the messages after it
// still answer.
func TestUsageLimitReadsTheTurnAfterALineThatDoesNotDecode(t *testing.T) {
	for name, tc := range map[string]struct {
		content string
		at      time.Time
	}{
		"a refusal":        {lines(wrapUpPrompt, wrapUpToolUse, garbledToolResult, refusedTurn), refusedAt},
		"a wrap-up notice": {lines(wrapUpPrompt, wrapUpToolUse, garbledToolResult, wrapUpNote, wrapUpReply), wrapUpAt},
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := usageLimit(t, tc.content)

			if !ok {
				t.Fatal("a line earlier in the turn that does not decode hid the limit")
			}
			if !got.At.Equal(tc.at) {
				t.Errorf("at = %v, want %v", got.At, tc.at)
			}
		})
	}
}

// The notice says claude cuts the turn off once the grace runs out. The refusal
// that ends the turn answers, because it says when the limit resets.
func TestUsageLimitReadsARefusalAfterAWrapUpNotice(t *testing.T) {
	got, ok := usageLimit(t, lines(with(wrapUpStart, wrapUpNote, wrapUpToolUse, wrapUpToolResult, refusedTurn)...))

	if !ok {
		t.Fatal("the refusal that ended a wrap-up turn was not recognized")
	}
	if !got.At.Equal(refusedAt) {
		t.Errorf("at = %v, want the refusal's timestamp %v", got.At, refusedAt)
	}
	if !got.Resets.Equal(refusalResets) {
		t.Errorf("resets = %v, want %v", got.Resets, refusalResets)
	}
}

// A person who attaches to a background session and leaves with /exit makes
// claude write three lines under one promptId. claude runs no turn for them.
func TestUsageLimitSkipsAPersonsExit(t *testing.T) {
	refused := []string{promptTurn, refusedTurn}
	for name, content := range map[string]string{
		"one exit":  lines(with(refused, exitLines...)...),
		"two exits": lines(slices.Concat(refused, exitLines, secondExit)...),
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := usageLimit(t, content)

			if !ok {
				t.Fatal("a person's /exit hid the session's refusal")
			}
			if !got.At.Equal(refusedAt) {
				t.Errorf("at = %v, want %v", got.At, refusedAt)
			}
		})
	}
}

// Only /exit leaves the session without a turn. Another command may mean that
// the person is about to continue. docket passes over a line only when its
// promptId is the promptId of an /exit command line.
func TestUsageLimitIsNoneAfterAnythingButAPersonsExit(t *testing.T) {
	refused := []string{promptTurn, refusedTurn}
	command := func(name string) []string {
		return replaced(exitLines,
			"<command-name>/exit</command-name>", "<command-name>/"+name+"</command-name>",
			"<command-message>exit</command-message>", "<command-message>"+name+"</command-message>")
	}
	commandOfItsOwn := []string{exitCaveat, strings.Replace(exitCommand, exitPromptID, "2b7e0f4c-5d6a-4b8c-9e1f-0a2b3c4d5e6f", 1), exitOutput}
	mentionsExit := strings.Replace(personTurn, `"content":"continue"`, `"content":"I typed <command-name>/exit</command-name> by mistake, carry on"`, 1)
	for name, content := range map[string]string{
		"a person's message after the exit":                 lines(with(with(refused, exitLines...), userTurn)...),
		"a /model command":                                  lines(with(refused, command("model")...)...),
		"a /login command":                                  lines(with(refused, command("login")...)...),
		"a command whose name starts with exit":             lines(with(refused, command("exit-plan")...)...),
		"an exit with no promptIds":                         lines(with(refused, replaced(exitLines, `"promptId":"`+exitPromptID+`",`, "")...)...),
		"an exit command under a promptId of its own":       lines(with(refused, commandOfItsOwn...)...),
		"a person's message that mentions /exit":            lines(with(refused, mentionsExit)...),
		"a person's message that starts with the /exit tag": lines(with(refused, typedExitTag)...),
	} {
		t.Run(name, func(t *testing.T) {
			if got, ok := usageLimit(t, content); ok {
				t.Errorf("reported a usage limit %+v", got)
			}
		})
	}
}
