package config

import (
	"testing"
)

// ParsePin reads a child's placement pin in the placement vocabulary a binding
// uses, plus in-loop. remote is binding configuration, never a pin.
func TestParsePin(t *testing.T) {
	good := []struct{ in, kind, path string }{
		{"in-loop", PlacementInLoop, ""},
		{"local", PlacementLocal, ""},
		{"team:/teams/x", PlacementTeam, "/teams/x"},
		{"  Team:acme/ops ", PlacementTeam, "acme/ops"},
	}
	for _, tc := range good {
		kind, path, err := ParsePin(tc.in)
		if err != nil || kind != tc.kind || path != tc.path {
			t.Errorf("ParsePin(%q) = %q, %q, %v; want %q, %q", tc.in, kind, path, err, tc.kind, tc.path)
		}
	}
	for _, in := range []string{"remote", "", "team:", "bogus", "dispatched"} {
		if kind, _, err := ParsePin(in); err == nil {
			t.Errorf("ParsePin(%q) = %q, want an error", in, kind)
		}
	}
}

// Sharing the grammar with ParsePin must not change what a binding's placement
// resolves to.
func TestResolvedPlacementUnchangedBySharedGrammar(t *testing.T) {
	cases := []struct{ in, kind, path string }{
		{"", PlacementLocal, ""},
		{"local", PlacementLocal, ""},
		{"remote", PlacementRemote, ""},
		{"team:acme/ops", PlacementTeam, "acme/ops"},
		{"team:", "team:", ""},
		{"Bogus", "bogus", ""},
		{"in-loop", "in-loop", ""},
	}
	for _, tc := range cases {
		kind, path := AgentBinding{Placement: tc.in}.ResolvedPlacement()
		if kind != tc.kind || path != tc.path {
			t.Errorf("ResolvedPlacement(%q) = %q, %q; want %q, %q", tc.in, kind, path, tc.kind, tc.path)
		}
	}
}
