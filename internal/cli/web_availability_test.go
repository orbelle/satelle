package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/testutil"
)

// stubHealthz swaps the shared reachability seam (runtime_liveness.go) for the
// test and records the URLs probed. Every test here stubs it, so no test dials
// a port on the developer's machine.
func stubHealthz(t *testing.T, live bool) *[]string {
	t.Helper()
	var probed []string
	prev := healthzOK
	healthzOK = func(url string) bool {
		probed = append(probed, url)
		return live
	}
	t.Cleanup(func() { healthzOK = prev })
	return &probed
}

// writeGlobalPort pins the global service port under the isolated home.
func writeGlobalPort(t *testing.T, port int) {
	t.Helper()
	body := "[service]\nport = " + strconv.Itoa(port) + "\n"
	if err := os.WriteFile(config.GlobalConfigPath(), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestProbeWebAvailabilityProbesTheResolvedPort (sty_fb5e6d96 AC3, AC4): the port
// shown is the port probed — reachability comes from the existing healthzOK seam,
// and a single port is probed so "live" can never name a different port than the URL.
func TestProbeWebAvailabilityProbesTheResolvedPort(t *testing.T) {
	testutil.IsolateHome(t)
	writeGlobalPort(t, 9911)
	probed := stubHealthz(t, true)

	got := probeWebAvailability()
	if !got.Resolved || !got.Live {
		t.Fatalf("want resolved+live, got %+v", got)
	}
	if got.Port != 9911 {
		t.Fatalf("port = %d, want 9911 (the global service port)", got.Port)
	}
	if len(*probed) != 1 || (*probed)[0] != "http://127.0.0.1:9911/healthz" {
		t.Fatalf("probed = %v, want exactly one /healthz on the resolved port", *probed)
	}
	if !strings.Contains(got.URL(), "9911") {
		t.Fatalf("URL = %q, must name the probed port", got.URL())
	}
}

// TestProbeWebAvailabilityNotAnswering (AC1): a configured port with nothing
// answering is never rendered as live.
func TestProbeWebAvailabilityNotAnswering(t *testing.T) {
	testutil.IsolateHome(t)
	writeGlobalPort(t, 9911)
	stubHealthz(t, false)

	got := probeWebAvailability()
	if !got.Resolved {
		t.Fatalf("readable config must resolve, got %+v", got)
	}
	if got.Live {
		t.Fatal("healthz said no — must not report live")
	}
	if strings.Contains(got.statusValue(), "live") || strings.Contains(got.hookLine(), "— live") {
		t.Fatalf("dead service rendered as live: %q / %q", got.statusValue(), got.hookLine())
	}
}

// TestProbeWebAvailabilityAbsentGlobalConfig (AC4): no global config is not an
// error — it resolves to the documented default port, and liveness still decides.
func TestProbeWebAvailabilityAbsentGlobalConfig(t *testing.T) {
	testutil.IsolateHome(t)
	stubHealthz(t, false)

	got := probeWebAvailability()
	if !got.Resolved || got.Port != config.DefaultWebPort {
		t.Fatalf("got %+v, want resolved on DefaultWebPort %d", got, config.DefaultWebPort)
	}
	if got.Live {
		t.Fatal("must not claim live")
	}
}

// TestProbeWebAvailabilityUnresolvableConfig (AC5): a malformed global config is
// the one honest "unknown" — never a fabricated available, and never a probe.
func TestProbeWebAvailabilityUnresolvableConfig(t *testing.T) {
	home := testutil.IsolateHome(t)
	if err := os.WriteFile(filepath.Join(home, config.GlobalConfigName), []byte("[service\nport = "), 0o644); err != nil {
		t.Fatal(err)
	}
	probed := stubHealthz(t, true) // would say live if consulted

	got := probeWebAvailability()
	if got.Resolved || got.Live {
		t.Fatalf("unreadable config must be unknown, got %+v", got)
	}
	if len(*probed) != 0 {
		t.Fatalf("must not probe when the port is unknown, probed %v", *probed)
	}
	for _, s := range []string{got.statusValue(), got.hookLine()} {
		if !strings.Contains(s, "unknown") || strings.Contains(s, "live") {
			t.Fatalf("want a plain unknown, got %q", s)
		}
	}
}

// TestWebAvailabilityRenderingsDiffer (AC1): live and configured-but-dead are
// visibly different in both renderings, and neither is the bare port.
func TestWebAvailabilityRenderingsDiffer(t *testing.T) {
	live := webAvailability{Port: 8787, Live: true, Resolved: true}
	dead := webAvailability{Port: 8787, Live: false, Resolved: true}

	if live.statusValue() == dead.statusValue() {
		t.Fatal("status value identical for live and dead service")
	}
	if live.hookLine() == dead.hookLine() {
		t.Fatal("hook line identical for live and dead service")
	}
	for _, s := range []string{live.statusValue(), dead.statusValue(), live.hookLine(), dead.hookLine()} {
		if !strings.Contains(s, "http://localhost:8787") {
			t.Fatalf("%q must carry the URL a user can open", s)
		}
		if strings.Count(s, "\n") != 0 {
			t.Fatalf("%q must be a single line", s)
		}
	}
	if !strings.Contains(live.statusValue(), "live") || !strings.Contains(dead.statusValue(), "not answering") {
		t.Fatalf("state tokens missing: %q / %q", live.statusValue(), dead.statusValue())
	}
}

// statusOutput runs the real `satelle status` RunE against a hermetic app.
func statusOutput(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	rt := filepath.Join(t.TempDir(), "rt")
	if err := os.MkdirAll(rt, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(rt, config.DefaultDBName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	a := &app.App{
		Config:     config.Config{},
		RepoRoot:   repo,
		DataDir:    filepath.Join(repo, config.DefaultDataDir),
		RuntimeDir: rt,
		DBPath:     filepath.Join(rt, config.DefaultDBName),
		Store:      db,
	}

	var cmd *cobra.Command
	for _, c := range NewRootCmd().Commands() {
		if c.Name() == "status" {
			cmd = c
		}
	}
	if cmd == nil {
		t.Fatal("status command not registered")
	}
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetContext(context.WithValue(context.Background(), appCtxKey{}, a))
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("status: %v", err)
	}
	return out.String()
}

// TestStatusReportsRealAvailability (AC1, AC4): status names the URL and whether
// anything answers; live and dead differ; the config-echo port row is gone.
func TestStatusReportsRealAvailability(t *testing.T) {
	testutil.IsolateHome(t)
	writeGlobalPort(t, 9911)

	stubHealthz(t, true)
	liveOut := statusOutput(t)

	stubHealthz(t, false)
	deadOut := statusOutput(t)

	if liveOut == deadOut {
		t.Fatal("status output identical whether or not the service answers")
	}
	for _, out := range []string{liveOut, deadOut} {
		if !strings.Contains(out, "http://localhost:9911") {
			t.Fatalf("status must print the URL, got:\n%s", out)
		}
		if strings.Contains(out, "web port") {
			t.Fatalf("config-echo `web port` row must be gone, got:\n%s", out)
		}
	}
	if !strings.Contains(liveOut, "live") {
		t.Fatalf("answering service must read as live, got:\n%s", liveOut)
	}
	if !strings.Contains(deadOut, "not answering") {
		t.Fatalf("dead service must say so, got:\n%s", deadOut)
	}
}

// hookContent runs the real runHookContext hermetically and returns the
// additionalContext it injected ("" when it injected nothing).
func hookContent(t *testing.T) string {
	t.Helper()
	var out, errb bytes.Buffer
	if err := runHookContext(&out, &errb, "grok", nil); err != nil {
		t.Fatalf("runHookContext must fail open, got %v", err)
	}
	if strings.TrimSpace(out.String()) == "" {
		return ""
	}
	var doc struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("hook emitted non-JSON %q: %v", out.String(), err)
	}
	return doc.HookSpecificOutput.AdditionalContext
}

// hookRepo puts the test in a hermetic repo + home so app.Open() succeeds
// without touching the operator's real ~/.satelle or this checkout.
func hookRepo(t *testing.T) {
	t.Helper()
	testutil.IsolateHome(t)
	t.Setenv("SATELLE_CONFIG", "")
	repo := t.TempDir()
	// A GOVERNED repo: these tests are about what the hook injects for a satelle
	// repo. The .satelle/ directory is what makes it governed — without it the
	// hook correctly injects nothing (sty_20a7824c), which is asserted separately
	// by TestHookContextInjectsNothingInUngovernedRepo below. Before that guard
	// this helper relied on zero-config open silently materialising a runtime
	// plane for a bare temp dir.
	if err := os.MkdirAll(filepath.Join(repo, config.DefaultDataDir), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
}

// TestHookContextInjectsNothingInUngovernedRepo (sty_20a7824c AC3): a session
// opened in an ordinary directory that satelle does not govern gets no
// injection and — critically — no runtime plane. The hook must go INERT, not
// error: it is strictly quieter than before, never fail-closed.
func TestHookContextInjectsNothingInUngovernedRepo(t *testing.T) {
	home := testutil.IsolateHome(t)
	t.Setenv("SATELLE_CONFIG", "")
	t.Chdir(t.TempDir()) // deliberately NO .satelle/

	if got := hookContent(t); strings.TrimSpace(got) != "" {
		t.Errorf("an ungoverned repo must get no injection, got:\n%s", got)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("a session hook must not materialise a plane for an ungoverned repo, found: %v", names)
	}
}

func countNonBlank(s string) int {
	n := 0
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n
}

// TestHookContextReportsNotAnswering (AC1, AC7): the hook call site itself —
// not just the pure renderer — says "not answering" for a configured-but-dead
// service, and never says live.
func TestHookContextReportsNotAnswering(t *testing.T) {
	hookRepo(t)
	writeGlobalPort(t, 9911)
	stubHealthz(t, false)

	content := hookContent(t)
	if !strings.Contains(content, "http://localhost:9911 — not answering") {
		t.Fatalf("dead service must render as not answering, got:\n%s", content)
	}
	if strings.Contains(content, "— live") {
		t.Fatalf("must not claim live, got:\n%s", content)
	}
}

// TestHookAndStatusAgreeOnPort (AC4): one authoritative resolution path — both
// surfaces name the same port for the same machine state.
func TestHookAndStatusAgreeOnPort(t *testing.T) {
	hookRepo(t)
	writeGlobalPort(t, 9911)
	stubHealthz(t, false)

	if got := statusOutput(t); !strings.Contains(got, "http://localhost:9911") {
		t.Fatalf("status resolved a different port:\n%s", got)
	}
	if got := hookContent(t); !strings.Contains(got, "http://localhost:9911") {
		t.Fatalf("hook resolved a different port:\n%s", got)
	}
}

// TestPrependAvailabilityLine covers the composition helper's edges.
func TestPrependAvailabilityLine(t *testing.T) {
	if got := prependContextLine("body", ""); got != "body" {
		t.Fatalf("empty line must not alter content, got %q", got)
	}
	if got := prependContextLine("", "line"); got != "line" {
		t.Fatalf("empty content must not gain a separator, got %q", got)
	}
	if got := prependContextLine("body", "line"); got != "line\n\nbody" {
		t.Fatalf("got %q", got)
	}
}
