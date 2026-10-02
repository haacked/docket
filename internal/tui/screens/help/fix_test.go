package help

import (
	"slices"
	"strings"
	"testing"
)

// The footer and the full help both read this table, so the key shows in both.
func TestTheNewReviewKeysIncludeTheFixChoice(t *testing.T) {
	i := slices.IndexFunc(NewReview, func(e Entry) bool { return e.Key == "ctrl+f" })
	if i < 0 {
		t.Fatalf("NewReview has no ctrl+f entry: %+v", NewReview)
	}
	if NewReview[i].Short == "" {
		t.Error("ctrl+f has no footer label")
	}
	if !strings.Contains(NewReview[i].Long, "fix") {
		t.Errorf("ctrl+f reads %q, want it to say it chooses fixing", NewReview[i].Long)
	}
}
