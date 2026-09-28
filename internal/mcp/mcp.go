// Package mcp serves docket's reviews to an agent session over the Model Context
// Protocol. It starts background reviews, lists the open ones, and submits a
// drafted one through the same session.Service the TUI drives.
package mcp

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/haacked/docket/internal/core/engine"
	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/core/session"
)

// Review is one docket record as the tools report it.
type Review struct {
	PullRequest string    `json:"pull_request" jsonschema:"the pull request as org/repo#number"`
	URL         string    `json:"url"`
	Title       string    `json:"title"`
	Author      string    `json:"author"`
	State       string    `json:"state" jsonschema:"where the review stands in docket"`
	PRState     string    `json:"pr_state,omitempty" jsonschema:"OPEN, MERGED, or CLOSED when docket last read GitHub"`
	Engine      string    `json:"engine"`
	Mode        string    `json:"mode" jsonschema:"background or interactive"`
	StartedAt   time.Time `json:"started_at,omitzero"`
	ReviewID    int64     `json:"review_id,omitempty" jsonschema:"the id of the review on GitHub"`
	Submittable bool      `json:"submittable" jsonschema:"whether submit_review can submit the review now"`
	NotesPath   string    `json:"notes_path,omitempty" jsonschema:"the notes file review-code writes for this pull request"`
	Error       string    `json:"error,omitempty" jsonschema:"what failed the last time docket acted on the review"`
	Progress    *Progress `json:"progress,omitempty" jsonschema:"what the background session is doing, present while it runs"`
}

// Progress is what the agent says about a running background session.
type Progress struct {
	Detail    string    `json:"detail,omitempty" jsonschema:"the agent's one-line summary of what the session is doing"`
	Waiting   bool      `json:"waiting" jsonschema:"whether the session has stopped until the user answers it"`
	Needs     string    `json:"needs,omitempty" jsonschema:"what a waiting session needs from the user, when the agent named it"`
	Agents    int       `json:"agents" jsonschema:"how many reviewer agents the session is running"`
	UpdatedAt time.Time `json:"updated_at,omitzero" jsonschema:"when the agent last updated its summary"`
}

type listOutput struct {
	Reviews   []Review `json:"reviews"`
	PollError string   `json:"poll_error,omitempty" jsonschema:"why docket could not ask the agent about running sessions, whose rows may then be out of date"`
}

// target is the pull request a tool acts on.
type target struct {
	PullRequest string `json:"pull_request" jsonschema:"a pull request URL, org/repo#123, or a bare number when docket's default_repo is set"`
}

func (t target) ref(defaultRepo string) (pr.Ref, error) {
	return pr.ParseRef(t.PullRequest, defaultRepo)
}

type startInput struct {
	target
	Existing review.Intent `json:"existing,omitempty" jsonschema:"what to do when the pull request already has review notes or a review of yours: append reviews what changed since, overwrite reviews the whole pull request again"`
}

type startOutput struct {
	Review Review `json:"review"`
	Plan   string `json:"plan,omitempty" jsonschema:"where the review runs and what docket cloned for it"`
}

type submitInput struct {
	target
	Event string `json:"event" jsonschema:"COMMENT, APPROVE, or REQUEST_CHANGES"`
	Body  string `json:"body,omitempty" jsonschema:"the review's summary; leave it out to keep the summary review-code posted with the draft"`
}

const instructions = `docket runs pull request reviews through the review-code skill in background claude sessions and tracks each one until its review is submitted.

start_review starts a review. list_reviews shows every open review and reads the agent and GitHub to move a finished session on, so call it rather than assuming a review is still running. A review is ready to submit once list_reviews reports it submittable, which means review-code posted a pending review on GitHub. submit_review submits it and archives the record.

Submitting publishes the review on GitHub under the user's name. Confirm the event and the body with the user before calling submit_review.`

const listDescription = `Lists the reviews docket has open, newest first, with what each running background session is doing.

It asks claude about every running session and reads GitHub for the ones that have finished, so a session that posted its draft moves to drafted here. The states are: preparing (docket is setting the review up, or setting it up failed when error is set), reviewing (a session is running), drafted (a pending review is on GitHub), unreviewed (the last session posted nothing new), reviewed (an existing review was adopted with nothing pending), and not_started (claude refused to start the session; error says why). submittable says whether submit_review can submit a review now. A session whose progress reads waiting has stopped until the user answers it, which they do by opening docket and pressing enter on the row. No tool abandons a review; the user presses x on its row in docket.`

const startDescription = `Starts a review-code review of a pull request in a background claude session and returns straight away. The review runs for minutes; call list_reviews to follow it.

When the pull request already has review notes or a review of yours, this refuses and says what it found. Ask the user whether to append (review what changed since) or overwrite (review the whole pull request again), then call it again with existing set to the answer.

claude runs a background session only in a directory someone has trusted, and it asks that question only in a terminal. When it refuses, the error names the directory. The user runs claude there once and accepts the trust prompt, then start_review starts the same review again. Each pull request docket clones gets a directory of its own, so this can happen once for each of them.

A pull request docket already has open is refused. The error says how the user clears it.`

