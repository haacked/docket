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
	"time"

	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/requests"
	"github.com/haacked/docket/internal/core/review"
)

// PRInfo is the pull request metadata docket needs to clone, to label a row, to
// tell whether the pull request is still open, to tell whether the user has
// reviewed its head, and to assign the user to a bot's pull request.
type PRInfo struct {
	Number      int            `json:"number"`
	Title       string         `json:"title"`
	HeadRefName string         `json:"headRefName"`
	HeadRefOid  string         `json:"headRefOid"`
	State       review.PRState `json:"state"`
	// IsCrossRepository reports a head branch in a fork. A fix review pushes to
	// the head branch by name, which only works on the base repository.
	IsCrossRepository bool `json:"isCrossRepository"`
	Author            struct {
		Login string `json:"login"`
		// IsBot reports a GitHub App, which gh names app/<name>.
		IsBot bool `json:"is_bot"`
	} `json:"author"`
	Assignees []review.GHUser `json:"assignees"`
}

// GitHub is the GitHub access docket needs. Tests substitute a fake.
type GitHub interface {
	Login(ctx context.Context) (string, error)
	PR(ctx context.Context, ref pr.Ref) (PRInfo, error)
	Reviews(ctx context.Context, ref pr.Ref) ([]review.GHReview, error)
	SubmitReview(ctx context.Context, ref pr.Ref, reviewID int64, event, body string) error
	CreateReview(ctx context.Context, ref pr.Ref, commitID, event, body string) error
	AddAssignee(ctx context.Context, ref pr.Ref, login string) error
	ReviewRequests(ctx context.Context, query string) ([]requests.PR, error)
	Teams(ctx context.Context) ([]string, error)
}

// CLI talks to GitHub through the gh command.
type CLI struct {
	Runner exec.Runner
	Path   string
}

func New(runner exec.Runner) *CLI { return &CLI{Runner: runner, Path: "gh"} }

func (c *CLI) run(ctx context.Context, args ...string) (exec.Result, error) {
	return c.Runner.Run(ctx, exec.CommandSpec{Path: cmp.Or(c.Path, "gh"), Args: args})
}

func (c *CLI) Login(ctx context.Context) (string, error) {
	res, err := c.run(ctx, "api", "user", "--jq", ".login")
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
	res, err := c.run(ctx,
		"pr", "view", strconv.Itoa(ref.Number),
		"--repo", ref.Slug(),
		"--json", "number,title,author,assignees,headRefName,headRefOid,state,isCrossRepository",
	)
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
	res, err := c.run(ctx,
		"api", "--paginate", "--slurp",
		fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews", ref.Org, ref.Repo, ref.Number),
	)
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
	if _, err := c.run(ctx, args...); err != nil {
		return fmt.Errorf("submit review %d on %s: %w", reviewID, ref, err)
	}
	return nil
}

// CreateReview posts a submitted review on the commit commitID. GitHub reads a
// review on an older commit as a review of that commit, not of the current head.
func (c *CLI) CreateReview(ctx context.Context, ref pr.Ref, commitID, event, body string) error {
	args := []string{
		"api", "--method", "POST",
		fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews", ref.Org, ref.Repo, ref.Number),
		"-f", "commit_id=" + commitID,
		"-f", "event=" + event,
	}
	if body != "" {
		args = append(args, "-f", "body="+body)
	}
	if _, err := c.run(ctx, args...); err != nil {
		return fmt.Errorf("post a review on %s: %w", ref, err)
	}
	return nil
}

// AddAssignee adds login to the pull request's assignees and keeps the ones
// already there.
func (c *CLI) AddAssignee(ctx context.Context, ref pr.Ref, login string) error {
	if _, err := c.run(ctx,
		"api", "--method", "POST",
		fmt.Sprintf("/repos/%s/%s/issues/%d/assignees", ref.Org, ref.Repo, ref.Number),
		"-f", "assignees[]="+login,
	); err != nil {
		return fmt.Errorf("assign %s to %s: %w", login, ref, err)
	}
	return nil
}

type searchItem struct {
	HTMLURL string `json:"html_url"`
	Title   string `json:"title"`
	Draft   bool   `json:"draft"`
	User    struct {
		Login string `json:"login"`
	} `json:"user"`
	Assignees []struct {
		Login string `json:"login"`
	} `json:"assignees"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ReviewRequests lists the open pull requests that match a search query, such as
// "user-review-requested:haacked",
// "team-review-requested:org/team -author:haacked", or
// "reviewed-by:haacked -author:haacked". It uses the REST search endpoint
// because gh search prs goes through GraphQL, which refused with a rate-limit
// error while the REST search still answered.
func (c *CLI) ReviewRequests(ctx context.Context, query string) ([]requests.PR, error) {
	res, err := c.run(ctx,
		"api", "-X", "GET", "--paginate", "--slurp", "search/issues",
		"-f", "q=is:pr is:open archived:false "+query,
		"-f", "per_page=100",
	)
	if err != nil {
		return nil, fmt.Errorf("search %s: %w", query, err)
	}
	out := strings.TrimSpace(res.Stdout)
	if out == "" {
		return nil, nil
	}

	var pages []struct {
		Items []searchItem `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &pages); err != nil {
		return nil, fmt.Errorf("parse search results for %s: %w", query, err)
	}
	var prs []requests.PR
	for _, page := range pages {
		for _, item := range page.Items {
			ref, err := pr.ParseRef(item.HTMLURL, "")
			if err != nil {
				return nil, fmt.Errorf("search %s returned %q: %w", query, item.HTMLURL, err)
			}
			var assignees []string
			for _, a := range item.Assignees {
				assignees = append(assignees, a.Login)
			}
			prs = append(prs, requests.PR{
				Ref:       ref,
				Title:     item.Title,
				Author:    item.User.Login,
				Assignees: assignees,
				IsDraft:   item.Draft,
				UpdatedAt: item.UpdatedAt,
			})
		}
	}
	return prs, nil
}

// Teams lists the "org/team" slugs of every team the user belongs to. GitHub
// answers only with the teams of organizations that the token's read:org scope
// reaches.
func (c *CLI) Teams(ctx context.Context) ([]string, error) {
	res, err := c.run(ctx, "api", "-X", "GET", "--paginate", "--slurp", "user/teams", "-f", "per_page=100")
	if err != nil {
		return nil, fmt.Errorf("gh api user/teams: %w", err)
	}
	out := strings.TrimSpace(res.Stdout)
	if out == "" {
		return nil, nil
	}

	var pages [][]struct {
		Slug         string `json:"slug"`
		Organization struct {
			Login string `json:"login"`
		} `json:"organization"`
	}
	if err := json.Unmarshal([]byte(out), &pages); err != nil {
		return nil, fmt.Errorf("parse gh api user/teams output: %w", err)
	}
	var teams []string
	for _, page := range pages {
		for _, team := range page {
			teams = append(teams, team.Organization.Login+"/"+team.Slug)
		}
	}
	return teams, nil
}
