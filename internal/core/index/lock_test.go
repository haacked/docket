package index_test

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/haacked/docket/internal/core/index"
	"github.com/haacked/docket/internal/core/review"
)

// TestCompactDoesNotSwallowConcurrentAppends is the test that actually needs the
// lock. Compaction reads the whole log and replaces the file, so an append that
// lands between the read and the rename is lost unless both hold the same lock.
func TestCompactDoesNotSwallowConcurrentAppends(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.jsonl")
	lock := filepath.Join(dir, "index.lock")

	const writers = 8
	const perWriter = 12

	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			store := index.New(path, lock)
			id := fmt.Sprintf("rec-%d", w)
			for i := range perWriter {
				fields, err := json.Marshal(map[string]any{"title": fmt.Sprintf("%s round %d", id, i)})
				if err != nil {
					t.Error(err)
					return
				}
				event := review.Event{ID: id, At: time.Now().UTC(), Type: review.EventDrafted, Fields: fields}
				if err := store.Append(event); err != nil {
					t.Errorf("append: %v", err)
					return
				}
			}
		}(w)
	}

	compactor := index.New(path, lock)
	var compactWG sync.WaitGroup
	compactWG.Add(1)
	done := make(chan struct{})
	go func() {
		defer compactWG.Done()
		for {
			select {
			case <-done:
				return
			default:
				if err := compactor.Compact(); err != nil {
					t.Errorf("compact: %v", err)
					return
				}
			}
		}
	}()

	wg.Wait()
	close(done)
	compactWG.Wait()

	records, err := compactor.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(records) != writers {
		t.Fatalf("records = %d, want %d", len(records), writers)
	}

	byID := map[string]review.Record{}
	for _, rec := range records {
		byID[rec.ID] = rec
	}
	for w := range writers {
		id := fmt.Sprintf("rec-%d", w)
		rec, ok := byID[id]
		if !ok {
			t.Errorf("%s is missing", id)
			continue
		}
		if want := fmt.Sprintf("%s round %d", id, perWriter-1); rec.Title != want {
			t.Errorf("%s title = %q, want %q", id, rec.Title, want)
		}
		if rec.State != review.StateDrafted {
			t.Errorf("%s state = %q, want drafted", id, rec.State)
		}
	}
}
