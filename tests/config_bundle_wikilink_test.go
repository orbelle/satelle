//go:build integration

package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateSkipsGeneratedConfigBundles proves the user flow: a config bundle
// satelle generated into a story's attachment dir embeds step.toml, whose TOML
// array-of-tables header `[[gate]]` is not a wikilink, so validate passes. The
// negative half, in the same seeded repo, shows the exemption is narrow: an
// ordinary attachment with a prose reference to a missing document still fails.
func TestValidateSkipsGeneratedConfigBundles(t *testing.T) {
	repo := t.TempDir()
	mustRun(t, testBin, repo, "init")

	taskDir := filepath.Join(repo, ".satelle", "tasks", "tsk_x")
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := sessionconfig.BundleName("aaaaaaaaaaaa", "bbbbbbbbbbbb")
	bundle := `{"format":1,"files":[{"path":"workflows/default/step.toml","content":"[[gate]]\nskill = \"x\"\n"}]}`
	writeFile(t, filepath.Join(taskDir, name+".md"),
		sessionconfig.Render("tsk_x", name, "aaaaaaaaaaaa", "bbbbbbbbbbbb", []byte(bundle)))

	for _, args := range [][]string{{"validate"}, {"principle", "validate"}} {
		out, err := run(t, testBin, repo, args...)
		if err != nil {
			t.Fatalf("%v should pass with a generated config bundle holding [[gate]]: %v\n%s", args, err, out)
		}
		if strings.Contains(out, "dangling wikilink") {
			t.Fatalf("%v reported a dangling wikilink for the config bundle:\n%s", args, out)
		}
	}

	writeFile(t, filepath.Join(taskDir, "note.md"),
		"---\nstory: tsk_x\ntype: note\nname: note\n---\n\nProse reference: [[definitely-not-a-real-doc]].\n")

	out, err := run(t, testBin, repo, "validate")
	if err == nil {
		t.Fatalf("validate should fail on a genuine dangling wikilink in an ordinary attachment:\n%s", out)
	}
	if !strings.Contains(out, "definitely-not-a-real-doc") {
		t.Fatalf("the failure should name the dangling target:\n%s", out)
	}
	if strings.Contains(out, name) {
		t.Fatalf("the failure must not mention the config bundle:\n%s", out)
	}
}
