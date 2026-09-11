package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/haacked/docket/internal/core/clone"
	"github.com/haacked/docket/internal/core/config"
	"github.com/haacked/docket/internal/core/gh"
	"github.com/haacked/docket/internal/core/index"
	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/core/tier"
)

var (
	listed   = pr.Ref{Org: "posthog", Repo: "posthog", Number: 101}
	unlisted = pr.Ref{Org: "haacked", Repo: "docket", Number: 7}
	start    = time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
)

type fakeGH struct {
	login     string
	loginErr  error
	info      gh.PRInfo
	infoErr   error
	reviews   []review.GHReview
	reviewErr error
	submitted []string
	logins    int
}

func (f *fakeGH) Login(context.Context) (string, error) {
	f.logins++
	return f.login, f.loginErr
}

func (f *fakeGH) PR(context.Context, pr.Ref) (gh.PRInfo, error) { return f.info, f.infoErr }

func (f *fakeGH) Reviews(context.Context, pr.Ref) ([]review.GHReview, error) {
	return f.reviews, f.reviewErr
}

func (f *fakeGH) SubmitReview(_ context.Context, _ pr.Ref, id int64, event, _ string) error {
	f.submitted = append(f.submitted, event)
	_ = id
	return nil
}

type fakeGit struct {
	calls      []string
	repos      map[string]bool
	branches   map[string]string
	checkedOut string
	failAt     string
}

func newFakeGit() *fakeGit {
	return &fakeGit{repos: map[string]bool{}, branches: map[string]string{}}
}

func (f *fakeGit) record(name string) error {
	f.calls = append(f.calls, name)
	if f.failAt == name {
		return errors.New("git failed: " + name)
	}
	return nil
}

func (f *fakeGit) IsRepo(_ context.Context, dir string) bool { return f.repos[dir] }

func (f *fakeGit) Init(_ context.Context, dir string) error {
	if err := f.record("init " + dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f.repos[dir] = true
	return nil
}

func (f *fakeGit) RemoteAdd(_ context.Context, _, _, _ string) error { return f.record("remote") }
func (f *fakeGit) SymbolicRef(_ context.Context, _, _, _ string) error {
	return f.record("symbolic-ref")
}

func (f *fakeGit) FetchPR(_ context.Context, dir, _ string, _ int, _ string, _ int) error {
	if err := f.record("fetch"); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi"), 0o644)
}

func (f *fakeGit) Checkout(_ context.Context, dir, branch string) error {
	if err := f.record("checkout"); err != nil {
		return err
	}
	f.repos[dir] = true
	f.branches[dir] = branch
	f.checkedOut = branch
	return nil
}

func (f *fakeGit) ResetHard(_ context.Context, _, _ string) error { return f.record("reset") }

func (f *fakeGit) CurrentBranch(_ context.Context, dir string) (string, error) {
	return f.branches[dir], nil
}

func (f *fakeGit) WorkTreeEmpty(_ context.Context, dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return true, nil
	}
	return len(entries) == 0, nil
}

