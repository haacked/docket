// Package session is the only entry point the UI calls. It prepares a review,
// hands back the command to launch, and reads what happened afterwards.
package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/haacked/docket/internal/core/clone"
	"github.com/haacked/docket/internal/core/config"
	"github.com/haacked/docket/internal/core/engine"
	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/gh"
	"github.com/haacked/docket/internal/core/git"
	"github.com/haacked/docket/internal/core/index"
	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/reposconf"
	"github.com/haacked/docket/internal/core/requests"
	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/core/tier"
	"github.com/haacked/docket/internal/core/worktree"
)

// Service holds everything a review needs and owns every write to the index.
type Service struct {
	Cfg    config.Config
	Paths  config.Paths
	Store  *index.Store
	GH     gh.GitHub
	Git    git.Git
	Cloner *clone.Cloner
	// Worktrees adds and removes the tier-3 worktree a fix review edits.
	Worktrees *worktree.Adder
	// Runner runs the commands that need nothing but their output. The session
	// a user works in is not one of them: internal/tui hands that to the
	// terminal. A background agent is driven entirely through here.
	Runner exec.Runner
	Now    func() time.Time
	NewID  func() string
	// CallerDir is the working directory of the agent session this service
	// runs under, or empty when there is none. docket mcp runs in the directory
	// of the claude session that started it.
	CallerDir string

	// idleSeen maps a background id to the agent's update time when docket
	// last read GitHub for that session while it was idle. A session stays
	// idle until the user answers it, so the poll reads GitHub once for each
	// stretch instead of on every tick.
	idleMu   sync.Mutex
	idleSeen map[string]time.Time

	// cfgMu guards config.toml and the fields of Cfg that change after startup:
	// the login and the teams. The poll can cache the login while the teams
	// screen saves the teams. Without the lock, the rename that lands last drops
	// the other write's key, and a search can read the teams mid-save. The lock
	// covers this process only. Two docket instances that write config.toml at
	// the same moment can still drop each other's key.
	cfgMu sync.Mutex
}

// Config is a copy of Cfg taken under the lock that its writers hold.
func (s *Service) Config() config.Config {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	return s.Cfg
}

// Plan is what Prepare worked out, for the UI to show before launching.
type Plan struct {
	Tier       tier.Tier
	Dir        string
	LocalClone string
	Worktree   string
	NotesPath  string
	// Branch is the head branch a fix review's checkout is on.
	Branch string
	// Assignee is the login docket adds to the pull request's assignees.
	Assignee string
}

// Description says in one line what the tier means and whom docket assigns.
func (p Plan) Description() string {
	if p.Assignee == "" {
		return p.tierDescription()
	}
	return p.tierDescription() + "; docket assigns the pull request to " + p.Assignee
}

