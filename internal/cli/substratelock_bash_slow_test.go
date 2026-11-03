//go:build clislow

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/ledger"
)

// AC1: every classified Bash mutation of locked substrate is refused with the Edit
// text and a ledger row, in both handlers.
func TestSlowSubstrateLockBashClassifiedTargetsRefused(t *testing.T) {
	cases := []struct {
		name, cmd, rel string
	}{
		{"redirect", "echo x > .satelle/documents/agent-roster.md", ".satelle/documents/agent-roster.md"},
		{"append redirect", "printf x >> .satelle/documents/agent-roster.md", ".satelle/documents/agent-roster.md"},
		{"rm", "rm .satelle/skills/x.md", ".satelle/skills/x.md"},
		{"sed -i", "sed -i s/a/b/ .satelle/x.md", ".satelle/x.md"},
		{"tee", "echo x | tee .satelle/x.md", ".satelle/x.md"},
		{"cp", "cp /tmp/a .satelle/skills/x.md", ".satelle/skills/x.md"},
		{"mv", "mv /tmp/a .satelle/skills/x.md", ".satelle/skills/x.md"},
		{"absolute redirect", "", ""}, // filled in below: needs the repo path
		{"heredoc with a redirect on the opener line", "cat <<'EOF' > .satelle/skills/x.md\nbody\nEOF", ".satelle/skills/x.md"},
		{"after a heredoc", "cat <<'EOF'\nbody\nEOF\nrm .satelle/skills/x.md", ".satelle/skills/x.md"},
		{"after a cd", "cd .satelle && echo x > skills/x.md", ".satelle/skills/x.md"},
	}
	for _, handler := range bashHandlers {
		for _, tc := range cases {
			t.Run(handler+"/"+tc.name, func(t *testing.T) {
				repo, id := lockRepo(t, "feature", "")
				cmd, rel := tc.cmd, tc.rel
				if cmd == "" {
					cmd, rel = "echo x > "+filepath.Join(repo, ".satelle", "documents", "agent-roster.md"), ".satelle/documents/agent-roster.md"
				}
				want := editDenyReason(t, repo, rel)
				before := len(ledgerRows(t, ledger.KindSubstrateLockDeny))
				out, err := runRootIn(t, bashEvent(cmd), "hook", handler)
				if err == nil {
					t.Fatalf("%q must be refused while %s holds a performing seat:\n%s", cmd, id, out)
				}
				if got := denyReasonOf(t, out); got != want {
					t.Errorf("deny text differs from the Edit's:\nbash: %s\nedit: %s", got, want)
				}
				rows := ledgerRows(t, ledger.KindSubstrateLockDeny)
				if len(rows) != before+1 {
					t.Fatalf("want one new %s row, got %d -> %d", ledger.KindSubstrateLockDeny, before, len(rows))
				}
				last := rows[len(rows)-1]
				var p substrateLockPayload
				if jerr := json.Unmarshal(last.Payload, &p); jerr != nil || p.Path != rel || last.StoryID != id {
					t.Errorf("row = story %q payload %s (%v), want story %s path %s", last.StoryID, last.Payload, jerr, id, rel)
				}
			})
		}
	}
}

