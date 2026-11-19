package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/requests"
	"github.com/haacked/docket/internal/tui/msg"
)

// onRequests is the requests screen with the candidates turned on or off.
func onRequests(rereview bool) App {
	a := app()
	a.screen = msg.Requests
	a.reqs.ShowRereview = rereview
	return a
}

// p can turn the candidates on while a search without them runs. That search's
// answer then searches once more, for the candidates.
func TestAnAnswerWithoutCandidatesSearchesAgainWhileTheyShow(t *testing.T) {
	svc, runner := batchService(t)
	svc.Cfg.GitHubUser = "haacked"
	runner.Results["search/issues"] = exec.Result{Stdout: `[{"items":[]}]`}
	a := New(svc, svc.Cfg, "", false)
	a.screen = msg.Requests
	a.reqs.ShowRereview = true

	next, cmd := a.Update(requestsLoadedMsg{fetched: requests.Fetched{Login: "haacked"}})

	if !next.(App).reqs.Loading {
		t.Error("the screen is not waiting on the second search")
	}
	// Update adds the spinner's tick to any command that leaves the screen
	// busy, so only the message the command sends shows a second search ran.
	if loaded := only[requestsLoadedMsg](t, cmd); loaded.fetched.Rereview == nil {
		t.Error("the answer did not search again for the candidates")
	}
}

func TestAnAnswerStartsNoSecondSearch(t *testing.T) {
	tests := []struct {
		name    string
		app     App
		fetched requests.Fetched
	}{
		{"with the candidates off", onRequests(false), requests.Fetched{Login: "haacked"}},
		{"on another screen", func() App {
			a := onRequests(true)
			a.screen = msg.Dashboard
			return a
		}(), requests.Fetched{Login: "haacked"}},
		// Requests always sets Rereview when asked for candidates, so a second
		// search would answer the same way and the root would loop.
		{"when it looked for candidates and found none", onRequests(true), requests.Fetched{Login: "haacked", Rereview: &requests.Rereview{}}},
		{"when the look for candidates failed", onRequests(true), requests.Fetched{Login: "haacked", Rereview: &requests.Rereview{Errs: []error{errors.New("rate limited")}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next, cmd := tt.app.Update(requestsLoadedMsg{fetched: tt.fetched})

			if next.(App).reqs.Loading || cmd != nil {
				t.Errorf("loading = %v, command = %v, want no second search", next.(App).reqs.Loading, cmd != nil)
			}
		})
	}
}

// The search for candidates is the only one that runs the reviewed-by search
// with no team configured, so its presence shows the toggle reached Requests.
func TestASearchLooksForCandidatesOnlyWhileTheyShow(t *testing.T) {
	for _, rereview := range []bool{false, true} {
		t.Run(map[bool]string{false: "off", true: "on"}[rereview], func(t *testing.T) {
			svc, runner := batchService(t)
			svc.Cfg.GitHubUser = "haacked"
			runner.Results["search/issues"] = exec.Result{Stdout: `[{"items":[]}]`}
			a := New(svc, svc.Cfg, "", false)
			a.screen = msg.Requests
			a.reqs.ShowRereview = rereview

			_, cmd := a.Update(msg.RefreshRequests{})

			loaded := only[requestsLoadedMsg](t, cmd)
			if got := loaded.fetched.Rereview != nil; got != rereview {
				t.Errorf("looked for candidates = %v, want %v", got, rereview)
			}
			if got := strings.Contains(strings.Join(runner.Lines(), "\n"), "reviewed-by:haacked"); got != rereview {
				t.Errorf("searched for reviewed pull requests = %v, want %v in:\n%s", got, rereview, strings.Join(runner.Lines(), "\n"))
			}
		})
	}
}

// p on a list that has no candidates asks for a search, and that search looks
// for them.
func TestPOnTheRequestsScreenSearchesForTheCandidates(t *testing.T) {
	svc, runner := batchService(t)
	svc.Cfg.GitHubUser = "haacked"
	runner.Results["search/issues"] = exec.Result{Stdout: `[{"items":[]}]`}
	next, _ := New(svc, svc.Cfg, "", false).Update(requestsLoadedMsg{fetched: requests.Fetched{Login: "haacked"}})
	a := next.(App)
	a.screen = msg.Requests

	next, cmd := a.Update(press("p"))
	refresh := only[msg.RefreshRequests](t, cmd)
	_, cmd = next.(App).Update(refresh)

	if loaded := only[requestsLoadedMsg](t, cmd); loaded.fetched.Rereview == nil {
		t.Error("the search after p did not look for candidates")
	}
}
