package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/haacked/docket/internal/core/account"
	"github.com/haacked/docket/internal/core/engine"
	"github.com/haacked/docket/internal/core/review"
)

// unknownReset is how long docket counts an account as over its limit when
// claude did not say when the limit resets.
const unknownReset = time.Hour

// rotatesAccounts reports whether a review under engineName runs under the
// claude account the rotation picks. Only claude has those accounts.
func rotatesAccounts(engineName string) bool {
	return engineName == (engine.Claude{}).Name()
}

// newAccount is the claude account a new record runs under. The rotation picks
// it from the limits claude has reported.
func (s *Service) newAccount() (account.Choice, error) {
	limits, err := account.Load(s.Paths.Limits)
	if err != nil {
		return account.Choice{}, err
	}
	return account.Pick(s.Cfg.ClaudeAccounts(), limits, s.now()), nil
}

// freshAccount puts rec on the claude account a fresh start of it runs under,
// and reports the choice. rec keeps its own account while that account is under
// its limits, so a change to claude_config_dir still applies only to new
// records. An account over its limit gives way to the one the rotation picks.
//
// claude keeps each conversation under the account that ran it. A resume or an
// attach therefore runs under rec's own account and never comes through here.
// A record that moves drops its question-and-answer session, which the new
// account could not resume.
func (s *Service) freshAccount(rec review.Record) (review.Record, account.Choice, error) {
	choice := account.Choice{Account: rec.ClaudeConfigDir}
	// A codex record keeps the question-and-answer session codex can resume.
	if !rotatesAccounts(rec.Engine) {
		return rec, choice, nil
	}
	limits, err := account.Load(s.Paths.Limits)
	if err != nil {
		return rec, account.Choice{}, err
	}
	if account.Limited(rec.ClaudeConfigDir, limits, s.now()) {
		choice = account.Pick(s.Cfg.ClaudeAccounts(), limits, s.now())
	}
	if choice.Account != rec.ClaudeConfigDir {
		rec.ClaudeConfigDir = choice.Account
		rec.AskSessionID = ""
		rec.AskStartedAt = time.Time{}
	}
	return rec, choice, nil
}

// recordLimit adds the limit that a session under rec's account ran into to the
// log the rotation reads. It skips a refusal that the log already holds. The
// poll reads the same refusal again after a stop that fails. Each exit from an
// interactive session that claude refused reads it again too.
func (s *Service) recordLimit(rec review.Record, limit engine.UsageLimit) error {
	limits, err := account.Load(s.Paths.Limits)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(limits, func(l account.Limit) bool {
		return l.Account == rec.ClaudeConfigDir && l.At.Equal(limit.At)
	}) {
		return nil
	}
	until := limit.Resets
	if until.IsZero() {
		until = limit.At.Add(unknownReset)
	}
	return account.Append(s.Paths.Limits, account.Limit{Account: rec.ClaudeConfigDir, At: limit.At, Until: until})
}

// noteLimit records the usage limit that an interactive session ran into, so the
// next fresh start moves to another account. Nothing starts again here, because
// the user saw claude's refusal in the terminal.
func (s *Service) noteLimit(rec review.Record, sessionID string) review.Record {
	eng, err := engine.For(rec.Engine)
	if err != nil {
		return rec
	}
	limit, ok := eng.UsageLimit(sessionID, s.enginePaths(rec.ClaudeConfigDir))
	if !ok {
		return rec
	}
	if err := s.recordLimit(rec, limit); err != nil {
		rec.Err = err.Error()
		return rec
	}
	rec.Err = fmt.Sprintf("%s is over its usage limit until %s", accountName(rec.ClaudeConfigDir), when(limit.Resets))
	return rec
}

// startElsewhere starts a background review again under another claude account,
// after claude refused the review's session for its account's usage limit.
// settle found nothing of the session's own on GitHub, so the review starts from
// the beginning.
//
// The record stays reviewing, with no session id, until the new launch names
// one. Every other path refuses a record in that state, so neither the user nor
// docket mcp can start a second session meanwhile. Every docket instance polls,
// so two can read the same refusal. An instance that finds the id gone leaves the
// record alone. The read and the write that drops the id take separate locks, so
// both can still pass the check at the same moment. That is the window Prepare
// documents.
func (s *Service) startElsewhere(ctx context.Context, rec review.Record, limit engine.UsageLimit) (review.Record, error) {
	if err := s.recordLimit(rec, limit); err != nil {
		return rec, err
	}
	if current := s.latest(rec); current.State != review.StateReviewing || current.BGID != rec.BGID {
		return current, nil
	}
	again := rec
	again.BGID, again.SessionID, again.Err = "", "", ""
	// A new start time keeps the poll and recoverLost off the record until the
	// launch writes the new id.
	again.StartedAt = s.now()
	if err := s.append(again); err != nil {
		return rec, err
	}
	if held, stopped := s.stopBackground(ctx, rec); !stopped {
		// The record names the refused session again, so the next poll tries
		// once more.
		return s.recordErr(held, errors.New(held.Err))
	}

	// docket asked about existing notes before the first launch, so notes there
	// now came from the refused session. review-code would ask what to do with
	// them, and nobody is at a background session to answer.
	if _, err := os.Stat(again.NotesPath); err == nil && again.Intent == review.IntentReview {
		again.Intent = review.IntentOverwrite
	}
	ready, choice, err := s.readyAgain(ctx, again)
	switch {
	case err != nil:
		again.Err = err.Error()
	case choice.Limited():
		again.Err = fmt.Sprintf("no claude account has usage left until %s, when %s resets", when(choice.Until), accountName(choice.Account))
	default:
		// StartBackground records a launch that fails.
		started, _ := s.StartBackground(ctx, ready)
		return started, nil
	}
	again.State = review.StateNotStarted
	if err := s.append(again); err != nil {
		again.Err = err.Error()
	}
	return again, nil
}

// readyAgain reads what a fresh start of rec needs from GitHub and puts rec on
// the account the start runs under. It writes nothing. The choice reports
// whether any account had usage left.
func (s *Service) readyAgain(ctx context.Context, rec review.Record) (review.Record, account.Choice, error) {
	pull, err := s.openPR(ctx, rec.Ref)
	if err != nil {
		return rec, account.Choice{}, err
	}
	rec.PRState = pull.State
	rec, choice, err := s.freshAccount(rec)
	if err != nil {
		return rec, choice, err
	}
	rec, err = s.snapshot(ctx, rec)
	return rec, choice, err
}

// accountName is how a message names a claude account.
func accountName(dir string) string {
	if dir == "" {
		return "the default claude account"
	}
	if home, err := os.UserHomeDir(); err == nil {
		if rest, ok := strings.CutPrefix(dir, home+string(filepath.Separator)); ok {
			return "~/" + rest
		}
	}
	return dir
}

// when is how a message names the time a limit resets.
func when(at time.Time) string {
	if at.IsZero() {
		return "claude says it resets"
	}
	return at.Local().Format("Jan 2 3:04 PM")
}
