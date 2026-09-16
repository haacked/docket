package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/haacked/docket/internal/core/config"
	"github.com/haacked/docket/internal/core/engine"
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

func backgroundRecord(state review.State) review.Record {
	return review.Record{
		ID:    "rec-1",
		Ref:   pr.Ref{Org: "haacked", Repo: "docket", Number: 7},
		Mode:  review.ModeBackground,
		BGID:  "6d681a76",
		State: state,
	}
}

// polled applies one poll result and reports whether the root armed another
// tick. The tick is the whole loop: without it the dashboard never notices a
// background review finishing.
func polled(t *testing.T, records []review.Record) (App, bool) {
	t.Helper()
	next, cmd := app().Update(bgPolledMsg{records: records, notes: map[string]string{}})
	return next.(App), cmd != nil
}

func TestThePollKeepsTickingWhileAReviewIsRunning(t *testing.T) {
	_, armed := polled(t, []review.Record{backgroundRecord(review.StateReviewing)})
	if !armed {
		t.Error("a running background review did not arm the next tick")
	}
}

// An idle docket runs no subprocesses. The tick stops when the last background
// review is over and starts again when one is launched.
func TestThePollStopsOnceNothingIsRunning(t *testing.T) {
	for _, state := range []review.State{review.StateDrafted, review.StateArchived, review.StateUnreviewed} {
		if _, armed := polled(t, []review.Record{backgroundRecord(state)}); armed {
			t.Errorf("a %s review kept the poll ticking", state)
		}
	}
	if _, armed := polled(t, nil); armed {
		t.Error("an empty dashboard kept the poll ticking")
	}
}

func TestAnInteractiveReviewIsNeverPolled(t *testing.T) {
	rec := backgroundRecord(review.StateReviewing)
	rec.Mode = review.ModeInteractive

	if _, armed := polled(t, []review.Record{rec}); armed {
		t.Error("an interactive review armed a background tick")
	}
}

func TestAPollPutsWhatTheSessionIsDoingOnTheRow(t *testing.T) {
	next, _ := app().Update(bgPolledMsg{
		records: []review.Record{backgroundRecord(review.StateReviewing)},
		notes:   map[string]string{"rec-1": "working busy"},
	})
	if got := next.(App).dash.Background["rec-1"]; got != "working busy" {
		t.Errorf("the row carries %q", got)
	}
}

// A listing that failed says nothing about the records, so they stay as they
// are and the tick that is already armed asks again.
func TestAFailedPollKeepsTheRecordsAndTheTick(t *testing.T) {
	first, cmd := app().Update(bgPolledMsg{records: []review.Record{backgroundRecord(review.StateReviewing)}})
	if cmd == nil {
		t.Fatal("the first poll armed no tick")
	}
	next, _ := first.(App).Update(bgPolledMsg{err: errTest})
	after := next.(App)

	if after.err == nil {
		t.Error("a failed poll said nothing to the user")
	}
	if len(after.dash.Records) != 1 {
		t.Errorf("a failed poll dropped the records: %+v", after.dash.Records)
	}
	if !after.polling {
		t.Error("a failed poll dropped the tick")
	}
}

// Several things ask for a poll: startup, a start, a record that went back to
// running, and the tick itself. Arming a tick per answer would leave every one
// of those chains re-arming itself, and each tick costs a subprocess and a full
// read of the index.
func TestPollsShareOneTickChain(t *testing.T) {
	running := []review.Record{backgroundRecord(review.StateReviewing)}

	first, cmd := app().Update(bgPolledMsg{records: running})
	if cmd == nil {
		t.Fatal("the first poll armed no tick")
	}
	if _, second := first.(App).Update(bgPolledMsg{records: running}); second != nil {
		t.Error("a second poll forked another tick chain")
	}
}

// The chain is released as the tick fires, so the poll it asks for arms the
// next one. Releasing it anywhere else would end the chain after one tick.
func TestTheTickHandsTheChainOn(t *testing.T) {
	running := []review.Record{backgroundRecord(review.StateReviewing)}
	armed, _ := app().Update(bgPolledMsg{records: running})

	ticked, cmd := armed.(App).Update(bgTickMsg{})
	if cmd == nil {
		t.Fatal("the tick asked for no poll")
	}
	if ticked.(App).polling {
		t.Error("the tick did not release the chain")
	}
	if _, next := ticked.(App).Update(bgPolledMsg{records: running}); next == nil {
		t.Error("the poll after a tick armed no tick, so the chain stopped")
	}
}

// Leaving a session that is still working puts the record back to running.
// Nothing else would start watching it again, so the detection that follows the
// exit has to.
func TestLeavingAWorkingSessionStartsWatchingItAgain(t *testing.T) {
	_, cmd := app().Update(detectedMsg{record: backgroundRecord(review.StateReviewing)})
	if cmd == nil {
		t.Error("a record that went back to running armed no poll")
	}
}

func TestNotesForJoinsWhatTheAgentReported(t *testing.T) {
	notes := notesFor(map[string]engine.BGStatus{
		"a": {State: "working", Activity: "busy"},
		"b": {State: "done"},
		"c": {},
	})
	if notes["a"] != "working busy" {
		t.Errorf("a = %q", notes["a"])
	}
	if notes["b"] != "done" {
		t.Errorf("b = %q, want no trailing separator", notes["b"])
	}
	if notes["c"] != "" {
		t.Errorf("c = %q, want nothing when the agent said nothing", notes["c"])
	}
}

var errTest = &stringErr{"claude is not on PATH"}

// A start that failed before reporting an id leaves a row that reads as running
// with no session behind it. Ticking over it forever is the thing to avoid.
func TestAReviewThatNeverStartedDoesNotKeepThePollTicking(t *testing.T) {
	rec := backgroundRecord(review.StateReviewing)
	rec.BGID = ""

	if _, armed := polled(t, []review.Record{rec}); armed {
		t.Error("a review with no session armed the next tick")
	}
}

// The poll is the one thing that runs on a timer rather than on a keystroke, so
// an error it left behind would sit over every status line until the user
// happened to change screens.
func TestAPollThatWorksClearsTheLastFailure(t *testing.T) {
	running := []review.Record{backgroundRecord(review.StateReviewing)}
	failed, _ := app().Update(bgPolledMsg{err: errTest})
	if failed.(App).err == nil {
		t.Fatal("a failed poll said nothing to the user")
	}

	next, _ := failed.(App).Update(bgPolledMsg{records: running})
	if next.(App).err != nil {
		t.Errorf("the error survived a poll that worked: %v", next.(App).err)
	}
}
