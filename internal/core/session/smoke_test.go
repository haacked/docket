package session

import (
	"context"
	"os"
	"testing"

	"github.com/haacked/docket/internal/core/clone"
	"github.com/haacked/docket/internal/core/config"
	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/gh"
	"github.com/haacked/docket/internal/core/git"
	"github.com/haacked/docket/internal/core/index"
	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/core/tier"
)

// TestSmokeAgainstARealPullRequest checks the one thing fakes cannot: that the
// clone sequence really lands on the pull request's head branch, with files in
// the working tree. Run it by hand against a pull request of your choice:
//
//	DOCKET_SMOKE_PR=haacked/review-code#159 go test -count=1 -v -run Smoke ./internal/core/session/
func TestSmokeAgainstARealPullRequest(t *testing.T) {
	input := os.Getenv("DOCKET_SMOKE_PR")
	if input == "" {
		t.Skip("set DOCKET_SMOKE_PR=<org/repo#N> to run against a real pull request")
	}
	ref, err := pr.ParseRef(input, "")
	if err != nil {
		t.Fatalf("DOCKET_SMOKE_PR: %v", err)
	}

	svc := realService(t)
	ctx := context.Background()

	plan, spec, err := svc.Explain(ctx, ref, "claude")
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	t.Logf("%s is %s: %s", ref, plan.Tier, plan.Description())
	t.Logf("would run: %s", spec)

	rec, _, err := svc.Prepare(ctx, ref, "claude")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Logf("prepared in %s", rec.Dir)

	if plan.Tier == tier.Tier2 {
		branch, err := svc.Git.CurrentBranch(ctx, rec.Dir)
		if err != nil {
			t.Fatalf("CurrentBranch: %v", err)
		}
		info, err := svc.GH.PR(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		if branch != info.HeadRefName {
			t.Errorf("clone is on %q, want the head branch %q", branch, info.HeadRefName)
		}
		empty, err := svc.Git.WorkTreeEmpty(ctx, rec.Dir)
		if err != nil {
			t.Fatal(err)
		}
		if empty {
			t.Error("the clone has no files, so review-code would have nothing to read")
		}
	}

	done, err := svc.Abandon(rec)
	if err != nil {
		t.Fatalf("Abandon: %v", err)
	}
	if done.State != review.StateAbandoned {
		t.Errorf("state = %q, want abandoned", done.State)
	}
	if plan.Tier == tier.Tier2 {
		if _, err := os.Stat(rec.Dir); !os.IsNotExist(err) {
			t.Errorf("the clone at %s survived", rec.Dir)
		}
	}
}

func realService(t *testing.T) *Service {
	t.Helper()

	paths, err := config.NewPaths(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := paths.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(paths.Config)
	if err != nil {
		t.Fatal(err)
	}

	runner := exec.Runner(exec.Real{})
	gitCLI := git.New(runner)
	return &Service{
		Cfg:    cfg,
		Paths:  paths,
		Store:  index.New(paths.Index, paths.Lock),
		GH:     gh.New(runner),
		Git:    gitCLI,
		Cloner: clone.New(gitCLI, paths),
	}
}
