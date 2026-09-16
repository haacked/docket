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

	tea "charm.land/bubbletea/v2"

	"github.com/haacked/docket/internal/core/config"
	"github.com/haacked/docket/internal/core/engine"
	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/core/session"
	"github.com/haacked/docket/internal/core/tier"
	"github.com/haacked/docket/internal/tui/msg"
	"github.com/haacked/docket/internal/tui/screens/dashboard"
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

	width  int
	height int
	status string
	err    error
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
			cfg.DefaultEngine,
			cfg.DefaultRepo,
		),
		sub:   submit.New(submit.Styles{Label: s.Label, Dim: s.Dim, Selected: s.Selected}),
		notes: notes.New(notes.Styles{Label: s.Label, Dim: s.Dim}),
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
	if a.dryRun {
		return tea.Batch(a.loadRecords(), requestBackground)
	}
	return tea.Batch(a.loadRecords(), a.reconcile(), requestBackground)
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

	case msg.StartReview:
		if a.dryRun {
			return a, a.explainStart(message.Input, message.Engine)
		}
		return a, a.prepare(message.Input, message.Engine)

	case msg.Resume:
		rec, ok := a.record(message.ID)
		if !ok {
			return a, nil
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
		return a, nil

	case preparedMsg:
		a.screen = msg.Dashboard
		a.status = message.plan.Description()
		return a, a.launch(message.record, false)

	case launchMsg:
		return a.handoff(message)

	case childExitedMsg:
		a.dash.Busy[message.record.ID] = "reading GitHub"
		// A session can leave the terminal dirty on its way out, so repaint
		// before anything else draws.
		return a, tea.Batch(tea.ClearScreen, a.afterExit(message.record, message.err))

	case detectedMsg:
		delete(a.dash.Busy, message.record.ID)
		a.status = describe(message.record)
		// A submit that worked has nothing left on its screen to look at.
		if message.submitted {
			a.screen = msg.Dashboard
			a.sub = a.sub.ClearBusy()
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
	return a, tea.ExecProcess(cmd, func(err error) tea.Msg {
		if launch.editor {
			return editorExitedMsg{record: launch.record, err: err}
		}
		return childExitedMsg{record: launch.record, err: err}
	})
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

func (a App) loadRecords() tea.Cmd {
	svc := a.svc
	return loaded(svc.Records)
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

func (a App) abandon(rec review.Record) tea.Cmd {
	svc := a.svc
	return detected(func() (review.Record, error) { return svc.Abandon(rec) })
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
		return launchMsg{record: rec, spec: spec, editor: true}
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

func (a App) prepare(input, engineName string) tea.Cmd {
	svc, defaultRepo := a.svc, a.cfg.DefaultRepo
	return func() tea.Msg {
		ref, err := pr.ParseRef(input, defaultRepo)
		if err != nil {
			return errMsg{err: err}
		}
		// Startup checks the default engine's binary, and the new review screen
		// offers the others too. Looking this one up before Prepare is what keeps
		// a missing binary from costing a tier-2 clone first.
		eng, err := engine.For(engineName)
		if err != nil {
			return errMsg{err: err}
		}
		if _, err := osexec.LookPath(eng.Binary()); err != nil {
			return errMsg{err: fmt.Errorf("%s is not on your PATH", eng.Binary())}
		}
		rec, plan, err := svc.Prepare(context.Background(), ref, engineName)
		if err != nil {
			return errMsg{err: err}
		}
		return preparedMsg{record: rec, plan: plan}
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

// explainStart is the dry-run counterpart of prepare. It reports the tier and the
// command that would launch, and writes nothing.
func (a App) explainStart(input, engineName string) tea.Cmd {
	svc, defaultRepo := a.svc, a.cfg.DefaultRepo
	return func() tea.Msg {
		ref, err := pr.ParseRef(input, defaultRepo)
		if err != nil {
			return errMsg{err: err}
		}
		plan, spec, err := svc.Explain(context.Background(), ref, engineName)
		if err != nil {
			return errMsg{err: err}
		}
		return statusMsg{text: plan.Description() + "\nWould run: " + spec.String()}
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
		return fmt.Sprintf("%s submitted and archived. Notes stay at %s", rec.Ref, rec.NotesPath)
	case review.StateDrafted:
		return fmt.Sprintf("%s has a pending review. Press enter to keep going", rec.Ref)
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
