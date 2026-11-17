package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/haacked/docket/internal/core/account"
	"github.com/haacked/docket/internal/core/config"
	"github.com/haacked/docket/internal/core/engine"
	"github.com/haacked/docket/internal/core/review"
)

// unknownReset is how long docket counts an account as over its limit when
// claude did not say when the limit resets.
const unknownReset = time.Hour

// liveTTL is how long docket trusts one reading of an account's usage. A burst
// of starts therefore reads each account once.
const liveTTL = 5 * time.Minute

// liveTimeout bounds one account's reading: the credentials, a refresh, and the
// request.
const liveTimeout = 10 * time.Second

// liveVerdict is what one reading of an account's usage said.
type liveVerdict struct {
	// Free is true when the account was under every limit. A failed reading is
	// not free, so docket falls back to the limits it recorded.
	Free    bool
	At      time.Time
	Expires time.Time
}

// liveCache holds the latest reading of each account dir.
type liveCache struct {
	mu       sync.Mutex
	verdicts map[string]liveVerdict
	// reading holds one lock per dir across a reading, so two reloads never
	// refresh one token at the same moment.
	reading map[string]*sync.Mutex
}

// NextAccount is the claude account a new record runs under. The rotation picks
// it from the limits claude has reported, and reads an account's usage before a
// recorded limit makes the rotation pass over it. It writes nothing, because a
// dry run calls it.
func (s *Service) NextAccount(ctx context.Context) (account.Choice, error) {
	limits, err := account.Load(s.Paths.Limits)
	if err != nil {
		return account.Choice{}, err
	}
	return s.pick(ctx, limits), nil
}

// pick runs the rotation over the accounts config.toml names.
func (s *Service) pick(ctx context.Context, limits []account.Limit) account.Choice {
	accounts := s.Cfg.ClaudeAccounts()
	logins := make(map[string]account.Login, len(accounts))
	for _, dir := range accounts {
		logins[dir] = s.claudeLogin(dir)
	}
	return account.Pick(accounts, logins, s.withLive(ctx, accounts, logins, limits), s.now())
}

// withLive adds a clear for each account whose live usage shows that a limit in
// force no longer applies. It walks accounts in order and stops at the first
// account no limit holds back, so it reads only the accounts that a recorded
// limit would make the pick pass over. The clear is stamped with the reading's
// time, so a refusal claude reports after the reading still counts.
func (s *Service) withLive(ctx context.Context, accounts []string, logins map[string]account.Login, limits []account.Limit) []account.Limit {
	limits = slices.Clip(limits)
	now := s.now()
	for _, dir := range accounts {
		if !account.Limited(dir, logins[dir], limits, now) {
			break
		}
		verdict := s.verdict(ctx, dir)
		if !verdict.Free {
			continue
		}
		limits = append(limits, account.Limit{Account: dir, Login: logins[dir], At: verdict.At, Clear: true})
		if !account.Limited(dir, logins[dir], limits, now) {
			break
		}
	}
	return limits
}

// verdict reads the usage of the account dir names, or returns the reading it
// cached less than liveTTL ago. A failed reading is cached too, so a broken
// token does not cost a refresh on every reload.
func (s *Service) verdict(ctx context.Context, dir string) liveVerdict {
	if s.Usage == nil {
		return liveVerdict{}
	}
	s.live.mu.Lock()
	if s.live.reading == nil {
		s.live.reading = map[string]*sync.Mutex{}
		s.live.verdicts = map[string]liveVerdict{}
	}
	lock, ok := s.live.reading[dir]
	if !ok {
		lock = &sync.Mutex{}
		s.live.reading[dir] = lock
	}
	s.live.mu.Unlock()

	lock.Lock()
	defer lock.Unlock()
	now := s.now()
	if cached, ok := s.cachedVerdict(dir); ok && now.Before(cached.Expires) {
		return cached
	}
	ctx, cancel := context.WithTimeout(ctx, liveTimeout)
	defer cancel()
	reading, err := s.Usage.Read(ctx, dir)
	verdict := liveVerdict{Free: err == nil && reading.Free(), At: now, Expires: now.Add(liveTTL)}
	s.live.mu.Lock()
	s.live.verdicts[dir] = verdict
	s.live.mu.Unlock()
	return verdict
}

