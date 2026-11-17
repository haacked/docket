package usage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/haacked/docket/internal/core/exec"
)

// token and refreshToken are the secrets claude keeps for an account. Neither
// may reach an error or a command line.
const (
	token        = "sk-ant-oat01-live-access-token-0d9f"
	freshToken   = "sk-ant-oat01-fresh-access-token-7c21"
	refreshToken = "sk-ant-ort01-refresh-token-44ab"
)

// refreshDir is where the refresh runs. docket passes its own home, never the
// working directory, which may be a pull request's clone.
const refreshDir = "/Users/me/.docket"

var now = time.Date(2026, 11, 17, 18, 0, 0, 0, time.UTC)

// credentials is the JSON claude keeps in the keychain and in
// .credentials.json for a token that expires at expires.
func credentials(access string, expires time.Time) string {
	return fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q,"refreshToken":%q,"expiresAt":%d,"scopes":["user:inference","user:profile"],"subscriptionType":"max"}}`, access, refreshToken, expires.UnixMilli())
}

// valid and expired are credentials whose token is good for an hour, and whose
// token expired an hour ago.
var (
	valid   = credentials(token, now.Add(time.Hour))
	expired = credentials(token, now.Add(-time.Hour))
)

// recovered is a body for an account at 7% of its weekly limit and 0% of its
// session limit, the state ~/.claude-max read on 2026-11-17 while docket still
// skipped it for a limit it had recorded. Its entries take the fields CLAUDE.md
// records for that answer. The legacy fields and extra_usage come alongside
// limits.
const recovered = `{
  "five_hour": {"utilization": 0.0, "resets_at": null},
  "seven_day": {"utilization": 7.0, "resets_at": "2026-11-21T04:00:00.000000+00:00"},
  "seven_day_opus": null,
  "extra_usage": {"is_enabled": false, "monthly_limit": null, "used_credits": null, "utilization": null},
  "limits": [
    {"kind": "session", "group": "session", "percent": 0, "severity": "normal", "resets_at": null, "scope": null, "is_active": false},
    {"kind": "weekly_all", "group": "weekly", "percent": 7, "severity": "normal", "resets_at": "2026-11-21T04:00:00.000000+00:00", "scope": null, "is_active": true},
    {"kind": "weekly_scoped", "group": "weekly", "percent": 0, "severity": "normal", "resets_at": "2026-11-21T04:00:00+00:00",
     "scope": {"model": {"display_name": "Fable"}}, "is_active": false}
  ]
}`

// endpoint is a stand-in for the usage endpoint. It answers every request with
// status and body and keeps each request's headers.
type endpoint struct {
	srv    *httptest.Server
	status int
	body   string

	mu       sync.Mutex
	requests []http.Header
	methods  []string
}

func serve(t *testing.T, status int, body string) *endpoint {
	t.Helper()
	e := &endpoint{status: status, body: body}
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		e.requests = append(e.requests, r.Header.Clone())
		e.methods = append(e.methods, r.Method)
		e.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(e.status)
		io.WriteString(w, e.body)
	}))
	t.Cleanup(e.srv.Close)
	return e
}

// tokens lists the bearer token of each request the endpoint received.
func (e *endpoint) tokens() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, h := range e.requests {
		out = append(out, strings.TrimPrefix(h.Get("Authorization"), "Bearer "))
	}
	return out
}

// keychain answers security with stdout, the way it prints a stored password.
func keychain(stdout string) *exec.Fake {
	return &exec.Fake{Results: map[string]exec.Result{"security": {Stdout: stdout + "\n"}}}
}

// noKeychainItem is security's answer when it holds no item for the service.
func noKeychainItem() *exec.Fake {
	return &exec.Fake{
		Results: map[string]exec.Result{"security": {ExitCode: 44, Stderr: "security: SecKeychainSearchCopyNext: The specified item could not be found in the keychain."}},
		Errs:    map[string]error{"security": errors.New("security exited 44: security: SecKeychainSearchCopyNext: The specified item could not be found in the keychain.")},
	}
}

// refreshing runs the refresh through the fake and then calls onRefresh, which
// stands for claude rewriting its credential store.
type refreshing struct {
	*exec.Fake
	onRefresh func()
}

func (r *refreshing) Run(ctx context.Context, spec exec.CommandSpec) (exec.Result, error) {
	res, err := r.Fake.Run(ctx, spec)
	if filepath.Base(spec.Path) == "claude" && r.onRefresh != nil {
		r.onRefresh()
	}
	return res, err
}

// newClient is a client of e that reads the keychain through runner and finds
// no credentials file.
func newClient(e *endpoint, runner exec.Runner) *Client {
	return &Client{
		Runner:          runner,
		HTTP:            e.srv.Client(),
		URL:             e.srv.URL,
		Keychain:        true,
		CredentialsFile: func(string) string { return "" },
		RefreshDir:      refreshDir,
		Now:             func() time.Time { return now },
	}
}

// commands lists the calls runner made to the program named name.
func commands(runner *exec.Fake, name string) []exec.CommandSpec {
	var out []exec.CommandSpec
	for _, spec := range runner.Calls {
		if filepath.Base(spec.Path) == name {
			out = append(out, spec)
		}
	}
	return out
}

// configDirOf is the CLAUDE_CONFIG_DIR a spec gives claude when docket itself
// runs with another one exported. It is "" when the spec removes the variable.
func configDirOf(spec exec.CommandSpec) string {
	for _, kv := range spec.Env([]string{"CLAUDE_CONFIG_DIR=/elsewhere", "HOME=/h"}) {
		if value, ok := strings.CutPrefix(kv, "CLAUDE_CONFIG_DIR="); ok {
			return value
		}
	}
	return ""
}

// writeCredentials writes body as a .credentials.json and returns its path.
func writeCredentials(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".credentials.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAnAccountUnderEveryLimitReadsFree(t *testing.T) {
	e := serve(t, http.StatusOK, recovered)

	got, err := newClient(e, keychain(valid)).Read(context.Background(), "/Users/me/.claude-max")

	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !got.Free() {
		t.Errorf("reading = %+v, want free at 7%% and 0%%", got)
	}
	if len(got.Limits) != 3 {
		t.Errorf("limits = %+v, want the three the endpoint listed", got.Limits)
	}
	weekly := slices.IndexFunc(got.Limits, func(w Window) bool { return w.Kind == "weekly_all" })
	if weekly < 0 || got.Limits[weekly].Percent != 7 {
		t.Errorf("limits = %+v, want weekly_all at 7%%", got.Limits)
	}
}

// docket does not choose the model a session runs on, so a limit scoped to one
// model counts, and so does a limit the endpoint marks inactive.
func TestAnyLimitAtItsCapIsNotFree(t *testing.T) {
	for _, tc := range []struct {
		name   string
		limits []Window
		want   bool
	}{
		{name: "every limit under its cap", limits: []Window{{Kind: "session", Percent: 0}, {Kind: "weekly_all", Percent: 7}}, want: true},
		{name: "a fraction under the cap", limits: []Window{{Kind: "weekly_all", Percent: 99.5}}, want: true},
		{name: "the weekly limit at its cap", limits: []Window{{Kind: "session", Percent: 3}, {Kind: "weekly_all", Percent: 100}}, want: false},
		{name: "the session limit at its cap", limits: []Window{{Kind: "session", Percent: 100}, {Kind: "weekly_all", Percent: 7}}, want: false},
		{name: "a model-scoped limit at its cap", limits: []Window{{Kind: "weekly_all", Percent: 7}, {Kind: "weekly_scoped", Percent: 100}}, want: false},
		{name: "a limit past its cap", limits: []Window{{Kind: "weekly_all", Percent: 104}}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := (Reading{Limits: tc.limits}).Free(); got != tc.want {
				t.Errorf("free = %v, want %v", got, tc.want)
			}
		})
	}
}

// A reading that lists no limits shows nothing about the account, so it never
// releases a recorded limit.
func TestAReadingWithNoLimitsIsNotFree(t *testing.T) {
	for name, reading := range map[string]Reading{
		"nil limits":   {},
		"empty limits": {Limits: []Window{}},
	} {
		t.Run(name, func(t *testing.T) {
			if reading.Free() {
				t.Error("a reading with no limits reads as free")
			}
		})
	}
}

func TestAModelScopedLimitAtItsCapReadsNotFree(t *testing.T) {
	body := strings.Replace(recovered, `"percent": 0, "severity": "normal", "resets_at": "2026-11-21T04:00:00+00:00"`, `"percent": 100, "severity": "critical", "resets_at": "2026-11-21T04:00:00+00:00"`, 1)
	e := serve(t, http.StatusOK, body)

	got, err := newClient(e, keychain(valid)).Read(context.Background(), "/Users/me/.claude-max")

	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Free() {
		t.Errorf("reading = %+v, want not free with the scoped limit at 100%%", got)
	}
}

// A percent the endpoint writes with a fraction still decodes.
func TestAFractionalPercentDecodes(t *testing.T) {
	e := serve(t, http.StatusOK, `{"limits":[{"kind":"weekly_all","percent":99.6,"resets_at":null}]}`)

	got, err := newClient(e, keychain(valid)).Read(context.Background(), "")

	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got.Limits) != 1 || got.Limits[0].Percent != 99.6 || !got.Free() {
		t.Errorf("reading = %+v, want one limit at 99.6%% that reads free", got)
	}
}

// Each of these answers says nothing about the account's usage. The caller
// then falls back to the limit it recorded.
func TestReadFailsWithoutAUsableAnswer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "no limits field", status: http.StatusOK, body: `{"five_hour":{"utilization":0,"resets_at":null},"seven_day":{"utilization":7,"resets_at":null}}`},
		{name: "a null limits field", status: http.StatusOK, body: `{"limits":null}`},
		{name: "an empty limits array", status: http.StatusOK, body: `{"limits":[]}`},
		{name: "limits that are not an array", status: http.StatusOK, body: `{"limits":{"kind":"session","percent":0}}`},
		{name: "a limit with no percent", status: http.StatusOK, body: `{"limits":[{"kind":"session","percent":null,"resets_at":null}]}`},
		{name: "a body cut short", status: http.StatusOK, body: `{"limits":[{"kind":"session","percent":`},
		{name: "a body that is not json", status: http.StatusOK, body: `<html>Bad gateway ` + token + `</html>`},
		{name: "an empty body", status: http.StatusOK, body: ``},
		{name: "an expired token", status: http.StatusUnauthorized, body: `{"type":"error","error":{"type":"authentication_error","message":"Invalid bearer token ` + token + `"}}`},
		{name: "a forbidden token", status: http.StatusForbidden, body: `{"type":"error","error":{"type":"permission_error"}}`},
		{name: "a rate limit", status: http.StatusTooManyRequests, body: `{"limits":[{"kind":"session","percent":0}]}`},
		{name: "a server error", status: http.StatusInternalServerError, body: `{"type":"error","error":{"type":"api_error","message":"` + token + `"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := serve(t, tc.status, tc.body)
			runner := keychain(valid)

			_, err := newClient(e, runner).Read(context.Background(), "/Users/me/.claude-max")

			if err == nil {
				t.Fatal("Read reported no error")
			}
			if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), refreshToken) {
				t.Errorf("error %q carries a secret", err)
			}
		})
	}
}

