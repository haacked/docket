// Package filemark tells whether a file changed since it was last read, from
// its modification time and size. A stat takes no lock and starts no
// subprocess, so a watcher can run one every couple of seconds.
package filemark

import (
	"fmt"
	"os"
	"time"
)

// Mark is a file's modification time and size. A missing file has the zero
// Mark.
type Mark struct {
	ModTime time.Time
	Size    int64
}

// Equal reports whether m and other describe the same state of the file. It
// compares both fields, because two writes in the same second on a filesystem
// with second-granularity mtimes differ only in size.
func (m Mark) Equal(other Mark) bool {
	return m.Size == other.Size && m.ModTime.Equal(other.ModTime)
}

// Changed reports whether the Mark of the file at path differs from prev. It
// returns the current Mark to compare against next time.
func Changed(path string, prev Mark) (Mark, bool, error) {
	cur, err := Of(path)
	if err != nil {
		return prev, false, err
	}
	return cur, !cur.Equal(prev), nil
}

// Of reads the Mark of the file at path.
func Of(path string) (Mark, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Mark{}, nil
		}
		return Mark{}, fmt.Errorf("stat %s: %w", path, err)
	}
	return Mark{ModTime: info.ModTime(), Size: info.Size()}, nil
}
