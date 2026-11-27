//go:build !windows

package remotenode

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/kernel"
)

func checkoutLocally(t *testing.T, nodeDir, commit string) {
	t.Helper()
	gittest.Git(t, nodeDir, "init", "-q", "work")
	work := filepath.Join(nodeDir, "work")
	gittest.Git(t, work, "fetch", "-q", filepath.Join("..", "in", "head.bundle"), "HEAD")
	gittest.Git(t, work, "checkout", "-q", "--detach", commit)
}

func stageCheckout(t *testing.T, f taskFixture, node string) string {
	t.Helper()
	f.def.Definition.Drive.Checkout = true
	inputs, err := f.k.TaskInputs(f.task)
	if err != nil {
		t.Fatal(err)
	}
	nodeDir := t.TempDir()
	commit, _, err := stageIn(f.def, filepath.Join(nodeDir, "in"), node, append([]string{f.task}, inputs...))
	if err != nil || commit == "" {
		t.Fatalf("stageIn with checkout = %q, %v", commit, err)
	}
	checkoutLocally(t, nodeDir, commit)
	return nodeDir
}

func commitTree(t *testing.T, tree string, files map[string]string) {
	t.Helper()
	writeFiles(t, filepath.Join(tree, ".satelle"), files)
	gittest.Git(t, tree, "add", ".satelle")
	gittest.Git(t, tree, "commit", "-q", "-m", "more")
}

