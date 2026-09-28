// Package tui is docket's terminal UI. It owns every command that touches the
// service, and it is the only package that hands a command to the terminal. It is
// also where a dry run stops. Most of what a dry run must not do is a write to the
// index or the filesystem, not a command a runner could intercept.
package tui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/haacked/docket/internal/core/config"
	"github.com/haacked/docket/internal/core/engine"
	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/index"
	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/requests"
	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/core/session"
	"github.com/haacked/docket/internal/core/tier"
	"github.com/haacked/docket/internal/tui/format"
	"github.com/haacked/docket/internal/tui/msg"
	"github.com/haacked/docket/internal/tui/screens/dashboard"
	"github.com/haacked/docket/internal/tui/screens/help"
	"github.com/haacked/docket/internal/tui/screens/inbox"
	"github.com/haacked/docket/internal/tui/screens/newreview"
	"github.com/haacked/docket/internal/tui/screens/notes"
	"github.com/haacked/docket/internal/tui/screens/submit"
	"github.com/haacked/docket/internal/tui/screens/teams"
)

// App is the root model.
type App struct {
	svc    *session.Service
	cfg    config.Config
	styles styles
	dryRun bool

	screen msg.Screen
	dash   dashboard.Model
	newrev newreview.Model
	sub    submit.Model
	notes  notes.Model
	help   help.Model
	reqs   inbox.Model
	teams  teams.Model
	// fetched is the last search for review requests. regroup reads it whenever
	// the records change.
	fetched *requests.Fetched
	// batch is a checked batch of background reviews that waits while the
	// requests screen asks what to do with the pull requests that already have
	// a review. esc on that question leaves it here. The next check replaces
	// it.
	batch *batchCheckedMsg

	width  int
	height int
	status string
	// working is work in flight that no screen shows a marker for, such as a
	// batch start or a refresh of every record. The status line shows it in
	// place of status until the work answers.
	working string
	err     error
	// spin draws every busy marker. spinning means a spinner tick is
	// outstanding. Each tick schedules the next one, so Update arms no second
	// tick while spinning is set.
	spin     spinner.Model
	spinning bool
	// polling means a background-session tick is outstanding. Several things ask
	// for a poll, and without this each answer would arm a tick of its own and
	// every one of them would re-arm itself forever.
	polling bool
	// indexStamp is the index file's state as of the last watch check, so the
	// next one can tell whether another docket process appended to it.
	indexStamp index.StatMark
}

// New builds the root model. A non-empty initialInput opens the new review screen
// with the field already filled.
func New(svc *session.Service, cfg config.Config, initialInput string, dryRun bool) App {
	s := newStyles()
	app := App{
		svc:    svc,
		cfg:    cfg,
		styles: s,
		dryRun: dryRun,
		spin:   spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		dash: dashboard.New(dashboard.Styles{
			Group:    s.Group,
			Row:      s.Row,
			Selected: s.Selected,
			Dim:      s.Dim,
			Err:      s.Err,
		}),
		newrev: newreview.New(
			newreview.Styles{Label: s.Label, Dim: s.Dim, Err: s.Err},
			engine.Names(),
			engine.BackgroundNames(),
			cfg.DefaultEngine,
			cfg.DefaultRepo,
			cfg.RunsInBackground(),
		),
		sub:   submit.New(submit.Styles{Label: s.Label, Dim: s.Dim, Selected: s.Selected}),
		notes: notes.New(notes.Styles{Label: s.Label, Dim: s.Dim}),
		help:  help.New(help.Styles{Group: s.Group, Label: s.Label}),
		reqs: inbox.New(inbox.Styles{
			Group:    s.Group,
			Row:      s.Row,
			Selected: s.Selected,
			Dim:      s.Dim,
		}, batchEngine(cfg.DefaultEngine)),
		teams: teams.New(teams.Styles{
			Row:      s.Row,
			Selected: s.Selected,
			Dim:      s.Dim,
			Err:      s.Err,
		}),
	}
	if initialInput != "" {
		app.screen = msg.NewReview
		app.newrev = app.newrev.SetValue(initialInput)
	}
	return app
}

// Init shows the index from disk first and re-reads GitHub after, so the first
// frame is the user's list rather than an empty dashboard waiting on the network.
func (a App) Init() tea.Cmd {
	// The notes pane renders markdown in a palette the terminal's background has
	// to pick, and glamour has no style that follows it.
	requestBackground := func() tea.Msg { return tea.RequestBackgroundColor() }
	// The watch tick is a stat, not a subprocess. It runs during a dry run too,
	// and whether or not anything else is running.
	if a.dryRun {
		return tea.Batch(a.loadRecords(), requestBackground, a.armIndexTick())
	}
	// A background session outlives the docket that started it, so startup asks
	// the agent about them straight away rather than waiting out the first tick.
	//
	// All three run in order because each reads the whole index and the last two
	// write to it. Side by side, whichever finished last would draw, and that is
	// the one that read the index before the others wrote to it.
	return tea.Batch(
		tea.Sequence(a.loadRecords(), a.reconcile(), a.pollBackground()),
		requestBackground,
		a.armIndexTick(),
	)
}

// Update runs the handler for the message, starts the spinner if the handler left
// something busy on screen, and refits the screens to what the handler left on
// the page. The refit comes last because a spinning status line is two columns
// wider. The extra width can wrap it to one more line.
func (a App) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	model, cmd := a.update(message)
	next := model.(App)
	if next.busy() && !next.spinning {
		next.spinning = true
		cmd = tea.Batch(cmd, next.spin.Tick)
	}
	return next.fit(), cmd
}

// busy reports whether the screen on display shows work in flight. A marker on a
// screen that is not showing does not count. The new review screen keeps its
// marker after it hands the dashboard a prepared review. That marker would
// otherwise keep the spinner ticking with nothing to draw.
func (a App) busy() bool {
	if a.working != "" {
		return true
	}
	switch a.screen {
	case msg.Dashboard:
		return len(a.dash.Busy) > 0
	case msg.Submit:
		return a.sub.Busy != ""
	case msg.NewReview:
		return a.newrev.Busy != ""
	case msg.Requests:
		return a.reqs.Loading
	case msg.Teams:
		return a.teams.Loading || a.teams.Busy
	}
	return false
}

