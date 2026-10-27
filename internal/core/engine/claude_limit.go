package engine

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
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
// limit refused (verified on 2.1.308, for a weekly limit).
const rateLimitError = "rate_limit"

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
	PromptSource        string    `json:"promptSource"`
	QueueTranscriptOnly bool      `json:"queueTranscriptOnly"`
	Timestamp           time.Time `json:"timestamp"`
	IsAPIErrorMessage   bool      `json:"isApiErrorMessage"`
	Error               string    `json:"error"`
	APIErrorParams      struct {
		RateLimitInfo struct {
			ResetsAt int64 `json:"resetsAt"`
		} `json:"rate_limit_info"`
	} `json:"apiErrorParams"`
}

// queuedWithoutTurn reports whether claude wrote the message itself and runs no
// turn for it. claude runs a turn for most of its own messages. It sets
// queueTranscriptOnly on the ones it does not.
func (e transcriptEntry) queuedWithoutTurn() bool {
	return e.Type == "user" && e.PromptSource == systemSource && e.QueueTranscriptOnly
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
	entry, ok := lastTurn(matches[0])
	if !ok || entry.Type != "assistant" || !entry.IsAPIErrorMessage || entry.Error != rateLimitError {
		return UsageLimit{}, false
	}
	limit := UsageLimit{At: entry.Timestamp}
	if resets := entry.APIErrorParams.RateLimitInfo.ResetsAt; resets > 0 {
		limit.Resets = time.Unix(resets, 0)
	}
	return limit, true
}

// lastTurn returns the last message of the session's own conversation, passing
// over a message that claude wrote itself and runs no turn for. claude can write
// one after a refused turn. lastTurn reports false when the tail it reads holds
// no whole message, when claude is still writing the last line, or when the last
// message has a shape docket does not know. An earlier message must never answer
// for a later one.
func lastTurn(path string) (transcriptEntry, bool) {
	f, err := os.Open(path)
	if err != nil {
		return transcriptEntry{}, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return transcriptEntry{}, false
	}
	// A tail that starts after the beginning of the file reads one byte more.
	// Everything up to the first newline in it is then a line that started
	// before the tail, or the newline that ends one.
	offset := max(0, info.Size()-transcriptTail-1)
	tail := make([]byte, info.Size()-offset)
	if _, err := f.ReadAt(tail, offset); err != nil && err != io.EOF {
		return transcriptEntry{}, false
	}
	if !bytes.HasSuffix(tail, []byte("\n")) {
		return transcriptEntry{}, false
	}
	if offset > 0 {
		_, tail, _ = bytes.Cut(tail, []byte("\n"))
	}

	for _, line := range slices.Backward(slices.Collect(bytes.Lines(tail))) {
		var whose transcriptLine
		if json.Unmarshal(line, &whose) != nil || !whose.turn() {
			continue
		}
		var entry transcriptEntry
		if json.Unmarshal(line, &entry) != nil {
			return transcriptEntry{}, false
		}
		if entry.queuedWithoutTurn() {
			continue
		}
		return entry, true
	}
	return transcriptEntry{}, false
}
