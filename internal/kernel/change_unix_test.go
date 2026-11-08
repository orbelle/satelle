//go:build !windows

package kernel

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/item"
)

// coveredKernel is w → r judged by code-ac, r → w and {w, r} → d gateless
// (d terminal), and r → x (x terminal) judged by its own final gate, over a
// repo on main with the config committed and the item's branch feat/x
// checked out.
func coveredKernel(t *testing.T) (dir string, k *Kernel) {
	t.Helper()
	dir = t.TempDir()
	gittest.Git(t, dir, "init", "-q", "-b", "main")
	write(t, filepath.Join(dir, "README"), "r\n")
	root := filepath.Join(dir, ".satelle")
	write(t, filepath.Join(root, "satelle.toml"), "[workflows]\ndefault = \"line\"\n")
	write(t, filepath.Join(root, "agents.toml"), "[agents.r1]\ncommand = \"claude\"\ntools = [\"Read\"]\n")
	write(t, filepath.Join(root, "skills", "review.md"), "Review.\n")
	write(t, filepath.Join(root, "workflows", "line.toml"), `
[states.w]
start = true
[states.r]
[states.d]
terminal = true
[states.x]
terminal = true
[gates.code-ac]
skill = "review"
agent = "r1"
[gates.final]
skill = "review"
agent = "r1"
[[transition]]
from = ["w"]
to = "r"
groups = [{ gates = ["code-ac"] }]
[[transition]]
from = ["r"]
to = "w"
[[transition]]
from = ["w", "r"]
to = "d"
[[transition]]
from = ["r"]
to = "x"
groups = [{ gates = ["final"] }]
`)
	write(t, filepath.Join(dir, ".gitignore"), ".satelle/runtime/\n")
	gittest.Git(t, dir, "add", "-A")
	gittest.Git(t, dir, "commit", "-q", "-m", "config")
	gittest.Git(t, dir, "update-ref", "refs/remotes/origin/main", gittest.Git(t, dir, "rev-parse", "HEAD"))
	gittest.Git(t, dir, "checkout", "-q", "-b", "feat/x")
	var err error
	k, err = Open(Options{Root: root, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	k.Dispatcher = &fakeDispatcher{answers: map[string]string{"r1": accept}}
	return dir, k
}

// seated creates an item in w seated in dir.
func seated(t *testing.T, k *Kernel, dir string) item.Item {
	t.Helper()
	it, err := k.Create(item.Fields{Kind: ptr("story"), Title: ptr("t")}, "ada")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Seat(it.ID, "s1", "local:"+dir, "", "ada"); err != nil {
		t.Fatal(err)
	}
	return it
}

func moveTo(t *testing.T, k *Kernel, id, to string) TransitionResult {
	t.Helper()
	res, err := k.Transition(context.Background(), id, to, "ada")
	if err != nil {
		t.Fatalf("%s -> %s: %v", id, to, err)
	}
	if !res.Moved {
		t.Fatalf("%s -> %s did not move: %+v", id, to, res.Gates)
	}
	return res
}

// judgedItem is an item whose code-ac accepted story.go on w → r.
func judgedItem(t *testing.T) (string, *Kernel, item.Item) {
	dir, k := coveredKernel(t)
	it := seated(t, k, dir)
	commitFile(t, dir, "story.go", "package story\n")
	res := moveTo(t, k, it.ID, "r")
	if c := res.Gates[0].Change; res.Gates[0].Result != gate.Accept || c == nil || c.Branch != "feat/x" || c.Hash == "" {
		t.Fatalf("code-ac result = %+v", res.Gates[0])
	}
	return dir, k, it
}

func frozenOf(t *testing.T, k *Kernel, id, to string) *item.Span {
	t.Helper()
	_, events, err := k.load(id)
	if err != nil {
		t.Fatal(err)
	}
	for i := len(events) - 1; i >= 0; i-- {
		var m item.Move
		if events[i].Kind == item.KindTransitioned && events[i].Decode(&m) == nil && m.To == to {
			return m.Change
		}
	}
	t.Fatalf("no move to %s", to)
	return nil
}

// (a) The kernel decides nothing about a judged change: a commit after the
// judge does not refuse a gateless terminal move (a workflow refuses it with
// a check). The move records the change it was made with.
func TestTheKernelDoesNotRefuseAnUnjudgedTerminalMove(t *testing.T) {
	dir, k, it := judgedItem(t)
	commitFile(t, dir, "later.go", "package later\n")
	res, err := k.Transition(context.Background(), it.ID, "d", "ada")
	if err != nil || !res.Moved {
		t.Fatalf("move to d after a later commit: moved %v, err %v", res.Moved, err)
	}
	head := gittest.Git(t, dir, "rev-parse", "HEAD")
	if fs := frozenOf(t, k, it.ID, "d"); fs == nil || fs.Head != head {
		t.Fatalf("frozen change = %+v, want head %s", fs, head)
	}
}

// No declaration of the kernel decides whether a terminal move's change is
// the judged one: the names that did are gone.
func TestNoKernelDeclarationDecidesAJudgedChange(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	banned := map[string]bool{"UncoveredError": true, "latestJudged": true, "uncovered": true}
	files := 0
	for _, p := range pkgs {
		for name, f := range p.Files {
			files++
			for _, d := range f.Decls {
				switch d := d.(type) {
				case *ast.FuncDecl:
					if banned[d.Name.Name] {
						t.Errorf("%s declares func %s", name, d.Name.Name)
					}
				case *ast.GenDecl:
					for _, s := range d.Specs {
						if ts, ok := s.(*ast.TypeSpec); ok && banned[ts.Name.Name] {
							t.Errorf("%s declares type %s", name, ts.Name.Name)
						}
					}
				}
			}
		}
	}
	if files == 0 {
		t.Fatal("parsed no kernel source file")
	}
}

// JudgedChanges reports each recorded verdict's change and the change now:
// equal until a later commit, different after it, and none once the item is
// terminal (its change is frozen).
func TestJudgedChangesReportNow(t *testing.T) {
	dir, k, it := judgedItem(t)
	one := func() JudgedChange {
		t.Helper()
		js, err := k.JudgedChanges(it.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(js) != 1 || js[0].Gate != "code-ac" || js[0].Transition != "w->r" || js[0].Result != gate.Accept || js[0].Branch != "feat/x" || js[0].Judged == "" {
			t.Fatalf("judged = %+v, want one code-ac accept on w->r", js)
		}
		return js[0]
	}
	if j := one(); j.Now != j.Judged || j.NowHead != gittest.Git(t, dir, "rev-parse", "HEAD") {
		t.Fatalf("before a later commit: %+v, want now == judged at HEAD", j)
	}
	commitFile(t, dir, "later.go", "package later\n")
	if j := one(); j.Now == j.Judged {
		t.Fatalf("after a later commit: %+v, want now != judged", j)
	}
	moveTo(t, k, it.ID, "d")
	if js, err := k.JudgedChanges(it.ID); err != nil || js == nil || len(js) != 0 {
		t.Fatalf("terminal item: judged = %#v, err %v, want an empty slice", js, err)
	}
}

// (b) A terminal transition carrying its own judge passes when that judge
// accepts the current change, a later commit included.
func TestATerminalJudgeCoversTheChangeItSaw(t *testing.T) {
	dir, k, it := judgedItem(t)
	commitFile(t, dir, "later.go", "package later\n")
	res := moveTo(t, k, it.ID, "x")
	if res.Gates[0].Gate != "final" || res.Gates[0].Result != gate.Accept {
		t.Fatalf("final = %+v", res.Gates[0])
	}
	if fs := frozenOf(t, k, it.ID, "x"); fs == nil {
		t.Fatal("the move to x froze no change")
	}
}

// (c) Accepts recorded before changes were (no Change) do not refuse. (d) An
// item no judge accepted moves.
func TestATerminalMoveWithoutAJudgedChangeMoves(t *testing.T) {
	dir, k := coveredKernel(t)
	legacy := seated(t, k, dir)
	if _, err := k.append(legacy.ID, gate.KindRun, "ada", gate.Result{Transition: "w->r", Gate: "code-ac", Result: gate.Accept, Coverage: "old"}); err != nil {
		t.Fatal(err)
	}
	none := seated(t, k, dir)
	commitFile(t, dir, "later.go", "package later\n")
	moveTo(t, k, legacy.ID, "d")
	moveTo(t, k, none.ID, "d")
}

// (f) A terminal item whose move recorded no change has no frozen diff, and
// says so instead of reading the tree.
func TestALegacyTerminalItemHasNoFrozenDiff(t *testing.T) {
	dir, k := coveredKernel(t)
	it := seated(t, k, dir)
	if _, err := k.append(it.ID, item.KindTransitioned, "ada", item.Move{From: "w", To: "d"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := k.ItemChange(it.ID); err == nil || !strings.Contains(err.Error(), "no frozen diff") {
		t.Fatalf("legacy terminal diff err = %v", err)
	}
}
