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

	if got := Pick(accounts, nil, limits, t1.Add(-time.Hour)); got != (Choice{Account: automation}) {
		t.Fatalf("with no limits, pick = %+v, want the automation account", got)
	}

	limits = append(limits, Limit{Account: automation, At: t1, Until: t1.Add(7 * 24 * time.Hour)})
	if got := Pick(accounts, nil, limits, t1.Add(time.Minute)); got != (Choice{Account: defaultAccount}) {
		t.Fatalf("after automation hit its limit, pick = %+v, want the default account", got)
	}

	if got := Pick(accounts, nil, limits, t1.Add(7*24*time.Hour+time.Hour)); got != (Choice{Account: defaultAccount}) {
		t.Fatalf("after automation's limit reset, pick = %+v, want docket to stay on the default account", got)
	}

	limits = append(limits, Limit{Account: defaultAccount, At: t2, Until: t2.Add(5 * time.Hour)})
	if got := Pick(accounts, nil, limits, t2.Add(time.Minute)); got != (Choice{Account: automation}) {
		t.Fatalf("after the default account hit its limit, pick = %+v, want the automation account again", got)
	}
}

func TestPickTakesTheAccountThatResetsFirstWhenEveryAccountIsLimited(t *testing.T) {
	accounts := []string{automation, defaultAccount}
	limits := []Limit{
		{Account: automation, At: at(0), Until: at(7 * 24 * 60)},
		{Account: defaultAccount, At: at(10), Until: at(10 + 5*60)},
	}

	got := Pick(accounts, nil, limits, at(20))

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

	got := Pick(accounts, nil, limits, at(30))

	if want := (Choice{Account: personal, Until: at(5 * 60)}); got.Account != want.Account || !got.Until.Equal(want.Until) {
		t.Errorf("pick = %+v, want %+v", got, want)
	}
}

