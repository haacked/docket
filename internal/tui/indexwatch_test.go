package tui

// This file tests the root's reaction to indexChangedMsg, which reports that
// another docket process appended to the shared index file. These tests
// construct indexChangedMsg{} with no fields. The type in fact carries a
// stamp, used to remember what was last seen, but a zero-value literal
// exercises the same Update case and keeps this file agnostic of that field.
//
// recordsLoadedMsg already preserves the dashboard's selection by record ID
// (dashboard.Model.SetRecords) and never touches dash.Busy, so routing the
// reload through the existing recordsLoadedMsg path is what keeps a
// mid-operation row's busy marker and the cursor both intact.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/haacked/docket/internal/core/config"
	"github.com/haacked/docket/internal/core/index"
	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/core/session"
)

// TestIndexChangedLeavesSelectionAndBusyAloneUntilTheReloadLands checks what
// Update itself can promise synchronously: it hands back a reload command and
// touches neither the cursor nor the busy map before that command's message
// comes back around. The reload is async, so this is as far as Update alone
// goes. TestIndexChangedReloadPreservesSelectionAndBusy below drives the
// returned command to completion against a real Store.
func TestIndexChangedLeavesSelectionAndBusyAloneUntilTheReloadLands(t *testing.T) {
	a := app()
	records := []review.Record{
		{ID: "rec-a", State: review.StateReviewing},
		{ID: "rec-b", State: review.StateReviewing},
		{ID: "rec-c", State: review.StateReviewing},
	}
	next, _ := a.Update(recordsLoadedMsg{records: records})
	a = next.(App)
	a.dash.Cursor = 1
	a.dash.Busy["rec-b"] = "reading GitHub"

	next, cmd := a.Update(indexChangedMsg{})
	a = next.(App)

	if cmd == nil {
		t.Fatal("indexChangedMsg returned no command; the index-watcher reload never happens")
	}
	if got, ok := a.dash.Selected(); !ok || got.ID != "rec-b" {
		t.Errorf("Update(indexChangedMsg{}) moved the selection to %+v (ok=%v), want it to stay on rec-b before the reload lands", got, ok)
	}
	if a.dash.Busy["rec-b"] != "reading GitHub" {
		t.Errorf(`Update(indexChangedMsg{}) changed Busy["rec-b"] to %q before the reload lands`, a.dash.Busy["rec-b"])
	}
}

// serviceOverIndex builds a session.Service backed by a real, temporary Store,
// which is enough to exercise Service.Records (it reads only s.Store) without
// a fake GitHub or git. It also returns the paths, so a test can open a second
// Store handle on the same files to stand in for a second docket process.
func serviceOverIndex(t *testing.T) (*session.Service, config.Paths) {
	t.Helper()
	paths, err := config.NewPaths(t.TempDir())
	if err != nil {
		t.Fatalf("config.NewPaths: %v", err)
	}
	if err := paths.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	store := index.New(paths.Index, paths.Lock)
	return &session.Service{Store: store}, paths
}

func startedEvent(t *testing.T, id, title string, at time.Time) review.Event {
	t.Helper()
	fields, err := json.Marshal(map[string]any{"title": title})
	if err != nil {
		t.Fatalf("encode fields: %v", err)
	}
	return review.Event{ID: id, At: at, Type: review.EventStarted, Fields: fields}
}

// TestIndexChangedReloadPreservesSelectionAndBusy drives the command
// indexChangedMsg returns to completion against a real index.Store, the way a
// second docket process's append would really be noticed. It appends through
// a second Store handle on the same path, feeds the resulting message back
// into Update, and checks that neither the selected row nor a row's busy
// marker gets disturbed by a reload this package itself did not ask for.
func TestIndexChangedReloadPreservesSelectionAndBusy(t *testing.T) {
	svc, paths := serviceOverIndex(t)
	base := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	if err := svc.Store.Append(startedEvent(t, "rec-a", "First review", base)); err != nil {
		t.Fatalf("seed rec-a: %v", err)
	}
	if err := svc.Store.Append(startedEvent(t, "rec-b", "Second review", base.Add(time.Minute))); err != nil {
		t.Fatalf("seed rec-b: %v", err)
	}
	a := New(svc, config.Config{DefaultEngine: "claude"}, "", false)
	next, _ := a.Update(recordsLoadedMsg{records: []review.Record{
		{ID: "rec-a", State: review.StateReviewing},
		{ID: "rec-b", State: review.StateReviewing},
	}})
	a = next.(App)
	a.dash.Cursor = 1 // selects rec-b
	a.dash.Busy["rec-b"] = "reading GitHub"

	// A second docket process, using a second Store handle on the same path,
	// appends a third record.
	other := index.New(paths.Index, paths.Lock)
	if err := other.Append(startedEvent(t, "rec-c", "A second process's review", base.Add(2*time.Minute))); err != nil {
		t.Fatalf("append from the second process: %v", err)
	}

	_, cmd := a.Update(indexChangedMsg{})
	if cmd == nil {
		t.Fatal("indexChangedMsg returned no reload command")
	}
	got := drain(cmd)
	if len(got) != 1 {
		t.Fatalf("indexChangedMsg's command produced %#v, want one recordsLoadedMsg", got)
	}
	loadedMsg, ok := got[0].(recordsLoadedMsg)
	if !ok {
		t.Fatalf("indexChangedMsg's command produced %#v, want a recordsLoadedMsg", got[0])
	}
	if len(loadedMsg.records) != 3 {
		t.Fatalf("the reload returned %d records, want the 3rd process's append to have landed (3)", len(loadedMsg.records))
	}

	next, _ = a.Update(loadedMsg)
	a = next.(App)

	if got, ok := a.dash.Selected(); !ok || got.ID != "rec-b" {
		t.Errorf("selection after the index-watcher reload = %+v (ok=%v), want it to stay on rec-b", got, ok)
	}
	if a.dash.Busy["rec-b"] != "reading GitHub" {
		t.Errorf(`Busy["rec-b"] = %q after the index-watcher reload, want "reading GitHub" untouched`, a.dash.Busy["rec-b"])
	}
}

// docket mcp can start a background review while this dashboard has nothing
// running and so no tick outstanding. The reload that brings the review in has
// to arm the tick, or nothing polls it.
func TestAReloadArmsThePollForABackgroundReviewAnotherProcessStarted(t *testing.T) {
	running := []review.Record{backgroundRecord(review.StateReviewing)}
	starting := backgroundRecord(review.StateReviewing)
	starting.BGID = ""

	tests := []struct {
		name    string
		dryRun  bool
		polling bool
		records []review.Record
		want    bool
	}{
		{name: "a running review", records: running, want: true},
		{name: "a tick already outstanding", polling: true, records: running},
		{name: "a dry run", dryRun: true, records: running},
		// A launch writes its record before claude reports the session's id. A
		// poll leaves it alone within the launch's grace, then adopts its session
		// or closes it.
		{name: "a launch still starting", records: []review.Record{starting}, want: true},
		{name: "nothing running", records: []review.Record{backgroundRecord(review.StateDrafted)}},
	}
	for _, tc := range tests {
		a := New(nil, config.Config{DefaultEngine: "claude"}, "", tc.dryRun)
		a.polling = tc.polling

		next, cmd := a.Update(recordsLoadedMsg{records: tc.records})

		if armed := cmd != nil && next.(App).polling; armed != tc.want {
			t.Errorf("%s: armed = %v, want %v", tc.name, armed, tc.want)
		}
	}
}