func filesUnder(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func allSealed(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			info, err := os.Lstat(p)
			if err != nil {
				return err
			}
			if info.Mode().Perm() != substrateFileMode {
				t.Errorf("%s has mode %o, want %o", p, info.Mode().Perm(), substrateFileMode)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func stampsTheSubstrate(t *testing.T, nodeDir string) {
	t.Helper()
	dir, err := config.CanonicalPath(nodeDir)
	if err != nil {
		t.Fatal(err)
	}
	s, err := config.ReadRuntimeStamp(filepath.Join(dir, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := kernel.SubstrateDigest(filepath.Join(dir, nodeSubstrateDir))
	if err != nil {
		t.Fatal(err)
	}
	if s.Substrate != filepath.Join(dir, nodeSubstrateDir) || s.Tree != filepath.Join(dir, "work") || s.Digest != digest {
		t.Fatalf("stamp = %+v, want substrate %s, tree %s, digest %s", s, filepath.Join(dir, nodeSubstrateDir), filepath.Join(dir, "work"), digest)
	}
}

func TestRunLocalUnpacksASealedSubstrateApartFromTheWorkTree(t *testing.T) {
	setup := []string{`ls -ln "$SATELLE_SUBSTRATE_DIR/satelle.toml" "$SATELLE_SUBSTRATE_DIR/skills/chk.md" | cut -c1-10 > ../modes && printf '%s\n' "$SATELLE_SUBSTRATE_DIR" >> ../modes`}
	check := `echo "pwd=$(pwd)"; echo "substrate=$SATELLE_SUBSTRATE_DIR"`
	for _, c := range []struct {
		name     string
		checkout bool
	}{{"no checkout", false}, {"checkout", true}} {
		t.Run(c.name, func(t *testing.T) {
			f := setupFixture(t, check, setup)
			var nodeDir string
			var tracked []string
			if c.checkout {
				nodeDir = stageCheckout(t, f, "nod_5ea1")
				tracked = filesUnder(t, filepath.Join(nodeDir, "work", ".satelle"))
			} else {
				nodeDir = f.stage(t, "nod_5ea2")
			}
			var log strings.Builder
			out, err := RunLocal(context.Background(), LocalOptions{Dir: nodeDir, Version: "1.0.0", Log: &log})
			if err != nil || out.Verdict != kernel.VerdictAccept {
				t.Fatalf("RunLocal = %+v, %v\n%s", out, err, log.String())
			}
			root, work := filepath.Join(nodeDir, "substrate"), filepath.Join(nodeDir, "work")
			modes, err := os.ReadFile(filepath.Join(nodeDir, "modes"))
			if want := "-r--r--r--\n-r--r--r--\n" + root + "\n"; err != nil || string(modes) != want {
				t.Fatalf("setup saw %q (%v), want %q", modes, err, want)
			}
			if !strings.Contains(out.Notes, "pwd="+work+"\n") || !strings.Contains(out.Notes, "substrate="+root) {
				t.Fatalf("the check did not run in the work tree with the substrate apart: %s", out.Notes)
			}
			allSealed(t, root)
			stampsTheSubstrate(t, nodeDir)
			if exists(filepath.Join(work, ".satelle", "remotes", "tprobe", "satelle.toml")) {
				t.Fatal("the overlay was written into the work tree")
			}
			if got := filesUnder(t, filepath.Join(work, ".satelle")); !slices.Equal(got, tracked) {
				t.Fatalf("the work tree's .satelle holds %v, want %v", got, tracked)
			}
			entries, err := os.ReadDir(filepath.Join(work, ".satelle", "runs"))
			if err != nil || len(entries) != 0 {
				t.Fatalf("the work tree's record folders: %v, %v", entries, err)
			}
			if b, err := os.ReadFile(filepath.Join(work, ".satelle", "satelle.toml")); c.checkout && (err != nil || strings.Contains(string(b), `default = "chk"`)) {
				t.Fatalf("the overlay was written into the work tree: %q, %v", b, err)
			}
			if c.checkout {
				if st := gittest.Git(t, work, "status", "--porcelain"); strings.TrimSpace(st) != "" {
					t.Fatalf("the checkout was changed:\n%s", st)
				}
			} else if top, err := os.ReadDir(work); err != nil || len(top) != 1 || top[0].Name() != ".satelle" {
				t.Fatalf("the work tree holds %v, %v", top, err)
			}
		})
	}
	t.Run("a performer's record folder is in the work tree", func(t *testing.T) {
		tree, k, it := localFixture(t, "1.0.0")
		child, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("a child"), Parent: &it.ID}, "ada")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := k.Transition(context.Background(), it.ID, "b", "ada"); err != nil {
			t.Fatal(err)
		}
		def, bundle, err := LoadLocal(filepath.Join(tree, ".satelle"), "probe")
		if err != nil {
			t.Fatal(err)
		}
		nodeDir := t.TempDir()
		o := FireOptions{Kernel: k, Tree: tree, DataDir: filepath.Join(tree, ".satelle"), Remote: "probe", Definition: def, Bundle: bundle, Item: it.ID}
		if _, _, err := stageIn(o, filepath.Join(nodeDir, "in"), "nod_5ea3", []string{it.ID, child.ID}); err != nil {
			t.Fatal(err)
		}
		stub := lineAnswers()
		out, err := RunLocal(context.Background(), LocalOptions{Dir: nodeDir, Version: "1.0.0", Dispatcher: stub})
		if err != nil || out.Verdict != kernel.VerdictAccept {
			t.Fatalf("RunLocal = %+v, %v", out, err)
		}
		records := filepath.Join(nodeDir, "work", ".satelle", "runs")
		if len(stub.folders) == 0 {
			t.Fatal("no dispatch named a record folder")
		}
		for _, d := range stub.folders {
			if !strings.HasPrefix(d, records+string(filepath.Separator)) {
				t.Fatalf("record folder %s is not under %s", d, records)
			}
		}
		allSealed(t, filepath.Join(nodeDir, "substrate"))
	})
}

type rejected struct {
	out   LocalOutcome
	err   error
	log   string
	node  string
	added int
}

func runRejected(t *testing.T, f taskFixture, nodeDir string, secrets map[string]string) rejected {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	given := map[string]string{sealPayloadKey: hex.EncodeToString(priv)}
	for k, v := range secrets {
		given[k] = v
	}
	var log strings.Builder
	out, err := RunLocal(context.Background(), LocalOptions{Dir: nodeDir, Version: "1.0.0", Log: &log, Secrets: secretPayload(t, given)})
	r := rejected{out: out, err: err, log: log.String(), node: nodeDir}
	if err != nil || out.Verdict != kernel.VerdictReject {
		t.Fatalf("RunLocal = %+v, %v\n%s", out, err, r.log)
	}
	b, err := os.ReadFile(filepath.Join(nodeDir, "out", localOutcome))
	if err != nil || !strings.Contains(string(b), `"verdict": "reject"`) {
		t.Fatalf("outcome.json = %s, %v", b, err)
	}
	var written LocalOutcome
	if err := json.Unmarshal(b, &written); err != nil {
		t.Fatal(err)
	}
	runtime := filepath.Join(nodeDir, "runtime")
	if err := checkSeal(hex.EncodeToString(pub), written); err != nil {
		t.Fatalf("the rejection's seal: %v", err)
	}
	if err := checkHeads(runtime, written.Heads); err != nil {
		t.Fatalf("the rejection's heads: %v", err)
	}
	if got := nodeCreated(t, runtime, f.chains(), "node:nod_7a"); len(got) != 0 {
		t.Fatalf("the node's runtime holds created items %v", got)
	}
	res, err := importBack(f.k, runtime, f.chains(), "nod_7a", nil)
	if err != nil || res.Added != 0 {
		t.Fatalf("importBack = %+v, %v", res, err)
	}
	r.added = res.Added
	return r
}

func (r rejected) names(t *testing.T, lines ...string) {
	t.Helper()
	for _, l := range lines {
		if !strings.Contains(r.out.Notes, l) {
			t.Fatalf("notes do not name %q:\n%s", l, r.out.Notes)
		}
	}
}

func (r rejected) noGates(t *testing.T) {
	t.Helper()
	if strings.Contains(r.out.Notes, "\ngate ") {
		t.Fatalf("a gate ran:\n%s", r.out.Notes)
	}
}

func withSetup(t *testing.T, f taskFixture, onCreate, postStart []string) taskFixture {
	t.Helper()
	list := func(cmds []string) string {
		var b strings.Builder
		for i, c := range cmds {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString("'''" + c + "'''")
		}
		return b.String()
	}
	root := filepath.Join(f.tree, ".satelle")
	def := "version = 1\nname = \"tprobe\"\nprovider = \"docker-local\"\nsatelle_version = \"1.0.0\"\n\n[drive]\nkind = \"task\"\ntask = \"" + f.task +
		"\"\n\n[setup]\non_create = [" + list(onCreate) + "]\npost_start = [" + list(postStart) + "]\n\n[lifecycle]\nscope = \"run\"\n"
	writeFiles(t, root, map[string]string{"remotes/tprobe/remote.toml": def})
	d, bundle, err := LoadLocal(root, "tprobe")
	if err != nil {
		t.Fatal(err)
	}
	f.def.Definition, f.def.Bundle = d, bundle
	return f
}

const runScript = "skills/scripts/run.sh"

func substitute(marker string, run bool) string {
	s := `S="$SATELLE_SUBSTRATE_DIR/` + runScript + `" && cp "$S" ../orig && rm -f "$S" && printf 'touch ` + marker + `\n' > "$S"`
	if run {
		s += ` && sh "$S"`
	}
	return s + ` && cp ../orig "$S" && chmod 444 "$S"`
}

func TestRunLocalRejectsAChangedSubstrate(t *testing.T) {
	t.Run("setup changes a file", func(t *testing.T) {
		f := setupFixture(t, "echo checked", []string{`rm -f "$SATELLE_SUBSTRATE_DIR/skills/chk.md" && echo changed > "$SATELLE_SUBSTRATE_DIR/skills/chk.md"`})
		r := runRejected(t, f, f.stage(t, "nod_7a"), nil)
		r.names(t, "changed by setup on_create command 1", "changed skills/chk.md")
		r.noGates(t)
	})
	scripted := func(t *testing.T, onCreate, postStart []string) taskFixture {
		f := newTaskFixture(t, "echo checked")
		commitTree(t, f.tree, map[string]string{runScript: "touch ../ran-setup-two\n"})
		t.Setenv("HOME", t.TempDir())
		return withSetup(t, f, onCreate, postStart)
	}
	t.Run("setup substitutes a script a later setup command runs", func(t *testing.T) {
		later := `sh "$SATELLE_SUBSTRATE_DIR/` + runScript + `"`
		for _, c := range []struct {
			name                string
			onCreate, postStart []string
		}{
			{"across phases", []string{substitute("../substituted-setup", false)}, []string{later}},
			{"within one phase", []string{substitute("../substituted-setup", false), later}, nil},
		} {
			t.Run(c.name, func(t *testing.T) {
				f := scripted(t, c.onCreate, c.postStart)
				nodeDir := f.stage(t, "nod_7a")
				r := runRejected(t, f, nodeDir, nil)
				r.names(t, "changed by setup on_create command 1", "changed "+runScript)
				r.noGates(t)
				for _, m := range []string{"ran-setup-two", "substituted-setup"} {
					if exists(filepath.Join(nodeDir, m)) {
						t.Fatalf("%s ran after the substrate changed", m)
					}
				}
			})
		}
	})
	t.Run("setup with a secret", func(t *testing.T) {
		const value = "s3cr3t-substrate-name"
		f := secretFixture(t, "echo checked", []string{`touch "$SATELLE_SUBSTRATE_DIR/$` + probeSecret + `"`}, "")
		nodeDir := f.stage(t, "nod_7a")
		r := runRejected(t, f, nodeDir, map[string]string{probeSecret: value})
		b, err := os.ReadFile(filepath.Join(nodeDir, "out", localOutcome))
		if err != nil || strings.Contains(string(b), value) || !strings.Contains(string(b), "added [secret]") {
			t.Fatalf("outcome.json = %s, %v", b, err)
		}
		if strings.Contains(r.log, value) || strings.Contains(r.out.Notes, value) {
			t.Fatal("the log or notes hold the secret")
		}
	})
	t.Run("the run adds a file", func(t *testing.T) {
		f := setupFixture(t, `echo x > "$SATELLE_SUBSTRATE_DIR/added.md"`, nil)
		r := runRejected(t, f, f.stage(t, "nod_7a"), nil)
		r.names(t, "changed by the run", "added added.md")
	})
	t.Run("the run removes a file", func(t *testing.T) {
		f := setupFixture(t, `rm -f "$SATELLE_SUBSTRATE_DIR/workflows/chk.toml"`, nil)
		r := runRejected(t, f, f.stage(t, "nod_7a"), nil)
		r.names(t, "changed by the run", "removed workflows/chk.toml")
	})
	t.Run("setup chmods a file 0000", func(t *testing.T) {
		f := setupFixture(t, "echo checked", []string{`chmod 000 "$SATELLE_SUBSTRATE_DIR/skills/chk.md"`})
		r := runRejected(t, f, f.stage(t, "nod_7a"), nil)
		r.names(t, "changed by setup on_create command 1", "changed skills/chk.md")
		r.noGates(t)
	})
	t.Run("as root", func(t *testing.T) {
		if os.Geteuid() != 0 {
			t.Skip("needs root: proves a user the file mode does not stop is still rejected")
		}
		f := setupFixture(t, `echo x > "$SATELLE_SUBSTRATE_DIR/added.md"`, nil)
		r := runRejected(t, f, f.stage(t, "nod_7a"), nil)
		r.names(t, "changed by the run", "added added.md")
	})
	t.Run("a passing check that tampers cannot pass its gate", func(t *testing.T) {
		f := newTaskFixture(t, `echo x > "$SATELLE_SUBSTRATE_DIR/added.md"`)
		commitTree(t, f.tree, map[string]string{
			"skills/aa.md": "---\ntype: check\nafter: always\n---\n```sh\ntouch ../after-always-ran\n```\n",
			"workflows/chk.toml": `[states.backlog]
start = true
[states.done]
terminal = true
[gates.chk]
skill = "chk"
after = ["aa"]
[[transition]]
from = ["backlog"]
to = "done"
groups = [{ gates = ["chk"] }]
`,
		})
		t.Setenv("HOME", t.TempDir())
		nodeDir := f.stage(t, "nod_7a")
		r := runRejected(t, f, nodeDir, nil)
		r.names(t, "gate chk error:", "added added.md", "changed by the run")
		if strings.Contains(r.out.Notes, "gate chk accept") || !strings.Contains(r.out.Notes, "backlog → done rejected") {
			t.Fatalf("the gate passed:\n%s", r.out.Notes)
		}
		if exists(filepath.Join(nodeDir, "after-always-ran")) {
			t.Fatal("the after-always skill ran on a changed substrate")
		}
	})
	t.Run("substitute and restore before a later check", func(t *testing.T) {
		f := newTaskFixture(t, substitute("../substituted", true))
		commitTree(t, f.tree, map[string]string{
			runScript:       "touch ../ran-two\n",
			"skills/two.md": "---\ntype: check\n---\n```sh\nsh \"$SATELLE_SKILL_DIR\"/scripts/run.sh\n```\n",
			"workflows/chk.toml": `[states.backlog]
start = true
[states.done]
terminal = true
[gates.chk]
skill = "chk"
[gates.two]
skill = "two"
[[transition]]
from = ["backlog"]
to = "done"
groups = [{ gates = ["chk", "two"] }]
`,
		})
		t.Setenv("HOME", t.TempDir())
		nodeDir := f.stage(t, "nod_7a")
		r := runRejected(t, f, nodeDir, nil)
		r.names(t, "changed "+runScript, "changed skills/scripts", "changed by the run", "gate two error:")
		if !exists(filepath.Join(nodeDir, "substituted")) {
			t.Fatal("gate one's own replacement did not run; the test proves nothing")
		}
		if exists(filepath.Join(nodeDir, "ran-two")) {
			t.Fatal("gate two ran the substrate's script after it was changed")
		}
	})
	t.Run("dispatch after a change", func(t *testing.T) {
		tree, k, it := localFixture(t, "1.0.0")
		commitTree(t, tree, map[string]string{
			"skills/tamper.md": "---\ntype: check\n---\n```sh\necho x > \"$SATELLE_SUBSTRATE_DIR/added.md\"\n```\n",
			"workflows/line.toml": strings.Replace(lineConfig["workflows/line.toml"], "to = \"b\"\n", "to = \"b\"\ngroups = [{ gates = [\"tamper\"] }]\n", 1) +
				"[gates.tamper]\nskill = \"tamper\"\n",
		})
		nodeDir := stageNode(t, tree, k, it.ID, "nod_7b", 0)
		stub := lineAnswers()
		var log strings.Builder
		out, err := RunLocal(context.Background(), LocalOptions{Dir: nodeDir, Version: "1.0.0", Dispatcher: stub, Log: &log})
		if err != nil || out.Verdict != kernel.VerdictReject {
			t.Fatalf("RunLocal = %+v, %v\n%s", out, err, log.String())
		}
		if len(stub.calls) != 0 {
			t.Fatalf("dispatched %v on a changed substrate", stub.calls)
		}
		if !strings.Contains(out.Notes, "added added.md") {
			t.Fatalf("notes = %s", out.Notes)
		}
		res, err := importBack(k, filepath.Join(nodeDir, "runtime"), []string{it.ID}, "nod_7b", nil)
		if err != nil || res.Added != 0 {
			t.Fatalf("importBack = %+v, %v", res, err)
		}
	})
}

func TestRunLocalCheckoutIsGovernedByTheFiringSubstrate(t *testing.T) {
	f := setupFixture(t, "echo committed-check", nil)
	root := filepath.Join(f.tree, ".satelle")
	committed, err := os.ReadFile(filepath.Join(root, "skills", "chk.md"))
	if err != nil {
		t.Fatal(err)
	}
	wf, err := os.ReadFile(filepath.Join(root, "workflows", "chk.toml"))
	if err != nil {
		t.Fatal(err)
	}
	writeFiles(t, root, map[string]string{
		"skills/chk.md":      "---\ntype: check\n---\n```sh\necho firing-check\necho \"porcelain=$(git status --porcelain -- .satelle)\"\necho \"committed=$(grep -c committed-check .satelle/skills/chk.md)\"\n```\n",
		"workflows/chk.toml": strings.Replace(strings.ReplaceAll(string(wf), "[gates.chk]", "[gates.chk-firing]"), `gates = ["chk"]`, `gates = ["chk-firing"]`, 1),
	})
	nodeDir := stageCheckout(t, f, "nod_c0")
	var log strings.Builder
	out, err := RunLocal(context.Background(), LocalOptions{Dir: nodeDir, Version: "1.0.0", Log: &log})
	if err != nil || out.Verdict != kernel.VerdictAccept {
		t.Fatalf("RunLocal = %+v, %v\n%s", out, err, log.String())
	}
	for _, want := range []string{"gate chk-firing accept:", "firing-check", "porcelain=\n", "committed=1"} {
		if !strings.Contains(out.Notes, want) {
			t.Fatalf("notes do not carry %q:\n%s", want, out.Notes)
		}
	}
	if b, err := os.ReadFile(filepath.Join(nodeDir, "substrate", "satelle.toml")); err != nil || !strings.Contains(string(b), `default = "chk"`) {
		t.Fatalf("the overlay in the substrate = %q, %v", b, err)
	}
	work := filepath.Join(nodeDir, "work")
	if b, err := os.ReadFile(filepath.Join(work, ".satelle", "skills", "chk.md")); err != nil || string(b) != string(committed) {
		t.Fatalf("the work tree's skill = %q, %v", b, err)
	}
	if st := gittest.Git(t, work, "status", "--porcelain"); strings.TrimSpace(st) != "" {
		t.Fatalf("the checkout was changed:\n%s", st)
	}
	if b, err := os.ReadFile(filepath.Join(work, ".satelle", "satelle.toml")); err != nil || string(b) != lineConfig["satelle.toml"] {
		t.Fatalf("the work tree's config = %q, %v; the overlay was written there", b, err)
	}
}

func TestRunLocalRefusesAPlantedRecordsPath(t *testing.T) {
	for _, c := range []struct {
		name  string
		plant func(t *testing.T, tree, outside string)
		named string
	}{
		{"the work tree's .satelle is a symlink", func(t *testing.T, tree, outside string) {
			gittest.Git(t, tree, "rm", "-q", "-r", "--cached", ".satelle")
			if err := os.Rename(filepath.Join(tree, ".satelle"), filepath.Join(tree, "data")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(tree, ".satelle")); err != nil {
				t.Fatal(err)
			}
			gittest.Git(t, tree, "add", ".satelle")
			gittest.Git(t, tree, "commit", "-q", "-m", "plant")
			if err := os.Remove(filepath.Join(tree, ".satelle")); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(filepath.Join(tree, "data"), filepath.Join(tree, ".satelle")); err != nil {
				t.Fatal(err)
			}
		}, ".satelle is a symlink"},
		{"the work tree's runs is a symlink", func(t *testing.T, tree, outside string) {
			if err := os.Symlink(outside, filepath.Join(tree, ".satelle", "runs")); err != nil {
				t.Fatal(err)
			}
			gittest.Git(t, tree, "add", "-f", ".satelle/runs")
			gittest.Git(t, tree, "commit", "-q", "-m", "plant")
			if err := os.Remove(filepath.Join(tree, ".satelle", "runs")); err != nil {
				t.Fatal(err)
			}
		}, filepath.Join(".satelle", "runs") + " already exists"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := setupFixture(t, "echo checked", nil)
			outside := t.TempDir()
			victim := filepath.Join(outside, "unowned")
			if err := os.Mkdir(victim, 0o755); err != nil {
				t.Fatal(err)
			}
			c.plant(t, f.tree, outside)
			nodeDir := stageCheckout(t, f, "nod_7a")
			r := runRejected(t, f, nodeDir, nil)
			r.names(t, c.named)
			r.noGates(t)
			if entries, err := os.ReadDir(outside); err != nil || len(entries) != 1 || !exists(victim) {
				t.Fatalf("the outside directory holds %v, %v", entries, err)
			}
			if _, err := os.Lstat(filepath.Join(nodeDir, "work", ".satelle", "runs", "unowned")); err != nil && !errors.Is(err, fs.ErrNotExist) {
				t.Fatal(err)
			}
		})
	}
}

func TestRunLocalRefusesASwappedWorkTree(t *testing.T) {
	outside := t.TempDir()
	keep := filepath.Join(outside, "keep")
	if err := os.Mkdir(keep, 0o755); err != nil {
		t.Fatal(err)
	}
	f := setupFixture(t, "echo checked", []string{`cd .. && mv work work.real && ln -s '` + outside + `' work`})
	nodeDir := f.stage(t, "nod_7a")
	r := runRejected(t, f, nodeDir, nil)
	r.names(t, "the work tree "+filepath.Join(nodeDir, "work")+" is not a directory")
	r.noGates(t)
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 1 || !exists(keep) {
		t.Fatalf("the run wrote outside the node: %v, %v", entries, err)
	}
}

func TestRunLocalWorkTreeSatelleWritesDoNotGovern(t *testing.T) {
	plant := `mkdir -p .satelle/workflows && printf '[workflows]\ndefault = "other"\n' > .satelle/satelle.toml && ` +
		`sed 's/gates.two/gates.planted/; s/"two"/"planted"/' "$SATELLE_SUBSTRATE_DIR/workflows/chk.toml" > .satelle/workflows/chk.toml`
	for _, c := range []struct {
		name     string
		checkout bool
	}{{"no checkout", false}, {"checkout", true}} {
		t.Run(c.name, func(t *testing.T) {
			f := newTaskFixture(t, plant)
			commitTree(t, f.tree, map[string]string{
				"skills/two.md": "---\ntype: check\n---\n```sh\necho two-ran\n```\n",
				"workflows/chk.toml": `[states.backlog]
start = true
[states.mid]
[states.done]
terminal = true
[gates.chk]
skill = "chk"
[gates.two]
skill = "two"
[[transition]]
from = ["backlog"]
to = "mid"
groups = [{ gates = ["chk"] }]
[[transition]]
from = ["mid"]
to = "done"
groups = [{ gates = ["two"] }]
`,
			})
			t.Setenv("HOME", t.TempDir())
			var nodeDir string
			if c.checkout {
				nodeDir = stageCheckout(t, f, "nod_7a")
			} else {
				nodeDir = f.stage(t, "nod_7a")
			}
			given := t.TempDir()
			bundle, err := os.ReadFile(filepath.Join(nodeDir, "in", localDefinition))
			if err != nil {
				t.Fatal(err)
			}
			_, overlay, err := ReadBundle(bundle)
			if err != nil {
				t.Fatal(err)
			}
			if err := unpackSubstrate(filepath.Join(nodeDir, "in"), given, overlay); err != nil {
				t.Fatal(err)
			}
			want, err := kernel.SubstrateDigest(given)
			if err != nil {
				t.Fatal(err)
			}
			pub, priv, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			var log strings.Builder
			out, err := RunLocal(context.Background(), LocalOptions{Dir: nodeDir, Version: "1.0.0", Log: &log,
				Secrets: secretPayload(t, map[string]string{sealPayloadKey: hex.EncodeToString(priv)})})
			if err != nil || out.Verdict != kernel.VerdictAccept {
				t.Fatalf("RunLocal = %+v, %v\n%s", out, err, log.String())
			}
			if !strings.Contains(out.Notes, "gate two accept:") || !strings.Contains(out.Notes, "two-ran") || strings.Contains(out.Notes, "planted") {
				t.Fatalf("the work tree's .satelle governed the run:\n%s", out.Notes)
			}
			if !exists(filepath.Join(nodeDir, "work", ".satelle", "workflows", "chk.toml")) {
				t.Fatal("the check did not plant its workflow; the test proves nothing")
			}
			root := filepath.Join(nodeDir, nodeSubstrateDir)
			got, err := kernel.SubstrateDigest(root)
			if err != nil || got != want {
				t.Fatalf("the substrate's digest = %s, %v; the node was given %s", got, err, want)
			}
			stampsTheSubstrate(t, nodeDir)
			for _, rel := range filesUnder(t, given) {
				a, aerr := os.ReadFile(filepath.Join(given, rel))
				b, berr := os.ReadFile(filepath.Join(root, rel))
				if aerr != nil || berr != nil || string(a) != string(b) {
					t.Fatalf("%s differs from what the node was given: %v, %v", rel, aerr, berr)
				}
			}
			if !slices.Equal(filesUnder(t, given), filesUnder(t, root)) {
				t.Fatalf("the substrate holds %v, the node was given %v", filesUnder(t, root), filesUnder(t, given))
			}
			runtime := filepath.Join(nodeDir, "runtime")
			if err := checkSeal(hex.EncodeToString(pub), out); err != nil {
				t.Fatal(err)
			}
			if err := checkHeads(runtime, out.Heads); err != nil {
				t.Fatal(err)
			}
			res, err := importBack(f.k, runtime, f.chains(), "nod_7a", nil)
			if err != nil {
				t.Fatal(err)
			}
			ids, err := f.k.Ledger.Items()
			if err != nil {
				t.Fatal(err)
			}
			var ran []string
			for _, id := range ids {
				events, err := f.k.Ledger.Read(id)
				if err != nil {
					t.Fatal(err)
				}
				for _, e := range events {
					if e.Kind == gate.KindRun && e.Actor == "node:nod_7a" {
						var g gate.Result
						if err := json.Unmarshal(e.Payload, &g); err != nil {
							t.Fatal(err)
						}
						ran = append(ran, g.Gate)
					}
				}
			}
			if res.Added == 0 || !slices.Equal(ran, []string{"chk", "two"}) {
				t.Fatalf("brought back %d events with gate runs %v, want chk and two", res.Added, ran)
			}
		})
	}
}