const submitDescription = `Submits the pending review docket is tracking for a pull request, with COMMENT, APPROVE, or REQUEST_CHANGES, and archives the record.

The review must be submittable in list_reviews. Leave body out to keep the summary review-code posted with the draft. GitHub refuses an approval of your own pull request. This publishes the review under the user's name and cannot be undone, so confirm the event and the body with the user first.`

type server struct {
	svc    *session.Service
	engine string
	// mu runs one tool call at a time within this process. A poll reads the
	// index and writes what it decided in separate steps. The poll's write would
	// overwrite a start or a submit that landed between the two. mu does not
	// coordinate with the TUI or with another docket mcp process, although they
	// share the index. PollBackground documents that window.
	mu sync.Mutex
}

// New builds the server. Every review it starts runs under engineName.
// engineName must have a background mode, because the server has no terminal
// to give a session.
func New(svc *session.Service, engineName string) *sdk.Server {
	s := &server{svc: svc, engine: engineName}
	srv := sdk.NewServer(
		&sdk.Implementation{Name: "docket", Version: version()},
		&sdk.ServerOptions{Instructions: instructions},
	)
	sdk.AddTool(srv, &sdk.Tool{
		Name:        "list_reviews",
		Description: listDescription,
		Annotations: &sdk.ToolAnnotations{IdempotentHint: true},
	}, s.list)
	sdk.AddTool(srv, &sdk.Tool{
		Name:        "start_review",
		Description: startDescription,
		InputSchema: schema[startInput]("existing", "", string(review.IntentAppend), string(review.IntentOverwrite)),
	}, s.start)
	sdk.AddTool(srv, &sdk.Tool{
		Name:        "submit_review",
		Description: submitDescription,
		InputSchema: schema[submitInput]("event", review.SubmitEvents...),
	}, s.submit)
	return srv
}

// Serve runs the server on stdin and stdout until the client disconnects.
func Serve(ctx context.Context, svc *session.Service, engineName string) error {
	return New(svc, engineName).Run(ctx, &sdk.StdioTransport{})
}

// schema is the input schema inferred from In, with property limited to values.
// The struct tags carry only descriptions and cannot express the limit.
func schema[In any](property string, values ...string) *jsonschema.Schema {
	s, err := jsonschema.For[In](nil)
	if err != nil {
		panic(fmt.Sprintf("infer the input schema: %v", err))
	}
	prop := s.Properties[property]
	for _, v := range values {
		prop.Enum = append(prop.Enum, v)
	}
	return s
}

func version() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "(devel)"
}

func (s *server) list(ctx context.Context, _ *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, listOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	records, statuses, err := s.svc.PollBackground(ctx)
	if records == nil {
		return nil, listOutput{}, err
	}
	out := listOutput{Reviews: []Review{}}
	if err != nil {
		out.PollError = err.Error()
	}
	for _, rec := range records {
		if rec.State.Open() {
			out.Reviews = append(out.Reviews, view(rec, statuses))
		}
	}
	return nil, out, nil
}

func (s *server) start(ctx context.Context, _ *sdk.CallToolRequest, in startInput) (*sdk.CallToolResult, startOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ref, err := in.ref(s.svc.Cfg.DefaultRepo)
	if err != nil {
		return nil, startOutput{}, err
	}
	records, err := s.svc.Records()
	if err != nil {
		return nil, startOutput{}, err
	}
	// A launch that claude refused leaves its record open. Existing refuses an
	// open record. The dashboard's enter also starts such a record again.
	if rec, ok := review.OpenRecord(records, ref); ok {
		if rec.State != review.StateNotStarted {
			return nil, startOutput{}, alreadyOpen(rec)
		}
		rec, err := s.svc.Restart(ctx, rec)
		return started(rec, "", err)
	}

	found, err := s.svc.Existing(ctx, ref)
	if err != nil {
		return nil, startOutput{}, err
	}
	intent := review.IntentReview
	if found.Any() {
		if in.Existing == "" {
			return nil, startOutput{}, askExisting(ref, found)
		}
		intent = in.Existing
	}

	rec, plan, err := s.svc.Prepare(ctx, ref, s.engine, review.ModeBackground, intent)
	// Prepare records the review before it provisions it, so a failure after that
	// leaves a record open that no tool can clear.
	if err != nil && rec.ID != "" {
		return nil, startOutput{}, fmt.Errorf("%w. %s", err, startOver)
	}
	if err != nil {
		return nil, startOutput{}, err
	}
	rec, err = s.svc.StartBackground(ctx, rec)
	return started(rec, plan.Description(), err)
}

// startOver is how the user clears an open review that no tool can move on.
const startOver = "To start over, open docket and press x on the review's row to abandon it, then call start_review again."