func (a App) update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case spinner.TickMsg:
		// An idle docket schedules no ticks. Update starts them again when
		// something becomes busy.
		if !a.busy() {
			a.spinning = false
			return a, nil
		}
		var cmd tea.Cmd
		a.spin, cmd = a.spin.Update(message)
		return a, cmd

	case tea.WindowSizeMsg:
		a.width, a.height = message.Width, message.Height
		return a, nil

	case tea.BackgroundColorMsg:
		a.notes = a.notes.SetDark(message.IsDark())
		return a, nil

	case tea.KeyPressMsg:
		if handled, app, cmd := a.globalKey(message); handled {
			return app, cmd
		}
		return a.routeToScreen(message)

	case msg.Goto:
		a.screen = message.Screen
		if a.screen == msg.NewReview {
			a.newrev = a.newrev.Reset()
		}
		a.err = nil
		return a, nil

	case msg.OpenHelp:
		a.help = a.help.For(a.screen)
		a.screen = msg.Help
		a.err = nil
		return a, nil

	case msg.OpenRequests:
		a.screen = msg.Requests
		a.err = nil
		if a.reqs.Loading {
			return a, nil
		}
		a.reqs.Loading = true
		return a, a.searchRequests()

	case msg.RefreshRequests:
		a.err = nil
		a.reqs.Loading = true
		return a, a.searchRequests()

	case requestsLoadedMsg:
		a.fetched = &message.fetched
		a.reqs.Loading = false
		return a.regroup(), nil

	case msg.OpenTeams:
		// A search reads the configured teams while it runs. A batch check's
		// answer switches to the requests screen. A teams read or save that
		// is still running would answer the screen this opens.
		if a.reqs.Loading || a.reqs.Busy || a.teams.Loading || a.teams.Busy {
			a.status = "A search, a batch check, or a read or save of your teams is still running. Press t again once it finishes"
			return a, nil
		}
		a.screen = msg.Teams
		a.err = nil
		a.teams = a.teams.Load(a.liveConfig().Teams)
		return a, a.loadTeams()

	case teamsLoadedMsg:
		a.teams = a.teams.SetMemberships(message.teams, message.err)
		return a, nil

	case msg.SaveTeams:
		if a.dryRun {
			a.teams.Busy = false
			a.screen = msg.Requests
			a.status = "Would save teams to config.toml: " + teamList(message.Teams)
			return a, nil
		}
		return a, a.saveTeams(message.Teams)

	case teamsSavedMsg:
		a.teams.Busy = false
		// esc lets the user leave while the save runs.
		if a.screen == msg.Teams {
			a.screen = msg.Requests
		}
		a.status = "Saved teams to config.toml: " + teamList(message.teams)
		return a.update(msg.RefreshRequests{})

	case msg.PrefillReview:
		a.screen = msg.NewReview
		a.newrev = a.newrev.Reset().SetValue(message.URL)
		a.err = nil
		return a, nil

	case msg.StartBatch:
		if a.working != "" {
			a.reqs.Busy = false
			return a.stillWorking(), nil
		}
		a = a.startWork(fmt.Sprintf("Checking %d pull %s for a review of yours", len(message.URLs), format.Plural(len(message.URLs), "request")))
		return a, a.checkBatch(message.URLs, message.Engine)

	case batchCheckedMsg:
		a.working = ""
		a.err = message.err
		a.status = failures(message.failed)
		// Nothing can start, so the list keeps its marks for another try.
		if message.err != nil || len(message.items) == 0 {
			a.reqs = a.reqs.SetExisting(nil, 0)
			return a, nil
		}
		var found []inbox.Existing
		for _, item := range message.items {
			if item.found.Any() {
				found = append(found, inbox.Existing{Ref: item.ref.String(), Found: item.found})
			}
		}
		if len(found) == 0 {
			return a.runBatch(message, "")
		}
		a.batch = &message
		a.screen = msg.Requests
		a.reqs = a.reqs.SetExisting(found, len(message.items)-len(found))
		return a, nil

	case msg.AnswerBatch:
		if a.batch == nil {
			a.reqs = a.reqs.SetExisting(nil, 0)
			return a, nil
		}
		return a.runBatch(*a.batch, review.Intent(message.Intent))

	case batchStartedMsg:
		a.working = ""
		status := fmt.Sprintf("Started %d background %s", message.started, format.Plural(message.started, "review"))
		if len(message.failed) > 0 {
			status += "\n" + failures(message.failed)
		}
		a = a.report(status, message.retry)
		cmds := []tea.Cmd{a.loadRecords(), a.pollBackground()}
		if len(message.untrusted) > 0 {
			cmds = append(cmds, a.trust(message.untrusted))
		}
		return a, tea.Batch(cmds...)

	case trustNeededMsg:
		for _, rec := range message.records {
			delete(a.dash.Busy, rec.ID)
		}
		return a, tea.Batch(a.loadRecords(), a.trust(message.records))

	case trustExitedMsg:
		return a.trusted(message)

	case msg.StartReview:
		return a.startReview(message)

	case existingMsg:
		a.newrev = a.newrev.SetExisting(newreview.Existing{
			Ref:    message.ref.String(),
			Engine: message.engine,
			Found:  message.found,
		})
		return a, nil

	case msg.OpenRereview:
		rec, ok := a.record(message.ID)
		if !ok {
			return a, nil
		}
		if rec.InProgress() {
			a.status = fmt.Sprintf("%s is still %s", rec.Ref, rec.State)
			return a, nil
		}
		// The stored PRState can be stale, so a closed pull request is not refused
		// here. Rereview and ExplainRereview read GitHub again before acting.
		a.newrev = a.newrev.Reset().SetExisting(newreview.Existing{
			Ref:      rec.Ref.String(),
			Engine:   rec.Engine,
			RecordID: rec.ID,
		})
		a.screen = msg.NewReview
		a.err = nil
		return a, nil

	case msg.Ask:
		rec, ok := a.record(message.ID)
		if !ok {
			return a, nil
		}
		if rec.InProgress() {
			a.status = fmt.Sprintf("%s is still %s; ask once it finishes", rec.Ref, rec.State)
			return a, nil
		}
		if a.dryRun {
			return a, a.explainAsk(rec)
		}
		// A second c before the first session takes the terminal would start
		// a second session and overwrite the first one's id.
		if next, busy := a.refuseBusy(rec); busy {
			return next, nil
		}
		a.dash.Busy[rec.ID] = "opening"
		return a, a.ask(rec)

	case msg.Resume:
		rec, ok := a.record(message.ID)
		if !ok {
			return a, nil
		}
		// A background session is held by the agent, which refuses a plain
		// resume while it holds one and says to attach instead. The check comes
		// before the dry run so both report the same command. Reading the
		// agent's listing is a read, which is all a dry run is allowed.
		if rec.HasBackgroundSession() {
			return a, a.openBackground(rec, a.dryRun)
		}
		// No session ran, so there is nothing to resume. Only a background
		// launch can be refused, so the launch runs again in the background.
		if rec.State == review.StateNotStarted {
			if a.dryRun {
				return a, a.explainRestart(rec)
			}
			// A second enter before the launch returns would start a second
			// session that nothing polls or stops.
			if next, busy := a.refuseBusy(rec); busy {
				return next, nil
			}
			a.dash.Busy[rec.ID] = "starting"
			return a, a.restart(rec)
		}
		if a.dryRun {
			return a, a.explainResume(rec)
		}
		return a, a.launch(rec, true)

	case msg.Abandon:
		rec, ok := a.record(message.ID)
		if !ok {
			return a, nil
		}
		if a.dryRun {
			a.status = wouldAbandon(rec)
			return a, nil
		}
		// An abandon beside a submit could record a submitted review as
		// abandoned, depending on which one finishes last.
		if next, busy := a.refuseBusy(rec); busy {
			return next, nil
		}
		a.dash.Busy[rec.ID] = "abandoning"
		return a, a.abandon(rec)

	case msg.RefreshRecords:
		return a.refreshRecords(message)

	case msg.OpenSubmit:
		rec, ok := a.record(message.ID)
		if !ok {
			return a, nil
		}
		if !rec.Submittable() {
			a.status = fmt.Sprintf("%s is %s; only a drafted review can be submitted", rec.Ref, rec.State)
			return a, nil
		}
		// A row left while its review was submitting would otherwise open a
		// second submit of the same draft.
		if next, busy := a.refuseBusy(rec); busy {
			return next, nil
		}
		a.sub = a.sub.For(rec, review.SubmitEventsFor(rec.Author, a.login()))
		a.screen = msg.Submit
		a.err = nil
		return a, a.loadDraft(rec)

	case draftLoadedMsg:
		// The read runs in a command. It can land after the user left the screen,
		// opened another record's, or reopened this one for a newer draft.
		if a.screen != msg.Submit || message.record.ID != a.sub.Record.ID || message.record.ReviewID != a.sub.Record.ReviewID {
			return a, nil
		}
		if message.err != nil {
			a.status = fmt.Sprintf("Could not read the draft's summary: %v", message.err)
			return a, nil
		}
		a.sub = a.sub.SetDraft(message.body)
		return a, nil

	case msg.SubmitReview:
		rec, ok := a.record(message.ID)
		if !ok {
			return a, nil
		}
		if a.dryRun {
			a.sub = a.sub.ClearBusy()
			a.status = fmt.Sprintf("Would submit review %d on %s as %s", rec.ReviewID, rec.Ref, message.Event)
			return a, nil
		}
		// The row carries the marker too, because esc leaves the screen while
		// the submit runs.
		a.dash.Busy[rec.ID] = "submitting"
		return a, a.submitReview(rec, message.Event, message.Body)

	case msg.OpenNotes:
		rec, ok := a.record(message.ID)
		if !ok {
			return a, nil
		}
		// Aim the pane at the record before the file is read, so the header is
		// this record's rather than the last one's until the load lands. The call
		// is cheap, because there is no markdown to wrap yet.
		a.notes = a.notes.SetNotes(rec, "", false)
		a.screen = msg.Notes
		a.err = nil
		return a, a.loadNotes(rec)

	case msg.EditNotes:
		rec, ok := a.record(message.ID)
		if !ok {
			return a, nil
		}
		if a.dryRun {
			a.status = "Would open " + rec.NotesPath + " in $EDITOR"
			return a, nil
		}
		return a, a.editNotes(rec)

	case msg.OpenOnGitHub:
		rec, ok := a.record(message.ID)
		if !ok {
			return a, nil
		}
		a.err = nil
		if a.dryRun {
			a.status = "Would run: " + session.BrowseSpec(rec).String()
			return a, nil
		}
		a.status = "Opening " + rec.WebURL()
		return a, a.browse(rec)

	case notesLoadedMsg:
		// The read runs in a command, so it can land after the user opened another
		// record.
		if message.record.ID != a.notes.Record.ID {
			return a, nil
		}
		a.notes = a.notes.SetNotes(message.record, message.markdown, message.missing)
		return a, nil

	case editorExitedMsg:
		if message.err != nil {
			a.err = message.err
		}
		// The editor can leave the terminal dirty on its way out, so repaint
		// before anything else draws.
		return a, tea.Batch(tea.ClearScreen, a.loadNotes(message.record))

	case refreshedMsg:
		a.working = ""
		if message.err != nil {
			return a.update(errMsg{err: message.err})
		}
		a.status = "Re-read GitHub for every record whose session is over"
		return a.update(recordsLoadedMsg{records: message.records})

	case recordsLoadedMsg:
		a.dash = a.dash.SetRecords(message.records)
		a = a.regroup()
		// A zero stamp means this load came from reconcile or refreshAll,
		// which carry none. Leaving indexStamp alone there is what keeps the
		// next watch tick from wrongly treating their writes as already seen.
		if message.stamp != (index.StatMark{}) {
			a.indexStamp = message.stamp
		}
		return a, nil

	case preparedMsg:
		a.screen = msg.Dashboard
		a.status = message.status
		switch {
		case message.record.Adopted():
			return a, a.ask(message.record)
		case message.record.Mode == review.ModeBackground:
			return a, a.startBackground(message.record)
		default:
			return a, a.launch(message.record, false)
		}

	case launchMsg:
		return a.handoff(message)

	case askExitedMsg:
		a.dash.Busy[message.record.ID] = "reading GitHub"
		return a, tea.Batch(tea.ClearScreen, a.afterAsk(message.record, message.err))

	case childExitedMsg:
		a.dash.Busy[message.record.ID] = "reading GitHub"
		// A session can leave the terminal dirty on its way out, so repaint
		// before anything else draws.
		return a, tea.Batch(tea.ClearScreen, a.afterExit(message.record, message.err))

	case bgTickMsg:
		// Cleared as the tick fires rather than where a chain ends, so the poll
		// this tick asks for is free to arm the next one.
		a.polling = false
		return a, a.pollBackground()

	case bgPolledMsg:
		return a.applyPoll(message)

	case indexTickMsg:
		return a, tea.Batch(a.checkIndex(), a.armIndexTick())

	case indexChangedMsg:
		a.indexStamp = message.stamp
		return a, a.loadRecords()

	case detectedMsg:
		delete(a.dash.Busy, message.record.ID)
		a.status = describe(message.record)
		// A submit that worked has nothing left on its screen to look at. A
		// user who left that screen with esc keeps the screen they went to.
		if message.submitted && a.screen == msg.Submit && a.sub.Record.ID == message.record.ID {
			a.screen = msg.Dashboard
			a.sub = a.sub.ClearBusy()
		}
		// Leaving a background session that is still working puts the record back
		// to running, and nothing else would start watching it again.
		if message.record.BackgroundRunning() {
			return a, tea.Batch(a.loadRecords(), a.pollBackground())
		}
		return a, a.loadRecords()

	case statusMsg:
		a.screen = msg.Dashboard
		a.status = message.text
		return a, nil

	case errMsg:
		a = a.resetBusy(message.err)
		return a, a.loadRecords()

	case bgStartFailedMsg:
		a = a.resetBusy(message.err)
		// A launch that recorded StateReviewing before it failed may still have
		// started a session. The poll adopts that session or closes the record.
		// r and R refuse the record until then. A poll that works clears a.err.
		// The status line keeps the failure because the poll never writes to it.
		if message.record.InBackgroundSession() {
			a.status = fmt.Sprintf("%s did not start in the background: %v", message.record.Ref, message.err)
			return a, tea.Batch(a.loadRecords(), a.pollBackground())
		}
		return a, a.loadRecords()
	}

	return a.routeToScreen(message)
}

