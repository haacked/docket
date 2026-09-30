package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/git"
	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/core/tier"
)

// botHead is the head branch of the bot's pull request, which lives in the
// base repository.
const botHead = "posthog/fix-thing"

// fixService is a service whose pull request the PostHog bot opened from a
// branch of the base repository, with the bot in fix_authors. gh pr view names
// a GitHub App's pull requests app/<name>.
func fixService(t *testing.T) (*Service, *fakeGH, *fakeGit) {
	t.Helper()
	info := prInfo()
	info.HeadRefName = botHead
	info.HeadRefOid = "base-sha"
	info.Author.Login = "app/posthog"
	ghc := &fakeGH{login: "haacked", info: info}
	gitc := newFakeGit()
	svc, _ := newService(t, ghc, gitc)
	svc.Cfg.FixAuthors = []string{"app/posthog"}
	return svc, ghc, gitc
}

func fixReviewPrepared(t *testing.T, svc *Service, ref pr.Ref, mode review.Mode) (review.Record, Plan) {
	t.Helper()
	rec, plan, err := svc.Prepare(context.Background(), ref, "claude", mode, review.IntentReview, review.FixOn)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if !rec.Fix {
		t.Fatalf("Prepare with %q recorded no fix review: %+v", review.FixOn, rec)
	}
	return rec, plan
}

// fixReviewLaunched is a fix review running in the terminal.
func fixReviewLaunched(t *testing.T, svc *Service, ref pr.Ref) review.Record {
	t.Helper()
	rec, _ := fixReviewPrepared(t, svc, ref, review.ModeInteractive)
	rec, _, err := svc.LaunchSpec(context.Background(), rec)
	if err != nil {
		t.Fatalf("LaunchSpec: %v", err)
	}
	return rec
}

// fixReviewPushed is a fix review whose session wrote its notes and ended with
// nothing local, so the row is ready to approve.
func fixReviewPushed(t *testing.T, svc *Service, ref pr.Ref) review.Record {
	t.Helper()
	rec := fixReviewLaunched(t, svc, ref)
	writeFixSummary(t, rec)
	rec, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}
	if rec.State != review.StatePushed {
		t.Fatalf("state = %q, want pushed", rec.State)
	}
	return rec
}

// fixReviewInBackground is a tier-2 fix review running in the background, with
// claude listing its session under listing.
func fixReviewInBackground(t *testing.T, svc *Service, listing string) review.Record {
	t.Helper()
	svc.Runner = bgRunner(listing)
	rec, _ := fixReviewPrepared(t, svc, unlisted, review.ModeBackground)
	rec, err := svc.StartBackground(context.Background(), rec)
	if err != nil {
		t.Fatalf("StartBackground: %v", err)
	}
	return rec
}

// writeFixSummary writes the notes review-code composes after its fix pass,
// modified a minute into the session.
func writeFixSummary(t *testing.T, rec review.Record) {
	t.Helper()
	writeNotes(t, rec.NotesPath, "# Review\n\n## Fix Summary\n\nFixed the retry loop. Skipped the rename.\n")
	at := rec.StartedAt.Add(time.Minute)
	if err := os.Chtimes(rec.NotesPath, at, at); err != nil {
		t.Fatal(err)
	}
}

