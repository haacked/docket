package main

import "testing"

func TestParseFlags(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		want  flags
		fails bool
	}{
		{name: "no arguments", args: nil},
		{
			name: "url argument",
			args: []string{"https://github.com/o/r/pull/1"},
			want: flags{input: "https://github.com/o/r/pull/1"},
		},
		{
			name: "flags before the argument",
			args: []string{"--engine", "claude", "--dry-run", "o/r#2"},
			want: flags{engine: "claude", dryRun: true, input: "o/r#2"},
		},
		{
			name: "flags after the argument",
			args: []string{"o/r#2", "--home", "/tmp/docket"},
			want: flags{home: "/tmp/docket", input: "o/r#2"},
		},
		{name: "unknown flag", args: []string{"--nope"}, fails: true},
		{name: "flag with no value", args: []string{"--engine"}, fails: true},
		{name: "two pull requests", args: []string{"1", "2"}, fails: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseFlags(tc.args)
			if tc.fails {
				if err == nil {
					t.Fatalf("parseFlags(%v) succeeded, want an error", tc.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseFlags(%v): %v", tc.args, err)
			}
			if got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
