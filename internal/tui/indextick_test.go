package tui

// This file drives the tick chain itself: indexTickMsg -> checkIndex() ->
// indexChangedMsg -> a.indexStamp = message.stamp. indexwatch_test.go covers
// what happens once indexChangedMsg arrives, but nothing there exercises how
// it gets produced. That is the one place a regression would be silent: drop
// the stamp assignment, and every tick reloads forever with no test failing.

import (
	"testing"
	"time"

	"github.com/haacked/docket/internal/core/config"
	"github.com/haacked/docket/internal/core/index"
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

func TestIndexTickBatchesTheCheckAndTheNextArm(t *testing.T) {
	_, cmd := app().Update(indexTickMsg{})
	if cmd == nil {
		t.Fatal("indexTickMsg produced no command")
	}
}
