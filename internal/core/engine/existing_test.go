package engine

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/haacked/docket/internal/core/review"
)

// intentFlags is what each intent adds after --draft. A plain review and an ask
// add nothing: a plain review only happens when there were no notes to prompt
// about, and an ask never invokes review-code at all.
var intentFlags = []struct {
	intent  review.Intent
	want    string
	refused []string
}{
	{intent: review.IntentReview, refused: []string{"--append", "--overwrite"}},
	{intent: review.IntentAppend, want: "--append", refused: []string{"--overwrite"}},
	{intent: review.IntentOverwrite, want: "--overwrite", refused: []string{"--append"}},
}

func intentRecord(intent review.Intent) review.Record {
	rec := record()
	rec.Intent = intent
	rec.NotesPath = "/opt/review-code/.reviews/haacked/docket/pr-7.md"
	return rec
}

func TestReviewArgsCarryTheIntentsFlag(t *testing.T) {
	for _, tt := range intentFlags {
		t.Run(string(tt.intent), func(t *testing.T) {
			args := reviewArgs(intentRecord(tt.intent))

			if !strings.HasPrefix(args, "https://github.com/haacked/docket/pull/7 --draft") {
				t.Errorf("args = %q, want the URL and --draft first", args)
			}
			if tt.want != "" && !strings.Contains(args, tt.want) {
				t.Errorf("args = %q, want %q so review-code skips its existing-notes prompt", args, tt.want)
			}
			for _, flag := range tt.refused {
				if strings.Contains(args, flag) {
					t.Errorf("args = %q, want no %q", args, flag)
				}
			}
		})
	}
}

func TestReviewArgsKeepSelfAlongsideTheIntent(t *testing.T) {
	rec := intentRecord(review.IntentAppend)
	rec.OwnPR = true

	args := reviewArgs(rec)

	for _, want := range []string{"--self", "--append"} {
		if !strings.Contains(args, want) {
			t.Errorf("args = %q, want %q", args, want)
		}
	}
}

func TestEveryStartCarriesTheIntentsFlag(t *testing.T) {
	starts := map[string]func(review.Record) string{
		"claude": func(rec review.Record) string { return Claude{}.Start(rec, Paths{Grant: grants}).String() },
		"codex": func(rec review.Record) string {
			rec.Engine = "codex"
			rec.SessionID = ""
			return Codex{}.Start(rec, Paths{Grant: grants}).String()
		},
		"claude background": func(rec review.Record) string {
			return Claude{}.StartBackground(rec, Paths{Grant: grants}).String()
		},
	}

	for name, start := range starts {
		for _, tt := range intentFlags {
			if tt.want == "" {
				continue
			}
			t.Run(name+" "+string(tt.intent), func(t *testing.T) {
				line := start(intentRecord(tt.intent))

				if !strings.Contains(line, tt.want) {
					t.Errorf("command %s is missing %q", line, tt.want)
				}
				for _, flag := range tt.refused {
					if strings.Contains(line, flag) {
						t.Errorf("command %s carries %q as well", line, flag)
					}
				}
			})
		}
	}
}

