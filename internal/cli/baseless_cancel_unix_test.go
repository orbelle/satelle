//go:build !windows

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/item"
)

// An item that never recorded a base (one created through the satelle MCP and
// placed by the repo's client) moves to a judged terminal state through the
// real command, and its output names it and holds no git error
// (sto_f98743d0). Before the fix this printed "fatal: bad revision ”".
func TestStorySetMovesABaselessItemToAJudgedTerminalState(t *testing.T) {
	dir := isolate(t)
	fake := t.TempDir()
	judge := "#!/bin/sh\nv=accept\n[ -f \"$FAKE_DIR/verdict\" ] && v=$(cat \"$FAKE_DIR/verdict\")\n" +
		"printf '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"{\\\\\"verdict\\\\\":\\\\\"%s\\\\\",\\\\\"notes\\\\\":\\\\\"judged\\\\\"}\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}\\n' \"$v\"\n"
	if err := os.WriteFile(filepath.Join(fake, "claude"), []byte(judge), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fake+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DIR", fake)
	files := map[string]string{
		".satelle/satelle.toml":     "[workflows]\ndefault = \"line\"\n",
		".satelle/agents.toml":      "[agents.r1]\ncommand = \"claude\"\nmodel = \"m1\"\ntools = [\"Read\"]\n",
		".satelle/skills/review.md": "---\nname: review\n---\nReview it.\n",
		".satelle/workflows/line.toml": "[states.w]\nstart = true\n[states.x]\nterminal = true\n" +
			"[gates.final]\nskill = \"review\"\npanel = [\"r1\"]\ncombine = \"all\"\n" +
			"[[transition]]\nfrom = [\"w\"]\nto = \"x\"\ngroups = [{ gates = [\"final\"] }]\n",
		".gitignore": ".satelle/runtime/\n",
	}
	for rel, body := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gittest.Git(t, dir, "add", "-A")
	gittest.Git(t, dir, "commit", "-q", "-m", "config")

	k, _, err := openKernel()
	if err != nil {
		t.Fatal(err)
	}
	empty := ""
	it, err := k.Create(item.Fields{Kind: &[]string{"story"}[0], Title: &[]string{"filed by mistake"}[0], Base: &empty}, "ada")
	k.Close()
	if err != nil {
		t.Fatal(err)
	}
	if it.Base != "" {
		t.Fatalf("item base = %q, want none", it.Base)
	}

	noGitError := func(s string) {
		t.Helper()
		if strings.Contains(s, "fatal:") || strings.Contains(s, "bad revision") {
			t.Fatalf("output holds a git error:\n%s", s)
		}
	}
	if err := os.WriteFile(filepath.Join(fake, "verdict"), []byte("reject"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "", "story", "set", it.ID, "--status", "x")
	if err == nil || !strings.HasPrefix(err.Error(), it.ID+" did not move") {
		t.Fatalf("refused move: err %v, want it to name %s\n%s", err, it.ID, out)
	}
	noGitError(out + err.Error())

	if err := os.Remove(filepath.Join(fake, "verdict")); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, "", "story", "set", it.ID, "--status", "x")
	if err != nil {
		t.Fatalf("accepted move: %v\n%s", err, out)
	}
	noGitError(out)
	if !strings.Contains(out, it.ID+": w -> x") {
		t.Fatalf("accepted move output %q, want %s: w -> x", out, it.ID)
	}
}
