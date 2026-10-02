package gh

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/haacked/docket/internal/core/exec"
)

// A fix review can only push to a head branch in the base repository, so
// resolve needs to know whether the head is in a fork.
func TestPRReadsWhetherTheHeadIsInAFork(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "head in a fork", body: `{"number":7,"headRefName":"patch-1","isCrossRepository":true}`, want: true},
		{name: "head in the base repository", body: `{"number":7,"headRefName":"posthog/fix","isCrossRepository":false}`, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &exec.Fake{Results: map[string]exec.Result{"pr view": {Stdout: tt.body}}}

			info, err := New(fake).PR(context.Background(), ref)
			if err != nil {
				t.Fatalf("PR: %v", err)
			}
			if info.IsCrossRepository != tt.want {
				t.Errorf("cross repository = %v, want %v", info.IsCrossRepository, tt.want)
			}
		})
	}
}

func TestPRAsksForWhetherTheHeadIsInAFork(t *testing.T) {
	fake := &exec.Fake{Results: map[string]exec.Result{"pr view": {Stdout: `{"number":7,"headRefName":"topic"}`}}}

	if _, err := New(fake).PR(context.Background(), ref); err != nil {
		t.Fatalf("PR: %v", err)
	}
	if line := fake.Lines()[0]; !strings.Contains(line, "isCrossRepository") {
		t.Errorf("command = %s, want it to ask for isCrossRepository", line)
	}
}

// A pushed fix review has no pending review to submit. Approving posts a new
// review, pinned to the commit the fixes were made on, so GitHub does not read
// it as an approval of whatever the head is by then.
func TestCreateReviewPostsASubmittedReviewOnTheCommit(t *testing.T) {
	fake := &exec.Fake{}

	if err := New(fake).CreateReview(context.Background(), ref, "abc123", "APPROVE", "fixed the retry loop"); err != nil {
		t.Fatalf("CreateReview: %v", err)
	}

	line := fake.Lines()[0]
	for _, want := range []string{
		"--method POST",
		"/repos/haacked/docket/pulls/7/reviews",
		"commit_id=abc123",
		"event=APPROVE",
		"body=fixed the retry loop",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("command %s is missing %q", line, want)
		}
	}
	if strings.Contains(line, "/events") {
		t.Errorf("command %s posts to a pending review's events, want a new review", line)
	}
}

// GitHub accepts an approval with no body, and an empty -f body= would send an
// empty string rather than leave the field out.
func TestCreateReviewLeavesOutAnEmptyBody(t *testing.T) {
	fake := &exec.Fake{}

	if err := New(fake).CreateReview(context.Background(), ref, "abc123", "APPROVE", ""); err != nil {
		t.Fatalf("CreateReview: %v", err)
	}

	if line := fake.Lines()[0]; strings.Contains(line, "body=") {
		t.Errorf("command %s sends a body, want none", line)
	}
}

func TestCreateReviewReportsWhatGitHubRefused(t *testing.T) {
	fake := &exec.Fake{Errs: map[string]error{"pulls/7/reviews": errors.New("HTTP 422: Can not approve your own pull request")}}

	err := New(fake).CreateReview(context.Background(), ref, "abc123", "APPROVE", "")
	if err == nil || !strings.Contains(err.Error(), "422") {
		t.Errorf("CreateReview answered %v, want GitHub's refusal", err)
	}
}

// A fix review of a bot's pull request assigns the user, unless the user is
// already an assignee. gh pr view names a GitHub App app/<name> and marks it a
// bot.
func TestPRReadsWhetherABotOpenedItAndWhoIsAssigned(t *testing.T) {
	fake := &exec.Fake{Results: map[string]exec.Result{"pr view": {Stdout: `{
		"number": 7,
		"headRefName": "posthog/fix",
		"author": {"is_bot": true, "login": "app/posthog"},
		"assignees": [{"id": "MDQ6VXNlcjE5OTc3", "login": "haacked", "name": "Phil Haack"}]
	}`}}}

	info, err := New(fake).PR(context.Background(), ref)
	if err != nil {
		t.Fatalf("PR: %v", err)
	}
	if !info.Author.IsBot {
		t.Error("an author gh marks is_bot did not read as a bot")
	}
	if len(info.Assignees) != 1 || info.Assignees[0].Login != "haacked" {
		t.Errorf("assignees = %+v, want haacked", info.Assignees)
	}
}

// The assignees endpoint adds to the pull request's assignees and keeps the
// ones already there.
func TestAddAssigneePostsTheLogin(t *testing.T) {
	fake := &exec.Fake{}

	if err := New(fake).AddAssignee(context.Background(), ref, "haacked"); err != nil {
		t.Fatalf("AddAssignee: %v", err)
	}

	line := fake.Lines()[0]
	for _, want := range []string{
		"--method POST",
		"/repos/haacked/docket/issues/7/assignees",
		"assignees[]=haacked",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("command %s is missing %q", line, want)
		}
	}
}

func TestAddAssigneeReportsWhatGitHubRefused(t *testing.T) {
	fake := &exec.Fake{Errs: map[string]error{"issues/7/assignees": errors.New("HTTP 403: Resource not accessible by integration")}}

	err := New(fake).AddAssignee(context.Background(), ref, "haacked")
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("AddAssignee answered %v, want GitHub's refusal", err)
	}
}
