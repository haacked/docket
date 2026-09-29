package gh

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/review"
)

var ref = pr.Ref{Org: "haacked", Repo: "docket", Number: 7}

func TestLoginTrimsTheOutput(t *testing.T) {
	fake := &exec.Fake{Results: map[string]exec.Result{"api user": {Stdout: "haacked\n"}}}

	login, err := New(fake).Login(context.Background())
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if login != "haacked" {
		t.Errorf("login = %q", login)
	}
}

func TestLoginFailsLoudlyWhenGhIsNotAuthenticated(t *testing.T) {
	fake := &exec.Fake{Results: map[string]exec.Result{"api user": {Stdout: "\n"}}}

	if _, err := New(fake).Login(context.Background()); err == nil {
		t.Error("Login succeeded on empty output")
	}
}

func TestPRReadsTheFieldsTheCloneNeeds(t *testing.T) {
	body := `{"number":7,"title":"Add a thing","headRefName":"haacked/a-thing","headRefOid":"abc123","isDraft":false,"state":"MERGED","author":{"login":"haacked"}}`
	fake := &exec.Fake{Results: map[string]exec.Result{"pr view": {Stdout: body}}}

	info, err := New(fake).PR(context.Background(), ref)
	if err != nil {
		t.Fatalf("PR: %v", err)
	}

	if info.HeadRefName != "haacked/a-thing" {
		t.Errorf("head branch = %q", info.HeadRefName)
	}
	if info.Author.Login != "haacked" {
		t.Errorf("author = %q", info.Author.Login)
	}
	if info.State != review.PRMerged {
		t.Errorf("state = %q, want %q", info.State, review.PRMerged)
	}
	line := fake.Lines()[0]
	if !strings.Contains(line, "--repo haacked/docket") {
		t.Errorf("command = %s", line)
	}
	if !strings.Contains(line, ",state") {
		t.Errorf("command = %s, want it to ask for the state", line)
	}
}

func TestPRRejectsAResponseWithNoHeadBranch(t *testing.T) {
	fake := &exec.Fake{Results: map[string]exec.Result{"pr view": {Stdout: `{"number":7}`}}}

	if _, err := New(fake).PR(context.Background(), ref); err == nil {
		t.Error("PR accepted a response with no head branch")
	}
}

// Reviews asks for every page at once. gh wraps each page in an outer array, so
// the result is pages to flatten rather than one list.
func TestReviewsFlattensThePages(t *testing.T) {
	body := `[[{"id":1,"user":{"login":"haacked"},"state":"PENDING","submitted_at":null}],` +
		`[{"id":2,"user":{"login":"someone"},"state":"APPROVED","submitted_at":"2026-09-10T12:00:00Z"}]]`
	fake := &exec.Fake{Results: map[string]exec.Result{"pulls/7/reviews": {Stdout: body}}}

	reviews, err := New(fake).Reviews(context.Background(), ref)
	if err != nil {
		t.Fatalf("Reviews: %v", err)
	}

	if len(reviews) != 2 {
		t.Fatalf("reviews = %d, want both pages", len(reviews))
	}
	if reviews[0].ID != 1 || reviews[1].ID != 2 {
		t.Errorf("ids = %d, %d, want 1, 2", reviews[0].ID, reviews[1].ID)
	}
	if reviews[0].SubmittedAt != nil {
		t.Error("a pending review has no submitted_at")
	}
	if reviews[1].SubmittedAt == nil {
		t.Fatal("a submitted review should carry submitted_at")
	}
	if line := fake.Lines()[0]; !strings.Contains(line, "--paginate") || !strings.Contains(line, "--slurp") {
		t.Errorf("command = %s, want every page in one call", line)
	}
}

func TestReviewsOnAPullRequestWithNone(t *testing.T) {
	fake := &exec.Fake{Results: map[string]exec.Result{"pulls/7/reviews": {Stdout: ""}}}

	reviews, err := New(fake).Reviews(context.Background(), ref)
	if err != nil {
		t.Fatalf("Reviews: %v", err)
	}
	if len(reviews) != 0 {
		t.Errorf("reviews = %d, want none", len(reviews))
	}
}

func TestSubmitReviewPostsTheEvent(t *testing.T) {
	fake := &exec.Fake{}

	if err := New(fake).SubmitReview(context.Background(), ref, 42, "APPROVE", "looks good"); err != nil {
		t.Fatalf("SubmitReview: %v", err)
	}

	line := fake.Lines()[0]
	for _, want := range []string{"--method POST", "/repos/haacked/docket/pulls/7/reviews/42/events", "event=APPROVE", "body=looks good"} {
		if !strings.Contains(line, want) {
			t.Errorf("command %s is missing %q", line, want)
		}
	}
}

