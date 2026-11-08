//go:build !windows

package ledgerstore

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// adapterPath is the reference adapter in the repo.
func adapterPath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "adapters", "satelle-store-fs"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
	return p
}

// fsStore runs the reference adapter through sh over root.
func fsStore(t *testing.T, root string) Command {
	t.Helper()
	return Command{Path: "sh", Args: []string{adapterPath(t), "--root", root}, Dir: filepath.Dir(root)}
}

func TestFSAdapterFreshRootIsAnEmptyStore(t *testing.T) {
	root := t.TempDir()
	c := fsStore(t, root)
	keys, err := c.List(ctx, "p/", "")
	if err != nil || len(keys) != 0 {
		t.Fatalf("list on a fresh root: %q %v", keys, err)
	}
	if _, found, err := c.Get(ctx, "p/x"); err != nil || found {
		t.Fatalf("get on a fresh root: %v %v", found, err)
	}
	if _, err := os.Stat(filepath.Join(root, "objects")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("list or get created objects/ (%v)", err)
	}
}

func TestFSAdapterPutIfAbsentNeverOverwrites(t *testing.T) {
	root := t.TempDir()
	c := fsStore(t, root)
	if _, created, err := c.PutIfAbsent(ctx, "chains/x/000000000001.json", []byte("A\n")); err != nil || !created {
		t.Fatalf("first put: %v %v", created, err)
	}
	existing, created, err := c.PutIfAbsent(ctx, "chains/x/000000000001.json", []byte("B\n"))
	if err != nil || created || string(existing) != "A\n" {
		t.Fatalf("second put: %q %v %v, want exit 3 with A", existing, created, err)
	}
	b, err := os.ReadFile(filepath.Join(root, "objects", "_chains%x%000000000001.json@"))
	if err != nil || string(b) != "A\n" {
		t.Fatalf("the stored object reads %q (%v)", b, err)
	}
	if body, found, err := c.Get(ctx, "chains/x/000000000001.json"); err != nil || !found || string(body) != "A\n" {
		t.Fatalf("get: %q %v %v", body, found, err)
	}
	if _, found, err := c.Get(ctx, "chains/x/000000000002.json"); err != nil || found {
		t.Fatalf("get of a missing key in a written store: %v %v", found, err)
	}
	if ents, _ := os.ReadDir(filepath.Join(root, "tmp")); len(ents) != 0 {
		t.Errorf("tmp holds %d leftovers", len(ents))
	}
}

func put(t *testing.T, c Command, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if _, created, err := c.PutIfAbsent(ctx, k, []byte("body of "+k)); err != nil || !created {
			t.Fatalf("put %s: %v %v", k, created, err)
		}
	}
}

func list(t *testing.T, c Command, prefix, after string) []string {
	t.Helper()
	keys, err := c.List(ctx, prefix, after)
	if err != nil {
		t.Fatalf("list %s %s: %v", prefix, after, err)
	}
	return keys
}

func TestFSAdapterListIsSortedPrefixedAndHonoursAfter(t *testing.T) {
	c := fsStore(t, t.TempDir())
	put(t, c, "p/c", "p/a", "pa", "p/b", "q/a", "p/a/deep")
	if got, want := list(t, c, "p/", ""), []string{"p/a", "p/a/deep", "p/b", "p/c"}; !slices.Equal(got, want) {
		t.Errorf("list p/: %q, want %q", got, want)
	}
	if got, want := list(t, c, "p/", "p/a/deep"), []string{"p/b", "p/c"}; !slices.Equal(got, want) {
		t.Errorf("list p/ after p/a/deep: %q, want %q", got, want)
	}
	if got := list(t, c, "none/", ""); len(got) != 0 {
		t.Errorf("an empty prefix lists %q", got)
	}
	if got, want := list(t, c, "p", ""), []string{"p/a", "p/a/deep", "p/b", "p/c", "pa"}; !slices.Equal(got, want) {
		t.Errorf("list p (a pure string prefix): %q, want %q", got, want)
	}
}

func TestFSAdapterComparesNumericLookingKeysAsStrings(t *testing.T) {
	c := fsStore(t, t.TempDir())
	put(t, c, "12", "1e2", "1", "102", "1.0")
	if got, want := list(t, c, "1", ""), []string{"1", "1.0", "102", "12", "1e2"}; !slices.Equal(got, want) {
		t.Errorf("list 1: %q, want %q", got, want)
	}
	if got, want := list(t, c, "1", "102"), []string{"12", "1e2"}; !slices.Equal(got, want) {
		t.Errorf("list 1 after 102: %q, want %q", got, want)
	}
	if got, want := list(t, c, "1", "1"), []string{"1.0", "102", "12", "1e2"}; !slices.Equal(got, want) {
		t.Errorf("list 1 after 1: %q, want %q", got, want)
	}
}

func TestFSAdapterUnusualKeysRoundTrip(t *testing.T) {
	c := fsStore(t, t.TempDir())
	long := strings.Repeat("k", MaxKeyBytes)
	keys := []string{long, "d/./x", "d/../y", "d/..", "a", "a/b", "_a", "a/_b"}
	put(t, c, keys...)
	for _, k := range keys {
		if body, found, err := c.Get(ctx, k); err != nil || !found || string(body) != "body of "+k {
			t.Errorf("get %s: %q %v %v", k, body, found, err)
		}
	}
	want := slices.Clone(keys)
	slices.Sort(want)
	var got []string
	for _, p := range []string{"_", "a", "d", "k"} {
		got = append(got, list(t, c, p, "")...)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("listed %q, want %q", got, want)
	}
}

func TestFSAdapterErrorsPrintTheLine(t *testing.T) {
	c := fsStore(t, filepath.Join(t.TempDir(), "missing"))
	_, _, err := c.Get(ctx, "k")
	var se *Error
	if !errors.As(err, &se) || se.Retryable || !strings.HasPrefix(se.Line, "error: invalid:") {
		t.Fatalf("a missing root: %#v", err)
	}
	r, err := fsStore(t, t.TempDir()).run(ctx, OpUnknown, []string{"conformance/x"}, nil)
	if err != nil || r.exit != ExitPermanent || r.code != "invalid" {
		t.Fatalf("an unknown operation: %+v %v", r, err)
	}
}

func TestFSAdapterScriptIsWellFormed(t *testing.T) {
	p := adapterPath(t)
	if out, err := exec.Command("sh", "-n", p).CombinedOutput(); err != nil {
		t.Fatalf("sh -n: %v\n%s", err, out)
	}
	sc, err := exec.LookPath("shellcheck")
	if err != nil {
		t.Log("shellcheck is not installed; sh -n only")
		return
	}
	if out, err := exec.Command(sc, "-s", "sh", p).CombinedOutput(); err != nil {
		t.Errorf("shellcheck: %v\n%s", err, out)
	}
}
