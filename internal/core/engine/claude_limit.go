package engine

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

// UsageLimit is the agent's refusal of a session's turn because the session's
// account is over a usage limit.
type UsageLimit struct {
	At time.Time
	// Resets is when the limit resets, or zero when the agent did not say.
	Resets time.Time
}

// transcriptTail is how much of the end of a transcript UsageLimit reads. claude
// writes a refused turn as one short line and follows it with a few lines of
// bookkeeping.
const transcriptTail = 64 << 10

// rateLimitError is the error claude records on a turn that an account's usage
// limit refused (verified on 2.1.308 for a weekly limit and on 2.1.312 for a
// five-hour limit).
const rateLimitError = "rate_limit"

// wrapUp is the usageLimitNote claude gives the message it adds to a turn when
// the account reaches its usage limit partway through (2.1.293 to 2.1.312). The
// model then has a short grace to end the turn. claude writes no
// rateLimitError for it. A later note of another kind withdraws it.
const wrapUp = "wrap_up"

// exitCommandName starts the message claude writes for a person's /exit.
const exitCommandName = "<command-name>/exit</command-name>"

// transcriptLine is the part of a line of claude's session transcript that says
// whose line it is. The format is claude's own and undocumented.
type transcriptLine struct {
	Type        string `json:"type"`
	IsSidechain bool   `json:"isSidechain"`
}

// turn reports whether the line is a message of the session's own conversation.
// The other lines are claude's bookkeeping and sub-agents' messages.
func (l transcriptLine) turn() bool {
	return !l.IsSidechain && (l.Type == "user" || l.Type == "assistant")
}

// systemSource is the promptSource claude gives a message it writes into the
// conversation for a sub-agent, a background task, or another session.
const systemSource = "system"

// transcriptEntry is the part of a message that UsageLimit reads.
type transcriptEntry struct {
	transcriptLine
	// PromptID is the same on every user message of one turn. Assistant
	// messages carry none.
	PromptID            string    `json:"promptId"`
	PromptSource        string    `json:"promptSource"`
	IsMeta              bool      `json:"isMeta"`
	QueueTranscriptOnly bool      `json:"queueTranscriptOnly"`
	UsageLimitNote      string    `json:"usageLimitNote"`
	Timestamp           time.Time `json:"timestamp"`
	Message             struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	IsAPIErrorMessage bool   `json:"isApiErrorMessage"`
	Error             string `json:"error"`
	APIErrorParams    struct {
		RateLimitInfo struct {
			ResetsAt int64 `json:"resetsAt"`
		} `json:"rate_limit_info"`
	} `json:"apiErrorParams"`
}

func (e transcriptEntry) refused() bool {
	return e.Type == "assistant" && e.IsAPIErrorMessage && e.Error == rateLimitError
}

// queuedWithoutTurn reports whether claude wrote the message itself and runs no
// turn for it. claude runs a turn for most of its own messages. It sets
// queueTranscriptOnly on the ones it does not. A meta message with no source is
// a companion claude writes ahead of one of its own. A person's message is never
// meta.
func (e transcriptEntry) queuedWithoutTurn() bool {
	return e.Type == "user" && e.QueueTranscriptOnly && (e.PromptSource == systemSource || e.PromptSource == "" && e.IsMeta)
}

// exits reports whether the message is a person's /exit. claude runs no turn for
// it. claude writes the command with no source, and it writes the command's
// caveat and output as messages of their own under the same promptId.
func (e transcriptEntry) exits() bool {
	var content string
	return e.Type == "user" && e.PromptID != "" && e.PromptSource == "" && json.Unmarshal(e.Message.Content, &content) == nil && strings.HasPrefix(content, exitCommandName)
}

// contentBlock is the part of a block of a message's content that callsTool
// reads.
type contentBlock struct {
	Type string `json:"type"`
}

// callsTool reports whether the message asks for a tool. A turn whose last
// message does has not ended. It waits for the tool's result, possibly at a
// permission prompt.
func (e transcriptEntry) callsTool() bool {
	var blocks []contentBlock
	return json.Unmarshal(e.Message.Content, &blocks) == nil && slices.Contains(blocks, contentBlock{Type: "tool_use"})
}

