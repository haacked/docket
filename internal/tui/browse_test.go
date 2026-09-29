package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/tui/msg"
)

const (
	draftURL = "https://github.com/haacked/docket/pull/7#pullrequestreview-4321"
	pullURL  = "https://github.com/haacked/docket/pull/9"
)

func browsingApp(runner *exec.Fake, screen msg.Screen) App {
	a := liveApp(draftedRecord())
	a.svc.Runner = runner
	a.screen = screen
	return a
}

var opens = []struct {
	name    string
	screen  msg.Screen
	message tea.Msg
	url     string
}{
	{"a record's review", msg.Notes, msg.OpenOnGitHub{ID: draftedRecord().ID}, draftURL},
	{"a pull request", msg.Requests, msg.OpenPullRequest{URL: pullURL}, pullURL},
}

// o works from several screens. Opening the browser leaves the user on the
// screen they pressed it from.
func TestOpeningOnGitHubRunsTheOpenerAndStaysOnTheScreen(t *testing.T) {
	t.Setenv("BROWSER", "")

	for _, tc := range opens {
		t.Run(tc.name, func(t *testing.T) {
			runner := &exec.Fake{}

			next, cmd := browsingApp(runner, tc.screen).Update(tc.message)
			a := next.(App)
			if cmd == nil {
				t.Fatal("opening on GitHub produced no command")
			}
			if got := cmd(); got != nil {
				t.Errorf("the command answered %#v, want nothing on success", got)
			}

			if a.screen != tc.screen {
				t.Errorf("screen = %v, want %v, the screen it was opened from", a.screen, tc.screen)
			}
			if !strings.Contains(a.status, tc.url) {
				t.Errorf("status = %q, want it to name %s", a.status, tc.url)
			}
			if lines := runner.Lines(); len(lines) != 1 || !strings.HasSuffix(lines[0], " "+tc.url) {
				t.Errorf("ran %q, want one command opening %s", lines, tc.url)
			}
		})
	}
}

func TestAnOpenerThatFailsIsReported(t *testing.T) {
	t.Setenv("BROWSER", "")
	runner := &exec.Fake{Errs: map[string]error{"github.com": errors.New("no opener")}}

	_, cmd := browsingApp(runner, msg.Dashboard).Update(msg.OpenOnGitHub{ID: draftedRecord().ID})
	if cmd == nil {
		t.Fatal("opening on GitHub produced no command")
	}

	if _, ok := cmd().(errMsg); !ok {
		t.Error("a failed opener did not report an error")
	}
}

func TestADryRunOpensNoBrowser(t *testing.T) {
	t.Setenv("BROWSER", "")

	for _, tc := range opens {
		t.Run(tc.name, func(t *testing.T) {
			next, cmd := dryRunApp(draftedRecord()).Update(tc.message)
			if cmd != nil {
				t.Errorf("a dry run ran a command: %#v", cmd())
			}

			status := next.(App).status
			for _, want := range []string{"Would run", tc.url} {
				if !strings.Contains(status, want) {
					t.Errorf("status = %q, want %q in it", status, want)
				}
			}
		})
	}
}
