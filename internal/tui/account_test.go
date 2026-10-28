package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/haacked/docket/internal/core/account"
	"github.com/haacked/docket/internal/core/config"
)

func titleLine(a App) string {
	above, _ := a.chrome()
	return ansi.Strip(strings.TrimSuffix(above, "\n"))
}

func TestTheTitleNamesTheAccountANewReviewRunsUnder(t *testing.T) {
	tests := []struct {
		name string
		next accountMsg
		want string
	}{
		{name: "the default account", next: accountMsg{}, want: "docket · claude account default"},
		{name: "a named account", next: accountMsg{choice: account.Choice{Account: "/opt/claude-max"}}, want: "docket · claude account /opt/claude-max"},
		{
			name: "every account limited",
			next: accountMsg{choice: account.Choice{Account: "/opt/claude-max", Until: time.Date(2026, 10, 28, 15, 4, 0, 0, time.Local)}},
			want: "docket · every claude account is over its limit until Oct 28 3:04 PM",
		},
		{name: "an unreadable log", next: accountMsg{err: errors.New("read limits.jsonl: permission denied")}, want: "docket · claude account unknown: read limits.jsonl: permission denied"},
	}
	for _, tc := range tests {
		a := New(nil, config.Config{DefaultEngine: "claude"}, "", false)
		next, _ := a.Update(tc.next)

		if got := titleLine(next.(App)); got != tc.want {
			t.Errorf("%s: title = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestTheTitleNamesNoAccountBeforeTheFirstRead(t *testing.T) {
	a := New(nil, config.Config{DefaultEngine: "claude"}, "", false)

	if got := titleLine(a); got != "docket" {
		t.Errorf("title = %q, want only docket", got)
	}
}

// Every reload reads the account through the service, so a limit another
// docket instance records shows up with the index append that follows it.
func TestAReloadReadsTheNextAccount(t *testing.T) {
	svc, paths := serviceOverIndex(t)
	svc.Paths = paths
	svc.Cfg = config.Config{ClaudeConfigDir: "/opt/claude-a", ClaudeFallbackConfigDirs: []string{"/opt/claude-b"}}
	now := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	svc.Now = func() time.Time { return now }
	resets := now.Add(5 * time.Hour)
	if err := account.Append(paths.Limits, account.Limit{Account: "/opt/claude-a", At: now, Until: resets}); err != nil {
		t.Fatalf("append limit: %v", err)
	}
	a := New(svc, config.Config{DefaultEngine: "claude"}, "", false)

	var next *accountMsg
	for _, m := range drain(a.reload()) {
		if m, ok := m.(accountMsg); ok {
			next = &m
		}
	}

	if next == nil || next.choice.Account != "/opt/claude-b" {
		t.Fatalf("next = %+v, want the fallback account", next)
	}
	if !next.choice.Recheck.Equal(resets) {
		t.Errorf("recheck = %v, want the first account's reset %v", next.choice.Recheck, resets)
	}
}

func TestTheIndexTickReadsTheAccountOnceARecheckPasses(t *testing.T) {
	a := New(nil, config.Config{DefaultEngine: "claude"}, "", false)
	model, _ := a.Update(accountMsg{choice: account.Choice{Account: "/opt/claude-b", Recheck: time.Now().Add(-time.Second)}})
	a = model.(App)

	model, cmd := a.Update(indexTickMsg{})

	if n := countCmds(cmd); n != 3 {
		t.Errorf("the tick ran %d commands, want the index check, the next tick, and the account read", n)
	}
	if got := model.(App).next.choice.Recheck; !got.IsZero() {
		t.Errorf("recheck = %v, want zero while the read runs, so the next tick does not read again", got)
	}
}

func TestTheIndexTickLeavesTheAccountBeforeItsRecheck(t *testing.T) {
	a := New(nil, config.Config{DefaultEngine: "claude"}, "", false)
	model, _ := a.Update(accountMsg{choice: account.Choice{Account: "/opt/claude-b", Recheck: time.Now().Add(time.Hour)}})
	a = model.(App)

	_, cmd := a.Update(indexTickMsg{})

	if n := countCmds(cmd); n != 2 {
		t.Errorf("the tick ran %d commands, want only the index check and the next tick", n)
	}
}

// countCmds counts the commands in cmd without running them. Update batches the
// index tick's commands. tea.Batch drops nil ones.
func countCmds(cmd tea.Cmd) int {
	if cmd == nil {
		return 0
	}
	if batch, ok := cmd().(tea.BatchMsg); ok {
		return len(batch)
	}
	return 1
}