// AC2: an interpreter command whose text names a locked path, or the bare lock
// root, is refused.
func TestSlowSubstrateLockBashInterpreterRefused(t *testing.T) {
	cases := []struct{ name, cmd string }{
		{"python heredoc", "python3 - <<'EOF'\nopen('.satelle/documents/agent-roster.md','w').write('x')\nEOF"},
		{"python -c", `python3 -c "open('.satelle/documents/agent-roster.md','w').write('x')"`},
		{"python2 style name", `python3.11 -c "open('.satelle/x','w')"`},
		{"node -e", `node -e "require('fs').writeFileSync('.satelle/x','y')"`},
		{"perl -e", `perl -e 'open(F,">.satelle/x")'`},
		{"ruby -e", `ruby -e 'File.write(".satelle/x","y")'`},
		{"bash -c", `bash -c 'printf x > .satelle/x'`},
		{"sh -c", `sh -c 'printf x > .satelle/x'`},
		{"zsh -c", `zsh -c 'printf x > .satelle/x'`},
		{"bash -lc cluster", `bash -lc 'printf x > .satelle/x'`},
		{"env python", `env FOO=1 python3 -c "open('.satelle/x','w')"`},
		{"os.path.join", `python3 -c "import os; open(os.path.join('.satelle','documents','agent-roster.md'),'w')"`},
		{"pathlib", `python3 -c "from pathlib import Path; (Path('.satelle')/'documents'/'x.md').write_text('y')"`},
		{"shell variable", `bash -c 'D=.satelle; echo x > $D/x'`},
		{"top-level shell variable", "D=.satelle; echo x > $D/x"},
		{"assignment then an interpreter reading the env", `D=.satelle; python3 -c "import os; open(os.environ['D']+'/x','w')"`},
		{"assignment then a script file", "D=.satelle python3 tool.py"},
		{"heredoc piped to python", "cat <<'EOF' | python3 -\nopen('.satelle/x','w')\nEOF"},
		{"echo piped to python", `echo "open('.satelle/x','w')" | python3`},
		{"here-string", `python3 - <<< "open('.satelle/x','w')"`},
		{"after a cd out of the tree", `cd /tmp && python3 -c "import os; os.chdir('REPO'); open('.satelle/x','w')"`},
		{"reads only (conservative)", `python3 -c "print(open('.satelle/satelle.toml').read())"`},
	}
	for _, handler := range bashHandlers {
		for _, tc := range cases {
			t.Run(handler+"/"+tc.name, func(t *testing.T) {
				repo, id := lockRepo(t, "feature", "")
				cmd := strings.ReplaceAll(tc.cmd, "REPO", repo)
				before := len(ledgerRows(t, ledger.KindSubstrateLockDeny))
				out, err := runRootIn(t, bashEvent(cmd), "hook", handler)
				if err == nil {
					t.Fatalf("%q must be refused while %s holds a performing seat:\n%s", cmd, id, out)
				}
				reason := denyReasonOf(t, out)
				for _, want := range []string{id, "substrate lock", `category "substrate"`, "finish or park " + id} {
					if !strings.Contains(reason, want) {
						t.Errorf("deny missing %q: %s", want, reason)
					}
				}
				if got := len(ledgerRows(t, ledger.KindSubstrateLockDeny)); got != before+1 {
					t.Errorf("want one new %s row, got %d -> %d", ledger.KindSubstrateLockDeny, before, got)
				}
			})
		}
	}
	t.Run("an absolute path names the file in the deny", func(t *testing.T) {
		repo, _ := lockRepo(t, "feature", "")
		abs := filepath.Join(repo, ".satelle", "documents", "agent-roster.md")
		out, err := runRootIn(t, bashEvent(`python3 -c "open('`+abs+`','w')"`), "hook", "commitgate")
		if err == nil {
			t.Fatalf("an absolute path in an interpreter command must be refused:\n%s", out)
		}
		if reason := denyReasonOf(t, out); reason != editDenyReason(t, repo, ".satelle/documents/agent-roster.md") {
			t.Errorf("deny should name the exact file: %s", reason)
		}
	})
}

// AC1 + AC2 for a linked worktree of the session repository: the foreign-tree
// fence lets it through, and the sibling tree's own lock root applies.
func TestSlowSubstrateLockBashLinkedWorktree(t *testing.T) {
	cases := []struct{ name, cmd string }{
		{"git -C into the sibling's substrate", "git -C WT/.satelle status"},
		{"redirect into the sibling's substrate", "echo x > WT/.satelle/documents/agent-roster.md"},
		{"interpreter with an absolute path", `python3 -c "open('WT/.satelle/documents/agent-roster.md','w')"`},
		{"interpreter after cd", `cd WT && python3 -c "open('.satelle/x','w')"`},
	}
	for _, handler := range bashHandlers {
		for _, tc := range cases {
			t.Run(handler+"/"+tc.name, func(t *testing.T) {
				fenceTempRootsElsewhere(t)
				repo, id := lockRepo(t, "feature", "")
				t.Setenv("SATELLE_PROJECT_DIR", repo)
				gitInitRepo(t, repo)
				wt := linkedWorktree(t, repo)
				if err := os.MkdirAll(filepath.Join(wt, ".satelle", "documents"), 0o755); err != nil {
					t.Fatal(err)
				}
				cmd := strings.ReplaceAll(tc.cmd, "WT", wt)
				out, err := runRootIn(t, bashEvent(cmd), "hook", handler)
				if err == nil {
					t.Fatalf("%q must be refused while %s holds a performing seat:\n%s", cmd, id, out)
				}
				if reason := denyReasonOf(t, out); !strings.Contains(reason, "substrate lock") || strings.Contains(reason, "another repo's tree") {
					t.Errorf("want the substrate lock, not the fence: %s", reason)
				}
				if len(ledgerRows(t, ledger.KindSubstrateLockDeny)) == 0 {
					t.Errorf("no %s row", ledger.KindSubstrateLockDeny)
				}
			})
		}
	}
}

