package tui

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/haacked/docket/internal/core/config"
	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/session"
	"github.com/haacked/docket/internal/tui/msg"
	"github.com/haacked/docket/internal/tui/screens/help"
)

const flags = "PostHog/team-feature-flags"

// userTeams is gh's answer to user/teams: flags on one page and a second team
// on the next.
const userTeams = `[[{"slug":"team-feature-flags","organization":{"login":"PostHog"}}],` +
	`[{"slug":"founders","organization":{"login":"aseriousbiz"}}]]`

// teamsApp is the root over a real service whose config names flags, with gh
// answering user/teams.
func teamsApp(t *testing.T, dryRun bool) (App, *session.Service, *exec.Fake) {
	t.Helper()
	svc, runner := batchService(t)
	svc.Cfg.Teams = []string{flags}
	runner.Results["user/teams"] = exec.Result{Stdout: userTeams}
	return New(svc, svc.Cfg, "", dryRun), svc, runner
}

// onTeams is the teams screen waiting on the root to save, the way enter leaves
// it.
func onTeams(a App) App {
	a.screen = msg.Teams
	a.teams = a.teams.Load([]string{flags})
	a.teams.Busy = true
	return a
}

func TestOpeningTheTeamsScreenChecksTheConfiguredTeams(t *testing.T) {
	a, _, _ := teamsApp(t, false)

	next, cmd := a.Update(msg.OpenTeams{})
	a = next.(App)

	if a.screen != msg.Teams {
		t.Errorf("screen = %v, want the teams screen", a.screen)
	}
	if !a.teams.Loading {
		t.Error("the teams screen is not waiting on GitHub")
	}
	if want := []string{flags}; !slices.Equal(a.teams.Checked(), want) {
		t.Errorf("checked = %v, want %v", a.teams.Checked(), want)
	}
	if cmd == nil {
		t.Error("opening the teams screen did not read GitHub")
	}
}

// The service's config is the one a save writes to. The copy the root took at
// startup is stale after the first save.
func TestTheTeamsScreenChecksTheTeamsTheServiceHolds(t *testing.T) {
	svc, _ := batchService(t)
	svc.Cfg.Teams = []string{flags}
	a := New(svc, config.Config{DefaultEngine: "claude", Teams: []string{"PostHog/old"}}, "", false)

	next, _ := a.Update(msg.OpenTeams{})

	if want := []string{flags}; !slices.Equal(next.(App).teams.Checked(), want) {
		t.Errorf("checked = %v, want %v", next.(App).teams.Checked(), want)
	}
}

func TestTheTeamsScreenWithoutAServiceChecksTheConfig(t *testing.T) {
	a := New(nil, config.Config{DefaultEngine: "claude", Teams: []string{flags}}, "", false)

	next, _ := a.Update(msg.OpenTeams{})

	if want := []string{flags}; !slices.Equal(next.(App).teams.Checked(), want) {
		t.Errorf("checked = %v, want %v", next.(App).teams.Checked(), want)
	}
}

func TestOpeningTheTeamsScreenReadsTheUsersTeamsFromGitHub(t *testing.T) {
	a, _, _ := teamsApp(t, false)

	_, cmd := a.Update(msg.OpenTeams{})

	got := only[teamsLoadedMsg](t, cmd)
	if want := []string{flags, "aseriousbiz/founders"}; !slices.Equal(got.teams, want) || got.err != nil {
		t.Errorf("loaded = %+v, want %v", got, want)
	}
}

// The configured teams stay on the screen when GitHub will not list the user's
// teams, so the failure goes to the screen rather than to the status line.
func TestAFailedReadOfTheUsersTeamsAnswersTheTeamsScreen(t *testing.T) {
	a, _, runner := teamsApp(t, false)
	runner.Errs = map[string]error{"user/teams": errors.New("HTTP 403")}

	_, cmd := a.Update(msg.OpenTeams{})

	if got := only[teamsLoadedMsg](t, cmd); got.err == nil {
		t.Errorf("loaded = %+v, want the failure", got)
	}
}

