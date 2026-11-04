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
// tell whether the pull request is still open, and to tell whether the user has
// reviewed its head.
type PRInfo struct {
	Number      int            `json:"number"`
	Title       string         `json:"title"`
	HeadRefName string         `json:"headRefName"`
	HeadRefOid  string         `json:"headRefOid"`
	State       review.PRState `json:"state"`
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
	ReviewRequests(ctx context.Context, query string) ([]requests.PR, error)
	Teams(ctx context.Context) ([]string, error)
	TeamMembers(ctx context.Context, team string) ([]string, error)
	FirstAssignee(ctx context.Context, ref pr.Ref, assigner string) (string, error)
	Assign(ctx context.Context, ref pr.Ref, login string) error
	Unassign(ctx context.Context, ref pr.Ref, login string) (bool, error)
	ReviewComments(ctx context.Context, ref pr.Ref, reviewID int64) ([]string, error)
	RequestedTeams(ctx context.Context, ref pr.Ref) ([]string, error)
	RequestTeamReview(ctx context.Context, ref pr.Ref, teams []string) error
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
		"--json", "number,title,author,headRefName,headRefOid,state",
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

type searchItem struct {
	HTMLURL string `json:"html_url"`
	Title   string `json:"title"`
	Draft   bool   `json:"draft"`
	Body    string `json:"body"`
	User    struct {
		Login string `json:"login"`
		Type  string `json:"type"`
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
			bot := item.User.Type == "Bot"
			prs = append(prs, requests.PR{
				Ref:           ref,
				Title:         item.Title,
				Author:        item.User.Login,
				Bot:           bot,
				PersonStarted: bot && requests.StartedByAPerson(item.Body),
				Assignees:     assignees,
				IsDraft:       item.Draft,
				UpdatedAt:     item.UpdatedAt,
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

// TeamMembers lists the logins of an "org/team" team's members. It needs the
// token's read:org scope, as Teams does.
func (c *CLI) TeamMembers(ctx context.Context, team string) ([]string, error) {
	org, slug, ok := strings.Cut(team, "/")
	if !ok {
		return nil, fmt.Errorf("team %q is not org/team", team)
	}
	res, err := c.run(ctx, "api", "-X", "GET", "--paginate", "--slurp", "orgs/"+org+"/teams/"+slug+"/members", "-f", "per_page=100")
	if err != nil {
		return nil, fmt.Errorf("gh api members of %s: %w", team, err)
	}
	out := strings.TrimSpace(res.Stdout)
	if out == "" {
		return nil, nil
	}

	var pages [][]review.GHUser
	if err := json.Unmarshal([]byte(out), &pages); err != nil {
		return nil, fmt.Errorf("parse the members of %s: %w", team, err)
	}
	var members []string
	for _, m := range slices.Concat(pages...) {
		members = append(members, m.Login)
	}
	return members, nil
}

// issueEvent is one entry of the issue events API. That API puts the assignee
// of an assigned event in both "assignee" and "actor", so only "assigner" says
// who made the assignment. A review_requested event names a team by slug alone.
type issueEvent struct {
	Event         string        `json:"event"`
	Assigner      review.GHUser `json:"assigner"`
	Assignee      review.GHUser `json:"assignee"`
	RequestedTeam *teamSlug     `json:"requested_team"`
}

type teamSlug struct {
	Slug string `json:"slug"`
}

// issueEvents reads every page of the pull request's issue events. That API
// lists events and none of the comments the timeline also carries.
func (c *CLI) issueEvents(ctx context.Context, ref pr.Ref) ([]issueEvent, error) {
	path := fmt.Sprintf("repos/%s/%s/issues/%d/events", ref.Org, ref.Repo, ref.Number)
	res, err := c.run(ctx, "api", "-X", "GET", "--paginate", "--slurp", path, "-f", "per_page=100")
	if err != nil {
		return nil, fmt.Errorf("gh api events of %s: %w", ref, err)
	}
	out := strings.TrimSpace(res.Stdout)
	if out == "" {
		return nil, nil
	}

	var pages [][]issueEvent
	if err := json.Unmarshal([]byte(out), &pages); err != nil {
		return nil, fmt.Errorf("parse the events of %s: %w", ref, err)
	}
	return slices.Concat(pages...), nil
}

// FirstAssignee returns the first user that assigner assigned to the pull
// request, or "" when assigner assigned nobody.
func (c *CLI) FirstAssignee(ctx context.Context, ref pr.Ref, assigner string) (string, error) {
	events, err := c.issueEvents(ctx, ref)
	if err != nil {
		return "", err
	}
	for _, e := range events {
		if e.Event == "assigned" && strings.EqualFold(e.Assigner.Login, assigner) {
			return e.Assignee.Login, nil
		}
	}
	return "", nil
}

// Assign adds login to the pull request's assignees and keeps the ones already
// there. GitHub answers 201 and leaves out a user it cannot assign, such as one
// without push access to the repository. Assign therefore reads the assignees
// GitHub answers with and fails when login is not among them.
func (c *CLI) Assign(ctx context.Context, ref pr.Ref, login string) error {
	res, err := c.run(ctx, "api", "-X", "POST", assigneesPath(ref), "-f", "assignees[]="+login)
	if err != nil {
		return fmt.Errorf("assign %s to %s: %w", login, ref, err)
	}
	assigned, err := amongAssignees(res.Stdout, login)
	if err != nil {
		return fmt.Errorf("parse the answer to assigning %s to %s: %w", login, ref, err)
	}
	if !assigned {
		return fmt.Errorf("GitHub left %s out of the assignees of %s, as it does for a user without push access", login, ref)
	}
	return nil
}

// Unassign removes login from the pull request's assignees, keeps the others,
// and reports whether login was an assignee to remove. GitHub answers a removal
// of a user who is not assigned the same way it answers a real one, so Unassign
// reads the assignees first and sends nothing when login is not among them. It
// fails when login is still among the assignees GitHub answers the removal with.
func (c *CLI) Unassign(ctx context.Context, ref pr.Ref, login string) (bool, error) {
	res, err := c.run(ctx, "api", "-X", "GET", fmt.Sprintf("repos/%s/%s/issues/%d", ref.Org, ref.Repo, ref.Number))
	if err != nil {
		return false, fmt.Errorf("read the assignees of %s: %w", ref, err)
	}
	assigned, err := amongAssignees(res.Stdout, login)
	if err != nil {
		return false, fmt.Errorf("parse the assignees of %s: %w", ref, err)
	}
	if !assigned {
		return false, nil
	}

	res, err = c.run(ctx, "api", "-X", "DELETE", assigneesPath(ref), "-f", "assignees[]="+login)
	if err != nil {
		return false, fmt.Errorf("unassign %s from %s: %w", login, ref, err)
	}
	if assigned, err = amongAssignees(res.Stdout, login); err != nil {
		return false, fmt.Errorf("parse the answer to unassigning %s from %s: %w", login, ref, err)
	}
	if assigned {
		return false, fmt.Errorf("GitHub kept %s among the assignees of %s", login, ref)
	}
	return true, nil
}

func assigneesPath(ref pr.Ref) string {
	return fmt.Sprintf("repos/%s/%s/issues/%d/assignees", ref.Org, ref.Repo, ref.Number)
}

// amongAssignees reports whether login is among the assignees of the issue in
// GitHub's JSON answer. GitHub compares logins without case.
func amongAssignees(answer, login string) (bool, error) {
	var issue struct {
		Assignees []review.GHUser `json:"assignees"`
	}
	if err := json.Unmarshal([]byte(answer), &issue); err != nil {
		return false, err
	}
	return slices.ContainsFunc(issue.Assignees, func(a review.GHUser) bool { return strings.EqualFold(a.Login, login) }), nil
}

// ReviewComments lists the bodies of a review's inline comments. The author of
// a pending review can read its comments. A submitted review keeps them at the
// same path.
func (c *CLI) ReviewComments(ctx context.Context, ref pr.Ref, reviewID int64) ([]string, error) {
	path := fmt.Sprintf("repos/%s/%s/pulls/%d/reviews/%d/comments", ref.Org, ref.Repo, ref.Number, reviewID)
	res, err := c.run(ctx, "api", "-X", "GET", "--paginate", "--slurp", path, "-f", "per_page=100")
	if err != nil {
		return nil, fmt.Errorf("gh api comments of review %d on %s: %w", reviewID, ref, err)
	}
	out := strings.TrimSpace(res.Stdout)
	if out == "" {
		return nil, nil
	}

	var pages [][]struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal([]byte(out), &pages); err != nil {
		return nil, fmt.Errorf("parse the comments of review %d on %s: %w", reviewID, ref, err)
	}
	var bodies []string
	for _, comment := range slices.Concat(pages...) {
		bodies = append(bodies, comment.Body)
	}
	return bodies, nil
}

// RequestedTeams lists the slug of every team that was ever asked to review the
// pull request, each once, including a team whose request a review has since
// fulfilled. GitHub can only request a team of the repository's own
// organization, so the slug is enough to name it.
func (c *CLI) RequestedTeams(ctx context.Context, ref pr.Ref) ([]string, error) {
	events, err := c.issueEvents(ctx, ref)
	if err != nil {
		return nil, err
	}
	var slugs []string
	for _, e := range events {
		if e.Event != "review_requested" || e.RequestedTeam == nil || slices.Contains(slugs, e.RequestedTeam.Slug) {
			continue
		}
		slugs = append(slugs, e.RequestedTeam.Slug)
	}
	return slugs, nil
}

// RequestTeamReview asks the "org/team" teams to review the pull request. Each
// team must belong to the repository's organization, because GitHub can request
// no other. It reads the requested teams GitHub answers with and fails when one
// of the teams is not among them.
func (c *CLI) RequestTeamReview(ctx context.Context, ref pr.Ref, teams []string) error {
	path := fmt.Sprintf("repos/%s/%s/pulls/%d/requested_reviewers", ref.Org, ref.Repo, ref.Number)
	args := []string{"api", "-X", "POST", path}
	slugs := make([]string, len(teams))
	for i, team := range teams {
		org, slug, ok := strings.Cut(team, "/")
		if !ok || !strings.EqualFold(org, ref.Org) {
			return fmt.Errorf("team %q is not a team of %s", team, ref.Org)
		}
		slugs[i] = slug
		args = append(args, "-f", "team_reviewers[]="+slug)
	}
	res, err := c.run(ctx, args...)
	if err != nil {
		return fmt.Errorf("request a review of %s from %s: %w", ref, strings.Join(teams, ", "), err)
	}
	var pull struct {
		RequestedTeams []teamSlug `json:"requested_teams"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &pull); err != nil {
		return fmt.Errorf("parse the answer to requesting a review of %s: %w", ref, err)
	}
	for i, slug := range slugs {
		if !slices.ContainsFunc(pull.RequestedTeams, func(t teamSlug) bool { return strings.EqualFold(t.Slug, slug) }) {
			return fmt.Errorf("GitHub left %s out of the teams requested on %s", teams[i], ref)
		}
	}
	return nil
}