func newService(t *testing.T, ghc *fakeGH, gitc *fakeGit) (*Service, config.Paths) {
	t.Helper()

	reviewCode := t.TempDir()
	localClone := t.TempDir()
	conf := "# a comment\n\nposthog/posthog  " + localClone + "\n"
	if err := os.WriteFile(filepath.Join(reviewCode, "repos.conf"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	gitc.repos[localClone] = true

	paths, err := config.NewPaths(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := paths.EnsureDirs(); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{ReviewCodeDir: reviewCode, DefaultEngine: "claude", GitHubUser: ghc.login}
	svc := &Service{
		Cfg:    cfg,
		Paths:  paths,
		Store:  index.New(paths.Index, paths.Lock),
		GH:     ghc,
		Git:    gitc,
		Cloner: clone.New(gitc, paths),
		Now:    func() time.Time { return start },
		NewID:  func() string { return "rec-1" },
	}
	return svc, paths
}

func prInfo() gh.PRInfo {
	info := gh.PRInfo{Number: 7, Title: "Add a thing", HeadRefName: "haacked/a-thing"}
	info.Author.Login = "someone"
	return info
}

func TestPrepareTier1MakesNoClone(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	gitc := newFakeGit()
	svc, paths := newService(t, ghc, gitc)

	rec, plan, err := svc.Prepare(context.Background(), listed, "claude")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	if plan.Tier != tier.Tier1 {
		t.Errorf("tier = %v, want tier1", plan.Tier)
	}
	if rec.Dir != paths.Scratch {
		t.Errorf("dir = %q, want the scratch directory %q", rec.Dir, paths.Scratch)
	}
	if strings.Contains(strings.Join(gitc.calls, ","), "fetch") {
		t.Errorf("a tier-1 review fetched something: %v", gitc.calls)
	}
	if !gitc.repos[paths.Scratch] {
		t.Error("the scratch directory is not a git repository")
	}
	if plan.Worktree == "" {
		t.Error("a tier-1 plan should name the worktree review-code will provision")
	}
}

func TestPrepareTier2ClonesTheHead(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	gitc := newFakeGit()
	svc, paths := newService(t, ghc, gitc)

	rec, plan, err := svc.Prepare(context.Background(), unlisted, "claude")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	if plan.Tier != tier.Tier2 {
		t.Errorf("tier = %v, want tier2", plan.Tier)
	}
	want := paths.CloneDir("haacked", "docket", 7)
	if rec.Dir != want {
		t.Errorf("dir = %q, want %q", rec.Dir, want)
	}
	if gitc.checkedOut != "haacked/a-thing" {
		t.Errorf("clone is on %q, want the head branch", gitc.checkedOut)
	}
}

func TestPrepareKeepsARecordWhenTheCloneFails(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	gitc := newFakeGit()
	gitc.failAt = "fetch"
	svc, _ := newService(t, ghc, gitc)

	if _, _, err := svc.Prepare(context.Background(), unlisted, "claude"); err == nil {
		t.Fatal("Prepare succeeded, want the fetch failure")
	}

	records, err := svc.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if records[0].State != review.StatePreparing {
		t.Errorf("state = %q, want preparing", records[0].State)
	}
	if records[0].Err == "" {
		t.Error("the record carries no error to retry from")
	}
}

func TestPrepareSnapshotsOnlySubmittedReviews(t *testing.T) {
	submitted := start.Add(-48 * time.Hour)
	ghc := &fakeGH{
		login: "haacked",
		info:  prInfo(),
		reviews: []review.GHReview{
			{ID: 1, User: review.GHUser{Login: "haacked"}, State: "APPROVED", SubmittedAt: &submitted},
			{ID: 2, User: review.GHUser{Login: "haacked"}, State: "PENDING"},
			{ID: 3, User: review.GHUser{Login: "someone"}, State: "APPROVED", SubmittedAt: &submitted},
		},
	}
	svc, _ := newService(t, ghc, newFakeGit())

	rec, _, err := svc.Prepare(context.Background(), unlisted, "claude")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	if len(rec.PriorReviewIDs) != 1 || rec.PriorReviewIDs[0] != 1 {
		t.Errorf("prior review ids = %v, want just the submitted one", rec.PriorReviewIDs)
	}
}

func TestLaunchSpecRecordsTheSessionBeforeReturning(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())

	rec, _, err := svc.Prepare(context.Background(), unlisted, "claude")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	launched, spec, err := svc.LaunchSpec(context.Background(), rec)
	if err != nil {
		t.Fatalf("LaunchSpec: %v", err)
	}

	if launched.State != review.StateReviewing {
		t.Errorf("state = %q, want reviewing", launched.State)
	}
	if !launched.StartedAt.Equal(start) {
		t.Errorf("started at %v, want %v", launched.StartedAt, start)
	}
	if spec.Path != "claude" || spec.Dir != rec.Dir {
		t.Errorf("spec = %s, want claude in %s", spec, rec.Dir)
	}
	line := spec.String()
	if !strings.Contains(line, "--session-id "+launched.SessionID) {
		t.Errorf("spec does not carry the session id: %s", line)
	}
	if !strings.Contains(line, "/review-code "+launched.URL+" --draft") {
		t.Errorf("spec does not run the review: %s", line)
	}

	records, err := svc.Records()
	if err != nil {
		t.Fatal(err)
	}
	if records[0].State != review.StateReviewing {
		t.Errorf("index state = %q, want reviewing", records[0].State)
	}
}

func TestLaunchSpecRefusesAMissingDirectory(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())

	rec, _, err := svc.Prepare(context.Background(), unlisted, "claude")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if err := os.RemoveAll(rec.Dir); err != nil {
		t.Fatal(err)
	}

	if _, _, err := svc.LaunchSpec(context.Background(), rec); err == nil {
		t.Error("LaunchSpec accepted a directory that is gone")
	}
}

func launched(t *testing.T, svc *Service, ref pr.Ref) review.Record {
	t.Helper()
	rec, _, err := svc.Prepare(context.Background(), ref, "claude")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	rec, _, err = svc.LaunchSpec(context.Background(), rec)
	if err != nil {
		t.Fatalf("LaunchSpec: %v", err)
	}
	return rec
}

func TestAfterExitArchivesAndDeletesTheCloneOnSubmission(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := launched(t, svc, unlisted)

	at := start.Add(3 * time.Minute)
	ghc.reviews = []review.GHReview{
		{ID: 9, User: review.GHUser{Login: "haacked"}, State: "APPROVED", SubmittedAt: &at},
	}

	done, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}

	if done.State != review.StateArchived {
		t.Fatalf("state = %q, want archived", done.State)
	}
	if done.ReviewID != 9 {
		t.Errorf("review id = %d, want 9", done.ReviewID)
	}
	if done.NotesPath == "" || !strings.HasSuffix(done.NotesPath, filepath.Join(".reviews", "haacked", "docket", "pr-7.md")) {
		t.Errorf("notes path = %q", done.NotesPath)
	}
	if _, err := os.Stat(rec.Dir); !os.IsNotExist(err) {
		t.Errorf("the clone at %s survived", rec.Dir)
	}
	if _, err := os.Stat(done.NotesPath); err == nil {
		t.Error("docket should not create the notes file itself")
	}
}

