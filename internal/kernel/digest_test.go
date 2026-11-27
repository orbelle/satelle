package kernel

import (
	"os"
	"path/filepath"
	"testing"
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