func TestReadSendsTheTokenAndTheHeadersTheEndpointNeeds(t *testing.T) {
	e := serve(t, http.StatusOK, recovered)

	if _, err := newClient(e, keychain(valid)).Read(context.Background(), "/Users/me/.claude-max"); err != nil {
		t.Fatalf("Read: %v", err)
	}

	if len(e.requests) != 1 {
		t.Fatalf("%d requests, want 1", len(e.requests))
	}
	if e.methods[0] != http.MethodGet {
		t.Errorf("method = %s, want GET", e.methods[0])
	}
	for name, want := range map[string]string{
		"Authorization":  "Bearer " + token,
		"Anthropic-Beta": "oauth-2025-04-20",
		"Accept":         "application/json",
	} {
		if got := e.requests[0].Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A client that names no URL reads claude's endpoint. The transport answers in
// place of the network.
func TestReadDefaultsToClaudesEndpoint(t *testing.T) {
	var asked []string
	client := &Client{
		Runner:   keychain(valid),
		Keychain: true,
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			asked = append(asked, r.URL.String())
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(recovered)), Request: r}, nil
		})},
		CredentialsFile: func(string) string { return "" },
		RefreshDir:      refreshDir,
		Now:             func() time.Time { return now },
	}

	if _, err := client.Read(context.Background(), ""); err != nil {
		t.Fatalf("Read: %v", err)
	}

	if want := []string{usageURL}; !slices.Equal(asked, want) {
		t.Errorf("asked %v, want %v", asked, want)
	}
	if usageURL != "https://api.anthropic.com/api/oauth/usage" {
		t.Errorf("endpoint = %q, want claude's OAuth usage endpoint", usageURL)
	}
}

