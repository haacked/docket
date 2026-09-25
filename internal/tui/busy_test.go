package tui

import (
	"slices"
	"strings"
	"testing"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/requests"
	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/tui/msg"
)

// nudge sends a message that does nothing on its own. The only command the root
// can then answer with is the spinner's first tick.
func nudge(a App) (App, tea.Cmd) {
	next, cmd := a.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	return next.(App), cmd
}

// spun hands the root the tick its spinner would receive next.
func spun(a App) (App, tea.Cmd) {
	next, cmd := a.Update(a.spin.Tick())
	return next.(App), cmd
}

// busyLine is what a line of work in flight reads as once the spinner draws its
// current frame in front of it.
func busyLine(a App, text string) string {
	return ansi.Strip(a.spin.View()) + " " + text + "…"
}

func TestTheSpinnerStartsOnlyForWorkOnTheScreenOnDisplay(t *testing.T) {
	tests := []struct {
		name  string
		setup func(App) App
		want  bool
	}{
		{"a busy row on the dashboard", func(a App) App {
			a.dash.Busy["rec-1"] = "refreshing"
			return a
		}, true},
		{"work the root is doing", func(a App) App {
			a.working = "Refreshing from GitHub"
			return a
		}, true},
		{"work the root is doing under the notes", func(a App) App {
			a.screen = msg.Notes
			a.working = "Refreshing from GitHub"
			return a
		}, true},
		{"the submit screen submitting", func(a App) App {
			a.screen = msg.Submit
			a.sub.Busy = "submitting"
			return a
		}, true},
		{"the new review screen resolving", func(a App) App {
			a.screen = msg.NewReview
			a.newrev.Busy = "resolving"
			return a
		}, true},
		{"the requests screen searching", func(a App) App {
			a.screen = msg.Requests
			a.reqs.Loading = true
			return a
		}, true},
		{"the new review screen busy behind the dashboard", func(a App) App {
			a.newrev.Busy = "preparing"
			return a
		}, false},
		{"the submit screen busy behind the dashboard", func(a App) App {
			a.sub.Busy = "submitting"
			return a
		}, false},
		{"the requests screen searching behind the dashboard", func(a App) App {
			a.reqs.Loading = true
			return a
		}, false},
		{"a busy row behind the submit screen", func(a App) App {
			a.screen = msg.Submit
			a.dash.Busy["rec-1"] = "submitting"
			return a
		}, false},
		{"nothing in flight", func(a App) App { return a }, false},
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

func TestTheSpinnersFirstCommandIsItsTick(t *testing.T) {
	a := app()
	a.dash.Busy["rec-1"] = "refreshing"

	_, cmd := nudge(a)

	if !sendsTick(cmd) {
		t.Error("starting the spinner did not send it a tick, so it never turns")
	}
}

// sendsTick reports whether cmd, or a command in the batch it returns, sends
// the spinner a tick. drain leaves ticks out and cannot answer this.
func sendsTick(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	switch out := cmd().(type) {
	case spinner.TickMsg:
		return true
	case tea.BatchMsg:
		return slices.ContainsFunc(out, sendsTick)
	}
	return false
}

func TestRefreshingEverythingStartsTheSpinner(t *testing.T) {
	next, cmd := app().Update(msg.RefreshRecords{})
	a := next.(App)

	if !a.spinning {
		t.Error("a refresh of every record did not start the spinner")
	}
	if cmd == nil {
		t.Error("a refresh of every record returned no command")
	}
}

// Several things mark work in flight. Each tick schedules the next one. A tick
// armed for each marker would therefore start one more chain, and every extra
// chain turns the spinner faster.
func TestTheSpinnerRunsOneTickChain(t *testing.T) {
	a := app()
	a.dash.Busy["rec-1"] = "refreshing"
	a, _ = nudge(a)

	a.dash.Busy["rec-2"] = "abandoning"
	if _, cmd := nudge(a); cmd != nil {
		t.Error("a second busy marker started a second tick chain")
	}
}

func TestATickWhileBusyTurnsTheSpinnerAndAsksForTheNext(t *testing.T) {
	a := app()
	a.dash.Busy["rec-1"] = "refreshing"
	a, _ = nudge(a)
	before := ansi.Strip(a.spin.View())

	a, cmd := spun(a)

	if cmd == nil {
		t.Error("a tick while busy asked for no next tick, so the spinner stops")
	}
	if !a.spinning {
		t.Error("a tick while busy released the chain")
	}
	if after := ansi.Strip(a.spin.View()); after == before {
		t.Errorf("the frame stayed %q after a tick", after)
	}
}

// An idle docket schedules nothing. The first tick after the work is done
// schedules no next tick. The next work in flight starts the ticks again.
func TestTheSpinnerStopsOnceNothingIsBusy(t *testing.T) {
	rec := review.Record{ID: "rec-1", Ref: pr.Ref{Org: "haacked", Repo: "docket", Number: 7}, State: review.StateDrafted}
	a := app()
	a.dash.Busy[rec.ID] = "refreshing"
	a, _ = nudge(a)
	next, _ := a.Update(detectedMsg{record: rec})
	a = next.(App)

	a, cmd := spun(a)

	if cmd != nil {
		t.Error("a tick after the work finished asked for another")
	}
	if a.spinning {
		t.Error("the chain lapsed but the root still thinks the spinner is turning")
	}
}

// The root hands each screen the same frame. Every line of work in flight
// therefore turns together.
func TestEveryLineOfWorkInFlightCarriesTheSpinnersFrame(t *testing.T) {
	rec := draftedRecord()
	tests := []struct {
		name  string
		setup func(App) App
		text  string
	}{
		{"a busy row", func(a App) App {
			a.dash.Busy[rec.ID] = "refreshing"
			return a
		}, "refreshing"},
		{"work the root is doing", func(a App) App {
			a.working = "Refreshing from GitHub"
			return a
		}, "Refreshing from GitHub"},
		{"the submit screen", func(a App) App {
			a.screen = msg.Submit
			a.sub = a.sub.For(rec, review.SubmitEvents)
			a.sub.Busy = "submitting"
			return a
		}, "submitting"},
		{"the new review screen", func(a App) App {
			a.screen = msg.NewReview
			a.newrev.Busy = "resolving"
			return a
		}, "resolving"},
		{"the requests screen", func(a App) App {
			a.screen = msg.Requests
			a.reqs.Loading = true
			return a
		}, "Searching GitHub for review requests"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := nudge(tc.setup(liveApp(rec)))
			a, _ = spun(a)

			want := busyLine(a, tc.text)
			if content := ansi.Strip(a.View().Content); !strings.Contains(content, want) {
				t.Errorf("the view does not read %q:\n%s", want, content)
			}
		})
	}
}

func TestRefreshingEverythingSaysSoWhileItRuns(t *testing.T) {
	next, _ := app().Update(msg.RefreshRecords{})
	a := next.(App)

	want := busyLine(a, "Refreshing from GitHub")
	if content := ansi.Strip(a.View().Content); !strings.Contains(content, want) {
		t.Errorf("the view does not read %q:\n%s", want, content)
	}
}

// An error takes the status line ahead of the work in flight. A refresh that
// left the last failure in place would therefore never show that it is running.
func TestRefreshingEverythingClearsTheLastError(t *testing.T) {
	a := app()
	a.err = errNotFound

	next, _ := a.Update(msg.RefreshRecords{})

	if content := next.(App).View().Content; strings.Contains(content, "not found") {
		t.Errorf("the last error is still shown during the refresh:\n%s", content)
	}
}

func TestWorkInFlightTakesThePlaceOfTheLastStatus(t *testing.T) {
	a := app()
	a.status = "haacked/docket#7 has a pending review"

	next, _ := a.Update(msg.RefreshRecords{})

	if content := next.(App).View().Content; strings.Contains(content, "pending review") {
		t.Errorf("the old status is shown beside the work in flight:\n%s", content)
	}
}

func TestAnErrorShowsOverWorkInFlight(t *testing.T) {
	a := app()
	a.working = "Refreshing from GitHub"
	a.err = errNotFound

	content := a.View().Content

	if !strings.Contains(content, "not found") {
		t.Errorf("the error is not shown:\n%s", content)
	}
	if strings.Contains(content, "Refreshing from GitHub") {
		t.Errorf("the work in flight is shown beside the error:\n%s", content)
	}
}

// The status line says when the refresh finishes. Without that, the user cannot
// tell whether it is still running.
func TestAFinishedRefreshSaysWhatItDid(t *testing.T) {
	next, _ := app().Update(msg.RefreshRecords{})

	next, _ = next.(App).Update(refreshedMsg{})
	a := next.(App)

	if content := a.View().Content; strings.Contains(content, "Refreshing from GitHub") {
		t.Errorf("the refresh still reads as running after it finished:\n%s", content)
	}
	if want := "Re-read GitHub for every record whose session is over"; a.status != want {
		t.Errorf("status = %q, want %q", a.status, want)
	}
}

func TestAFinishedRefreshShowsTheRecordsItRead(t *testing.T) {
	rec := draftedRecord()
	next, _ := app().Update(msg.RefreshRecords{})

	next, _ = next.(App).Update(refreshedMsg{records: []review.Record{rec}})

	if got, ok := next.(App).dash.Selected(); !ok || got.ID != rec.ID {
		t.Errorf("selected = %+v (ok=%v), want the record the refresh read", got, ok)
	}
}

// A review that the refresh archived leaves the requests screen's row without
// another search. Any other reload does the same.
func TestAFinishedRefreshRegroupsTheRequestsScreen(t *testing.T) {
	ref := pr.Ref{Org: "o", Repo: "r", Number: 1}
	next, _ := app().Update(requestsLoadedMsg{fetched: requests.Fetched{Mine: []requests.PR{{Ref: ref}}}})

	next, _ = next.(App).Update(refreshedMsg{records: []review.Record{{ID: "a", Ref: ref, State: review.StateDrafted}}})

	if row, ok := next.(App).reqs.Selected(); !ok || row.State != review.StateDrafted {
		t.Errorf("row = %+v (ok=%v), want it to carry the refreshed record's state", row, ok)
	}
}

// refreshAll writes to the index partway through its own work. A stamp taken
// around it would call those writes already seen.
func TestAFinishedRefreshLeavesTheIndexStampAlone(t *testing.T) {
	a := app()
	a.indexStamp.Size = 42

	next, _ := a.Update(refreshedMsg{records: []review.Record{draftedRecord()}})

	if got := next.(App).indexStamp.Size; got != 42 {
		t.Errorf("indexStamp.Size = %d, want the stamp from before the refresh", got)
	}
}

func TestCheckingABatchSaysSoWhileItRuns(t *testing.T) {
	tests := []struct {
		urls []string
		want string
	}{
		{[]string{"https://github.com/haacked/docket/pull/7"}, "Checking 1 pull request for a review of yours"},
		{[]string{"https://github.com/haacked/docket/pull/7", "https://github.com/haacked/docket/pull/8"}, "Checking 2 pull requests for a review of yours"},
	}

	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			next, _ := app().Update(msg.StartBatch{URLs: tc.urls, Engine: "claude"})
			a := next.(App)

			want := busyLine(a, tc.want)
			if content := ansi.Strip(a.View().Content); !strings.Contains(content, want) {
				t.Errorf("the view does not read %q:\n%s", want, content)
			}
		})
	}
}

