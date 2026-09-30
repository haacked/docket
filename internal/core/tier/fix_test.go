package tier_test

import (
	"testing"

	"github.com/haacked/docket/internal/core/tier"
)

// The dashboard prints the tier on every row, and the index stores the number.
func TestTier3IsItsOwnTier(t *testing.T) {
	if tier.Tier3 == tier.Tier1 || tier.Tier3 == tier.Tier2 {
		t.Fatalf("Tier3 = %d, want a value of its own", tier.Tier3)
	}
	if int(tier.Tier3) != 3 {
		t.Errorf("Tier3 = %d, want 3", tier.Tier3)
	}
	if got := tier.Tier3.String(); got != "tier3" {
		t.Errorf("Tier3.String() = %q, want tier3", got)
	}
}
