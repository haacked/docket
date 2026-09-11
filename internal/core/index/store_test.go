package index_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/haacked/docket/internal/core/index"
	"github.com/haacked/docket/internal/core/review"
)

var indexBase = time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)

// newStore is the single place the tests construct a Store.
func newStore(t *testing.T, path string) *index.Store {
	t.Helper()
	return index.New(path, filepath.Join(filepath.Dir(path), "index.lock"))
}

// ev is the single place the tests name Event's fields.
func ev(id, eventType string, at time.Time, fields map[string]any) review.Event {
	return review.Event{ID: id, At: at, Type: eventType, Fields: rawFields(fields)}
}

func rawFields(fields map[string]any) json.RawMessage {
	if len(fields) == 0 {
		return nil
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	return raw
}

func indexPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "index.jsonl")
}

func appendAll(t *testing.T, store *index.Store, events ...review.Event) {
	t.Helper()
	for _, e := range events {
		if err := store.Append(e); err != nil {
			t.Fatalf("Append(%+v) returned error %v", e, err)
		}
	}
}

func load(t *testing.T, store *index.Store) []review.Record {
	t.Helper()
	records, err := store.Load()
	if err != nil {
		t.Fatalf("Load() returned error %v", err)
	}
	return records
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func readJSONLines(t *testing.T, path string) []string {
	t.Helper()
	lines := readLines(t, path)
	for i, line := range lines {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Errorf("line %d of the index is not valid JSON (%v): %s", i+1, err, line)
		}
	}
	return lines
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Size()
}

// projection carries the record fields these tests set, so comparisons skip the
// zero-value and timestamp fields that a JSONL round trip may represent
// differently.
type projection struct {
	ID        string
	State     review.State
	Title     string
	NotesPath string
}

func project(records []review.Record) []projection {
	out := make([]projection, 0, len(records))
	for _, rec := range records {
		out = append(out, projection{ID: rec.ID, State: rec.State, Title: rec.Title, NotesPath: rec.NotesPath})
	}
	return out
}

func TestAppendThenLoadReturnsTheFoldedRecords(t *testing.T) {
	path := indexPath(t)
	store := newStore(t, path)
	events := []review.Event{
		ev("rec-1", "started", indexBase, map[string]any{"title": "Fix the retry loop"}),
		ev("rec-2", "started", indexBase.Add(time.Minute), map[string]any{"title": "Add the index watcher"}),
		ev("rec-1", "drafted", indexBase.Add(20*time.Minute), map[string]any{"notes_path": "/notes/acme/tool/pr-123.md"}),
	}
	want := []projection{
		{ID: "rec-1", State: review.StateDrafted, Title: "Fix the retry loop", NotesPath: "/notes/acme/tool/pr-123.md"},
		{ID: "rec-2", State: review.StateReviewing, Title: "Add the index watcher"},
	}

	appendAll(t, store, events...)
	got := project(load(t, store))

	if !slices.Equal(got, want) {
		t.Errorf("Load() = %+v, want %+v", got, want)
	}
}

func TestLoadMatchesFoldOverTheSameEvents(t *testing.T) {
	path := indexPath(t)
	store := newStore(t, path)
	events := []review.Event{
		ev("rec-1", "started", indexBase, map[string]any{"title": "Fix the retry loop"}),
		ev("rec-2", "started", indexBase.Add(time.Minute), map[string]any{"title": "Add the index watcher"}),
		ev("rec-1", "submitted", indexBase.Add(30*time.Minute), map[string]any{"notes_path": "/notes/acme/tool/pr-123.md"}),
		ev("rec-2", "abandoned", indexBase.Add(40*time.Minute), nil),
	}

	appendAll(t, store, events...)
	got := project(load(t, store))
	want := project(review.Fold(events))

	if !slices.Equal(got, want) {
		t.Errorf("Load() = %+v, want Fold of the same events %+v", got, want)
	}
}

func TestLoadReturnsNoRecordsWhenTheIndexDoesNotExist(t *testing.T) {
	store := newStore(t, indexPath(t))

	got := load(t, store)

	if len(got) != 0 {
		t.Errorf("Load() on a missing index returned %d records (%+v), want 0", len(got), got)
	}
}

