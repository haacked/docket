package index_test

// This file tests Store.Stat and Store.Changed, which let one docket process
// notice that another process appended to (or compacted) the shared index
// file, without taking the shared flock on every check:
//
//	func (s *Store) Stat() (StatMark, error)
//	func (s *Store) Changed(prev StatMark) (StatMark, bool, error)
//
// Changed compares both ModTime and Size against prev. Compact swaps the file
// in with a rename. On a filesystem with second-granularity mtimes, a
// compaction landing in the same second as the appends before it can leave
// ModTime unchanged while Size drops, so ModTime alone would miss it.
// TestChangedDetectsACompactEvenWithAnUnchangedModTime forces exactly that
// case with os.Chtimes. It is the one test here that fails against an
// mtime-only implementation.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/haacked/docket/internal/core/index"
)

func TestStatOnAFreshIndexEstablishesABaselineWithoutError(t *testing.T) {
	store := newStore(t, indexPath(t))

	mark, err := store.Stat()

	if err != nil {
		t.Fatalf("Stat() on a store with no index file returned error %v", err)
	}
	if mark != (index.StatMark{}) {
		t.Errorf("Stat() on a missing index = %+v, want the zero StatMark", mark)
	}
}

// A missing index file is not an error, distinct from a genuine stat
// failure. A parent path component that is a regular file, not a directory,
// gives os.Stat a deterministic error that is not IsNotExist.
func TestStatReturnsAnErrorForAFailureThatIsNotAMissingFile(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parent, nil, 0o644); err != nil {
		t.Fatalf("seed %s: %v", parent, err)
	}
	store := newStore(t, filepath.Join(parent, "index.jsonl"))

	if _, err := store.Stat(); err == nil {
		t.Error("Stat() through a path component that is a file returned no error")
	}
}

func TestChangedLeavesTheMarkAloneOnAStatFailure(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parent, nil, 0o644); err != nil {
		t.Fatalf("seed %s: %v", parent, err)
	}
	store := newStore(t, filepath.Join(parent, "index.jsonl"))
	prev := index.StatMark{Size: 7}

	got, changed, err := store.Changed(prev)

	if err == nil {
		t.Fatal("Changed() through a path component that is a file returned no error")
	}
	if changed {
		t.Error("Changed() reported a change on a stat failure, want none: nothing was confirmed")
	}
	if got != prev {
		t.Errorf("Changed() returned mark %+v on error, want prev unchanged (%+v)", got, prev)
	}
}

func TestChangedReportsNoChangeAgainstItsOwnFreshBaseline(t *testing.T) {
	store := newStore(t, indexPath(t))
	mark, err := store.Stat()
	if err != nil {
		t.Fatalf("Stat() returned error %v", err)
	}

	_, changed, err := store.Changed(mark)

	if err != nil {
		t.Fatalf("Changed() returned error %v", err)
	}
	if changed {
		t.Error("Changed() reported a change against its own fresh baseline, want none")
	}
}

// TestChangedReportsTrueAfterASecondStoreHandleAppends is the core of the
// watcher: a second Store pointed at the same path stands in for a second
// docket process.
func TestChangedReportsTrueAfterASecondStoreHandleAppends(t *testing.T) {
	path := indexPath(t)
	first := newStore(t, path)
	second := newStore(t, path)
	mark, err := first.Stat()
	if err != nil {
		t.Fatalf("Stat() returned error %v", err)
	}

	appendAll(t, second, ev("rec-1", "started", indexBase, map[string]any{"title": "Fix the retry loop"}))

	_, changed, err := first.Changed(mark)
	if err != nil {
		t.Fatalf("Changed() returned error %v", err)
	}
	if !changed {
		t.Error("Changed() missed an append made through a second Store handle on the same path")
	}
}

// TestChangedIsFalseOnceTheMarkCatchesUp is what keeps a poll loop from
// falsely firing on every tick: the mark Changed just returned must itself
// report no change until something appends again.
func TestChangedIsFalseOnceTheMarkCatchesUp(t *testing.T) {
	path := indexPath(t)
	first := newStore(t, path)
	second := newStore(t, path)
	mark, err := first.Stat()
	if err != nil {
		t.Fatalf("Stat() returned error %v", err)
	}
	appendAll(t, second, ev("rec-1", "started", indexBase, map[string]any{"title": "Fix the retry loop"}))
	mark, changed, err := first.Changed(mark)
	if err != nil {
		t.Fatalf("Changed() returned error %v", err)
	}
	if !changed {
		t.Fatal("Changed() missed the append this test seeds")
	}

	_, changedAgain, err := first.Changed(mark)

	if err != nil {
		t.Fatalf("Changed() returned error %v", err)
	}
	if changedAgain {
		t.Error("Changed() fired again against the mark it just returned, with no new append; a poll loop would false-fire forever")
	}
}

