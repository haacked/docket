package session

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

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
	bg, err := background(rec)
	if err != nil {
		return rec, err
	}

	rec.Mode = review.ModeBackground
	rec.StartedAt = s.now()
	rec.State = review.StateReviewing
	rec.Err = ""
	if err := s.append(rec); err != nil {
		return rec, err
	}

	res, err := s.Runner.Run(ctx, bg.StartBackground(rec, s.enginePaths(rec.ClaudeConfigDir)))
	if err != nil {
		if bg.Untrusted(res) {
			err = fmt.Errorf("%s %w %s yet", rec.Engine, ErrUntrusted, rec.Dir)
		}
		// No session ran, so the record leaves StateReviewing. The poll and a
		// refresh then leave the record and the reason alone.
		rec.State = review.StateNotStarted
		return s.recordErr(rec, err)
	}
	// A launch that exited zero may have started a session even when docket
	// cannot read its id. The record therefore stays reviewing, and recoverLost
	// looks for the session.
	id, err := bg.ParseBackgroundID(res)
	if err != nil {
		return s.recordErr(rec, err)
	}
	rec.BGID = id
	return rec, s.append(rec)
}

// ErrUntrusted marks a background launch the agent refused because nobody has
// told it to trust the directory. The agent asks that question only on a
// terminal. The caller can hand the terminal over with TrustSpec and launch
// again.
var ErrUntrusted = errors.New("does not trust")

// background returns the engine that runs rec in the background, once rec's
// directory is there to run in.
func background(rec review.Record) (engine.BackgroundEngine, error) {
	bg, ok := engine.Background(rec.Engine)
	if !ok {
		return nil, fmt.Errorf("%s runs no background sessions", rec.Engine)
	}
	return bg, checkDir(rec)
}

// TrustSpec puts the agent's trust prompt for the record's directory on the
// terminal. It runs after a launch fails with ErrUntrusted.
func (s *Service) TrustSpec(rec review.Record) (exec.CommandSpec, error) {
	bg, err := background(rec)
	if err != nil {
		return exec.CommandSpec{}, err
	}
	return bg.TrustSpec(rec.Dir, s.enginePaths(rec.ClaudeConfigDir)), nil
}

// Restart launches again a background review the agent refused. It reads
// GitHub first, as Rereview does. A refresh skips a row that never started, so
// the stored pull request state and the snapshot of my earlier reviews are as old
// as the refused launch.
func (s *Service) Restart(ctx context.Context, rec review.Record) (review.Record, error) {
	if rec.State != review.StateNotStarted {
		return rec, fmt.Errorf("%s is %s, so there is no refused launch to start again", rec.Ref, rec.State)
	}
	pull, err := s.openPR(ctx, rec.Ref)
	if err != nil {
		return rec, err
	}
	rec.PRState = pull.State
	rec, _, err = s.snapshot(ctx, rec)
	if err != nil {
		return rec, err
	}
	return s.StartBackground(ctx, rec)
}

// ExplainRestart is the command Restart would run, without recording anything.
// It reads the pull request's state the way Restart does, so the dry run refuses
// what the real run refuses.
func (s *Service) ExplainRestart(ctx context.Context, rec review.Record) (exec.CommandSpec, error) {
	if _, err := s.openPR(ctx, rec.Ref); err != nil {
		return exec.CommandSpec{}, err
	}
	bg, err := background(rec)
	if err != nil {
		return exec.CommandSpec{}, err
	}
	return bg.StartBackground(rec, s.enginePaths(rec.ClaudeConfigDir)), nil
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
	// One listing answers for every record of an engine and account. The poll
	// therefore groups the records rather than asking about each one.
	groups := map[listing][]int{}
	for i, rec := range records {
		if rec.BackgroundRunning() {
			key := listingOf(rec)
			groups[key] = append(groups[key], i)
		}
	}

	// A record whose launch never got its id written needs one before it can be
	// asked about, so recovery runs first and folds its finds into the groups.
	records, groups = s.recoverLost(ctx, records, groups)

	statuses := make(map[string]engine.BGStatus)
	var failure error
	for key, indexes := range groups {
		bg, ok := engine.Background(key.engine)
		if !ok {
			continue
		}
		paths := s.enginePaths(key.claudeConfig)
		found, err := s.statuses(ctx, bg, paths)
		if err != nil {
			// The agent is the only thing that can answer, so its records stay
			// as they are and the next tick asks again.
			failure = err
			continue
		}
		for _, i := range indexes {
			rec, status, running := s.applyStatus(ctx, records[i], found)
			if running {
				status.Progress = bg.Progress(rec.BGID, paths)
				if status.Idle {
					rec, running = s.settleIdle(ctx, rec, status)
				} else {
					s.forgetIdle(rec.BGID)
				}
			}
			records[i] = rec
			if running {
				statuses[rec.ID] = status
			}
		}
	}
	return records, statuses, failure
}

