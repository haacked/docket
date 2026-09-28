package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/haacked/docket/internal/core/clone"
	"github.com/haacked/docket/internal/core/config"
	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/gh"
	"github.com/haacked/docket/internal/core/git"
	"github.com/haacked/docket/internal/core/index"
	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/requests"
	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/core/session"
)

const (
	me       = "haacked"
	pull     = "haacked/docket#7"
	bgID     = "0a1b2c3d"
	launched = "backgrounded · " + bgID + "\n"
)

// fakeGitHub answers for one pull request. A submit turns the pending review
// into a submitted one, which is what detection reads back afterwards.
type fakeGitHub struct {
	reviews []review.GHReview
	submits []string
}

func (f *fakeGitHub) Login(context.Context) (string, error) { return me, nil }

func (f *fakeGitHub) PR(context.Context, pr.Ref) (gh.PRInfo, error) {
	info := gh.PRInfo{Number: 7, Title: "Add a thing", HeadRefName: "haacked/a-thing", State: review.PROpen}
	info.Author.Login = "someone"
	return info, nil
}

func (f *fakeGitHub) Reviews(context.Context, pr.Ref) ([]review.GHReview, error) {
	return f.reviews, nil
}

func (f *fakeGitHub) SubmitReview(_ context.Context, _ pr.Ref, id int64, event, body string) error {
	f.submits = append(f.submits, fmt.Sprintf("%d %s %q", id, event, body))
	for i := range f.reviews {
		if f.reviews[i].ID == id {
			now := time.Now()
			f.reviews[i].State = "COMMENTED"
			f.reviews[i].SubmittedAt = &now
		}
	}
	return nil
}

func (f *fakeGitHub) ReviewRequests(context.Context, string) ([]requests.PR, error) { return nil, nil }

func (f *fakeGitHub) post(r review.GHReview) {
	r.User.Login = me
	f.reviews = append(f.reviews, r)
}

type fixture struct {
	svc    *session.Service
	gh     *fakeGitHub
	runner *exec.Fake
	client *sdk.ClientSession
}

