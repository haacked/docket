package help

import (
	"slices"
	"strings"
	"testing"
)

// c and u are listed once, in the table the footer and the full screen both read.
func TestDashboardListsAskAndRereviewOnce(t *testing.T) {
	for _, key := range []string{"c", "u"} {
		t.Run(key, func(t *testing.T) {
			var matches []Entry
			for _, e := range Dashboard {
				if e.Key == key {
					matches = append(matches, e)
				}
			}

			if len(matches) != 1 {
				t.Fatalf("Dashboard has %d entries for %q, want 1", len(matches), key)
			}
			if matches[0].Short == "" {
				t.Errorf("%q has no footer label", key)
			}
			if !slices.ContainsFunc(Footer(Dashboard), func(part string) bool { return strings.HasPrefix(part, key+" ") }) {
				t.Errorf("the dashboard footer leaves out %q", key)
			}
		})
	}
}

func TestTheFullHelpScreenListsAskAndRereview(t *testing.T) {
	view := sized().View()

	for _, e := range Dashboard {
		if e.Key != "c" && e.Key != "u" {
			continue
		}
		if !strings.Contains(view, e.Long) {
			t.Errorf("the help screen does not describe %q (%q):\n%s", e.Key, e.Long, view)
		}
	}
}