// listing names one agent listing: an engine, and the claude account whose
// sessions it lists.
type listing struct {
	engine       string
	claudeConfig string
}

func listingOf(rec review.Record) listing {
	return listing{engine: rec.Engine, claudeConfig: rec.ClaudeConfigDir}
}

// recoverLost adopts the sessions that background records launched but never
// recorded. StartBackground writes the id in a second append, so a docket killed
// between the launch and that write leaves a record naming no session, which
// BackgroundRunning excludes from every poll: the agent runs on with nobody
// watching and nothing to stop it with.
//
// A record with no session to find is one whose launch failed before it started
// anything. Detection closes it rather than leaving a row that reads as running
// for ever.
func (s *Service) recoverLost(ctx context.Context, records []review.Record, groups map[listing][]int) ([]review.Record, map[listing][]int) {
	lost := map[listing][]int{}
	for i, rec := range records {
		if rec.Mode == review.ModeBackground && rec.State == review.StateReviewing && rec.BGID == "" {
			key := listingOf(rec)
			lost[key] = append(lost[key], i)
		}
	}

	for key, indexes := range lost {
		bg, ok := engine.Background(key.engine)
		if !ok {
			continue
		}
		res, err := s.Runner.Run(ctx, bg.StatusSpec(s.enginePaths(key.claudeConfig)))
		if err != nil {
			continue
		}
		for _, i := range indexes {
			rec := records[i]
			id, found := bg.RecoverBackgroundID(rec, res)
			// Within launchGrace the launch may still be running in another docket
			// process, which writes the id in its second append. The agent may also
			// not list the session yet.
			if !found && s.now().Sub(rec.StartedAt) < launchGrace {
				continue
			}
			if !found {
				records[i] = s.detectPolled(ctx, rec)
				continue
			}
			rec.BGID = id
			rec.Err = ""
			if err := s.append(rec); err != nil {
				rec.Err = err.Error()
			}
			records[i] = rec
			groups[key] = append(groups[key], i)
		}
	}
	return records, groups
}

