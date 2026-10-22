package account

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	automation = "/Users/me/.claude-automation"
	personal   = "/Users/me/.claude-personal"
	work       = "/Users/me/.claude-work"
	// defaultAccount is claude's own account, which runs with CLAUDE_CONFIG_DIR
	// unset.
	defaultAccount = ""
)

var t0 = time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)

// at is a moment a number of minutes after t0.
func at(minutes int) time.Time { return t0.Add(time.Duration(minutes) * time.Minute) }

// The user's own setup: an automation account first, and the default account
// as the fallback.
func TestPickStaysOnAnAccountUntilItHitsALimit(t *testing.T) {
	accounts := []string{automation, defaultAccount}
	t1 := at(0)
	t2 := t1.Add(8 * 24 * time.Hour)
	var limits []Limit

	if got := Pick(accounts, limits, t1.Add(-time.Hour)); got != (Choice{Account: automation}) {
		t.Fatalf("with no limits, pick = %+v, want the automation account", got)
	}

	limits = append(limits, Limit{Account: automation, At: t1, Until: t1.Add(7 * 24 * time.Hour)})
	if got := Pick(accounts, limits, t1.Add(time.Minute)); got != (Choice{Account: defaultAccount}) {
		t.Fatalf("after automation hit its limit, pick = %+v, want the default account", got)
	}

	if got := Pick(accounts, limits, t1.Add(7*24*time.Hour+time.Hour)); got != (Choice{Account: defaultAccount}) {
		t.Fatalf("after automation's limit reset, pick = %+v, want docket to stay on the default account", got)
	}

	limits = append(limits, Limit{Account: defaultAccount, At: t2, Until: t2.Add(5 * time.Hour)})
	if got := Pick(accounts, limits, t2.Add(time.Minute)); got != (Choice{Account: automation}) {
		t.Fatalf("after the default account hit its limit, pick = %+v, want the automation account again", got)
	}
}

func TestPickTakesTheAccountThatResetsFirstWhenEveryAccountIsLimited(t *testing.T) {
	accounts := []string{automation, defaultAccount}
	limits := []Limit{
		{Account: automation, At: at(0), Until: at(7 * 24 * 60)},
		{Account: defaultAccount, At: at(10), Until: at(10 + 5*60)},
	}

	got := Pick(accounts, limits, at(20))

	if got.Account != defaultAccount {
		t.Errorf("account = %q, want the default account, whose limit resets first", got.Account)
	}
	if !got.Until.Equal(at(10 + 5*60)) {
		t.Errorf("until = %v, want the default account's reset %v", got.Until, at(10+5*60))
	}
	if !got.Limited() {
		t.Error("a pick with every account limited does not report itself limited")
	}
}

// claude can refuse an account twice before docket moves off it, and the second
// refusal can report a later reset. The account stays limited until the later
// one.
func TestPickReadsAnAccountsResetAsTheLatestOfItsLimits(t *testing.T) {
	accounts := []string{automation, personal}
	limits := []Limit{
		{Account: automation, At: at(0), Until: at(60)},
		{Account: automation, At: at(5), Until: at(7 * 24 * 60)},
		{Account: personal, At: at(10), Until: at(5 * 60)},
	}

	got := Pick(accounts, limits, at(30))

	if want := (Choice{Account: personal, Until: at(5 * 60)}); got.Account != want.Account || !got.Until.Equal(want.Until) {
		t.Errorf("pick = %+v, want %+v", got, want)
	}
}

func TestPickWithNoAccountsChoosesNothing(t *testing.T) {
	for name, accounts := range map[string][]string{"nil": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			limits := []Limit{{Account: automation, At: at(0), Until: at(60)}}

			if got := Pick(accounts, limits, at(1)); got != (Choice{}) {
				t.Errorf("pick = %+v, want the zero choice", got)
			}
		})
	}
}

