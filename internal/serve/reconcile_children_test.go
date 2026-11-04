package serve

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
)

// sty_a003a459 AC2: a reconcile pass spawns no `sync config push` child, while
// the reseed (`workspace add`), the documents push and the fast-forward pull
// each run once per pass. Every leg is the real *ViaCLI exec against the fake.
func TestReconcilePassSpawnsNoConfigPush(t *testing.T) {
	cli := newFakeCLI(t)
	repo := t.TempDir()
	r := &Reconciler{
		Targets: func(context.Context) []string { return []string{repo} },
		Bound:   func(string) bool { return false },
	}
	const passes = 3
	for i := 0; i < passes; i++ {
		r.pass(context.Background())
	}
	if n := cli.count(t, "sync config push"); n != 0 {
		t.Fatalf("%d pass(es) spawned %d `sync config push` child(ren), want 0: %v", passes, n, cli.lines(t))
	}
	for _, verb := range []string{"workspace add", "sync documents push --snapshot-only", "sync pull --fast-forward-only"} {
		if n := cli.count(t, verb); n != passes {
			t.Errorf("`%s` ran %d time(s) over %d passes, want once per pass: %v", verb, n, passes, cli.lines(t))
		}
	}
}

// sty_a003a459 AC3: the reseed child gets satelled's own endpoint in
// SATELLE_SERVER_ENDPOINT, so its `workspace add` posts there without probing
// /healthz; the other legs keep satelled's environment.
func TestReseedChildGetsTheServiceEndpoint(t *testing.T) {
	newFakeCLI(t)
	seen := filepath.Join(t.TempDir(), "endpoints")
	t.Setenv("SATELLE_TEST_ENDPOINTS", seen)
	t.Setenv(config.EnvServerEndpoint, "")
	_ = os.Unsetenv(config.EnvServerEndpoint)
	const self = "http://127.0.0.1:8787"
	repo := t.TempDir()
	r := &Reconciler{
		Targets:  func(context.Context) []string { return []string{repo} },
		Bound:    func(string) bool { return false },
		Endpoint: self,
	}
	r.pass(context.Background())
	raw, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	var reseeds int
	for _, ln := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.HasPrefix(ln, "workspace add ") {
			reseeds++
			if !strings.HasSuffix(ln, " "+self) {
				t.Errorf("reseed child's %s: %q, want %s", config.EnvServerEndpoint, ln, self)
			}
		} else if !strings.HasSuffix(ln, " unset") {
			t.Errorf("a non-reseed child was handed an endpoint: %q", ln)
		}
	}
	if reseeds != 1 {
		t.Fatalf("reseed children = %d, want 1: %s", reseeds, raw)
	}
}

// The endpoint satelled hands its children reaches the listener it bound.
func TestSelfEndpointReachesTheBoundListener(t *testing.T) {
	for addr, want := range map[string]string{
		"":          "http://127.0.0.1:8787",
		"0.0.0.0":   "http://127.0.0.1:8787",
		"::":        "http://127.0.0.1:8787",
		"127.0.0.1": "http://127.0.0.1:8787",
		"10.1.2.3":  "http://10.1.2.3:8787",
		"::1":       "http://[::1]:8787",
	} {
		if got := selfEndpoint(addr, 8787); got != want {
			t.Errorf("selfEndpoint(%q) = %q, want %q", addr, got, want)
		}
	}
}