func (a App) refreshRecords(message msg.RefreshRecords) (tea.Model, tea.Cmd) {
	if message.ID == "" {
		if a.dryRun {
			a.status = "Would re-read GitHub for every record whose session is over"
			return a, nil
		}
		if a.working != "" {
			return a.stillWorking(), nil
		}
		return a.startWork("Refreshing from GitHub"), a.refreshAll()
	}

	rec, ok := a.record(message.ID)
	if !ok {
		return a, nil
	}
	if a.dryRun {
		a.status = "Would re-read GitHub for " + rec.Ref.String()
		return a, nil
	}
	a.dash.Busy[rec.ID] = "refreshing"
	return a, a.refresh(rec)
}

func (a App) globalKey(key tea.KeyPressMsg) (bool, App, tea.Cmd) {
	switch key.String() {
	case "ctrl+c":
		return true, a, tea.Quit
	case "q":
		if a.screen == msg.Dashboard {
			return true, a, tea.Quit
		}
	}
	return false, a, nil
}

func (a App) routeToScreen(message tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch a.screen {
	case msg.NewReview:
		a.newrev, cmd = a.newrev.Update(message)
	case msg.Submit:
		a.sub, cmd = a.sub.Update(message)
	case msg.Notes:
		a.notes, cmd = a.notes.Update(message)
	case msg.Help:
		a.help, cmd = a.help.Update(message)
	case msg.Requests:
		a.reqs, cmd = a.reqs.Update(message)
	case msg.Teams:
		a.teams, cmd = a.teams.Update(message)
	default:
		a.dash, cmd = a.dash.Update(message)
	}
	return a, cmd
}