func (p Plan) tierDescription() string {
	switch p.Tier {
	case tier.Tier1:
		return fmt.Sprintf("review-code knows this repo (%s), so it provisions and tears down its own worktree at %s; docket clones nothing", p.LocalClone, p.Worktree)
	case tier.Tier2:
		// Decide names no local clone for tier 2. A tier-2 plan that has one is a
		// fix review whose clone already has a branch named like the head branch.
		if p.LocalClone != "" {
			return fmt.Sprintf("%s already has a branch named %s, so docket checks the head out at %s instead of adding a worktree", p.LocalClone, p.Branch, p.Dir)
		}
		return fmt.Sprintf("review-code has no clone for this repo, so docket checks the head out at %s", p.Dir)
	case tier.Tier3:
		return fmt.Sprintf("a fix review edits the head branch, so docket adds a worktree of %s on %s at %s", p.LocalClone, p.Branch, p.Dir)
	default:
		return "unknown tier"
	}
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) newID() string {
	if s.NewID != nil {
		return s.NewID()
	}
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

// enginePaths are the paths for a record whose claude sessions run under
// claudeConfig, which is the record's ClaudeConfigDir.
func (s *Service) enginePaths(claudeConfig string) engine.Paths {
	return engine.Paths{
		Grant:         s.Cfg.AgentDirs(),
		CodexSessions: s.Cfg.CodexSessionsDir,
		ClaudeConfig:  claudeConfig,
		ClaudeJobs:    s.Cfg.ClaudeJobsDir(claudeConfig),
	}
}

// Records lists every record, newest first, with open ones ahead of closed ones.
func (s *Service) Records() ([]review.Record, error) {
	records, err := s.Store.Load()
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(records, func(a, b review.Record) int {
		if a.State.Open() != b.State.Open() {
			if a.State.Open() {
				return -1
			}
			return 1
		}
		return b.StartedAt.Compare(a.StartedAt)
	})
	return records, nil
}

// Login is the GitHub login docket compares review authors against. docket caches
// it in config.toml, because every detection needs it.
func (s *Service) Login(ctx context.Context) (string, error) {
	login, err := s.peekLogin(ctx)
	if err != nil {
		return "", err
	}
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	// Two first detections can both ask gh. The second one to get here finds
	// the login cached and writes nothing.
	if s.Cfg.GitHubUser != "" {
		return s.Cfg.GitHubUser, nil
	}
	s.Cfg.GitHubUser = login
	if err := config.SaveKey(s.Paths.Config, "github_user", login); err != nil {
		return login, fmt.Errorf("cache github login: %w", err)
	}
	return login, nil
}

// Requests searches GitHub for the open pull requests that ask for the user's
// review or for a configured team's review. It writes nothing, so a dry run may
// call it. It reads the login through peekLogin rather than Login, because Login
// caches the login in config.toml. The searches run one after another because
// GitHub's secondary rate limits ask that one user's requests not run
// concurrently. The query names the login rather than @me, because the REST
// search answers 422 to @me under some gh credentials. A team whose search fails
// carries the error and the other searches still run. GitHub answers 422 to a
// team it cannot resolve, so one misspelled slug would otherwise hide every
// request. A team search leaves out the user's own pull requests, because
// CODEOWNERS asks the author's team to review them.
func (s *Service) Requests(ctx context.Context) (requests.Fetched, error) {
	me, err := s.peekLogin(ctx)
	if err != nil {
		return requests.Fetched{}, err
	}
	mine, err := s.GH.ReviewRequests(ctx, "user-review-requested:"+me)
	if err != nil {
		return requests.Fetched{}, err
	}
	f := requests.Fetched{Login: me, Mine: mine}
	check := caughtUpCheck{s: s, me: me, skip: refsOf(mine)}
	for _, team := range s.Config().Teams {
		query := "team-review-requested:" + team + " -author:" + me
		prs, err := s.GH.ReviewRequests(ctx, query)
		if err == nil {
			if prs, err = check.drop(ctx, prs); err != nil {
				err = fmt.Errorf("could not tell which of these you already reviewed: %w", err)
			}
		}
		f.Teams = append(f.Teams, requests.Team{Slug: team, PRs: prs, Err: err})
	}
	return f, nil
}

// caughtUpCheck leaves out of each team's search the pull requests the user has
// reviewed at their current head. A team's request can outlast the user's
// review or arrive after it, so the team search alone lists pull requests with
// nothing new to review. requests.Group lists a pull request only under the
// first section that has it. So a pull request in the user's own section is
// never checked, and one in two teams' searches is checked once.
//
// GitHub allows 30 searches a minute, so one reviewed-by search serves every
// team. It runs when the first team has rows, and a failure of it stands for
// every later team without searching again.
type caughtUpCheck struct {
	s         *Service
	me        string
	searched  bool
	reviewed  []pr.Ref
	searchErr error
	skip      []pr.Ref
	caught    []pr.Ref
}

// drop checks only the pull requests that are both in prs and in the reviewed-by
// search, because each check costs up to two calls. A check that fails ends the
// checks of this team's rows. The team then keeps every row that no check found
// caught up.
func (c *caughtUpCheck) drop(ctx context.Context, prs []requests.PR) ([]requests.PR, error) {
	if len(prs) == 0 {
		return prs, nil
	}
	if !c.searched {
		c.searched = true
		var reviewed []requests.PR
		reviewed, c.searchErr = c.s.GH.ReviewRequests(ctx, "reviewed-by:"+c.me+" -author:"+c.me)
		c.reviewed = refsOf(reviewed)
	}
	if c.searchErr != nil {
		return prs, c.searchErr
	}
	for _, p := range prs {
		if slices.ContainsFunc(c.skip, p.Ref.Equal) || !slices.ContainsFunc(c.reviewed, p.Ref.Equal) {
			continue
		}
		c.skip = append(c.skip, p.Ref)
		caught, err := c.caughtUp(ctx, p.Ref)
		if err != nil {
			return c.withoutCaught(prs), err
		}
		if caught {
			c.caught = append(c.caught, p.Ref)
		}
	}
	return c.withoutCaught(prs), nil
}

func (c *caughtUpCheck) withoutCaught(prs []requests.PR) []requests.PR {
	return slices.DeleteFunc(slices.Clone(prs), func(p requests.PR) bool {
		return slices.ContainsFunc(c.caught, p.Ref.Equal)
	})
}

// caughtUp reads the reviews first, because a pull request with none of the
// user's reviews to count needs no read of its head.
func (c *caughtUpCheck) caughtUp(ctx context.Context, ref pr.Ref) (bool, error) {
	reviews, err := c.s.GH.Reviews(ctx, ref)
	if err != nil {
		return false, err
	}
	commits := review.ReviewedCommits(reviews, c.me)
	if len(commits) == 0 {
		return false, nil
	}
	info, err := c.s.GH.PR(ctx, ref)
	if err != nil {
		return false, err
	}
	return slices.Contains(commits, info.HeadRefOid), nil
}

func refsOf(prs []requests.PR) []pr.Ref {
	refs := make([]pr.Ref, 0, len(prs))
	for _, p := range prs {
		refs = append(refs, p.Ref)
	}
	return refs
}

// Teams lists the teams the user belongs to on GitHub, as "org/team" slugs. It
// writes nothing, so a dry run may call it.
func (s *Service) Teams(ctx context.Context) ([]string, error) {
	return s.GH.Teams(ctx)
}

// SaveTeams makes teams the ones whose review requests Requests searches for,
// and writes them to config.toml.
func (s *Service) SaveTeams(teams []string) error {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	if err := config.SaveKey(s.Paths.Config, "teams", teams); err != nil {
		return fmt.Errorf("save teams: %w", err)
	}
	s.Cfg.Teams = teams
	return nil
}

// peekLogin is Login without caching the answer, for the paths a dry run takes.
func (s *Service) peekLogin(ctx context.Context) (string, error) {
	if login := s.Config().GitHubUser; login != "" {
		return login, nil
	}
	return s.GH.Login(ctx)
}

// Prepare resolves the pull request, works out the tier, provisions the
// directory the session will run in, and snapshots the reviews that already
// exist. The record is written before provisioning, so a clone that fails leaves
// a row carrying the reason. Nothing re-provisions that row: resume does not, and
// a second Prepare is refused while it is open, so the user abandons it with x
// and starts again with n.
//
// An ask intent adopts the review that is already there instead of starting one.
// Prepare provisions it the same way, which keeps Prepare the only place that
// provisions a row. On tier 2 that puts the files the notes cite in the working
// directory. On tier 1 the working directory is the shared scratch repository,
// so the ask prompt sends the agent to GitHub for the files.
func (s *Service) Prepare(ctx context.Context, ref pr.Ref, engineName string, mode review.Mode, intent review.Intent, choice review.FixChoice) (review.Record, Plan, error) {
	rec, plan, info, _, err := s.resolve(ctx, ref, engineName, mode, intent, choice)
	if err != nil {
		return review.Record{}, Plan{}, err
	}

	// Two records for one pull request derive the same clone directory, so
	// abandoning either deletes the clone the other is using. The read and the
	// append take separate locks, so a second instance can still pass this check
	// before either appends. The check sits here rather than before resolve to keep
	// that window off the call to GitHub. Closing it needs a compare-and-append the
	// store does not have.
	if err := s.refuseOpen(ref); err != nil {
		return review.Record{}, Plan{}, err
	}
	if err := s.append(rec); err != nil {
		return rec, plan, err
	}

	if rec, err = s.provision(ctx, rec, plan, info); err != nil {
		return s.fail(rec, plan, err)
	}

	// resolve read the login docket had cached, which a fresh install does not
	// have yet. Asking now settles it before the session is launched.
	me, reviews, err := s.myReviews(ctx, ref, s.Login)
	if err != nil {
		return s.fail(rec, plan, err)
	}
	rec.OwnPR = ownPR(rec.Author, me)
	if plan.Assignee, err = s.assign(ctx, ref, info, rec.Fix, me); err != nil {
		return s.fail(rec, plan, err)
	}

	rec.PriorReviewIDs = review.PriorSubmittedIDs(reviews, me)
	rec.PriorPendingID = review.PendingReviewID(reviews, me)
	rec.Err = ""
	if intent == review.IntentAsk {
		rec = adopt(rec, reviews, me, s.now())
	}

	if err := s.append(rec); err != nil {
		return rec, plan, err
	}
	return rec, plan, nil
}

// provision makes the directory the session runs in: the shared scratch
// repository on tier 1, a clone of the head on tier 2, and a worktree of the
// repos.conf clone on tier 3. A fix review's checkout also records the branch,
// the remote it tracks, and the commit it starts from.
func (s *Service) provision(ctx context.Context, rec review.Record, plan Plan, info gh.PRInfo) (review.Record, error) {
	if rec.Fix {
		// A checkout kept from an earlier review may hold fixes. Ensure resets a
		// clone it reuses.
		if err := s.refuseLocalWork(ctx, plan.Dir, "origin/"+info.HeadRefName); err != nil {
			// docket did not create the directory, so abandoning this record must
			// not delete it.
			rec.Dir = ""
			return rec, err
		}
	}
	switch plan.Tier {
	case tier.Tier1:
		return rec, s.ensureScratch(ctx)
	case tier.Tier3:
		dir, remote, err := s.Worktrees.Ensure(ctx, plan.LocalClone, rec.Ref, info.HeadRefName)
		if err != nil {
			return rec, err
		}
		rec.Dir, rec.Remote = dir, remote
	default:
		dir, err := s.Cloner.Ensure(ctx, rec.Ref, info)
		if err != nil {
			return rec, err
		}
		rec.Dir = dir
		if rec.Fix {
			if err := s.trackOrigin(ctx, dir, info.HeadRefName); err != nil {
				return rec, err
			}
			rec.Remote = "origin"
		}
	}
	if !rec.Fix {
		return rec, nil
	}
	return s.stampBase(ctx, rec)
}

// stampBase records the commit a fix review's checkout starts from.
func (s *Service) stampBase(ctx context.Context, rec review.Record) (review.Record, error) {
	head, err := s.Git.Head(ctx, rec.Dir)
	if err != nil {
		return rec, err
	}
	rec.FixBase, rec.FixHead = head, head
	rec.FixBaseAt = s.now()
	return rec, nil
}

// trackOrigin makes the tier-2 clone's head branch track origin, so a push from
// the fix session needs no arguments and docket can count the commits origin
// does not have. The clone's own credential helper makes that push
// authenticate the way docket's fetches do.
func (s *Service) trackOrigin(ctx context.Context, dir, branch string) error {
	if err := s.Git.FetchBranch(ctx, dir, "origin", branch); err != nil {
		return err
	}
	if err := s.Git.SetUpstream(ctx, dir, branch, "origin/"+branch); err != nil {
		return err
	}
	return s.Git.SetCredentialHelper(ctx, dir)
}

// refuseLocalWork refuses a repository at dir that holds uncommitted changes or
// commits upstream does not have. A directory that is not a repository holds
// nothing git can lose, and provisioning builds a new checkout there. A clone
// made for a draft review has no remote-tracking ref for the branch, so a
// failed count reads as nothing ahead.
func (s *Service) refuseLocalWork(ctx context.Context, dir, upstream string) error {
	if !s.Git.IsRepo(ctx, dir) {
		return nil
	}
	var checkout review.Checkout
	var err error
	if checkout.Dirty, err = s.Git.Dirty(ctx, dir); err != nil {
		return err
	}
	checkout.Ahead, _ = s.Git.Ahead(ctx, dir, upstream)
	if checkout.Local() {
		return fmt.Errorf("%s holds changes that are not on GitHub; push or discard them first", dir)
	}
	return nil
}

// adopt sets the state of a record taken over from a review docket did not run.
// Decide cannot produce that state. With my submitted review in PriorReviewIDs,
// Decide reads unreviewed. Without it, Decide reads that review as this record's
// own submission, and record archives the row at once. The snapshot includes the
// review all the same, so a later re-review does not count it as its own.
func adopt(rec review.Record, reviews []review.GHReview, me string, now time.Time) review.Record {
	rec.StartedAt = now
	if id := review.PendingReviewID(reviews, me); id != 0 {
		rec.State = review.StateDrafted
		rec.ReviewID = id
		return rec
	}
	rec.State = review.StateReviewed
	return rec
}

// refuseOpen refuses a second record for a pull request that already has an
// open one.
func (s *Service) refuseOpen(ref pr.Ref) error {
	records, err := s.Records()
	if err != nil {
		return err
	}
	if _, ok := review.OpenRecord(records, ref); ok {
		return fmt.Errorf("%s is already open; abandon it first", ref)
	}
	return nil
}

// Existing reports what review of a pull request is already there, so the user
// can choose to ask about it, append to it, or overwrite it before review-code
// asks the same question in a terminal nobody may be watching.
//
// It writes nothing, so a dry run calls it too. That includes the login cache.
// On an install that has not cached a login, Existing asks GitHub without saving
// the answer, and Prepare saves it later.
func (s *Service) Existing(ctx context.Context, ref pr.Ref) (review.Found, error) {
	// The index is local, so the refusal comes before anything asks GitHub.
	if err := s.refuseOpen(ref); err != nil {
		return review.Found{}, err
	}

	var found review.Found
	if info, err := os.Stat(s.Cfg.NotesPath(ref.Org, ref.Repo, ref.Number)); err == nil {
		found.NotesAt = info.ModTime()
	}

	me, reviews, err := s.myReviews(ctx, ref, s.peekLogin)
	if err != nil {
		return review.Found{}, err
	}
	found.PendingID = review.PendingReviewID(reviews, me)
	found.Submitted = len(review.PriorSubmittedIDs(reviews, me)) > 0
	// Prepare refuses a closed pull request. Refusing here first spares the user
	// a choice about the existing review that Prepare would then refuse. Without
	// a review the user has no choice to make, and the check skips the call.
	if found.Any() {
		if _, err := s.openPR(ctx, ref); err != nil {
			return review.Found{}, err
		}
	}
	return found, nil
}

// resolve works out everything about a review that reads nothing but GitHub and
// repos.conf. Prepare goes on to provision and record. Explain stops here.
func (s *Service) resolve(ctx context.Context, ref pr.Ref, engineName string, mode review.Mode, intent review.Intent, choice review.FixChoice) (review.Record, Plan, gh.PRInfo, engine.Engine, error) {
	eng, err := engine.For(engineName)
	if err != nil {
		return review.Record{}, Plan{}, gh.PRInfo{}, nil, err
	}

	info, err := s.openPR(ctx, ref)
	if err != nil {
		return review.Record{}, Plan{}, gh.PRInfo{}, nil, err
	}

	entries := reposconf.ParseFile(s.Cfg.ReposConfPath())
	decided, localClone := tier.Decide(ref, entries, func(path string) bool {
		return s.Git.IsRepo(ctx, path)
	})

	fix, err := fixes(ref, info, intent, choice, s.Config().FixAuthors)
	if err != nil {
		return review.Record{}, Plan{}, gh.PRInfo{}, nil, err
	}
	plan := Plan{
		Tier:       decided,
		LocalClone: localClone,
		NotesPath:  s.Cfg.NotesPath(ref.Org, ref.Repo, ref.Number),
	}
	if fix {
		plan.Branch = info.HeadRefName
		taken := false
		if decided == tier.Tier1 {
			if taken, err = s.Git.BranchExists(ctx, localClone, info.HeadRefName); err != nil {
				return review.Record{}, Plan{}, gh.PRInfo{}, nil, err
			}
		}
		plan.Tier = tier.ForFix(decided, taken)
	}
	switch plan.Tier {
	case tier.Tier1:
		plan.Dir = s.Paths.Scratch
		plan.Worktree = s.Cfg.WorktreeDir(ref.Org, ref.Repo, ref.Number)
	case tier.Tier3:
		plan.Dir = s.Worktrees.Dir(ref)
	default:
		plan.Dir = s.Cloner.Dir(ref)
	}

	// A question-and-answer session needs the terminal, and an adopted record
	// has no review session of its own to name.
	sessionID := startingSessionID(eng, mode)
	if intent == review.IntentAsk {
		mode = review.ModeInteractive
		sessionID = ""
	}

	rec := review.Record{
		ID:        s.newID(),
		Ref:       ref,
		URL:       ref.URL(),
		Title:     info.Title,
		Author:    info.Author.Login,
		Engine:    eng.Name(),
		Tier:      plan.Tier,
		Mode:      mode,
		Dir:       plan.Dir,
		SessionID: sessionID,
		State:     review.StatePreparing,
		NotesPath: plan.NotesPath,
		Intent:    intent,
		PRState:   info.State,
		// The cached login, because Explain reaches here too and a dry run asks
		// GitHub for nothing it can avoid. Prepare settles it properly below. A
		// dry run on an install that has never cached a login therefore leaves
		// --self off the command it prints.
		OwnPR:           ownPR(info.Author.Login, s.Config().GitHubUser),
		ClaudeConfigDir: s.Cfg.ClaudeConfigDir,
		Fix:             fix,
		Branch:          plan.Branch,
	}
	if plan.Tier == tier.Tier3 {
		rec.WorktreeOf = localClone
	}
	return rec, plan, info, eng, nil
}

// fixes decides whether a review runs --fix. An ask adopts a review rather
// than running one. A fix review pushes to the head branch by name, which
// lives in the fork for a cross-repository pull request. Auto then drafts a
// review, and an explicit fix is refused.
func fixes(ref pr.Ref, info gh.PRInfo, intent review.Intent, choice review.FixChoice, authors []string) (bool, error) {
	if intent == review.IntentAsk {
		return false, nil
	}
	fix := choice.Fixes(review.ListedAuthor(authors, info.Author.Login))
	if !fix || !info.IsCrossRepository {
		return fix, nil
	}
	if choice == review.FixOn {
		return false, fmt.Errorf("%s comes from a fork, so a fix review has no branch it can push to", ref)
	}
	return false, nil
}

// assignee is the login a review adds to the pull request's assignees. A fix
// review of a bot's pull request adds me, unless I am already an assignee.
// GitHub logins ignore case.
func assignee(info gh.PRInfo, fix bool, me string) string {
	if !fix || !info.Author.IsBot || me == "" {
		return ""
	}
	if slices.ContainsFunc(info.Assignees, func(a review.GHUser) bool { return strings.EqualFold(a.Login, me) }) {
		return ""
	}
	return me
}

// assign adds me to the pull request's assignees when assignee says to, and
// returns the login it added.
func (s *Service) assign(ctx context.Context, ref pr.Ref, info gh.PRInfo, fix bool, me string) (string, error) {
	login := assignee(info, fix, me)
	if login == "" {
		return "", nil
	}
	return login, s.GH.AddAssignee(ctx, ref, login)
}

// Explain says what a review would do without doing any of it. A dry run must not
// provision. A check that reads what provisioning would have written then tells
// the user nothing, so Explain stops before both.
func (s *Service) Explain(ctx context.Context, ref pr.Ref, engineName string, mode review.Mode, intent review.Intent, choice review.FixChoice) (Plan, exec.CommandSpec, error) {
	rec, plan, info, eng, err := s.resolve(ctx, ref, engineName, mode, intent, choice)
	if err != nil {
		return Plan{}, exec.CommandSpec{}, err
	}
	// A dry run reads only the cached login, as resolve does for OwnPR. On an
	// install that has not cached one, the dry run names no assignee.
	plan.Assignee = assignee(info, rec.Fix, s.Config().GitHubUser)
	if intent == review.IntentAsk {
		rec.AskSessionID = eng.NewSessionID()
		return plan, eng.Ask(rec, s.enginePaths(rec.ClaudeConfigDir)), nil
	}
	spec, err := s.startSpec(eng, rec)
	return plan, spec, err
}

// startSpec is the command that starts a fresh review of rec in its mode.
func (s *Service) startSpec(eng engine.Engine, rec review.Record) (exec.CommandSpec, error) {
	if rec.Mode != review.ModeBackground {
		return eng.Start(rec, s.enginePaths(rec.ClaudeConfigDir)), nil
	}
	bg, ok := eng.(engine.BackgroundEngine)
	if !ok {
		return exec.CommandSpec{}, fmt.Errorf("%s cannot run a review in the background", eng.Name())
	}
	return bg.StartBackground(rec, s.enginePaths(rec.ClaudeConfigDir)), nil
}

// ensureScratch keeps the tier-1 launch directory a git repository with no
// remote. review-code then reads the org and repo as unknown and takes its
// cross-repo path, and interactive codex still sees a git repository.
func (s *Service) ensureScratch(ctx context.Context) error {
	// Ask for this directory's own repository rather than git's, because
	// `rev-parse --git-dir` walks up to an ancestor. A scratch directory inside a
	// checkout would otherwise hand review-code that checkout's remote.
	if _, err := os.Stat(filepath.Join(s.Paths.Scratch, ".git")); err == nil {
		return nil
	}
	if err := s.Git.Init(ctx, s.Paths.Scratch); err != nil {
		return fmt.Errorf("prepare scratch directory: %w", err)
	}
	return nil
}

// LaunchSpec returns the command that starts the review, after recording that
// the session started. It writes the record first, so a crash mid-session still
// leaves something to detect against.
func (s *Service) LaunchSpec(ctx context.Context, rec review.Record) (review.Record, exec.CommandSpec, error) {
	return s.launch(ctx, rec, false)
}

// ResumeSpec reopens the conversation a launch left behind, falling back to a
// fresh review when the record carries no session to resume.
func (s *Service) ResumeSpec(ctx context.Context, rec review.Record) (review.Record, exec.CommandSpec, error) {
	return s.launch(ctx, rec, true)
}

// specFor is the command a launch would run, and the record it would run it
// against. It writes nothing, so the dry run and the real launch both go through
// it and cannot drift apart. It returns the record because falling back to a
// fresh start mints a session id the caller has to keep.
func (s *Service) specFor(rec review.Record, resume bool) (review.Record, exec.CommandSpec, error) {
	eng, err := engine.For(rec.Engine)
	if err != nil {
		return rec, exec.CommandSpec{}, err
	}
	if err := checkDir(rec); err != nil {
		return rec, exec.CommandSpec{}, err
	}

	if resume {
		if spec, ok := eng.Resume(rec, s.enginePaths(rec.ClaudeConfigDir)); ok {
			return rec, spec, nil
		}
	}
	// An adopted record has no review session. A fresh start would pass
	// review-code neither --append nor --overwrite, so review-code would stop and
	// ask the question the user already answered when choosing to adopt.
	if rec.Adopted() {
		return rec, exec.CommandSpec{}, fmt.Errorf("%s has no review session to resume; press u to review it again", rec.Ref)
	}
	if rec.SessionID == "" {
		rec.SessionID = eng.NewSessionID()
	}
	return rec, eng.Start(rec, s.enginePaths(rec.ClaudeConfigDir)), nil
}

func (s *Service) launch(ctx context.Context, rec review.Record, resume bool) (review.Record, exec.CommandSpec, error) {
	rec, spec, err := s.specFor(rec, resume)
	if err != nil {
		return rec, exec.CommandSpec{}, err
	}

	// Every launch opens a new detection window. A resume also re-snapshots the
	// already-submitted reviews, because its record may be hours old. The launch
	// that follows Prepare does not, because Prepare just took that snapshot.
	rec.StartedAt = s.now()
	rec.State = review.StateReviewing
	rec.Err = ""
	if resume {
		// A stale snapshot is worse than a refused resume. Keeping the old list
		// would leave out a review submitted since the last session. Detection
		// would then read that review as this session's own and archive against it.
		rec, _, err = s.snapshot(ctx, rec)
		if err != nil {
			return rec, spec, fmt.Errorf("refresh the submitted reviews for %s: %w", rec.Ref, err)
		}
	}

	if err := s.append(rec); err != nil {
		return rec, spec, err
	}
	return rec, spec, nil
}

// ExplainResume is the command a resume would run, without recording anything. A
// dry run needs to report it, and reporting is all it may do. The record specFor
// returns is discarded, which is what keeps a minted session id off the log.
func (s *Service) ExplainResume(rec review.Record) (exec.CommandSpec, error) {
	_, spec, err := s.specFor(rec, true)
	return spec, err
}

// AfterExit reads what the finished session left on GitHub. It runs whatever the
// child's exit status was, because a session the user interrupted may still have
// created the draft.
func (s *Service) AfterExit(ctx context.Context, rec review.Record, childErr error) (review.Record, error) {
	if childErr != nil {
		rec.Err = childErr.Error()
	}
	if rec.Mode == review.ModeBackground {
		return s.afterBackgroundExit(ctx, rec)
	}
	rec = s.captureSessionID(rec)
	return s.detect(ctx, rec)
}

// captureSessionID stores the id of the session that just ran, for an engine
// that names its own. It runs after every exit rather than only the first,
// because resuming writes a further session and the record has to name the
// newest one to resume again.
//
// A capture that finds nothing leaves the record alone. Losing the id costs a
// resume, which specFor already handles by starting fresh, so it is not worth
// failing the detection that follows.
func (s *Service) captureSessionID(rec review.Record) review.Record {
	id, err := s.capture(rec)
	if err != nil {
		rec.Err = err.Error()
		return rec
	}
	if id != "" {
		rec.SessionID = id
	}
	return rec
}

func (s *Service) capture(rec review.Record) (string, error) {
	eng, err := engine.For(rec.Engine)
	if err != nil {
		return "", nil
	}
	return eng.CaptureSessionID(rec, s.enginePaths(rec.ClaudeConfigDir))
}

// AskSpec returns the command that opens a question-and-answer session about the
// record's notes, after recording that it started. It resumes the record's
// earlier session about them when there is one.
//
// It leaves the review's own state alone. The session is not meant to post
// anything, so a row that was drafted stays drafted.
func (s *Service) AskSpec(rec review.Record) (review.Record, exec.CommandSpec, error) {
	rec, spec, err := s.askSpecFor(rec)
	if err != nil {
		return rec, exec.CommandSpec{}, err
	}
	rec.AskStartedAt = s.now()
	rec.Err = ""
	return rec, spec, s.append(rec)
}

// ExplainAsk is the command AskSpec would run, without recording anything.
func (s *Service) ExplainAsk(rec review.Record) (exec.CommandSpec, error) {
	_, spec, err := s.askSpecFor(rec)
	return spec, err
}

func (s *Service) askSpecFor(rec review.Record) (review.Record, exec.CommandSpec, error) {
	eng, err := engine.For(rec.Engine)
	if err != nil {
		return rec, exec.CommandSpec{}, err
	}
	if err := refuseInProgress(rec); err != nil {
		return rec, exec.CommandSpec{}, err
	}
	if err := checkDir(rec); err != nil {
		return rec, exec.CommandSpec{}, err
	}
	if _, err := os.Stat(rec.NotesPath); err != nil {
		return rec, exec.CommandSpec{}, fmt.Errorf("%s has no review notes to ask about", rec.Ref)
	}

	if rec.AskSessionID != "" {
		if spec, ok := eng.Resume(asAsk(rec), s.enginePaths(rec.ClaudeConfigDir)); ok {
			return rec, spec, nil
		}
	}
	rec.AskSessionID = eng.NewSessionID()
	return rec, eng.Ask(rec, s.enginePaths(rec.ClaudeConfigDir)), nil
}

// AfterAsk records the session a question-and-answer launch left behind, then
// reads GitHub for anything the user had the agent post during it. A review
// submitted in the session archives the row, and a new pending review moves it to
// drafted.
//
// It writes onto the stored record rather than the one AskSpec returned. The row
// is not reviewing during the session, so another instance may have submitted,
// archived, or re-reviewed it meanwhile.
func (s *Service) AfterAsk(ctx context.Context, rec review.Record, childErr error) (review.Record, error) {
	current := s.latest(rec)
	current.AskStartedAt = rec.AskStartedAt
	current.AskSessionID = rec.AskSessionID
	if childErr != nil {
		current.Err = childErr.Error()
	}
	id, err := s.capture(asAsk(rec))
	if err != nil {
		current.Err = err.Error()
	} else if id != "" {
		current.AskSessionID = id
	}

	switch current.State {
	case review.StateDrafted, review.StateReviewed, review.StateUnreviewed, review.StateFixed, review.StatePushed:
	case review.StateSubmitted:
		// The review is already on GitHub. Only the archive that waited for this
		// session is left.
		return s.Archive(ctx, current)
	default:
		return current, s.append(current)
	}
	decided, reviews, err := s.decide(ctx, current)
	if err != nil {
		current.Err = err.Error()
		return current, s.append(current)
	}
	// Decide reads a row with no new review of mine as unreviewed. After a Q&A
	// session that only means that the session posted nothing. The row keeps its
	// state. decide already keeps a reviewed row, so this rule matters for a
	// drafted one.
	if decided.State == review.StateUnreviewed {
		decided.State = current.State
		decided.ReviewID = current.ReviewID
	}
	return s.record(ctx, decided, reviews)
}

// latest is the index's copy of rec, or rec itself when the index cannot be read
// or no longer holds it.
func (s *Service) latest(rec review.Record) review.Record {
	records, err := s.Records()
	if err != nil {
		return rec
	}
	if i := slices.IndexFunc(records, func(r review.Record) bool { return r.ID == rec.ID }); i >= 0 {
		return records[i]
	}
	return rec
}

// asAsk is the record as an engine sees the question-and-answer session. Resume
// reads SessionID and CaptureSessionID reads StartedAt, and on the record both
// belong to the review session.
func asAsk(rec review.Record) review.Record {
	rec.SessionID = rec.AskSessionID
	rec.StartedAt = rec.AskStartedAt
	return rec
}

// Rereview readies a record to be reviewed again with --append or --overwrite.
// The caller launches it the way it launches a prepared record, in the mode
// given here.
//
// It re-snapshots the submitted reviews, because the record may be old and a
// launch that is not a resume does not. It drops the review session, because a
// fresh start is what passes the new flag. It first stops a background session
// the agent still holds. Otherwise the agent would hold that session for ever
// with no record naming it.
func (s *Service) Rereview(ctx context.Context, rec review.Record, intent review.Intent, mode review.Mode) (review.Record, error) {
	rearmed, err := s.rearm(rec, intent, mode)
	if err != nil {
		return rec, err
	}
	// The stored state is only as fresh as the last detection. The pull request
	// may have merged since. The check runs before the stop, so a refusal leaves a
	// background session running.
	pull, err := s.openPR(ctx, rec.Ref)
	if err != nil {
		return rec, err
	}
	rearmed.PRState = pull.State
	// The stop reads the mode the session was started in, not the new one.
	rec, ok := s.stopBackground(ctx, rec)
	if !ok {
		return s.recordErr(rec, errors.New(rec.Err))
	}
	rearmed, me, err := s.snapshot(ctx, rearmed)
	if err != nil {
		return s.recordErr(rec, err)
	}
	if _, err := s.assign(ctx, rec.Ref, pull, rearmed.Fix, me); err != nil {
		return s.recordErr(rec, err)
	}
	// The reset comes last. A failure after it would record rec, whose FixBase
	// names the commit the checkout has left.
	if rearmed.Fix {
		if rearmed, err = s.refreshCheckout(ctx, rearmed); err != nil {
			return s.recordErr(rec, err)
		}
	}
	rearmed.BGID = ""
	return rearmed, s.append(rearmed)
}

// refreshCheckout moves a fix review's checkout to the head branch as GitHub
// has it, so the review and its fixes see the current head. It refuses a
// checkout that holds work GitHub does not have, which the reset would lose,
// and one whose branch it cannot fetch.
func (s *Service) refreshCheckout(ctx context.Context, rec review.Record) (review.Record, error) {
	checkout, err := s.readCheckout(ctx, rec, "")
	if err != nil {
		return rec, err
	}
	if checkout.Local() {
		return rec, fmt.Errorf("%s holds fixes that are not on GitHub; push or discard them before reviewing again", rec.Dir)
	}
	if checkout.FetchErr != nil {
		return rec, checkout.FetchErr
	}
	if err := s.Git.ResetHard(ctx, rec.Dir, rec.Upstream()); err != nil {
		return rec, err
	}
	return s.stampBase(ctx, rec)
}

// ExplainRereview is the command a re-review would run, without recording or
// stopping anything. It reads the pull request's state the way Rereview does, so
// the dry run refuses what the real run refuses.
func (s *Service) ExplainRereview(ctx context.Context, rec review.Record, intent review.Intent, mode review.Mode) (exec.CommandSpec, error) {
	rec, err := s.rearm(rec, intent, mode)
	if err != nil {
		return exec.CommandSpec{}, err
	}
	if _, err := s.openPR(ctx, rec.Ref); err != nil {
		return exec.CommandSpec{}, err
	}
	eng, err := engine.For(rec.Engine)
	if err != nil {
		return exec.CommandSpec{}, err
	}
	if err := checkDir(rec); err != nil {
		return exec.CommandSpec{}, err
	}
	rec.SessionID = startingSessionID(eng, mode)
	return s.startSpec(eng, rec)
}

// refuseInProgress refuses a record that another step may still be writing. The
// check reads the state rather than whether a background session is running,
// because another docket instance can hold an interactive session or be
// provisioning the clone.
func refuseInProgress(rec review.Record) error {
	if rec.InProgress() {
		return fmt.Errorf("%s is still %s", rec.Ref, rec.State)
	}
	return nil
}

// ErrClosed marks a refusal to review a pull request that merged or closed.
// Starting the review again cannot succeed.
var ErrClosed = errors.New("nothing left to review")

// RefuseClosed refuses to review a pull request that merged or closed.
func RefuseClosed(ref pr.Ref, state review.PRState) error {
	if state.Closed() {
		return fmt.Errorf("%s is %s, so there is %w", ref, state.Label(), ErrClosed)
	}
	return nil
}

// RefuseSubmit refuses a record that has no pending review docket may submit.
// Submittable refuses a pending draft only when its interactive session is open
// in a terminal.
func RefuseSubmit(rec review.Record) error {
	switch {
	case rec.Submittable():
		return nil
	case rec.HasPendingDraft():
		return fmt.Errorf("%s has a pending review, but its interactive session may still be using the clone; close it first", rec.Ref)
	case rec.State == review.StateFixed:
		return fmt.Errorf("%s's fixes in %s are not on GitHub yet; press enter to open the session and push them", rec.Ref, rec.Dir)
	default:
		return fmt.Errorf("%s is %s with no pending review to submit", rec.Ref, rec.State)
	}
}

// openPR reads the pull request and refuses it when it merged or closed.
func (s *Service) openPR(ctx context.Context, ref pr.Ref) (gh.PRInfo, error) {
	info, err := s.GH.PR(ctx, ref)
	if err != nil {
		return gh.PRInfo{}, err
	}
	return info, RefuseClosed(ref, info.State)
}

// rearm is the part of a re-review that writes nothing: the checks, and the
// fields a fresh start needs.
func (s *Service) rearm(rec review.Record, intent review.Intent, mode review.Mode) (review.Record, error) {
	if intent != review.IntentAppend && intent != review.IntentOverwrite {
		return rec, fmt.Errorf("a re-review appends or overwrites, not %q", intent)
	}
	if err := refuseInProgress(rec); err != nil {
		return rec, err
	}
	if mode == review.ModeBackground {
		if _, ok := engine.Background(rec.Engine); !ok {
			return rec, fmt.Errorf("%s cannot run a review in the background", rec.Engine)
		}
	}
	rec.Intent = intent
	rec.Mode = mode
	rec.SessionID = ""
	rec.Err = ""
	rec.CloneInUse = false
	// The record is written before the launch that marks it reviewing. Until
	// then it must not read as drafted, or another instance could submit the
	// draft this review is about to replace.
	rec.State = review.StatePreparing
	rec.ReviewID = 0
	return rec, nil
}

// Submit turns the session's pending review into a submitted one and closes the
// record. It reads the outcome back off GitHub rather than assuming it. The
// submitted review is no longer pending, which is what Decide reads as this
// session's submission, so archiving and the tier-2 cleanup run through the path
// detection already uses.
func (s *Service) Submit(ctx context.Context, rec review.Record, event, body string) (review.Record, error) {
	if !slices.Contains(review.SubmitEvents, event) {
		return rec, fmt.Errorf("%q is not a review event; use one of %s", event, strings.Join(review.SubmitEvents, ", "))
	}
	if err := RefuseSubmit(rec); err != nil {
		return rec, err
	}

	// The screen offers the same list, but it reads a login the service owns and
	// a fresh install has not cached one yet. Asking for it here is what makes
	// the refusal certain.
	me, err := s.Login(ctx)
	if err != nil {
		return s.recordErr(rec, err)
	}
	if !slices.Contains(review.SubmitEventsFor(rec.Author, me), event) {
		return rec, fmt.Errorf("GitHub refuses an approval of your own pull request; submit %s as %s instead", rec.Ref, review.EventComment)
	}
	if rec.State == review.StatePushed {
		return s.reviewFixes(ctx, rec, me, event, body)
	}

	// A reviewing row's background session may have replaced or submitted this
	// draft since the poll last read GitHub. GitHub refuses a submit of either id
	// and leaves the review as it was. This check replaces GitHub's 404 or 422
	// with a refusal that says what docket does next.
	if rec.State == review.StateReviewing {
		reviews, err := s.GH.Reviews(ctx, rec.Ref)
		if err != nil {
			return s.recordErr(rec, err)
		}
		if review.PendingReviewID(reviews, me) != rec.ReviewID {
			return rec, fmt.Errorf("%s's review %d is no longer pending; docket reads GitHub for %s again once its session ends its turn", rec.Ref, rec.ReviewID, rec.Ref)
		}
	}

	// The user is retrying, so drop what the last attempt recorded. Keeping it
	// would print the old failure under the row of a review that did go in.
	// recordErr writes a new one if this attempt fails too.
	rec.Err = ""
	if err := s.GH.SubmitReview(ctx, rec.Ref, rec.ReviewID, event, body); err != nil {
		return s.recordErr(rec, err)
	}
	return s.detect(ctx, rec)
}

// reviewFixes posts a new review of a fix review's pull request, on the commit
// docket saw pushed. A fix review leaves no pending review to submit.
//
// It refuses a pull request whose head has moved past that commit, because the
// review would then vouch for commits the user has not seen. It also refuses
// while I have a pending review there, which the new review would sit beside.
func (s *Service) reviewFixes(ctx context.Context, rec review.Record, me, event, body string) (review.Record, error) {
	if event != review.EventApprove && strings.TrimSpace(body) == "" {
		return rec, fmt.Errorf("GitHub needs a body to post a review of %s as %s", rec.Ref, event)
	}
	info, err := s.GH.PR(ctx, rec.Ref)
	if err != nil {
		return s.recordErr(rec, err)
	}
	if info.HeadRefOid != rec.FixHead {
		return rec, fmt.Errorf("%s has commits the fixes were not made on; press u to review them", rec.Ref)
	}
	reviews, err := s.GH.Reviews(ctx, rec.Ref)
	if err != nil {
		return s.recordErr(rec, err)
	}
	if review.PendingReviewID(reviews, me) != 0 {
		return rec, fmt.Errorf("you have a pending review on %s; submit or delete it on GitHub first", rec.Ref)
	}
	// A review of mine on FixHead posted since docket set the checkout up means
	// this submit would post a second one. An earlier submit may have posted and
	// then failed to read GitHub back, or the user reviewed on GitHub. A review
	// from before the checkout does not count, so an earlier comment or request
	// for changes on the same commit does not block an approval. The cutoff is
	// FixBaseAt rather than PriorReviewIDs, because reopening the session takes a
	// new snapshot that would count a review docket posted as an earlier one.
	since := slices.DeleteFunc(slices.Clone(reviews), func(r review.GHReview) bool {
		return r.SubmittedAt != nil && r.SubmittedAt.Before(rec.FixBaseAt.Add(-review.SubmitTolerance))
	})
	reviewed := slices.Contains(review.ReviewedCommits(since, me), rec.FixHead)
	rec.Err = ""
	if !reviewed {
		if err := s.GH.CreateReview(ctx, rec.Ref, rec.FixHead, event, body); err != nil {
			return s.recordErr(rec, err)
		}
	}
	done, err := s.detect(ctx, rec)
	// detect archives a review it reads as this session's. One it reads as
	// earlier, after a reopened session's new snapshot, leaves the row pushed.
	if reviewed && err == nil && done.State == review.StatePushed {
		return done, fmt.Errorf("you already reviewed %s at %s; press x to close the row", rec.Ref, rec.FixHead)
	}
	return done, err
}

// DraftBody reads the body of the record's pending review, which is the summary
// review-code posted with it. A review that is no longer on GitHub answers
// empty. Submit reports the missing review when the user tries it.
func (s *Service) DraftBody(ctx context.Context, rec review.Record) (string, error) {
	reviews, err := s.GH.Reviews(ctx, rec.Ref)
	if err != nil {
		return "", err
	}
	for _, r := range reviews {
		if r.ID == rec.ReviewID {
			return r.Body, nil
		}
	}
	return "", nil
}

// RefreshFixed re-reads a fixed record. The user may push its fixes from outside
// any session docket opened. The poll reads only running sessions, so the MCP
// tools call this to notice that push. A dirty checkout still holds local work.
// So does a clean one still on FixHead with commits its remote-tracking ref
// lacks, because a push from the checkout moves that ref. Either comes back as
// it is, with nothing read from GitHub and nothing written. A push made from
// another clone therefore waits for r. Any record that is not fixed comes back
// unchanged.
func (s *Service) RefreshFixed(ctx context.Context, rec review.Record) (review.Record, error) {
	if rec.State != review.StateFixed {
		return rec, nil
	}
	if dirty, err := s.Git.Dirty(ctx, rec.Dir); err == nil && dirty {
		return rec, nil
	}
	if head, err := s.Git.Head(ctx, rec.Dir); err == nil && head == rec.FixHead {
		if ahead, err := s.Git.Ahead(ctx, rec.Dir, rec.Upstream()); err == nil && ahead > 0 {
			return rec, nil
		}
	}
	return s.Refresh(ctx, rec)
}

// Refresh re-reads GitHub for a record whose session is over.
func (s *Service) Refresh(ctx context.Context, rec review.Record) (review.Record, error) {
	if err := refuseRefresh(rec); err != nil {
		return rec, err
	}
	// The user asked for a fresh read, so drop what the last attempt recorded.
	// detect writes a new Err through recordErr when this attempt fails too.
	rec.Err = ""
	return s.detect(ctx, rec)
}

// refuseRefresh refuses a record that a refresh must not read GitHub for. The
// poll owns a background review that is still running. A read in the middle of
// that session finds no draft yet. On a merged pull request, the archive that
// follows would stop the session and delete its clone.
func refuseRefresh(rec review.Record) error {
	if rec.InBackgroundSession() {
		return fmt.Errorf("%s is still reviewing in the background; docket reads GitHub for it when the session ends", rec.Ref)
	}
	if !detectable(rec) {
		return fmt.Errorf("%s is %s, so there is no session to read GitHub against", rec.Ref, rec.State)
	}
	return nil
}

// ownPR reports whether the pull request is the signed-in user's own. An
// unknown login answers no, because passing --self on somebody else's pull
// request is the mistake worth avoiding.
func ownPR(author, me string) bool {
	return me != "" && strings.EqualFold(author, me)
}

// detectable reports whether a record has a session to measure GitHub against. A
// record that never launched carries a zero StartedAt and no PriorReviewIDs, and
// Decide then reads any earlier review of mine as this session's submission. An
// adopted record never launched a review either. adopt stamps its StartedAt and
// snapshots every submitted review, which gives it a window to measure against.
func detectable(rec review.Record) bool {
	switch rec.State {
	case review.StateReviewing, review.StateDrafted, review.StateSubmitted, review.StateUnreviewed, review.StateReviewed, review.StateFixed, review.StatePushed:
		return true
	default:
		return false
	}
}

func (s *Service) detect(ctx context.Context, rec review.Record) (review.Record, error) {
	decided, reviews, err := s.decide(ctx, rec)
	if err != nil {
		return s.recordErr(rec, err)
	}
	return s.record(ctx, decided, reviews)
}

// decide reads GitHub and works out where the review stands. It writes nothing
// to the index.
//
// It reads the pull request's state as well as its reviews, because record
// archives a row whose pull request merged or closed. The two reads are
// independent and run concurrently. A detection then takes about as long as one
// read. A fix review's detection also reads the checkout, which may fetch the
// head branch and wait up to two seconds for review-code's lock on a tier-3
// worktree.
func (s *Service) decide(ctx context.Context, rec review.Record) (review.Record, []review.GHReview, error) {
	me, err := s.Login(ctx)
	if err != nil {
		return rec, nil, err
	}
	type prRead struct {
		info gh.PRInfo
		err  error
	}
	pull := make(chan prRead, 1)
	go func() {
		info, err := s.GH.PR(ctx, rec.Ref)
		pull <- prRead{info: info, err: err}
	}()
	reviews, err := s.GH.Reviews(ctx, rec.Ref)
	read := <-pull
	if err != nil {
		return rec, nil, err
	}
	if read.err != nil {
		return rec, nil, read.err
	}

	rec.PRState = read.info.State
	rec.NotesPath = s.Cfg.NotesPath(rec.Ref.Org, rec.Ref.Repo, rec.Ref.Number)
	state, reviewID := review.Decide(reviews, me, rec.StartedAt, rec.PriorReviewIDs)
	// Decide cannot produce reviewed. It reads an adopted row with nothing new on
	// GitHub as unreviewed. Taking that state would drop the reviewed state that
	// adopt gave the row.
	if rec.State == review.StateReviewed && state == review.StateUnreviewed {
		return rec, reviews, nil
	}
	// A fix review posts nothing, so GitHub cannot say where it stands unless
	// the user submitted a review. Anything else, including a pending review
	// left from before the launch, is read off the checkout.
	if rec.Fix && state != review.StateSubmitted {
		checkout, err := s.readCheckout(ctx, rec, read.info.HeadRefOid)
		if err != nil {
			return rec, nil, err
		}
		rec.State = checkout.State(rec.FixBase, fixNotesWritten(rec))
		rec.FixHead = checkout.Head
		rec.ReviewID = 0
		if checkout.FetchErr != nil {
			rec.Err = fmt.Sprintf("docket could not fetch %s, so it compared the checkout with what it fetched last: %v", rec.Upstream(), checkout.FetchErr)
		}
		return rec, reviews, nil
	}
	rec.State = state
	rec.ReviewID = reviewID
	return rec, reviews, nil
}

// fixNotesWritten reports whether review-code wrote a fix review's notes since
// docket stamped the checkout's base. It composes the notes after the fix pass
// and puts a Fix Summary in them. The append and overwrite paths do not write
// the notes file before that step.
func fixNotesWritten(rec review.Record) bool {
	info, err := os.Stat(rec.NotesPath)
	if err != nil || !info.ModTime().After(rec.FixBaseAt) {
		return false
	}
	notes, err := os.ReadFile(rec.NotesPath)
	return err == nil && strings.Contains(string(notes), "## Fix Summary")
}

// readCheckout reads a fix review's working tree. remoteHead is a commit the
// caller knows is on the remote branch, or empty when it knows none.
//
// It fetches the branch unless the checkout is dirty or on remoteHead. A dirty
// checkout holds local work whatever the remote has. A clean checkout on
// remoteHead has nothing local. A clean checkout still on FixBase has nothing
// local either, because every commit up to FixBase came from GitHub. A
// force-push can move the branch off FixBase, and counting against the
// rewritten branch would then read those commits as local.
//
// A failed fetch is not an error. The head branch is deleted when its pull
// request merges, and every fetch fails from then on. Counting against the
// remote-tracking ref from the last fetch that worked can only count a commit
// pushed since then as local, so a checkout that reads as clean is clean.
func (s *Service) readCheckout(ctx context.Context, rec review.Record, remoteHead string) (review.Checkout, error) {
	var c review.Checkout
	var err error
	if c.Head, err = s.Git.Head(ctx, rec.Dir); err != nil {
		return c, err
	}
	if c.Dirty, err = s.Git.Dirty(ctx, rec.Dir); err != nil {
		return c, err
	}
	if c.Dirty || (remoteHead != "" && c.Head == remoteHead) {
		return c, nil
	}
	if rec.Tier == tier.Tier3 {
		c.FetchErr = s.Worktrees.Fetch(ctx, rec.Ref, rec.Dir, rec.Remote, rec.Branch)
	} else {
		c.FetchErr = s.Git.FetchBranch(ctx, rec.Dir, rec.Remote, rec.Branch)
	}
	// The fetch runs for a checkout on FixBase too, because refreshCheckout
	// resets to the ref it updates.
	if c.Head == rec.FixBase {
		return c, nil
	}
	if c.Ahead, err = s.Git.Ahead(ctx, rec.Dir, rec.Upstream()); err != nil {
		return c, err
	}
	return c, nil
}

// record writes what decide worked out. It archives a review that went in, and a
// row whose pull request merged or closed with nothing pending.
func (s *Service) record(ctx context.Context, rec review.Record, reviews []review.GHReview) (review.Record, error) {
	if rec.Finished() {
		return s.Archive(ctx, rec)
	}
	if rec.State != review.StateSubmitted {
		return rec, s.append(rec)
	}
	rec.SubmittedAt = submittedAt(reviews, rec.ReviewID, s.now())
	if err := s.append(rec); err != nil {
		return rec, err
	}
	return s.Archive(ctx, rec)
}

// detectPolled reads GitHub for a record the poll found finished.
//
// A failure is kept on the record rather than written to the index. The poll
// comes back every few seconds and the record stays running until GitHub
// answers, so recording each failure would append the same event for as long as
// GitHub is unreachable. Nothing is lost: PollBackground hands these records
// straight to the screen, so the row still carries the error, and the next poll
// tries again.
func (s *Service) detectPolled(ctx context.Context, rec review.Record) review.Record {
	decided, reviews, err := s.decide(ctx, rec)
	if err != nil {
		rec.Err = err.Error()
		return rec
	}
	saved, err := s.record(ctx, decided, reviews)
	if err != nil {
		saved.Err = err.Error()
	}
	return saved
}

// Notes reads the review review-code wrote for a record. A file that is not
// there reports missing rather than failing: review-code writes it during the
// session, so a record that has not reached one yet simply has no notes.
func (s *Service) Notes(rec review.Record) (string, bool, error) {
	if rec.NotesPath == "" {
		return "", true, nil
	}
	data, err := os.ReadFile(rec.NotesPath)
	if os.IsNotExist(err) {
		return "", true, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read notes for %s: %w", rec.Ref, err)
	}
	return string(data), false, nil
}

// EditNotesSpec is the command that opens a record's notes in the user's editor.
func (s *Service) EditNotesSpec(rec review.Record) (exec.CommandSpec, error) {
	if rec.NotesPath == "" {
		return exec.CommandSpec{}, fmt.Errorf("%s has no notes path", rec.Ref)
	}
	return EditorSpec(os.Getenv("EDITOR"), rec.NotesPath), nil
}

// DefaultEditor opens the notes when the environment names no editor. vi is on
// every machine docket runs on.
const DefaultEditor = "vi"

// EditorSpec builds the command that opens path in editor, which is $EDITOR.
//
// The spec names no directory. The path is absolute, and the notes file may not
// exist yet. Pointing the child at its parent would fail to start the editor on
// exactly the record that has no notes to read.
func EditorSpec(editor, path string) exec.CommandSpec {
	if strings.TrimSpace(editor) == "" {
		editor = DefaultEditor
	}
	return shellSpec(editor, path)
}

// shellSpec runs line with arg after it. line is $EDITOR or $BROWSER, which is
// a shell command line rather than an executable and its arguments. git and
// crontab read $EDITOR the same way. Running it through sh accepts both
// "code --wait" and an executable whose path contains a space. arg is a
// positional parameter, so the shell never reads it as code.
func shellSpec(line, arg string) exec.CommandSpec {
	return exec.CommandSpec{
		Path: "sh",
		Args: []string{"-c", line + ` "$1"`, "sh", arg},
	}
}

// Archive cleans up what docket created and closes the record. It leaves
// review-code's notes file alone, because that is the part worth keeping.
//
// It ends the agent session first. Submitting is the ordinary end of a
// background review, and the agent goes on holding the session it ran until
// something stops it. Left alone, one session would be held per review.
//
// A session that is still working is left running. The user can open a drafted
// row's session and give it more work, and submitting the draft must not cut
// that work off. A session docket cannot read is left the same way.
//
// A stop that fails leaves the clone alone, and so does a session left running.
// Archiving is something docket does on its own once a review goes in, so there
// is nobody to weigh an agent that may still be writing against a directory
// removed under it. The record closes, and the directory stays for the user to
// deal with. Abandon makes the opposite call, because there the user asked.
//
// An archive requested from inside the record's clone waits. The session that
// asked is still running there. Removing the clone would delete the directory it
// works in. Stopping the record's session could end the caller. The record stays
// open with the reason. The session's exit, or r on its row, then archives it.
func (s *Service) Archive(ctx context.Context, rec review.Record) (review.Record, error) {
	if s.callerInClone(rec) {
		rec.Err = fmt.Sprintf("docket kept the clone at %s because a session is running in it. The review archives when that session ends in docket, or when you press r on its row afterwards", rec.Dir)
		rec.CloneInUse = true
		return rec, s.append(rec)
	}
	// The status line prints an archived row's Err, and the row is hidden once it
	// archives. A failure from an earlier step would read as this archive's.
	rec.Err = ""
	rec.CloneInUse = false
	rec, stopped := s.stopFinished(ctx, rec)
	if !stopped {
		at := s.now()
		rec.ArchivedAt = &at
		rec.State = review.StateArchived
		return rec, s.append(rec)
	}
	if err := s.cleanup(ctx, rec); err != nil {
		return s.recordErr(rec, err)
	}
	at := s.now()
	rec.ArchivedAt = &at
	rec.State = review.StateArchived
	return rec, s.append(rec)
}

// Abandon drops a review the user is done with. The record stays in the index.
//
// A stop that fails is recorded and the abandon goes on anyway. The user asked
// to be rid of this review, and a row that cannot be closed because its agent
// will not answer is worse than a directory deleted under one.
func (s *Service) Abandon(ctx context.Context, rec review.Record) (review.Record, error) {
	rec, _ = s.stopBackground(ctx, rec)
	if err := s.cleanup(ctx, rec); err != nil {
		return s.recordErr(rec, err)
	}
	rec.State = review.StateAbandoned
	return rec, s.append(rec)
}

// cleanup deletes the record's checkout: the tier-2 clone, or the tier-3
// worktree and its branch. A tier-1 worktree belongs to review-code, which
// tears it down at its own session end. docket reports that worktree and never
// deletes it.
//
// It keeps a fix review's checkout that holds work GitHub does not have, or
// that it cannot read, and says why. Archive and Abandon then leave the record
// open, which also keeps a new review of the pull request from resetting the
// checkout.
func (s *Service) cleanup(ctx context.Context, rec review.Record) error {
	if !rec.HasCheckout() {
		return nil
	}
	// A checkout whose setup failed before it existed holds nothing. docket owns
	// no branch for it.
	if _, err := os.Stat(rec.Dir); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	// provision records the remote last. A launch needs provision to finish. A fix
	// record with no remote therefore ran no session in its checkout. It also has
	// no remote-tracking ref to count against.
	if rec.Fix && rec.Remote != "" {
		remoteHead, err := s.knownRemoteHead(ctx, rec)
		if err != nil {
			return fmt.Errorf("docket kept %s because it could not read %s from GitHub to check the fixes: %w", rec.Dir, rec.Ref, err)
		}
		checkout, err := s.readCheckout(ctx, rec, remoteHead)
		if err != nil {
			return fmt.Errorf("docket kept %s because it could not check it for fixes that are not on GitHub: %w", rec.Dir, err)
		}
		if checkout.Local() {
			return fmt.Errorf("docket kept %s because it holds fixes that are not on GitHub; push or discard them, then try again", rec.Dir)
		}
		// A count against a stale ref misses fixes that a force-push left only in
		// the checkout. A checkout still on FixBase holds nothing from the session.
		// One on remoteHead fetched nothing.
		if checkout.FetchErr != nil && checkout.Head != rec.FixBase {
			return fmt.Errorf("docket kept %s because it could not fetch %s to check the fixes: %w", rec.Dir, rec.Upstream(), checkout.FetchErr)
		}
	}
	if rec.Tier == tier.Tier3 {
		return s.Worktrees.Remove(ctx, rec.WorktreeOf, rec.Ref, rec.Dir, rec.Branch)
	}
	return s.Cloner.Remove(rec.Dir)
}

// knownRemoteHead is a commit that cleanup may treat as on GitHub without a
// fetch, or empty when it knows none. A row that changed nothing is still on
// FixBase, and every commit up to FixBase came from GitHub. A pushed row's
// FixHead is on GitHub while it is the pull request's head. GitHub keeps that
// head after the pull request merges or closes and its branch is deleted, when
// the user's clone may have pruned the tracking ref and a count would fail. A
// bot that force-pushed after the fixes moves the head. The count then decides.
// On any other row FixHead may be a local commit.
func (s *Service) knownRemoteHead(ctx context.Context, rec review.Record) (string, error) {
	if rec.State != review.StatePushed {
		return "", nil
	}
	if rec.NoChanges() {
		return rec.FixHead, nil
	}
	info, err := s.GH.PR(ctx, rec.Ref)
	if err != nil {
		return "", err
	}
	if info.HeadRefOid == rec.FixHead {
		return rec.FixHead, nil
	}
	return "", nil
}

// callerInClone reports whether the agent session this service runs under works
// inside rec's clone. A tier-1 record has no directory of its own, so a match on
// the scratch directory would not say which record the caller belongs to.
func (s *Service) callerInClone(rec review.Record) bool {
	if s.CallerDir == "" || !rec.HasCheckout() {
		return false
	}
	rel, err := filepath.Rel(engine.RealPath(rec.Dir), engine.RealPath(s.CallerDir))
	return err == nil && filepath.IsLocal(rel)
}

// Reconcile re-runs detection on records left mid-session. It assumes this is the
// only docket instance. Another instance's session is still running against a
// record that reads as reviewing here, and finding that review submitted archives
// it, which deletes the clone that session is working in.
func (s *Service) Reconcile(ctx context.Context) ([]review.Record, error) {
	return s.detectWhere(ctx, func(rec review.Record) bool {
		return rec.State == review.StateReviewing && rec.Mode == review.ModeInteractive
	})
}

// RefreshAll re-reads GitHub for every record whose session is over, which is
// what the dashboard's refresh-everything key asks for.
//
// It skips a record whose clone is in use. The dashboard runs outside that
// clone, so the archive a refresh finishes would delete the directory the
// session works in. r on the row finishes it once the session ends.
func (s *Service) RefreshAll(ctx context.Context) ([]review.Record, error) {
	return s.detectWhere(ctx, func(rec review.Record) bool {
		return refuseRefresh(rec) == nil && !rec.CloneInUse
	})
}

// detectWhere re-reads GitHub for the records that match. A record whose
// detection fails carries the error detect recorded on it, so one unreachable
// pull request does not cost the user the rest of the list.
func (s *Service) detectWhere(ctx context.Context, match func(review.Record) bool) ([]review.Record, error) {
	records, err := s.Records()
	if err != nil {
		return nil, err
	}
	for i, rec := range records {
		if !match(rec) {
			continue
		}
		rec.Err = ""
		// detect returns the record with its error already recorded, so taking it
		// either way is what puts that error on the row.
		updated, _ := s.detect(ctx, rec)
		records[i] = updated
	}
	return records, nil
}

// append writes the record as one event. The event carries the whole record, so a
// folded record never depends on how many events came before it.
func (s *Service) append(rec review.Record) error {
	eventType, ok := review.EventForState(rec.State)
	if !ok {
		return fmt.Errorf("record %s has no event type for state %q", rec.ID, rec.State)
	}
	fields, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encode record %s: %w", rec.ID, err)
	}
	return s.Store.Append(review.Event{ID: rec.ID, At: s.now(), Type: eventType, Fields: fields})
}