func TestTheUsersTeamsReachTheTeamsScreen(t *testing.T) {
	next, _ := New(nil, config.Config{DefaultEngine: "claude", Teams: []string{flags}}, "", false).Update(msg.OpenTeams{})

	next, _ = next.(App).Update(teamsLoadedMsg{teams: []string{"aseriousbiz/founders", flags}})
	a := next.(App)

	if a.teams.Loading || len(a.teams.Rows) != 2 {
		t.Errorf("loading = %v, rows = %+v, want both teams listed", a.teams.Loading, a.teams.Rows)
	}
	if want := []string{flags}; !slices.Equal(a.teams.Checked(), want) {
		t.Errorf("checked = %v, want %v", a.teams.Checked(), want)
	}
}

func TestAFailedReadReachesTheTeamsScreen(t *testing.T) {
	failure := errors.New("HTTP 403")
	next, _ := New(nil, config.Config{DefaultEngine: "claude", Teams: []string{flags}}, "", false).Update(msg.OpenTeams{})

	next, _ = next.(App).Update(teamsLoadedMsg{err: failure})
	a := next.(App)

	if !errors.Is(a.teams.Err, failure) {
		t.Errorf("the teams screen's err = %v, want %v", a.teams.Err, failure)
	}
	if want := []string{flags}; !slices.Equal(a.teams.Checked(), want) {
		t.Errorf("checked = %v, want the configured teams kept", a.teams.Checked())
	}
}

func TestKeysOnTheTeamsScreenReachIt(t *testing.T) {
	next, _ := New(nil, config.Config{DefaultEngine: "claude", Teams: []string{flags}}, "", false).Update(msg.OpenTeams{})
	if checked := next.(App).teams.Checked(); !slices.Equal(checked, []string{flags}) {
		t.Fatalf("checked = %v, want %s before the key", checked, flags)
	}

	next, _ = next.(App).Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})

	if checked := next.(App).teams.Checked(); len(checked) != 0 {
		t.Errorf("checked = %v, want space to have unchecked %s", checked, flags)
	}
}

func TestEnterOnTheTeamsScreenAsksTheRootToSave(t *testing.T) {
	next, _ := New(nil, config.Config{DefaultEngine: "claude", Teams: []string{flags}}, "", false).Update(msg.OpenTeams{})

	_, cmd := next.(App).Update(press("\r"))

	if got := only[msg.SaveTeams](t, cmd); !slices.Equal(got.Teams, []string{flags}) {
		t.Errorf("teams = %v, want %v", got.Teams, []string{flags})
	}
}

func TestSavingTheTeamsWritesThemToConfig(t *testing.T) {
	a, svc, _ := teamsApp(t, false)
	want := []string{"aseriousbiz/founders", flags}

	_, cmd := onTeams(a).Update(msg.SaveTeams{Teams: want})

	saved := only[teamsSavedMsg](t, cmd)
	if !slices.Equal(saved.teams, want) {
		t.Errorf("saved = %v, want %v", saved.teams, want)
	}
	cfg, err := config.Load(svc.Paths.Config)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.Teams, want) {
		t.Errorf("config.toml teams = %v, want %v", cfg.Teams, want)
	}
}

func TestASavedTeamsListReturnsToTheRequestsScreen(t *testing.T) {
	next, _ := onTeams(app()).Update(teamsSavedMsg{teams: []string{flags}})
	a := next.(App)

	if a.screen != msg.Requests {
		t.Errorf("screen = %v, want the requests screen", a.screen)
	}
	if a.teams.Busy {
		t.Error("the teams screen is still busy after the save")
	}
}

func TestASavedTeamsListSearchesAgain(t *testing.T) {
	next, cmd := onTeams(app()).Update(teamsSavedMsg{teams: []string{flags}})

	if !next.(App).reqs.Loading || cmd == nil {
		t.Errorf("loading = %v, cmd = %v, want a new search", next.(App).reqs.Loading, cmd != nil)
	}
}

func TestASavedTeamsListSaysSo(t *testing.T) {
	next, _ := onTeams(app()).Update(teamsSavedMsg{teams: []string{flags}})

	if view := next.(App).View().Content; !strings.Contains(view, "Saved") {
		t.Errorf("the view does not say the teams were saved:\n%s", view)
	}
}