// UsageLimit reads the session's transcript. claude names the transcript's
// directory after the session's working directory, so the glob finds the file
// without depending on how claude spells that name. A session id that is not a
// UUID cannot name a file outside that directory.
func (Claude) UsageLimit(sessionID string, paths Paths) (UsageLimit, bool) {
	if paths.ClaudeProjects == "" || uuid.Validate(sessionID) != nil {
		return UsageLimit{}, false
	}
	matches, err := filepath.Glob(filepath.Join(paths.ClaudeProjects, "*", sessionID+".jsonl"))
	if err != nil || len(matches) == 0 {
		return UsageLimit{}, false
	}
	turn, ok := lastTurn(matches[0])
	if !ok {
		return UsageLimit{}, false
	}
	if last := turn[0]; last.refused() {
		limit := UsageLimit{At: last.Timestamp}
		if resets := last.APIErrorParams.RateLimitInfo.ResetsAt; resets > 0 {
			limit.Resets = time.Unix(resets, 0)
		}
		return limit, true
	}
	if note, ok := wrappedUp(turn); ok {
		return UsageLimit{At: note.Timestamp}, true
	}
	return UsageLimit{}, false
}

// wrappedUp returns the note claude added to the turn when the account reached
// its usage limit. It reports false unless the turn has ended with the model's
// message and that note is the turn's latest. An API error other than the
// refusal does not end the turn, because claude can retry the request after it.
func wrappedUp(turn []transcriptEntry) (transcriptEntry, bool) {
	if last := turn[0]; last.Type != "assistant" || last.IsAPIErrorMessage || last.callsTool() {
		return transcriptEntry{}, false
	}
	for _, e := range turn {
		if e.UsageLimitNote != "" {
			return e, e.UsageLimitNote == wrapUp
		}
	}
	return transcriptEntry{}, false
}

// lastTurn returns the session's last message, followed by the messages before
// it in the same turn, newest first. It stops at a user message with another
// promptId. When the last message is a user message, or the turn's first user
// message has no promptId, the turn's start is unknown. lastTurn then stops
// after that message. It passes over the messages claude runs no turn for, because
// claude can write them after a turn ends. The model's messages never follow
// one of those directly, so lastTurn stops at one that the model's messages
// follow. It also stops at a message that has a shape docket does not know,
// because that message may be the person continuing or a note that withdraws an
// older one. lastTurn reports false when the tail it reads holds no whole
// message, when claude is still writing the last line, or when it stops before
// it finds the last message. An earlier message must never answer for a later
// one.
func lastTurn(path string) ([]transcriptEntry, bool) {
	tail, ok := readTail(path)
	if !ok {
		return nil, false
	}
	var messages []transcriptEntry
	// undecoded is the index of the latest message that did not decode.
	undecoded := -1
	exits := map[string]bool{}
	for line := range bytes.Lines(tail) {
		var whose transcriptLine
		if json.Unmarshal(line, &whose) != nil || !whose.turn() {
			continue
		}
		var entry transcriptEntry
		if json.Unmarshal(line, &entry) != nil {
			undecoded = len(messages)
		} else if entry.exits() {
			exits[entry.PromptID] = true
		}
		messages = append(messages, entry)
	}

	var turn []transcriptEntry
	prompt := ""
	for i, m := range slices.Backward(messages) {
		if i == undecoded {
			break
		}
		if m.queuedWithoutTurn() || exits[m.PromptID] {
			if len(turn) > 0 && prompt == "" {
				break
			}
			continue
		}
		if m.Type == "user" && prompt != "" && m.PromptID != prompt {
			break
		}
		turn = append(turn, m)
		if m.Type == "user" {
			if len(turn) == 1 || m.PromptID == "" {
				break
			}
			prompt = m.PromptID
		}
	}
	return turn, len(turn) > 0
}

// readTail returns the whole lines at the end of the transcript, up to
// transcriptTail bytes. It reports false when claude is still writing the last
// line.
func readTail(path string) ([]byte, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, false
	}
	// A tail that starts after the beginning of the file reads one byte more.
	// Everything up to the first newline in it is then a line that started
	// before the tail, or the newline that ends one.
	offset := max(0, info.Size()-transcriptTail-1)
	tail := make([]byte, info.Size()-offset)
	if _, err := f.ReadAt(tail, offset); err != nil && err != io.EOF {
		return nil, false
	}
	if !bytes.HasSuffix(tail, []byte("\n")) {
		return nil, false
	}
	if offset > 0 {
		_, tail, _ = bytes.Cut(tail, []byte("\n"))
	}
	return tail, true
}