// claude names the keychain item after CLAUDE_CONFIG_DIR exactly as it is set,
// so two spellings of one dir are two items.
func TestReadLooksUpTheKeychainItemClaudeNamesForTheDir(t *testing.T) {
	suffix := func(dir string) string {
		sum := sha256.Sum256([]byte(dir))
		return "-" + hex.EncodeToString(sum[:])[:8]
	}
	for _, tc := range []struct {
		name string
		dir  string
		want string
	}{
		{name: "the default account", dir: "", want: "Claude Code-credentials"},
		{name: "a named dir", dir: "/Users/me/.claude-max", want: "Claude Code-credentials" + suffix("/Users/me/.claude-max")},
		{name: "a named dir with a trailing slash", dir: "/Users/me/.claude-max/", want: "Claude Code-credentials" + suffix("/Users/me/.claude-max/")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := serve(t, http.StatusOK, recovered)
			runner := keychain(valid)

			if _, err := newClient(e, runner).Read(context.Background(), tc.dir); err != nil {
				t.Fatalf("Read: %v", err)
			}

			lookups := commands(runner, "security")
			if len(lookups) != 1 || !slices.Contains(lookups[0].Args, tc.want) {
				t.Errorf("security calls = %v, want one for the service %q", runner.Lines(), tc.want)
			}
		})
	}
}

