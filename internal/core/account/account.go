// Package account picks the claude account a new session runs under. docket
// rotates through the accounts config.toml names, and moves past an account once
// claude refuses one of its sessions for a usage limit.
package account

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"time"
)

// Limit records that claude refused a session because its account was over a
// usage limit.
type Limit struct {
	// Account is the account's CLAUDE_CONFIG_DIR, or "" for claude's default
	// account.
	Account string    `json:"account"`
	At      time.Time `json:"at"`
	Until   time.Time `json:"until"`
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
// limits is every limit docket has recorded, in any order. A limit on an
// account that is not in the rotation counts for nothing.
//
// The rotation starts after the account that most recently hit a limit, and
// takes the first account that is not over one. docket therefore stays on an
// account until that account hits a limit, even after an earlier account's
// limit resets. Before any account has hit a limit, the pick is the first.
func Pick(accounts []string, limits []Limit, now time.Time) Choice {
	if len(accounts) == 0 {
		return Choice{}
	}
	start := 0
	var latest time.Time
	for _, limit := range limits {
		if i := slices.Index(accounts, limit.Account); i >= 0 && limit.At.After(latest) {
			latest, start = limit.At, i+1
		}
	}

	var soonest Choice
	for step := range len(accounts) {
		candidate := accounts[(start+step)%len(accounts)]
		until := reset(candidate, limits)
		if !until.After(now) {
			return Choice{Account: candidate}
		}
		if soonest.Until.IsZero() || until.Before(soonest.Until) {
			soonest = Choice{Account: candidate, Until: until}
		}
	}
	return soonest
}

// Limited reports whether account is over a limit at now.
func Limited(account string, limits []Limit, now time.Time) bool {
	return reset(account, limits).After(now)
}

// reset is when the last of account's limits resets.
func reset(account string, limits []Limit) time.Time {
	var until time.Time
	for _, limit := range limits {
		if limit.Account == account && limit.Until.After(until) {
			until = limit.Until
		}
	}
	return until
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