// statuses lists the sessions the agent holds under the claude account paths
// names. The agent lists a session only under the account it started under.
func (s *Service) statuses(ctx context.Context, bg engine.BackgroundEngine, paths engine.Paths) (map[string]engine.BGStatus, error) {
	res, err := s.Runner.Run(ctx, bg.StatusSpec(paths))
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
	status, over := s.readStatus(rec, found)
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

// settleIdle settles a session that has ended its turn, and reports whether the
// session is still running. It reads GitHub once for each stretch the session
// stays idle, keyed on the time the agent last updated its account, because a
// session stays idle until the user answers it.
func (s *Service) settleIdle(ctx context.Context, rec review.Record, status engine.BGStatus) (review.Record, bool) {
	s.idleMu.Lock()
	seen, checked := s.idleSeen[rec.BGID]
	if checked && seen.Equal(status.Progress.UpdatedAt) {
		s.idleMu.Unlock()
		return rec, true
	}
	if s.idleSeen == nil {
		s.idleSeen = map[string]time.Time{}
	}
	s.idleSeen[rec.BGID] = status.Progress.UpdatedAt
	s.idleMu.Unlock()

	settled, running, err := s.settle(ctx, rec)
	if err != nil || !running {
		// A failure is tried again on the next poll. A record that moved on
		// may come back to running when the user opens its session, and the
		// next idle stretch then has to be read.
		s.forgetIdle(rec.BGID)
	}
	if err != nil {
		// The error goes on the row the way detectPolled puts it there. It is
		// not written to the index, so the next poll that works clears it.
		rec.Err = err.Error()
		return rec, true
	}
	return settled, running
}

func (s *Service) forgetIdle(id string) {
	s.idleMu.Lock()
	delete(s.idleSeen, id)
	s.idleMu.Unlock()
}

// settle reads GitHub for a session that has ended its turn while the agent
// still holds it, and reports whether the session is still running. The
// session's own draft or submission moves the record on, so the user can act on
// it from docket, and enter still opens the session. A session that ended its
// turn before posting anything keeps running.
func (s *Service) settle(ctx context.Context, rec review.Record) (review.Record, bool, error) {
	decided, reviews, err := s.decide(ctx, rec)
	if err != nil {
		return rec, true, err
	}
	if !ownWork(rec, decided) {
		return rec, true, nil
	}
	saved, err := s.record(ctx, decided, reviews)
	if err != nil {
		return rec, true, err
	}
	return saved, false, nil
}

// ownWork reports whether what detection decided for a session that is still
// running is that session's own draft or submission. Detection reads any
// pending review as a draft, and it counts a submission a little before the
// launch because of clock skew. A finished session is judged that way too, but
// a running one may not have posted anything yet.
//
// A fix review posts nothing. Its session is done once review-code has written
// the notes, which it does after the fix pass. Until then the checkout may hold
// only part of the fixes.
func ownWork(rec, decided review.Record) bool {
	switch decided.State {
	case review.StateFixed, review.StatePushed:
		return fixNotesWritten(rec)
	case review.StateDrafted:
		return decided.ReviewID != rec.PriorPendingID
	case review.StateSubmitted:
		return decided.ReviewID != rec.PriorPendingID && !slices.Contains(rec.PriorReviewIDs, decided.ReviewID)
	default:
		return false
	}
}

// launchGrace is how long after a launch a session the agent does not list, or
// lists with no process, still counts as starting. claude --bg prints the id
// before the session's process starts, and the poll that follows a launch runs
// at once.
const launchGrace = 30 * time.Second

// readStatus answers what a poll and a closed session both ask: is this session
// over, and what has the agent said about it. A session the agent no longer
// lists counts as over. A record waiting on a session nobody holds would wait
// forever. Within launchGrace of the launch, no session is over yet, so the
// first poll does not close a record whose session has not started.
//
// The status carries the record's own id when the agent named none, so a caller
// that adopts it never blanks the id the record already had.
func (s *Service) readStatus(rec review.Record, found map[string]engine.BGStatus) (engine.BGStatus, bool) {
	status, known := found[rec.BGID]
	if status.SessionID == "" {
		status.SessionID = rec.SessionID
	}
	if s.now().Sub(rec.StartedAt) < launchGrace {
		return status, false
	}
	return status, !known || status.Done
}

// OpenBackgroundSpec hands a background session back to the terminal.
//
// It stamps no new start time and takes no fresh snapshot of the reviews on
// GitHub, which a resume does. This is the session the record already describes,
// so the window detection measures against is still the right one.
func (s *Service) OpenBackgroundSpec(ctx context.Context, rec review.Record) (exec.CommandSpec, error) {
	bg, err := background(rec)
	if err != nil {
		return exec.CommandSpec{}, err
	}

	// Which verb opens the session depends on whether the agent still holds it,
	// so the status is read now rather than taken from the last poll.
	paths := s.enginePaths(rec.ClaudeConfigDir)
	found, err := s.statuses(ctx, bg, paths)
	if err != nil {
		return exec.CommandSpec{}, err
	}
	spec, ok := bg.OpenSpec(rec, found[rec.BGID], paths)
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
	found, err := s.statuses(ctx, bg, s.enginePaths(rec.ClaudeConfigDir))
	if err != nil {
		// Nothing says the session is over, so the record keeps running and the
		// next poll asks again.
		return rec, nil
	}

	status, over := s.readStatus(rec, found)
	rec.SessionID = status.SessionID
	if over {
		return s.detect(ctx, rec)
	}
	// A session the user left unanswered keeps the draft it posted. A failed
	// read keeps the row where it was, as detect does for a finished session.
	if status.Idle {
		settled, running, err := s.settle(ctx, rec)
		if err != nil {
			return s.recordErr(rec, err)
		}
		if !running {
			return settled, nil
		}
	}
	// The user left the session working. Opening it may also have restarted a
	// review that detection had already closed, so the record goes back to
	// running and the poll picks it up again.
	rec.State = review.StateReviewing
	return rec, s.append(rec)
}

// stopBackground ends the agent session behind a record and reports whether the
// agent has let go of it. The caller deletes the working tree the session was
// running in, so a false answer means that tree has to stay.
//
// It asks whether a session exists rather than whether one is running. The agent
// goes on holding a session after the review it ran has finished, so a record
// closed once it was drafted still has one to end.
func (s *Service) stopBackground(ctx context.Context, rec review.Record) (review.Record, bool) {
	bg, ok := engine.Background(rec.Engine)
	if !ok || !rec.HasBackgroundSession() {
		return rec, true
	}
	if _, err := s.Runner.Run(ctx, bg.StopSpec(rec, s.enginePaths(rec.ClaudeConfigDir))); err != nil {
		rec.Err = fmt.Sprintf("stop the background session: %v", err)
		return rec, false
	}
	return rec, true
}

// stopFinished is stopBackground for a session the agent does not report as
// working. It answers false for a working session, which it leaves running, and
// for a listing that fails. Either way the record says why.
//
// The listing can read idle just as a new turn starts. Waiting also reads the
// progress file, so this does not stop that session and delete the clone under
// it.
func (s *Service) stopFinished(ctx context.Context, rec review.Record) (review.Record, bool) {
	bg, ok := engine.Background(rec.Engine)
	if !ok || !rec.HasBackgroundSession() {
		return rec, true
	}
	paths := s.enginePaths(rec.ClaudeConfigDir)
	found, err := s.statuses(ctx, bg, paths)
	if err != nil {
		rec.Err = fmt.Sprintf("read the background session's status: %v", err)
		return rec, false
	}
	status, over := s.readStatus(rec, found)
	status.Progress = bg.Progress(rec.BGID, paths)
	if !over && !status.Waiting() {
		rec.Err = "the background session was still working, so docket left it running"
		return rec, false
	}
	return s.stopBackground(ctx, rec)
}
