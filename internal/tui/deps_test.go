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

// TestOnlyTheRunnerAndTheTUICallOsExec is the other half of the layering rule.
// `go list -deps` cannot express it, because every core package reaches os/exec
// transitively through internal/core/exec, so this reads direct imports instead.
// A core package shelling out on its own is what would move the suite off
// exec.Fake and onto a real repo and a real network.
func TestOnlyTheRunnerAndTheTUICallOsExec(t *testing.T) {
	allowed := []string{
		"github.com/haacked/docket/internal/core/exec",
		"github.com/haacked/docket/internal/tui",
		"github.com/haacked/docket/cmd/docket",
	}

	out, err := exec.Command("go", "list", "-f", "{{.ImportPath}} {{join .Imports \" \"}}", "github.com/haacked/docket/...").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}

	for line := range strings.Lines(string(out)) {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		pkg, imports := fields[0], fields[1:]
		if slices.Contains(allowed, pkg) || !slices.Contains(imports, "os/exec") {
			continue
		}
		t.Errorf("%s imports os/exec directly; build an exec.CommandSpec and hand it to a Runner instead", pkg)
	}
}