// Off macOS claude keeps the credentials in a file.
func TestReadUsesTheCredentialsFileWithoutTheKeychain(t *testing.T) {
	e := serve(t, http.StatusOK, recovered)
	runner := keychain(credentials("sk-ant-oat01-keychain-token", now.Add(time.Hour)))
	path := writeCredentials(t, valid)
	var asked []string
	client := newClient(e, runner)
	client.Keychain = false
	client.CredentialsFile = func(dir string) string {
		asked = append(asked, dir)
		return path
	}

	if _, err := client.Read(context.Background(), "/Users/me/.claude-max"); err != nil {
		t.Fatalf("Read: %v", err)
	}

	if n := len(commands(runner, "security")); n != 0 {
		t.Errorf("%d security calls, want none with the keychain off", n)
	}
	if got := e.tokens(); !slices.Equal(got, []string{token}) {
		t.Errorf("requests carried %v, want the file's token", got)
	}
	if !slices.Contains(asked, "/Users/me/.claude-max") {
		t.Errorf("asked for the credentials file of %q, want the dir that was read", asked)
	}
}

// claude can hold the credentials in either place. The keychain answers first,
// and the file stands in when the keychain holds no token.
func TestReadTakesTheFirstPlaceThatHoldsAToken(t *testing.T) {
	keychainToken := "sk-ant-oat01-keychain-token"
	for _, tc := range []struct {
		name   string
		runner *exec.Fake
		want   string
	}{
		{name: "a token in both places", runner: keychain(credentials(keychainToken, now.Add(time.Hour))), want: keychainToken},
		{name: "no keychain item", runner: noKeychainItem(), want: token},
		{name: "a keychain item with no token", runner: keychain(`{"claudeAiOauth":{"refreshToken":"` + refreshToken + `"}}`), want: token},
		{name: "a keychain item with an empty token", runner: keychain(credentials("", now.Add(time.Hour))), want: token},
		{name: "a keychain item that is not json", runner: keychain("not json"), want: token},
		{name: "a keychain item for another service", runner: keychain(`{"mcpOAuth":{}}`), want: token},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := serve(t, http.StatusOK, recovered)
			path := writeCredentials(t, valid)
			client := newClient(e, tc.runner)
			client.CredentialsFile = func(string) string { return path }

			if _, err := client.Read(context.Background(), "/Users/me/.claude-max"); err != nil {
				t.Fatalf("Read: %v", err)
			}

			if got := e.tokens(); !slices.Equal(got, []string{tc.want}) {
				t.Errorf("requests carried %v, want %q", got, tc.want)
			}
		})
	}
}

