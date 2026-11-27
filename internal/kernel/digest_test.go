package kernel

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeDigestFile(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustDigest(t *testing.T, root string) string {
	t.Helper()
	d, err := SubstrateDigest(root)
	if err != nil {
		t.Fatalf("SubstrateDigest: %v", err)
	}
	return d
}

func TestSubstrateDigestCoversLocalOverrides(t *testing.T) {
	root := t.TempDir()
	writeDigestFile(t, root, "satelle.toml", "")
	base := mustDigest(t, root)
	for _, rel := range []string{"agents.local.toml", "workflows/x.local.toml", "skills/y.local.md", "queues/z.local.toml", "satelle.local.toml"} {
		writeDigestFile(t, root, rel, "one")
		d := mustDigest(t, root)
		if d == base {
			t.Fatalf("adding %s left the digest unchanged", rel)
		}
		writeDigestFile(t, root, rel, "two")
		if mustDigest(t, root) == d {
			t.Fatalf("changing %s left the digest unchanged", rel)
		}
		base = mustDigest(t, root)
	}
}

func TestSubstrateDigestSkipsOnlyTheRecordFolders(t *testing.T) {
	root := t.TempDir()
	writeDigestFile(t, root, "satelle.toml", "")
	base := mustDigest(t, root)
	writeDigestFile(t, root, "runs/sto_1-1/attachments/plan", "x")
	if mustDigest(t, root) != base {
		t.Fatal("a record folder under runs/ changed the digest")
	}
	writeDigestFile(t, root, "skills/runs/x.md", "x")
	if mustDigest(t, root) == base {
		t.Fatal("a nested runs/ directory was skipped")
	}
}

func TestSubstrateDigestFollowsLinks(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	writeDigestFile(t, out, "target.toml", "a")
	if err := os.Symlink(filepath.Join(out, "target.toml"), filepath.Join(root, "agents.local.toml")); err != nil {
		t.Fatal(err)
	}
	d := mustDigest(t, root)
	writeDigestFile(t, out, "target.toml", "b")
	if mustDigest(t, root) == d {
		t.Fatal("a change to a linked file's target left the digest unchanged")
	}
	if err := os.Symlink(root, filepath.Join(root, "loop")); err != nil {
		t.Fatal(err)
	}
	if _, err := SubstrateDigest(root); err == nil {
		t.Fatal("a directory link back into the substrate was not refused")
	}
}

func mustDigestOf(t *testing.T, root string) string {
	t.Helper()
	d, err := SubstrateDigestOf(root, "check", "skill")
	if err != nil {
		t.Fatalf("SubstrateDigestOf: %v", err)
	}
	if len(d) != 64 {
		t.Fatalf("digest %q is not a SHA-256 hex", d)
	}
	return d
}

func TestSubstrateDigestOfIgnoresOrderTimesModesAndUnnamedEntries(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	writeDigestFile(t, a, "check", "echo ok")
	writeDigestFile(t, a, "skill/x.md", "x")
	writeDigestFile(t, a, "skill/sub/y.sh", "y")
	writeDigestFile(t, a, "payload.json", `{"item":"one"}`)
	writeDigestFile(t, b, "skill/sub/y.sh", "y")
	writeDigestFile(t, b, "skill/x.md", "x")
	writeDigestFile(t, b, "check", "echo ok")
	writeDigestFile(t, b, "payload.json", `{"item":"two"}`)
	writeDigestFile(t, b, "run-check", "env")
	old := time.Unix(1000, 0)
	if err := os.Chtimes(filepath.Join(b, "check"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(b, "skill", "x.md"), 0o600); err != nil {
		t.Fatal(err)
	}
	if mustDigestOf(t, a) != mustDigestOf(t, b) {
		t.Fatal("identical staged bytes gave different digests")
	}
}

func TestSubstrateDigestOfChangesWithEveryNamedFile(t *testing.T) {
	root := t.TempDir()
	writeDigestFile(t, root, "check", "echo ok")
	writeDigestFile(t, root, "skill/x.md", "x")
	base := mustDigestOf(t, root)
	writeDigestFile(t, root, "payload.json", "changed")
	if mustDigestOf(t, root) != base {
		t.Fatal("an unnamed entry changed the digest")
	}
	steps := []struct {
		name string
		do   func()
	}{
		{"a changed check byte", func() { writeDigestFile(t, root, "check", "echo oK") }},
		{"a changed skill file", func() { writeDigestFile(t, root, "skill/x.md", "y") }},
		{"an added skill file", func() { writeDigestFile(t, root, "skill/new.md", "n") }},
		{"a removed skill file", func() {
			if err := os.Remove(filepath.Join(root, "skill", "x.md")); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, s := range steps {
		s.do()
		d := mustDigestOf(t, root)
		if d == base {
			t.Fatalf("%s left the digest unchanged", s.name)
		}
		base = d
	}
}

func TestSubstrateDigestOfKeepsANamedRunsEntry(t *testing.T) {
	root := t.TempDir()
	writeDigestFile(t, root, "runs/x", "a")
	d, err := SubstrateDigestOf(root, "runs")
	if err != nil {
		t.Fatal(err)
	}
	writeDigestFile(t, root, "runs/x", "b")
	if d2, _ := SubstrateDigestOf(root, "runs"); d2 == d {
		t.Fatal("a named runs entry was skipped")
	}
}
