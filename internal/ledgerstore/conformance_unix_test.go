//go:build !windows

package ledgerstore

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var checkNames = []string{CheckListEmpty, CheckGetMissing, CheckPutNew, CheckPutExisting, CheckGetPresent, CheckListSorted, CheckListAfter, CheckErrorLine}

func TestRunPrefixIsFreshUnderConformance(t *testing.T) {
	now := time.Date(2026, 11, 8, 1, 2, 3, 0, time.UTC)
	a, b := RunPrefix(now), RunPrefix(now)
	if a == b || !regexp.MustCompile(`^conformance/20261108T010203Z-[0-9a-f]{8}/$`).MatchString(a) || CheckKey(a) != nil {
		t.Errorf("run prefixes %q %q", a, b)
	}
}

func TestConformancePassesOnTheReferenceAdapterFromAFreshRoot(t *testing.T) {
	root := t.TempDir()
	res := Conformance(ctx, fsStore(t, root))
	if len(res) != ConformanceChecks {
		t.Fatalf("%d results, want %d", len(res), ConformanceChecks)
	}
	for i, r := range res {
		if r.Name != checkNames[i] || !r.OK {
			t.Errorf("check %d %q: ok=%v %s", i+1, r.Name, r.OK, r.Reason)
		}
	}
}

// overwriting is a store whose put-if-absent always overwrites and exits 0;
// get and list are honest. It keeps objects as files named by the key with '/'
// as '%'.
const overwriting = `d=$(dirname "$0"); shift 2; op=$1; shift
mkdir -p "$d/o"
case $op in
put-if-absent) cat > "$d/o/$(printf %s "$1" | tr / %)"; exit 0 ;;
get) f="$d/o/$(printf %s "$1" | tr / %)"; [ -f "$f" ] || exit 4; cat "$f" ;;
list) ls "$d/o" | tr % / | awk -v p="$1" -v a="$2" 'index($0,p)==1 && (a=="" || (""$0) > (""a))' | LC_ALL=C sort ;;
*) echo "error: invalid: unknown operation $op" >&2; exit 2 ;;
esac
`

func writeStub(t *testing.T, body string) Command {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "store")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return Command{Path: "sh", Args: []string{p, "--root", dir}, Dir: dir}
}

func TestConformanceFailsAStoreThatOverwrites(t *testing.T) {
	res := Conformance(ctx, writeStub(t, overwriting))
	for _, r := range res {
		switch r.Name {
		case CheckPutExisting:
			if r.OK || !strings.Contains(r.Reason, "exited 0") || !strings.Contains(r.Reason, "expects exit 3") {
				t.Errorf("%s: ok=%v %s", r.Name, r.OK, r.Reason)
			}
		case CheckGetPresent:
			if r.OK || !strings.Contains(r.Reason, "overwrote") {
				t.Errorf("%s: ok=%v %s", r.Name, r.OK, r.Reason)
			}
		default:
			if !r.OK {
				t.Errorf("%s failed on an honest operation: %s", r.Name, r.Reason)
			}
		}
	}
}

// recording appends each invocation's operation and operands to a log, then
// execs the reference adapter.
func recording(t *testing.T, root string) (Command, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	p := filepath.Join(dir, "store")
	body := `shift 2; printf '%s\n' "$*" >> "` + log + `"; exec sh "` + adapterPath(t) + `" --root "` + root + `" "$@"` + "\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return Command{Path: "sh", Args: []string{p, "--root", root}, Dir: dir}, log
}

func TestConformanceOnlyTouchesConformanceKeys(t *testing.T) {
	c, log := recording(t, t.TempDir())
	for _, r := range Conformance(ctx, c) {
		if !r.OK {
			t.Errorf("%s: %s", r.Name, r.Reason)
		}
	}
	seen := map[string]bool{}
	for _, l := range readLines(t, log) {
		f := strings.Fields(l)
		if len(f) < 2 {
			t.Errorf("invocation %q has no operand", l)
			continue
		}
		seen[f[0]] = true
		for _, operand := range f[1:] {
			if !strings.HasPrefix(operand, ConformancePrefix) {
				t.Errorf("invocation %q: operand %q is outside %s", l, operand, ConformancePrefix)
			}
		}
	}
	for _, op := range []string{OpPutIfAbsent, OpGet, OpList, OpUnknown} {
		if !seen[op] {
			t.Errorf("store-test never ran %s", op)
		}
	}
}