func TestSubmitReviewLeavesOutAnEmptyBody(t *testing.T) {
	fake := &exec.Fake{}

	if err := New(fake).SubmitReview(context.Background(), ref, 42, "COMMENT", ""); err != nil {
		t.Fatalf("SubmitReview: %v", err)
	}
	if strings.Contains(fake.Lines()[0], "body=") {
		t.Errorf("command = %s, want no body argument", fake.Lines()[0])
	}
}

func TestErrorsSayWhichPullRequest(t *testing.T) {
	fake := &exec.Fake{Errs: map[string]error{"pr view": errors.New("gh: not found")}}

	_, err := New(fake).PR(context.Background(), ref)
	if err == nil || !strings.Contains(err.Error(), "haacked/docket#7") {
		t.Errorf("error = %v, want it to name the pull request", err)
	}
}

// The search asks for every page at once, and gh wraps each page's object in an
// outer array, so the items of every page have to be gathered.
func TestReviewRequestsReadsEverySearchPage(t *testing.T) {
	body := `[{"total_count":2,"items":[{"html_url":"https://github.com/haacked/docket/pull/7","title":"Add a thing","draft":true,"user":{"login":"someone"},"updated_at":"2026-09-20T10:00:00Z"}]},` +
		`{"total_count":2,"items":[{"html_url":"https://github.com/PostHog/posthog/pull/42","title":"Fix it","draft":false,"user":{"login":"other"},"updated_at":"2026-09-21T10:00:00Z"}]}]`
	fake := &exec.Fake{Results: map[string]exec.Result{"search/issues": {Stdout: body}}}

	prs, err := New(fake).ReviewRequests(context.Background(), "user-review-requested:haacked")
	if err != nil {
		t.Fatalf("ReviewRequests: %v", err)
	}

	if len(prs) != 2 {
		t.Fatalf("prs = %d, want one from each page", len(prs))
	}
	first := prs[0]
	if first.Ref != ref {
		t.Errorf("ref = %v, want %v read from html_url", first.Ref, ref)
	}
	if first.Ref.URL() != "https://github.com/haacked/docket/pull/7" || first.Title != "Add a thing" || first.Author != "someone" || !first.IsDraft {
		t.Errorf("first = %+v", first)
	}
	if want := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC); !first.UpdatedAt.Equal(want) {
		t.Errorf("updated = %v, want %v", first.UpdatedAt, want)
	}
	if prs[1].Ref.String() != "PostHog/posthog#42" || prs[1].IsDraft {
		t.Errorf("second = %+v", prs[1])
	}
}

// The call uses the REST search endpoint. gh search prs goes through GraphQL,
// which hits a rate limit the REST call does not.
func TestReviewRequestsPassesTheQualifierInsideTheQuery(t *testing.T) {
	fake := &exec.Fake{Results: map[string]exec.Result{"search/issues": {Stdout: `[{"items":[]}]`}}}

	if _, err := New(fake).ReviewRequests(context.Background(), "team-review-requested:PostHog/team-feature-flags"); err != nil {
		t.Fatalf("ReviewRequests: %v", err)
	}

	call := fake.Calls[0]
	var query string
	for _, arg := range call.Args {
		if strings.HasPrefix(arg, "q=") {
			query = arg
		}
	}
	for _, want := range []string{"is:pr", "is:open", "team-review-requested:PostHog/team-feature-flags"} {
		if !strings.Contains(query, want) {
			t.Errorf("q = %q, missing %q", query, want)
		}
	}
	line := call.String()
	for _, want := range []string{"api", "search/issues", "--paginate", "--slurp"} {
		if !strings.Contains(line, want) {
			t.Errorf("command %s is missing %q", line, want)
		}
	}
}

func TestReviewRequestsWithNothingWaiting(t *testing.T) {
	fake := &exec.Fake{Results: map[string]exec.Result{"search/issues": {Stdout: `[{"total_count":0,"items":[]}]`}}}

	prs, err := New(fake).ReviewRequests(context.Background(), "user-review-requested:haacked")
	if err != nil {
		t.Fatalf("ReviewRequests: %v", err)
	}
	if len(prs) != 0 {
		t.Errorf("prs = %d, want none", len(prs))
	}
}

func TestReviewRequestsErrorNamesTheQualifier(t *testing.T) {
	fake := &exec.Fake{Errs: map[string]error{"search/issues": errors.New("gh: rate limited")}}

	_, err := New(fake).ReviewRequests(context.Background(), "team-review-requested:PostHog/team-feature-flags")
	if err == nil || !strings.Contains(err.Error(), "PostHog/team-feature-flags") {
		t.Errorf("error = %v, want it to name the search", err)
	}
}

