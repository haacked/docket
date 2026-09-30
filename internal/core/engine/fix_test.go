package engine

import (
	"strings"
	"testing"

	"github.com/haacked/docket/internal/core/review"
)

func fixRecord() review.Record {
	rec := record()
	rec.Fix = true
	return rec
}

// review-code edits the checkout for --fix and posts nothing unless --draft is
// also passed. A fix review that passed --draft would post a review the fixes
// are meant to replace.
func TestReviewArgsForAFixReviewPassFixInPlaceOfDraft(t *testing.T) {
	args := reviewArgs(fixRecord())

	if !strings.HasPrefix(args, "https://github.com/haacked/docket/pull/7 --fix") {
		t.Errorf("args = %q, want the URL and --fix first", args)
	}
	if strings.Contains(args, "--draft") {
		t.Errorf("args = %q, want no --draft", args)
	}
}

// --self lets review-code draft a review of your own pull request. A fix review
// drafts nothing, so the flag has nothing to allow.
func TestReviewArgsForAFixReviewOfYourOwnPullRequestLeaveOutSelf(t *testing.T) {
	rec := fixRecord()
	rec.OwnPR = true

	args := reviewArgs(rec)

	if strings.Contains(args, "--self") {
		t.Errorf("args = %q, want no --self", args)
	}
	if !strings.Contains(args, "--fix") {
		t.Errorf("args = %q, want --fix", args)
	}
}

// The existing-notes prompt is review-code's either way, so the intent's flag
// still answers it.
func TestReviewArgsForAFixReviewKeepTheIntentsFlag(t *testing.T) {
	for _, tt := range intentFlags {
		t.Run(string(tt.intent), func(t *testing.T) {
			rec := intentRecord(tt.intent)
			rec.Fix = true

			args := reviewArgs(rec)

			if !strings.Contains(args, "--fix") {
				t.Errorf("args = %q, want --fix", args)
			}
			if tt.want != "" && !strings.Contains(args, tt.want) {
				t.Errorf("args = %q, want %q", args, tt.want)
			}
			for _, flag := range tt.refused {
				if strings.Contains(args, flag) {
					t.Errorf("args = %q, want no %q", args, flag)
				}
			}
		})
	}
}

// A draft review is untouched by fix mode.
func TestReviewArgsForADraftReviewStillPassDraftAndSelf(t *testing.T) {
	rec := record()
	rec.OwnPR = true

	args := reviewArgs(rec)

	for _, want := range []string{"--draft", "--self"} {
		if !strings.Contains(args, want) {
			t.Errorf("args = %q, want %q", args, want)
		}
	}
	if strings.Contains(args, "--fix") {
		t.Errorf("args = %q, want no --fix", args)
	}
}

// A fix review runs in the terminal and in the background alike. Nobody is at
// the terminal to answer the pre-flight prompt in the background, so --force
// stays.
func TestEveryStartOfAFixReviewPassesFix(t *testing.T) {
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
		t.Run(name, func(t *testing.T) {
			rec := intentRecord(review.IntentAppend)
			rec.Fix = true

			line := start(rec)

			if !strings.Contains(line, rec.URL+" --fix") {
				t.Errorf("command %s does not run the review with --fix", line)
			}
			if strings.Contains(line, "--draft") {
				t.Errorf("command %s carries --draft", line)
			}
			if !strings.Contains(line, "--append") {
				t.Errorf("command %s dropped --append", line)
			}
		})
	}
}

func TestABackgroundFixReviewKeepsForce(t *testing.T) {
	line := Claude{}.StartBackground(fixRecord(), Paths{}).String()

	if !strings.Contains(line, "--fix") || !strings.Contains(line, "--force") {
		t.Errorf("command %s, want both --fix and --force", line)
	}
}