func checkedBatch(numbers ...int) batchCheckedMsg {
	checked := batchCheckedMsg{engine: "claude"}
	for _, n := range numbers {
		checked.items = append(checked.items, batchItem{ref: pr.Ref{Org: "haacked", Repo: "docket", Number: n}})
	}
	return checked
}

func TestStartingABatchSaysSoWhileItRuns(t *testing.T) {
	tests := []struct {
		numbers []int
		want    string
	}{
		{[]int{7}, "Starting 1 background review"},
		{[]int{7, 8}, "Starting 2 background reviews"},
	}

	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			a := app()
			a.working = "Checking the batch"

			next, _ := a.Update(checkedBatch(tc.numbers...))
			a = next.(App)

			want := busyLine(a, tc.want)
			if content := ansi.Strip(a.View().Content); !strings.Contains(content, want) {
				t.Errorf("the view does not read %q:\n%s", want, content)
			}
		})
	}
}

// explainBatch answers with a statusMsg, and a statusMsg clears nothing. A line
// of work set by a dry run would therefore stay on screen for good.
func TestADryRunBatchLeavesNothingInFlight(t *testing.T) {
	a := dryRunApp()
	a.working = "Checking the batch"

	next, _ := a.Update(checkedBatch(7))

	if got := next.(App).working; got != "" {
		t.Errorf("working = %q, want nothing in a dry run", got)
	}
}