// AC3: nothing the lock does not refuse for an Edit is refused for Bash.
func TestSlowSubstrateLockBashAllowed(t *testing.T) {
	type tc struct {
		name, cmd string
		gate      string // [gate] extra
		category  string
		noSeat    bool
		// editGate: the edit gate, not the lock, may refuse this one: an inline
		// interpreter write needs an executor engagement like an Edit does.
		editGate bool
	}
	cases := []tc{
		{name: "no live seat: footprint redirect", cmd: "echo x > .claude/settings.json", noSeat: true},
		{name: "no live seat: temp redirect", cmd: "echo x > /tmp/x", noSeat: true},
		{name: "substrate lane: redirect", cmd: "echo x > .satelle/skills/x.md", category: "substrate"},
		{name: "substrate lane: rm", cmd: "rm .satelle/skills/x.md", category: "substrate"},
		{name: "substrate lane: interpreter", cmd: `python3 -c "open('.satelle/x','w')"`, category: "substrate"},
		{name: "substrate lane: bash -c", cmd: `bash -c 'echo x > .satelle/x'`, category: "substrate"},
		{name: "outside the lock roots", cmd: "rm build/x"},
		{name: "outside the lock roots: interpreter", cmd: `python3 -c "open('build/x','w')"`},
		{name: "footprint: claude", cmd: "echo x > .claude/settings.json"},
		{name: "footprint: grok", cmd: "echo x > .grok/hooks/satelle.json"},
		{name: "footprint: gitignore", cmd: "echo x >> .gitignore"},
		{name: "temp", cmd: "echo x > /tmp/x"},
		{name: "exempt glob: story dump", cmd: "echo x > .satelle/documents/sty_abc12345_ac.md"},
		{name: "lock list empty: redirect", cmd: "echo x > .satelle/skills/x.md", gate: "lock_substrate_paths = []\n"},
		{name: "lock list empty: interpreter", cmd: `python3 -c "open('.satelle/x','w')"`, gate: "lock_substrate_paths = []\n"},
		{name: "read: cat", cmd: "cat .satelle/skills/x.md"},
		{name: "read: grep -r", cmd: "grep -r foo .satelle/"},
		{name: "read: ls", cmd: "ls .satelle"},
		{name: "read: satelle doc get", cmd: "satelle doc get principles satelle-agent-goals"},
		{name: "read: assigned variable, ls", cmd: "D=.satelle; ls $D"},
		{name: "read: assigned variable, cat", cmd: `F=.satelle/x; cat "$F"`},
		{name: "read: sed without -i", cmd: "sed -n 1p .satelle/skills/x.md"},
		{name: "another name, not the lock root", cmd: `python3 -c "print('.satellerc', 'my.satelle', '.satelle-x')"`},
		{name: "operator prefix elsewhere is not the default", cmd: "echo x > .satelle/x", gate: "lock_substrate_paths = [\"policy/\"]\n"},
		{name: "plain commit", cmd: "git commit -m x"},
		// AC4: the two documented residuals — invisible in the command text, so
		// allowed. See the `hook gate` Long help and bashSubstrateLockGate.
		{name: "residual (a): a script file", cmd: "python3 tool.py"},
		{name: "residual (b): an assembled path", cmd: `python3 -c "open('.sat'+'elle/x','w')"`},
	}
	for _, handler := range bashHandlers {
		for _, c := range cases {
			t.Run(handler+"/"+c.name, func(t *testing.T) {
				var repo string
				if c.noSeat {
					repo = tempRepo(t)
					t.Chdir(repo)
					if err := os.WriteFile(filepath.Join(repo, ".satelle", "satelle.toml"), []byte(lockExemptToml), 0o644); err != nil {
						t.Fatal(err)
					}
				} else {
					cat := c.category
					if cat == "" {
						cat = "feature"
					}
					repo, _ = lockRepo(t, cat, c.gate)
				}
				_ = repo
				// `git commit` also needs the engaged-story gate; the seat is
				// engaged so it passes, and what is asserted is only that no lock
				// refusal appears.
				out, err := runRootIn(t, bashEvent(c.cmd), "hook", handler)
				if err != nil && !c.editGate {
					t.Fatalf("%q must stay allowed: %v\n%s", c.cmd, err, out)
				}
				if err != nil && strings.Contains(out, "substrate lock") {
					t.Errorf("%q: a refusal here is the edit gate's, not the substrate lock's:\n%s", c.cmd, out)
				}
				if rows := ledgerRows(t, ledger.KindSubstrateLockDeny); len(rows) != 0 {
					t.Errorf("an allowed command records no refusal, got %d", len(rows))
				}
			})
		}
	}
}