func TestLoadSkipsMalformedLines(t *testing.T) {
	path := indexPath(t)
	store := newStore(t, path)
	events := []review.Event{
		ev("rec-1", "started", indexBase, map[string]any{"title": "Fix the retry loop"}),
		ev("rec-2", "started", indexBase.Add(time.Minute), map[string]any{"title": "Add the index watcher"}),
		ev("rec-1", "drafted", indexBase.Add(20*time.Minute), map[string]any{"notes_path": "/notes/acme/tool/pr-123.md"}),
	}
	appendAll(t, store, events...)
	good := readLines(t, path)
	if len(good) != len(events) {
		t.Fatalf("the index holds %d lines after %d appends, want one line per event", len(good), len(events))
	}
	corrupted := strings.Join([]string{
		good[0],
		"{not json at all",
		good[1],
		`{"id":`,
		"",
		"   ",
		good[2],
		"}",
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(corrupted), 0o644); err != nil {
		t.Fatalf("writing the corrupted index: %v", err)
	}
	want := []projection{
		{ID: "rec-1", State: review.StateDrafted, Title: "Fix the retry loop", NotesPath: "/notes/acme/tool/pr-123.md"},
		{ID: "rec-2", State: review.StateReviewing, Title: "Add the index watcher"},
	}

	got := project(load(t, store))

	if !slices.Equal(got, want) {
		t.Errorf("Load() over an index with malformed lines = %+v, want %+v", got, want)
	}
}

func TestAppendWritesOneValidJSONLinePerEvent(t *testing.T) {
	path := indexPath(t)
	store := newStore(t, path)
	events := []review.Event{
		ev("rec-1", "started", indexBase, map[string]any{"title": "Fix the retry loop"}),
		ev("rec-1", "drafted", indexBase.Add(time.Minute), nil),
		ev("rec-1", "submitted", indexBase.Add(2*time.Minute), nil),
	}

	appendAll(t, store, events...)

	lines := readJSONLines(t, path)
	if len(lines) != len(events) {
		t.Errorf("the index holds %d lines after %d appends, want one line per event", len(lines), len(events))
	}
}

func TestConcurrentAppendsFromSeparateStoresAllLand(t *testing.T) {
	const writers = 8
	const perWriter = 12
	path := indexPath(t)
	var wantIDs []string
	for w := range writers {
		for i := range perWriter {
			wantIDs = append(wantIDs, fmt.Sprintf("rec-%d-%d", w, i))
		}
	}
	start := make(chan struct{})
	failures := make(chan error, writers*perWriter)
	var wg sync.WaitGroup

	for w := range writers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			// A Store per goroutine: coordination has to happen in the file,
			// not in one instance's memory.
			store := newStore(t, path)
			<-start
			for i := range perWriter {
				id := fmt.Sprintf("rec-%d-%d", w, i)
				e := ev(id, "started", indexBase.Add(time.Duration(i)*time.Second), map[string]any{"title": "Review " + id})
				if err := store.Append(e); err != nil {
					failures <- fmt.Errorf("Append(%s): %w", id, err)
				}
			}
		}(w)
	}
	close(start)
	wg.Wait()
	close(failures)

	for err := range failures {
		t.Errorf("concurrent append failed: %v", err)
	}
	lines := readJSONLines(t, path)
	if len(lines) != writers*perWriter {
		t.Errorf("the index holds %d lines after %d concurrent appends, want %d", len(lines), writers*perWriter, writers*perWriter)
	}
	var gotIDs []string
	for _, rec := range load(t, newStore(t, path)) {
		gotIDs = append(gotIDs, rec.ID)
	}
	sort.Strings(gotIDs)
	sort.Strings(wantIDs)
	if !slices.Equal(gotIDs, wantIDs) {
		t.Errorf("Load() returned %d record ids, want the %d appended ids (missing %v)", len(gotIDs), len(wantIDs), missing(wantIDs, gotIDs))
	}
}

func missing(want, got []string) []string {
	var out []string
	for _, id := range want {
		if !slices.Contains(got, id) {
			out = append(out, id)
		}
	}
	return out
}

func TestCompactPreservesTheFoldedRecordsAndShrinksTheFile(t *testing.T) {
	path := indexPath(t)
	store := newStore(t, path)
	at := indexBase
	for _, id := range []string{"rec-1", "rec-2"} {
		appendAll(t, store, ev(id, "started", at, map[string]any{"title": "Review " + id}))
		at = at.Add(time.Minute)
		for i := range 8 {
			appendAll(t, store, ev(id, "drafted", at, map[string]any{"notes_path": fmt.Sprintf("/notes/%s-%d.md", id, i)}))
			at = at.Add(time.Minute)
		}
		appendAll(t, store, ev(id, "submitted", at, nil))
		at = at.Add(time.Minute)
	}
	before := project(load(t, store))
	if len(before) != 2 {
		t.Fatalf("Load() before Compact returned %d records (%+v), want 2", len(before), before)
	}
	linesBefore := len(readLines(t, path))
	sizeBefore := fileSize(t, path)

	if err := store.Compact(); err != nil {
		t.Fatalf("Compact() returned error %v", err)
	}

	after := project(load(t, store))
	if !slices.Equal(after, before) {
		t.Errorf("Load() after Compact = %+v, want the records from before Compact %+v", after, before)
	}
	linesAfter := len(readJSONLines(t, path))
	if linesAfter >= linesBefore {
		t.Errorf("the index holds %d lines after Compact, want fewer than the %d lines before it", linesAfter, linesBefore)
	}
	if sizeAfter := fileSize(t, path); sizeAfter >= sizeBefore {
		t.Errorf("the index is %d bytes after Compact, want smaller than the %d bytes before it", sizeAfter, sizeBefore)
	}
}

func TestCompactedIndexAcceptsFurtherAppends(t *testing.T) {
	path := indexPath(t)
	store := newStore(t, path)
	appendAll(t, store,
		ev("rec-1", "started", indexBase, map[string]any{"title": "Fix the retry loop"}),
		ev("rec-1", "drafted", indexBase.Add(time.Minute), nil),
		ev("rec-1", "submitted", indexBase.Add(2*time.Minute), nil),
	)
	if err := store.Compact(); err != nil {
		t.Fatalf("Compact() returned error %v", err)
	}

	appendAll(t, store, ev("rec-1", "archived", indexBase.Add(3*time.Minute), map[string]any{"notes_path": "/notes/acme/tool/pr-123.md"}))

	want := []projection{{ID: "rec-1", State: review.StateArchived, Title: "Fix the retry loop", NotesPath: "/notes/acme/tool/pr-123.md"}}
	got := project(load(t, store))
	if !slices.Equal(got, want) {
		t.Errorf("Load() after appending past a compaction = %+v, want %+v", got, want)
	}
}