func TestAFinishedBatchClearsTheLineOfWorkInFlight(t *testing.T) {
	next, _ := app().Update(msg.StartBatch{URLs: []string{"https://github.com/haacked/docket/pull/7"}, Engine: "claude"})

	next, _ = next.(App).Update(batchStartedMsg{started: 1})

	if content := next.(App).View().Content; strings.Contains(content, "Starting 1") {
		t.Errorf("the batch still reads as starting after it reported:\n%s", content)
	}
}

func TestTheWorksOwnFailureClearsTheLineOfWorkInFlight(t *testing.T) {
	tests := []struct {
		name    string
		message tea.Msg
	}{
		{"a refresh that failed", errMsg{err: errNotFound, endsWork: true}},
		{"a batch check that failed", batchCheckedMsg{err: errNotFound}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := app()
			a.working = "Refreshing from GitHub"

			next, _ := a.Update(tc.message)

			if got := next.(App).working; got != "" {
				t.Errorf("working = %q, want it cleared by the failure", got)
			}
		})
	}
}

// Clearing the line on an unrelated failure would hide the batch or refresh that
// still runs. It would also let R or another batch start beside it.
func TestAnUnrelatedFailureLeavesTheLineOfWorkInFlight(t *testing.T) {
	tests := []struct {
		name    string
		message tea.Msg
	}{
		{"an error", errMsg{err: errNotFound}},
		{"a background start that failed", bgStartFailedMsg{record: draftedRecord(), err: errNotFound}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := app()
			a.working = "Starting 3 background reviews"

			next, _ := a.Update(tc.message)

			if got := next.(App).working; got != "Starting 3 background reviews" {
				t.Errorf("working = %q, want the batch's line left alone", got)
			}
		})
	}
}