func called(gitc *fakeGit, prefix string) []string {
	var out []string
	for _, c := range gitc.calls {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func launchLine(t *testing.T, spec exec.CommandSpec) string {
	t.Helper()
	line := spec.String()
	if !strings.Contains(line, "/review-code ") {
		t.Fatalf("command %s runs no review", line)
	}
	return line
}

// --- Choosing fix mode ---

// Explain reads what a review would do and provisions nothing, so it shows the
// decision without a clone.
func TestTheFixChoiceAndTheAuthorListDecideWhetherAReviewFixes(t *testing.T) {
	tests := []struct {
		name    string
		author  string
		choice  review.FixChoice
		wantFix bool
	}{
		{name: "auto with the author listed", author: "app/posthog", choice: review.FixAuto, wantFix: true},
		{name: "auto with the search spelling of a listed author", author: "posthog[bot]", choice: review.FixAuto, wantFix: true},
		{name: "auto with an author not listed", author: "someone", choice: review.FixAuto, wantFix: false},
		{name: "fix with an author not listed", author: "someone", choice: review.FixOn, wantFix: true},
		{name: "draft with the author listed", author: "app/posthog", choice: review.FixOff, wantFix: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, ghc, _ := fixService(t)
			ghc.info.Author.Login = tt.author

			_, spec, err := svc.Explain(context.Background(), unlisted, "claude", review.ModeInteractive, review.IntentReview, tt.choice)
			if err != nil {
				t.Fatalf("Explain: %v", err)
			}

			line := launchLine(t, spec)
			if got := strings.Contains(line, " --fix"); got != tt.wantFix {
				t.Errorf("command %s carries --fix = %v, want %v", line, got, tt.wantFix)
			}
			if got := strings.Contains(line, " --draft"); got == tt.wantFix {
				t.Errorf("command %s carries --draft = %v, want %v", line, got, !tt.wantFix)
			}
		})
	}
}

func TestPrepareRecordsTheFixDecision(t *testing.T) {
	svc, _, _ := fixService(t)

	rec, _, err := svc.Prepare(context.Background(), unlisted, "claude", review.ModeInteractive, review.IntentReview, review.FixAuto)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	if !rec.Fix {
		t.Error("a review of a listed author's pull request under auto is not a fix review")
	}
	if got := storedByID(t, svc, rec.ID); !got.Fix {
		t.Error("the stored record lost the fix flag")
	}
}

// An ask adopts a review that is already there and runs no review, so there is
// nothing to fix.
func TestAnAskNeverFixes(t *testing.T) {
	svc, _, _ := fixService(t)
	notesFor(t, svc, unlisted)

	rec, _, err := svc.Prepare(context.Background(), unlisted, "claude", review.ModeInteractive, review.IntentAsk, review.FixOn)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	if rec.Fix {
		t.Error("an ask was recorded as a fix review")
	}
}

// A fork's head branch cannot be pushed to by name from the base repository.
// Asking for a fix explicitly is refused, before anything is recorded.
func TestAFixOfAForkPullRequestIsRefused(t *testing.T) {
	svc, ghc, gitc := fixService(t)
	ghc.info.IsCrossRepository = true

	_, _, err := svc.Prepare(context.Background(), unlisted, "claude", review.ModeInteractive, review.IntentReview, review.FixOn)

	if err == nil {
		t.Fatal("Prepare accepted a fix of a pull request from a fork")
	}
	if records := stored(t, svc); len(records) != 0 {
		t.Errorf("records = %+v, want none for a refused fix", records)
	}
	if len(gitc.calls) != 0 {
		t.Errorf("git ran %v for a refused fix", gitc.calls)
	}
}

// Under auto the author list only suggests a fix, so a fork falls back to a
// draft review rather than refusing.
func TestAutoDraftsAForkPullRequestByAListedAuthor(t *testing.T) {
	svc, ghc, _ := fixService(t)
	ghc.info.IsCrossRepository = true

	_, spec, err := svc.Explain(context.Background(), unlisted, "claude", review.ModeInteractive, review.IntentReview, review.FixAuto)
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if line := launchLine(t, spec); !strings.Contains(line, "--draft") || strings.Contains(line, "--fix") {
		t.Errorf("command %s, want a draft review", line)
	}

	rec, _, err := svc.Prepare(context.Background(), unlisted, "claude", review.ModeInteractive, review.IntentReview, review.FixAuto)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if rec.Fix {
		t.Error("a pull request from a fork was recorded as a fix review")
	}
}

// --- Where the fix runs ---

// review-code's own tier-1 worktree is detached, and its fix pass refuses to
// edit one. docket adds a worktree of the user's clone on the head branch.
func TestAFixOfAReposConfRepoAddsAWorktreeOnTheHeadBranch(t *testing.T) {
	svc, _, gitc := fixService(t)

	rec, plan := fixReviewPrepared(t, svc, listed, review.ModeInteractive)

	if plan.Tier != tier.Tier3 || rec.Tier != tier.Tier3 {
		t.Errorf("tier = %v on the plan and %v on the record, want tier3", plan.Tier, rec.Tier)
	}
	if want := svc.Paths.FixWorktreeDir(listed.Org, listed.Repo, listed.Number); rec.Dir != want {
		t.Errorf("dir = %q, want %q", rec.Dir, want)
	}
	if rec.Branch != botHead {
		t.Errorf("branch = %q, want %q", rec.Branch, botHead)
	}
	if rec.Remote != "origin" {
		t.Errorf("remote = %q, want the remote that points at PostHog/posthog", rec.Remote)
	}
	if rec.WorktreeOf != plan.LocalClone || rec.WorktreeOf == "" {
		t.Errorf("worktree of = %q, want the repos.conf clone %q", rec.WorktreeOf, plan.LocalClone)
	}
	if rec.FixBase != "base-sha" {
		t.Errorf("fix base = %q, want the commit the worktree started on", rec.FixBase)
	}
	if gitc.branches[rec.Dir] != botHead {
		t.Errorf("the worktree is on %q, want %q", gitc.branches[rec.Dir], botHead)
	}
	if got := called(gitc, "worktree-add "+plan.LocalClone+" "+rec.Dir+" "+botHead); len(got) != 1 {
		t.Errorf("git calls = %v, want one worktree add of the clone", gitc.calls)
	}
	if !strings.Contains(plan.Description(), rec.Dir) {
		t.Errorf("plan = %q, want it to name the worktree", plan.Description())
	}
	if got := storedByID(t, svc, rec.ID); got.Dir != rec.Dir || got.WorktreeOf != rec.WorktreeOf || got.FixBase != rec.FixBase || got.Remote != rec.Remote {
		t.Errorf("stored %+v, want the checkout fields of %+v", got, rec)
	}
}

// git checks a branch out in one worktree only. A clone that already has the
// head branch, from a worktree the user made by hand for example, gets a
// tier-2 clone instead, and the plan says why.
func TestAFixOfAReposConfRepoWhoseCloneHasTheBranchClonesInstead(t *testing.T) {
	svc, _, gitc := fixService(t)
	planned, _, err := svc.Explain(context.Background(), listed, "claude", review.ModeInteractive, review.IntentReview, review.FixOn)
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	gitc.localBranches[planned.LocalClone+" "+botHead] = true

	rec, plan := fixReviewPrepared(t, svc, listed, review.ModeInteractive)

	if plan.Tier != tier.Tier2 || rec.Tier != tier.Tier2 {
		t.Errorf("tier = %v on the plan and %v on the record, want tier2", plan.Tier, rec.Tier)
	}
	if want := svc.Paths.CloneDir(listed.Org, listed.Repo, listed.Number); rec.Dir != want {
		t.Errorf("dir = %q, want the clone at %q", rec.Dir, want)
	}
	if got := called(gitc, "worktree-add"); len(got) != 0 {
		t.Errorf("git calls = %v, want no worktree added", got)
	}
	if desc := plan.Description(); !strings.Contains(desc, botHead) {
		t.Errorf("plan = %q, want it to say the clone already has %s", desc, botHead)
	}
}

// A push from the tier-2 clone runs in the user's session with no arguments,
// so the head branch needs an upstream, and the clone needs the gh credential
// helper docket's own fetches use.
func TestAFixOfAnUnlistedRepoClonesAndTracksTheHeadBranch(t *testing.T) {
	svc, _, gitc := fixService(t)

	rec, plan := fixReviewPrepared(t, svc, unlisted, review.ModeInteractive)

	if plan.Tier != tier.Tier2 || rec.Tier != tier.Tier2 {
		t.Errorf("tier = %v on the plan and %v on the record, want tier2", plan.Tier, rec.Tier)
	}
	if rec.Remote != "origin" || rec.Branch != botHead {
		t.Errorf("remote %q and branch %q, want origin and %q", rec.Remote, rec.Branch, botHead)
	}
	if rec.FixBase != "base-sha" {
		t.Errorf("fix base = %q, want the commit the clone started on", rec.FixBase)
	}
	for _, want := range []string{
		"fetch-branch " + rec.Dir + " origin/" + botHead,
		"upstream " + rec.Dir + " origin/" + botHead,
		"credential " + rec.Dir,
	} {
		if !slices.Contains(gitc.calls, want) {
			t.Errorf("git calls = %v, want %q", gitc.calls, want)
		}
	}
}

func TestADraftReviewAddsNoWorktreeAndNoUpstream(t *testing.T) {
	svc, ghc, gitc := fixService(t)
	ghc.info.Author.Login = "someone"

	rec, plan, err := svc.Prepare(context.Background(), listed, "claude", review.ModeInteractive, review.IntentReview, review.FixAuto)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	if rec.Fix || plan.Tier != tier.Tier1 {
		t.Errorf("fix %v on %v, want a tier-1 draft review", rec.Fix, plan.Tier)
	}
	for _, prefix := range []string{"worktree-add", "fetch-branch", "upstream", "credential"} {
		if got := called(gitc, prefix); len(got) != 0 {
			t.Errorf("a draft review ran %v", got)
		}
	}
}

// Cloner.Ensure resets a clone it reuses, which would throw away fixes left in
// a checkout nobody pushed.
func TestAFixRefusesAnExistingCloneThatHoldsLocalWork(t *testing.T) {
	svc, _, gitc := fixService(t)
	dir := svc.Paths.CloneDir(unlisted.Org, unlisted.Repo, unlisted.Number)
	keep := filepath.Join(dir, "fixed.go")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keep, []byte("package fixed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitc.repos[dir] = true
	gitc.branches[dir] = botHead
	gitc.dirty[dir] = true

	_, _, err := svc.Prepare(context.Background(), unlisted, "claude", review.ModeInteractive, review.IntentReview, review.FixOn)

	if err == nil || !strings.Contains(err.Error(), dir) {
		t.Fatalf("Prepare answered %v, want a refusal that names %s", err, dir)
	}
	if got := called(gitc, "reset"); len(got) != 0 {
		t.Errorf("git calls = %v, want no reset of a clone that holds fixes", gitc.calls)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("the local work at %s is gone: %v", keep, err)
	}
}

// A worktree directory left from an earlier fix review may hold fixes too.
func TestAFixRefusesAnExistingWorktreeThatHoldsLocalWork(t *testing.T) {
	svc, _, gitc := fixService(t)
	dir := svc.Paths.FixWorktreeDir(listed.Org, listed.Repo, listed.Number)
	keep := filepath.Join(dir, "fixed.go")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keep, []byte("package fixed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitc.repos[dir] = true
	gitc.dirty[dir] = true

	_, _, err := svc.Prepare(context.Background(), listed, "claude", review.ModeInteractive, review.IntentReview, review.FixOn)

	if err == nil {
		t.Fatal("Prepare provisioned over a worktree that holds fixes")
	}
	if got := called(gitc, "worktree-add"); len(got) != 0 {
		t.Errorf("git calls = %v, want no worktree added over the old one", got)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("the local work at %s is gone: %v", keep, err)
	}
}

// --- Detecting the outcome ---

// A fix review posts nothing, so the checkout says where it stands.
func TestAFixSessionThatLeftUncommittedChangesIsFixed(t *testing.T) {
	svc, _, gitc := fixService(t)
	rec := fixReviewLaunched(t, svc, unlisted)
	gitc.dirty[rec.Dir] = true

	done, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}

	if done.State != review.StateFixed {
		t.Errorf("state = %q, want fixed", done.State)
	}
	if got := storedByID(t, svc, rec.ID); got.State != review.StateFixed {
		t.Errorf("stored state = %q, want fixed", got.State)
	}
	if _, err := os.Stat(rec.Dir); err != nil {
		t.Errorf("the checkout that holds the fixes is gone: %v", err)
	}
}

// The user commits in the session but has not pushed yet.
func TestAFixSessionWithCommitsTheRemoteDoesNotHaveIsFixed(t *testing.T) {
	svc, _, gitc := fixService(t)
	rec := fixReviewLaunched(t, svc, unlisted)
	gitc.heads[rec.Dir] = "fix-sha"
	gitc.ahead[rec.Dir] = 1

	done, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}

	if done.State != review.StateFixed {
		t.Errorf("state = %q, want fixed", done.State)
	}
	if done.FixHead != "fix-sha" {
		t.Errorf("fix head = %q, want the commit the checkout is on", done.FixHead)
	}
	if got := called(gitc, "ahead "+rec.Dir+" origin/"+botHead); len(got) == 0 {
		t.Errorf("git calls = %v, want the count against origin/%s", gitc.calls, botHead)
	}
}

// A clone whose origin is the user's fork reaches the pull request's
// repository through another remote. Tier 2 always names origin, so only a
// tier-3 review shows that the remote Ensure found reaches the count.
func TestATier3FixReviewCountsAgainstTheRemoteForThePullRequestsRepository(t *testing.T) {
	svc, _, gitc := fixService(t)
	gitc.remotes = []git.Remote{
		{Name: "origin", URL: "git@github.com:haacked/posthog.git"},
		{Name: "upstream", URL: "git@github.com:PostHog/posthog.git"},
	}
	rec := fixReviewLaunched(t, svc, listed)
	if rec.Remote != "upstream" {
		t.Fatalf("remote = %q, want upstream", rec.Remote)
	}
	gitc.heads[rec.Dir] = "fix-sha"
	gitc.ahead[rec.Dir] = 1

	done, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}

	if done.State != review.StateFixed {
		t.Errorf("state = %q, want fixed", done.State)
	}
	if got := called(gitc, "ahead "+rec.Dir+" upstream/"+botHead); len(got) == 0 {
		t.Errorf("git calls = %v, want the count against upstream/%s", gitc.calls, botHead)
	}
}