func TestTheSearchAfterASaveLooksForTheSavedTeams(t *testing.T) {
	a, svc, runner := teamsApp(t, false)
	svc.Cfg.GitHubUser = "haacked"
	runner.Results["search/issues"] = exec.Result{Stdout: `[{"items":[]}]`}
	next, cmd := onTeams(a).Update(msg.SaveTeams{Teams: []string{"aseriousbiz/founders"}})
	saved := only[teamsSavedMsg](t, cmd)

	_, cmd = next.(App).Update(saved)

	var searched []string
	for _, m := range drain(cmd) {
		if loaded, ok := m.(requestsLoadedMsg); ok {
			for _, team := range loaded.fetched.Teams {
				searched = append(searched, team.Slug)
			}
		}
	}
	if want := []string{"aseriousbiz/founders"}; !slices.Equal(searched, want) {
		t.Errorf("searched teams = %v, want %v", searched, want)
	}
}

func TestAFailedSaveKeepsTheTeamsScreen(t *testing.T) {
	a, svc, _ := teamsApp(t, false)
	svc.Paths.Config = filepath.Join(t.TempDir(), "missing", "config.toml")
	a = onTeams(a)
	_, cmd := a.Update(msg.SaveTeams{Teams: []string{flags}})
	failed := only[errMsg](t, cmd)

	next, _ := a.Update(failed)
	a = next.(App)

	if a.screen != msg.Teams {
		t.Errorf("screen = %v, want the teams screen kept", a.screen)
	}
	if a.teams.Busy {
		t.Error("the teams screen is still busy, so enter cannot save again")
	}
}

func TestADryRunSaveWritesNothing(t *testing.T) {
	a, svc, _ := teamsApp(t, true)

	_, cmd := onTeams(a).Update(msg.SaveTeams{Teams: []string{"aseriousbiz/founders"}})
	drain(cmd)

	if _, err := os.Stat(svc.Paths.Config); !os.IsNotExist(err) {
		t.Errorf("stat config.toml = %v, want it never written", err)
	}
}

func TestADryRunSaveReturnsToTheRequestsScreen(t *testing.T) {
	a, _, _ := teamsApp(t, true)

	next, _ := onTeams(a).Update(msg.SaveTeams{Teams: []string{"aseriousbiz/founders"}})
	a = next.(App)

	if a.screen != msg.Requests || a.teams.Busy {
		t.Errorf("screen = %v, busy = %v, want the requests screen and nothing in flight", a.screen, a.teams.Busy)
	}
	if view := a.View().Content; !strings.Contains(view, "Would") {
		t.Errorf("the view does not say what the save would do:\n%s", view)
	}
}

func TestTheSpinnerTurnsWhileTheTeamsScreenWorks(t *testing.T) {
	tests := []struct {
		name  string
		setup func(App) App
		want  bool
	}{
		{"reading GitHub", func(a App) App {
			a.screen = msg.Teams
			a.teams.Loading = true
			return a
		}, true},
		{"saving", func(a App) App {
			a.screen = msg.Teams
			a.teams.Busy = true
			return a
		}, true},
		{"reading GitHub behind the dashboard", func(a App) App {
			a.teams.Loading = true
			return a
		}, false},
		{"idle", func(a App) App {
			a.screen = msg.Teams
			return a
		}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, cmd := nudge(tc.setup(app()))

			if a.spinning != tc.want {
				t.Errorf("spinning = %v, want %v", a.spinning, tc.want)
			}
			if armed := cmd != nil; armed != tc.want {
				t.Errorf("armed a tick = %v, want %v", armed, tc.want)
			}
		})
	}
}

func TestOpeningTheTeamsScreenStartsTheSpinner(t *testing.T) {
	a, _, _ := teamsApp(t, false)

	_, cmd := a.Update(msg.OpenTeams{})

	if !sendsTick(cmd) {
		t.Error("opening the teams screen did not start the spinner")
	}
}

func TestTheRootDrawsTheTeamsScreen(t *testing.T) {
	next, _ := New(nil, config.Config{DefaultEngine: "claude", Teams: []string{flags}}, "", false).Update(msg.OpenTeams{})

	if view := next.(App).View().Content; !strings.Contains(view, flags) {
		t.Errorf("the view does not show the configured team:\n%s", view)
	}
}