func TestAfterExitKeepsTheCloneWhenOnlyADraftExists(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := launched(t, svc, unlisted)

	ghc.reviews = []review.GHReview{
		{ID: 4, User: review.GHUser{Login: "haacked"}, State: "PENDING"},
	}

	done, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}

	if done.State != review.StateDrafted {
		t.Errorf("state = %q, want drafted", done.State)
	}
	if done.ReviewID != 4 {
		t.Errorf("review id = %d, want 4", done.ReviewID)
	}
	if _, err := os.Stat(rec.Dir); err != nil {
		t.Errorf("the clone is gone, but the review is still a draft: %v", err)
	}
}

func TestAfterExitReportsUnreviewedAndCleansNothing(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := launched(t, svc, unlisted)

	ghc.reviews = []review.GHReview{
		{ID: 5, User: review.GHUser{Login: "someone-else"}, State: "APPROVED", SubmittedAt: &start},
	}

	done, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}

	if done.State != review.StateUnreviewed {
		t.Errorf("state = %q, want unreviewed", done.State)
	}
	if _, err := os.Stat(rec.Dir); err != nil {
		t.Errorf("nothing should have been cleaned up: %v", err)
	}
}

func TestAfterExitIgnoresAnOlderReviewOfMine(t *testing.T) {
	old := start.Add(-72 * time.Hour)
	ghc := &fakeGH{
		login: "haacked",
		info:  prInfo(),
		reviews: []review.GHReview{
			{ID: 1, User: review.GHUser{Login: "haacked"}, State: "COMMENTED", SubmittedAt: &old},
		},
	}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := launched(t, svc, unlisted)

	done, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}

	if done.State != review.StateUnreviewed {
		t.Errorf("state = %q, want unreviewed", done.State)
	}
}

func TestAfterExitRunsEvenWhenTheChildFailed(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := launched(t, svc, unlisted)

	ghc.reviews = []review.GHReview{
		{ID: 6, User: review.GHUser{Login: "haacked"}, State: "PENDING"},
	}

	done, err := svc.AfterExit(context.Background(), rec, errors.New("exit status 130"))
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}

	if done.State != review.StateDrafted {
		t.Errorf("state = %q, want drafted even after an interrupt", done.State)
	}
	if done.Err == "" {
		t.Error("the child's failure is not recorded")
	}
}