// The user pushed from the session, but GitHub still reports the old head, so
// docket fetches and finds nothing the remote lacks. The row is ready to approve.
func TestAFixSessionWhoseCommitsArePushedIsReadyToApprove(t *testing.T) {
	svc, _, gitc := fixService(t)
	rec := fixReviewLaunched(t, svc, unlisted)
	gitc.heads[rec.Dir] = "pushed-sha"

	done, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}

	if done.State != review.StatePushed {
		t.Errorf("state = %q, want pushed", done.State)
	}
	if done.FixHead != "pushed-sha" {
		t.Errorf("fix head = %q, want pushed-sha", done.FixHead)
	}
	if done.FixBase != "base-sha" {
		t.Errorf("fix base = %q, want the commit the review started on", done.FixBase)
	}
	if got := called(gitc, "fetch-branch "+rec.Dir); len(got) < 2 {
		t.Errorf("git calls = %v, want a fetch before the checkout was read", gitc.calls)
	}
}

// review-code found nothing it could fix cleanly. The row is still ready to
// approve, and the dashboard tags it as having no changes.
func TestAFixSessionThatChangedNothingIsReadyToApproveWithHeadAtBase(t *testing.T) {
	svc, _, _ := fixService(t)
	rec := fixReviewLaunched(t, svc, unlisted)
	writeFixSummary(t, rec)

	done, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}

	if done.State != review.StatePushed {
		t.Errorf("state = %q, want pushed", done.State)
	}
	if done.FixHead != done.FixBase || done.FixHead == "" {
		t.Errorf("fix head %q and base %q, want both at the starting commit", done.FixHead, done.FixBase)
	}
}

// A session the user left before review-code wrote its notes did not finish,
// so its clean checkout says nothing about the review. The row reads as a
// session that posted nothing, and enter resumes it.
func TestAFixSessionThatEndedBeforeItsNotesIsUnreviewed(t *testing.T) {
	svc, _, _ := fixService(t)
	rec := fixReviewLaunched(t, svc, unlisted)

	done, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}

	if done.State != review.StateUnreviewed {
		t.Errorf("state = %q, want unreviewed", done.State)
	}
}

// A session that committed its fixes finished them, even if it ended before
// the notes were written, so the row reads the checkout as usual.
func TestAFixSessionThatMovedTheHeadIsReadWithoutItsNotes(t *testing.T) {
	svc, _, gitc := fixService(t)
	rec := fixReviewLaunched(t, svc, unlisted)
	gitc.heads[rec.Dir] = "fixed-sha"

	done, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}

	if done.State != review.StatePushed || done.FixHead != "fixed-sha" {
		t.Errorf("state %q at %q, want pushed at fixed-sha", done.State, done.FixHead)
	}
}

// enter on a finished row resumes its session, and the resume stamps a new
// StartedAt. The notes review-code wrote are older than that, and they still
// say the fix pass finished.
func TestResumingAFinishedFixRowAndLeavingKeepsItReadyToApprove(t *testing.T) {
	svc, _, _ := fixService(t)
	rec := fixReviewPushed(t, svc, unlisted)
	svc.Now = func() time.Time { return start.Add(2 * time.Hour) }

	resumed, _, err := svc.ResumeSpec(context.Background(), rec)
	if err != nil {
		t.Fatalf("ResumeSpec: %v", err)
	}
	done, err := svc.AfterExit(context.Background(), resumed, nil)
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}

	if done.State != review.StatePushed {
		t.Errorf("state = %q, want pushed", done.State)
	}
}

