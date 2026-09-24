package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/tui/msg"
)

const draftURL = "https://github.com/haacked/docket/pull/7/files"

func browsingApp(runner *exec.Fake, screen msg.Screen) App {
	a := liveApp(draftedRecord())
	a.svc.Runner = runner
	a.screen = screen
	return a
}

// o works from the notes as well as the dashboard, so opening the browser
// leaves the user on the screen they pressed it from.
func TestOpeningOnGitHubRunsTheOpenerAndStaysOnTheScreen(t *testing.T) {
	t.Setenv("BROWSER", "")
	runner := &exec.Fake{}

	next, cmd := browsingApp(runner, msg.Notes).Update(msg.OpenOnGitHub{ID: draftedRecord().ID})
	a := next.(App)
	if cmd == nil {
		t.Fatal("opening on GitHub produced no command")
	}
	if got := cmd(); got != nil {
		t.Errorf("the command answered %#v, want nothing on success", got)
	}

	if a.screen != msg.Notes {
		t.Errorf("screen = %v, want the notes it was opened from", a.screen)
	}
	if !strings.Contains(a.status, draftURL) {
		t.Errorf("status = %q, want it to name %s", a.status, draftURL)
	}
	if lines := runner.Lines(); len(lines) != 1 || !strings.HasSuffix(lines[0], " "+draftURL) {
		t.Errorf("ran %q, want one command opening %s", lines, draftURL)
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
	rec := draftedRecord()

	next, cmd := dryRunApp(rec).Update(msg.OpenOnGitHub{ID: rec.ID})
	if cmd != nil {
		t.Errorf("a dry run ran a command: %#v", cmd())
	}

	status := next.(App).status
	for _, want := range []string{"Would run", draftURL} {
		if !strings.Contains(status, want) {
			t.Errorf("status = %q, want %q in it", status, want)
		}
	}
}
