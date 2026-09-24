package format

import (
	"strings"
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

func TestTruncateLeavesNothingWithNoRoom(t *testing.T) {
	for _, width := range []int{-3, 0, 1} {
		if got := Truncate("title", width); got != "" {
			t.Errorf("Truncate(_, %d) = %q, want nothing", width, got)
		}
	}
}

// The metadata is what tells rows apart, so Row shortens the title first.
func TestRowShrinksTheTitleBeforeTheMetadata(t *testing.T) {
	left, meta := Row("> o/r#1  ", strings.Repeat("title ", 20), "· claude · 4m ago", 60)

	if meta != "· claude · 4m ago" {
		t.Errorf("meta = %q, want it whole", meta)
	}
	if got := Columns(left) + 1 + Columns(meta); got != 60 {
		t.Errorf("the row is %d columns, want 60", got)
	}
}

// A terminal too narrow for a minimum title cuts the metadata so the row still
// fits.
func TestRowCutsTheMetadataWhenTheTitleCannotShrinkFurther(t *testing.T) {
	left, meta := Row("> o/r#1  ", strings.Repeat("title ", 20), "· claude · tier1 · 4m ago · background", 40)

	if got := Columns(left) + 1 + Columns(meta); got > 40 {
		t.Errorf("the row is %d columns, want at most 40", got)
	}
}

func TestDurationPicksTheCoarsestUnitThatFits(t *testing.T) {
	for d, want := range map[time.Duration]string{
		12 * time.Minute: "12m",
		3 * time.Hour:    "3h",
		50 * time.Hour:   "2d",
	} {
		if got := Duration(d); got != want {
			t.Errorf("Duration(%v) = %q, want %q", d, got, want)
		}
	}
}

// Below the minimum title, the row itself is cut, so no line is wider than the
// terminal.
func TestRowFitsATerminalNarrowerThanTheMinimumTitle(t *testing.T) {
	left, meta := Row("> PostHog/posthog-android#805  ", strings.Repeat("title ", 20), "· claude", 30)

	if got := Columns(left) + Columns(meta); got > 30 {
		t.Errorf("the row is %d columns, want at most 30", got)
	}
	if meta != "" {
		t.Errorf("meta = %q, want nothing on a row with no room for it", meta)
	}
}

// An emoji takes two terminal columns. Counting runes would let a title that
// carries one overflow the row by a column.
func TestWideCharactersCountAsTwoColumns(t *testing.T) {
	if got := Columns("✨ feat"); got != 7 {
		t.Errorf("Columns = %d, want 7", got)
	}
	if got := Columns(Truncate("✨✨✨✨✨✨", 5)); got > 5 {
		t.Errorf("Truncate left %d columns, want at most 5", got)
	}
	left, meta := Row("> o/r#1  ", strings.Repeat("✨ feat ", 10), "· claude", 40)
	if got := Columns(left) + 1 + Columns(meta); got > 40 {
		t.Errorf("the row is %d columns, want at most 40", got)
	}
}
