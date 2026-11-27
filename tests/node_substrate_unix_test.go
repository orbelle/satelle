//go:build integration && !windows

package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// governed is a sandbox whose probe workflow carries node-probe, with a remote
// that fires it on a node: a command node without a checkout, or a docker-local
// node (the kind that checks out) with one.
type governed struct {
	s       *sandbox
	out     string
	remote  string
	task    string
	runtime func(node string) string
}

func governedNode(t *testing.T, checkout bool, script string, files map[string]string) governed {
	t.Helper()
	if !checkout {
		b, out := governedBox(t, script, "", files)
		return governed{s: b.s, out: out, remote: "command-governed", task: b.task,
			runtime: func(node string) string { return filepath.Join(b.root, node, "node", "runtime") }}
	}
	s, task, _, fake := taskSandbox(t, true)
	out := governProbe(t, s, script, files)
	s.write(".satelle/remotes/docker-governed/remote.toml", `version = 1
name = "docker-governed"
provider = "docker-local"
satelle_version = "1.0.21"

[drive]
kind = "task"
task = "`+task+`"
checkout = true

[setup]
on_create = ["satelle version"]

[lifecycle]
scope = "run"
`)
	s.write(".satelle/remotes/docker-governed/satelle.toml", "[workflows]\ndefault = \"probe\"\n")
	s.git("add", "-f", ".satelle")
	s.git("commit", "-q", "-m", "governed probe")
	return governed{s: s, out: out, remote: "docker-governed", task: task,
		runtime: func(node string) string { return filepath.Join(fake, "satelle-node-"+node, "node", "runtime") }}
}

func (g governed) fire(t *testing.T) string {
	t.Helper()
	r := g.s.run("remote", "fire", g.remote, g.task)
	if r.code != 0 || !strings.Contains(r.out, "verdict: accept") {
		t.Fatalf("fire %s: exit %d\n%s%s", g.remote, r.code, r.out, r.err)
	}
	if m := nodeLine.FindStringSubmatch(r.out); m != nil {
		return m[1]
	}
	return nodeOf(t, r.out)
}

func readTree(t *testing.T, s *sandbox, rel string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(s.dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

var checkoutCases = []struct {
	name     string
	checkout bool
}{{"no checkout", false}, {"checkout", true}}

func TestNodeCLIOpensTheNodeSubstrate(t *testing.T) {
	t.Parallel()
	for _, c := range checkoutCases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			g := governedNode(t, c.checkout, `printf '%s\n' "$SATELLE_ITEM" > "$out/item"
satelle workflow show probe > "$out/show" 2>&1
satelle runtime path > "$out/path" 2>&1
satelle ledger list "$SATELLE_ITEM" > "$out/ledger" 2>&1`, nil)
			node, out := g.fire(t), g.out
			// Without a checkout /node/work/.satelle holds only runs/, so walking up
			// would find no workflow: show naming node-probe proves the node's stamp
			// names /node/substrate.
			if show := readOut(t, out, "show"); !strings.Contains(show, "node-probe") {
				t.Fatalf("satelle workflow show probe inside the node:\n%s", show)
			}
			if got, want := readOut(t, out, "path"), g.runtime(node); got != want {
				t.Fatalf("satelle runtime path inside the node = %q, want %s", got, want)
			}
			item := readOut(t, out, "item")
			if l := readOut(t, out, "ledger"); item == "" || !strings.Contains(l, item) || !strings.Contains(l, "node:"+node) {
				t.Fatalf("satelle ledger list %s inside the node:\n%s", item, l)
			}
		})
	}
}

func TestNodeCheckoutGovernedByTheFiringSubstrate(t *testing.T) {
	t.Parallel()
	g := governedNode(t, true, `satelle workflow show probe > "$out/show" 2>&1
git status --porcelain -- .satelle > "$out/porcelain" 2>&1
echo done > "$out/node-probe"`, nil)
	b, out := g.s, g.out
	// Uncommitted: only the firing tree has this gate, so only the firing
	// substrate can have governed it.
	wf := readTree(t, b, ".satelle/workflows/probe.toml")
	firing := strings.Replace(wf, `"node-probe"]`, `"node-probe", "firing-only"]`, 1)
	if firing == wf {
		t.Fatal("governedBox's gate group moved; update this test")
	}
	b.write(".satelle/workflows/probe.toml", firing+"\n[gates.firing-only]\nskill = \"firing-only-check\"\n")
	b.write(".satelle/skills/firing-only-check.md", "---\nname: firing-only-check\ntype: check\ndescription: probe\n---\n\n```sh\necho firing-only > '"+out+"/firing'\n```\n")
	b.git("add", ".satelle/skills/firing-only-check.md")
	g.fire(t)
	if got := readOut(t, out, "firing"); got != "firing-only" {
		t.Fatalf("the firing tree's gate wrote %q", got)
	}
	if show := readOut(t, out, "show"); !strings.Contains(show, "firing-only") {
		t.Fatalf("satelle workflow show probe inside the node did not read the firing substrate:\n%s", show)
	}
	readOut(t, out, "node-probe")
	if p := readOut(t, out, "porcelain"); p != "" {
		t.Fatalf("the commit's .satelle in the work tree was changed:\n%s", p)
	}
}

func TestNodeWorkTreeSatelleWritesDoNotGovern(t *testing.T) {
	t.Parallel()
	for _, c := range checkoutCases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			g := governedNode(t, c.checkout, `mkdir -p .satelle/workflows
{ cat "$SATELLE_SUBSTRATE_DIR/workflows/probe.toml"; printf '\n[gates.planted]\nskill = "node-probe-check"\n'; } > .satelle/workflows/probe.toml
satelle workflow show probe > "$out/show" 2>&1
printf '%s\n' "$SATELLE_ITEM" > "$out/item"`, nil)
			g.fire(t)
			out := g.out
			if show := readOut(t, out, "show"); !strings.Contains(show, "node-probe") || strings.Contains(show, "planted") {
				t.Fatalf("satelle workflow show probe read the work tree's .satelle:\n%s", show)
			}
			l := g.s.must("ledger", "list", readOut(t, out, "item"))
			if !strings.Contains(l, `"gate":"node-probe"`) || strings.Contains(l, "planted") {
				t.Fatalf("the gate runs brought back were not the governing workflow's:\n%s", l)
			}
		})
	}
}