func (s *Service) cachedVerdict(dir string) (liveVerdict, bool) {
	s.live.mu.Lock()
	defer s.live.mu.Unlock()
	verdict, ok := s.live.verdicts[dir]
	return verdict, ok
}

// distrust records that claude refused a session under a login after a reading
// that docket still trusts said a dir on that login was free. The endpoint and
// claude then disagree. docket trusts claude until the limit resets, so it does
// not return to the login after every reading and lose a launch each time.
func (s *Service) distrust(limit account.Limit) {
	s.live.mu.Lock()
	var trusted []string
	for dir, verdict := range s.live.verdicts {
		if verdict.Free && verdict.At.Before(limit.At) && limit.At.Before(verdict.Expires) {
			trusted = append(trusted, dir)
		}
	}
	s.live.mu.Unlock()

	for _, dir := range trusted {
		if !limit.AppliesTo(dir, s.claudeLogin(dir)) {
			continue
		}
		s.live.mu.Lock()
		s.live.verdicts[dir] = liveVerdict{At: limit.At, Expires: limit.Until}
		s.live.mu.Unlock()
	}
}

// keepReading writes to the log the clear that a reading of rec's account
// implies, so other docket instances stop passing over the account too. Only a
// path that starts a session calls it, because a dry run writes nothing. A
// failed write costs only another reading, so keepReading drops the error.
func (s *Service) keepReading(rec review.Record) {
	if !config.UsesClaudeAccount(rec.Engine) {
		return
	}
	if verdict, ok := s.cachedVerdict(rec.ClaudeConfigDir); ok && verdict.Free {
		_ = s.keepClear(rec.ClaudeConfigDir, verdict.At)
	}
}

// keepClear appends a clear for dir at at when it releases a limit in force.
func (s *Service) keepClear(dir string, at time.Time) error {
	limits, err := account.Load(s.Paths.Limits)
	if err != nil {
		return err
	}
	return s.appendClear(dir, at, limits)
}

func (s *Service) appendClear(dir string, at time.Time, limits []account.Limit) error {
	login := s.claudeLogin(dir)
	if !account.WouldClear(dir, login, at, limits, s.now()) {
		return nil
	}
	return account.Append(s.Paths.Limits, account.Limit{Account: dir, Login: login, At: at, Clear: true})
}

// claudeLogin is the login the account dir is signed in to now.
func (s *Service) claudeLogin(dir string) account.Login {
	return account.ReadLogin(s.Cfg.ClaudeLoginFile(dir))
}

// AccountNames names each account in the rotation by who it is signed in as.
// It leaves out an account whose profile docket cannot read.
func (s *Service) AccountNames() account.Names {
	names := account.Names{}
	for _, dir := range s.Cfg.ClaudeAccounts() {
		if label := s.profile(dir).Label(); label != "" {
			names[dir] = label
		}
	}
	return names
}