// A bot that rebases the head branch moves the remote-tracking ref off the
// commits the checkout started from. Those commits came from GitHub, so a
// checkout nobody touched still holds nothing local.
func TestAForcePushDoesNotMakeAnUntouchedCheckoutReadAsFixed(t *testing.T) {
	svc, ghc, gitc := fixService(t)
	rec := fixReviewLaunched(t, svc, unlisted)
	writeFixSummary(t, rec)
	ghc.info.HeadRefOid = "rebased-sha"
	gitc.ahead[rec.Dir] = 1

	done, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}

	if done.State != review.StatePushed {
		t.Errorf("state = %q, want pushed with no changes", done.State)
	}
	if _, err := svc.Abandon(context.Background(), done); err != nil {
		t.Errorf("Abandon refused the untouched checkout: %v", err)
	}
}

// Worktrees share the user's clone's refs, and pruning there after a merge
// removes the tracking ref of the deleted branch. A pushed row's checkout is
// still on a commit docket saw on GitHub, so it needs no count.
func TestAPushedRowWhoseTrackingRefIsGoneStillArchives(t *testing.T) {
	svc, ghc, gitc := fixService(t)
	rec := fixReviewLaunched(t, svc, listed)
	gitc.heads[rec.Dir] = "pushed-sha"
	ghc.info.HeadRefOid = "pushed-sha"
	rec, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil || rec.State != review.StatePushed {
		t.Fatalf("AfterExit = %q, %v, want pushed", rec.State, err)
	}
	ghc.info.State = review.PRMerged
	gitc.failAt = "ahead " + rec.Dir + " " + rec.Upstream()

	done, err := svc.Refresh(context.Background(), rec)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	if done.State != review.StateArchived {
		t.Errorf("state = %q, want archived", done.State)
	}
	if _, err := os.Stat(rec.Dir); !os.IsNotExist(err) {
		t.Errorf("the worktree at %s survived", rec.Dir)
	}
}

// provision records the remote last. A fix review whose provisioning failed
// after the clone was made has none, and no session ran in the clone.
func TestAbandonDeletesAFixCloneWhoseProvisioningFailed(t *testing.T) {
	svc, _, gitc := fixService(t)
	dir := svc.Cloner.Dir(unlisted)
	gitc.failAt = "upstream " + dir + " origin/" + botHead

	rec, _, err := svc.Prepare(context.Background(), unlisted, "claude", review.ModeInteractive, review.IntentReview, review.FixOn)
	if err == nil {
		t.Fatal("Prepare succeeded, want the failed tracking setup")
	}
	if rec.Dir != dir || rec.Remote != "" {
		t.Fatalf("dir %q with remote %q, want %s with none", rec.Dir, rec.Remote, dir)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("the clone at %s was not made: %v", dir, err)
	}
	// git cannot count against the upstream of a checkout with no remote.
	gitc.failAt = "ahead " + dir + " " + rec.Upstream()

	done, err := svc.Abandon(context.Background(), rec)
	if err != nil {
		t.Fatalf("Abandon: %v", err)
	}

	if done.State != review.StateAbandoned {
		t.Errorf("state = %q, want abandoned", done.State)
	}
	if _, err := os.Stat(rec.Dir); !os.IsNotExist(err) {
		t.Errorf("the clone at %s survived", rec.Dir)
	}
}

// Decide reads any pending review of mine as a draft. A fix review posted none,
// so a draft left from an earlier review says nothing about this one.
func TestAPendingReviewFromBeforeDoesNotMakeAFixRowADraft(t *testing.T) {
	svc, ghc, gitc := fixService(t)
	ghc.reviews = []review.GHReview{myPending()}
	rec := fixReviewLaunched(t, svc, unlisted)
	gitc.dirty[rec.Dir] = true

	done, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}

	if done.State != review.StateFixed {
		t.Errorf("state = %q, want fixed", done.State)
	}
	if done.ReviewID != 0 {
		t.Errorf("review id = %d, want none: a fix row has no draft of its own", done.ReviewID)
	}
}

// The user can still submit a review of their own from the session. That
// closes the row the way it closes any other.
func TestMyOwnSubmissionAfterALaunchClosesAFixRow(t *testing.T) {
	svc, ghc, _ := fixService(t)
	rec := fixReviewLaunched(t, svc, unlisted)
	at := start.Add(3 * time.Minute)
	ghc.reviews = []review.GHReview{{ID: 9, User: review.GHUser{Login: "haacked"}, State: "APPROVED", SubmittedAt: &at}}

	done, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}

	if done.State != review.StateArchived {
		t.Errorf("state = %q, want archived", done.State)
	}
	if _, err := os.Stat(rec.Dir); !os.IsNotExist(err) {
		t.Errorf("the clean checkout at %s survived the archive", rec.Dir)
	}
}

// A submission does not push the fixes. The row stays open with the reason, so
// the user can push them or throw them away.
func TestMyOwnSubmissionKeepsACheckoutThatHoldsFixes(t *testing.T) {
	svc, ghc, gitc := fixService(t)
	rec := fixReviewLaunched(t, svc, unlisted)
	gitc.dirty[rec.Dir] = true
	at := start.Add(3 * time.Minute)
	ghc.reviews = []review.GHReview{{ID: 9, User: review.GHUser{Login: "haacked"}, State: "APPROVED", SubmittedAt: &at}}

	done, _ := svc.AfterExit(context.Background(), rec, nil)

	if done.State == review.StateArchived {
		t.Error("the row archived with fixes still in its checkout")
	}
	if !strings.Contains(done.Err, rec.Dir) {
		t.Errorf("err = %q, want it to name %s", done.Err, rec.Dir)
	}
	if _, err := os.Stat(rec.Dir); err != nil {
		t.Errorf("the checkout that holds the fixes is gone: %v", err)
	}
}

// PostHog deletes a head branch when its pull request merges, so the fetch
// fails from then on. The decision falls back to the ref the last fetch left,
// and the failure goes on the row.
func TestAFailedFetchFallsBackToTheTrackingRef(t *testing.T) {
	svc, _, gitc := fixService(t)
	rec := fixReviewLaunched(t, svc, unlisted)
	writeFixSummary(t, rec)
	gitc.heads[rec.Dir] = "fix-sha"
	gitc.fetchErr = errors.New("couldn't find remote ref refs/heads/" + botHead)

	done, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}

	if done.State != review.StatePushed {
		t.Errorf("state = %q, want pushed read off the tracking ref", done.State)
	}
	if !strings.Contains(done.Err, "couldn't find remote ref") {
		t.Errorf("err = %q, want the fetch's failure on the row", done.Err)
	}
	if got := called(gitc, "ahead "+rec.Dir+" origin/"+botHead); len(got) == 0 {
		t.Errorf("git calls = %v, want the count against the tracking ref", gitc.calls)
	}
}

// Without the fallback, a merged pushed row could never archive.
func TestAPushedRowOnAMergedPullRequestWithADeletedBranchArchives(t *testing.T) {
	svc, ghc, gitc := fixService(t)
	rec := fixReviewPushed(t, svc, unlisted)
	ghc.info.State = review.PRMerged
	gitc.fetchErr = errors.New("couldn't find remote ref refs/heads/" + botHead)

	done, err := svc.Refresh(context.Background(), rec)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	if done.State != review.StateArchived {
		t.Errorf("state = %q, want archived", done.State)
	}
	if _, err := os.Stat(rec.Dir); !os.IsNotExist(err) {
		t.Errorf("the clean checkout at %s survived", rec.Dir)
	}
}

