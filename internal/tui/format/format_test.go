package format

import (
	"errors"
	"slices"
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

// A view drawn before the root's spinner first ticks has no frame to draw.
func TestTheSpinnerPutsItsFrameBeforeWhatIsRunning(t *testing.T) {
	for frame, want := range map[string]string{
		"⠙": "⠙ submitting…",
		"":  "submitting…",
	} {
		if got := (Spinner{Frame: frame}).Render("submitting"); got != want {
			t.Errorf("Spinner{Frame: %q}.Render(submitting) = %q, want %q", frame, got, want)
		}
	}
}

// A key and its label stay on one line, so the footer breaks only between hints.
func TestWrapBreaksOnlyBetweenItems(t *testing.T) {
	items := []string{"n new", "i requests", "enter resume", "R refresh all", "a show archived", "q quit"}

	got := Wrap(items, " · ", 30)

	for _, line := range strings.Split(got, "\n") {
		if Columns(line) > 30 {
			t.Errorf("line %q is %d columns, want at most 30", line, Columns(line))
		}
		if strings.HasPrefix(line, " · ") || strings.HasSuffix(line, " · ") {
			t.Errorf("line %q starts or ends with the separator", line)
		}
	}
	for _, item := range items {
		if !strings.Contains(got, item) {
			t.Errorf("%q was split or dropped:\n%s", item, got)
		}
	}
}

func TestWrapKeepsEverythingOnOneLineWhenItFits(t *testing.T) {
	if got := Wrap([]string{"a b", "c d"}, " · ", 40); got != "a b · c d" {
		t.Errorf("Wrap = %q, want one line", got)
	}
}

// Styled items carry escape sequences, which take no columns on the terminal.
func TestWrapMeasuresStyledItemsByTheirColumns(t *testing.T) {
	styled := "\x1b[35mn\x1b[m new"
	if got := Wrap([]string{styled, styled}, " · ", 13); strings.Contains(got, "\n") {
		t.Errorf("two 5-column items and a 3-column separator fit in 13 columns, got:\n%q", got)
	}
}

// An item too wide for any line goes on a line of its own, cut to fit.
func TestWrapCutsAnItemWiderThanTheWidth(t *testing.T) {
	got := Wrap([]string{"q quit", "enter start the marked pull requests", "? help"}, " · ", 12)

	lines := strings.Split(got, "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want the wide item alone on the middle one:\n%s", len(lines), got)
	}
	if Columns(lines[1]) > 12 || !strings.HasSuffix(lines[1], "…") {
		t.Errorf("the wide item is %q, want it cut to 12 columns", lines[1])
	}
}

func TestWindowKeepsTheCursorsLineInView(t *testing.T) {
	lines := []string{"0", "1", "2", "3", "4", "5"}
	tests := []struct {
		name           string
		cursor, height int
		want           []string
	}{
		{"everything fits", 2, 10, lines},
		{"cursor at the top", 0, 3, []string{"0", "1", "2"}},
		{"cursor in the middle", 3, 3, []string{"2", "3", "4"}},
		{"cursor at the bottom", 5, 3, []string{"3", "4", "5"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := window(lines, tc.cursor, tc.height); !slices.Equal(got, tc.want) {
				t.Errorf("window = %v, want %v", got, tc.want)
			}
		})
	}
}

// gh's errors carry its stderr, which can run over several lines. A screen that
// drew them as they are would push its footer down.
func TestOneLineJoinsAMultiLineError(t *testing.T) {
	got := OneLine(errors.New("gh exited 1: gh: HTTP 403\nThis API operation needs the \"read:org\" scope"))

	if want := `gh exited 1: gh: HTTP 403 This API operation needs the "read:org" scope`; got != want {
		t.Errorf("OneLine = %q, want %q", got, want)
	}
}