// sty_ca667036 AC1: a satelle verb whose --body names a substrate file writes
// nothing, bare or wrapped in a shell -c program, so the lock refuses none of them.
func TestSlowSubstrateLockBashBodyNamingSubstrateAllowed(t *testing.T) {
	var cmds []string
	for _, bare := range []string{
		"satelle story create --title t --body 'note .satelle/satelle.toml here'",
		"satelle story set sty_x --body 'note .satelle/satelle.toml here'",
		"satelle story message sty_x --from a --to b --body 'note .satelle/satelle.toml here'",
	} {
		cmds = append(cmds, bare, `bash -c "`+bare+`"`, `bash -lc "`+bare+`"`, `sh -c "`+bare+`"`)
	}
	cmds = append(cmds, "satelle story create --title t --body \"$(cat <<'B'\nnote .satelle/satelle.toml here\nB\n)\"")
	for _, handler := range bashHandlers {
		for _, cmd := range cmds {
			t.Run(handler+"/"+cmd, func(t *testing.T) {
				lockRepo(t, "feature", "")
				out, _ := runRootIn(t, bashEvent(cmd), "hook", handler)
				if strings.Contains(out, "substrate lock") {
					t.Errorf("%q: refused by the substrate lock:\n%s", cmd, out)
				}
				if rows := ledgerRows(t, ledger.KindSubstrateLockDeny); len(rows) != 0 {
					t.Errorf("%q: want no %s row, got %d", cmd, ledger.KindSubstrateLockDeny, len(rows))
				}
			})
		}
	}
}

// sty_ca667036 AC2 + AC5: a shell -c program that writes locked substrate is
// refused as its bare form is, and one the classifier cannot read keeps the text
// scan — including the stated limits a wrapped --body falls into.
func TestSlowSubstrateLockBashShellProgramRefused(t *testing.T) {
	cases := []struct{ name, cmd string }{
		{"redirect", `bash -lc 'echo x > .satelle/satelle.toml'`},
		{"sed -i", `sh -c "sed -i s/a/b/ .satelle/workflows/x.toml"`},
		{"tee with a here-string", `bash -c 'tee .satelle/x <<<y'`},
		{"nested interpreter", `bash -c "python3 -c 'open(\".satelle/x\",\"w\")'"`},
		{"program assignment", `bash -c 'D=.satelle; echo x > $D/x'`},
		{"positional redirect", `bash -c 'echo x > "$1"' _ .satelle/satelle.toml`},
		{"positional rm", `bash -c 'rm -f "$1"' _ .satelle/satelle.toml`},
		{"assignment, dynamic command word", `CMD='rm .satelle/x' bash -c '$CMD'`},
		{"assignment, quoted dynamic command word", `CMD='rm .satelle/x' bash -c '"$CMD"'`},
		{"stdin program", `echo "open('.satelle/x','w')" | bash -c python3`},
		{"glued quoting", `bash -c 'echo x '\''y'\'' > .satelle/x'`},
		{"double-quoted escapes", `bash -c "echo \"a b\" > .satelle/x"`},
		{"unexpanded program", `bash -c "$P .satelle/x"`},
		{"eval", `bash -c 'eval "rm .satelle/x"'`},
		{"source", `bash -c 'source .satelle/x'`},
		{"dot", `bash -c '. .satelle/x'`},
		{"limit: wrapped heredoc body", "bash -c \"satelle story create --title t --body \\\"$(cat <<'B'\nnote .satelle/satelle.toml\nB\n)\\\"\""},
		{"limit: wrapped glued body", `bash -lc 'satelle story create --title t --body '\''note .satelle/satelle.toml'\'''`},
	}
	for _, handler := range bashHandlers {
		for _, tc := range cases {
			t.Run(handler+"/"+tc.name, func(t *testing.T) {
				_, id := lockRepo(t, "feature", "")
				out, err := runRootIn(t, bashEvent(tc.cmd), "hook", handler)
				if err == nil {
					t.Fatalf("%q must be refused while %s holds a performing seat:\n%s", tc.cmd, id, out)
				}
				if reason := denyReasonOf(t, out); !strings.Contains(reason, "substrate lock") {
					t.Errorf("%q: want the substrate lock, got: %s", tc.cmd, reason)
				}
				if got := len(ledgerRows(t, ledger.KindSubstrateLockDeny)); got != 1 {
					t.Errorf("%q: want one %s row, got %d", tc.cmd, ledger.KindSubstrateLockDeny, got)
				}
			})
		}
	}
}
