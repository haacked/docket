// Package tui is docket's terminal UI. It owns every command that touches the
// service, and it is the only package that hands a command to the terminal. It is
// also where a dry run stops. Most of what a dry run must not do is a write to the
// index or the filesystem, not a command a runner could intercept.
package tui

import (
	"context"
	"fmt"
	"os"
	osexec "os/exec"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/haacked/docket/internal/core/config"
	"github.com/haacked/docket/internal/core/engine"
	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/index"
	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/requests"
	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/core/session"
	"github.com/haacked/docket/internal/core/tier"
	"github.com/haacked/docket/internal/tui/msg"
	"github.com/haacked/docket/internal/tui/screens/dashboard"
	"github.com/haacked/docket/internal/tui/screens/help"
	"github.com/haacked/docket/internal/tui/screens/inbox"
	"github.com/haacked/docket/internal/tui/screens/newreview"
	"github.com/haacked/docket/internal/tui/screens/notes"
	"github.com/haacked/docket/internal/tui/screens/submit"
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
	// fetched is the last search for review requests. regroup reads it whenever
	// the records change.
	fetched *requests.Fetched

	width  int
	height int
	status string
	err    error
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

func (a App) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = message.Width, message.Height
		a.dash.Width = message.Width
		// Only the pane on screen is refitted. Re-wrapping a long review costs
		// tens of milliseconds. A drag-resize sends a stream of these messages,
		// and one follows every return from a child process. Opening the notes
		// refits them, so a resize the pane sat out is not missed.
		if a.screen == msg.Notes {
			a.notes = a.notes.SetSize(message.Width, a.notesHeight())
		}
		if a.screen == msg.Help {
			a.help = a.help.SetSize(message.Width, a.paneHeight())
		}
		a.reqs.Width, a.reqs.Height = message.Width, a.paneHeight()
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
		// OpenNotes already refits on its own way in. Help closing back to Notes
		// goes through here instead. A resize while help was on top would
		// otherwise leave the pane wrapped to a stale width until the next one.
		if a.screen == msg.Notes {
			a.notes = a.notes.SetSize(a.width, a.notesHeight())
		}
		a.err = nil
		return a, nil

	case msg.OpenHelp:
		a.help = a.help.For(a.screen).SetSize(a.width, a.paneHeight())
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

	case msg.PrefillReview:
		a.screen = msg.NewReview
		a.newrev = a.newrev.Reset().SetValue(message.URL)
		a.err = nil
		return a, nil

	case msg.StartBatch:
		if a.dryRun {
			return a, a.explainBatch(message.URLs, message.Engine)
		}
		a.screen = msg.Dashboard
		a.status = fmt.Sprintf("starting %d background reviews…", len(message.URLs))
		a.reqs.Marked = map[string]bool{}
		return a, a.startBatch(message.URLs, message.Engine)

	case batchStartedMsg:
		a.status = fmt.Sprintf("Started %d background reviews", message.started)
		if len(message.failed) > 0 {
			a.status += fmt.Sprintf("; %d failed:\n%s", len(message.failed), strings.Join(message.failed, "\n"))
		}
		return a, tea.Batch(a.loadRecords(), a.pollBackground())

	case msg.StartReview:
		return a.startReview(message)

	case existingMsg:
		a.newrev = a.newrev.SetExisting(newreview.Existing{
			Ref:       message.ref.String(),
			Engine:    message.engine,
			NotesAt:   message.found.NotesAt,
			Pending:   message.found.PendingID != 0,
			Submitted: message.found.Submitted,
		})
		return a, nil

	case msg.OpenRereview:
		rec, ok := a.record(message.ID)
		if !ok {
			return a, nil
		}
		if rec.State == review.StateReviewing {
			a.status = fmt.Sprintf("%s is still being reviewed", rec.Ref)
			return a, nil
		}
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
		if rec.State == review.StateReviewing {
			a.status = fmt.Sprintf("%s is still being reviewed; ask once the review finishes", rec.Ref)
			return a, nil
		}
		if a.dryRun {
			return a, a.explainAsk(rec)
		}
		// A second c before the first session takes the terminal would start
		// a second session and overwrite the first one's id.
		if _, busy := a.dash.Busy[rec.ID]; busy {
			return a, nil
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
		a.sub = a.sub.For(rec, review.SubmitEventsFor(rec.Author, a.login()))
		a.screen = msg.Submit
		a.err = nil
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
		return a, a.submitReview(rec, message.Event, message.Body)

	case msg.OpenNotes:
		rec, ok := a.record(message.ID)
		if !ok {
			return a, nil
		}
		// Aim the pane at the record before the file is read, so the header is
		// this record's rather than the last one's until the load lands. Both
		// calls are cheap here: there is no markdown to wrap yet.
		a.notes = a.notes.SetNotes(rec, "", false).SetSize(a.width, a.notesHeight())
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
		// A submit that worked has nothing left on its screen to look at.
		if message.submitted {
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
		a.err = message.err
		a.dash.Busy = map[string]string{}
		a.newrev = a.newrev.ClearBusy()
		a.sub = a.sub.ClearBusy()
		a.reqs.Loading = false
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
		a.status = "refreshing from GitHub"
		return a, a.refreshAll()
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
	default:
		return childExitedMsg{record: launch.record, err: err}
	}
}

func (a App) View() tea.View {
	var b strings.Builder
	b.WriteString(a.styles.Title.Render("docket"))
	if a.dryRun {
		b.WriteString(" " + a.styles.Dim.Render("dry run"))
	}
	b.WriteString("\n\n")

	switch a.screen {
	case msg.NewReview:
		b.WriteString(a.newrev.View())
	case msg.Submit:
		b.WriteString(a.sub.View())
	case msg.Notes:
		b.WriteString(a.notes.View() + "\n")
	case msg.Help:
		b.WriteString(a.help.View() + "\n")
	case msg.Requests:
		b.WriteString(a.reqs.View() + "\n")
	default:
		b.WriteString(a.dash.View() + "\n")
	}

	if a.err != nil {
		b.WriteString("\n" + a.styles.Err.Render(wrap(a.err.Error(), a.width)) + "\n")
	} else if a.status != "" {
		b.WriteString("\n" + a.styles.Dim.Render(wrap(a.status, a.width)) + "\n")
	}
	b.WriteString("\n" + a.styles.Footer.Render(helpFor(a.screen, a.dash.ShowArchived)))

	view := tea.NewView(b.String())
	view.AltScreen = true
	view.MouseMode = tea.MouseModeNone
	view.WindowTitle = "docket"
	return view
}

// login is the user a pull request's author is compared against. The service
// caches it the first time anything reads GitHub. The copy taken at startup is
// empty until then, so the service's is the one that is current. The root is
// built without a service in tests, which is what the fallback is for.
func (a App) login() string {
	if a.svc != nil {
		return a.svc.Cfg.GitHubUser
	}
	return a.cfg.GitHubUser
}

func (a App) record(id string) (review.Record, bool) {
	i := slices.IndexFunc(a.dash.Records, func(rec review.Record) bool { return rec.ID == id })
	if i < 0 {
		return review.Record{}, false
	}
	return a.dash.Records[i], true
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
// saw. reconcile and refreshAll go through loaded instead. Each makes its
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
	return loaded(func() ([]review.Record, error) { return svc.RefreshAll(context.Background()) })
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

// notesChrome is what View draws around the notes pane: the title, the blank
// line under it, the record header, the notes path, the blank line under that,
// the blank line below the pane, the status line and its blank line, and the
// footer. Changing View's layout means changing this count.
const notesChrome = 9

// notesHeight is the room the notes pane gets. It is never cached, because a
// WindowSizeMsg follows every return from a child process.
func (a App) notesHeight() int {
	return max(a.height-notesChrome, 1)
}

// paneChrome is what View draws around the help pane and the requests list:
// the title, the blank line under it, the blank line below the pane, the status
// line and its blank line, and the footer. Changing View's layout means changing
// this count.
const paneChrome = 6

// paneHeight is the room the help pane and the requests list get. Either is
// long enough to overflow an ordinary terminal on its own.
func (a App) paneHeight() int {
	return max(a.height-paneChrome, 1)
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

// startBatch prepares and starts each pull request in turn. It does not run them
// side by side, because each Prepare makes several calls to GitHub and may clone
// a repository. Starting one is quick, since the engine's background mode
// returns straight away. It reports once for the whole batch, because the
// detectedMsg a single start answers with would overwrite the status line once
// per pull request.
func (a App) startBatch(urls []string, engineName string) tea.Cmd {
	svc := a.svc
	return func() tea.Msg {
		if err := checkEngine(engineName); err != nil {
			return errMsg{err: err}
		}
		ctx := context.Background()
		var done batchStartedMsg
		for _, url := range urls {
			if err := startOne(ctx, svc, url, engineName); err != nil {
				done.failed = append(done.failed, err.Error())
				continue
			}
			done.started++
		}
		return done
	}
}

func startOne(ctx context.Context, svc *session.Service, url, engineName string) error {
	ref, err := pr.ParseRef(url, "")
	if err != nil {
		return err
	}
	rec, _, err := svc.Prepare(ctx, ref, engineName, review.ModeBackground, review.IntentReview)
	if err != nil {
		return fmt.Errorf("%s: %w", ref, err)
	}
	if _, err := svc.StartBackground(ctx, rec); err != nil {
		return fmt.Errorf("%s: %w", ref, err)
	}
	return nil
}

// explainBatch is the dry-run counterpart of startBatch. Like startBatch, it
// reports a pull request that fails and goes on to the next.
func (a App) explainBatch(urls []string, engineName string) tea.Cmd {
	svc := a.svc
	return func() tea.Msg {
		var lines []string
		for _, url := range urls {
			lines = append(lines, explainOne(svc, url, engineName))
		}
		return statusMsg{text: strings.Join(lines, "\n")}
	}
}

func explainOne(svc *session.Service, url, engineName string) string {
	ref, err := pr.ParseRef(url, "")
	if err != nil {
		return err.Error()
	}
	plan, spec, err := svc.Explain(context.Background(), ref, engineName, review.ModeBackground, review.IntentReview)
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
		spec, err := svc.ExplainRereview(rec, intent, mode)
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
		return fmt.Sprintf("%s submitted and archived. Notes stay at %s", rec.Ref, rec.NotesPath)
	case review.StateDrafted:
		if rec.Adopted() {
			return fmt.Sprintf("%s has a pending review. Press s to submit it or c to ask about it", rec.Ref)
		}
		return fmt.Sprintf("%s has a pending review. Press enter to keep going", rec.Ref)
	case review.StateReviewed:
		return fmt.Sprintf("%s has your review notes. Press c to ask about them or u to review it again", rec.Ref)
	case review.StateUnreviewed:
		return fmt.Sprintf("%s has no review of yours on GitHub. Nothing was cleaned up", rec.Ref)
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
		a.dash.Background = polled.notes
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
		return bgPolledMsg{records: records, notes: notesFor(statuses), err: err}
	}
}

// notesFor renders each status for the dashboard, which holds no engine to ask.
// The activity is what says a session is waiting at a permission prompt rather
// than working, so both go on the row.
func notesFor(statuses map[string]engine.BGStatus) map[string]string {
	notes := make(map[string]string, len(statuses))
	for id, status := range statuses {
		notes[id] = strings.TrimSpace(status.State + " " + status.Activity)
	}
	return notes
}

// startBackground launches a review that runs without the terminal. The poll
// that follows is the one detectedMsg arms for any record that comes back
// running, so this adds none of its own: two would read the index and the agent
// twice for one keystroke.
func (a App) startBackground(rec review.Record) tea.Cmd {
	svc := a.svc
	return detected(func() (review.Record, error) {
		return svc.StartBackground(context.Background(), rec)
	})
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