func TestReviewRequestsFailsOnOutputThatIsNotJSON(t *testing.T) {
	fake := &exec.Fake{Results: map[string]exec.Result{"search/issues": {Stdout: "<html>502 Bad Gateway</html>"}}}

	_, err := New(fake).ReviewRequests(context.Background(), "team-review-requested:PostHog/team-feature-flags")
	if err == nil || !strings.Contains(err.Error(), "PostHog/team-feature-flags") {
		t.Errorf("error = %v, want a parse error that names the qualifier", err)
	}
}

// Every row is a pull request docket may clone or start a session in, so one
// result it cannot read as a pull request fails the whole search rather than
// dropping that row without a word.
func TestReviewRequestsFailsOnAResultThatIsNotAPullRequest(t *testing.T) {
	body := `[{"items":[` +
		`{"html_url":"https://github.com/haacked/docket/pull/7","title":"Add a thing","user":{"login":"someone"},"updated_at":"2026-09-20T10:00:00Z"},` +
		`{"html_url":"https://github.com/haacked/docket/issues/8","title":"Not a pull request","user":{"login":"someone"},"updated_at":"2026-09-20T10:00:00Z"}]}]`
	fake := &exec.Fake{Results: map[string]exec.Result{"search/issues": {Stdout: body}}}

	prs, err := New(fake).ReviewRequests(context.Background(), "user-review-requested:haacked")
	if err == nil || !strings.Contains(err.Error(), "https://github.com/haacked/docket/issues/8") {
		t.Errorf("error = %v, want it to name the URL it could not read", err)
	}
	if prs != nil {
		t.Errorf("prs = %v, want none from a failed search", prs)
	}
}

// gh wraps each page of teams in an outer array. Teams gathers the teams of
// every page.
func TestTeamsReadsEveryPageAsOrgSlashTeam(t *testing.T) {
	body := `[[{"slug":"team-feature-flags","organization":{"login":"PostHog"}}],` +
		`[{"slug":"founders","organization":{"login":"aseriousbiz"}}]]`
	fake := &exec.Fake{Results: map[string]exec.Result{"user/teams": {Stdout: body}}}

	teams, err := New(fake).Teams(context.Background())
	if err != nil {
		t.Fatalf("Teams: %v", err)
	}

	if !slices.Contains(teams, "PostHog/team-feature-flags") || !slices.Contains(teams, "aseriousbiz/founders") || len(teams) != 2 {
		t.Errorf("teams = %v, want one org/team slug from each page", teams)
	}
}

func TestTeamsAsksForEveryPageOfTheUsersTeams(t *testing.T) {
	fake := &exec.Fake{Results: map[string]exec.Result{"user/teams": {Stdout: `[[]]`}}}

	if _, err := New(fake).Teams(context.Background()); err != nil {
		t.Fatalf("Teams: %v", err)
	}

	if len(fake.Calls) != 1 {
		t.Fatalf("calls = %v, want one", fake.Lines())
	}
	line := fake.Calls[0].String()
	for _, want := range []string{"api", "GET", "user/teams", "--paginate", "--slurp", "per_page=100"} {
		if !strings.Contains(line, want) {
			t.Errorf("command %s is missing %q", line, want)
		}
	}
}

func TestTeamsWithNoOutputListsNone(t *testing.T) {
	fake := &exec.Fake{Results: map[string]exec.Result{"user/teams": {Stdout: " \n"}}}

	teams, err := New(fake).Teams(context.Background())
	if err != nil {
		t.Fatalf("Teams: %v", err)
	}
	if teams != nil {
		t.Errorf("teams = %v, want none", teams)
	}
}

func TestTeamsPassesOnTheRunnersFailure(t *testing.T) {
	failure := errors.New("gh: HTTP 403")
	fake := &exec.Fake{Errs: map[string]error{"user/teams": failure}}

	_, err := New(fake).Teams(context.Background())
	if !errors.Is(err, failure) {
		t.Errorf("error = %v, want it to wrap %v", err, failure)
	}
}

func TestTeamsFailsOnOutputThatIsNotJSON(t *testing.T) {
	fake := &exec.Fake{Results: map[string]exec.Result{"user/teams": {Stdout: "<html>502 Bad Gateway</html>"}}}

	if _, err := New(fake).Teams(context.Background()); err == nil {
		t.Error("Teams succeeded on output that is not JSON")
	}
}
