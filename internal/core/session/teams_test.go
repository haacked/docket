package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/haacked/docket/internal/core/config"
)

func TestTeamsListsTheTeamsGitHubReports(t *testing.T) {
	fake := &fakeGH{login: "haacked", teams: []string{"aseriousbiz/founders", "PostHog/team-feature-flags"}}
	svc, _ := newService(t, fake, newFakeGit())

	teams, err := svc.Teams(context.Background())
	if err != nil {
		t.Fatalf("Teams: %v", err)
	}

	if !slices.Equal(teams, fake.teams) {
		t.Errorf("teams = %v, want %v", teams, fake.teams)
	}
}

func TestTeamsPassesOnGitHubsFailure(t *testing.T) {
	failure := errors.New("HTTP 403")
	svc, _ := newService(t, &fakeGH{login: "haacked", teamsErr: failure}, newFakeGit())

	if _, err := svc.Teams(context.Background()); !errors.Is(err, failure) {
		t.Errorf("error = %v, want %v", err, failure)
	}
}

func TestSaveTeamsWritesTheTeamsToConfig(t *testing.T) {
	svc, paths := newService(t, &fakeGH{login: "haacked"}, newFakeGit())
	want := []string{"aseriousbiz/founders", "PostHog/team-feature-flags"}

	if err := svc.SaveTeams(want); err != nil {
		t.Fatalf("SaveTeams: %v", err)
	}

	cfg, err := config.Load(paths.Config)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.Teams, want) {
		t.Errorf("config.toml teams = %v, want %v", cfg.Teams, want)
	}
}

func TestSaveTeamsKeepsTheOtherSettings(t *testing.T) {
	svc, paths := newService(t, &fakeGH{login: "haacked"}, newFakeGit())
	if err := os.WriteFile(paths.Config, []byte("github_user = \"haacked\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := svc.SaveTeams([]string{"PostHog/team-feature-flags"}); err != nil {
		t.Fatalf("SaveTeams: %v", err)
	}

	cfg, err := config.Load(paths.Config)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitHubUser != "haacked" {
		t.Errorf("github_user = %q, want the cached login kept", cfg.GitHubUser)
	}
}

// The service's Config holds every default, expanded. A save of the teams must
// not write those into a file that leaves them out.
func TestSaveTeamsWritesNoOtherKey(t *testing.T) {
	svc, paths := newService(t, &fakeGH{login: "haacked"}, newFakeGit())

	if err := svc.SaveTeams([]string{"PostHog/team-feature-flags"}); err != nil {
		t.Fatalf("SaveTeams: %v", err)
	}

	data, err := os.ReadFile(paths.Config)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(data)); got != `teams = ["PostHog/team-feature-flags"]` {
		t.Errorf("config.toml =\n%s\nwant only the teams", got)
	}
}

// Requests reads the service's own config, so the next search looks for the
// saved teams without a restart.
func TestTheSearchAfterSaveTeamsLooksForTheSavedTeams(t *testing.T) {
	fake := &fakeGH{login: "haacked"}
	svc, _ := newService(t, fake, newFakeGit())
	svc.Cfg.Teams = []string{"PostHog/old"}

	if err := svc.SaveTeams([]string{"PostHog/team-feature-flags"}); err != nil {
		t.Fatalf("SaveTeams: %v", err)
	}
	if _, err := svc.Requests(context.Background()); err != nil {
		t.Fatalf("Requests: %v", err)
	}

	want := []string{"user-review-requested:haacked", teamQuery("PostHog/team-feature-flags")}
	if !slices.Equal(fake.searches, want) {
		t.Errorf("searches = %v, want %v", fake.searches, want)
	}
}

func TestSaveTeamsWithNoneCheckedClearsTheTeams(t *testing.T) {
	svc, paths := newService(t, &fakeGH{login: "haacked"}, newFakeGit())
	if err := os.WriteFile(paths.Config, []byte("teams = [\"PostHog/team-feature-flags\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := svc.SaveTeams(nil); err != nil {
		t.Fatalf("SaveTeams: %v", err)
	}

	cfg, err := config.Load(paths.Config)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Teams) != 0 {
		t.Errorf("config.toml teams = %v, want none", cfg.Teams)
	}
}

// The next search reads the service's teams. After a failed save they must still
// be the teams config.toml holds.
func TestSaveTeamsReportsAConfigItCannotWrite(t *testing.T) {
	svc := &Service{Paths: config.Paths{Config: filepath.Join(t.TempDir(), "missing", "config.toml")}}
	svc.Cfg.Teams = []string{"PostHog/old"}

	if err := svc.SaveTeams([]string{"PostHog/team-feature-flags"}); err == nil {
		t.Error("SaveTeams succeeded with no directory to write config.toml in")
	}
	if !slices.Equal(svc.Cfg.Teams, []string{"PostHog/old"}) {
		t.Errorf("teams = %v, want the old teams kept after a failed save", svc.Cfg.Teams)
	}
}

// The poll can cache the login while the teams screen saves the teams. Each
// write must keep the other's key.
func TestSaveTeamsAndLoginKeepEachOthersKeys(t *testing.T) {
	for i := range 20 {
		svc, paths := newService(t, &fakeGH{login: "haacked"}, newFakeGit())
		svc.Cfg.GitHubUser = ""
		want := []string{"PostHog/team-feature-flags"}

		var wg sync.WaitGroup
		wg.Go(func() {
			if _, err := svc.Login(context.Background()); err != nil {
				t.Errorf("Login: %v", err)
			}
		})
		wg.Go(func() {
			if err := svc.SaveTeams(want); err != nil {
				t.Errorf("SaveTeams: %v", err)
			}
		})
		wg.Wait()

		cfg, err := config.Load(paths.Config)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.GitHubUser != "haacked" || !slices.Equal(cfg.Teams, want) {
			t.Fatalf("run %d: github_user = %q, teams = %v, want both saved", i, cfg.GitHubUser, cfg.Teams)
		}
	}
}

// A search can start while the teams screen saves: enter, then esc and r before
// the save finishes. Run under -race, this test fails when Requests reads the
// teams without the lock.
func TestRequestsDuringSaveTeams(t *testing.T) {
	svc, _ := newService(t, &fakeGH{login: "haacked"}, newFakeGit())

	var wg sync.WaitGroup
	wg.Go(func() {
		if err := svc.SaveTeams([]string{"PostHog/team-feature-flags"}); err != nil {
			t.Errorf("SaveTeams: %v", err)
		}
	})
	wg.Go(func() {
		if _, err := svc.Requests(context.Background()); err != nil {
			t.Errorf("Requests: %v", err)
		}
	})
	wg.Wait()
}
