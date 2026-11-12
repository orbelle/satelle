package hook

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/bobmcallan/satelle/internal/gittest"
)

// WiringOf reports each harness by its hook file: absent, present with only
// the user's entries, installed, and installed but hidden from git by an
// ignore rule, which it names.
func TestWiringOf(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	gittest.Git(t, dir, "init", "-q")
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// claude: installed. grok: installed and ignored. cursor: the user's own
	// hooks file with no satelle entry. pi: absent.
	for _, h := range []string{Claude, Grok} {
		if _, err := Install(dir, h); err != nil {
			t.Fatal(err)
		}
	}
	write(".cursor/hooks.json", `{"version":1,"hooks":{"stop":[{"command":"echo mine"}]}}`)
	write(".gitignore", "# harness\n.grok/\n")

	got := map[string]Wiring{}
	all := WiringOf(dir)
	if len(all) != len(Harnesses) {
		t.Fatalf("WiringOf returned %d entries, want one per harness", len(all))
	}
	for i, w := range all {
		if w.Harness != Harnesses[i] {
			t.Errorf("entry %d is %s, want %s (Harnesses order)", i, w.Harness, Harnesses[i])
		}
		got[w.Harness] = w
	}
	want := map[string]Wiring{
		Claude: {Harness: Claude, File: ".claude/settings.json", State: WiringInstalled, Present: true},
		Grok:   {Harness: Grok, File: ".grok/hooks/satelle.json", State: WiringIgnored, Rule: ".gitignore:2: .grok/", Present: true},
		Cursor: {Harness: Cursor, File: ".cursor/hooks.json", State: WiringNotInstalled, Present: true},
		Pi:     {Harness: Pi, File: ".pi/extensions/satelle.ts", State: WiringNotInstalled},
	}
	for h, w := range want {
		if got[h] != w {
			t.Errorf("%s: got %+v, want %+v", h, got[h], w)
		}
		if wired := w.State != WiringNotInstalled; got[h].Wired() != wired {
			t.Errorf("%s: Wired() = %v, want %v", h, got[h].Wired(), wired)
		}
	}

	// Outside a repository git cannot answer, and nothing is reported hidden.
	plain := t.TempDir()
	if _, err := Install(plain, Claude); err != nil {
		t.Fatal(err)
	}
	if w := WiringOf(plain)[0]; w.State != WiringInstalled {
		t.Errorf("outside a repository: claude is %q, want %q", w.State, WiringInstalled)
	}
}
