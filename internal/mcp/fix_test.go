package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/gh"
	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/review"
)

// fixable answers the git reads a fix review's checkout needs. The clone starts
// on base-sha with nothing local.
func (f *fixture) fixable() {
	f.runner.Results["rev-parse HEAD"] = exec.Result{Stdout: "base-sha\n"}
	f.runner.Results["rev-list --count"] = exec.Result{Stdout: "0\n"}
	f.runner.Results["status --porcelain"] = exec.Result{Stdout: ""}
}

// launchedWith is the one claude --bg launch the fixture ran.
func (f *fixture) launchedWith(t *testing.T) string {
	t.Helper()
	launches := f.ran("--bg")
	if len(launches) != 1 {
		t.Fatalf("launches = %v, want one", launches)
	}
	return launches[0]
}

// headAt answers PR with the head at a commit, which the fixture's own fake
// leaves empty.
type headAt struct {
	*fakeGitHub
	head string
}

func (h headAt) PR(ctx context.Context, ref pr.Ref) (gh.PRInfo, error) {
	info, err := h.fakeGitHub.PR(ctx, ref)
	info.HeadRefOid = h.head
	return info, err
}

// fix is optional. Absent means auto, so fix_authors decides.
func TestStartReviewFixesWhenAskedOrWhenTheAuthorIsListed(t *testing.T) {
	tests := []struct {
		name    string
		listed  bool
		args    map[string]any
		wantFix bool
	}{
		{name: "fix true", args: map[string]any{"fix": true}, wantFix: true},
		{name: "fix left out with the author not listed", args: nil, wantFix: false},
		{name: "fix left out with the author listed", listed: true, args: nil, wantFix: true},
		{name: "fix false with the author listed", listed: true, args: map[string]any{"fix": false}, wantFix: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.fixable()
			if tt.listed {
				f.svc.Cfg.FixAuthors = []string{"someone"}
			}

			out := decode[startOutput](t, f.start(t, tt.args))

			line := f.launchedWith(t)
			if got := strings.Contains(line, " --fix"); got != tt.wantFix {
				t.Errorf("launch %s carries --fix = %v, want %v", line, got, tt.wantFix)
			}
			if got := strings.Contains(line, " --draft"); got == tt.wantFix {
				t.Errorf("launch %s carries --draft = %v, want %v", line, got, !tt.wantFix)
			}
			if out.Review.Fix != tt.wantFix {
				t.Errorf("review = %+v, want fix %v", out.Review, tt.wantFix)
			}
		})
	}
}

func TestListReviewsReportsAFixReviewAndItsStates(t *testing.T) {
	tests := []struct {
		name        string
		status      string
		head        string
		wantState   review.State
		submittable bool
	}{
		{name: "fixes left in the checkout", status: " M src/retry.go\n", head: "base-sha", wantState: review.StateFixed, submittable: false},
		{name: "fixes pushed", status: "", head: "pushed-sha", wantState: review.StatePushed, submittable: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.fixable()
			decode[startOutput](t, f.start(t, map[string]any{"fix": true}))
			// The session is over: claude no longer lists it.
			f.runner.Results["status --porcelain"] = exec.Result{Stdout: tt.status}
			f.runner.Results["rev-parse HEAD"] = exec.Result{Stdout: tt.head + "\n"}

			got := f.listedOne(t)

			if got.State != string(tt.wantState) {
				t.Errorf("state = %q, want %q", got.State, tt.wantState)
			}
			if !got.Fix {
				t.Error("list_reviews does not say the review is a fix review")
			}
			if got.Submittable != tt.submittable {
				t.Errorf("submittable = %v, want %v", got.Submittable, tt.submittable)
			}
			if dir := f.records(t)[0].Dir; dir == "" || got.Checkout != dir {
				t.Errorf("checkout = %q, want the fix review's directory %q", got.Checkout, dir)
			}
		})
	}
}

// The checkout field tells an agent where to push, and docket never pushes. A
// push made there moves the review to pushed on the next list_reviews.
func TestListReviewsReadsAFixedCheckoutAgain(t *testing.T) {
	f := newFixture(t)
	f.fixable()
	decode[startOutput](t, f.start(t, map[string]any{"fix": true}))
	f.runner.Results["status --porcelain"] = exec.Result{Stdout: " M src/retry.go\n"}
	if got := f.listedOne(t); got.State != string(review.StateFixed) {
		t.Fatalf("state = %q, want fixed", got.State)
	}
	f.runner.Results["status --porcelain"] = exec.Result{Stdout: ""}
	f.runner.Results["rev-parse HEAD"] = exec.Result{Stdout: "pushed-sha\n"}

	got := f.listedOne(t)

	if got.State != string(review.StatePushed) || !got.Submittable {
		t.Errorf("state = %q with submittable %v, want pushed and submittable", got.State, got.Submittable)
	}
}