// newFixture is a real service over a temporary index, connected to a client
// through the SDK's in-memory transport. The pull request is not in repos.conf,
// so every review is a tier-2 clone. claude reports an id for every start.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	paths, err := config.NewPaths(t.TempDir())
	if err != nil {
		t.Fatalf("config.NewPaths: %v", err)
	}
	if err := paths.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	runner := &exec.Fake{Results: map[string]exec.Result{
		"branch --show-current": {Stdout: "haacked/a-thing\n"},
		"ls-files":              {Stdout: "README.md\n"},
		"--bg":                  {Stdout: launched},
		"agents --json":         {Stdout: "[]"},
	}}
	gitc := git.New(runner)
	github := &fakeGitHub{}
	svc := &session.Service{
		Cfg: config.Config{
			ReviewCodeDir: t.TempDir(),
			ClaudeJobsDir: t.TempDir(),
			DefaultEngine: "claude",
			GitHubUser:    me,
		},
		Paths:  paths,
		Store:  index.New(paths.Index, paths.Lock),
		GH:     github,
		Git:    gitc,
		Cloner: clone.New(gitc, paths),
		Runner: runner,
	}

	serverEnd, clientEnd := sdk.NewInMemoryTransports()
	if _, err := New(svc, "claude").Connect(t.Context(), serverEnd, nil); err != nil {
		t.Fatalf("connect the server: %v", err)
	}
	client, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "v0"}, nil).Connect(t.Context(), clientEnd, nil)
	if err != nil {
		t.Fatalf("connect the client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return &fixture{svc: svc, gh: github, runner: runner, client: client}
}

func (f *fixture) call(t *testing.T, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	res, err := f.client.CallTool(t.Context(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

// start calls start_review for the pull request with any further arguments.
func (f *fixture) start(t *testing.T, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	return f.call(t, "start_review", withPull(args))
}

// started starts the review and reports what start_review said.
func (f *fixture) started(t *testing.T) startOutput {
	t.Helper()
	return decode[startOutput](t, f.start(t, nil))
}

func (f *fixture) submit(t *testing.T, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	return f.call(t, "submit_review", withPull(args))
}

func (f *fixture) listed(t *testing.T) listOutput {
	t.Helper()
	return decode[listOutput](t, f.call(t, "list_reviews", nil))
}

// listedOne is the one review list_reviews reports.
func (f *fixture) listedOne(t *testing.T) Review {
	t.Helper()
	out := f.listed(t)
	if len(out.Reviews) != 1 {
		t.Fatalf("reviews = %+v, want one", out.Reviews)
	}
	return out.Reviews[0]
}

func withPull(args map[string]any) map[string]any {
	out := map[string]any{"pull_request": pull}
	for k, v := range args {
		out[k] = v
	}
	return out
}

// drafted starts a review whose session finished after posting review 55 as a
// pending draft. The index still reads reviewing until something polls.
func (f *fixture) drafted(t *testing.T) {
	t.Helper()
	f.started(t)
	f.gh.post(review.GHReview{ID: 55, State: review.StatePending})
	f.agents("done", "idle")
}

// reviewedBefore puts a review of mine that was submitted an hour ago on GitHub.
func (f *fixture) reviewedBefore() {
	earlier := time.Now().Add(-time.Hour)
	f.gh.post(review.GHReview{ID: 40, State: "COMMENTED", SubmittedAt: &earlier})
}

// agents sets what `claude agents --json --all` lists for the review's session.
func (f *fixture) agents(state, status string) {
	f.runner.Results["agents --json"] = exec.Result{Stdout: fmt.Sprintf(
		`[{"kind":"bg","id":%q,"sessionId":"5f0c2a9e-0000-4000-8000-000000000000","state":%q,"status":%q,"pid":42}]`,
		bgID, state, status,
	)}
}

// jobState writes claude's status file for the review's session.
func (f *fixture) jobState(t *testing.T, state string) {
	t.Helper()
	dir := filepath.Join(f.svc.Cfg.ClaudeJobsDir, bgID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(state), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) records(t *testing.T) []review.Record {
	t.Helper()
	records, err := f.svc.Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	return records
}

func (f *fixture) launches() []string {
	var out []string
	for _, line := range f.runner.Lines() {
		if strings.Contains(line, "--bg") {
			out = append(out, line)
		}
	}
	return out
}

func decode[T any](t *testing.T, res *sdk.CallToolResult) T {
	t.Helper()
	if res.IsError {
		t.Fatalf("the tool failed: %s", text(res))
	}
	data, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("encode the structured content: %v", err)
	}
	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
	return out
}

// refused fails the test unless the tool failed with a message that mentions
// each of wants.
func refused(t *testing.T, res *sdk.CallToolResult, wants ...string) {
	t.Helper()
	if !res.IsError {
		t.Fatalf("the tool succeeded, want a failure: %s", text(res))
	}
	msg := text(res)
	for _, want := range wants {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not mention %q: %s", want, msg)
		}
	}
}

func text(res *sdk.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestTheServerOffersItsThreeTools(t *testing.T) {
	f := newFixture(t)
	res, err := f.client.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	if want := []string{"list_reviews", "start_review", "submit_review"}; !slices.Equal(names, want) {
		t.Errorf("tools = %v, want %v", names, want)
	}
}

// Every tool writes to the index. list_reviews archives a review it finds
// submitted, which stops the session and deletes the clone, and an append or an
// overwrite replaces the pending draft on GitHub. A client that trusts the hints
// must not read any of them as harmless.
func TestNoToolClaimsToBeReadOnlyOrHarmless(t *testing.T) {
	f := newFixture(t)
	res, err := f.client.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tool := range res.Tools {
		a := tool.Annotations
		if a == nil {
			continue
		}
		if a.ReadOnlyHint {
			t.Errorf("%s claims to be read-only", tool.Name)
		}
		if a.DestructiveHint != nil && !*a.DestructiveHint {
			t.Errorf("%s claims to be harmless", tool.Name)
		}
	}
}

func TestStartReviewStartsABackgroundReview(t *testing.T) {
	f := newFixture(t)
	out := f.started(t)

	if out.Review.PullRequest != pull || out.Review.State != string(review.StateReviewing) {
		t.Errorf("review = %+v, want %s reviewing", out.Review, pull)
	}
	if out.Review.Mode != string(review.ModeBackground) {
		t.Errorf("mode = %q, want background", out.Review.Mode)
	}
	if out.Plan == "" {
		t.Error("the result does not say where the review runs")
	}
	if got := f.launches(); len(got) != 1 {
		t.Fatalf("launches = %v, want one claude --bg", got)
	}
	records := f.records(t)
	if len(records) != 1 || records[0].BGID != bgID {
		t.Errorf("records = %+v, want one carrying the background id", records)
	}
}

func TestStartReviewAsksWhatToDoWithAnExistingReview(t *testing.T) {
	f := newFixture(t)
	f.reviewedBefore()

	refused(t, f.start(t, nil), "submitted review", "append", "overwrite")

	if records := f.records(t); len(records) != 0 {
		t.Errorf("records = %+v, want none before the caller answers", records)
	}
	if got := f.launches(); len(got) != 0 {
		t.Errorf("launches = %v, want none", got)
	}
}

// Agents often send an empty string for an optional field they have no value
// for.
func TestStartReviewReadsAnEmptyAnswerAsNoAnswer(t *testing.T) {
	f := newFixture(t)
	f.reviewedBefore()

	refused(t, f.start(t, map[string]any{"existing": ""}), "already has a review", "submitted review")
}

func TestStartReviewAppendsWhenAsked(t *testing.T) {
	f := newFixture(t)
	f.reviewedBefore()

	decode[startOutput](t, f.start(t, map[string]any{"existing": "append"}))

	launches := f.launches()
	if len(launches) != 1 || !strings.Contains(launches[0], "--append") {
		t.Errorf("launches = %v, want one passing --append", launches)
	}
}

func TestStartReviewRejectsAnAnswerItDoesNotKnow(t *testing.T) {
	f := newFixture(t)
	refused(t, f.start(t, map[string]any{"existing": "ask"}))
	if records := f.records(t); len(records) != 0 {
		t.Errorf("records = %+v, want none", records)
	}
}

func TestStartReviewRefusesAPullRequestDocketAlreadyHasOpen(t *testing.T) {
	f := newFixture(t)
	f.started(t)

	refused(t, f.start(t, nil), "already", "reviewing", "list_reviews")

	if got := f.launches(); len(got) != 1 {
		t.Errorf("launches = %v, want only the first", got)
	}
}

// claude asks whether to trust a directory only on a terminal, which an MCP
// server does not have. The refusal has to tell the caller what to run. The
// next start has to start the record that the refusal left.
func TestStartReviewExplainsAnUntrustedDirectoryAndStartsItAgainAfterwards(t *testing.T) {
	f := newFixture(t)
	f.runner.Results["--bg"] = exec.Result{Stderr: "Workspace not trusted. Run `claude` in /x once and accept the trust prompt, then retry.\n", ExitCode: 1}
	f.runner.Errs = map[string]error{"--bg": errors.New("exit status 1")}

	res := f.start(t, nil)

	records := f.records(t)
	if len(records) != 1 || records[0].State != review.StateNotStarted {
		t.Fatalf("records = %+v, want one that did not start", records)
	}
	refused(t, res, records[0].Dir, "trust", "start_review again")

	delete(f.runner.Errs, "--bg")
	f.runner.Results["--bg"] = exec.Result{Stdout: launched}
	out := f.started(t)

	if out.Review.State != string(review.StateReviewing) {
		t.Errorf("state = %q, want reviewing", out.Review.State)
	}
	records = f.records(t)
	if len(records) != 1 || records[0].BGID != bgID {
		t.Errorf("records = %+v, want the same record started", records)
	}
}

func TestStartReviewPointsADraftedReviewAtSubmitReview(t *testing.T) {
	f := newFixture(t)
	f.drafted(t)
	f.listedOne(t)

	res := f.start(t, nil)

	refused(t, res, "drafted", "submit_review")
	if strings.Contains(text(res), "press x") {
		t.Errorf("the refusal tells the user to abandon a draft: %s", text(res))
	}
}

// Prepare records the review before it clones, so a failed clone leaves an open
// record that no tool can clear. The agent has to be told how to clear it
// rather than to wait for it.
func TestStartReviewSaysHowToClearAReviewThatFailedToSetUp(t *testing.T) {
	f := newFixture(t)
	f.runner.Errs = map[string]error{"fetch": errors.New("could not resolve host")}

	refused(t, f.start(t, nil), "could not resolve host", "press x")
	refused(t, f.start(t, nil), "preparing", "could not resolve host", "press x")
}

// A launch that exited zero may have started a session even though docket could
// not read its id. The record stays reviewing, and the poll looks for the
// session.
func TestStartReviewSaysASessionMayHaveStartedWhenItCannotReadTheID(t *testing.T) {
	f := newFixture(t)
	f.runner.Results["--bg"] = exec.Result{}

	refused(t, f.start(t, nil), "may have started", "list_reviews")
}

func TestListReviewsReportsAPollThatFailed(t *testing.T) {
	f := newFixture(t)
	f.started(t)
	f.runner.Errs = map[string]error{"agents --json": errors.New("claude is not responding")}

	out := f.listed(t)

	if len(out.Reviews) != 1 {
		t.Errorf("reviews = %+v, want the one still open", out.Reviews)
	}
	if !strings.Contains(out.PollError, "claude is not responding") {
		t.Errorf("poll_error = %q, want the agent's failure", out.PollError)
	}
}

// claude's own reading of the conversation can name a need while the session's
// reviewer agents still run. A working session therefore reports no need.
func TestListReviewsReportsWhatARunningSessionIsDoing(t *testing.T) {
	f := newFixture(t)
	f.started(t)
	f.agents("blocked", "busy")
	f.jobState(t, `{"detail":"running reviewers","tempo":"active","needs":"results from 2 reviewers","fan":[{},{},{"doneAt":1}]}`)

	got := f.listedOne(t)

	if got.State != string(review.StateReviewing) || got.Progress == nil {
		t.Fatalf("review = %+v, want reviewing with progress", got)
	}
	if p := *got.Progress; p.Detail != "running reviewers" || p.Agents != 2 || p.Waiting || p.Needs != "" {
		t.Errorf("progress = %+v, want running reviewers with 2 agents, neither waiting nor needing anything", p)
	}
}

func TestListReviewsSaysWhatAWaitingSessionNeeds(t *testing.T) {
	f := newFixture(t)
	f.started(t)
	f.agents("blocked", "idle")
	f.jobState(t, `{"tempo":"blocked","needs":"permission to run gh"}`)

	got := f.listedOne(t)

	if got.Progress == nil {
		t.Fatalf("review = %+v, want progress", got)
	}
	if p := *got.Progress; !p.Waiting || p.Needs != "permission to run gh" {
		t.Errorf("progress = %+v, want waiting on permission to run gh", p)
	}
}

// Nothing else moves a finished background session on while the TUI is closed.
// Listing therefore reads the agent and GitHub rather than the index alone.
func TestListReviewsMovesAFinishedSessionToDrafted(t *testing.T) {
	f := newFixture(t)
	f.drafted(t)

	got := f.listedOne(t)

	if got.State != string(review.StateDrafted) || !got.Submittable || got.ReviewID != 55 {
		t.Errorf("review = %+v, want drafted and submittable with review 55", got)
	}
	if got.Progress != nil {
		t.Errorf("progress = %+v, want none once the session finished", *got.Progress)
	}
}

func TestListReviewsLeavesOutClosedReviews(t *testing.T) {
	f := newFixture(t)
	f.started(t)
	if _, err := f.svc.Abandon(t.Context(), f.records(t)[0]); err != nil {
		t.Fatalf("Abandon: %v", err)
	}

	if out := f.listed(t); len(out.Reviews) != 0 {
		t.Errorf("reviews = %+v, want none", out.Reviews)
	}
}

func TestSubmitReviewSubmitsTheDraftListReviewsFound(t *testing.T) {
	f := newFixture(t)
	f.drafted(t)
	f.listedOne(t)

	out := decode[Review](t, f.submit(t, map[string]any{"event": review.EventComment}))

	if want := []string{`55 COMMENT ""`}; !slices.Equal(f.gh.submits, want) {
		t.Errorf("submits = %v, want %v", f.gh.submits, want)
	}
	if out.State != string(review.StateArchived) {
		t.Errorf("state = %q, want archived", out.State)
	}
}

// The draft can be on GitHub while the index still says reviewing, because only
// a poll moves the record on. Submitting must not need a list first.
func TestSubmitReviewSubmitsADraftTheIndexHasNotSeenYet(t *testing.T) {
	f := newFixture(t)
	f.drafted(t)

	out := decode[Review](t, f.submit(t, map[string]any{"event": review.EventComment}))

	if want := []string{`55 COMMENT ""`}; !slices.Equal(f.gh.submits, want) {
		t.Errorf("submits = %v, want %v", f.gh.submits, want)
	}
	if out.State != string(review.StateArchived) {
		t.Errorf("state = %q, want archived", out.State)
	}
}

func TestSubmitReviewPassesTheBody(t *testing.T) {
	f := newFixture(t)
	f.drafted(t)

	decode[Review](t, f.submit(t, map[string]any{"event": review.EventRequestChanges, "body": "Please add a test."}))

	if want := []string{`55 REQUEST_CHANGES "Please add a test."`}; !slices.Equal(f.gh.submits, want) {
		t.Errorf("submits = %v, want %v", f.gh.submits, want)
	}
}

func TestSubmitReviewRefusesAReviewStillRunning(t *testing.T) {
	f := newFixture(t)
	f.started(t)
	f.agents("working", "busy")

	refused(t, f.submit(t, map[string]any{"event": review.EventComment}), "no pending review")

	if len(f.gh.submits) != 0 {
		t.Errorf("submits = %v, want none", f.gh.submits)
	}
}

func TestSubmitReviewSaysWhenItCannotAskTheAgent(t *testing.T) {
	f := newFixture(t)
	f.drafted(t)
	f.runner.Errs = map[string]error{"agents --json": errors.New("claude is not responding")}

	refused(t, f.submit(t, map[string]any{"event": review.EventComment}), "could not ask", "claude is not responding")

	if len(f.gh.submits) != 0 {
		t.Errorf("submits = %v, want none", f.gh.submits)
	}
}

// The session can submit the review itself before anything polls. The poll then
// archives the record, and the agent must not be sent to start_review.
func TestSubmitReviewSaysWhenTheReviewClosedWhileItRead(t *testing.T) {
	f := newFixture(t)
	f.started(t)
	now := time.Now()
	f.gh.post(review.GHReview{ID: 55, State: "COMMENTED", SubmittedAt: &now})
	f.agents("done", "idle")

	res := f.submit(t, map[string]any{"event": review.EventComment})

	refused(t, res, "archived")
	if strings.Contains(text(res), "start_review") {
		t.Errorf("the refusal sends the agent to start_review: %s", text(res))
	}
}

func TestSubmitReviewRefusesAPullRequestDocketIsNotReviewing(t *testing.T) {
	f := newFixture(t)
	refused(t, f.submit(t, map[string]any{"event": review.EventComment}), "start_review")
}

func TestSubmitReviewRejectsAnEventGitHubDoesNotTake(t *testing.T) {
	f := newFixture(t)
	f.drafted(t)

	refused(t, f.submit(t, map[string]any{"event": "LGTM"}))

	if len(f.gh.submits) != 0 {
		t.Errorf("submits = %v, want none", f.gh.submits)
	}
}