// Fixes nobody pushed are the user's to push or drop, merged or not.
func TestAFixedRowOnAMergedPullRequestStaysOpen(t *testing.T) {
	svc, ghc, gitc := fixService(t)
	rec := fixReviewLaunched(t, svc, unlisted)
	gitc.dirty[rec.Dir] = true
	ghc.info.State = review.PRMerged

	done, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}

	if done.State != review.StateFixed {
		t.Errorf("state = %q, want fixed", done.State)
	}
	if done.PRState != review.PRMerged {
		t.Errorf("pr state = %q, want merged so the row shows the tag", done.PRState)
	}
	if _, err := os.Stat(rec.Dir); err != nil {
		t.Errorf("the checkout that holds the fixes is gone: %v", err)
	}
}

// A clean checkout on the head GitHub reports has nothing local, so docket
// reads it without fetching. A merged pull request's deleted branch then costs
// no failing fetch either.
func TestACleanCheckoutOnGitHubsHeadIsReadWithoutAFetch(t *testing.T) {
	svc, _, gitc := fixService(t)
	rec := fixReviewLaunched(t, svc, unlisted)
	writeFixSummary(t, rec)
	gitc.fetchErr = errors.New("couldn't find remote ref refs/heads/" + botHead)
	before := len(called(gitc, "fetch-branch "))

	done, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}

	if done.State != review.StatePushed || done.Err != "" {
		t.Errorf("state %q with err %q, want pushed with no error", done.State, done.Err)
	}
	if got := called(gitc, "fetch-branch "); len(got) != before {
		t.Errorf("fetches = %v, want none after the launch", got[before:])
	}
}

// The user opens a fixed row's session, pushes, and leaves. r reads the
// checkout again.
func TestRefreshMovesAFixedRowToPushedOnceTheFixesArePushed(t *testing.T) {
	svc, _, gitc := fixService(t)
	rec := fixReviewLaunched(t, svc, unlisted)
	gitc.heads[rec.Dir] = "fix-sha"
	gitc.ahead[rec.Dir] = 1
	fixed, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil || fixed.State != review.StateFixed {
		t.Fatalf("AfterExit = %q, %v, want fixed", fixed.State, err)
	}
	gitc.ahead[rec.Dir] = 0
	gitc.heads[rec.Dir] = "pushed-sha"

	done, err := svc.Refresh(context.Background(), fixed)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	if done.State != review.StatePushed || done.FixHead != "pushed-sha" {
		t.Errorf("state %q at %q, want pushed at pushed-sha", done.State, done.FixHead)
	}
}

// c on a fixed row opens an ask session, and the user may have the agent push
// from it. Leaving the session reads the checkout again, with no r.
func TestLeavingAnAskSessionAfterPushingReadsTheCheckout(t *testing.T) {
	svc, _, gitc := fixService(t)
	rec := fixReviewLaunched(t, svc, unlisted)
	writeFixSummary(t, rec)
	gitc.dirty[rec.Dir] = true
	fixed, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil || fixed.State != review.StateFixed {
		t.Fatalf("AfterExit = %q, %v, want fixed", fixed.State, err)
	}
	gitc.dirty[rec.Dir] = false
	gitc.heads[rec.Dir] = "pushed-sha"

	asked, _, err := svc.AskSpec(fixed)
	if err != nil {
		t.Fatalf("AskSpec: %v", err)
	}
	done, err := svc.AfterAsk(context.Background(), asked, nil)
	if err != nil {
		t.Fatalf("AfterAsk: %v", err)
	}

	if done.State != review.StatePushed || done.FixHead != "pushed-sha" {
		t.Errorf("state %q at %q, want pushed at pushed-sha", done.State, done.FixHead)
	}
}

func TestRefreshAllReadsTheCheckoutOfEveryFixRow(t *testing.T) {
	svc, _, gitc := fixService(t)
	rec := fixReviewLaunched(t, svc, unlisted)
	writeFixSummary(t, rec)
	gitc.dirty[rec.Dir] = true
	if _, err := svc.AfterExit(context.Background(), rec, nil); err != nil {
		t.Fatalf("AfterExit: %v", err)
	}
	gitc.dirty[rec.Dir] = false

	records, err := svc.RefreshAll(context.Background())
	if err != nil {
		t.Fatalf("RefreshAll: %v", err)
	}

	if len(records) != 1 || records[0].State != review.StatePushed {
		t.Errorf("records = %+v, want the one row pushed", records)
	}
}

// A docket that died during a terminal fix review reads the checkout on its
// next start, the way it reads GitHub for a draft review.
func TestReconcileReadsTheCheckoutOfAFixSessionLeftRunning(t *testing.T) {
	svc, _, gitc := fixService(t)
	rec := fixReviewLaunched(t, svc, unlisted)
	gitc.dirty[rec.Dir] = true

	records, err := svc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if len(records) != 1 || records[0].State != review.StateFixed {
		t.Errorf("records = %+v, want the one row fixed", records)
	}
}

// --- A background fix session ---

// review-code writes the notes at its compose step, after the fix pass. A
// session that ended its turn before that stopped partway, at a permission
// prompt for example, and is still running.
func TestAPollKeepsAnIdleFixSessionRunningUntilItWritesTheNotes(t *testing.T) {
	svc, _, gitc := fixService(t)
	rec := fixReviewInBackground(t, svc, idleListing("6d681a76", bgSession))
	gitc.dirty[rec.Dir] = true

	records, statuses, err := svc.PollBackground(context.Background())
	if err != nil {
		t.Fatalf("PollBackground: %v", err)
	}

	if records[0].State != review.StateReviewing {
		t.Errorf("state = %q, want the fix session still running", records[0].State)
	}
	if _, polled := statuses[rec.ID]; !polled {
		t.Error("the fix session is no longer watched")
	}
}

func TestAPollMovesAnIdleFixSessionOnOnceItWritesTheFixSummary(t *testing.T) {
	tests := []struct {
		name  string
		dirty bool
		want  review.State
	}{
		{name: "fixes left in the checkout", dirty: true, want: review.StateFixed},
		{name: "nothing left in the checkout", dirty: false, want: review.StatePushed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _, gitc := fixService(t)
			rec := fixReviewInBackground(t, svc, idleListing("6d681a76", bgSession))
			gitc.dirty[rec.Dir] = tt.dirty
			writeFixSummary(t, rec)

			records, statuses, err := svc.PollBackground(context.Background())
			if err != nil {
				t.Fatalf("PollBackground: %v", err)
			}

			if records[0].State != tt.want {
				t.Errorf("state = %q, want %q", records[0].State, tt.want)
			}
			if _, polled := statuses[rec.ID]; polled {
				t.Error("a settled fix session is still reported as running")
			}
			if got := storedByID(t, svc, rec.ID); got.State != tt.want || !got.HasBackgroundSession() {
				t.Errorf("stored %q with background session %v, want %q with the session kept for enter", got.State, got.HasBackgroundSession(), tt.want)
			}
		})
	}
}

