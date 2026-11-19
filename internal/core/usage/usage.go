// Package usage reads how much of its usage limits a claude account has used,
// from the endpoint that claude's /usage reads. It reads the account's OAuth
// token where claude keeps it. It never uses an API key.
package usage

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/haacked/docket/internal/core/engine"
	"github.com/haacked/docket/internal/core/exec"
)

// usageURL answers with the usage of the account whose token the request
// carries.
const usageURL = "https://api.anthropic.com/api/oauth/usage"

// Reader reads the usage of the claude account that a config dir is signed in
// to. dir is "" for claude's default account.
type Reader interface {
	Read(ctx context.Context, dir string) (Reading, error)
}

// Reading is the usage the endpoint reported.
type Reading struct {
	Limits []Window `json:"limits"`
}

// Window is one usage limit and how much of it the account has used.
type Window struct {
	Kind    string
	Percent float64
}

// UnmarshalJSON refuses a limit with no percent, because its usage is unknown.
func (w *Window) UnmarshalJSON(data []byte) error {
	var raw struct {
		Kind    string   `json:"kind"`
		Percent *float64 `json:"percent"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw.Percent == nil {
		return errors.New("a limit with no percent")
	}
	*w = Window{Kind: raw.Kind, Percent: *raw.Percent}
	return nil
}

// Free reports whether the account is under every limit the endpoint reported.
// A limit scoped to one model counts too, because docket does not choose the
// model a session runs on. A reading with no limits says nothing about the limit
// docket recorded, so it is not free.
func (r Reading) Free() bool {
	if len(r.Limits) == 0 {
		return false
	}
	for _, w := range r.Limits {
		if w.Percent >= 100 {
			return false
		}
	}
	return true
}

// Client reads usage from the endpoint.
type Client struct {
	Runner exec.Runner
	// HTTP is http.DefaultClient when nil. The caller's context bounds a read.
	HTTP *http.Client
	// URL is the endpoint. It is claude's usage endpoint when empty.
	URL string
	// Keychain makes the client read the token from the macOS login keychain
	// before it reads CredentialsFile.
	Keychain bool
	// CredentialsFile is where claude keeps the token of a config dir when it
	// does not keep it in the keychain.
	CredentialsFile func(dir string) string
	// RefreshDir is where the refresh command runs. claude reads the
	// .mcp.json of the directory it runs in, so it must not be a clone of a
	// pull request.
	RefreshDir string
	// NoRefresh leaves an expired token alone, because the refresh rewrites
	// claude's credentials. An expired token is then a failed read.
	NoRefresh bool
	Now       func() time.Time
}

// expirySlack counts a token as expired this long before it expires, so the
// request does not race the expiry.
const expirySlack = time.Minute

// Read reads the usage of the account dir is signed in to. An error never
// carries the token or the credentials it was read from.
func (c *Client) Read(ctx context.Context, dir string) (Reading, error) {
	creds, ok := c.credentials(ctx, dir)
	if ok && c.expired(creds) && !c.NoRefresh {
		// claude reports a failed refresh by leaving the token expired.
		_, _ = c.Runner.Run(ctx, engine.RefreshCredentialsSpec(dir, c.RefreshDir))
		creds, ok = c.credentials(ctx, dir)
	}
	if !ok {
		return Reading{}, errors.New("the account is not signed in")
	}
	if c.expired(creds) {
		return Reading{}, errors.New("the account's access token expired")
	}
	return c.fetch(ctx, creds.AccessToken)
}

// oauth is the part of claude's stored credentials that the client reads.
type oauth struct {
	AccessToken string `json:"accessToken"`
	// ExpiresAt is in unix milliseconds, or zero when claude gave none.
	ExpiresAt int64 `json:"expiresAt"`
}

func (c *Client) expired(creds oauth) bool {
	return creds.ExpiresAt != 0 && time.UnixMilli(creds.ExpiresAt).Before(c.now().Add(expirySlack))
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// credentials reads the token claude keeps for dir: from the keychain first,
// then from the credentials file. It takes the first that holds a token.
func (c *Client) credentials(ctx context.Context, dir string) (oauth, bool) {
	if c.Keychain {
		res, err := c.Runner.Run(ctx, keychainSpec(dir))
		if err == nil {
			if creds, ok := parseCredentials([]byte(res.Stdout)); ok {
				return creds, true
			}
		}
	}
	if c.CredentialsFile == nil {
		return oauth{}, false
	}
	path := c.CredentialsFile(dir)
	if path == "" {
		return oauth{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return oauth{}, false
	}
	return parseCredentials(data)
}

func parseCredentials(data []byte) (oauth, bool) {
	var stored struct {
		OAuth oauth `json:"claudeAiOauth"`
	}
	if json.Unmarshal(bytes.TrimSpace(data), &stored) != nil || stored.OAuth.AccessToken == "" {
		return oauth{}, false
	}
	return stored.OAuth, true
}

// keychainSpec prints the credentials claude keeps in the login keychain for
// dir. claude names the item after the exact CLAUDE_CONFIG_DIR string, and adds
// a suffix only when the variable is set: the first 8 hex digits of the
// string's SHA-256.
func keychainSpec(dir string) exec.CommandSpec {
	return exec.CommandSpec{Path: "security", Args: []string{"find-generic-password", "-s", keychainService(dir), "-w"}}
}

// keychainService is the name of the keychain item that holds dir's
// credentials.
func keychainService(dir string) string {
	const service = "Claude Code-credentials"
	if dir == "" {
		return service
	}
	sum := sha256.Sum256([]byte(dir))
	return service + "-" + hex.EncodeToString(sum[:])[:8]
}

// fetch asks the endpoint for the usage of the account token belongs to.
func (c *Client) fetch(ctx context.Context, token string) (Reading, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cmp.Or(c.URL, usageURL), nil)
	if err != nil {
		return Reading{}, errors.New("build the usage request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("Accept", "application/json")
	resp, err := cmp.Or(c.HTTP, http.DefaultClient).Do(req)
	if err != nil {
		// An error from the client names the URL and the cause, never a header.
		return Reading{}, fmt.Errorf("the usage request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Reading{}, fmt.Errorf("the usage endpoint answered HTTP %d", resp.StatusCode)
	}
	var reading Reading
	if json.NewDecoder(resp.Body).Decode(&reading) != nil {
		return Reading{}, errors.New("the usage endpoint answered with a body docket cannot read")
	}
	if len(reading.Limits) == 0 {
		return Reading{}, errors.New("the usage endpoint reported no limits")
	}
	return reading, nil
}
