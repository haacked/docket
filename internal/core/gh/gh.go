// Package gh reads and writes the GitHub facts docket depends on, by shelling
// out to the gh CLI so it inherits the user's existing authentication.
package gh

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/review"
)

// PRInfo is the pull request metadata docket needs to clone and to label a row.
type PRInfo struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	HeadRefName string `json:"headRefName"`
	Author      struct {
		Login string `json:"login"`
	} `json:"author"`
}

// GitHub is the GitHub access docket needs. Tests substitute a fake.
type GitHub interface {
	Login(ctx context.Context) (string, error)
	PR(ctx context.Context, ref pr.Ref) (PRInfo, error)
	Reviews(ctx context.Context, ref pr.Ref) ([]review.GHReview, error)
	SubmitReview(ctx context.Context, ref pr.Ref, reviewID int64, event, body string) error
}

// CLI talks to GitHub through the gh command.
type CLI struct {
	Runner exec.Runner
	Path   string
}

func New(runner exec.Runner) *CLI { return &CLI{Runner: runner, Path: "gh"} }

func (c *CLI) Login(ctx context.Context) (string, error) {
	res, err := c.Runner.Run(ctx, exec.CommandSpec{
		Path: cmp.Or(c.Path, "gh"),
		Args: []string{"api", "user", "--jq", ".login"},
	})
	if err != nil {
		return "", fmt.Errorf("gh api user: %w", err)
	}
	login := strings.TrimSpace(res.Stdout)
	if login == "" {
		return "", fmt.Errorf("gh api user returned no login; is gh authenticated?")
	}
	return login, nil
}

func (c *CLI) PR(ctx context.Context, ref pr.Ref) (PRInfo, error) {
	res, err := c.Runner.Run(ctx, exec.CommandSpec{
		Path: cmp.Or(c.Path, "gh"),
		Args: []string{
			"pr", "view", strconv.Itoa(ref.Number),
			"--repo", ref.Slug(),
			"--json", "number,title,author,headRefName",
		},
	})
	if err != nil {
		return PRInfo{}, fmt.Errorf("gh pr view %s: %w", ref, err)
	}
	var info PRInfo
	if err := json.Unmarshal([]byte(res.Stdout), &info); err != nil {
		return PRInfo{}, fmt.Errorf("parse gh pr view output for %s: %w", ref, err)
	}
	if info.HeadRefName == "" {
		return info, fmt.Errorf("gh pr view %s returned no head branch", ref)
	}
	return info, nil
}

// Reviews lists every review on the pull request. --slurp wraps the pages in an
// outer array, so the result is a list of pages to flatten.
func (c *CLI) Reviews(ctx context.Context, ref pr.Ref) ([]review.GHReview, error) {
	res, err := c.Runner.Run(ctx, exec.CommandSpec{
		Path: cmp.Or(c.Path, "gh"),
		Args: []string{
			"api", "--paginate", "--slurp",
			fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews", ref.Org, ref.Repo, ref.Number),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("gh api reviews for %s: %w", ref, err)
	}
	out := strings.TrimSpace(res.Stdout)
	if out == "" {
		return nil, nil
	}

	var pages [][]review.GHReview
	if err := json.Unmarshal([]byte(out), &pages); err != nil {
		return nil, fmt.Errorf("parse reviews for %s: %w", ref, err)
	}
	return slices.Concat(pages...), nil
}

// SubmitReview turns a pending review into a submitted one. event is APPROVE,
// COMMENT, or REQUEST_CHANGES.
func (c *CLI) SubmitReview(ctx context.Context, ref pr.Ref, reviewID int64, event, body string) error {
	args := []string{
		"api", "--method", "POST",
		fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews/%d/events", ref.Org, ref.Repo, ref.Number, reviewID),
		"-f", "event=" + event,
	}
	if body != "" {
		args = append(args, "-f", "body="+body)
	}
	if _, err := c.Runner.Run(ctx, exec.CommandSpec{Path: cmp.Or(c.Path, "gh"), Args: args}); err != nil {
		return fmt.Errorf("submit review %d on %s: %w", reviewID, ref, err)
	}
	return nil
}