// TestChangedReportsTrueAfterCompact covers Compact under normal conditions.
// TestChangedDetectsACompactEvenWithAnUnchangedModTime below covers the
// same-second mtime collision that motivates comparing Size too.
func TestChangedReportsTrueAfterCompact(t *testing.T) {
	path := indexPath(t)
	store := newStore(t, path)
	appendAll(t, store,
		ev("rec-1", "started", indexBase, map[string]any{"title": "Fix the retry loop"}),
		ev("rec-1", "drafted", indexBase.Add(time.Minute), nil),
		ev("rec-1", "submitted", indexBase.Add(2*time.Minute), nil),
	)
	mark, err := store.Stat()
	if err != nil {
		t.Fatalf("Stat() returned error %v", err)
	}

	if err := store.Compact(); err != nil {
		t.Fatalf("Compact() returned error %v", err)
	}

	_, changed, err := store.Changed(mark)
	if err != nil {
		t.Fatalf("Changed() returned error %v", err)
	}
	if !changed {
		t.Error("Changed() missed a Compact that rewrote the file")
	}
}

// TestChangedDetectsACompactEvenWithAnUnchangedModTime forces the collision a
// same-second Compact can produce. It pins the compacted file's mtime back to
// the pre-compact mark with os.Chtimes, so ModTime alone would report no
// change even though Compact shrank the file. Changed must still catch it,
// which only holds if it also compares Size.
func TestChangedDetectsACompactEvenWithAnUnchangedModTime(t *testing.T) {
	path := indexPath(t)
	store := newStore(t, path)
	appendAll(t, store,
		ev("rec-1", "started", indexBase, map[string]any{"title": "Fix the retry loop"}),
		ev("rec-1", "drafted", indexBase.Add(time.Minute), nil),
		ev("rec-1", "submitted", indexBase.Add(2*time.Minute), nil),
	)
	mark, err := store.Stat()
	if err != nil {
		t.Fatalf("Stat() returned error %v", err)
	}

	if err := store.Compact(); err != nil {
		t.Fatalf("Compact() returned error %v", err)
	}
	if err := os.Chtimes(path, mark.ModTime, mark.ModTime); err != nil {
		t.Fatalf("os.Chtimes: %v", err)
	}

	_, changed, err := store.Changed(mark)

	if err != nil {
		t.Fatalf("Changed() returned error %v", err)
	}
	if !changed {
		t.Error("Changed() missed a Compact whose mtime collided with the mark before it; comparing ModTime alone is not enough")
	}
}

// TestTwoStoreHandlesWatchingTheSamePathSeeEachOthersAppends is the scenario
// the whole feature exists for: two docket processes, each polling with its
// own Store and its own remembered mark.
func TestTwoStoreHandlesWatchingTheSamePathSeeEachOthersAppends(t *testing.T) {
	path := indexPath(t)
	alice := newStore(t, path)
	bob := newStore(t, path)
	aliceMark, err := alice.Stat()
	if err != nil {
		t.Fatalf("alice: Stat() returned error %v", err)
	}
	bobMark, err := bob.Stat()
	if err != nil {
		t.Fatalf("bob: Stat() returned error %v", err)
	}

	appendAll(t, alice, ev("rec-1", "started", indexBase, map[string]any{"title": "Alice's review"}))

	bobMark, bobSeesAlice, err := bob.Changed(bobMark)
	if err != nil {
		t.Fatalf("bob: Changed() returned error %v", err)
	}
	if !bobSeesAlice {
		t.Error("bob's Changed() missed alice's append")
	}

	// Catch alice's own mark up past her own append, so the check below can
	// only pass by seeing bob's, not by seeing her own again.
	aliceMark, _, err = alice.Changed(aliceMark)
	if err != nil {
		t.Fatalf("alice: Changed() returned error %v", err)
	}

	appendAll(t, bob, ev("rec-2", "started", indexBase.Add(time.Minute), map[string]any{"title": "Bob's review"}))

	_, aliceSeesBob, err := alice.Changed(aliceMark)
	if err != nil {
		t.Fatalf("alice: Changed() returned error %v", err)
	}
	if !aliceSeesBob {
		t.Error("alice's Changed() missed bob's append")
	}
	_, bobSeesHisOwnAppend, err := bob.Changed(bobMark)
	if err != nil {
		t.Fatalf("bob: Changed() returned error %v", err)
	}
	if !bobSeesHisOwnAppend {
		t.Error("bob's Changed() missed his own append against his last-seen mark")
	}
}