func (s *Service) fail(rec review.Record, plan Plan, cause error) (review.Record, Plan, error) {
	rec, _ = s.recordErr(rec, cause)
	return rec, plan, cause
}

// snapshot records my reviews on GitHub as a launch finds them, so that an
// older review of mine does not look like this session's. It also returns my
// login.
func (s *Service) snapshot(ctx context.Context, rec review.Record) (review.Record, string, error) {
	me, reviews, err := s.myReviews(ctx, rec.Ref, s.Login)
	if err != nil {
		return rec, "", err
	}
	rec.PriorReviewIDs = review.PriorSubmittedIDs(reviews, me)
	rec.PriorPendingID = review.PendingReviewID(reviews, me)
	return rec, me, nil
}

// myReviews is the login docket compares authors against and every review on the
// pull request. login is Login, or peekLogin where nothing may be written.
func (s *Service) myReviews(ctx context.Context, ref pr.Ref, login func(context.Context) (string, error)) (string, []review.GHReview, error) {
	me, err := login(ctx)
	if err != nil {
		return "", nil, err
	}
	reviews, err := s.GH.Reviews(ctx, ref)
	if err != nil {
		return "", nil, err
	}
	return me, reviews, nil
}

func (s *Service) recordErr(rec review.Record, cause error) (review.Record, error) {
	rec.Err = cause.Error()
	if err := s.append(rec); err != nil {
		return rec, err
	}
	return rec, cause
}

func submittedAt(reviews []review.GHReview, id int64, fallback time.Time) *time.Time {
	for _, r := range reviews {
		if r.ID == id && r.SubmittedAt != nil {
			at := r.SubmittedAt.UTC()
			return &at
		}
	}
	return &fallback
}

// checkDir refuses a record whose working directory is not there. The clone
// behind a tier-2 review can be deleted from outside docket, and an agent
// started in a directory that is gone fails in its own way rather than docket's.
func checkDir(rec review.Record) error {
	if rec.Dir == "" {
		return fmt.Errorf("record %s has no directory to run in", rec.ID)
	}
	if info, err := os.Stat(rec.Dir); err != nil || !info.IsDir() {
		return fmt.Errorf("%s is gone; start the review again", rec.Dir)
	}
	return nil
}

// startingSessionID is the id a new record carries before it launches.
//
// A background session has none. claude refuses the --session-id a background
// start passes and mints its own, so an id minted here would name a session that
// never existed and a later resume would fail on it. The first poll fills the
// record in from what the agent reports.
func startingSessionID(eng engine.Engine, mode review.Mode) string {
	if mode == review.ModeBackground {
		return ""
	}
	return eng.NewSessionID()
}