// handoff gives the child the real terminal. Bubble Tea pauses, restores the
// alternate screen when the child exits, and sends a fresh window size.
func (a App) handoff(launch launchMsg) (tea.Model, tea.Cmd) {
	cmd := osexec.Command(launch.spec.Path, launch.spec.Args...)
	cmd.Dir = launch.spec.Dir
	cmd.Env = launch.spec.Env(os.Environ())
	return a, tea.ExecProcess(cmd, func(err error) tea.Msg { return exited(launch, err) })
}

// exited is the message a child's exit sends, which decides what runs next. Only
// a review session's exit reads GitHub.
func exited(launch launchMsg, err error) tea.Msg {
	switch launch.kind {
	case launchEditor:
		return editorExitedMsg{record: launch.record, err: err}
	case launchAsk:
		return askExitedMsg{record: launch.record, err: err}
	case launchTrust:
		return trustExitedMsg{records: launch.waiting, err: err}
	default:
		return childExitedMsg{record: launch.record, err: err}
	}
}

// View draws the title, the screen, the status, and the footer inside the page's
// margins.
func (a App) View() tea.View {
	spin := a.spinner()
	a.dash.Spinner, a.newrev.Spinner, a.sub.Spinner, a.reqs.Spinner, a.teams.Spinner = spin, spin, spin, spin, spin

	var body string
	switch a.screen {
	case msg.NewReview:
		body = a.newrev.View()
	case msg.Submit:
		body = a.sub.View()
	case msg.Notes:
		body = a.notes.View()
	case msg.Help:
		body = a.help.View()
	case msg.Requests:
		body = a.reqs.View()
	case msg.Teams:
		body = a.teams.View()
	default:
		body = a.dash.View()
	}

	above, below := a.chrome()
	view := tea.NewView(inMargins(above+"\n"+strings.TrimSuffix(body, "\n")+"\n"+below, a.inner()))
	view.AltScreen = true
	view.MouseMode = tea.MouseModeNone
	view.WindowTitle = "docket"
	return view
}

// choosing reports whether the screen on show is asking what to do with a review
// that already exists.
func (a App) choosing() bool {
	switch a.screen {
	case msg.NewReview:
		return a.newrev.Existing != nil
	case msg.Requests:
		return a.reqs.Asking()
	}
	return false
}

// chrome is what View draws above and below the screen. fit measures the same
// text, so the room it gives the screen is the room View leaves.
func (a App) chrome() (above, below string) {
	above = a.styles.Title.Render("docket")
	if a.dryRun {
		above += " " + a.styles.Dim.Render("dry run")
	}
	status := a.statusLine()
	if a.err != nil {
		status = a.styles.Err.Render(wrap(a.err.Error(), a.inner()))
	} else if status != "" {
		status = wrap(status, a.inner())
	}
	below = a.footer()
	if status != "" {
		below = status + "\n\n" + below
	}
	return above + "\n", "\n" + below
}

// spinner is what every busy marker draws with. Its frame is empty while the
// root's spinner is stopped.
func (a App) spinner() format.Spinner {
	frame := ""
	if a.spinning {
		frame = a.spin.View()
	}
	return format.Spinner{Frame: frame, Style: a.styles.Busy}
}

// inner is the page's width inside its margins. It is zero until the first
// WindowSizeMsg arrives. The screens read zero as an unknown width.
func (a App) inner() int {
	if a.width <= 0 {
		return 0
	}
	return max(a.width-2*marginX, 1)
}

// fit sizes the screens to the room the page leaves them. It measures the
// chrome's height on every call, because the footer and the status wrap to as
// many lines as the width needs.
//
// fit refits the notes only while they are on screen. Re-wrapping long notes
// costs tens of milliseconds. A drag-resize sends a stream of WindowSizeMsg
// values.
func (a App) fit() App {
	width, height := a.inner(), 0
	if a.height > 0 {
		above, below := a.chrome()
		height = max(a.height-2*marginY-lipgloss.Height(above)-lipgloss.Height(below), 1)
	}

	a.dash.Width = width
	a.reqs.Width, a.reqs.Height = width, height
	a.teams.Width, a.teams.Height = width, height
	a.newrev = a.newrev.SetWidth(width)
	a.sub = a.sub.SetWidth(width)
	a.help = a.help.SetSize(width, height)
	if a.screen == msg.Notes {
		a.notes = a.notes.SetSize(width, height)
	}
	return a
}

// statusLine is the work in flight followed by the last status. A status written
// while the work runs, such as a refusal, stays on screen beside it.
func (a App) statusLine() string {
	var parts []string
	if a.working != "" {
		parts = append(parts, a.spinner().Render(a.working))
	}
	if a.status != "" {
		parts = append(parts, a.styles.Dim.Render(a.status))
	}
	return strings.Join(parts, a.styles.Dim.Render(" · "))
}

// stillWorking refuses a batch start or a refresh of every record while the
// other runs. Both use the one line of work in flight, and the first to finish
// would clear it while the other still runs.
func (a App) stillWorking() App {
	a.status = "A refresh or a batch start is still running. Try again once it finishes"
	return a
}