func TestPickWithNoAccountsChoosesNothing(t *testing.T) {
	for name, accounts := range map[string][]string{"nil": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			limits := []Limit{{Account: automation, At: at(0), Until: at(60)}}

			if got := Pick(accounts, nil, limits, at(1)); got != (Choice{}) {
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
			got := Pick(accounts, nil, limits, tc.now)

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
			got := Pick(accounts, nil, tc.limits, tc.now)

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

		if got := Pick(accounts, nil, limits, at(30)); got != (Choice{Account: work}) {
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

		got := Pick(accounts, nil, limits, at(10))

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
			if got := Limited(automation, Login{}, limits, tc.now); got != tc.want {
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
			if got := Limited(tc.account, Login{}, limits, at(30)); got != tc.want {
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

	if !Limited(defaultAccount, Login{}, limits, at(30)) {
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

var (
	// team is the PostHog seat. The user signs both the automation dir and the
	// default account in to it.
	team = Login{AccountUUID: "4f6f73cc-a2c3-4aef-adfd-615b0d6ede86", OrganizationUUID: "b94d544b-7e01-43e9-9fa2-0f99539bc5d1"}
	// ownOrg is the same person's account in another organization.
	ownOrg = Login{AccountUUID: team.AccountUUID, OrganizationUUID: "2bb69dd2-45b3-40ef-80fe-d40827cc4cd5"}
)

// Two config dirs signed in to one login share that login's limits. claude
// refuses both once either is over a limit.
func TestALimitBlocksEveryDirSignedInToItsLogin(t *testing.T) {
	accounts := []string{automation, defaultAccount, personal}
	logins := map[string]Login{automation: team, defaultAccount: team, personal: ownOrg}
	limits := []Limit{{Account: defaultAccount, Login: team, At: at(0), Until: at(600)}}

	if !Limited(automation, team, limits, at(30)) {
		t.Error("automation reads as free although the default account's login is over its limit")
	}
	if got := Pick(accounts, logins, limits, at(30)); got != (Choice{Account: personal}) {
		t.Errorf("pick = %+v, want personal, the one dir signed in to another login", got)
	}
}

// The user can /login the default account to another login after claude
// refused it. The old limit belongs to the old login.
func TestADirSignedInToAnotherLoginSinceTheLimitIsFree(t *testing.T) {
	accounts := []string{defaultAccount, automation}
	logins := map[string]Login{defaultAccount: ownOrg, automation: team}
	limits := []Limit{{Account: defaultAccount, Login: team, At: at(0), Until: at(600)}}

	if Limited(defaultAccount, ownOrg, limits, at(30)) {
		t.Error("the default account reads as limited by a limit on the login it left")
	}
	got := Pick(accounts, logins, limits, at(30))
	if got.Account != defaultAccount || got.Limited() {
		t.Errorf("pick = %+v, want the default account under its limits", got)
	}
}

// A limit docket recorded before it read logins, or for a dir whose login it
// could not read, matches by dir.
func TestALimitMatchesByDirWhenEitherLoginIsUnknown(t *testing.T) {
	for _, tc := range []struct {
		name    string
		limit   Limit
		account string
		login   Login
		want    bool
	}{
		{name: "an old limit on the same dir", limit: Limit{Account: defaultAccount}, account: defaultAccount, login: team, want: true},
		{name: "an old limit on another dir with the same login", limit: Limit{Account: defaultAccount}, account: automation, login: team, want: false},
		{name: "a dir whose login is unknown", limit: Limit{Account: automation, Login: team}, account: automation, login: Login{}, want: true},
		{name: "another dir whose login is unknown", limit: Limit{Account: automation, Login: team}, account: defaultAccount, login: Login{}, want: false},
		{name: "a limit with only an account id on its own dir", limit: Limit{Account: automation, Login: Login{AccountUUID: team.AccountUUID}}, account: automation, login: team, want: true},
		{name: "a limit with only an account id on another dir", limit: Limit{Account: automation, Login: Login{AccountUUID: team.AccountUUID}}, account: defaultAccount, login: team, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.limit.At, tc.limit.Until = at(0), at(600)

			if got := Limited(tc.account, tc.login, []Limit{tc.limit}, at(30)); got != tc.want {
				t.Errorf("limited = %v, want %v", got, tc.want)
			}
		})
	}
}

// The rotation starts past every dir that the most recent limit applies to. docket
// therefore stays on the dir it moved to after that limit resets.
func TestPickStaysOffALoginAfterItsLimitResets(t *testing.T) {
	for _, tc := range []struct {
		name     string
		accounts []string
	}{
		{name: "the limited login first", accounts: []string{automation, defaultAccount, personal}},
		{name: "the limited login around the other", accounts: []string{automation, personal, defaultAccount}},
		{name: "the other login first", accounts: []string{personal, automation, defaultAccount}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logins := map[string]Login{automation: team, defaultAccount: team, personal: ownOrg}
			limits := []Limit{{Account: defaultAccount, Login: team, At: at(0), Until: at(10)}}

			if got := Pick(tc.accounts, logins, limits, at(30)); got != (Choice{Account: personal}) {
				t.Errorf("pick = %+v, want personal, which never hit a limit", got)
			}
		})
	}
}

// A limit on a dir that the user has since signed in to another login applies
// to no dir in the rotation. It must not move the rotation.
func TestPickIgnoresALimitThatAppliesToNoDirInTheRotation(t *testing.T) {
	accounts := []string{automation, personal}
	logins := map[string]Login{automation: team, personal: ownOrg}
	other := Login{AccountUUID: "ef2621bd-2fb9-4866-8453-6b8e4875b115", OrganizationUUID: "2bb69dd2-45b3-40ef-80fe-d40827cc4cd5"}
	limits := []Limit{
		{Account: automation, Login: team, At: at(0), Until: at(10)},
		{Account: personal, Login: other, At: at(20), Until: at(600)},
	}

	if got := Pick(accounts, logins, limits, at(30)); got != (Choice{Account: personal}) {
		t.Errorf("pick = %+v, want personal, after automation, which was limited last", got)
	}
}

func TestAppendWritesTheLoginAndOmitsAnUnknownOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "limits.jsonl")
	for _, limit := range []Limit{
		{Account: automation, Login: team, At: at(0), Until: at(60)},
		{Account: defaultAccount, At: at(1), Until: at(61)},
	} {
		if err := Append(path, limit); err != nil {
			t.Fatal(err)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if want := `"login":{"account_uuid":"` + team.AccountUUID + `","organization_uuid":"` + team.OrganizationUUID + `"}`; !strings.Contains(lines[0], want) {
		t.Errorf("line = %s, want it to hold %s", lines[0], want)
	}
	if strings.Contains(lines[1], "login") {
		t.Errorf("line = %s, want no login for a dir whose login is unknown", lines[1])
	}

	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Login != team || got[1].Login != (Login{}) {
		t.Errorf("limits = %+v, want the team login and then none", got)
	}
}

// writeConfig writes a claude .claude.json holding body and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".claude.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadLoginReadsTheAccountAndTheOrganization(t *testing.T) {
	path := writeConfig(t, `{"numStartups":3,"oauthAccount":{"accountUuid":"`+team.AccountUUID+`","emailAddress":"me@example.com","organizationUuid":"`+team.OrganizationUUID+`"}}`)

	if got := ReadLogin(path); got != team {
		t.Errorf("login = %+v, want %+v", got, team)
	}
}

// Without both ids docket cannot tell two logins apart. It treats the dir's
// login as unknown.
func TestReadLoginKnowsNoLoginWithoutBothIDs(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "no path", path: ""},
		{name: "a missing file", path: filepath.Join(t.TempDir(), ".claude.json")},
		{name: "a file that does not parse", path: writeConfig(t, `{"oauthAccount":{"accountUuid":"`)},
		{name: "a dir that never signed in", path: writeConfig(t, `{"numStartups":1}`)},
		{name: "no organization", path: writeConfig(t, `{"oauthAccount":{"accountUuid":"`+team.AccountUUID+`"}}`)},
		{name: "an empty account", path: writeConfig(t, `{"oauthAccount":{"accountUuid":"","organizationUuid":"`+team.OrganizationUUID+`"}}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ReadLogin(tc.path); got != (Login{}) {
				t.Errorf("login = %+v, want none", got)
			}
		})
	}
}