// profile is who the account dir is signed in as now.
func (s *Service) profile(dir string) account.Profile {
	return account.ReadProfile(s.Cfg.ClaudeLoginFile(dir))
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
func (s *Service) freshAccount(ctx context.Context, rec review.Record) (review.Record, account.Choice, error) {
	choice := account.Choice{Account: rec.ClaudeConfigDir}
	// A codex record keeps the question-and-answer session codex can resume.
	if !config.UsesClaudeAccount(rec.Engine) {
		return rec, choice, nil
	}
	limits, err := account.Load(s.Paths.Limits)
	if err != nil {
		return rec, account.Choice{}, err
	}
	dir := rec.ClaudeConfigDir
	login := s.claudeLogin(dir)
	limits = s.withLive(ctx, []string{dir}, map[string]account.Login{dir: login}, limits)
	if account.Limited(dir, login, limits, s.now()) {
		choice = s.pick(ctx, limits)
	}
	if choice.Account != rec.ClaudeConfigDir {
		rec.ClaudeConfigDir = choice.Account
		rec.AskSessionID = ""
		rec.AskStartedAt = time.Time{}
	}
	return rec, choice, nil
}

// recordLimit adds the limit that a session under rec's account ran into to the
// log the rotation reads, with the login the account is signed in to. The user
// can sign the dir in to another login later. docket therefore reads the login
// when it records the limit. It skips a refusal that the log already holds.
// The poll reads the same refusal again after a stop that fails. Each exit from
// an interactive session that claude refused reads it again too.
func (s *Service) recordLimit(rec review.Record, limit engine.UsageLimit) error {
	limits, err := account.Load(s.Paths.Limits)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(limits, func(l account.Limit) bool {
		return !l.Clear && l.Account == rec.ClaudeConfigDir && l.At.Equal(limit.At)
	}) {
		return nil
	}
	recorded := account.Limit{Account: rec.ClaudeConfigDir, Login: s.claudeLogin(rec.ClaudeConfigDir), At: limit.At, Until: limitUntil(limit)}
	s.distrust(recorded)
	return account.Append(s.Paths.Limits, recorded)
}

// limitUntil is when a limit stops counting: when claude said it resets, or
// unknownReset after the refusal when claude did not say.
func limitUntil(limit engine.UsageLimit) time.Time {
	if limit.Resets.IsZero() {
		return limit.At.Add(unknownReset)
	}
	return limit.Resets
}

// noteUsage records the usage limit that an interactive session ran into, so the
// next fresh start moves to another account. Nothing starts again here, because
// the user saw claude's refusal in the terminal. It skips a limit that has
// reset, because the user can resume a refused session after the reset and
// leave it without sending anything. A session that claude served instead
// clears its account's limits.
func (s *Service) noteUsage(rec review.Record, sessionID string) review.Record {
	eng, err := engine.For(rec.Engine)
	if err != nil {
		return rec
	}
	limit, ok := eng.UsageLimit(sessionID, s.enginePaths(rec.ClaudeConfigDir))
	if !ok {
		return s.noteServed(rec, sessionID)
	}
	if !limitUntil(limit).After(s.now()) {
		return rec
	}
	if err := s.recordLimit(rec, limit); err != nil {
		rec.Err = err.Error()
		return rec
	}
	rec.Err = fmt.Sprintf("%s is over its usage limit until %s", s.accountName(rec.ClaudeConfigDir), account.When(limit.Resets))
	return rec
}

// noteServed clears the limits of rec's account when claude answered the
// session's last turn with the model's reply. That reply shows the account had
// usage left when claude wrote it. It reads the transcript only while a limit is
// in force, which is rare. A failed write leaves only a stale limit, so
// noteServed drops the error, as keepReading does.
func (s *Service) noteServed(rec review.Record, sessionID string) review.Record {
	eng, err := engine.For(rec.Engine)
	if err != nil {
		return rec
	}
	limits, err := account.Load(s.Paths.Limits)
	if err != nil || !account.Limited(rec.ClaudeConfigDir, s.claudeLogin(rec.ClaudeConfigDir), limits, s.now()) {
		return rec
	}
	if at, ok := eng.ServedTurn(sessionID, s.enginePaths(rec.ClaudeConfigDir)); ok {
		_ = s.appendClear(rec.ClaudeConfigDir, at, limits)
	}
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
		again.Err = fmt.Sprintf("no claude account has usage left until %s, when %s resets", account.When(choice.Until), s.accountName(choice.Account))
	default:
		s.keepReading(ready)
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
	rec, choice, err := s.freshAccount(ctx, rec)
	if err != nil {
		return rec, choice, err
	}
	rec, err = s.snapshot(ctx, rec)
	return rec, choice, err
}

// accountName is how a message names a claude account.
func (s *Service) accountName(dir string) string {
	if label := s.profile(dir).Label(); label != "" {
		return label
	}
	if dir == "" {
		return "the default claude account"
	}
	return account.Name(dir)
}
