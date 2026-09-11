// Package session is the only entry point the UI calls. It prepares a review,
// hands back the command to launch, and reads what happened afterwards.
package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
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
	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/core/tier"
)

// Service holds everything a review needs and owns every write to the index.
type Service struct {
	Cfg    config.Config
	Paths  config.Paths
	Store  *index.Store
	GH     gh.GitHub
	Git    git.Git
	Cloner *clone.Cloner
	Now    func() time.Time
	NewID  func() string
}

// Plan is what Prepare worked out, for the UI to show before launching.
type Plan struct {
	Tier       tier.Tier
	Dir        string
	LocalClone string
	Worktree   string
	NotesPath  string
}

// Description says what the tier means in one line.
func (p Plan) Description() string {
	switch p.Tier {
	case tier.Tier1:
		return fmt.Sprintf("review-code knows this repo (%s), so it provisions and tears down its own worktree at %s; docket clones nothing", p.LocalClone, p.Worktree)
	case tier.Tier2:
		return fmt.Sprintf("review-code has no clone for this repo, so docket checks the head out at %s", p.Dir)
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
	if s.Cfg.GitHubUser != "" {
		return s.Cfg.GitHubUser, nil
	}
	login, err := s.GH.Login(ctx)
	if err != nil {
		return "", err
	}
	s.Cfg.GitHubUser = login
	if err := config.Save(s.Paths.Config, s.Cfg); err != nil {
		return login, fmt.Errorf("cache github login: %w", err)
	}
	return login, nil
}

// Prepare resolves the pull request, works out the tier, provisions the
// directory the session will run in, and snapshots the reviews that already
// exist. The record is written before provisioning, so a clone that fails leaves
// a row the user can retry.
func (s *Service) Prepare(ctx context.Context, ref pr.Ref, engineName string) (review.Record, Plan, error) {
	rec, plan, info, _, err := s.resolve(ctx, ref, engineName)
	if err != nil {
		return review.Record{}, Plan{}, err
	}
	if err := s.append(rec); err != nil {
		return rec, plan, err
	}

	if plan.Tier == tier.Tier1 {
		if err := s.ensureScratch(ctx); err != nil {
			return s.fail(rec, plan, err)
		}
	} else {
		dir, err := s.Cloner.Ensure(ctx, ref, info)
		if err != nil {
			return s.fail(rec, plan, err)
		}
		rec.Dir = dir
	}

	ids, err := s.priorIDs(ctx, ref)
	if err != nil {
		return s.fail(rec, plan, err)
	}
	rec.PriorReviewIDs = ids
	rec.Err = ""

	if err := s.append(rec); err != nil {
		return rec, plan, err
	}
	return rec, plan, nil
}

// resolve works out everything about a review that reads nothing but GitHub and
// repos.conf. Prepare goes on to provision and record. Explain stops here.
func (s *Service) resolve(ctx context.Context, ref pr.Ref, engineName string) (review.Record, Plan, gh.PRInfo, engine.Engine, error) {
	eng, err := engine.For(engineName)
	if err != nil {
		return review.Record{}, Plan{}, gh.PRInfo{}, nil, err
	}

	info, err := s.GH.PR(ctx, ref)
	if err != nil {
		return review.Record{}, Plan{}, gh.PRInfo{}, nil, err
	}

	entries := reposconf.ParseFile(s.Cfg.ReposConfPath())
	decided, localClone := tier.Decide(ref, entries, func(path string) bool {
		return s.Git.IsRepo(ctx, path)
	})

	plan := Plan{
		Tier:       decided,
		LocalClone: localClone,
		NotesPath:  s.Cfg.NotesPath(ref.Org, ref.Repo, ref.Number),
	}
	if decided == tier.Tier1 {
		plan.Dir = s.Paths.Scratch
		plan.Worktree = s.Cfg.WorktreeDir(ref.Org, ref.Repo, ref.Number)
	} else {
		plan.Dir = s.Cloner.Dir(ref)
	}

	rec := review.Record{
		ID:        s.newID(),
		Ref:       ref,
		URL:       ref.URL(),
		Title:     info.Title,
		Author:    info.Author.Login,
		Engine:    eng.Name(),
		Tier:      decided,
		Mode:      review.ModeInteractive,
		Dir:       plan.Dir,
		SessionID: eng.NewSessionID(),
		State:     review.StatePreparing,
		NotesPath: plan.NotesPath,
	}
	return rec, plan, info, eng, nil
}

// Explain says what a review would do without doing any of it. A dry run must not
// provision. A check that reads what provisioning would have written then tells
// the user nothing, so Explain stops before both.
func (s *Service) Explain(ctx context.Context, ref pr.Ref, engineName string) (Plan, exec.CommandSpec, error) {
	rec, plan, _, eng, err := s.resolve(ctx, ref, engineName)
	if err != nil {
		return Plan{}, exec.CommandSpec{}, err
	}
	return plan, eng.Start(rec, s.Cfg.ReviewCodeDir), nil
}

// ensureScratch keeps the tier-1 launch directory a git repository with no
// remote. review-code then reads the org and repo as unknown and takes its
// cross-repo path, and interactive codex still sees a git repository.
func (s *Service) ensureScratch(ctx context.Context) error {
	if s.Git.IsRepo(ctx, s.Paths.Scratch) {
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

func (s *Service) launch(ctx context.Context, rec review.Record, resume bool) (review.Record, exec.CommandSpec, error) {
	eng, err := engine.For(rec.Engine)
	if err != nil {
		return rec, exec.CommandSpec{}, err
	}
	if rec.Dir == "" {
		return rec, exec.CommandSpec{}, fmt.Errorf("record %s has no directory to run in", rec.ID)
	}
	if info, err := os.Stat(rec.Dir); err != nil || !info.IsDir() {
		return rec, exec.CommandSpec{}, fmt.Errorf("%s is gone; start the review again", rec.Dir)
	}

	spec, ok := exec.CommandSpec{}, false
	if resume {
		spec, ok = eng.Resume(rec, s.Cfg.ReviewCodeDir)
	}
	if !ok {
		if rec.SessionID == "" {
			rec.SessionID = eng.NewSessionID()
		}
		spec = eng.Start(rec, s.Cfg.ReviewCodeDir)
	}

	// Every launch opens a new detection window. A resume also re-snapshots the
	// already-submitted reviews, because its record may be hours old. The launch
	// that follows Prepare does not, because Prepare just took that snapshot.
	rec.StartedAt = s.now()
	rec.State = review.StateReviewing
	rec.Err = ""
	if resume {
		if ids, err := s.priorIDs(ctx, rec.Ref); err == nil {
			rec.PriorReviewIDs = ids
		}
	}

	if err := s.append(rec); err != nil {
		return rec, spec, err
	}
	return rec, spec, nil
}

// ExplainResume is the command a resume would run, without recording anything. A
// dry run needs to report it, and reporting is all it may do.
func (s *Service) ExplainResume(rec review.Record) (exec.CommandSpec, error) {
	eng, err := engine.For(rec.Engine)
	if err != nil {
		return exec.CommandSpec{}, err
	}
	if spec, ok := eng.Resume(rec, s.Cfg.ReviewCodeDir); ok {
		return spec, nil
	}
	return eng.Start(rec, s.Cfg.ReviewCodeDir), nil
}

// AfterExit reads what the finished session left on GitHub. It runs whatever the
// child's exit status was, because a session the user interrupted may still have
// created the draft.
func (s *Service) AfterExit(ctx context.Context, rec review.Record, childErr error) (review.Record, error) {
	if childErr != nil {
		rec.Err = childErr.Error()
	}
	return s.detect(ctx, rec)
}

// Refresh re-reads GitHub for a record whose session is over.
func (s *Service) Refresh(ctx context.Context, rec review.Record) (review.Record, error) {
	return s.detect(ctx, rec)
}

func (s *Service) detect(ctx context.Context, rec review.Record) (review.Record, error) {
	me, err := s.Login(ctx)
	if err != nil {
		return s.recordErr(rec, err)
	}
	reviews, err := s.GH.Reviews(ctx, rec.Ref)
	if err != nil {
		return s.recordErr(rec, err)
	}

	state, reviewID := review.Decide(reviews, me, rec.StartedAt, rec.PriorReviewIDs)
	rec.State = state
	rec.ReviewID = reviewID
	rec.NotesPath = s.Cfg.NotesPath(rec.Ref.Org, rec.Ref.Repo, rec.Ref.Number)

	if state != review.StateSubmitted {
		return rec, s.append(rec)
	}

	rec.SubmittedAt = submittedAt(reviews, reviewID, s.now())
	if err := s.append(rec); err != nil {
		return rec, err
	}
	return s.Archive(rec)
}

// Archive cleans up what docket created and closes the record. It leaves
// review-code's notes file alone, because that is the part worth keeping.
func (s *Service) Archive(rec review.Record) (review.Record, error) {
	if err := s.cleanup(rec); err != nil {
		return s.recordErr(rec, err)
	}
	at := s.now()
	rec.ArchivedAt = &at
	rec.State = review.StateArchived
	return rec, s.append(rec)
}

// Abandon drops a review the user is done with. The record stays in the index.
func (s *Service) Abandon(rec review.Record) (review.Record, error) {
	if err := s.cleanup(rec); err != nil {
		return s.recordErr(rec, err)
	}
	rec.State = review.StateAbandoned
	return rec, s.append(rec)
}

// cleanup deletes the tier-2 clone. A tier-1 worktree belongs to review-code,
// which tears it down at its own session end. docket reports that worktree and
// never deletes it.
func (s *Service) cleanup(rec review.Record) error {
	if rec.Tier != tier.Tier2 || rec.Dir == "" {
		return nil
	}
	return s.Cloner.Remove(rec.Dir)
}

// Reconcile re-runs detection on records left mid-session. docket was the parent
// of every interactive session, so a record still reviewing at startup has no
// live child.
func (s *Service) Reconcile(ctx context.Context) ([]review.Record, error) {
	return s.detectWhere(ctx, func(rec review.Record) bool {
		return rec.State == review.StateReviewing && rec.Mode == review.ModeInteractive
	})
}

// RefreshAll re-reads GitHub for every record whose session is over, which is
// what the dashboard's refresh-everything key asks for.
func (s *Service) RefreshAll(ctx context.Context) ([]review.Record, error) {
	return s.detectWhere(ctx, func(rec review.Record) bool {
		switch rec.State {
		case review.StateReviewing, review.StateDrafted, review.StateUnreviewed:
			return true
		default:
			return false
		}
	})
}

// detectWhere re-reads GitHub for the records that match. A record whose
// detection fails keeps its stored state, so one unreachable pull request does
// not cost the user the rest of the list.
func (s *Service) detectWhere(ctx context.Context, match func(review.Record) bool) ([]review.Record, error) {
	records, err := s.Records()
	if err != nil {
		return nil, err
	}
	for i, rec := range records {
		if !match(rec) {
			continue
		}
		if updated, err := s.detect(ctx, rec); err == nil {
			records[i] = updated
		}
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

// priorIDs is the snapshot that stops an older review of mine from looking like
// this session's submission.
func (s *Service) priorIDs(ctx context.Context, ref pr.Ref) ([]int64, error) {
	me, err := s.Login(ctx)
	if err != nil {
		return nil, err
	}
	reviews, err := s.GH.Reviews(ctx, ref)
	if err != nil {
		return nil, err
	}
	return review.PriorSubmittedIDs(reviews, me), nil
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