// A dir that never signed in has no token anywhere. There is nothing to send.
func TestReadFailsWithNoCredentials(t *testing.T) {
	for name, file := range map[string]func(t *testing.T) string{
		"no credentials file":               func(*testing.T) string { return "" },
		"a credentials file not there":      func(t *testing.T) string { return filepath.Join(t.TempDir(), ".credentials.json") },
		"a credentials file with no token":  func(t *testing.T) string { return writeCredentials(t, `{"claudeAiOauth":{}}`) },
		"a credentials file that is broken": func(t *testing.T) string { return writeCredentials(t, `{"claudeAiOauth":{"accessToken":"`+token) },
	} {
		t.Run(name, func(t *testing.T) {
			e := serve(t, http.StatusOK, recovered)
			path := file(t)
			client := newClient(e, noKeychainItem())
			client.CredentialsFile = func(string) string { return path }

			_, err := client.Read(context.Background(), "/Users/me/.claude-max")

			if err == nil {
				t.Fatal("Read reported no error")
			}
			if strings.Contains(err.Error(), token) {
				t.Errorf("error %q carries the token", err)
			}
			if n := len(e.tokens()); n != 0 {
				t.Errorf("%d requests, want none without a token", n)
			}
		})
	}
}

// claude refreshes an expired token when `claude mcp get` looks up its
// connectors. The refresh runs under the dir's own account, in docket's home.
func TestAnExpiredTokenIsRefreshedAndReadAgain(t *testing.T) {
	for _, tc := range []struct {
		name string
		dir  string
	}{
		{name: "the default account", dir: ""},
		{name: "a named dir", dir: "/Users/me/.claude-max"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := serve(t, http.StatusOK, recovered)
			fake := keychain(expired)
			runner := &refreshing{Fake: fake, onRefresh: func() {
				fake.Results["security"] = exec.Result{Stdout: credentials(freshToken, now.Add(time.Hour)) + "\n"}
			}}

			if _, err := newClient(e, runner).Read(context.Background(), tc.dir); err != nil {
				t.Fatalf("Read: %v", err)
			}

			refreshes := commands(fake, "claude")
			if len(refreshes) != 1 {
				t.Fatalf("claude calls = %v, want one refresh", fake.Lines())
			}
			refresh := refreshes[0]
			if want := []string{"mcp", "get", "claude-usage-refresh"}; !slices.Equal(refresh.Args, want) {
				t.Errorf("refresh args = %q, want %q", refresh.Args, want)
			}
			if refresh.Dir != refreshDir {
				t.Errorf("refresh runs in %q, want %q", refresh.Dir, refreshDir)
			}
			if got := configDirOf(refresh); got != tc.dir {
				t.Errorf("refresh runs under CLAUDE_CONFIG_DIR %q, want %q", got, tc.dir)
			}
			if n := len(commands(fake, "security")); n != 2 {
				t.Errorf("%d keychain reads, want one before the refresh and one after", n)
			}
			if got := e.tokens(); !slices.Equal(got, []string{freshToken}) {
				t.Errorf("requests carried %v, want only the refreshed token", got)
			}
		})
	}
}

// claude rewrites .credentials.json when it refreshes a token kept there.
func TestAnExpiredTokenInTheCredentialsFileIsRefreshedAndReadAgain(t *testing.T) {
	e := serve(t, http.StatusOK, recovered)
	path := writeCredentials(t, expired)
	fake := &exec.Fake{}
	runner := &refreshing{Fake: fake, onRefresh: func() {
		if err := os.WriteFile(path, []byte(credentials(freshToken, now.Add(time.Hour))), 0o600); err != nil {
			t.Error(err)
		}
	}}
	client := newClient(e, runner)
	client.Keychain = false
	client.CredentialsFile = func(string) string { return path }

	if _, err := client.Read(context.Background(), "/Users/me/.claude-max"); err != nil {
		t.Fatalf("Read: %v", err)
	}

	if n := len(commands(fake, "claude")); n != 1 {
		t.Errorf("claude calls = %v, want one refresh", fake.Lines())
	}
	if got := e.tokens(); !slices.Equal(got, []string{freshToken}) {
		t.Errorf("requests carried %v, want only the refreshed token", got)
	}
}

