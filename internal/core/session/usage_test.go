package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/haacked/docket/internal/core/account"
	"github.com/haacked/docket/internal/core/engine"
	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/core/usage"
)

// fakeUsage answers each account dir with its reading or its error, and records
// every dir it was asked about. A dir it has neither for fails to read.
type fakeUsage struct {
	mu       sync.Mutex
	readings map[string]usage.Reading
	errs     map[string]error
	calls    []string
	// started, when set, receives each dir as its reading begins.
	started chan string
	// gate, when set, holds every reading until the test closes it.
	gate chan struct{}
}

func (f *fakeUsage) Read(_ context.Context, dir string) (usage.Reading, error) {
	f.mu.Lock()
	f.calls = append(f.calls, dir)
	f.mu.Unlock()
	if f.started != nil {
		f.started <- dir
	}
	if f.gate != nil {
		<-f.gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.errs[dir]; err != nil {
		return usage.Reading{}, err
	}
	if reading, ok := f.readings[dir]; ok {
		return reading, nil
	}
	return usage.Reading{}, fmt.Errorf("no reading for %s", dir)
}

func (f *fakeUsage) reads() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

var (
	// underLimits is the reading of an account that has usage left: 6% of its
	// weekly limit, as ~/.claude-max read on 2026-11-17.
	underLimits = usage.Reading{Limits: []usage.Window{{Kind: "session", Percent: 0}, {Kind: "weekly_all", Percent: 6}}}
	// atLimit is the reading of an account still at its weekly limit.
	atLimit = usage.Reading{Limits: []usage.Window{{Kind: "session", Percent: 12}, {Kind: "weekly_all", Percent: 100}}}
)

// live is a service whose rotation runs primary and then fallback, each signed
// in to a login of its own, with a usage reader the test controls.
type live struct {
	svc               *Service
	usage             *fakeUsage
	primary, fallback string
}

func liveRotation(t *testing.T) live {
	t.Helper()
	svc, _ := newService(t, &fakeGH{login: "haacked", info: prInfo()}, newFakeGit())
	l := live{
		svc:      svc,
		usage:    &fakeUsage{readings: map[string]usage.Reading{}, errs: map[string]error{}},
		primary:  t.TempDir(),
		fallback: t.TempDir(),
	}
	writeLogin(t, filepath.Join(l.primary, ".claude.json"), teamSeat)
	writeLogin(t, filepath.Join(l.fallback, ".claude.json"), ownOrg)
	svc.Cfg.ClaudeConfigDir = l.primary
	svc.Cfg.ClaudeFallbackConfigDirs = []string{l.fallback}
	svc.Usage = l.usage
	return l
}

// staleLimit is liveRotation with a limit on primary that docket recorded an
// hour ago and that claude said lasts two more days.
func staleLimit(t *testing.T) live {
	t.Helper()
	l := liveRotation(t)
	l.limitPrimary(t)
	return l
}

// limitPrimary records a limit on primary's login from an hour ago that is in
// force for two more days.
func (l live) limitPrimary(t *testing.T) account.Limit {
	t.Helper()
	limit := account.Limit{Account: l.primary, Login: teamSeat, At: l.svc.now().Add(-time.Hour), Until: l.svc.now().Add(48 * time.Hour)}
	appendLimit(t, l.svc, limit)
	return limit
}

func appendLimit(t *testing.T, svc *Service, limit account.Limit) {
	t.Helper()
	if err := account.Append(svc.Paths.Limits, limit); err != nil {
		t.Fatal(err)
	}
}

func nextAccount(t *testing.T, svc *Service) account.Choice {
	t.Helper()
	choice, err := svc.NextAccount(context.Background())
	if err != nil {
		t.Fatalf("NextAccount: %v", err)
	}
	return choice
}

// clearsIn returns the clear lines in svc's limits log.
func clearsIn(t *testing.T, svc *Service) []account.Limit {
	t.Helper()
	limits, err := account.Load(svc.Paths.Limits)
	if err != nil {
		t.Fatal(err)
	}
	var out []account.Limit
	for _, l := range limits {
		if l.Clear {
			out = append(out, l)
		}
	}
	return out
}

// logBytes is the limits log as it is on disk, or nil when there is none.
func logBytes(t *testing.T, svc *Service) []byte {
	t.Helper()
	data, err := os.ReadFile(svc.Paths.Limits)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return data
}

// clock is a service clock the test moves.
type clock struct{ at time.Time }

func (c *clock) now() time.Time { return c.at }

func useClock(svc *Service) *clock {
	c := &clock{at: svc.now()}
	svc.Now = c.now
	return c
}

func TestANewReviewReturnsToAnAccountWhoseLiveUsageRecovered(t *testing.T) {
	l := staleLimit(t)
	l.usage.readings[l.primary] = underLimits

	got := nextAccount(t, l.svc)

	if got.Account != l.primary || got.Limited() {
		t.Errorf("pick = %+v, want the primary, whose live usage is under every limit", got)
	}
}

func TestAnAccountStillAtItsLimitStaysSkipped(t *testing.T) {
	l := staleLimit(t)
	l.usage.readings[l.primary] = atLimit

	got := nextAccount(t, l.svc)

	if got.Account != l.fallback || got.Limited() {
		t.Errorf("pick = %+v, want the fallback while the primary reads 100%%", got)
	}
	if reads := l.usage.reads(); !slices.Equal(reads, []string{l.primary}) {
		t.Errorf("read %q, want only the primary, the one account a limit skipped", reads)
	}
}

// A reading that says nothing about the account leaves the recorded limit to
// decide.
func TestAFailedReadFallsBackToTheRecordedLimit(t *testing.T) {
	for name, arrange := range map[string]func(l live){
		"a read that fails":        func(l live) { l.usage.errs[l.primary] = errors.New("the usage endpoint answered HTTP 500") },
		"a reading with no limits": func(l live) { l.usage.readings[l.primary] = usage.Reading{} },
		"a reading with an empty list": func(l live) {
			l.usage.readings[l.primary] = usage.Reading{Limits: []usage.Window{}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			l := staleLimit(t)
			arrange(l)

			got := nextAccount(t, l.svc)

			if got.Account != l.fallback || got.Limited() {
				t.Errorf("pick = %+v, want the fallback, as the recorded limit says", got)
			}
			if reads := l.usage.reads(); !slices.Equal(reads, []string{l.primary}) {
				t.Errorf("read %q, want one read of the primary", reads)
			}
		})
	}
}

// The live read is for a recorded limit that would decide the pick. With none
// in force, the pick costs no request.
func TestNoLimitInForceReadsNothing(t *testing.T) {
	for name, arrange := range map[string]func(t *testing.T, l live){
		"no limits log": func(*testing.T, live) {},
		"a limit that has reset": func(t *testing.T, l live) {
			appendLimit(t, l.svc, account.Limit{Account: l.primary, Login: teamSeat, At: l.svc.now().Add(-3 * time.Hour), Until: l.svc.now().Add(-time.Hour)})
		},
		"a limit a clear already released": func(t *testing.T, l live) {
			l.limitPrimary(t)
			appendLimit(t, l.svc, account.Limit{Clear: true, Account: l.primary, Login: teamSeat, At: l.svc.now().Add(-time.Minute)})
		},
		"a limit on an account outside the rotation": func(t *testing.T, l live) {
			appendLimit(t, l.svc, account.Limit{Account: "/Users/me/.claude-gone", At: l.svc.now().Add(-time.Hour), Until: l.svc.now().Add(48 * time.Hour)})
		},
	} {
		t.Run(name, func(t *testing.T) {
			l := liveRotation(t)
			l.usage.readings[l.primary] = underLimits
			arrange(t, l)

			got := nextAccount(t, l.svc)

			if got.Account != l.primary || got.Limited() {
				t.Errorf("pick = %+v, want the primary", got)
			}
			if reads := l.usage.reads(); len(reads) != 0 {
				t.Errorf("read %q, want nothing read", reads)
			}
		})
	}
}

// A limit on an account after the pick cannot change the pick.
func TestALimitBehindAFreeAccountReadsNothing(t *testing.T) {
	l := liveRotation(t)
	appendLimit(t, l.svc, account.Limit{Account: l.fallback, Login: ownOrg, At: l.svc.now().Add(-time.Hour), Until: l.svc.now().Add(48 * time.Hour)})
	l.usage.readings[l.fallback] = underLimits

	got := nextAccount(t, l.svc)

	if got.Account != l.primary || got.Limited() {
		t.Errorf("pick = %+v, want the primary", got)
	}
	if reads := l.usage.reads(); len(reads) != 0 {
		t.Errorf("read %q, want nothing read", reads)
	}
}

// The walk stops at the first account that reads free, so it reads no account
// behind the pick.
func TestReadsStopAtTheFirstAccountThatReadsFree(t *testing.T) {
	l := staleLimit(t)
	appendLimit(t, l.svc, account.Limit{Account: l.fallback, Login: ownOrg, At: l.svc.now().Add(-time.Hour), Until: l.svc.now().Add(48 * time.Hour)})
	l.usage.readings[l.primary] = underLimits
	l.usage.readings[l.fallback] = underLimits

	got := nextAccount(t, l.svc)

	if got.Account != l.primary || got.Limited() {
		t.Errorf("pick = %+v, want the primary", got)
	}
	if reads := l.usage.reads(); !slices.Equal(reads, []string{l.primary}) {
		t.Errorf("read %q, want only the primary", reads)
	}
}

// Every limited account behind a failed read gets a read of its own, in the
// rotation's order.
func TestAnAccountThatReadsNotFreeMovesTheWalkOn(t *testing.T) {
	l := staleLimit(t)
	appendLimit(t, l.svc, account.Limit{Account: l.fallback, Login: ownOrg, At: l.svc.now().Add(-time.Hour), Until: l.svc.now().Add(48 * time.Hour)})
	l.usage.readings[l.primary] = atLimit
	l.usage.readings[l.fallback] = underLimits

	got := nextAccount(t, l.svc)

	if got.Account != l.fallback || got.Limited() {
		t.Errorf("pick = %+v, want the fallback, which reads free", got)
	}
	if reads := l.usage.reads(); !slices.Equal(reads, []string{l.primary, l.fallback}) {
		t.Errorf("read %q, want the primary and then the fallback", reads)
	}
}

// The dashboard reads the next account on every reload. One reading answers
// for a while, and so does a failed one.
func TestOneReadServesUntilItExpires(t *testing.T) {
	for name, arrange := range map[string]func(l live){
		"a free reading":   func(l live) { l.usage.readings[l.primary] = underLimits },
		"a failed reading": func(l live) { l.usage.errs[l.primary] = errors.New("the usage endpoint answered HTTP 500") },
	} {
		t.Run(name, func(t *testing.T) {
			l := staleLimit(t)
			arrange(l)
			c := useClock(l.svc)

			first := nextAccount(t, l.svc)
			c.at = c.at.Add(liveTTL - time.Second)
			second := nextAccount(t, l.svc)

			if n := len(l.usage.reads()); n != 1 {
				t.Errorf("%d reads within liveTTL, want 1", n)
			}
			if first.Account != second.Account {
				t.Errorf("picks = %q then %q, want the cached reading to pick the same account", first.Account, second.Account)
			}

			c.at = c.at.Add(2 * time.Second)
			nextAccount(t, l.svc)

			if n := len(l.usage.reads()); n != 2 {
				t.Errorf("%d reads once liveTTL passed, want 2", n)
			}
		})
	}
}

// A record keeps its own account while that account has usage left, so a
// change to claude_config_dir still applies only to new records.
func TestAFreshStartKeepsARecordWhoseAccountRecovered(t *testing.T) {
	l := staleLimit(t)
	l.svc.Cfg.ClaudeConfigDir = l.fallback
	l.svc.Cfg.ClaudeFallbackConfigDirs = []string{l.primary}
	l.usage.readings[l.primary] = underLimits
	rec := review.Record{Engine: "claude", ClaudeConfigDir: l.primary, AskSessionID: "a5c0ffee-0000-4000-8000-000000000003", AskStartedAt: l.svc.now()}

	got, choice, err := l.svc.freshAccount(context.Background(), rec)
	if err != nil {
		t.Fatalf("freshAccount: %v", err)
	}

	if got.ClaudeConfigDir != l.primary || choice.Limited() {
		t.Errorf("record moved to %q with %+v, want it kept on its own account", got.ClaudeConfigDir, choice)
	}
	if got.AskSessionID != rec.AskSessionID {
		t.Errorf("ask session = %q, want %q kept with the account", got.AskSessionID, rec.AskSessionID)
	}
}

func TestAFreshStartMovesARecordWhoseAccountIsStillAtItsLimit(t *testing.T) {
	l := staleLimit(t)
	l.usage.readings[l.primary] = atLimit

	got, choice, err := l.svc.freshAccount(context.Background(), review.Record{Engine: "claude", ClaudeConfigDir: l.primary})
	if err != nil {
		t.Fatalf("freshAccount: %v", err)
	}

	if got.ClaudeConfigDir != l.fallback || choice.Limited() {
		t.Errorf("record moved to %q with %+v, want the fallback", got.ClaudeConfigDir, choice)
	}
}

// A dry run reads usage the way a real run does, so it reports the account the
// real run would use. It writes nothing to the limits log.
func TestExplainWritesNoClear(t *testing.T) {
	for name, explain := range map[string]func(t *testing.T, l live, rec review.Record) string{
		"Explain": func(t *testing.T, l live, _ review.Record) string {
			_, spec, err := l.svc.Explain(context.Background(), pr.Ref{Org: "haacked", Repo: "docket", Number: 8}, "claude", review.ModeBackground, review.IntentReview)
			if err != nil {
				t.Fatalf("Explain: %v", err)
			}
			return accountOf(spec)
		},
		"ExplainRestart": func(t *testing.T, l live, rec review.Record) string {
			rec.State = review.StateNotStarted
			spec, err := l.svc.ExplainRestart(context.Background(), rec)
			if err != nil {
				t.Fatalf("ExplainRestart: %v", err)
			}
			return accountOf(spec)
		},
		"ExplainRereview": func(t *testing.T, l live, rec review.Record) string {
			rec.State = review.StateDrafted
			spec, err := l.svc.ExplainRereview(context.Background(), rec, review.IntentAppend, review.ModeBackground)
			if err != nil {
				t.Fatalf("ExplainRereview: %v", err)
			}
			return accountOf(spec)
		},
		"NextAccount": func(t *testing.T, l live, _ review.Record) string {
			return nextAccount(t, l.svc).Account
		},
	} {
		t.Run(name, func(t *testing.T) {
			l := liveRotation(t)
			rec, _, err := l.svc.Prepare(context.Background(), unlisted, "claude", review.ModeBackground, review.IntentReview)
			if err != nil {
				t.Fatalf("Prepare: %v", err)
			}
			l.limitPrimary(t)
			l.usage.readings[l.primary] = underLimits
			before := logBytes(t, l.svc)

			got := explain(t, l, rec)

			if after := logBytes(t, l.svc); string(after) != string(before) {
				t.Errorf("limits log changed:\nbefore %s\nafter  %s", before, after)
			}
			if got != l.primary {
				t.Errorf("explained a run under %q, want the primary, whose live usage recovered", got)
			}
		})
	}
}

// A start under an account that only the live reading freed writes the clear,
// so the next pick and every other docket instance read it without a request.
func TestAStartRecordsTheClearOnce(t *testing.T) {
	l := staleLimit(t)
	l.usage.readings[l.primary] = underLimits
	readAt := l.svc.now()

	rec := prepareWith(t, l.svc, unlisted, "claude", review.IntentReview)

	if rec.ClaudeConfigDir != l.primary {
		t.Fatalf("account = %q, want the primary", rec.ClaudeConfigDir)
	}
	clears := clearsIn(t, l.svc)
	if len(clears) != 1 {
		t.Fatalf("clears = %+v, want one", clears)
	}
	if c := clears[0]; c.Account != l.primary || c.Login != teamSeat || !c.At.Equal(readAt) {
		t.Errorf("clear = %+v, want the primary on the team seat at the reading's time %v", c, readAt)
	}

	prepareWith(t, l.svc, pr.Ref{Org: "haacked", Repo: "docket", Number: 8}, "claude", review.IntentReview)

	if n := len(clearsIn(t, l.svc)); n != 1 {
		t.Errorf("%d clears after a second start, want the one", n)
	}
}

// Every fresh start writes the clear for the account it runs under.
func TestEveryFreshStartRecordsTheClear(t *testing.T) {
	for name, start := range map[string]func(t *testing.T, l live, rec review.Record){
		"a re-review": func(t *testing.T, l live, rec review.Record) {
			rec.State = review.StateDrafted
			if _, err := l.svc.Rereview(context.Background(), rec, review.IntentAppend, review.ModeInteractive); err != nil {
				t.Fatalf("Rereview: %v", err)
			}
		},
		"a restart": func(t *testing.T, l live, rec review.Record) {
			l.svc.Runner = bgRunner(bgListing("6d681a76", bgSession, "working", true))
			rec.State = review.StateNotStarted
			if _, err := l.svc.Restart(context.Background(), rec); err != nil {
				t.Fatalf("Restart: %v", err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			l := liveRotation(t)
			rec, _, err := l.svc.Prepare(context.Background(), unlisted, "claude", review.ModeBackground, review.IntentReview)
			if err != nil {
				t.Fatalf("Prepare: %v", err)
			}
			l.limitPrimary(t)
			l.usage.readings[l.primary] = underLimits
			readAt := l.svc.now()

			start(t, l, rec)

			clears := clearsIn(t, l.svc)
			if len(clears) != 1 || clears[0].Account != l.primary || !clears[0].At.Equal(readAt) {
				t.Errorf("clears = %+v, want one for the primary at the reading's time", clears)
			}
		})
	}
}

// A review refused for its account's usage starts again under the next account
// that has usage left, and records the clear that freed that account.
func TestAReviewStartedAgainRecordsTheClearOfTheAccountItMovesTo(t *testing.T) {
	r := refusedForUsage(t)
	appendLimit(t, r.svc, account.Limit{Account: r.fallback, At: r.svc.now().Add(-time.Hour), Until: r.resets})
	reader := &fakeUsage{readings: map[string]usage.Reading{r.fallback: underLimits}}
	r.svc.Usage = reader

	got, _ := r.poll(t)

	if got.ClaudeConfigDir != r.fallback || got.BGID != "bbbbbbbb" {
		t.Errorf("record is %q with session %q under %q, want it started again under the fallback", got.State, got.BGID, got.ClaudeConfigDir)
	}
	clears := clearsIn(t, r.svc)
	if len(clears) != 1 || clears[0].Account != r.fallback {
		t.Errorf("clears = %+v, want one for the fallback", clears)
	}
}

// claude can refuse an account a reading found under its limits. That refusal
// is newer evidence than the reading. docket must not flip back to the account
// on a fresh reading while the refused limit lasts.
func TestARefusalAfterAFreeReadingKeepsTheAccountSkipped(t *testing.T) {
	l := staleLimit(t)
	l.usage.readings[l.primary] = underLimits
	c := useClock(l.svc)
	if got := nextAccount(t, l.svc); got.Account != l.primary {
		t.Fatalf("pick = %+v, want the primary on its free reading", got)
	}

	c.at = c.at.Add(time.Minute)
	if err := l.svc.recordLimit(review.Record{ClaudeConfigDir: l.primary}, engine.UsageLimit{At: c.at, Resets: c.at.Add(48 * time.Hour)}); err != nil {
		t.Fatal(err)
	}

	if got := nextAccount(t, l.svc); got.Account != l.fallback {
		t.Errorf("pick within liveTTL = %+v, want the fallback after the refusal", got)
	}
	c.at = c.at.Add(2 * liveTTL)
	if got := nextAccount(t, l.svc); got.Account != l.fallback {
		t.Errorf("pick after liveTTL = %+v, want the fallback while the refused limit lasts", got)
	}
	if n := len(l.usage.reads()); n != 1 {
		t.Errorf("%d reads, want no read of the primary again while its refused limit lasts", n)
	}
}

// startElsewhere moves a refused review through freshAccount. A reading cached
// a moment before the refusal must not send the review back to the account
// that just refused it.
func TestAStartElsewhereDoesNotReturnToAnAccountThatJustRefused(t *testing.T) {
	r := refusedForUsage(t)
	appendLimit(t, r.svc, account.Limit{Account: r.primary, At: r.svc.now().Add(-2 * time.Hour), Until: r.svc.now().Add(time.Hour)})
	r.svc.Usage = &fakeUsage{readings: map[string]usage.Reading{r.primary: underLimits}}
	refusedAt := r.svc.now()
	r.svc.Now = func() time.Time { return refusedAt.Add(-time.Minute) }
	if got := nextAccount(t, r.svc); got.Account != r.primary {
		t.Fatalf("pick = %+v, want the primary on its free reading", got)
	}
	r.svc.Now = func() time.Time { return refusedAt }

	got, _ := r.poll(t)

	if got.ClaudeConfigDir != r.fallback {
		t.Errorf("record started again under %q, want the fallback", got.ClaudeConfigDir)
	}
	if n := len(launches(r.runner, r.primary)); n != 1 {
		t.Errorf("%d starts under the primary, want only the one claude refused", n)
	}
}

// The user's max and automation logins share an account UUID and differ by
// organization. A reading through one says nothing about the other.
func TestAClearOnOneOrganizationLeavesTheOtherLimited(t *testing.T) {
	for _, tc := range []struct {
		name     string
		free     func(primary, twin, other string) string
		freed    func(primary, twin, other string) []string
		held     func(primary, twin, other string) []string
		freedOn  account.Login
		heldOn   account.Login
		wantPick func(primary, twin, other string) string
	}{
		{
			name:     "a free reading on the team seat",
			free:     func(primary, _, _ string) string { return primary },
			freed:    func(primary, twin, _ string) []string { return []string{primary, twin} },
			held:     func(_, _, other string) []string { return []string{other} },
			freedOn:  teamSeat,
			heldOn:   ownOrg,
			wantPick: func(primary, _, _ string) string { return primary },
		},
		{
			name:     "a free reading on the other organization",
			free:     func(_, _, other string) string { return other },
			freed:    func(_, _, other string) []string { return []string{other} },
			held:     func(primary, twin, _ string) []string { return []string{primary, twin} },
			freedOn:  ownOrg,
			heldOn:   teamSeat,
			wantPick: func(_, _, other string) string { return other },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, primary, twin, other := twins(t)
			appendLimit(t, svc, account.Limit{Account: primary, Login: teamSeat, At: svc.now().Add(-time.Hour), Until: svc.now().Add(48 * time.Hour)})
			appendLimit(t, svc, account.Limit{Account: other, Login: ownOrg, At: svc.now().Add(-time.Hour), Until: svc.now().Add(48 * time.Hour)})
			svc.Usage = &fakeUsage{readings: map[string]usage.Reading{tc.free(primary, twin, other): underLimits}}

			rec := prepareWith(t, svc, unlisted, "claude", review.IntentReview)

			if want := tc.wantPick(primary, twin, other); rec.ClaudeConfigDir != want {
				t.Errorf("account = %q, want %q", rec.ClaudeConfigDir, want)
			}
			limits, err := account.Load(svc.Paths.Limits)
			if err != nil {
				t.Fatal(err)
			}
			for _, dir := range tc.freed(primary, twin, other) {
				if account.Limited(dir, tc.freedOn, limits, svc.now()) {
					t.Errorf("%s reads as limited after a clear on its login", dir)
				}
			}
			for _, dir := range tc.held(primary, twin, other) {
				if !account.Limited(dir, tc.heldOn, limits, svc.now()) {
					t.Errorf("%s reads as free after a clear on the other organization", dir)
				}
			}
		})
	}
}

// Lines of a session's transcript that the served-turn tests write. promptID
// ties the user lines of one turn together.
const servedPromptID = "4508d009-d69c-4b25-9d7f-3123cd3a91ae"

func promptLine(at time.Time) string {
	return fmt.Sprintf(`{"type":"user","isSidechain":false,"promptId":%q,"timestamp":%q,"message":{"role":"user","content":"/review-code https://github.com/haacked/docket/pull/7 --draft"}}`, servedPromptID, at.Format(time.RFC3339Nano))
}

func replyLine(at time.Time) string {
	return fmt.Sprintf(`{"type":"assistant","isSidechain":false,"timestamp":%q,"message":{"role":"assistant","content":[{"type":"text","text":"Posted the draft review."}]}}`, at.Format(time.RFC3339Nano))
}

func wrapUpLine(at time.Time) string {
	return fmt.Sprintf(`{"type":"user","isSidechain":false,"promptId":%q,"isMeta":true,"turnCompanion":true,"usageLimitNote":"wrap_up","timestamp":%q,"message":{"role":"user","content":"[Usage limit reached; a short grace allowance remains.]"}}`, servedPromptID, at.Format(time.RFC3339Nano))
}

func toolCallLine(at time.Time) string {
	return fmt.Sprintf(`{"type":"assistant","isSidechain":false,"timestamp":%q,"message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_01","name":"Bash","input":{}}]}}`, at.Format(time.RFC3339Nano))
}

func personLine(at time.Time) string {
	return fmt.Sprintf(`{"type":"user","isSidechain":false,"promptSource":"typed","promptId":"85520f37-fc0f-48e5-8dbc-e8a3deaa5eaf","timestamp":%q,"message":{"role":"user","content":"continue"}}`, at.Format(time.RFC3339Nano))
}

// interactiveOnPrimary is an interactive review started under the primary,
// whose login claude then refused an hour before now. The session's transcript
// is not written yet.
func interactiveOnPrimary(t *testing.T) (live, review.Record) {
	t.Helper()
	l := liveRotation(t)
	l.svc.Usage = nil
	rec := prepareWith(t, l.svc, unlisted, "claude", review.IntentReview)
	if rec.ClaudeConfigDir != l.primary {
		t.Fatalf("account = %q, want the primary", rec.ClaudeConfigDir)
	}
	return l, rec
}

// A session claude served shows that its account has usage again, without any
// request to the usage endpoint.
func TestAServedInteractiveSessionClearsItsAccount(t *testing.T) {
	l, rec := interactiveOnPrimary(t)
	l.limitPrimary(t)
	repliedAt := l.svc.now().Add(-time.Minute)
	writeTranscript(t, l.svc.Cfg.ClaudeProjectsDir(l.primary), rec.SessionID, promptLine(repliedAt.Add(-time.Minute)), replyLine(repliedAt))

	if _, err := l.svc.AfterExit(context.Background(), rec, nil); err != nil {
		t.Fatalf("AfterExit: %v", err)
	}

	clears := clearsIn(t, l.svc)
	if len(clears) != 1 {
		t.Fatalf("clears = %+v, want one", clears)
	}
	if c := clears[0]; c.Account != l.primary || c.Login != teamSeat || !c.At.Equal(repliedAt) {
		t.Errorf("clear = %+v, want the primary on the team seat at the reply's time %v", c, repliedAt)
	}
	if got := nextAccount(t, l.svc); got.Account != l.primary || got.Limited() {
		t.Errorf("pick = %+v, want the primary back with no usage reader", got)
	}
}

func TestAServedQuestionSessionClearsItsAccount(t *testing.T) {
	l := liveRotation(t)
	l.svc.Usage = nil
	rec := adopted(t, l.svc, unlisted)
	if rec.ClaudeConfigDir != l.primary || rec.AskSessionID == "" {
		t.Fatalf("record is under %q with ask session %q, want an ask session under the primary", rec.ClaudeConfigDir, rec.AskSessionID)
	}
	l.limitPrimary(t)
	repliedAt := l.svc.now().Add(-time.Minute)
	writeTranscript(t, l.svc.Cfg.ClaudeProjectsDir(l.primary), rec.AskSessionID, promptLine(repliedAt.Add(-time.Minute)), replyLine(repliedAt))

	if _, err := l.svc.AfterAsk(context.Background(), rec, nil); err != nil {
		t.Fatalf("AfterAsk: %v", err)
	}

	clears := clearsIn(t, l.svc)
	if len(clears) != 1 || clears[0].Account != l.primary || !clears[0].At.Equal(repliedAt) {
		t.Errorf("clears = %+v, want one for the primary at the reply's time", clears)
	}
}

// A turn the model ended under claude's wrap-up note ran on a grace allowance.
// It shows that the account reached its limit, not that it has usage.
func TestAWrapUpTurnClearsNothing(t *testing.T) {
	l, rec := interactiveOnPrimary(t)
	l.limitPrimary(t)
	at := l.svc.now().Add(-time.Minute)
	writeTranscript(t, l.svc.Cfg.ClaudeProjectsDir(l.primary), rec.SessionID, promptLine(at.Add(-time.Minute)), wrapUpLine(at), replyLine(at.Add(time.Second)))

	if _, err := l.svc.AfterExit(context.Background(), rec, nil); err != nil {
		t.Fatalf("AfterExit: %v", err)
	}

	if clears := clearsIn(t, l.svc); len(clears) != 0 {
		t.Errorf("clears = %+v, want none", clears)
	}
	if got := nextAccount(t, l.svc); got.Account != l.fallback {
		t.Errorf("pick = %+v, want the fallback", got)
	}
}

// None of these turns ended with a reply claude served.
func TestATurnClaudeDidNotServeClearsNothing(t *testing.T) {
	for name, transcript := range map[string]func(at time.Time) []string{
		"a refusal": func(at time.Time) []string {
			return []string{promptLine(at.Add(-time.Minute)), refusedTurn(at, at.Add(48*time.Hour))}
		},
		"a tool call with no result": func(at time.Time) []string {
			return []string{promptLine(at.Add(-time.Minute)), toolCallLine(at)}
		},
		"a person's message last": func(at time.Time) []string {
			return []string{promptLine(at.Add(-time.Minute)), replyLine(at.Add(-30 * time.Second)), personLine(at)}
		},
		"no transcript": func(time.Time) []string { return nil },
	} {
		t.Run(name, func(t *testing.T) {
			l, rec := interactiveOnPrimary(t)
			l.limitPrimary(t)
			if lines := transcript(l.svc.now().Add(-time.Minute)); lines != nil {
				writeTranscript(t, l.svc.Cfg.ClaudeProjectsDir(l.primary), rec.SessionID, lines...)
			}

			if _, err := l.svc.AfterExit(context.Background(), rec, nil); err != nil {
				t.Fatalf("AfterExit: %v", err)
			}

			if clears := clearsIn(t, l.svc); len(clears) != 0 {
				t.Errorf("clears = %+v, want none", clears)
			}
		})
	}
}

// A clear goes to the log only when it changes what the rotation reads.
func TestAServedSessionWithNoLimitInForceWritesNothing(t *testing.T) {
	for name, arrange := range map[string]func(t *testing.T, l live, repliedAt time.Time){
		"no limits log": func(*testing.T, live, time.Time) {},
		"a limit that has reset": func(t *testing.T, l live, repliedAt time.Time) {
			appendLimit(t, l.svc, account.Limit{Account: l.primary, Login: teamSeat, At: repliedAt.Add(-3 * time.Hour), Until: repliedAt.Add(-2 * time.Hour)})
		},
		"a limit recorded after the reply": func(t *testing.T, l live, repliedAt time.Time) {
			appendLimit(t, l.svc, account.Limit{Account: l.primary, Login: teamSeat, At: repliedAt.Add(time.Second), Until: repliedAt.Add(48 * time.Hour)})
		},
		"a limit on the other organization": func(t *testing.T, l live, repliedAt time.Time) {
			appendLimit(t, l.svc, account.Limit{Account: l.fallback, Login: ownOrg, At: repliedAt.Add(-time.Hour), Until: repliedAt.Add(48 * time.Hour)})
		},
	} {
		t.Run(name, func(t *testing.T) {
			l, rec := interactiveOnPrimary(t)
			repliedAt := l.svc.now().Add(-time.Minute)
			arrange(t, l, repliedAt)
			before := logBytes(t, l.svc)
			writeTranscript(t, l.svc.Cfg.ClaudeProjectsDir(l.primary), rec.SessionID, promptLine(repliedAt.Add(-time.Minute)), replyLine(repliedAt))

			if _, err := l.svc.AfterExit(context.Background(), rec, nil); err != nil {
				t.Fatalf("AfterExit: %v", err)
			}

			if after := logBytes(t, l.svc); string(after) != string(before) {
				t.Errorf("limits log changed:\nbefore %s\nafter  %s", before, after)
			}
			if before == nil {
				if _, err := os.Stat(l.svc.Paths.Limits); !os.IsNotExist(err) {
					t.Errorf("limits log stat = %v, want no log", err)
				}
			}
		})
	}
}

// The poll reads the transcript of a background session that ended its turn,
// and of one that finished.
func TestAPollClearsTheAccountOfAServedBackgroundSession(t *testing.T) {
	for name, listing := range map[string]string{
		"a session waiting after its turn": idleListing("6d681a76", bgSession),
		"a session that finished":          bgListing("6d681a76", bgSession, "done", false),
	} {
		t.Run(name, func(t *testing.T) {
			l := liveRotation(t)
			l.svc.Usage = nil
			rec := startedBackground(t, l.svc, bgRunner(listing))
			if rec.ClaudeConfigDir != l.primary {
				t.Fatalf("account = %q, want the primary", rec.ClaudeConfigDir)
			}
			l.limitPrimary(t)
			repliedAt := l.svc.now().Add(-10 * time.Second)
			writeTranscript(t, l.svc.Cfg.ClaudeProjectsDir(l.primary), bgSession, promptLine(repliedAt.Add(-time.Minute)), replyLine(repliedAt))

			if _, _, err := l.svc.PollBackground(context.Background()); err != nil {
				t.Fatalf("PollBackground: %v", err)
			}

			clears := clearsIn(t, l.svc)
			if len(clears) != 1 || clears[0].Account != l.primary || !clears[0].At.Equal(repliedAt) {
				t.Errorf("clears = %+v, want one for the primary at the reply's time", clears)
			}
		})
	}
}

// docket rotates claude accounts only. A codex record runs under
// claude_config_dir without the rotation, so neither its start nor its session
// says anything about a claude account's usage.
func TestACodexSessionClearsNothing(t *testing.T) {
	t.Run("a start", func(t *testing.T) {
		l := staleLimit(t)
		l.usage.readings[l.primary] = underLimits
		before := logBytes(t, l.svc)

		if _, _, err := l.svc.Prepare(context.Background(), unlisted, "codex", review.ModeInteractive, review.IntentReview); err != nil {
			t.Fatalf("Prepare(codex): %v", err)
		}

		if reads := l.usage.reads(); len(reads) != 0 {
			t.Errorf("read %q, want nothing read for a codex review", reads)
		}
		if after := logBytes(t, l.svc); string(after) != string(before) {
			t.Errorf("limits log changed:\nbefore %s\nafter  %s", before, after)
		}
	})

	t.Run("an exit", func(t *testing.T) {
		l := liveRotation(t)
		l.svc.Usage = nil
		rec := prepareWith(t, l.svc, unlisted, "codex", review.IntentReview)
		rec.SessionID = "823735c4-c8ff-4e82-9dc4-82e0e1356c92"
		l.limitPrimary(t)
		repliedAt := l.svc.now().Add(-time.Minute)
		writeTranscript(t, l.svc.Cfg.ClaudeProjectsDir(l.primary), rec.SessionID, promptLine(repliedAt.Add(-time.Minute)), replyLine(repliedAt))
		before := logBytes(t, l.svc)

		if _, err := l.svc.AfterExit(context.Background(), rec, nil); err != nil {
			t.Fatalf("AfterExit: %v", err)
		}

		if after := logBytes(t, l.svc); string(after) != string(before) {
			t.Errorf("limits log changed:\nbefore %s\nafter  %s", before, after)
		}
	})
}

// A clear is not a refusal. A refusal at the same moment as a clear for the
// same dir still has to reach the log.
func TestARefusalAtAClearsTimeIsStillRecorded(t *testing.T) {
	svc, _ := newService(t, &fakeGH{login: "haacked", info: prInfo()}, newFakeGit())
	appendLimit(t, svc, account.Limit{Clear: true, Account: "/a", At: svc.now()})

	if err := svc.recordLimit(review.Record{ClaudeConfigDir: "/a"}, engine.UsageLimit{At: svc.now(), Resets: svc.now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	limits, err := account.Load(svc.Paths.Limits)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(limits, func(l account.Limit) bool { return !l.Clear && l.Account == "/a" && l.At.Equal(svc.now()) }) {
		t.Errorf("limits = %+v, want the refusal recorded beside the clear", limits)
	}
}

// claude counts usage per login. A refusal through one dir contradicts a free
// reading of every other dir signed in to the same login.
func TestARefusalContradictsAFreeReadingOfEveryDirOnItsLogin(t *testing.T) {
	svc, primary, twin, other := twins(t)
	appendLimit(t, svc, account.Limit{Account: primary, Login: teamSeat, At: svc.now().Add(-time.Hour), Until: svc.now().Add(48 * time.Hour)})
	reader := &fakeUsage{readings: map[string]usage.Reading{twin: underLimits}}
	svc.Usage = reader
	c := useClock(svc)
	// twin's reading frees the login, and primary comes first on it.
	if got := nextAccount(t, svc); got.Account != primary {
		t.Fatalf("pick = %+v, want primary, freed by twin's reading of their login", got)
	}

	c.at = c.at.Add(time.Minute)
	if err := svc.recordLimit(review.Record{ClaudeConfigDir: primary}, engine.UsageLimit{At: c.at, Resets: c.at.Add(48 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	c.at = c.at.Add(2 * liveTTL)

	if got := nextAccount(t, svc); got.Account != other {
		t.Errorf("pick = %+v, want other, the one dir on another login", got)
	}
	twinReads := 0
	for _, dir := range reader.reads() {
		if dir == twin {
			twinReads++
		}
	}
	if twinReads != 1 {
		t.Errorf("twin read %d times, want no read again while its login's refused limit lasts", twinReads)
	}
}

// A refusal contradicts only the readings of dirs on its own login. A free
// reading of a dir on another login still stands after it.
func TestARefusalLeavesAFreeReadingOfAnotherLoginStanding(t *testing.T) {
	svc, primary, _, other := twins(t)
	appendLimit(t, svc, account.Limit{Account: primary, Login: teamSeat, At: svc.now().Add(-time.Hour), Until: svc.now().Add(48 * time.Hour)})
	appendLimit(t, svc, account.Limit{Account: other, Login: ownOrg, At: svc.now().Add(-time.Hour), Until: svc.now().Add(48 * time.Hour)})
	svc.Usage = &fakeUsage{readings: map[string]usage.Reading{primary: atLimit, other: underLimits}}
	c := useClock(svc)
	if got := nextAccount(t, svc); got.Account != other {
		t.Fatalf("pick = %+v, want other on its free reading", got)
	}

	c.at = c.at.Add(time.Minute)
	if err := svc.recordLimit(review.Record{ClaudeConfigDir: primary}, engine.UsageLimit{At: c.at, Resets: c.at.Add(48 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	c.at = c.at.Add(2 * liveTTL)

	if got := nextAccount(t, svc); got.Account != other || got.Limited() {
		t.Errorf("pick = %+v, want other: a refusal on the team seat says nothing about its organization", got)
	}
}

// verdict holds one lock per dir across a reading, so two picks that both miss
// the cache read the account once and refresh its token once.
func TestConcurrentPicksReadAnAccountOnce(t *testing.T) {
	l := staleLimit(t)
	l.usage.readings[l.primary] = underLimits
	l.usage.started = make(chan string, 2)
	l.usage.gate = make(chan struct{})

	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			if got, err := l.svc.NextAccount(context.Background()); err != nil {
				t.Errorf("NextAccount: %v", err)
			} else if got.Account != l.primary {
				t.Errorf("pick = %+v, want the primary on its free reading", got)
			}
		})
	}
	<-l.usage.started
	// The second pick has time to reach the reading while the first one holds
	// it open. A slow second pick lets the test pass without checking the lock,
	// but never makes it fail.
	time.Sleep(50 * time.Millisecond)
	close(l.usage.gate)
	wg.Wait()

	if n := len(l.usage.reads()); n != 1 {
		t.Errorf("%d reads from two concurrent picks, want 1", n)
	}
}

// A reading that finds an account still at its limit runs out after liveTTL.
// The title line reads the account again then, rather than wait for the reset
// the log recorded.
func TestAPickRechecksWhenAReadingThatKeptAnAccountLimitedExpires(t *testing.T) {
	l := staleLimit(t)
	l.usage.readings[l.primary] = atLimit

	got := nextAccount(t, l.svc)

	if got.Account != l.fallback {
		t.Fatalf("pick = %+v, want the fallback while the primary reads at its limit", got)
	}
	if want := l.svc.now().Add(liveTTL); got.Recheck.IsZero() || got.Recheck.After(want) {
		t.Errorf("recheck = %v, want no later than %v, when the primary's reading expires", got.Recheck, want)
	}
}
