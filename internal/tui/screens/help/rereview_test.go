package help

import (
	"strings"
	"testing"
)

func TestViewListsTheReReviewToggleUnderReviewRequests(t *testing.T) {
	lines := section(t, sized().View(), "Review requests")

	if !hasKey(lines, "p") {
		t.Errorf("the Review requests section does not list p:\n%s", strings.Join(lines, "\n"))
	}
}

// p shows rows, as d and c do, so its label in the footer follows its state.
// The table therefore carries no short label for it.
func TestTheReReviewToggleHasNoFixedFooterLabel(t *testing.T) {
	for _, e := range Requests {
		if e.Key == "p" && e.Short != "" {
			t.Errorf("p has the fixed footer label %q, want the footer to build it from the toggle", e.Short)
		}
	}
}