// Notes from an earlier review are on disk before the session starts. They
// say nothing about this session.
func TestAPollIgnoresNotesWrittenBeforeTheFixSessionStarted(t *testing.T) {
	svc, _, _ := fixService(t)
	rec := fixReviewInBackground(t, svc, idleListing("6d681a76", bgSession))
	writeNotes(t, rec.NotesPath, "# Review\n\n## Fix Summary\n\nFrom an earlier run.\n")
	earlier := rec.StartedAt.Add(-time.Hour)
	if err := os.Chtimes(rec.NotesPath, earlier, earlier); err != nil {
		t.Fatal(err)
	}

	records, _, err := svc.PollBackground(context.Background())
	if err != nil {
		t.Fatalf("PollBackground: %v", err)
	}

	if records[0].State != review.StateReviewing {
		t.Errorf("state = %q, want the fix session still running", records[0].State)
	}
}

// Only the compose step after the fix pass writes the Fix Summary. The branch
// review merge writes the notes earlier, without one.
func TestAPollIgnoresNotesWithoutAFixSummary(t *testing.T) {
	svc, _, _ := fixService(t)
	rec := fixReviewInBackground(t, svc, idleListing("6d681a76", bgSession))
	writeNotes(t, rec.NotesPath, "# Review\n\nMerged from the branch review.\n")
	at := rec.StartedAt.Add(time.Minute)
	if err := os.Chtimes(rec.NotesPath, at, at); err != nil {
		t.Fatal(err)
	}

	records, _, err := svc.PollBackground(context.Background())
	if err != nil {
		t.Fatalf("PollBackground: %v", err)
	}

	if records[0].State != review.StateReviewing {
		t.Errorf("state = %q, want the fix session still running", records[0].State)
	}
}

// A session claude no longer holds is over, notes or not.
func TestAPollReadsTheCheckoutOfAFinishedFixSession(t *testing.T) {
	svc, _, gitc := fixService(t)
	rec := fixReviewInBackground(t, svc, bgListing("6d681a76", bgSession, "done", false))
	gitc.dirty[rec.Dir] = true

	records, _, err := svc.PollBackground(context.Background())
	if err != nil {
		t.Fatalf("PollBackground: %v", err)
	}

	if records[0].State != review.StateFixed {
		t.Errorf("state = %q, want fixed", records[0].State)
	}
}

// --- Protecting local work ---

// x on a fixed row must not delete fixes nobody pushed. The row stays open and
// says where they are.
func TestAbandonKeepsACheckoutThatHoldsFixes(t *testing.T) {
	for name, spoil := range map[string]func(*fakeGit, string){
		"uncommitted changes": func(g *fakeGit, dir string) { g.dirty[dir] = true },
		"commits not pushed":  func(g *fakeGit, dir string) { g.heads[dir], g.ahead[dir] = "fix-sha", 2 },
	} {
		t.Run(name, func(t *testing.T) {
			svc, _, gitc := fixService(t)
			rec := fixReviewLaunched(t, svc, unlisted)
			spoil(gitc, rec.Dir)
			fixed, err := svc.AfterExit(context.Background(), rec, nil)
			if err != nil || fixed.State != review.StateFixed {
				t.Fatalf("AfterExit = %q, %v, want fixed", fixed.State, err)
			}

			done, err := svc.Abandon(context.Background(), fixed)

			if err == nil || !strings.Contains(err.Error(), rec.Dir) {
				t.Errorf("Abandon answered %v, want a refusal that names %s", err, rec.Dir)
			}
			if done.State == review.StateAbandoned {
				t.Error("the row was abandoned with fixes still in its checkout")
			}
			if got := storedByID(t, svc, rec.ID); got.State != review.StateFixed {
				t.Errorf("stored state = %q, want the row left fixed", got.State)
			}
			if _, err := os.Stat(rec.Dir); err != nil {
				t.Errorf("the checkout that holds the fixes is gone: %v", err)
			}
		})
	}
}

// A checkout docket cannot read may hold fixes, so it stays.
func TestAbandonKeepsACheckoutItCannotCheck(t *testing.T) {
	svc, _, gitc := fixService(t)
	rec := fixReviewPushed(t, svc, unlisted)
	gitc.failAt = "status " + rec.Dir

	if _, err := svc.Abandon(context.Background(), rec); err == nil {
		t.Error("Abandon deleted a checkout it could not check")
	}
	if _, err := os.Stat(rec.Dir); err != nil {
		t.Errorf("the checkout is gone: %v", err)
	}
}

func TestAbandonDeletesACleanFixClone(t *testing.T) {
	svc, _, _ := fixService(t)
	rec := fixReviewPushed(t, svc, unlisted)

	done, err := svc.Abandon(context.Background(), rec)
	if err != nil {
		t.Fatalf("Abandon: %v", err)
	}

	if done.State != review.StateAbandoned {
		t.Errorf("state = %q, want abandoned", done.State)
	}
	if _, err := os.Stat(rec.Dir); !os.IsNotExist(err) {
		t.Errorf("the clone at %s survived", rec.Dir)
	}
}

// A tier-3 worktree belongs to the user's clone. Removing its directory alone
// would leave git's record of it and the branch behind, and the next fix review
// of the pull request would find the branch taken.
func TestAbandonRemovesACleanWorktreeAndItsBranchFromTheUsersClone(t *testing.T) {
	svc, _, gitc := fixService(t)
	rec := fixReviewPushed(t, svc, listed)

	done, err := svc.Abandon(context.Background(), rec)
	if err != nil {
		t.Fatalf("Abandon: %v", err)
	}

	if done.State != review.StateAbandoned {
		t.Errorf("state = %q, want abandoned", done.State)
	}
	removed := slices.Index(gitc.calls, "worktree-remove "+rec.WorktreeOf+" "+rec.Dir)
	deleted := slices.Index(gitc.calls, "branch-delete "+rec.WorktreeOf+" "+botHead)
	if removed < 0 || deleted < 0 || removed > deleted {
		t.Errorf("git calls = %v, want the worktree removed from %s and then its branch deleted", gitc.calls, rec.WorktreeOf)
	}
	if gitc.localBranches[rec.WorktreeOf+" "+botHead] {
		t.Errorf("%s still has the branch %s", rec.WorktreeOf, botHead)
	}
	if _, err := os.Stat(rec.Dir); !os.IsNotExist(err) {
		t.Errorf("the worktree at %s survived", rec.Dir)
	}
}

// An open record blocks a new Prepare, so a checkout kept for its fixes is
// never reset by the next review of the pull request.
func TestAKeptFixRowBlocksANewReviewOfThePullRequest(t *testing.T) {
	svc, _, gitc := fixService(t)
	rec := fixReviewLaunched(t, svc, unlisted)
	gitc.dirty[rec.Dir] = true
	fixed, _ := svc.AfterExit(context.Background(), rec, nil)
	_, _ = svc.Abandon(context.Background(), fixed)
	before := len(called(gitc, "reset"))

	if _, _, err := svc.Prepare(context.Background(), unlisted, "claude", review.ModeInteractive, review.IntentReview, review.FixOn); err == nil {
		t.Error("Prepare started a second review over a checkout that holds fixes")
	}
	if got := len(called(gitc, "reset")); got != before {
		t.Errorf("git calls = %v, want no reset", gitc.calls)
	}
}

