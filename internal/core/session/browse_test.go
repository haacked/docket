package session

import (
	"slices"
	"testing"
)

const pullURL = "https://github.com/haacked/docket/pull/7/files"

func TestBrowserSpecUsesThePlatformOpener(t *testing.T) {
	tests := []struct {
		browser string
		goos    string
		want    string
	}{
		{goos: "darwin", want: "open"},
		{goos: "linux", want: "xdg-open"},
		{goos: "freebsd", want: "xdg-open"},
		{browser: "   ", goos: "darwin", want: "open"},
	}

	for _, tt := range tests {
		t.Run(tt.goos, func(t *testing.T) {
			spec := BrowserSpec(tt.browser, tt.goos, pullURL)

			if spec.Path != tt.want {
				t.Errorf("path = %q, want %q", spec.Path, tt.want)
			}
			if !slices.Equal(spec.Args, []string{pullURL}) {
				t.Errorf("args = %q, want only the URL", spec.Args)
			}
		})
	}
}

func TestBrowserSpecPrefersTheConfiguredBrowser(t *testing.T) {
	spec := BrowserSpec("firefox --new-tab", "darwin", pullURL)

	want := []string{"-c", `firefox --new-tab "$1"`, "sh", pullURL}
	if spec.Path != "sh" || !slices.Equal(spec.Args, want) {
		t.Errorf("spec = %s, want sh with %q", spec, want)
	}
}
