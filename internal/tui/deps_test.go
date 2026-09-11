package tui

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func deps(t *testing.T, pattern string) []string {
	t.Helper()
	out, err := exec.Command("go", "list", "-deps", pattern).Output()
	if err != nil {
		t.Fatalf("go list -deps %s: %v", pattern, err)
	}
	return strings.Fields(string(out))
}

func refuse(t *testing.T, pattern string, banned ...string) {
	t.Helper()
	for _, dep := range deps(t, pattern) {
		if i := slices.IndexFunc(banned, func(p string) bool { return strings.HasPrefix(dep, p) }); i >= 0 {
			t.Errorf("%s depends on %s", pattern, dep)
		}
	}
}

// TestCoreHasNoUIDependencies keeps the core packages usable without a terminal.
// The rule is easy to break by reaching for a lipgloss style while editing core,
// and nothing else would notice.
func TestCoreHasNoUIDependencies(t *testing.T) {
	refuse(t, "github.com/haacked/docket/internal/core/...",
		"charm.land/",
		"github.com/charmbracelet/",
		"github.com/haacked/docket/internal/tui",
	)
}

// TestScreensHoldNoService is what makes the screens testable with synthetic key
// messages: they turn keys into intents and run no commands. Reaching into the
// service from a screen would work and would quietly end that.
func TestScreensHoldNoService(t *testing.T) {
	refuse(t, "github.com/haacked/docket/internal/tui/screens/...",
		"github.com/haacked/docket/internal/core/session",
		"github.com/haacked/docket/internal/core/clone",
		"github.com/haacked/docket/internal/core/engine",
		"github.com/haacked/docket/internal/core/exec",
		"github.com/haacked/docket/internal/core/gh",
		"github.com/haacked/docket/internal/core/git",
		"github.com/haacked/docket/internal/core/index",
	)
}
