//go:build !windows

package kernel

import (
	"context"
	"errors"
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

// (a) A commit after the judge refuses a gateless terminal move, naming the
// gate and its transition, and records nothing. (e) Re-running the judge
// over the new change lets it through, frozen at that change.
func TestATerminalMoveRefusesAChangeNoJudgeAccepted(t *testing.T) {
	dir, k, it := judgedItem(t)
	commitFile(t, dir, "later.go", "package later\n")
	_, before, _ := k.load(it.ID)

	_, err := k.Transition(context.Background(), it.ID, "d", "ada")
	var unc *UncoveredError
	if !errors.As(err, &unc) {
		t.Fatalf("move to d after a later commit: err = %v, want an UncoveredError", err)
	}
	if unc.Gate != "code-ac" || unc.Transition != "w->r" || unc.To != "d" {
		t.Fatalf("refusal = %+v", unc)
	}
	for _, want := range []string{"gate code-ac", "w -> r", "Re-run code-ac", "return the item to w"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not say %q", err, want)
		}
	}
	got, _ := k.Get(it.ID)
	_, after, _ := k.load(it.ID)
	if got.Status != "r" || len(after) != len(before) {
		t.Fatalf("a refused move recorded something: status %s, %d events, was %d", got.Status, len(after), len(before))
	}

	moveTo(t, k, it.ID, "w")
	res := moveTo(t, k, it.ID, "r")
	if res.Gates[0].Result != gate.Accept {
		t.Fatalf("code-ac on the new change = %+v, want a fresh accept", res.Gates[0])
	}
	moveTo(t, k, it.ID, "d")
	head := gittest.Git(t, dir, "rev-parse", "HEAD")
	if fs := frozenOf(t, k, it.ID, "d"); fs == nil || fs.Head != head || fs.Base != res.Gates[0].Change.Base {
		t.Fatalf("frozen change = %+v, want base %s head %s", fs, res.Gates[0].Change.Base, head)
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
