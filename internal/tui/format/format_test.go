package format

import (
	"testing"
	"time"
	"unicode/utf8"
)

func TestTruncateCutsOnRuneBoundaries(t *testing.T) {
	got := Truncate("Fix the café résumé parser ✨ and the widgets", 22)
	if !utf8.ValidString(got) {
		t.Errorf("Truncate returned invalid UTF-8: %q", got)
	}
	if r := []rune(got); len(r) > 22 {
		t.Errorf("Truncate returned %d runes, want at most 22", len(r))
	}
}

func TestAgoPicksTheCoarsestUnitThatFits(t *testing.T) {
	for d, want := range map[time.Duration]string{
		-time.Second:     "just now",
		30 * time.Second: "just now",
		5 * time.Minute:  "5m ago",
		3 * time.Hour:    "3h ago",
		50 * time.Hour:   "2d ago",
	} {
		if got := Ago(d); got != want {
			t.Errorf("Ago(%v) = %q, want %q", d, got, want)
		}
	}
}