// Leaving the submit screen with esc while the submit runs must not hide that it
// is running. A user who cannot see it would press s again.
func TestARowBeingSubmittedSaysSoOnTheDashboard(t *testing.T) {
	rec := draftedRecord()
	next, _ := liveApp(rec).Update(msg.SubmitReview{ID: rec.ID, Event: review.EventComment})

	next, _ = next.(App).Update(msg.Goto{Screen: msg.Dashboard})

	if content := ansi.Strip(next.(App).View().Content); !strings.Contains(content, "submitting…") {
		t.Errorf("the row does not say it is being submitted:\n%s", content)
	}
}

func TestADryRunSubmitLeavesNoRowSubmitting(t *testing.T) {
	rec := draftedRecord()

	next, _ := dryRunApp(rec).Update(msg.SubmitReview{ID: rec.ID, Event: review.EventComment})

	if busy := next.(App).dash.Busy; len(busy) != 0 {
		t.Errorf("busy = %v, want no row marked: no detection follows a dry run to clear it", busy)
	}
}

// Reopening the submit screen while the first submit runs would post the same
// pending review twice.
func TestReopeningSubmitWhileTheReviewIsSubmittingIsRefused(t *testing.T) {
	rec := draftedRecord()
	next, _ := liveApp(rec).Update(msg.SubmitReview{ID: rec.ID, Event: review.EventComment})
	next, _ = next.(App).Update(msg.Goto{Screen: msg.Dashboard})

	next, _ = next.(App).Update(msg.OpenSubmit{ID: rec.ID})
	a := next.(App)

	if a.screen != msg.Dashboard {
		t.Errorf("screen = %v, want the dashboard still", a.screen)
	}
	if want := "haacked/docket#7 is still submitting"; a.status != want {
		t.Errorf("status = %q, want %q", a.status, want)
	}
}

