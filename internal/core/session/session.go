// Package session is the only entry point the UI calls. It prepares a review,
// hands back the command to launch, and reads what happened afterwards.
package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

func (s *Service) enginePaths() engine.Paths {
	return engine.Paths{
		Grant:         s.Cfg.AgentDirs(),
		CodexSessions: s.Cfg.CodexSessionsDir,
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
// a row carrying the reason. Nothing re-provisions that row: resume does not, and
// a second Prepare is refused while it is open, so the user abandons it with x
// and starts again with n.
func (s *Service) Prepare(ctx context.Context, ref pr.Ref, engineName string) (review.Record, Plan, error) {
	rec, plan, info, _, err := s.resolve(ctx, ref, engineName)
	if err != nil {
		return review.Record{}, Plan{}, err
	}

	// Two records for one pull request derive the same clone directory, so
	// abandoning either deletes the clone the other is using. The read and the
	// append take separate locks, so a second instance can still pass this check
	// before either appends. The check sits here rather than before resolve to keep
	// that window off the call to GitHub. Closing it needs a compare-and-append the
	// store does not have.
	records, err := s.Records()
	if err != nil {
		return review.Record{}, Plan{}, err
	}
	if slices.ContainsFunc(records, func(r review.Record) bool {
		return sameRef(r.Ref, ref) && r.State.Open()
	}) {
		return review.Record{}, Plan{}, fmt.Errorf("%s is already open; abandon it first", ref)
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
	return plan, eng.Start(rec, s.enginePaths()), nil
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
	if rec.Dir == "" {
		return rec, exec.CommandSpec{}, fmt.Errorf("record %s has no directory to run in", rec.ID)
	}
	if info, err := os.Stat(rec.Dir); err != nil || !info.IsDir() {
		return rec, exec.CommandSpec{}, fmt.Errorf("%s is gone; start the review again", rec.Dir)
	}

	if resume {
		if spec, ok := eng.Resume(rec, s.enginePaths()); ok {
			return rec, spec, nil
		}
	}
	if rec.SessionID == "" {
		rec.SessionID = eng.NewSessionID()
	}
	return rec, eng.Start(rec, s.enginePaths()), nil
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
		ids, err := s.priorIDs(ctx, rec.Ref)
		if err != nil {
			return rec, spec, fmt.Errorf("refresh the submitted reviews for %s: %w", rec.Ref, err)
		}
		rec.PriorReviewIDs = ids
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
	eng, err := engine.For(rec.Engine)
	if err != nil {
		return rec
	}
	id, err := eng.CaptureSessionID(rec, s.enginePaths())
	if err != nil {
		rec.Err = err.Error()
		return rec
	}
	if id != "" {
		rec.SessionID = id
	}
	return rec
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
	if !rec.Submittable() {
		return rec, fmt.Errorf("%s is %s with no pending review to submit", rec.Ref, rec.State)
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

	if err := s.GH.SubmitReview(ctx, rec.Ref, rec.ReviewID, event, body); err != nil {
		return s.recordErr(rec, err)
	}
	return s.detect(ctx, rec)
}

// Refresh re-reads GitHub for a record whose session is over.
func (s *Service) Refresh(ctx context.Context, rec review.Record) (review.Record, error) {
	if !detectable(rec) {
		return rec, fmt.Errorf("%s is %s, so there is no session to read GitHub against", rec.Ref, rec.State)
	}
	// The user asked for a fresh read, so drop what the last attempt recorded.
	// detect writes a new Err through recordErr when this attempt fails too.
	rec.Err = ""
	return s.detect(ctx, rec)
}

// sameRef reports whether two references name one pull request. GitHub compares an
// owner and a repository name without case, so o/r#7 and O/R#7 are one review and
// one clone directory.
func sameRef(a, b pr.Ref) bool {
	return a.Number == b.Number && strings.EqualFold(a.Org, b.Org) && strings.EqualFold(a.Repo, b.Repo)
}

// detectable reports whether a record has a session to measure GitHub against. A
// record that never launched carries a zero StartedAt and no PriorReviewIDs, and
// Decide then reads any earlier review of mine as this session's submission.
func detectable(rec review.Record) bool {
	switch rec.State {
	case review.StateReviewing, review.StateDrafted, review.StateSubmitted, review.StateUnreviewed:
		return true
	default:
		return false
	}
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

// EditorSpec builds the command that opens path in editor, which is $EDITOR and
// may carry arguments of its own, as "code --wait" does.
//
// The spec names no directory. The path is absolute, and the notes file may not
// exist yet. Pointing the child at its parent would fail to start the editor on
// exactly the record that has no notes to read.
func EditorSpec(editor, path string) exec.CommandSpec {
	fields := strings.Fields(editor)
	if len(fields) == 0 {
		fields = []string{DefaultEditor}
	}
	return exec.CommandSpec{
		Path: fields[0],
		Args: append(slices.Clone(fields[1:]), path),
	}
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
func (s *Service) RefreshAll(ctx context.Context) ([]review.Record, error) {
	return s.detectWhere(ctx, detectable)
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
