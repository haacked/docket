// Package exec describes a command to run and the ways docket runs one. Core
// packages build CommandSpecs. Only internal/tui hands one to the terminal.
package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	osexec "os/exec"
	"slices"
	"strings"
)

// CommandSpec is one command, complete enough to run or to print.
type CommandSpec struct {
	Path  string
	Args  []string
	Dir   string
	Unset []string
}

func (s CommandSpec) String() string {
	var b strings.Builder
	if s.Dir != "" {
		fmt.Fprintf(&b, "[%s] ", s.Dir)
	}
	if len(s.Unset) > 0 {
		b.WriteString("env")
		for _, name := range s.Unset {
			fmt.Fprintf(&b, " -u %s", name)
		}
		b.WriteByte(' ')
	}
	b.WriteString(s.Path)
	for _, arg := range s.Args {
		fmt.Fprintf(&b, " %s", quote(arg))
	}
	return b.String()
}

func quote(arg string) string {
	if arg != "" && !strings.ContainsAny(arg, " \t\"'$") {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

// Env returns the environment for the spec: the given environment minus the
// names in Unset.
func (s CommandSpec) Env(environ []string) []string {
	if len(s.Unset) == 0 {
		return environ
	}
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(s.Unset, name) {
			out = append(out, kv)
		}
	}
	return out
}

// Result is what a finished command produced.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Runner runs a command and captures its output. A command that needs the
// terminal does not go through a Runner: internal/tui hands it to Bubble Tea,
// which is also where a dry run is stopped, since most of what a dry run must not
// do never reaches a Runner.
type Runner interface {
	Run(ctx context.Context, spec CommandSpec) (Result, error)
}

// Real runs commands.
type Real struct{}

func (Real) Run(ctx context.Context, spec CommandSpec) (Result, error) {
	cmd := osexec.CommandContext(ctx, spec.Path, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env(os.Environ())
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	var exitErr *osexec.ExitError
	if errors.As(err, &exitErr) {
		res.ExitCode = exitErr.ExitCode()
		return res, fmt.Errorf("%s exited %d: %s", spec.Path, res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	if err != nil {
		return res, fmt.Errorf("run %s: %w", spec.Path, err)
	}
	return res, nil
}

// Fake records calls and replays canned results. It looks a result up by the
// lexically first key that appears anywhere in the rendered command line. Map
// iteration is randomized, so the keys are sorted before matching: two keys that
// both match one command line would otherwise pick a winner per run.
type Fake struct {
	Calls   []CommandSpec
	Results map[string]Result
	Errs    map[string]error
	Default Result
}

func (f *Fake) Run(_ context.Context, spec CommandSpec) (Result, error) {
	f.Calls = append(f.Calls, spec)
	line := spec.String()
	for _, key := range slices.Sorted(maps.Keys(f.Errs)) {
		if strings.Contains(line, key) {
			return f.Results[key], f.Errs[key]
		}
	}
	for _, key := range slices.Sorted(maps.Keys(f.Results)) {
		if strings.Contains(line, key) {
			return f.Results[key], nil
		}
	}
	return f.Default, nil
}

// Lines returns every recorded call, for assertions.
func (f *Fake) Lines() []string {
	out := make([]string, 0, len(f.Calls))
	for _, c := range f.Calls {
		out = append(out, c.String())
	}
	return out
}
