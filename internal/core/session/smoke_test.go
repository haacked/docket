package session

import (
	"context"
	"os"
	"strings"
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

	plan, spec, err := svc.Explain(ctx, ref, "claude", review.ModeInteractive)
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	t.Logf("%s is %s: %s", ref, plan.Tier, plan.Description())
	t.Logf("would run: %s", spec)

	rec, _, err := svc.Prepare(ctx, ref, "claude", review.ModeInteractive)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Logf("prepared in %s", rec.Dir)

	if plan.Tier == tier.Tier1 {
		assertTier1(t, ctx, svc, rec, ref)
	}

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

	done, err := svc.Abandon(context.Background(), rec)
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
	if plan.Tier == tier.Tier1 {
		// cleanup deletes tier-2 clones only. Scratch is shared across reviews, so
		// losing it to one abandon would take the other records' directory with it.
		if _, err := os.Stat(svc.Paths.Scratch); err != nil {
			t.Errorf("abandoning a tier-1 review removed the scratch directory: %v", err)
		}
	}
}

// assertTier1 checks what a fake cannot: that docket supplied no repository of
// its own, and that the directory it does hand review-code sends review-code
// looking in repos.conf rather than at a remote of docket's making.
func assertTier1(t *testing.T, ctx context.Context, svc *Service, rec review.Record, ref pr.Ref) {
	t.Helper()

	if rec.Dir != svc.Paths.Scratch {
		t.Errorf("tier-1 review runs in %s, want the scratch directory %s", rec.Dir, svc.Paths.Scratch)
	}
	if _, err := os.Stat(svc.Cloner.Dir(ref)); !os.IsNotExist(err) {
		t.Errorf("docket cloned %s for a repository review-code already knows", svc.Cloner.Dir(ref))
	}

	// ensureScratch leaves the directory without a remote on purpose. review-code
	// asks the working directory for its org and repo, and a remote here would
	// answer with docket's own, which sends it down the in-repo path for the
	// wrong repository instead of the cross-repo path through repos.conf.
	res, err := exec.Real{}.Run(ctx, exec.CommandSpec{Path: "git", Args: []string{"remote"}, Dir: rec.Dir})
	if err != nil {
		t.Fatalf("git remote in %s: %v", rec.Dir, err)
	}
	if got := strings.TrimSpace(res.Stdout); got != "" {
		t.Errorf("the scratch directory has remote %q, so review-code would read the wrong repository", got)
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
