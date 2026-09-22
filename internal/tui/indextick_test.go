package tui

// This file drives the tick chain itself: indexTickMsg -> checkIndex() ->
// indexChangedMsg -> a.indexStamp = message.stamp. indexwatch_test.go covers
// what happens once indexChangedMsg arrives, but nothing there exercises how
// it gets produced. That is the one place a regression would be silent: drop
// the stamp assignment, and every tick reloads forever with no test failing.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/haacked/docket/internal/core/config"
	"github.com/haacked/docket/internal/core/index"
	"github.com/haacked/docket/internal/core/session"
)

func TestCheckIndexReportsNoChangeAgainstItsOwnFreshStamp(t *testing.T) {
	svc, _ := serviceOverIndex(t)
	a := New(svc, config.Config{DefaultEngine: "claude"}, "", false)
	stamp, err := svc.Store.Stat()
	if err != nil {
		t.Fatalf("Stat() returned error %v", err)
	}
	a.indexStamp = stamp

	if got := a.checkIndex()(); got != nil {
		t.Errorf("checkIndex() = %#v, want nil against its own fresh stamp", got)
	}
}

func TestCheckIndexReportsTheChangeAndFeedingItBackUpdatesTheStamp(t *testing.T) {
	svc, paths := serviceOverIndex(t)
	a := New(svc, config.Config{DefaultEngine: "claude"}, "", false)
	stamp, err := svc.Store.Stat()
	if err != nil {
		t.Fatalf("Stat() returned error %v", err)
	}
	a.indexStamp = stamp

	// A second Store handle on the same path stands in for a second docket
	// process appending while this one is idle.
	other := index.New(paths.Index, paths.Lock)
	if err := other.Append(startedEvent(t, "rec-1", "Another process's review", time.Now())); err != nil {
		t.Fatalf("append from the second process: %v", err)
	}

	got := a.checkIndex()()
	changed, ok := got.(indexChangedMsg)
	if !ok {
		t.Fatalf("checkIndex() produced %#v, want an indexChangedMsg", got)
	}

	next, cmd := a.Update(changed)
	a = next.(App)
	if cmd == nil {
		t.Fatal("indexChangedMsg returned no reload command")
	}
	if a.indexStamp != changed.stamp {
		t.Error("Update(indexChangedMsg) did not adopt the new stamp")
	}

	// Nothing appended since the stamp was adopted, so polling again with it
	// must report no change. A poll loop that false-fired here would reload
	// forever.
	if got := a.checkIndex()(); got != nil {
		t.Errorf("checkIndex() = %#v after adopting the new stamp, want nil", got)
	}
}

// A stat failure other than a missing file must be dropped rather than
// surfaced as errMsg, which would clear the dashboard's busy markers on a
// transient failure. A path component that is a regular file, not a
// directory, gives os.Stat a deterministic error to force this against.
func TestCheckIndexDropsAStatFailureRatherThanSurfacingIt(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parent, nil, 0o644); err != nil {
		t.Fatalf("seed %s: %v", parent, err)
	}
	store := index.New(filepath.Join(parent, "index.jsonl"), filepath.Join(parent, "index.lock"))
	a := New(&session.Service{Store: store}, config.Config{DefaultEngine: "claude"}, "", false)

	if got := a.checkIndex()(); got != nil {
		t.Errorf("checkIndex() = %#v on a stat failure, want nil rather than an errMsg", got)
	}
}

// Calling a tea.Batch command returns the tea.BatchMsg of its children
// without running them, so this is safe against the nil-svc app().
func TestIndexTickBatchesTheCheckAndTheNextArm(t *testing.T) {
	_, cmd := app().Update(indexTickMsg{})
	if cmd == nil {
		t.Fatal("indexTickMsg produced no command")
	}

	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("indexTickMsg produced %#v, want a batch of the check and the next tick", cmd())
	}
}