// --- Reviewing again ---

// The re-review resets the checkout to the remote branch. That is safe only
// with nothing local.
func TestRereviewOfAFixRowRefusesWhileTheCheckoutHoldsFixes(t *testing.T) {
	svc, _, gitc := fixService(t)
	rec := fixReviewLaunched(t, svc, unlisted)
	gitc.dirty[rec.Dir] = true
	fixed, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}

	_, err = svc.Rereview(context.Background(), fixed, review.IntentOverwrite, review.ModeInteractive)

	if err == nil || !strings.Contains(err.Error(), rec.Dir) {
		t.Errorf("Rereview answered %v, want a refusal that names %s", err, rec.Dir)
	}
	if got := called(gitc, "reset"); len(got) != 0 {
		t.Errorf("git calls = %v, want no reset of a checkout that holds fixes", gitc.calls)
	}
}

// A reset to the tracking ref from the last fetch that worked would start the
// new review on an out-of-date head.
func TestRereviewOfAFixRowRefusesABranchItCannotFetch(t *testing.T) {
	svc, _, gitc := fixService(t)
	rec := fixReviewPushed(t, svc, unlisted)
	gitc.fetchErr = errors.New("couldn't find remote ref refs/heads/" + botHead)

	if _, err := svc.Rereview(context.Background(), rec, review.IntentOverwrite, review.ModeInteractive); err == nil {
		t.Error("Rereview reset a checkout whose branch it could not fetch")
	}
	if got := called(gitc, "reset"); len(got) != 0 {
		t.Errorf("git calls = %v, want no reset", gitc.calls)
	}
}

// The pull request may have moved since the fixes were pushed, and the review
// and its fixes have to see the current head.
func TestRereviewOfAPushedRowMovesTheCheckoutToTheCurrentHead(t *testing.T) {
	svc, _, gitc := fixService(t)
	rec := fixReviewPushed(t, svc, unlisted)
	gitc.refs["origin/"+botHead] = "new-head"
	gitc.calls = nil

	again, err := svc.Rereview(context.Background(), rec, review.IntentAppend, review.ModeInteractive)
	if err != nil {
		t.Fatalf("Rereview: %v", err)
	}

	if !again.Fix {
		t.Error("the re-review dropped the fix flag")
	}
	fetched := slices.IndexFunc(gitc.calls, func(c string) bool { return strings.HasPrefix(c, "fetch-branch "+rec.Dir) })
	reset := slices.Index(gitc.calls, "reset "+rec.Dir+" origin/"+botHead)
	if fetched < 0 || reset < 0 || fetched > reset {
		t.Errorf("git calls = %v, want a fetch and then a reset", gitc.calls)
	}
	if again.FixBase != "new-head" {
		t.Errorf("fix base = %q, want the head the checkout was reset to", again.FixBase)
	}

	_, spec, err := svc.LaunchSpec(context.Background(), again)
	if err != nil {
		t.Fatalf("LaunchSpec: %v", err)
	}
	if line := launchLine(t, spec); !strings.Contains(line, "--fix") || !strings.Contains(line, "--append") {
		t.Errorf("command %s, want --fix and --append", line)
	}
}

// A dry run reaches no runner with a write.
func TestExplainRereviewOfAFixRowResetsNothing(t *testing.T) {
	svc, _, gitc := fixService(t)
	rec := fixReviewPushed(t, svc, unlisted)
	gitc.calls = nil

	spec, err := svc.ExplainRereview(context.Background(), rec, review.IntentOverwrite, review.ModeInteractive)
	if err != nil {
		t.Fatalf("ExplainRereview: %v", err)
	}

	if got := called(gitc, "reset"); len(got) != 0 {
		t.Errorf("git calls = %v, want no reset in a dry run", gitc.calls)
	}
	if line := launchLine(t, spec); !strings.Contains(line, "--fix") {
		t.Errorf("command %s, want --fix", line)
	}
}

// A launch claude refused starts again as the same kind of review.
func TestRestartKeepsTheFixFlag(t *testing.T) {
	svc, _, _ := fixService(t)
	svc.Runner = &exec.Fake{Errs: map[string]error{"--bg": errors.New("claude is not logged in")}}
	rec, _ := fixReviewPrepared(t, svc, unlisted, review.ModeBackground)
	refused, _ := svc.StartBackground(context.Background(), rec)
	if refused.State != review.StateNotStarted {
		t.Fatalf("state = %q, want not started", refused.State)
	}
	runner := bgRunner(bgListing("6d681a76", bgSession, "working", true))
	svc.Runner = runner

	if _, err := svc.Restart(context.Background(), refused); err != nil {
		t.Fatalf("Restart: %v", err)
	}

	var launches []string
	for _, line := range runner.Lines() {
		if strings.Contains(line, "--bg") {
			launches = append(launches, line)
		}
	}
	if len(launches) != 1 || !strings.Contains(launches[0], "--fix") {
		t.Errorf("launches = %v, want one with --fix", launches)
	}
}

// --- Approving ---

// A pushed row has no pending review. Approving posts a new review on the
// commit the fixes ended at, and detection then reads it as submitted.
func TestSubmittingAPushedRowPostsANewReviewOnTheFixHeadAndArchives(t *testing.T) {
	svc, ghc, gitc := fixService(t)
	rec := fixReviewLaunched(t, svc, unlisted)
	gitc.heads[rec.Dir] = "pushed-sha"
	rec, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil || rec.State != review.StatePushed {
		t.Fatalf("AfterExit = %q, %v, want pushed", rec.State, err)
	}
	ghc.info.HeadRefOid = "pushed-sha"

	done, err := svc.Submit(context.Background(), rec, review.EventApprove, "")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	want := createCall{ref: unlisted, commitID: "pushed-sha", event: review.EventApprove, body: ""}
	if len(ghc.created) != 1 || ghc.created[0] != want {
		t.Errorf("created %+v, want one call of %+v", ghc.created, want)
	}
	if len(ghc.submitted) != 0 {
		t.Errorf("submitted %+v to a pending review's events, want none", ghc.submitted)
	}
	if done.State != review.StateArchived {
		t.Errorf("state = %q, want archived", done.State)
	}
	if done.SubmittedAt == nil {
		t.Error("the record carries no submission time")
	}
	if _, err := os.Stat(rec.Dir); !os.IsNotExist(err) {
		t.Errorf("the clone at %s survived the approval", rec.Dir)
	}
}

func TestSubmittingAPushedRowWithACommentSendsTheBody(t *testing.T) {
	svc, ghc, _ := fixService(t)
	rec := fixReviewPushed(t, svc, unlisted)

	if _, err := svc.Submit(context.Background(), rec, review.EventComment, "Fixed the retry loop; the rename is yours."); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	if len(ghc.created) != 1 || ghc.created[0].body != "Fixed the retry loop; the rename is yours." || ghc.created[0].event != review.EventComment {
		t.Errorf("created %+v, want one comment with the body", ghc.created)
	}
}