func TestPickWithASingleAccount(t *testing.T) {
	accounts := []string{defaultAccount}
	limits := []Limit{{Account: defaultAccount, At: at(0), Until: at(60)}}

	for _, tc := range []struct {
		name string
		now  time.Time
		want Choice
	}{
		{name: "under its limit", now: at(61), want: Choice{Account: defaultAccount}},
		{name: "over its limit", now: at(30), want: Choice{Account: defaultAccount, Until: at(60)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Pick(accounts, limits, tc.now)

			if got.Account != tc.want.Account || !got.Until.Equal(tc.want.Until) {
				t.Errorf("pick = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// Each case lists the accounts in rotation order. The rotation starts after the
// account that hit a limit most recently, wraps past the end, and skips every
// account still over a limit.
func TestPickRotatesFromTheMostRecentlyLimitedAccount(t *testing.T) {
	accounts := []string{automation, defaultAccount, work}
	for _, tc := range []struct {
		name   string
		limits []Limit
		now    time.Time
		want   string
	}{
		{
			name: "no limits at all",
			now:  at(0),
			want: automation,
		},
		{
			name: "limits that have all reset",
			limits: []Limit{
				{Account: automation, At: at(0), Until: at(10)},
				{Account: defaultAccount, At: at(20), Until: at(30)},
			},
			now:  at(60),
			want: work,
		},
		{
			name: "the next account is free",
			limits: []Limit{
				{Account: defaultAccount, At: at(0), Until: at(600)},
			},
			now:  at(1),
			want: work,
		},
		{
			name: "the rotation wraps past the last account",
			limits: []Limit{
				{Account: work, At: at(0), Until: at(600)},
			},
			now:  at(1),
			want: automation,
		},
		{
			name: "the next account is still limited",
			limits: []Limit{
				{Account: work, At: at(0), Until: at(600)},
				{Account: defaultAccount, At: at(5), Until: at(600)},
			},
			now:  at(10),
			want: automation,
		},
		{
			name: "the most recently limited account is checked last",
			limits: []Limit{
				{Account: automation, At: at(0), Until: at(600)},
				{Account: defaultAccount, At: at(5), Until: at(600)},
				{Account: work, At: at(10), Until: at(20)},
			},
			now:  at(30),
			want: work,
		},
		{
			name: "limits recorded out of order",
			limits: []Limit{
				{Account: automation, At: at(10), Until: at(20)},
				{Account: work, At: at(0), Until: at(600)},
			},
			now:  at(30),
			want: defaultAccount,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Pick(accounts, tc.limits, tc.now)

			if got != (Choice{Account: tc.want}) {
				t.Errorf("pick = %+v, want %q under its limits", got, tc.want)
			}
		})
	}
}

// config.toml can drop an account that docket recorded a limit for. That limit
// must neither move the rotation nor offer a reset for an account docket no
// longer runs.
func TestPickIgnoresLimitsOnAccountsOutsideTheRotation(t *testing.T) {
	accounts := []string{automation, defaultAccount, work}

	t.Run("for the order", func(t *testing.T) {
		limits := []Limit{
			{Account: defaultAccount, At: at(0), Until: at(10)},
			{Account: personal, At: at(20), Until: at(600)},
		}

		if got := Pick(accounts, limits, at(30)); got != (Choice{Account: work}) {
			t.Errorf("pick = %+v, want work, after the default account that was limited last", got)
		}
	})

	t.Run("for the reset", func(t *testing.T) {
		limits := []Limit{
			{Account: automation, At: at(0), Until: at(500)},
			{Account: defaultAccount, At: at(1), Until: at(400)},
			{Account: work, At: at(2), Until: at(300)},
			{Account: personal, At: at(3), Until: at(100)},
		}

		got := Pick(accounts, limits, at(10))

		if got.Account != work || !got.Until.Equal(at(300)) {
			t.Errorf("pick = %+v, want work until %v", got, at(300))
		}
	})
}

// A limit whose reset is the current moment no longer counts.
func TestALimitEndsAtItsReset(t *testing.T) {
	limits := []Limit{{Account: automation, At: at(0), Until: at(60)}}

	for _, tc := range []struct {
		name string
		now  time.Time
		want bool
	}{
		{name: "before the reset", now: at(59), want: true},
		{name: "at the reset", now: at(60), want: false},
		{name: "after the reset", now: at(61), want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Limited(automation, limits, tc.now); got != tc.want {
				t.Errorf("limited = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLimitedReadsOnlyTheAccountItIsAsked(t *testing.T) {
	limits := []Limit{
		{Account: automation, At: at(0), Until: at(600)},
		{Account: work, At: at(0), Until: at(10)},
	}

	for _, tc := range []struct {
		account string
		want    bool
	}{
		{account: automation, want: true},
		{account: defaultAccount, want: false},
		{account: work, want: false},
		{account: personal, want: false},
	} {
		t.Run(tc.account, func(t *testing.T) {
			if got := Limited(tc.account, limits, at(30)); got != tc.want {
				t.Errorf("limited = %v, want %v", got, tc.want)
			}
		})
	}
}

// One of several limits for an account is enough, whatever order they come in.
func TestLimitedCountsAnyLimitThatHasNotReset(t *testing.T) {
	limits := []Limit{
		{Account: defaultAccount, At: at(5), Until: at(600)},
		{Account: defaultAccount, At: at(0), Until: at(10)},
	}

	if !Limited(defaultAccount, limits, at(30)) {
		t.Error("an account with one limit still in force reads as free")
	}
}

func TestChoiceIsLimitedOnlyWithAReset(t *testing.T) {
	if (Choice{Account: automation}).Limited() {
		t.Error("a choice with no reset reads as limited")
	}
	if !(Choice{Account: automation, Until: at(60)}).Limited() {
		t.Error("a choice with a reset reads as free")
	}
}

func TestLoadAMissingFileHoldsNoLimits(t *testing.T) {
	limits, err := Load(filepath.Join(t.TempDir(), "limits.jsonl"))

	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(limits) != 0 {
		t.Errorf("limits = %+v, want none", limits)
	}
}

func TestAppendCreatesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "limits.jsonl")

	if err := Append(path, Limit{Account: automation, At: at(0), Until: at(60)}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Errorf("the file is not there: %v", err)
	}
}

func TestAppendThenLoadReturnsTheLimitsInOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "limits.jsonl")
	want := []Limit{
		{Account: automation, At: at(0), Until: at(7 * 24 * 60)},
		{Account: defaultAccount, At: at(30).Add(722 * time.Millisecond), Until: at(30 + 5*60)},
		{Account: automation, At: at(45), Until: at(90)},
	}
	for _, limit := range want {
		if err := Append(path, limit); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	got, err := Load(path)

	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("loaded %d limits, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Account != want[i].Account || !got[i].At.Equal(want[i].At) || !got[i].Until.Equal(want[i].Until) {
			t.Errorf("limit %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestAppendWritesOneLinePerLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "limits.jsonl")
	for range 3 {
		if err := Append(path, Limit{Account: automation, At: at(0), Until: at(60)}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasSuffix(string(data), "\n") {
		t.Errorf("the file does not end in a newline:\n%s", data)
	}
	if lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n"); len(lines) != 3 {
		t.Errorf("the file has %d lines, want 3:\n%s", len(lines), data)
	}
}

// One bad write must not hide the limits around it, or docket would send new
// sessions back to an account claude has refused.
func TestLoadSkipsLinesItCannotParse(t *testing.T) {
	for _, tc := range []struct {
		name string
		bad  string
	}{
		{name: "a blank line", bad: ""},
		{name: "whitespace", bad: "   \t"},
		{name: "not json", bad: "this is not json"},
		{name: "a write cut short", bad: `{"account":"/Users/me/.claude-automation","at":"2026-10-`},
		{name: "a time that does not parse", bad: `{"account":"","at":"yesterday","until":"tomorrow"}`},
		{name: "a run of zero bytes", bad: strings.Repeat("\x00", 512)},
		{name: "a line longer than a scanner's buffer", bad: strings.Repeat("x", 200<<10)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "limits.jsonl")
			if err := Append(path, Limit{Account: automation, At: at(0), Until: at(60)}); err != nil {
				t.Fatal(err)
			}
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.WriteString(tc.bad + "\n"); err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			if err := Append(path, Limit{Account: defaultAccount, At: at(10), Until: at(70)}); err != nil {
				t.Fatal(err)
			}

			got, err := Load(path)

			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if len(got) != 2 || got[0].Account != automation || got[1].Account != defaultAccount {
				t.Errorf("limits = %+v, want the automation limit and then the default account's", got)
			}
		})
	}
}
