package gh

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/haacked/docket/internal/core/exec"
)

// author_association is relative to the pull request's repository. Only a
// member or an owner of its organization counts as in the org.
func TestReviewRequestsReadsWhetherTheAuthorIsInTheOrg(t *testing.T) {
	tests := []struct {
		association string
		want        bool
	}{
		{"MEMBER", true},
		{"OWNER", true},
		{"COLLABORATOR", false},
		{"CONTRIBUTOR", false},
		{"FIRST_TIME_CONTRIBUTOR", false},
		{"FIRST_TIMER", false},
		{"NONE", false},
		{"MANNEQUIN", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.association, func(t *testing.T) {
			item := `{"html_url":"https://github.com/haacked/docket/pull/7","user":{"login":"x","type":"User"},"updated_at":"2026-09-20T10:00:00Z"`
			if tt.association != "" {
				item += `,"author_association":"` + tt.association + `"`
			}
			fake := &exec.Fake{Results: map[string]exec.Result{"search/issues": {Stdout: `[{"items":[` + item + `}]}]`}}}

			prs, err := New(fake).ReviewRequests(context.Background(), "reviewed-by:haacked -author:haacked")
			if err != nil {
				t.Fatalf("ReviewRequests: %v", err)
			}

			if len(prs) != 1 || prs[0].AuthorInOrg != tt.want {
				t.Errorf("prs = %+v, want one with AuthorInOrg %v", prs, tt.want)
			}
		})
	}
}

// timelinePages is two pages of the timeline of haacked/docket#7, as gh
// --slurp wraps them. Every event that is not a commit or a force push carries
// a field a careless parse could mistake for one: a review names the commit it
// was on, and a force push names the head after it.
const timelinePages = `[[` +
	`{"event":"committed","sha":"aaaa111","author":{"date":"2026-11-14T10:00:00Z"},"committer":{"date":"2026-11-18T05:37:00Z"}},` +
	`{"event":"reviewed","id":1,"commit_id":"aaaa111","submitted_at":"2026-11-15T09:00:00Z","state":"commented"},` +
	`{"event":"commented","created_at":"2026-11-15T10:00:00Z","body":"Please fix"},` +
	`{"event":"head_ref_force_pushed","created_at":"2026-11-16T05:43:33Z","commit_id":"bbbb222"}` +
	`],[` +
	`{"event":"committed","sha":"bbbb222"},` +
	`{"event":"labeled","created_at":"2026-11-17T01:00:00Z","label":{"name":"bug"}},` +
	`{"event":"committed","sha":"cccc333"},` +
	`{"event":"head_ref_force_pushed","created_at":"2026-11-18T05:43:33Z","commit_id":"cccc333"}` +
	`]]`

func TestTimelineKeepsTheCommitsInOrderAcrossPages(t *testing.T) {
	fake := &exec.Fake{Results: map[string]exec.Result{"timeline": {Stdout: timelinePages}}}

	got, err := New(fake).Timeline(context.Background(), ref)
	if err != nil {
		t.Fatalf("Timeline: %v", err)
	}

	if want := []string{"aaaa111", "bbbb222", "cccc333"}; !slices.Equal(got.Commits, want) {
		t.Errorf("commits = %v, want %v", got.Commits, want)
	}
}

func TestTimelineTakesTheTimeOfEachForcePush(t *testing.T) {
	fake := &exec.Fake{Results: map[string]exec.Result{"timeline": {Stdout: timelinePages}}}

	got, err := New(fake).Timeline(context.Background(), ref)
	if err != nil {
		t.Fatalf("Timeline: %v", err)
	}

	want := []time.Time{
		time.Date(2026, 11, 16, 5, 43, 33, 0, time.UTC),
		time.Date(2026, 11, 18, 5, 43, 33, 0, time.UTC),
	}
	if !slices.EqualFunc(got.ForcePushes, want, time.Time.Equal) {
		t.Errorf("force pushes = %v, want %v", got.ForcePushes, want)
	}
}

func TestTimelineReadsEveryPageOfThePullRequestsTimeline(t *testing.T) {
	fake := &exec.Fake{Results: map[string]exec.Result{"timeline": {Stdout: `[[]]`}}}

	if _, err := New(fake).Timeline(context.Background(), ref); err != nil {
		t.Fatalf("Timeline: %v", err)
	}

	line := fake.Calls[0].String()
	for _, want := range []string{"api", "GET", "repos/haacked/docket/issues/7/timeline", "--paginate", "--slurp", "per_page=100"} {
		if !strings.Contains(line, want) {
			t.Errorf("command %s is missing %q", line, want)
		}
	}
}

func TestTimelineWithNoOutputHasNoCommitsOrForcePushes(t *testing.T) {
	fake := &exec.Fake{Results: map[string]exec.Result{"timeline": {Stdout: "\n"}}}

	got, err := New(fake).Timeline(context.Background(), ref)
	if err != nil {
		t.Fatalf("Timeline: %v", err)
	}
	if len(got.Commits) != 0 || len(got.ForcePushes) != 0 {
		t.Errorf("timeline = %+v, want it empty", got)
	}
}

func TestTimelineWrapsAParseErrorAndNamesThePullRequest(t *testing.T) {
	fake := &exec.Fake{Results: map[string]exec.Result{"timeline": {Stdout: "<html>502 Bad Gateway</html>"}}}

	_, err := New(fake).Timeline(context.Background(), ref)

	var syntax *json.SyntaxError
	if !errors.As(err, &syntax) {
		t.Errorf("error = %v, want it to wrap the JSON error", err)
	}
	if err == nil || !strings.Contains(err.Error(), ref.String()) {
		t.Errorf("error = %v, want it to name %s", err, ref)
	}
}

func TestTimelinePassesOnTheRunnersFailure(t *testing.T) {
	failure := errors.New("gh: HTTP 403: rate limited")
	fake := &exec.Fake{Errs: map[string]error{"timeline": failure}}

	_, err := New(fake).Timeline(context.Background(), ref)

	if !errors.Is(err, failure) {
		t.Errorf("error = %v, want it to wrap %v", err, failure)
	}
	if err == nil || !strings.Contains(err.Error(), ref.String()) {
		t.Errorf("error = %v, want it to name %s", err, ref)
	}
}