// startWork puts text on the line of work in flight. It clears the last error
// and status, because they describe work that is over.
func (a App) startWork(text string) App {
	a.err = nil
	a.status = ""
	a.working = text
	return a
}

// refuseBusy reports whether the row has work in flight, and says so on the
// status line when it does.
func (a App) refuseBusy(rec review.Record) (App, bool) {
	note, busy := a.dash.Busy[rec.ID]
	if busy {
		a.status = fmt.Sprintf("%s is still %s", rec.Ref, note)
	}
	return a, busy
}

// login is the user a pull request's author is compared against. The service
// caches it the first time anything reads GitHub.
func (a App) login() string {
	return a.liveConfig().GitHubUser
}

// liveConfig is the service's Config. The service caches the login and saves
// the teams into its own copy. The copy taken at startup sees neither. The
// fallback serves tests, which build the root without a service.
func (a App) liveConfig() config.Config {
	if a.svc != nil {
		return a.svc.Cfg
	}
	return a.cfg
}

func (a App) record(id string) (review.Record, bool) {
	i := slices.IndexFunc(a.dash.Records, func(rec review.Record) bool { return rec.ID == id })
	if i < 0 {
		return review.Record{}, false
	}
	return a.dash.Records[i], true
}

// report puts a line on the status. A trust prompt takes the terminal between a
// batch's report and the reports that follow the prompt, so those add to the
// status rather than replace the batch's list of what failed.
func (a App) report(line string, after bool) App {
	if after && a.status != "" {
		line = a.status + "\n" + line
	}
	a.status = line
	return a
}

// resetBusy records a failure for the status line and clears every screen's busy
// marker. Nothing records which marker the failed work set. resetBusy therefore
// clears all of them. The requests screen's Busy is the exception. The batch
// check always answers with a batchCheckedMsg, which clears it. An unrelated
// failure that cleared it would let a second enter check and start the same
// batch. The teams screen's Loading is another, because the read of the user's
// teams answers with teamsLoadedMsg even when it fails. resetBusy also leaves
// the line of work in flight, which only that work's own failure clears.
func (a App) resetBusy(err error) App {
	a.err = err
	a.dash.Busy = map[string]string{}
	a.newrev = a.newrev.ClearBusy()
	a.sub = a.sub.ClearBusy()
	a.reqs.Loading = false
	a.teams.Busy = false
	return a
}

// failures lists the pull requests a batch could not check or start, one to a
// line. It is empty when there are none.
func failures(failed []string) string {
	if len(failed) == 0 {
		return ""
	}
	return fmt.Sprintf("%d failed:\n%s", len(failed), strings.Join(failed, "\n"))
}

// loaded and detected give the commands below their one shared shape. Each runs
// its work, turns a failure into errMsg, and otherwise reports the result.
func loaded(work func() ([]review.Record, error)) tea.Cmd {
	return func() tea.Msg {
		records, err := work()
		if err != nil {
			return errMsg{err: err}
		}
		return recordsLoadedMsg{records: records}
	}
}

func detected(work func() (review.Record, error)) tea.Cmd {
	return func() tea.Msg {
		rec, err := work()
		if err != nil {
			return errMsg{err: err}
		}
		return detectedMsg{record: rec}
	}
}

// loadRecords stats the index before reading it, so the stamp it hands back
// reflects the file as of just before this read rather than just after. Any
// write landing during or after the read is then still new to the next
// watch tick, rather than being folded silently into what this load already
// saw. reconcile and refreshAll carry no stamp. Each makes its
// own writes partway through its work, so a stamp taken at their start
// would call those writes "already seen." It could also miss a genuinely
// concurrent external write landing in the same window.
func (a App) loadRecords() tea.Cmd {
	svc := a.svc
	return func() tea.Msg {
		stamp, _ := svc.Store.Stat()
		records, err := svc.Records()
		if err != nil {
			return errMsg{err: err}
		}
		return recordsLoadedMsg{records: records, stamp: stamp}
	}
}

func (a App) reconcile() tea.Cmd {
	svc := a.svc
	return loaded(func() ([]review.Record, error) { return svc.Reconcile(context.Background()) })
}

func (a App) refreshAll() tea.Cmd {
	svc := a.svc
	return func() tea.Msg {
		records, err := svc.RefreshAll(context.Background())
		return refreshedMsg{records: records, err: err}
	}
}

func (a App) refresh(rec review.Record) tea.Cmd {
	svc := a.svc
	return detected(func() (review.Record, error) { return svc.Refresh(context.Background(), rec) })
}

func (a App) afterExit(rec review.Record, childErr error) tea.Cmd {
	svc := a.svc
	return detected(func() (review.Record, error) {
		return svc.AfterExit(context.Background(), rec, childErr)
	})
}

func (a App) afterAsk(rec review.Record, childErr error) tea.Cmd {
	svc := a.svc
	return detected(func() (review.Record, error) { return svc.AfterAsk(context.Background(), rec, childErr) })
}

func (a App) abandon(rec review.Record) tea.Cmd {
	svc := a.svc
	return detected(func() (review.Record, error) { return svc.Abandon(context.Background(), rec) })
}

func (a App) submitReview(rec review.Record, event, body string) tea.Cmd {
	svc := a.svc
	return func() tea.Msg {
		updated, err := svc.Submit(context.Background(), rec, event, body)
		if err != nil {
			return errMsg{err: err}
		}
		return detectedMsg{record: updated, submitted: true}
	}
}

func (a App) loadDraft(rec review.Record) tea.Cmd {
	svc := a.svc
	return func() tea.Msg {
		body, err := svc.DraftBody(context.Background(), rec)
		return draftLoadedMsg{record: rec, body: body, err: err}
	}
}

func (a App) loadNotes(rec review.Record) tea.Cmd {
	svc := a.svc
	return func() tea.Msg {
		markdown, missing, err := svc.Notes(rec)
		if err != nil {
			return errMsg{err: err}
		}
		return notesLoadedMsg{record: rec, markdown: markdown, missing: missing}
	}
}

// editNotes hands the terminal to $EDITOR the same way a review session gets it.
func (a App) editNotes(rec review.Record) tea.Cmd {
	svc := a.svc
	return func() tea.Msg {
		spec, err := svc.EditNotesSpec(rec)
		if err != nil {
			return errMsg{err: err}
		}
		return launchMsg{record: rec, spec: spec, kind: launchEditor}
	}
}

// browse needs no terminal, so it runs through the service's runner rather
// than taking the screen the way editNotes does.
func (a App) browse(rec review.Record) tea.Cmd {
	svc := a.svc
	return func() tea.Msg {
		if err := svc.Browse(rec); err != nil {
			return errMsg{err: err}
		}
		return nil
	}
}

