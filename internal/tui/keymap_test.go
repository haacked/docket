package tui

import (
	"strings"
	"testing"

	"github.com/haacked/docket/internal/tui/msg"
)

// TestHelpForNamesTheKeysOfTheHelpScreenItself covers msg.Help's own footer.
// TestTheFooterNamesTheKeysOfEachScreen (submit_notes_test.go) covers the
// other screens, the ones it leaves out.
func TestHelpForNamesTheKeysOfTheHelpScreenItself(t *testing.T) {
	got := helpFor(msg.Help, false)

	for _, part := range []string{"esc/? back", "ctrl+c quit"} {
		if !strings.Contains(got, part) {
			t.Errorf("the footer for the help screen is %q, want %q in it", got, part)
		}
	}
}