// Somebody pushed after the fixes, and an approval would vouch for commits the
// user has not seen.
func TestSubmittingAPushedRowRefusesAHeadThatMovedPastTheFixes(t *testing.T) {
	svc, ghc, _ := fixService(t)
	rec := fixReviewPushed(t, svc, unlisted)
	ghc.info.HeadRefOid = "someone-elses-sha"

	done, err := svc.Submit(context.Background(), rec, review.EventApprove, "")

	if err == nil || !strings.Contains(err.Error(), "press u") {
		t.Errorf("Submit answered %v, want a refusal that says to press u", err)
	}
	if len(ghc.created) != 0 {
		t.Errorf("created %+v, want none", ghc.created)
	}
	if done.State != review.StatePushed {
		t.Errorf("state = %q, want the row left pushed", done.State)
	}
	if got := storedByID(t, svc, rec.ID); got.State != review.StatePushed {
		t.Errorf("stored state = %q, want pushed", got.State)
	}
}

// Whether GitHub accepts a new review beside a pending one of mine is
// unverified, so docket refuses first.
func TestSubmittingAPushedRowRefusesWhileIHaveAPendingReview(t *testing.T) {
	svc, ghc, _ := fixService(t)
	rec := fixReviewPushed(t, svc, unlisted)
	ghc.reviews = []review.GHReview{myPending()}

	_, err := svc.Submit(context.Background(), rec, review.EventApprove, "")

	if err == nil || !strings.Contains(err.Error(), "pending") {
		t.Errorf("Submit answered %v, want a refusal that names the pending review", err)
	}
	if len(ghc.created) != 0 {
		t.Errorf("created %+v, want none", ghc.created)
	}
}

// The user approved on GitHub while the row still read ready to approve. s
// reads that review rather than posting a second one.
func TestSubmittingAPushedRowIAlreadyReviewedPostsNothing(t *testing.T) {
	svc, ghc, _ := fixService(t)
	rec := fixReviewPushed(t, svc, unlisted)
	at := start.Add(3 * time.Minute)
	ghc.reviews = []review.GHReview{{ID: 9, User: review.GHUser{Login: "haacked"}, State: "APPROVED", SubmittedAt: &at, CommitID: rec.FixHead}}

	done, err := svc.Submit(context.Background(), rec, review.EventApprove, "")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	if len(ghc.created) != 0 {
		t.Errorf("created %+v, want no second review", ghc.created)
	}
	if done.State != review.StateArchived {
		t.Errorf("state = %q, want archived", done.State)
	}
}

// GitHub requires a body for a comment and for a request for changes. Only an
// approval may go without one.
func TestSubmittingAPushedRowRefusesAnEmptyBodyForACommentOrARequestForChanges(t *testing.T) {
	for _, event := range []string{review.EventComment, review.EventRequestChanges} {
		t.Run(event, func(t *testing.T) {
			svc, ghc, _ := fixService(t)
			rec := fixReviewPushed(t, svc, unlisted)

			done, err := svc.Submit(context.Background(), rec, event, "")

			if err == nil {
				t.Fatal("Submit accepted an empty body")
			}
			if len(ghc.created) != 0 {
				t.Errorf("created %+v, want none", ghc.created)
			}
			if done.State != review.StatePushed {
				t.Errorf("state = %q, want the row left pushed", done.State)
			}
		})
	}
}

// GitHub answers 422 to approving your own pull request.
func TestSubmittingAPushedRowOfMyOwnPullRequestRefusesAnApproval(t *testing.T) {
	svc, ghc, _ := fixService(t)
	ghc.info.Author.Login = "haacked"
	rec := fixReviewPushed(t, svc, unlisted)

	if _, err := svc.Submit(context.Background(), rec, review.EventApprove, ""); err == nil {
		t.Error("Submit approved my own pull request")
	}
	if len(ghc.created) != 0 {
		t.Errorf("created %+v, want none", ghc.created)
	}
}

func TestAFailedApprovalKeepsThePushedRow(t *testing.T) {
	svc, ghc, _ := fixService(t)
	rec := fixReviewPushed(t, svc, unlisted)
	ghc.createErr = errors.New("HTTP 502")

	done, err := svc.Submit(context.Background(), rec, review.EventApprove, "")

	if err == nil {
		t.Fatal("Submit reported success on a failed post")
	}
	if done.State != review.StatePushed || !strings.Contains(done.Err, "502") {
		t.Errorf("state %q with err %q, want pushed with the failure", done.State, done.Err)
	}
	if _, err := os.Stat(rec.Dir); err != nil {
		t.Errorf("the clone is gone after a failed approval: %v", err)
	}
}

// The archive after an approval goes through the same cleanup, which removes a
// tier-3 worktree from the user's clone.
func TestApprovingATier3RowRemovesTheWorktreeAndItsBranch(t *testing.T) {
	svc, _, gitc := fixService(t)
	rec := fixReviewPushed(t, svc, listed)

	done, err := svc.Submit(context.Background(), rec, review.EventApprove, "")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	if done.State != review.StateArchived {
		t.Errorf("state = %q, want archived", done.State)
	}
	if got := called(gitc, "worktree-remove "+rec.WorktreeOf+" "+rec.Dir); len(got) != 1 {
		t.Errorf("git calls = %v, want the worktree removed", gitc.calls)
	}
	if got := called(gitc, "branch-delete "+rec.WorktreeOf+" "+botHead); len(got) != 1 {
		t.Errorf("git calls = %v, want the branch deleted", gitc.calls)
	}
}

// The user opens a background fix row's session, has the agent push, and
// leaves it idle. Leaving reads the checkout again.
func TestLeavingAnIdleFixSessionAfterPushingReadsTheCheckout(t *testing.T) {
	svc, _, gitc := fixService(t)
	rec := fixReviewInBackground(t, svc, idleListing("6d681a76", bgSession))
	writeFixSummary(t, rec)
	gitc.heads[rec.Dir] = "pushed-sha"

	done, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil {
		t.Fatalf("AfterExit: %v", err)
	}

	if done.State != review.StatePushed || done.FixHead != "pushed-sha" {
		t.Errorf("state %q at %q, want pushed at pushed-sha", done.State, done.FixHead)
	}
}

// An agent calls list_reviews often. A dirty checkout still holds local work, so
// a refresh of its fixed row reads nothing from GitHub and writes nothing.
func TestRefreshFixedLeavesADirtyCheckoutAlone(t *testing.T) {
	svc, ghc, gitc := fixService(t)
	rec := fixReviewLaunched(t, svc, unlisted)
	gitc.dirty[rec.Dir] = true
	fixed, err := svc.AfterExit(context.Background(), rec, nil)
	if err != nil || fixed.State != review.StateFixed {
		t.Fatalf("AfterExit = %q, %v, want fixed", fixed.State, err)
	}
	reads := ghc.reads
	before, err := svc.Records()
	if err != nil {
		t.Fatal(err)
	}

	got, err := svc.RefreshFixed(context.Background(), fixed)
	if err != nil {
		t.Fatalf("RefreshFixed: %v", err)
	}

	if got.State != review.StateFixed {
		t.Errorf("state = %q, want fixed", got.State)
	}
	if ghc.reads != reads {
		t.Errorf("GitHub reads = %d, want %d", ghc.reads, reads)
	}
	after, err := svc.Records()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Error("RefreshFixed wrote to the index")
	}
}
