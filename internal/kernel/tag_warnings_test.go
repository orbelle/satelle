package kernel

import (
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
)

func TestTagWarnings(t *testing.T) {
	got := TagWarnings([]item.Item{
		{ID: "sto_a", Tags: []string{"priority:urgent", "surface:anything"}},
		{ID: "sto_b", Tags: []string{"priority:high", "epic:x"}},
	})
	if len(got) != 1 {
		t.Fatalf("warnings %v, want one", got)
	}
	w := got[0]
	named := []string{"priority:high", "priority:medium", "priority:low"}
	if w.Item != "sto_a" || w.Tag != "priority:urgent" || !slices.Equal(w.Named, named) {
		t.Errorf("warning %+v", w)
	}
	line := w.String()
	for _, want := range append([]string{"sto_a", "priority:urgent"}, named...) {
		if !strings.Contains(line, want) {
			t.Errorf("%q does not name %q", line, want)
		}
	}
}

func TestTagWarningsIgnoreUntagged(t *testing.T) {
	got := TagWarnings([]item.Item{{ID: "sto_a"}, {ID: "sto_b", Tags: []string{"ui", "reporter:operator", "order:2"}}})
	if got == nil || len(got) != 0 {
		t.Fatalf("warnings %v, want an empty list", got)
	}
}
