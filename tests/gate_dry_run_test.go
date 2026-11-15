//go:build integration

package tests

import (
	"strings"
	"testing"
)

const writerWorkflow = `name = "writer"
[states.todo]
start = true
[states.done]
terminal = true

[gates.writes]
skill = "attach-note"

[[transition]]
from = ["todo"]
to = "done"
groups = [{ gates = ["writes"] }]
`

func TestGateDryRunRecordsNothingWhenTheCheckWrites(t *testing.T) {
	t.Parallel()
	s := newSandbox(t)
	s.must("init")
	s.write(".satelle/workflows/writer.toml", writerWorkflow)
	s.write(".satelle/skills/attach-note.md", "---\ntype: check\n---\n```sh\n"+
		"printf 'noted\\n' | satelle story attach \"$SATELLE_ITEM\" --name note --file - || echo \"attach refused\"\n"+
		"true\n```\n")
	s.write(".satelle/satelle.toml", "[workflows]\ndefault = \"writer\"\n")
	s.must("validate")
	it := s.create("story", "--title", "writes")
	before := s.must("ledger", "list", it.ID)

	out := s.must("gate", "run", "writer/writes", "--item", it.ID)
	if !strings.Contains(out, "gate writes  accept") || !strings.Contains(out, "attach refused") || !strings.Contains(out, "read-only") {
		t.Fatalf("dry run:\n%s", out)
	}
	if after := s.must("ledger", "list", it.ID); after != before {
		t.Fatalf("the dry run wrote to the ledger:\n--- before\n%s--- after\n%s", before, after)
	}

	s.must("story", "set", it.ID, "--status", "done")
	if list := s.must("story", "attachments", it.ID); !strings.Contains(list, "note") {
		t.Fatalf("the real move's check did not attach:\n%s", list)
	}
}