func TestOpeningSubmitOnARowWithWorkInFlightIsRefused(t *testing.T) {
	rec := draftedRecord()
	a := liveApp(rec)
	a.dash.Busy[rec.ID] = "abandoning"

	next, _ := a.Update(msg.OpenSubmit{ID: rec.ID})
	a = next.(App)

	if a.screen != msg.Dashboard {
		t.Errorf("screen = %v, want the dashboard still", a.screen)
	}
	if want := "haacked/docket#7 is still abandoning"; a.status != want {
		t.Errorf("status = %q, want %q", a.status, want)
	}
}

// A status written while work runs, such as a refusal, has to show. Otherwise
// the key that caused it seems to do nothing.
func TestAStatusShowsBesideWorkInFlight(t *testing.T) {
	a := app()
	a.working = "Starting 3 background reviews"
	a.status = "haacked/docket#7 is still submitting"

	content := ansi.Strip(a.View().Content)

	for _, want := range []string{"Starting 3 background reviews…", "haacked/docket#7 is still submitting"} {
		if !strings.Contains(content, want) {
			t.Errorf("the view does not say %q:\n%s", want, content)
		}
	}
}

// A batch and a refresh of every record share the line of work in flight. The
// first to finish would clear it while the other still runs.
func TestWorkInFlightRefusesMoreWorkOfItsKind(t *testing.T) {
	tests := []struct {
		name    string
		message tea.Msg
	}{
		{"refresh", msg.RefreshRecords{}},
		{"batch", msg.StartBatch{URLs: []string{"https://github.com/haacked/docket/pull/7"}, Engine: "claude"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := app()
			a.working = "Starting 3 background reviews"

			next, cmd := a.Update(tc.message)
			a = next.(App)

			if len(drain(cmd)) != 0 {
				t.Error("the second piece of work started")
			}
			if a.working != "Starting 3 background reviews" {
				t.Errorf("working = %q, want the first work's line left alone", a.working)
			}
			if !strings.Contains(a.status, "still running") {
				t.Errorf("status = %q, want it to say the first work is still running", a.status)
			}
		})
	}
}

// An abandon that ran beside a submit could record a submitted review as
// abandoned, depending on which finished last.
func TestAbandonIsRefusedOnARowWithWorkInFlight(t *testing.T) {
	rec := draftedRecord()
	a := liveApp(rec)
	a.dash.Busy[rec.ID] = "submitting"

	next, cmd := a.Update(msg.Abandon{ID: rec.ID})
	a = next.(App)

	if len(drain(cmd)) != 0 {
		t.Error("the abandon ran while the submit was in flight")
	}
	if a.dash.Busy[rec.ID] != "submitting" {
		t.Errorf("busy = %q, want the submit's marker left alone", a.dash.Busy[rec.ID])
	}
	if want := "haacked/docket#7 is still submitting"; a.status != want {
		t.Errorf("status = %q, want %q", a.status, want)
	}
}

// A user who left the submit screen with esc may be on another screen when the
// submit answers, and switching away would drop what they were typing there.
func TestAFinishedSubmitLeavesAnotherScreenAlone(t *testing.T) {
	first, second := draftedRecord(), draftedRecord()
	second.ID, second.Ref.Number, second.ReviewID = "rec-2", 8, 2
	a := liveApp(first, second)
	next, _ := a.Update(msg.SubmitReview{ID: first.ID, Event: review.EventComment})
	next, _ = next.(App).Update(msg.OpenSubmit{ID: second.ID})
	a = next.(App)
	a.sub.Busy = ""

	next, _ = a.Update(detectedMsg{record: first, submitted: true})
	a = next.(App)

	if a.screen != msg.Submit || a.sub.Record.ID != second.ID {
		t.Errorf("screen = %v on %q, want the second record's submit screen still open", a.screen, a.sub.Record.ID)
	}
}
