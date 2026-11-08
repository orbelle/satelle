//go:build !windows

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/ledgerstore"
)

// adapterAbs is resolved at init, while the working directory is the package.
var adapterAbs, adapterErr = filepath.Abs(filepath.Join("..", "..", "adapters", "satelle-store-fs"))

func storeAdapter(t *testing.T) string {
	t.Helper()
	p, err := adapterAbs, adapterErr
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// configureLedger appends a [ledger] section to the repo's satelle.toml.
func configureLedger(t *testing.T, dir, body string) {
	t.Helper()
	p := filepath.Join(dir, ".satelle", "satelle.toml")
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("\n[ledger]\n" + body + "\n"); err != nil {
		t.Fatal(err)
	}
}

var checkNames = []string{ledgerstore.CheckListEmpty, ledgerstore.CheckGetMissing, ledgerstore.CheckPutNew,
	ledgerstore.CheckPutExisting, ledgerstore.CheckGetPresent, ledgerstore.CheckListSorted,
	ledgerstore.CheckListAfter, ledgerstore.CheckErrorLine}

func TestLedgerStoreTestPassesOnTheReferenceAdapter(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	root := t.TempDir()
	configureLedger(t, dir, `store = { command = "sh", args = ['`+storeAdapter(t)+`', "--root", '`+root+`'] }`)
	out := must(t, "ledger", "store-test")
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 9 {
		t.Fatalf("output:\n%s", out)
	}
	for i, name := range checkNames {
		if lines[i] != "ok    "+name {
			t.Errorf("line %d %q, want ok for %q", i+1, lines[i], name)
		}
	}
	if lines[8] != "store-test: 8 checks passed against sh" {
		t.Errorf("summary %q", lines[8])
	}
	n := 0
	err := filepath.WalkDir(filepath.Join(root, "objects"), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		n++
		if !strings.HasPrefix(strings.TrimPrefix(p, filepath.Join(root, "objects")+"/"), "_conformance%") {
			t.Errorf("the adapter wrote %s, outside conformance/", p)
		}
		return nil
	})
	if err != nil || n == 0 {
		t.Fatalf("walk: %d objects, %v", n, err)
	}
}

const overwritingStore = `shift 2; op=$1; shift; d=$(dirname "$0"); mkdir -p "$d/o"
case $op in
put-if-absent) cat > "$d/o/$(printf %s "$1" | tr / %)" ;;
get) f="$d/o/$(printf %s "$1" | tr / %)"; [ -f "$f" ] || exit 4; cat "$f" ;;
list) ls "$d/o" | tr % / | awk -v p="$1" -v a="$2" 'index($0,p)==1 && (a=="" || (""$0) > (""a))' | LC_ALL=C sort ;;
*) echo "error: invalid: unknown operation $op" >&2; exit 2 ;;
esac
`

func TestLedgerStoreTestFailsAStoreThatOverwrites(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	stub := filepath.Join(t.TempDir(), "store")
	if err := os.WriteFile(stub, []byte(overwritingStore), 0o644); err != nil {
		t.Fatal(err)
	}
	configureLedger(t, dir, `store = { command = "sh", args = ['`+stub+`', "--root", "x"] }`)
	out, err := run(t, "", "ledger", "store-test")
	if err == nil || ExitCodeOf(err) != 1 || !strings.Contains(err.Error(), ledgerstore.CheckPutExisting) || !strings.Contains(err.Error(), "of 8 checks failed") {
		t.Fatalf("err %v\n%s", err, out)
	}
	if !strings.Contains(out, "FAIL  "+ledgerstore.CheckPutExisting+": ") {
		t.Errorf("output lacks the FAIL line:\n%s", out)
	}
}

func TestLedgerStoreTestOnlyTouchesConformanceAndWithholdsSatelleCredentials(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	t.Setenv("SATELLE_TOKEN", "tok")
	t.Setenv("SATELLE_NODE_TOKEN", "node")
	work := t.TempDir()
	root := t.TempDir()
	log, envf := filepath.Join(work, "log"), filepath.Join(work, "env")
	stub := filepath.Join(work, "store")
	body := `shift 2; env > '` + envf + `'; printf '%s\n' "$*" >> '` + log + `'; exec sh '` + storeAdapter(t) + `' --root '` + root + `' "$@"` + "\n"
	if err := os.WriteFile(stub, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	configureLedger(t, dir, `store = { command = "sh", args = ['`+stub+`', "--root", "x"] }`)
	must(t, "ledger", "store-test")
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		f := strings.Fields(l)
		for _, operand := range f[1:] {
			if !strings.HasPrefix(operand, ledgerstore.ConformancePrefix) {
				t.Errorf("invocation %q: operand %q is outside conformance/", l, operand)
			}
		}
	}
	env, err := os.ReadFile(envf)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(string(env), "\n") {
		if strings.HasPrefix(l, "SATELLE_TOKEN=") || strings.HasPrefix(l, "SATELLE_NODE_TOKEN=") {
			t.Errorf("the store command saw satelle's credential %s", l)
		}
	}
}

func TestLedgerStoreTestRefusesWithoutAnExternalCommand(t *testing.T) {
	for name, body := range map[string]string{
		"no [ledger] store": "",
		"satelle.dev":       `store = "satelle.dev"`,
		"local":             `store = "local"`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := isolate(t)
			must(t, "init")
			if body != "" {
				configureLedger(t, dir, body)
			}
			// Any sh the command started would log here first.
			shim := t.TempDir()
			log := filepath.Join(shim, "log")
			if err := os.WriteFile(filepath.Join(shim, "sh"), []byte("#!/bin/sh\necho \"$@\" >> '"+log+"'\nexec /bin/sh \"$@\"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))
			out, err := run(t, "", "ledger", "store-test")
			if err == nil || ExitCodeOf(err) != 1 || !strings.Contains(err.Error(), "only an external store command") || !strings.Contains(err.Error(), "implements the adapter operations") {
				t.Fatalf("err %v\n%s", err, out)
			}
			if body != "" && !strings.Contains(err.Error(), "built-in") {
				t.Errorf("the refusal does not name the built-in kind: %v", err)
			}
			if strings.Contains(out, "ok ") || strings.Contains(out, "FAIL") {
				t.Errorf("checks ran:\n%s", out)
			}
			if _, err := os.Stat(log); !os.IsNotExist(err) {
				t.Errorf("a command ran (%v)", err)
			}
		})
	}
}
