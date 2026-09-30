package tui

import (
	"strings"
	"testing"

	"github.com/haacked/docket/internal/core/config"
	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/tui/msg"
)

func pushedFixRecord() review.Record {
	rec := draftedRecord()
	rec.State = review.StatePushed
	rec.ReviewID = 0
	rec.Fix = true
	rec.FixBase = "base-sha"
	rec.FixHead = "pushed-sha"
	return rec
}

// A pushed fix review has no pending review, so there is no summary to read
// into the body, and reading one would ask GitHub for nothing.
func TestOpeningSubmitForAPushedRowReadsNoDraft(t *testing.T) {
	rec := pushedFixRecord()

	next, cmd := liveApp(rec).Update(msg.OpenSubmit{ID: rec.ID})
	a := next.(App)

	if a.screen != msg.Submit {
		t.Fatalf("screen = %v, want the submit screen", a.screen)
	}
	if cmd != nil {
		t.Errorf("opening the submit screen for a pushed row ran %#v, want no read of a draft", cmd())
	}
	if a.sub.Event != review.EventApprove {
		t.Errorf("event = %q, want approve first", a.sub.Event)
	}
	if got := a.sub.Body.Value(); got != "" {
		t.Errorf("body = %q, want it empty", got)
	}
}

// The status line says what to do next once a row lands in a fix state.
func TestDescribeSaysWhatToDoWithAFixRow(t *testing.T) {
	fixed := pushedFixRecord()
	fixed.State = review.StateFixed
	pushed := pushedFixRecord()
	unchanged := pushedFixRecord()
	unchanged.FixHead = unchanged.FixBase

	tests := []struct {
		name  string
		rec   review.Record
		wants []string
	}{
		{name: "fixed", rec: fixed, wants: []string{"enter", "push", fixed.Dir}},
		{name: "pushed", rec: pushed, wants: []string{"s to approve"}},
		{name: "pushed with no changes", rec: unchanged, wants: []string{"s to approve"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := describe(tt.rec)
			for _, want := range tt.wants {
				if !strings.Contains(got, want) {
					t.Errorf("describe(%s) = %q, want it to mention %q", tt.name, got, want)
				}
			}
		})
	}
}

// A row with no changes approves the pull request as its author left it, so
// the status line should not read like one with fixes.
func TestDescribeTellsAPushedRowWithNoChangesApart(t *testing.T) {
	pushed := pushedFixRecord()
	unchanged := pushedFixRecord()
	unchanged.FixHead = unchanged.FixBase

	if describe(pushed) == describe(unchanged) {
		t.Errorf("describe reads the same with and without changes: %q", describe(pushed))
	}
}

// A batch from the requests screen offers no fix toggle, so fix_authors
// decides for each pull request on its own.
func TestABatchLetsFixAuthorsDecideForEachPullRequest(t *testing.T) {
	tests := []struct {
		name    string
		authors []string
		want    string
		refused string
	}{
		{name: "author listed", authors: []string{"someone"}, want: "--fix", refused: "--draft"},
		{name: "author not listed", authors: nil, want: "--draft", refused: "--fix"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _ := batchService(t)
			svc.Cfg.GitHubUser = "haacked"
			svc.Cfg.FixAuthors = tt.authors
			a := New(svc, config.Config{DefaultEngine: "claude"}, "", true)

			_, cmd := check(t, a, "https://github.com/haacked/docket/pull/7")

			var report strings.Builder
			for _, m := range drain(cmd) {
				switch m := m.(type) {
				case statusMsg:
					report.WriteString(m.text + "\n")
				case errMsg:
					t.Fatalf("the dry run failed: %v", m.err)
				}
			}
			if !strings.Contains(report.String(), tt.want) || strings.Contains(report.String(), tt.refused) {
				t.Errorf("the report is %q, want %s and no %s", report.String(), tt.want, tt.refused)
			}
		})
	}
}

// ctrl+f on the new review screen reaches the review the root starts. A dry
// run shows the command that would run.
func TestTheNewReviewScreensFixChoiceReachesTheReview(t *testing.T) {
	tests := []struct {
		fix     string
		authors []string
		want    string
		refused string
	}{
		{fix: string(review.FixOn), authors: nil, want: "--fix", refused: "--draft"},
		{fix: string(review.FixOff), authors: []string{"someone"}, want: "--draft", refused: "--fix"},
		{fix: string(review.FixAuto), authors: []string{"someone"}, want: "--fix", refused: "--draft"},
	}

	for _, tt := range tests {
		name := tt.fix
		if name == "" {
			name = "auto"
		}
		t.Run(name, func(t *testing.T) {
			svc, _ := batchService(t)
			svc.Cfg.GitHubUser = "haacked"
			svc.Cfg.FixAuthors = tt.authors
			a := New(svc, config.Config{DefaultEngine: "claude"}, "", true)

			_, cmd := a.Update(msg.StartReview{Input: "https://github.com/haacked/docket/pull/7", Engine: "claude", Fix: tt.fix})

			var report strings.Builder
			for _, m := range drain(cmd) {
				switch m := m.(type) {
				case statusMsg:
					report.WriteString(m.text + "\n")
				case errMsg:
					t.Fatalf("the dry run failed: %v", m.err)
				}
			}
			if !strings.Contains(report.String(), tt.want) || strings.Contains(report.String(), tt.refused) {
				t.Errorf("the report is %q, want %s and no %s", report.String(), tt.want, tt.refused)
			}
		})
	}
}
