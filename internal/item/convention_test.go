package item

import (
	"maps"
	"slices"
	"testing"
)

func TestDefaultChipColours(t *testing.T) {
	levels := map[string]bool{}
	for _, tag := range []string{"priority:high", "priority:medium", "priority:low"} {
		c := ChipColour(tag, nil)
		if c == "" {
			t.Fatalf("%s has no default colour", tag)
		}
		levels[c] = true
	}
	if len(levels) != 3 {
		t.Errorf("the three priority levels share colours: %v", levels)
	}
	others := map[string]bool{}
	for _, tag := range []string{"surface:x", "reporter:x", "source:x"} {
		c := ChipColour(tag, nil)
		if c == "" {
			t.Fatalf("%s has no default colour", tag)
		}
		others[c] = true
	}
	if len(others) != 3 {
		t.Errorf("surface, reporter and source share colours: %v", others)
	}
	for _, conv := range Conventions {
		for _, c := range append([]string{conv.Colour}, slices.Collect(maps.Values(conv.Colours))...) {
			if c != "" && !slices.Contains(ChipColours, c) {
				t.Errorf("%s: colour %q is not in ChipColours", conv.Key, c)
			}
		}
	}
	for _, tag := range []string{"epic:x", "ui", "priority:urgent"} {
		if c := ChipColour(tag, nil); c != "" {
			t.Errorf("%s has colour %q, want none", tag, c)
		}
	}
	over := map[string]string{"priority:high": "ok", "priority": "muted", "surface": "warn", "epic:x": "note"}
	for tag, want := range map[string]string{"priority:high": "ok", "priority:low": "muted", "surface:ui": "warn", "epic:x": "note", "reporter:x": ChipColour("reporter:x", nil)} {
		if got := ChipColour(tag, over); got != want {
			t.Errorf("%s with overrides: %q, want %q", tag, got, want)
		}
	}
}

func TestTagKV(t *testing.T) {
	for tag, want := range map[string][3]string{
		"epic:x":   {"epic", "x", "true"},
		"a:b:c":    {"a", "b:c", "true"},
		"ui":       {"", "", "false"},
		":x":       {"", "", "false"},
		"x:":       {"", "", "false"},
		"priority": {"", "", "false"},
	} {
		k, v, ok := TagKV(tag)
		if k != want[0] || v != want[1] || (ok && want[2] != "true") || (!ok && want[2] != "false") {
			t.Errorf("TagKV(%q) = %q, %q, %v; want %v", tag, k, v, ok, want)
		}
	}
}

func TestFacetKeysAndOutsideValues(t *testing.T) {
	if got := FacetKeys(); !slices.Equal(got, []string{"priority"}) {
		t.Fatalf("FacetKeys %v, want [priority]", got)
	}
	named, outside := OutsideValues("priority:urgent")
	if !outside || !slices.Equal(named, []string{"priority:high", "priority:medium", "priority:low"}) {
		t.Errorf("priority:urgent: %v %v", named, outside)
	}
	for _, tag := range []string{"priority:high", "priority:low", "surface:anything", "epic:x", "ui"} {
		if _, outside := OutsideValues(tag); outside {
			t.Errorf("%s is reported outside its values", tag)
		}
	}
}