// The record's intent sets the flag. A notes file on disk adds none on its own.
// A background start still answers the pre-flight prompt with --force.
func TestABackgroundStartTakesItsFlagFromTheIntentNotTheNotesFile(t *testing.T) {
	rec := intentRecord(review.IntentReview)
	rec.NotesPath = filepath.Join(t.TempDir(), "pr-7.md")
	if err := os.WriteFile(rec.NotesPath, []byte("# an earlier review\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	line := Claude{}.StartBackground(rec, Paths{}).String()

	if strings.Contains(line, "--append") {
		t.Errorf("command %s appends because a notes file exists, want the intent to decide", line)
	}
	if !strings.Contains(line, "--force") {
		t.Errorf("command %s dropped --force", line)
	}
}

func TestABackgroundOverwriteForcesAndOverwrites(t *testing.T) {
	line := Claude{}.StartBackground(intentRecord(review.IntentOverwrite), Paths{}).String()

	for _, want := range []string{"--force", "--overwrite"} {
		if !strings.Contains(line, want) {
			t.Errorf("command %s is missing %q", line, want)
		}
	}
	if strings.Contains(line, "--append") {
		t.Errorf("command %s appends as well as overwriting", line)
	}
}

// promptOf is the one argument that is not a flag, which is where the question
// for the agent goes.
func promptOf(t *testing.T, args []string) string {
	t.Helper()
	if len(args) == 0 {
		t.Fatal("the ask command has no arguments")
	}
	return args[len(args)-1]
}

// An ask reads the notes and answers questions. Any review-code flag would turn
// it into a review, and --draft or --force would post or skip a prompt the user
// never saw.
func assertNoReview(t *testing.T, line string) {
	t.Helper()
	// The notes path sits under review-code's own directory, so the invocation is
	// matched with the character that follows the skill name.
	for _, banned := range []string{"/review-code ", "$review-code", "--draft", "--self", "--force", "--append", "--overwrite", "--resume"} {
		if strings.Contains(line, banned) {
			t.Errorf("ask command %s carries %q", line, banned)
		}
	}
}

// The Q&A session has an id of its own, so asking never replaces the review
// session enter resumes.
func TestClaudeAskRunsInTheRecordsDirectoryUnderItsOwnSession(t *testing.T) {
	rec := intentRecord(review.IntentAsk)
	rec.OwnPR = true
	rec.AskSessionID = "a5c0a5c0-0000-4000-8000-000000000002"

	spec := Claude{}.Ask(rec, Paths{Grant: grants})

	if spec.Path != "claude" {
		t.Errorf("path = %q, want claude", spec.Path)
	}
	if spec.Dir != rec.Dir {
		t.Errorf("dir = %q, want %q", spec.Dir, rec.Dir)
	}
	if !strings.Contains(spec.String(), "--session-id "+rec.AskSessionID) {
		t.Errorf("command %s does not carry the ask's session id, so it cannot be resumed", spec)
	}
	if strings.Contains(spec.String(), rec.SessionID) {
		t.Errorf("command %s runs under the review's session id", spec)
	}
	assertNoReview(t, spec.String())
}

func TestClaudeAskPromptNamesTheNotesAndThePullRequest(t *testing.T) {
	rec := intentRecord(review.IntentAsk)

	prompt := promptOf(t, Claude{}.Ask(rec, Paths{}).Args)

	for _, want := range []string{rec.NotesPath, rec.URL, "Do not post anything to GitHub"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt %q is missing %q", prompt, want)
		}
	}
}

func TestClaudeAskWithoutAnIdPassesNoEmptySessionID(t *testing.T) {
	rec := intentRecord(review.IntentAsk)
	rec.AskSessionID = ""

	line := Claude{}.Ask(rec, Paths{}).String()

	if strings.Contains(line, "--session-id") {
		t.Errorf("command = %s, want no empty session id", line)
	}
}

func TestCodexAskRunsWithTheSameDirectoriesAsAReview(t *testing.T) {
	rec := codexRecord("/tmp/clone", time.Now())
	rec.Intent = review.IntentAsk
	rec.NotesPath = "/opt/review-code/.reviews/haacked/docket/pr-7.md"
	rec.OwnPR = true

	spec := Codex{}.Ask(rec, Paths{Grant: grants})

	if spec.Path != "codex" {
		t.Errorf("path = %q, want codex", spec.Path)
	}
	if spec.Dir != "/tmp/clone" {
		t.Errorf("dir = %q, want the record's directory", spec.Dir)
	}
	if !slices.Equal(spec.Unset, scrubbed) {
		t.Errorf("unset = %v, want %v so codex launched from a claude session still runs", spec.Unset, scrubbed)
	}
	// The notes live under review-code's directory, outside the working root, so
	// the ask needs the same grants a review gets.
	if want := codexDirs("/tmp/clone", Paths{Grant: grants}); !slices.Equal(spec.Args[:len(want)], want) {
		t.Errorf("args = %v, want them to start with %v", spec.Args, want)
	}
	assertNoReview(t, spec.String())
}

func TestCodexAskPromptNamesTheNotesAndThePullRequest(t *testing.T) {
	rec := codexRecord("/tmp/clone", time.Now())
	rec.Intent = review.IntentAsk
	rec.NotesPath = "/opt/review-code/.reviews/haacked/docket/pr-7.md"

	prompt := promptOf(t, Codex{}.Ask(rec, Paths{Grant: grants}).Args)

	for _, want := range []string{rec.NotesPath, rec.URL} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt %q is missing %q", prompt, want)
		}
	}
}