// A token about to expire would expire in flight, so it counts as expired a
// minute early.
func TestATokenCountsAsExpiredAMinuteEarly(t *testing.T) {
	for _, tc := range []struct {
		name    string
		expires time.Time
		refresh bool
	}{
		{name: "an hour left", expires: now.Add(time.Hour), refresh: false},
		{name: "two minutes left", expires: now.Add(2 * time.Minute), refresh: false},
		{name: "thirty seconds left", expires: now.Add(30 * time.Second), refresh: true},
		{name: "expired a moment ago", expires: now.Add(-time.Second), refresh: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := serve(t, http.StatusOK, recovered)
			fake := keychain(credentials(token, tc.expires))
			runner := &refreshing{Fake: fake, onRefresh: func() {
				fake.Results["security"] = exec.Result{Stdout: credentials(freshToken, now.Add(time.Hour)) + "\n"}
			}}

			if _, err := newClient(e, runner).Read(context.Background(), "/Users/me/.claude-max"); err != nil {
				t.Fatalf("Read: %v", err)
			}

			if got := len(commands(fake, "claude")) == 1; got != tc.refresh {
				t.Errorf("refreshed = %v, want %v: %v", got, tc.refresh, fake.Lines())
			}
		})
	}
}

// The refresh rewrites claude's credential store, which a dry run must not do.
func TestNoRefreshLeavesAnExpiredTokenAFailedRead(t *testing.T) {
	e := serve(t, http.StatusOK, recovered)
	runner := keychain(expired)
	client := newClient(e, runner)
	client.NoRefresh = true

	_, err := client.Read(context.Background(), "/Users/me/.claude-max")

	if err == nil {
		t.Fatal("Read reported no error for an expired token it may not refresh")
	}
	if n := len(commands(runner, "claude")); n != 0 {
		t.Errorf("claude calls = %v, want none", runner.Lines())
	}
}

// claude can fail to refresh, for instance when the refresh token was revoked.
func TestATokenStillExpiredAfterTheRefreshIsAnError(t *testing.T) {
	for name, runner := range map[string]*exec.Fake{
		"a refresh that changes nothing": keychain(expired),
		"a refresh that fails": {
			Results: map[string]exec.Result{"security": {Stdout: expired + "\n"}},
			Errs:    map[string]error{"claude": errors.New("claude exited 1: not logged in")},
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := serve(t, http.StatusOK, recovered)

			_, err := newClient(e, runner).Read(context.Background(), "/Users/me/.claude-max")

			if err == nil {
				t.Fatal("Read reported no error")
			}
			if n := len(commands(runner, "claude")); n != 1 {
				t.Errorf("claude calls = %v, want one refresh and no more", runner.Lines())
			}
		})
	}
}

// The token travels only in the Authorization header. An error carries fixed
// text, and security gets no secret on its command line.
func TestTheTokenStaysOutOfErrorsAndCommandLines(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		keychain  string
		noRefresh bool
	}{
		{name: "a refused token echoed in the answer", status: http.StatusUnauthorized, body: `{"error":"bad token ` + token + `"}`, keychain: valid},
		{name: "a server error echoing the token", status: http.StatusInternalServerError, body: token, keychain: valid},
		{name: "an answer that is not json", status: http.StatusOK, body: `limits for ` + token, keychain: valid},
		{name: "a token still expired after the refresh", status: http.StatusOK, body: recovered, keychain: expired},
		{name: "an expired token with no refresh", status: http.StatusOK, body: recovered, keychain: expired, noRefresh: true},
		{name: "credentials with an expiry that does not decode", status: http.StatusOK, body: recovered, keychain: `{"claudeAiOauth":{"accessToken":"` + token + `","refreshToken":"` + refreshToken + `","expiresAt":"soon"}}`},
		{name: "credentials cut short", status: http.StatusOK, body: recovered, keychain: `{"claudeAiOauth":{"accessToken":"` + token + `","refreshToken":"` + refreshToken},
		{name: "a read that works", status: http.StatusOK, body: recovered, keychain: valid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := serve(t, tc.status, tc.body)
			runner := keychain(tc.keychain)
			client := newClient(e, runner)
			client.NoRefresh = tc.noRefresh

			_, err := client.Read(context.Background(), "/Users/me/.claude-max")

			for _, secret := range []string{token, refreshToken} {
				if err != nil && strings.Contains(err.Error(), secret) {
					t.Errorf("error %q carries a secret", err)
				}
				for _, line := range runner.Lines() {
					if strings.Contains(line, secret) {
						t.Errorf("command line %q carries a secret", line)
					}
				}
			}
		})
	}
}