// searchRequests only reads GitHub, so a dry run may run it. The screen keeps the
// rows it already has while the search runs.
func (a App) searchRequests() tea.Cmd {
	svc := a.svc
	return func() tea.Msg {
		fetched, err := svc.Requests(context.Background())
		if err != nil {
			return errMsg{err: err}
		}
		return requestsLoadedMsg{fetched: fetched}
	}
}

// loadTeams only reads GitHub, so a dry run may run it.
func (a App) loadTeams() tea.Cmd {
	svc := a.svc
	return func() tea.Msg {
		teams, err := svc.Teams(context.Background())
		return teamsLoadedMsg{teams: teams, err: err}
	}
}

func (a App) saveTeams(teams []string) tea.Cmd {
	svc := a.svc
	return func() tea.Msg {
		if err := svc.SaveTeams(teams); err != nil {
			return errMsg{err: err}
		}
		return teamsSavedMsg{teams: teams}
	}
}

// teamList names the teams for the status line.
func teamList(teams []string) string {
	if len(teams) == 0 {
		return "none"
	}
	return strings.Join(teams, ", ")
}

// checkBatch reads what review each pull request already has, before the batch
// starts any of them. A pull request with a review needs the user's answer to
// append or overwrite. Without that answer, review-code asks the question in a
// background session where nobody can answer it. The check writes nothing, so
// a dry run runs it too and asks the same question.
func (a App) checkBatch(urls []string, engineName string) tea.Cmd {
	svc, dryRun := a.svc, a.dryRun
	return func() tea.Msg {
		// Looking the binary up here is what keeps a missing one from costing
		// the user an answer and then a tier-2 clone.
		if !dryRun {
			if err := checkEngine(engineName); err != nil {
				return batchCheckedMsg{err: err}
			}
		}
		ctx := context.Background()
		checked := batchCheckedMsg{engine: engineName}
		for _, url := range urls {
			ref, err := pr.ParseRef(url, "")
			if err != nil {
				checked.failed = append(checked.failed, err.Error())
				continue
			}
			found, err := svc.Existing(ctx, ref)
			if err != nil {
				checked.failed = append(checked.failed, fmt.Sprintf("%s: %v", ref, err))
				continue
			}
			checked.items = append(checked.items, batchItem{ref: ref, found: found})
		}
		return checked
	}
}

// runBatch starts a checked batch, or explains it in a dry run. answer is what to
// do with the pull requests that already have a review.
func (a App) runBatch(checked batchCheckedMsg, answer review.Intent) (tea.Model, tea.Cmd) {
	checked = checked.resolve(answer)
	a.batch = nil
	a.reqs = a.reqs.SetExisting(nil, 0)
	if a.dryRun {
		return a, a.explainBatch(checked)
	}
	a = a.startWork(fmt.Sprintf("Starting %d background %s", len(checked.items), format.Plural(len(checked.items), "review")))
	a.screen = msg.Dashboard
	a.reqs.Marked = map[string]bool{}
	return a, a.startBatch(checked)
}

// startBatch prepares and starts each pull request in turn. It does not run them
// side by side, because each Prepare makes several calls to GitHub and may clone
// a repository. Starting one is quick, since the engine's background mode
// returns straight away. It reports once for the whole batch, because the
// detectedMsg a single start answers with would overwrite the status line once
// per pull request.
func (a App) startBatch(checked batchCheckedMsg) tea.Cmd {
	svc := a.svc
	return func() tea.Msg {
		ctx := context.Background()
		done := batchStartedMsg{failed: checked.failed}
		for _, item := range checked.items {
			rec, err := startOne(ctx, svc, item.ref, checked.engine, item.intent)
			switch {
			case errors.Is(err, session.ErrUntrusted):
				done.untrusted = append(done.untrusted, rec)
			case err != nil:
				done.failed = append(done.failed, err.Error())
			default:
				done.started++
			}
		}
		return done
	}
}

// startOne returns the record it started. When a start fails after Prepare wrote
// the record, it returns that record too.
func startOne(ctx context.Context, svc *session.Service, ref pr.Ref, engineName string, intent review.Intent) (review.Record, error) {
	rec, _, err := svc.Prepare(ctx, ref, engineName, review.ModeBackground, intent)
	if err != nil {
		return rec, fmt.Errorf("%s: %w", ref, err)
	}
	rec, err = svc.StartBackground(ctx, rec)
	if err != nil {
		return rec, fmt.Errorf("%s: %w", ref, err)
	}
	return rec, nil
}

// trust hands the terminal to the agent's trust prompt for the first record's
// directory. The prompt's exit starts the records in that directory again and
// then asks about the next directory, so each directory is asked about once.
func (a App) trust(records []review.Record) tea.Cmd {
	svc := a.svc
	return func() tea.Msg {
		spec, err := svc.TrustSpec(records[0])
		if err != nil {
			// trusted reports this directory and still asks about the others.
			return trustExitedMsg{records: records, err: err}
		}
		return launchMsg{record: records[0], spec: spec, kind: launchTrust, waiting: records}
	}
}

// trusted starts again the launches that were waiting on the directory the
// prompt asked about, and asks about the next directory. A user who declines one
// directory may still trust the next, because each prompt names a different
// repository.
func (a App) trusted(exited trustExitedMsg) (tea.Model, tea.Cmd) {
	dir := exited.records[0].Dir
	var here, rest []review.Record
	for _, rec := range exited.records {
		if rec.Dir == dir {
			here = append(here, rec)
		} else {
			rest = append(rest, rec)
		}
	}

	// The prompt can leave the terminal dirty on its way out, so repaint before
	// anything else draws.
	cmds := []tea.Cmd{tea.ClearScreen}
	if exited.err != nil {
		reason := fmt.Sprintf("%s is still not trusted", dir)
		// An exit status is the user declining. Any other error means the
		// prompt never ran.
		if exitErr := (*osexec.ExitError)(nil); !errors.As(exited.err, &exitErr) {
			reason = fmt.Sprintf("the trust prompt for %s did not run: %v", dir, exited.err)
		}
		a = a.report(fmt.Sprintf("%s, so %d background %s did not start", reason, len(here), format.Plural(len(here), "review")), true)
		cmds = append(cmds, a.loadRecords())
	} else {
		cmds = append(cmds, a.retryBackground(here))
	}
	if len(rest) > 0 {
		cmds = append(cmds, a.trust(rest))
	}
	return a, tea.Batch(cmds...)
}

// retryBackground starts again the launches the agent refused for trust. A
// second refusal counts as a failure and does not bring the prompt back.
// Otherwise a prompt that did not help would return after every launch.
func (a App) retryBackground(records []review.Record) tea.Cmd {
	svc := a.svc
	return func() tea.Msg {
		done := batchStartedMsg{retry: true}
		for _, rec := range records {
			if _, err := svc.StartBackground(context.Background(), rec); err != nil {
				done.failed = append(done.failed, fmt.Sprintf("%s: %v", rec.Ref, err))
				continue
			}
			done.started++
		}
		return done
	}
}