func TestTheTeamsScreensReadCarriesTheSpinnersFrame(t *testing.T) {
	next, _ := app().Update(msg.OpenTeams{})
	a, _ := spun(next.(App))

	want := busyLine(a, "Reading your teams from GitHub")
	if content := ansi.Strip(a.View().Content); !strings.Contains(content, want) {
		t.Errorf("the view does not read %q:\n%s", want, content)
	}
}

func TestTheTeamsFooterListsItsKeys(t *testing.T) {
	entries := helpFor(msg.Teams, false, false)

	var keys []string
	for _, e := range entries {
		keys = append(keys, e.Key)
	}
	for _, want := range []string{"space", "enter", "esc"} {
		if !slices.Contains(keys, want) {
			t.Errorf("footer keys = %v, missing %q", keys, want)
		}
	}
	if entries[len(entries)-1] != help.Quit {
		t.Errorf("the footer ends with %+v, want %+v", entries[len(entries)-1], help.Quit)
	}
}

func TestTheRequestsFooterListsTheTeamsKey(t *testing.T) {
	entries := helpFor(msg.Requests, false, false)

	if !slices.ContainsFunc(entries, func(e help.Entry) bool { return e.Key == "t" }) {
		t.Errorf("footer = %+v, want t", entries)
	}
}

// Saving the teams searches again, so the teams screen waits for the search
// that is running.
func TestTheTeamsScreenWaitsForASearch(t *testing.T) {
	a := app()
	a.screen = msg.Requests
	a.reqs.Loading = true

	next, cmd := a.Update(msg.OpenTeams{})
	a = next.(App)

	if a.screen != msg.Requests {
		t.Errorf("screen = %v, want the requests screen", a.screen)
	}
	if drain(cmd) != nil {
		t.Error("the refusal read GitHub")
	}
	if !strings.Contains(a.View().Content, "Press t again once it finishes") {
		t.Errorf("the view does not say why t did nothing:\n%s", a.View().Content)
	}
}

// A batch check's answer switches to the requests screen, which would take the
// user off the teams screen.
func TestTheTeamsScreenWaitsForABatchCheck(t *testing.T) {
	a := app()
	a.screen = msg.Requests
	a.reqs.Busy = true

	next, _ := a.Update(msg.OpenTeams{})
	a = next.(App)

	if a.screen != msg.Requests {
		t.Errorf("screen = %v, want the requests screen", a.screen)
	}
	if !strings.Contains(a.View().Content, "Press t again once it finishes") {
		t.Errorf("the view does not say why t did nothing:\n%s", a.View().Content)
	}
}

// esc leaves the teams screen while GitHub is still answering. The answer
// would land on a screen opened after it.
func TestTheTeamsScreenWaitsForItsOwnRead(t *testing.T) {
	a := app()
	a.screen = msg.Requests
	a.teams = a.teams.Load([]string{flags})

	next, cmd := a.Update(msg.OpenTeams{})
	a = next.(App)

	if a.screen != msg.Requests {
		t.Errorf("screen = %v, want the requests screen", a.screen)
	}
	if drain(cmd) != nil {
		t.Error("the refusal read GitHub again")
	}
	if !strings.Contains(a.View().Content, "Press t again once it finishes") {
		t.Errorf("the view does not say why t did nothing:\n%s", a.View().Content)
	}
}

func TestTheTeamsScreenWaitsForItsOwnSave(t *testing.T) {
	a := app()
	a.screen = msg.Requests
	a.teams.Busy = true

	next, _ := a.Update(msg.OpenTeams{})
	a = next.(App)

	if a.screen != msg.Requests {
		t.Errorf("screen = %v, want the requests screen", a.screen)
	}
	if !strings.Contains(a.View().Content, "Press t again once it finishes") {
		t.Errorf("the view does not say why t did nothing:\n%s", a.View().Content)
	}
}

// esc lets the user leave the teams screen while the save runs.
func TestASaveThatFinishesElsewhereLeavesTheScreenAlone(t *testing.T) {
	a := onTeams(app())
	a.screen = msg.Dashboard

	next, _ := a.Update(teamsSavedMsg{teams: []string{flags}})
	a = next.(App)

	if a.screen != msg.Dashboard {
		t.Errorf("screen = %v, want the dashboard the user went to", a.screen)
	}
	if a.teams.Busy {
		t.Error("the teams screen is still busy after the save")
	}
}