// alreadyOpen refuses a start for a pull request that docket already tracks, and
// names the next step for the state the review is in. No tool abandons a
// record, so a review that cannot move on names the dashboard key that does.
func alreadyOpen(rec review.Record) error {
	msg := fmt.Sprintf("docket already has a review of %s open, and it is %s", rec.Ref, rec.State.Label())
	if rec.Err != "" {
		msg += " after this error: " + rec.Err
	}
	switch {
	case rec.Submittable():
		return fmt.Errorf("%s. submit_review submits it", msg)
	case rec.State == review.StateReviewing, rec.State == review.StatePreparing && rec.Err == "":
		return fmt.Errorf("%s. list_reviews shows where it stands", msg)
	default:
		return fmt.Errorf("%s. %s", msg, startOver)
	}
}

// started reports a background launch. A launch that claude refused for trust
// says what the user has to run, because no terminal here can ask them.
func started(rec review.Record, plan string, err error) (*sdk.CallToolResult, startOutput, error) {
	switch {
	case errors.Is(err, session.ErrUntrusted):
		return nil, startOutput{}, fmt.Errorf("%w. %s asks that question only in a terminal, so run %s in that directory once and accept its trust prompt, then call start_review again", err, rec.Engine, rec.Engine)
	case err != nil && rec.State == review.StateNotStarted:
		return nil, startOutput{}, fmt.Errorf("%w. start_review tries it again. %s", err, startOver)
	case err != nil && rec.State == review.StateReviewing:
		// StartBackground leaves the record reviewing when the launch exited zero
		// without an id docket could read. The poll then looks for the session.
		return nil, startOutput{}, fmt.Errorf("%w. A session may have started anyway, and list_reviews finds it", err)
	case err != nil:
		return nil, startOutput{}, err
	}
	return nil, startOutput{Review: view(rec, nil), Plan: plan}, nil
}

// askExisting refuses a start that has to choose what to do with the review that
// is already there. review-code would otherwise ask the same question in a
// background session where nobody can answer it.
func askExisting(ref pr.Ref, found review.Found) error {
	return fmt.Errorf("%s already has a review: %s. Call start_review again with existing set to append, to review what changed since, or to overwrite, to review the whole pull request again", ref, strings.Join(found.Phrases(), ", "))
}

// poll reads the agent and GitHub for rec and returns what became of it. A poll
// that cannot reach the agent still returns the records, so its failure is
// reported only when rec is still not submittable.
func (s *server) poll(ctx context.Context, rec review.Record) (review.Record, error) {
	records, _, pollErr := s.svc.PollBackground(ctx)
	if records == nil {
		return rec, pollErr
	}
	polled, ok := review.OpenRecord(records, rec.Ref)
	if !ok {
		return rec, fmt.Errorf("docket archived its review of %s while it read GitHub, because the review was already submitted or the pull request merged or closed", rec.Ref)
	}
	if pollErr != nil && !polled.Submittable() {
		return polled, fmt.Errorf("docket could not ask %s whether the session finished: %w", polled.Engine, pollErr)
	}
	return polled, nil
}

func (s *server) submit(ctx context.Context, _ *sdk.CallToolRequest, in submitInput) (*sdk.CallToolResult, Review, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ref, err := in.ref(s.svc.Cfg.DefaultRepo)
	if err != nil {
		return nil, Review{}, err
	}
	records, err := s.svc.Records()
	if err != nil {
		return nil, Review{}, err
	}
	rec, ok := review.OpenRecord(records, ref)
	if !ok {
		return nil, Review{}, fmt.Errorf("docket has no open review of %s; start_review starts one", ref)
	}
	// Only a poll moves a finished background session to drafted. Without one, a
	// review whose draft is already on GitHub still reads as reviewing and Submit
	// refuses it.
	if !rec.Submittable() && rec.InBackgroundSession() {
		if rec, err = s.poll(ctx, rec); err != nil {
			return nil, Review{}, err
		}
	}
	rec, err = s.svc.Submit(ctx, rec, in.Event, in.Body)
	if err != nil {
		return nil, Review{}, err
	}
	return nil, view(rec, nil), nil
}

// view is the record as the tools report it. statuses holds the poll's answer
// for each session still running, keyed by record id.
func view(rec review.Record, statuses map[string]engine.BGStatus) Review {
	out := Review{
		PullRequest: rec.Ref.String(),
		URL:         rec.URL,
		Title:       rec.Title,
		Author:      rec.Author,
		State:       string(rec.State),
		PRState:     string(rec.PRState),
		Engine:      rec.Engine,
		Mode:        string(rec.Mode),
		StartedAt:   rec.StartedAt,
		ReviewID:    rec.ReviewID,
		Submittable: rec.Submittable(),
		NotesPath:   rec.NotesPath,
		Error:       rec.Err,
	}
	if status, ok := statuses[rec.ID]; ok {
		p := status.Progress
		out.Progress = &Progress{
			Detail:    p.Detail,
			Waiting:   status.Waiting(),
			Agents:    p.Agents,
			UpdatedAt: p.UpdatedAt,
		}
		// claude's own reading of the conversation can name a need while the
		// session's reviewer agents still run.
		if out.Progress.Waiting {
			out.Progress.Needs = p.Needs
		}
	}
	return out
}
