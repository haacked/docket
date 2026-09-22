// Package index stores docket's records as an append-only JSONL event log.
package index

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/haacked/docket/internal/core/review"
)

// CompactRatio is how many events per record docket tolerates before it
// rewrites the log as one snapshot event per record.
const CompactRatio = 5

// Store is the event log. Several docket instances can append to it at once.
// Every append holds an exclusive lock on the lock file, then writes one line in a
// single write to a file opened for append.
type Store struct {
	path     string
	lockPath string
}

func New(indexPath, lockPath string) *Store {
	return &Store{path: indexPath, lockPath: lockPath}
}

// Append adds one event to the log.
func (s *Store) Append(e review.Event) error {
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encode event: %w", err)
	}
	line = append(line, '\n')

	release, err := s.lock(true)
	if err != nil {
		return err
	}
	defer release()

	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", s.path, err)
	}
	defer f.Close()

	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("append to %s: %w", s.path, err)
	}
	return nil
}

// Events reads the log. It skips a line that does not parse, because one bad
// write must not cost the user every other record.
func (s *Store) Events() ([]review.Event, error) {
	release, err := s.lock(false)
	if err != nil {
		return nil, err
	}
	defer release()
	return s.readEvents()
}

func (s *Store) readEvents() ([]review.Event, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", s.path, err)
	}

	var events []review.Event
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var e review.Event
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		events = append(events, e)
	}
	if err := sc.Err(); err != nil {
		return events, fmt.Errorf("scan %s: %w", s.path, err)
	}
	return events, nil
}

// Load folds the log into records.
func (s *Store) Load() ([]review.Record, error) {
	events, err := s.Events()
	if err != nil {
		return nil, err
	}
	return review.Fold(events), nil
}

// Compact rewrites the log as one snapshot event per record, whatever its size.
// It holds the same lock an append does, and swaps the file in with a rename so a
// reader sees either the old log or the new one. docket itself goes through
// CompactIfNeeded. This is the entry the locking test drives.
func (s *Store) Compact() error {
	release, err := s.lock(true)
	if err != nil {
		return err
	}
	defer release()

	events, err := s.readEvents()
	if err != nil {
		return err
	}
	if i := slices.IndexFunc(events, func(e review.Event) bool { return !review.KnownEvent(e) }); i >= 0 {
		return fmt.Errorf("event type %q is not one this docket knows; compacting would delete it", events[i].Type)
	}
	return s.compactLocked(review.Fold(events))
}

// CompactIfNeeded compacts once the log carries more than CompactRatio events
// per record. docket calls it at startup.
func (s *Store) CompactIfNeeded() (bool, error) {
	release, err := s.lock(true)
	if err != nil {
		return false, err
	}
	defer release()

	events, err := s.readEvents()
	if err != nil {
		return false, err
	}
	// A log carrying an event this build does not know belongs to a newer docket.
	// Rewriting it from the fold would delete that event, so leave the file alone
	// and let the newer binary compact it. The log grows meanwhile.
	if slices.ContainsFunc(events, func(e review.Event) bool { return !review.KnownEvent(e) }) {
		return false, nil
	}
	records := review.Fold(events)
	if len(records) == 0 || len(events) <= CompactRatio*len(records) {
		return false, nil
	}
	if err := s.compactLocked(records); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) compactLocked(records []review.Record) error {
	var buf bytes.Buffer
	now := time.Now().UTC()
	for _, rec := range records {
		eventType, ok := review.EventForState(rec.State)
		if !ok {
			return fmt.Errorf("record %s has unknown state %q", rec.ID, rec.State)
		}
		fields, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("encode record %s: %w", rec.ID, err)
		}
		line, err := json.Marshal(review.Event{ID: rec.ID, At: now, Type: eventType, Fields: fields})
		if err != nil {
			return fmt.Errorf("encode snapshot for %s: %w", rec.ID, err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}

	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".index.jsonl.*")
	if err != nil {
		return fmt.Errorf("create temp index: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp index: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp index: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("replace %s: %w", s.path, err)
	}
	return nil
}

// StatMark is a cheap fingerprint of the index file's state on disk, used to
// detect that another process appended or compacted without taking the
// shared lock. Compact swaps the file in with a rename. Two appends within
// one coarse-mtime second can differ only in size, which is why ModTime alone
// is not enough.
type StatMark struct {
	ModTime time.Time
	Size    int64
}

func (s StatMark) equal(other StatMark) bool {
	return s.Size == other.Size && s.ModTime.Equal(other.ModTime)
}

// Stat reads the index file's current StatMark. It takes no lock, so polling
// it does not contend with another process's Append. A missing index reports
// the zero StatMark rather than an error, matching Events' treatment of one.
func (s *Store) Stat() (StatMark, error) {
	info, err := os.Stat(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return StatMark{}, nil
		}
		return StatMark{}, fmt.Errorf("stat %s: %w", s.path, err)
	}
	return StatMark{ModTime: info.ModTime(), Size: info.Size()}, nil
}

// Changed reports whether the index file's StatMark differs from prev. It
// returns the current StatMark to compare against next time.
func (s *Store) Changed(prev StatMark) (StatMark, bool, error) {
	cur, err := s.Stat()
	if err != nil {
		return prev, false, err
	}
	return cur, !cur.equal(prev), nil
}

func (s *Store) lock(exclusive bool) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(s.lockPath), 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", filepath.Dir(s.lockPath), err)
	}
	f, err := os.OpenFile(s.lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", s.lockPath, err)
	}
	if err := lockFD(int(f.Fd()), exclusive); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock %s: %w", s.lockPath, err)
	}
	return func() {
		_ = unlockFD(int(f.Fd()))
		_ = f.Close()
	}, nil
}