// explainBatch is the dry-run counterpart of startBatch. Like startBatch, it
// reports a pull request that fails and goes on to the next.
func (a App) explainBatch(checked batchCheckedMsg) tea.Cmd {
	svc := a.svc
	return func() tea.Msg {
		var lines []string
		for _, item := range checked.items {
			lines = append(lines, explainOne(svc, item.ref, checked.engine, item.intent))
		}
		return statusMsg{text: strings.Join(append(lines, checked.failed...), "\n")}
	}
}

func explainOne(svc *session.Service, ref pr.Ref, engineName string, intent review.Intent) string {
	plan, spec, err := svc.Explain(context.Background(), ref, engineName, review.ModeBackground, intent)
	if err != nil {
		return fmt.Sprintf("%s: %v", ref, err)
	}
	return ref.String() + ": " + explainLine(plan, spec)
}

// regroup rebuilds the requests screen from the last search and the records as
// they now stand. It does nothing before the first search.
func (a App) regroup() App {
	if a.fetched != nil {
		a.reqs = a.reqs.SetSections(requests.Group(*a.fetched, a.dash.Records))
	}
	return a
}

// batchEngine is the engine a batch of background reviews runs under: the
// default engine when it has a background mode, and otherwise the first engine
// that does. It is empty when no engine has one.
func batchEngine(defaultEngine string) string {
	names := engine.BackgroundNames()
	if slices.Contains(names, defaultEngine) {
		return defaultEngine
	}
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

// startReview routes the new review screen's request. A request with no intent
// is the first one, which asks what review is already there before preparing.
func (a App) startReview(start msg.StartReview) (tea.Model, tea.Cmd) {
	intent, mode := review.Intent(start.Intent), modeFor(start.Background)
	if start.RecordID == "" {
		return a, a.prepare(start.Input, start.Engine, mode, intent)
	}
	rec, ok := a.record(start.RecordID)
	if !ok {
		a.newrev = a.newrev.ClearBusy()
		return a, nil
	}
	if a.dryRun {
		return a, a.explainRereview(rec, intent, mode)
	}
	return a, a.rereview(rec, intent, mode)
}

// prepare resolves the typed pull request and readies its record. With no intent
// it first reads what review is already there. That read writes nothing, so a
// dry run does it too and shows the same choice. A dry run then stops at the
// command that would run.
func (a App) prepare(input, engineName string, mode review.Mode, intent review.Intent) tea.Cmd {
	svc, defaultRepo, dryRun := a.svc, a.cfg.DefaultRepo, a.dryRun
	return func() tea.Msg {
		ctx := context.Background()
		ref, err := pr.ParseRef(input, defaultRepo)
		if err != nil {
			return errMsg{err: err}
		}
		if intent == "" {
			found, err := svc.Existing(ctx, ref)
			if err != nil {
				return errMsg{err: err}
			}
			if found.Any() {
				return existingMsg{ref: ref, engine: engineName, found: found}
			}
			intent = review.IntentReview
		}

		if dryRun {
			plan, spec, err := svc.Explain(ctx, ref, engineName, mode, intent)
			if err != nil {
				return errMsg{err: err}
			}
			if intent == review.IntentAsk {
				return statusMsg{text: "Would adopt the review of " + ref.String() + " in " + plan.Dir + "\nWould run: " + spec.String()}
			}
			return statusMsg{text: explainLine(plan, spec)}
		}

		if err := checkEngine(engineName); err != nil {
			return errMsg{err: err}
		}
		rec, plan, err := svc.Prepare(ctx, ref, engineName, mode, intent)
		if err != nil {
			return errMsg{err: err}
		}
		return preparedMsg{record: rec, status: plan.Description()}
	}
}

func (a App) rereview(rec review.Record, intent review.Intent, mode review.Mode) tea.Cmd {
	svc := a.svc
	return func() tea.Msg {
		updated, err := svc.Rereview(context.Background(), rec, intent, mode)
		if err != nil {
			return errMsg{err: err}
		}
		return preparedMsg{record: updated, status: fmt.Sprintf("Reviewing %s again with --%s", rec.Ref, intent)}
	}
}

func (a App) explainRereview(rec review.Record, intent review.Intent, mode review.Mode) tea.Cmd {
	svc := a.svc
	return func() tea.Msg {
		spec, err := svc.ExplainRereview(context.Background(), rec, intent, mode)
		if err != nil {
			return errMsg{err: err}
		}
		return statusMsg{text: "Would run: " + spec.String()}
	}
}

// ask hands the terminal to a question-and-answer session about the notes.
func (a App) ask(rec review.Record) tea.Cmd {
	svc := a.svc
	return func() tea.Msg {
		updated, spec, err := svc.AskSpec(rec)
		if err != nil {
			return errMsg{err: err}
		}
		return launchMsg{record: updated, spec: spec, kind: launchAsk}
	}
}

func (a App) explainAsk(rec review.Record) tea.Cmd {
	svc := a.svc
	return func() tea.Msg {
		spec, err := svc.ExplainAsk(rec)
		if err != nil {
			return errMsg{err: err}
		}
		return statusMsg{text: "Would run: " + spec.String()}
	}
}

func (a App) launch(rec review.Record, resume bool) tea.Cmd {
	svc := a.svc
	spec := svc.LaunchSpec
	if resume {
		spec = svc.ResumeSpec
	}
	return func() tea.Msg {
		updated, launchSpec, err := spec(context.Background(), rec)
		if err != nil {
			return errMsg{err: err}
		}
		return launchMsg{record: updated, spec: launchSpec}
	}
}

// explainLine is what a dry run reports for one review it would start.
func explainLine(plan session.Plan, spec exec.CommandSpec) string {
	return plan.Description() + "\nWould run: " + spec.String()
}

// checkEngine finds the engine's binary. Startup checks only the default engine,
// and the new review screen offers the others too. Looking the binary up before
// Prepare is what keeps a missing one from costing a tier-2 clone first.
func checkEngine(name string) error {
	eng, err := engine.For(name)
	if err != nil {
		return err
	}
	if _, err := osexec.LookPath(eng.Binary()); err != nil {
		return fmt.Errorf("%s is not on your PATH", eng.Binary())
	}
	return nil
}

func (a App) explainRestart(rec review.Record) tea.Cmd {
	svc := a.svc
	return func() tea.Msg {
		spec, err := svc.ExplainRestart(context.Background(), rec)
		if err != nil {
			return errMsg{err: err}
		}
		return statusMsg{text: "Would run: " + spec.String()}
	}
}

func (a App) explainResume(rec review.Record) tea.Cmd {
	svc := a.svc
	return func() tea.Msg {
		spec, err := svc.ExplainResume(rec)
		if err != nil {
			return errMsg{err: err}
		}
		return statusMsg{text: "Would run: " + spec.String()}
	}
}

func wouldAbandon(rec review.Record) string {
	if rec.Tier == tier.Tier2 && rec.Dir != "" {
		return fmt.Sprintf("Would abandon %s and delete %s", rec.Ref, rec.Dir)
	}
	return fmt.Sprintf("Would abandon %s; docket created nothing to delete", rec.Ref)
}

func describe(rec review.Record) string {
	switch rec.State {
	case review.StateArchived:
		if rec.SubmittedAt == nil && rec.PRState.Closed() {
			return fmt.Sprintf("%s is %s, so docket archived it. Notes stay at %s", rec.Ref, rec.PRState.Label(), rec.NotesPath)
		}
		return fmt.Sprintf("%s submitted and archived. Notes stay at %s", rec.Ref, rec.NotesPath)
	case review.StateDrafted:
		if rec.PRState.Closed() {
			return fmt.Sprintf("%s is %s and your review is still pending. Press s to submit it or x to abandon", rec.Ref, rec.PRState.Label())
		}
		if rec.Adopted() {
			return fmt.Sprintf("%s has a pending review. Press s to submit it or c to ask about it", rec.Ref)
		}
		return fmt.Sprintf("%s has a pending review. Press enter to keep going", rec.Ref)
	case review.StateReviewed:
		return fmt.Sprintf("%s has your review notes. Press c to ask about them or u to review it again", rec.Ref)
	case review.StateUnreviewed:
		return fmt.Sprintf("%s: the session ended without posting a review. Press enter to resume it, u to review again, or x to abandon", rec.Ref)
	case review.StateAbandoned:
		return fmt.Sprintf("%s abandoned", rec.Ref)
	default:
		return fmt.Sprintf("%s is %s", rec.Ref, rec.State)
	}
}

// Run starts the program. It keeps Bubble Tea's default signal handling, which the
// terminal handoff depends on. The child must stay the foreground process group of
// the TTY.
func Run(app App) error {
	_, err := tea.NewProgram(app).Run()
	return err
}

// modeFor turns the screen's choice into the mode a record carries.
func modeFor(background bool) review.Mode {
	if background {
		return review.ModeBackground
	}
	return review.ModeInteractive
}

// bgInterval is how often docket asks the agent about its running sessions. A
// review takes minutes, so this is about how soon the dashboard notices rather
// than about catching the moment it ends.
const bgInterval = 15 * time.Second

// indexInterval is how often docket checks whether another instance appended
// to the index. The check is a stat, not a subprocess. Unlike bgInterval,
// this ticks whether or not anything is running.
const indexInterval = 2 * time.Second

func (a App) armIndexTick() tea.Cmd {
	return tea.Tick(indexInterval, func(time.Time) tea.Msg { return indexTickMsg{} })
}

// checkIndex stats the index file and reports a change without taking the
// lock Load does. A stat failure is dropped rather than surfaced. errMsg
// clears the dashboard's busy markers on any error, and a transient failure
// on this 2-second poll must not wipe one of those markers out from under an
// operation still mid-flight.
func (a App) checkIndex() tea.Cmd {
	svc, prev := a.svc, a.indexStamp
	return func() tea.Msg {
		stamp, changed, err := svc.Store.Changed(prev)
		if err != nil || !changed {
			return nil
		}
		return indexChangedMsg{stamp: stamp}
	}
}

// applyPoll redraws from a poll and arms the next one. The tick is re-armed here
// rather than on a timer of its own, so an idle docket runs no subprocesses: the
// poll stops when nothing is running and starts again when something is.
func (a App) applyPoll(polled bgPolledMsg) (tea.Model, tea.Cmd) {
	// A poll that worked clears what a failed one said. The poll is the one
	// thing here that runs on a timer rather than on a keystroke, so an error it
	// left behind would sit over every status line until the user happened to
	// change screens.
	a.err = polled.err
	if polled.records != nil {
		a.dash = a.dash.SetRecords(polled.records)
		a.dash.Background = polled.progress
	}
	if !a.watching() || a.polling {
		return a, nil
	}
	a.polling = true
	return a, tea.Tick(bgInterval, func(time.Time) tea.Msg { return bgTickMsg{} })
}

// watching reports whether any record is a background session still running.
func (a App) watching() bool {
	return slices.ContainsFunc(a.dash.Records, review.Record.BackgroundRunning)
}

func (a App) pollBackground() tea.Cmd {
	svc := a.svc
	return func() tea.Msg {
		records, statuses, err := svc.PollBackground(context.Background())
		return bgPolledMsg{records: records, progress: progressFor(statuses), err: err}
	}
}

// progressFor hands each session's progress to the dashboard, which holds no
// engine to ask. A session is waiting for the user when the listing reports it
// idle and the status file does not report it active, which claude does at
// launch before the first turn. claude's own reading of the conversation can
// call a session blocked while its reviewer agents still run, so a working
// session's need is dropped. A working session with no detail shows the agent's
// state.
func progressFor(statuses map[string]engine.BGStatus) map[string]review.Progress {
	progress := make(map[string]review.Progress, len(statuses))
	for id, status := range statuses {
		p := status.Progress
		if status.Idle && !p.Active {
			p.Needs = cmp.Or(p.Needs, "your input")
		} else {
			p.Needs = ""
			p.Detail = cmp.Or(p.Detail, status.State)
		}
		progress[id] = p
	}
	return progress
}

// startBackground launches a review that runs without the terminal. The poll
// that follows a success is the one detectedMsg arms for any record that comes
// back running, so this adds none of its own there: two would read the index
// and the agent twice for one keystroke. A failure can come after StartBackground
// recorded StateReviewing or started a session. detected would replace the
// record with a bare errMsg. A failure therefore goes out as bgStartFailedMsg,
// which carries the record.
func (a App) startBackground(rec review.Record) tea.Cmd {
	svc := a.svc
	return func() tea.Msg { return started(svc.StartBackground(context.Background(), rec)) }
}

// restart launches again a background review the agent refused.
func (a App) restart(rec review.Record) tea.Cmd {
	svc := a.svc
	return func() tea.Msg { return started(svc.Restart(context.Background(), rec)) }
}

// started is the message a background launch answers with.
func started(out review.Record, err error) tea.Msg {
	if errors.Is(err, session.ErrUntrusted) {
		return trustNeededMsg{records: []review.Record{out}}
	}
	if err != nil {
		return bgStartFailedMsg{record: out, err: err}
	}
	return detectedMsg{record: out}
}

// openBackground puts a running session on the terminal. Which command does that
// depends on whether the agent still holds the session, so the service reads its
// status before building the spec.
func (a App) openBackground(rec review.Record, explain bool) tea.Cmd {
	svc := a.svc
	return func() tea.Msg {
		spec, err := svc.OpenBackgroundSpec(context.Background(), rec)
		if err != nil {
			return errMsg{err: err}
		}
		if explain {
			return statusMsg{text: "Would run: " + spec.String()}
		}
		return launchMsg{record: rec, spec: spec}
	}
}
