package cli

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
)

// An item's explicit assignees are a list: --assignee is repeatable, set
// replaces the list only with --replace, and --add-assignee/--remove-assignee
// edit it (sto_600b9a85 AC2). No seat is bound, so get --json shows the
// record.
func TestAssigneeFlags(t *testing.T) {
	isolate(t)
	must(t, "init")
	assignees := func(id string) []string {
		t.Helper()
		var it item.Item
		if err := json.Unmarshal([]byte(must(t, "story", "get", id, "--json")), &it); err != nil {
			t.Fatal(err)
		}
		return it.Assignees
	}
	id := createID(t, "story", "create", "--title", "pair", "--assignee", "a", "--assignee", "b")
	if got := assignees(id); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("created assignees %q, want [a b]", got)
	}

	out, err := run(t, "", "story", "set", id, "--assignee", "c")
	if err == nil {
		t.Fatalf("set --assignee without --replace dropped the list:\n%s", out)
	}
	for _, want := range []string{"- a", "- b", "--add-assignee/--remove-assignee"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q lacks %q", err, want)
		}
	}
	if got := assignees(id); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("a refused set changed the assignees to %q", got)
	}

	must(t, "story", "set", id, "--assignee", "c", "--replace")
	if got := assignees(id); !slices.Equal(got, []string{"c"}) {
		t.Fatalf("replaced assignees %q, want [c]", got)
	}

	must(t, "story", "set", id, "--add-assignee", "d", "--remove-assignee", "c")
	if got := assignees(id); !slices.Equal(got, []string{"d"}) {
		t.Fatalf("edited assignees %q, want [d]", got)
	}
}