func TestAbandonCleansUpAndKeepsTheRecord(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := launched(t, svc, unlisted)

	done, err := svc.Abandon(rec)
	if err != nil {
		t.Fatalf("Abandon: %v", err)
	}

	if done.State != review.StateAbandoned {
		t.Errorf("state = %q, want abandoned", done.State)
	}
	if _, err := os.Stat(rec.Dir); !os.IsNotExist(err) {
		t.Errorf("the clone at %s survived", rec.Dir)
	}

	records, err := svc.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].State != review.StateAbandoned {
		t.Errorf("records = %+v, want one abandoned record", records)
	}
}

func TestAbandonLeavesATier1WorktreeAlone(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, paths := newService(t, ghc, newFakeGit())
	rec := launched(t, svc, listed)

	if _, err := svc.Abandon(rec); err != nil {
		t.Fatalf("Abandon: %v", err)
	}
	if _, err := os.Stat(paths.Scratch); err != nil {
		t.Errorf("the scratch directory was deleted: %v", err)
	}
}

func TestReconcileRereadsRecordsLeftMidSession(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	launched(t, svc, unlisted)

	ghc.reviews = []review.GHReview{
		{ID: 8, User: review.GHUser{Login: "haacked"}, State: "PENDING"},
	}

	records, err := svc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if records[0].State != review.StateDrafted {
		t.Errorf("state = %q, want drafted", records[0].State)
	}
}

func TestLoginIsCachedInConfig(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, paths := newService(t, ghc, newFakeGit())
	svc.Cfg.GitHubUser = ""

	for range 3 {
		if _, err := svc.Login(context.Background()); err != nil {
			t.Fatalf("Login: %v", err)
		}
	}

	if ghc.logins != 1 {
		t.Errorf("asked GitHub for the login %d times, want 1", ghc.logins)
	}
	cfg, err := config.Load(paths.Config)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitHubUser != "haacked" {
		t.Errorf("cached login = %q, want haacked", cfg.GitHubUser)
	}
}

func TestExplainResumeReportsWithoutRecording(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := launched(t, svc, unlisted)

	before, err := svc.Records()
	if err != nil {
		t.Fatal(err)
	}

	spec, err := svc.ExplainResume(rec)
	if err != nil {
		t.Fatalf("ExplainResume: %v", err)
	}
	if !strings.Contains(spec.String(), "--resume "+rec.SessionID) {
		t.Errorf("spec = %s, want it to resume the stored session", spec)
	}

	after, err := svc.Records()
	if err != nil {
		t.Fatal(err)
	}
	if after[0].State != before[0].State || !after[0].StartedAt.Equal(before[0].StartedAt) {
		t.Error("ExplainResume changed the record")
	}
}

func TestExplainResumeFallsBackToAFreshReview(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := launched(t, svc, unlisted)
	rec.SessionID = ""

	spec, err := svc.ExplainResume(rec)
	if err != nil {
		t.Fatalf("ExplainResume: %v", err)
	}
	if !strings.Contains(spec.String(), "/review-code "+rec.URL+" --draft") {
		t.Errorf("spec = %s, want a fresh review", spec)
	}
}

func TestRefreshAllReReadsEveryRecordWhoseSessionIsOver(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())

	ids := []string{"rec-1", "rec-2"}
	next := 0
	svc.NewID = func() string {
		id := ids[next]
		next++
		return id
	}
	first := launched(t, svc, unlisted)
	second := launched(t, svc, listed)

	ghc.reviews = []review.GHReview{
		{ID: 11, User: review.GHUser{Login: "haacked"}, State: "PENDING"},
	}
	if _, err := svc.AfterExit(context.Background(), first, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AfterExit(context.Background(), second, nil); err != nil {
		t.Fatal(err)
	}

	records, err := svc.RefreshAll(context.Background())
	if err != nil {
		t.Fatalf("RefreshAll: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %d, want 2", len(records))
	}
	for _, rec := range records {
		if rec.State != review.StateDrafted {
			t.Errorf("%s state = %q, want drafted", rec.ID, rec.State)
		}
	}
}

func TestReconcileLeavesSettledRecordsAlone(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := launched(t, svc, unlisted)

	ghc.reviews = []review.GHReview{{ID: 12, User: review.GHUser{Login: "haacked"}, State: "PENDING"}}
	if _, err := svc.AfterExit(context.Background(), rec, nil); err != nil {
		t.Fatal(err)
	}

	ghc.reviews = nil
	records, err := svc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if records[0].State != review.StateDrafted {
		t.Errorf("state = %q, want the drafted record left as it was", records[0].State)
	}
}