func TestListReviewsSaysADraftReviewIsNotAFixReview(t *testing.T) {
	f := newFixture(t)
	f.started(t)

	got := f.listedOne(t)

	if got.Fix {
		t.Errorf("review = %+v, want fix false", got)
	}
	if got.Checkout != "" {
		t.Errorf("checkout = %q, want none for a draft review", got.Checkout)
	}
}

// A pushed fix review has no pending review. submit_review posts a new one on
// the commit the fixes are on and archives the record.
func TestSubmitReviewApprovesAPushedFixReview(t *testing.T) {
	f := newFixture(t)
	f.fixable()
	decode[startOutput](t, f.start(t, map[string]any{"fix": true}))
	f.runner.Results["rev-parse HEAD"] = exec.Result{Stdout: "pushed-sha\n"}
	if got := f.listedOne(t); got.State != string(review.StatePushed) {
		t.Fatalf("state = %q, want pushed", got.State)
	}
	f.svc.GH = headAt{fakeGitHub: f.gh, head: "pushed-sha"}

	out := decode[Review](t, f.submit(t, map[string]any{"event": review.EventApprove}))

	if out.State != string(review.StateArchived) {
		t.Errorf("state = %q, want archived", out.State)
	}
	if len(f.gh.submits) != 1 || !strings.HasPrefix(f.gh.submits[0], "new pushed-sha APPROVE") {
		t.Errorf("submits = %v, want one new review on pushed-sha", f.gh.submits)
	}
}

// An agent may push from the checkout and call submit_review with no
// list_reviews in between. submit_review reads the fixed record again first.
func TestSubmitReviewReadsAFixedCheckoutAgain(t *testing.T) {
	f := newFixture(t)
	f.fixable()
	decode[startOutput](t, f.start(t, map[string]any{"fix": true}))
	f.runner.Results["status --porcelain"] = exec.Result{Stdout: " M src/retry.go\n"}
	if got := f.listedOne(t); got.State != string(review.StateFixed) {
		t.Fatalf("state = %q, want fixed", got.State)
	}
	f.runner.Results["status --porcelain"] = exec.Result{Stdout: ""}
	f.runner.Results["rev-parse HEAD"] = exec.Result{Stdout: "pushed-sha\n"}
	f.svc.GH = headAt{fakeGitHub: f.gh, head: "pushed-sha"}

	out := decode[Review](t, f.submit(t, map[string]any{"event": review.EventApprove}))

	if out.State != string(review.StateArchived) {
		t.Errorf("state = %q, want archived", out.State)
	}
	if len(f.gh.submits) != 1 || !strings.HasPrefix(f.gh.submits[0], "new pushed-sha APPROVE") {
		t.Errorf("submits = %v, want one new review on pushed-sha", f.gh.submits)
	}
}

// No tool abandons a record, so a start refused over an open fix review names
// the next step for its state.
func TestAStartOverAnOpenFixReviewNamesTheNextStep(t *testing.T) {
	ref := pr.Ref{Org: "haacked", Repo: "docket", Number: 7}
	tests := []struct {
		name  string
		state review.State
		wants []string
	}{
		{name: "fixed", state: review.StateFixed, wants: []string{"fixed", "enter", "/tmp/clones/haacked/docket/pr-7"}},
		{name: "pushed", state: review.StatePushed, wants: []string{"pushed", "submit_review", "commit"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := review.Record{Ref: ref, State: tt.state, Fix: true, Dir: "/tmp/clones/haacked/docket/pr-7"}

			err := alreadyOpen(rec)

			if err == nil {
				t.Fatal("alreadyOpen returned no error")
			}
			for _, want := range tt.wants {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("alreadyOpen = %q, want it to mention %q", err, want)
				}
			}
		})
	}
}

func TestStartReviewOverAnOpenFixedReviewIsRefused(t *testing.T) {
	f := newFixture(t)
	f.fixable()
	decode[startOutput](t, f.start(t, map[string]any{"fix": true}))
	f.runner.Results["status --porcelain"] = exec.Result{Stdout: " M src/retry.go\n"}
	if got := f.listedOne(t); got.State != string(review.StateFixed) {
		t.Fatalf("state = %q, want fixed", got.State)
	}

	refused(t, f.start(t, map[string]any{"fix": true}), "fixed", "enter")

	if got := f.ran("--bg"); len(got) != 1 {
		t.Errorf("launches = %v, want only the first", got)
	}
}
