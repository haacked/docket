package session

import (
	"context"
	"fmt"

	"github.com/haacked/docket/internal/core/engine"
	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/review"
)

// StartBackground launches a review that runs without the terminal. It records
// the session as started before running anything, so a docket that dies between
// the two leaves a row the user can see rather than an agent nobody knows about.
//
// The id arrives afterwards. claude mints its own for a background session, so
// the record carries none until the command reports one.
func (s *Service) StartBackground(ctx context.Context, rec review.Record) (review.Record, error) {
	bg, ok := engine.Background(rec.Engine)
	if !ok {
		return rec, fmt.Errorf("%s cannot run a review in the background", rec.Engine)
	}
	if err := checkDir(rec); err != nil {
		return rec, err
	}

	rec.Mode = review.ModeBackground
	rec.StartedAt = s.now()
	rec.State = review.StateReviewing
	rec.Err = ""
	if err := s.append(rec); err != nil {
		return rec, err
	}

	res, err := s.Runner.Run(ctx, bg.StartBackground(rec, s.enginePaths()))
	if err != nil {
		return s.recordErr(rec, err)
	}
	id, err := bg.ParseBackgroundID(res)
	if err != nil {
		return s.recordErr(rec, err)
	}
	rec.BGID = id
	return rec, s.append(rec)
}

// PollBackground asks each agent how its sessions are doing and reads GitHub for
// the ones that have finished.
//
// A poll runs as its own command while the user keeps typing, so an abandon can
// land between the read here and the write that follows, and the poll then
// writes a running record over the abandoned one. Closing that needs a
// compare-and-append the store does not have, which is the same window Prepare
// documents. It returns every record, so the caller redraws
// from one list, plus the status of each session still running, keyed by record.
//
// A session the agent no longer lists counts as finished. The user can delete one
// from outside docket, and a record that waits for a session nobody holds would
// wait forever.
func (s *Service) PollBackground(ctx context.Context) ([]review.Record, map[string]engine.BGStatus, error) {
	records, err := s.Records()
	if err != nil {
		return nil, nil, err
	}
	// One listing answers for every record of an engine, so the records are
	// grouped rather than asked about one at a time.
	byEngine := map[string][]int{}
	for i, rec := range records {
		if rec.BackgroundRunning() {
			byEngine[rec.Engine] = append(byEngine[rec.Engine], i)
		}
	}

	statuses := make(map[string]engine.BGStatus)
	var failure error
	for name, indexes := range byEngine {
		bg, ok := engine.Background(name)
		if !ok {
			continue
		}
		found, err := s.statuses(ctx, bg)
		if err != nil {
			// The agent is the only thing that can answer, so its records stay
			// as they are and the next tick asks again.
			failure = err
			continue
		}
		for _, i := range indexes {
			rec, status, running := s.applyStatus(ctx, records[i], found)
			records[i] = rec
			if running {
				statuses[rec.ID] = status
			}
		}
	}
	return records, statuses, failure
}

func (s *Service) statuses(ctx context.Context, bg engine.BackgroundEngine) (map[string]engine.BGStatus, error) {
	res, err := s.Runner.Run(ctx, bg.StatusSpec(s.enginePaths()))
	if err != nil {
		return nil, err
	}
	return bg.ParseStatus(res)
}

// applyStatus moves one record on by what the agent said about it, and reports
// the status of a session still running.
//
// A running session records its id the first time the agent names it, and
// nothing more. The status itself is for the screen, not the log, so a poll every
// few seconds does not grow the index.
func (s *Service) applyStatus(ctx context.Context, rec review.Record, found map[string]engine.BGStatus) (review.Record, engine.BGStatus, bool) {
	before := rec.SessionID
	status, over := readStatus(rec, found)
	rec.SessionID = status.SessionID
	if over {
		// detectPolled writes what it decides, so an id learned on this same poll
		// is saved with it.
		return s.detectPolled(ctx, rec), status, false
	}
	if status.SessionID != before {
		if err := s.append(rec); err != nil {
			rec.Err = err.Error()
		}
	}
	return rec, status, true
}

// readStatus answers what a poll and a closed session both ask: is this session
// over, and what has the agent said about it. A session the agent no longer
// lists counts as over. A record waiting on a session nobody holds would wait
// forever.
//
// The status carries the record's own id when the agent named none, so a caller
// that adopts it never blanks the id the record already had.
func readStatus(rec review.Record, found map[string]engine.BGStatus) (engine.BGStatus, bool) {
	status, known := found[rec.BGID]
	if status.SessionID == "" {
		status.SessionID = rec.SessionID
	}
	return status, !known || status.Done
}

// OpenBackgroundSpec hands a background session back to the terminal.
//
// It stamps no new start time and takes no fresh snapshot of the reviews on
// GitHub, which a resume does. This is the session the record already describes,
// so the window detection measures against is still the right one.
func (s *Service) OpenBackgroundSpec(ctx context.Context, rec review.Record) (exec.CommandSpec, error) {
	bg, ok := engine.Background(rec.Engine)
	if !ok {
		return exec.CommandSpec{}, fmt.Errorf("%s runs no background sessions", rec.Engine)
	}
	if err := checkDir(rec); err != nil {
		return exec.CommandSpec{}, err
	}

	// Which verb opens the session depends on whether the agent still holds it,
	// so the status is read now rather than taken from the last poll.
	found, err := s.statuses(ctx, bg)
	if err != nil {
		return exec.CommandSpec{}, err
	}
	spec, ok := bg.OpenSpec(rec, found[rec.BGID], s.enginePaths())
	if !ok {
		return exec.CommandSpec{}, fmt.Errorf("%s has no session left to open", rec.Ref)
	}
	return spec, nil
}

// afterBackgroundExit runs when the user closes a background session they had
// opened. Detecting here the way an interactive exit does would read GitHub for
// a review still being written and land on unreviewed, which also takes the
// record out of the poll. So the agent is asked first, and only a session that
// has finished is detected.
func (s *Service) afterBackgroundExit(ctx context.Context, rec review.Record) (review.Record, error) {
	bg, ok := engine.Background(rec.Engine)
	if !ok || rec.BGID == "" {
		return s.detect(ctx, rec)
	}
	found, err := s.statuses(ctx, bg)
	if err != nil {
		// Nothing says the session is over, so the record keeps running and the
		// next poll asks again.
		return rec, nil
	}

	status, over := readStatus(rec, found)
	rec.SessionID = status.SessionID
	if over {
		return s.detect(ctx, rec)
	}
	// The user left the session working. Opening it may also have restarted a
	// review that detection had already closed, so the record goes back to
	// running and the poll picks it up again.
	rec.State = review.StateReviewing
	return rec, s.append(rec)
}

// stopBackground ends the agent session behind a record, so nothing is still
// writing when the clone under it is deleted.
//
// It asks whether a session exists rather than whether one is running. The agent
// goes on holding a session after the review it ran has finished, so a record
// abandoned once it was drafted still has one to end.
//
// A failure is recorded and does not stop the abandon. The user asked to be rid
// of the record, and a row that cannot be abandoned because its agent will not
// answer is worse than an agent left running.
func (s *Service) stopBackground(ctx context.Context, rec review.Record) review.Record {
	bg, ok := engine.Background(rec.Engine)
	if !ok || !rec.HasBackgroundSession() {
		return rec
	}
	if _, err := s.Runner.Run(ctx, bg.StopSpec(rec, s.enginePaths())); err != nil {
		rec.Err = fmt.Sprintf("stop the background session: %v", err)
	}
	return rec
}
