package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/haacked/docket/internal/core/config"
	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/core/tier"
	"github.com/haacked/docket/internal/tui/msg"
)

func app() App {
	return New(nil, config.Config{DefaultEngine: "claude"}, "", false)
}

func press(s string) tea.KeyPressMsg {
	switch s {
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	case "\r":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	}
	return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
}

func quits(t *testing.T, cmd tea.Cmd) bool {
	t.Helper()
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func TestQuitKeysDependOnTheScreen(t *testing.T) {
	model, cmd := app().Update(press("q"))
	if !quits(t, cmd) {
		t.Error("q on the dashboard should quit")
	}
	_ = model

	onNewReview := app()
	next, _ := onNewReview.Update(msg.Goto{Screen: msg.NewReview})
	onNewReview = next.(App)

	_, cmd = onNewReview.Update(press("q"))
	if quits(t, cmd) {
		t.Error("q while typing a URL should not quit")
	}

	_, cmd = onNewReview.Update(press("ctrl+c"))
	if !quits(t, cmd) {
		t.Error("ctrl+c should quit from any screen")
	}
}

func TestGotoNewReviewClearsTheField(t *testing.T) {
	a := app()
	next, _ := a.Update(msg.Goto{Screen: msg.NewReview})
	a = next.(App)
	a.newrev = a.newrev.SetValue("o/r#1")

	next, _ = a.Update(msg.Goto{Screen: msg.Dashboard})
	next, _ = next.(App).Update(msg.Goto{Screen: msg.NewReview})
	a = next.(App)

	if got := a.newrev.Input.Value(); got != "" {
		t.Errorf("field = %q, want it cleared", got)
	}
}

func TestStatusReturnsToTheDashboard(t *testing.T) {
	a := app()
	next, _ := a.Update(msg.Goto{Screen: msg.NewReview})

	next, _ = next.(App).Update(statusMsg{text: "Would run: claude"})
	a = next.(App)

	if a.screen != msg.Dashboard {
		t.Error("a status message should land back on the dashboard")
	}
	if !strings.Contains(a.View().Content, "Would run: claude") {
		t.Error("the status is not shown")
	}
}

func TestViewKeepsTheAlternateScreenAndMouseModeConstant(t *testing.T) {
	a := app()

	for _, m := range []tea.Msg{tea.WindowSizeMsg{Width: 100, Height: 40}, msg.Goto{Screen: msg.NewReview}} {
		next, _ := a.Update(m)
		a = next.(App)
		view := a.View()
		if !view.AltScreen {
			t.Error("AltScreen must stay on so the restore after a session is predictable")
		}
		if view.MouseMode != tea.MouseModeNone {
			t.Errorf("MouseMode = %v, want none", view.MouseMode)
		}
	}
}

func TestErrorsAreShown(t *testing.T) {
	a := app()
	a.dash.Busy["rec-1"] = "refreshing"

	next, _ := a.Update(errMsg{err: errNotFound})
	a = next.(App)

	if !strings.Contains(a.View().Content, "not found") {
		t.Error("the error is not shown")
	}
	if len(a.dash.Busy) != 0 {
		t.Error("a failure should clear the rows that were marked busy")
	}
}

var errNotFound = &stringErr{"pull request not found"}

type stringErr struct{ s string }

func (e *stringErr) Error() string { return e.s }

func TestAFailureLeavesTheNewReviewScreenUsable(t *testing.T) {
	a := app()
	next, _ := a.Update(msg.Goto{Screen: msg.NewReview})
	a = next.(App)
	a.newrev = a.newrev.SetValue("haacked/docket#7")

	next, _ = a.Update(press("\r"))
	a = next.(App)
	if a.newrev.Busy == "" {
		t.Fatal("enter should mark the screen busy")
	}

	next, _ = a.Update(errMsg{err: errNotFound})
	a = next.(App)

	next, _ = a.Update(msg.Goto{Screen: msg.NewReview})
	a = next.(App)
	a.newrev = a.newrev.SetValue("haacked/docket#7")
	_, cmd := a.newrev.Update(press("\r"))
	if cmd == nil {
		t.Error("enter is dead after a failure")
	}
}

func TestTheCursorStaysOnAVisibleRow(t *testing.T) {
	a := app()
	records := []review.Record{
		{ID: "a", State: review.StateReviewing},
		{ID: "b", State: review.StateArchived},
		{ID: "c", State: review.StateArchived},
	}

	a.dash.Cursor = 2
	next, _ := a.Update(recordsLoadedMsg{records: records})
	a = next.(App)

	if _, ok := a.dash.Selected(); !ok {
		t.Errorf("cursor %d points at no row, with archived records hidden", a.dash.Cursor)
	}
}

func dryRunApp(records ...review.Record) App {
	a := New(nil, config.Config{DefaultEngine: "claude"}, "", true)
	a.dash = a.dash.SetRecords(records)
	return a
}

// A dry run must change nothing, and most of what it must not do is a write to the
// index or the filesystem rather than a command a runner could intercept. These
// are the actions that would have written.
func TestADryRunReportsInsteadOfActing(t *testing.T) {
	rec := review.Record{
		ID:    "rec-1",
		Ref:   pr.Ref{Org: "haacked", Repo: "docket", Number: 7},
		State: review.StateDrafted,
		Tier:  tier.Tier2,
		Dir:   "/tmp/clones/haacked/docket/pr-7",
	}

	tests := []struct {
		name    string
		message tea.Msg
		want    string
	}{
		{"abandon", msg.Abandon{ID: "rec-1"}, "Would abandon haacked/docket#7 and delete /tmp/clones/haacked/docket/pr-7"},
		{"refresh one", msg.RefreshRecords{ID: "rec-1"}, "Would re-read GitHub for haacked/docket#7"},
		{"refresh all", msg.RefreshRecords{}, "Would re-read GitHub for every record whose session is over"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			next, cmd := dryRunApp(rec).Update(tc.message)
			if cmd != nil {
				t.Errorf("%s ran a command in a dry run: %#v", tc.name, cmd())
			}
			if got := next.(App).status; got != tc.want {
				t.Errorf("status = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestADryRunSaysWhenThereIsNothingToDelete(t *testing.T) {
	rec := review.Record{ID: "rec-1", Ref: pr.Ref{Org: "posthog", Repo: "posthog", Number: 1}, State: review.StateDrafted, Tier: tier.Tier1}

	next, _ := dryRunApp(rec).Update(msg.Abandon{ID: "rec-1"})

	if got := next.(App).status; !strings.Contains(got, "created nothing to delete") {
		t.Errorf("status = %q, want it to say docket created nothing", got)
	}
}

func TestADryRunSaysSoInTheUI(t *testing.T) {
	if view := dryRunApp().View(); !strings.Contains(view.Content, "dry run") {
		t.Error("the dry run is not visible in the UI")
	}
	if view := app().View(); strings.Contains(view.Content, "dry run") {
		t.Error("a live run should not claim to be a dry run")
	}
}
