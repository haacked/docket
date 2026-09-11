package gh

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/pr"
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
	body := `{"number":7,"title":"Add a thing","headRefName":"haacked/a-thing","headRefOid":"abc123","isDraft":false,"author":{"login":"haacked"}}`
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
	if line := fake.Lines()[0]; !strings.Contains(line, "--repo haacked/docket") {
		t.Errorf("command = %s", line)
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
