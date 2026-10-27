// Package account picks the claude account a new session runs under. docket
// rotates through the accounts config.toml names, and moves past an account once
// claude refuses one of its sessions for a usage limit. claude counts usage per
// login. A limit therefore applies to every account dir signed in to that login.
package account

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"time"
)

// Login is the claude login an account dir is signed in to: one account in one
// organization. The same person has one account UUID in every organization.
// The organization UUID tells two of their logins apart. The zero Login is a
// dir whose login docket could not read.
type Login struct {
	AccountUUID      string `json:"account_uuid"`
	OrganizationUUID string `json:"organization_uuid"`
}

// Limit records that claude refused a session because its account was over a
// usage limit.
type Limit struct {
	// Account is the account's CLAUDE_CONFIG_DIR, or "" for claude's default
	// account.
	Account string `json:"account"`
	// Login is the login Account was signed in to when docket recorded the
	// limit. It is zero for a limit recorded before docket read logins, and for
	// a dir whose login docket could not read.
	Login Login     `json:"login,omitzero"`
	At    time.Time `json:"at"`
	Until time.Time `json:"until"`
}

// appliesTo reports whether the limit counts against account, which is signed
// in to login. When both logins are known, only the logins decide. Otherwise
// the limit counts against the dir it was recorded for.
func (l Limit) appliesTo(account string, login Login) bool {
	if l.Login.known() && login.known() {
		return l.Login == login
	}
	return l.Account == account
}

// known reports whether the login has both ids. A limit with only one id
// matches by dir, as a limit without a login does.
func (l Login) known() bool {
	return l.AccountUUID != "" && l.OrganizationUUID != ""
}

// Choice is the account Pick chose.
type Choice struct {
	Account string
	// Until is zero when Account is under its limits. Otherwise every account
	// is over a limit, and Until is when Account's limit resets, which is the
	// earliest reset of them all.
	Until time.Time
}

// Limited reports whether every account was over a limit when Pick chose.
func (c Choice) Limited() bool {
	return !c.Until.IsZero()
}

// Pick chooses the account for a new session. accounts is the rotation order.
// logins maps each account to the login it is signed in to. An account that
// logins leaves out has an unknown login. limits is every limit docket has
// recorded, in any order. A limit that applies to no account in the rotation
// counts for nothing.
//
// The rotation starts after the first account that the most recent limit
// applies to, passes every account that limit applies to, and takes the first
// account that is not over a limit. docket therefore stays on an account until
// that account hits a limit, even after an earlier account's limit resets.
// Before any account has hit a limit, the pick is the first.
func Pick(accounts []string, logins map[string]Login, limits []Limit, now time.Time) Choice {
	if len(accounts) == 0 {
		return Choice{}
	}
	start := 0
	var latest Limit
	for _, limit := range limits {
		i := slices.IndexFunc(accounts, func(account string) bool {
			return limit.appliesTo(account, logins[account])
		})
		if i >= 0 && limit.At.After(latest.At) {
			latest, start = limit, i+1
		}
	}
	// The latest limit also covers every later dir on its login. A start on one of
	// those would move docket back to that login once the limit resets.
	for range accounts {
		candidate := accounts[start%len(accounts)]
		if latest.At.IsZero() || !latest.appliesTo(candidate, logins[candidate]) {
			break
		}
		start++
	}

	var soonest Choice
	for step := range len(accounts) {
		candidate := accounts[(start+step)%len(accounts)]
		until := reset(candidate, logins[candidate], limits)
		if !until.After(now) {
			return Choice{Account: candidate}
		}
		if soonest.Until.IsZero() || until.Before(soonest.Until) {
			soonest = Choice{Account: candidate, Until: until}
		}
	}
	return soonest
}

// Limited reports whether account, which is signed in to login, is over a
// limit at now.
func Limited(account string, login Login, limits []Limit, now time.Time) bool {
	return reset(account, login, limits).After(now)
}

// reset is when the last of the limits that apply to account resets.
func reset(account string, login Login, limits []Limit) time.Time {
	var until time.Time
	for _, limit := range limits {
		if limit.appliesTo(account, login) && limit.Until.After(until) {
			until = limit.Until
		}
	}
	return until
}

// ReadLogin reads the login that claude keeps in the .claude.json at path. It
// returns the zero Login when the file is missing or does not parse, or when it
// lacks either id.
func ReadLogin(path string) Login {
	data, err := os.ReadFile(path)
	if err != nil {
		return Login{}
	}
	var config struct {
		OAuthAccount struct {
			AccountUUID      string `json:"accountUuid"`
			OrganizationUUID string `json:"organizationUuid"`
		} `json:"oauthAccount"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return Login{}
	}
	login := Login(config.OAuthAccount)
	if login.AccountUUID == "" || login.OrganizationUUID == "" {
		return Login{}
	}
	return login
}

// Load reads the limits recorded at path. A missing file holds none. A line that
// does not parse is skipped, because one bad write must not hide the others.
func Load(path string) ([]Limit, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var limits []Limit
	for line := range bytes.Lines(data) {
		var limit Limit
		if err := json.Unmarshal(line, &limit); err != nil {
			continue
		}
		limits = append(limits, limit)
	}
	return limits, nil
}

// Append records limit at path. Every docket instance appends to the one file,
// so they all pick the same account. Nothing rewrites the file, and each limit
// is one short write to a file opened for append. An append therefore needs no
// lock.
func Append(path string, limit Limit) error {
	line, err := json.Marshal(limit)
	if err != nil {
		return fmt.Errorf("encode limit: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return fmt.Errorf("append to %s: %w", path, err)
	}
	return f.Close()
}
