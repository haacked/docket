package engine

import (
	"testing"

	"github.com/haacked/docket/internal/core/review"
)

// At launch the listing reads idle before the session starts, while the status
// file already says active.
func TestWaitingNeedsAnEndedTurnThatTheStatusFileDoesNotCallActive(t *testing.T) {
	tests := []struct {
		name   string
		status BGStatus
		want   bool
	}{
		{name: "ended its turn", status: BGStatus{Idle: true}, want: true},
		{name: "working", status: BGStatus{}, want: false},
		{name: "starting", status: BGStatus{Idle: true, Progress: review.Progress{Active: true}}, want: false},
	}
	for _, tc := range tests {
		if got := tc.status.Waiting(); got != tc.want {
			t.Errorf("%s: Waiting() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestBackgroundNamePrefersTheOneAskedForAndFallsBackToOneWithABackgroundMode(t *testing.T) {
	if got := BackgroundName("claude"); got != "claude" {
		t.Errorf("BackgroundName(claude) = %q", got)
	}
	if got := BackgroundName("codex"); got != "claude" {
		t.Errorf("BackgroundName(codex) = %q, want claude: codex has no background mode", got)
	}
}
