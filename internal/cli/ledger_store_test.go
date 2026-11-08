package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
)

// pkgDir is this package's source directory, read at init while the working
// directory is still the package.
var pkgDir, _ = os.Getwd()

// appendLedger appends a [ledger] section to the repo's satelle.toml.
func appendLedger(t *testing.T, dir, body string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, ".satelle", "satelle.toml"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("\n[ledger]\n" + body + "\n"); err != nil {
		t.Fatal(err)
	}
}

func calledAny(srv *fakeserver.Server, from int, services ...string) []string {
	var out []string
	for _, c := range srv.Calls()[from:] {
		for _, s := range services {
			if strings.Contains(c.Method, s) {
				out = append(out, c.Method)
			}
		}
	}
	return out
}

// The replication services a store call reaches on satelle.dev.
var storeServices = []string{"satelle.chain.v2.Chain/", "AttachmentService/", "ItemService/"}

// With no [ledger] store set the store is local while signed out and
// satelle.dev while signed in; signed in but unbound, push names both ways
// out.
func TestLedgerStoreDefaults(t *testing.T) {
	t.Run("signed out: local", func(t *testing.T) {
		isolate(t)
		must(t, "init")
		for _, verb := range []string{"push", "pull", "status"} {
			if out := must(t, "ledger", verb); !strings.Contains(out, "ledger store is local: nothing replicates") {
				t.Errorf("ledger %s = %q", verb, out)
			}
		}
	})
	t.Run("signed in: satelle.dev", func(t *testing.T) {
		srv := fakeserver.Start(t, testToken)
		newMachine(t)
		must(t, "project", "create", "shared", "--bind", "--server", srv.URL)
		createID(t, "story", "create", "--title", "x")
		out := must(t, "ledger", "status")
		if !strings.Contains(out, "ledger store: satelle.dev project ") || !strings.Contains(out, "local only") {
			t.Errorf("status = %q", out)
		}
		if got := calledAny(srv, 0, "Chain/Heads"); len(got) == 0 {
			t.Error("status made no Chain.Heads call")
		}
	})
	t.Run("signed in, unbound", func(t *testing.T) {
		newMachine(t)
		createID(t, "story", "create", "--title", "x")
		_, err := run(t, "", "ledger", "push")
		if ExitCodeOf(err) != 1 || !strings.Contains(err.Error(), "satelle project bind") ||
			!strings.Contains(err.Error(), `[ledger] store = "local"`) || !strings.Contains(err.Error(), "no [ledger] store set") {
			t.Fatalf("unbound push: %v", err)
		}
	})
	t.Run("satelle.dev named, unbound", func(t *testing.T) {
		dir := isolate(t)
		must(t, "init")
		appendLedger(t, dir, `store = "satelle.dev"`)
		_, err := run(t, "", "ledger", "pull")
		if ExitCodeOf(err) != 1 || !strings.Contains(err.Error(), "ledger store is satelle.dev but this repo is not bound") {
			t.Fatalf("unbound pull: %v", err)
		}
	})
}

// With store local, push, pull and status make no store or network call and
// say the store is local: signed in and bound to a server that is not
// running, and signed out with no store set.
func TestLocalStoreMakesNoCall(t *testing.T) {
	t.Run("named, signed in, bound to an unreachable server", func(t *testing.T) {
		srv := fakeserver.Start(t, testToken)
		m := newMachine(t)
		must(t, "project", "create", "shared", "--bind", "--server", srv.URL)
		createID(t, "story", "create", "--title", "x")
		appendLedger(t, m.dir, `store = "local"`)
		before := len(srv.Calls())
		srv.Stop()
		for _, verb := range []string{"push", "pull", "status"} {
			if out := must(t, "ledger", verb); !strings.Contains(out, "ledger store is local") {
				t.Errorf("ledger %s = %q", verb, out)
			}
		}
		if n := len(srv.Calls()); n != before {
			t.Errorf("the local store made %d call(s)", n-before)
		}
	})
	t.Run("unset, signed out", func(t *testing.T) {
		isolate(t)
		must(t, "init")
		createID(t, "story", "create", "--title", "x")
		for _, verb := range []string{"push", "pull", "status"} {
			if out := must(t, "ledger", verb); !strings.Contains(out, "ledger store is local") {
				t.Errorf("ledger %s = %q", verb, out)
			}
		}
	})
}

// satelle sync is gone, with no alias: ledger push, pull and status are the
// one replication surface.
func TestSyncIsRemoved(t *testing.T) {
	isolate(t)
	must(t, "init")
	if _, err := run(t, "", "sync", "push"); err == nil || !strings.Contains(err.Error(), `unknown command "sync"`) {
		t.Fatalf("sync push: %v", err)
	}
	help := must(t, "--help")
	if regexp.MustCompile(`(?m)^\s+sync\s`).MatchString(help) {
		t.Errorf("--help lists sync:\n%s", help)
	}
	lh := must(t, "ledger", "--help")
	for _, verb := range []string{"push", "pull", "status"} {
		if !regexp.MustCompile(`(?m)^\s+` + verb + `\s`).MatchString(lh) {
			t.Errorf("ledger --help lacks %s:\n%s", verb, lh)
		}
	}
	files, err := filepath.Glob(filepath.Join(pkgDir, "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("glob: %v", err)
	}
	src := regexp.MustCompile(`Use:\s*"sync\b`)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if src.Match(b) {
			t.Errorf("%s defines a sync command", f)
		}
	}
}

// No Go branch picks a vendor: the store code names satelle.dev, local and
// the operator's command, never a storage vendor.
func TestNoVendorBranch(t *testing.T) {
	vendor := regexp.MustCompile(`(?i)azure|\bs3\b|gcs|tigris|\baws\b|minio`)
	var files []string
	for _, pattern := range []string{"../ledgerstore/*.go", "../ledgerrepl/*.go", "cmd_ledger.go", "ledger_store.go"} {
		m, err := filepath.Glob(filepath.Join(pkgDir, pattern))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, m...)
	}
	n := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		n++
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if loc := vendor.FindIndex(b); loc != nil {
			t.Errorf("%s names a vendor: %q", f, b[loc[0]:loc[1]])
		}
	}
	if n < 8 {
		t.Fatalf("scanned %d files; the globs found too few", n)
	}
}
