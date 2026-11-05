package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/lease"
	"github.com/bobmcallan/satelle/internal/store"
)

func disableServeProbe(t *testing.T) {
	t.Helper()
	prev := serveProbeEnabled
	serveProbeEnabled = false
	t.Cleanup(func() { serveProbeEnabled = prev })
}

// liveLegacyFixture builds a legacy-layout repo with a fresh engagement lease
// in the in-repo DB. Owner is pid-less so only heartbeat governs Alive.
func liveLegacyFixture(t *testing.T) (home, repo, dataDir, legacyDB string, a *app.App) {
	t.Helper()
	disableServeProbe(t)
	home = t.TempDir()
	t.Setenv("SATELLE_HOME", home)

	repo = t.TempDir()
	dataDir = filepath.Join(repo, config.DefaultDataDir)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "agents.toml"), []byte("[executor]\nharness = \"in-loop\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "satelle.toml"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	legacyDB = filepath.Join(dataDir, config.DefaultDBName)
	db, err := store.Open(legacyDB)
	if err != nil {
		t.Fatal(err)
	}
	// pid-less owner: only heartbeat staleness applies (not dead-pid steal).
	_, _, _, err = db.Leases.Acquire(context.Background(), "sty_livetest", "story", "local@test-host", "in_progress", true)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	_ = db.Close()

	a = &app.App{
		Config:     config.Config{},
		RepoRoot:   repo,
		DataDir:    dataDir,
		RuntimeDir: dataDir,
		DBPath:     legacyDB,
	}
	return home, repo, dataDir, legacyDB, a
}

// TestRuntimeMigrateRefusesLiveLease covers the public runtime migrate entrypoint.
func TestRuntimeMigrateRefusesLiveLease(t *testing.T) {
	home, _, _, _, a := liveLegacyFixture(t)
	var out strings.Builder
	err := runRuntimeMigrate(&out, a, false, false, false)
	if err == nil {
		t.Fatalf("expected refuse, got nil\n%s", out.String())
	}
	if !strings.Contains(err.Error(), "sty_livetest") {
		t.Errorf("refusal should name lease: %v", err)
	}
	homeDB := filepath.Join(home, config.RepoKey(a.RepoRoot), config.DefaultDBName)
	if fileExists(homeDB) {
		t.Errorf("home DB must not exist after refuse: %s", homeDB)
	}
}

// holdersGone makes the pid probe say the process that stamped a planted seat's
// in-flight mark is gone: a planted lease carries this test process's own (live)
// pid, and a live in-flight pid keeps a seat Alive whatever its heartbeat, so a
// simulated kill needs the probe to agree.
func holdersGone(t *testing.T) {
	t.Helper()
	prev := lease.PidAlive
	lease.PidAlive = func(int) bool { return false }
	t.Cleanup(func() { lease.PidAlive = prev })
}

// TestMigrateHelpDocumentsSplitBrainRecovery (AC4).
func TestMigrateHelpDocumentsSplitBrainRecovery(t *testing.T) {
	if !strings.Contains(migrateSplitBrainHelp, "satelle story seat") {
		t.Error("split-brain help must name satelle story seat")
	}
	if !strings.Contains(migrateSplitBrainHelp, "through the workflow") {
		t.Error("split-brain help must tell operators to re-drive through gates")
	}
	// Command Long embeds the const.
	root := NewRootCmd()
	cmd, _, err := root.Find([]string{"migrate"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cmd.Long, migrateSplitBrainHelp) {
		t.Error("satelle migrate Long must embed migrateSplitBrainHelp")
	}
	if !strings.Contains(cmd.Long, "--allow-live") && !strings.Contains(cmd.Long, "LIVE RUNTIME") {
		t.Error("Long should document live-runtime refusal")
	}
}

// TestProbeServeViaHealthz: serve probe returns a holder when /healthz is ok.
func TestProbeServeViaHealthz(t *testing.T) {
	t.Setenv("SATELLE_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			_, _ = w.Write([]byte("ok"))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	prev := healthzOK
	healthzOK = func(url string) bool {
		// Redirect any probe URL to our test server.
		resp, err := http.Get(srv.URL + "/healthz")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == 200
	}
	t.Cleanup(func() { healthzOK = prev })

	prevProbe := serveProbeEnabled
	serveProbeEnabled = true
	t.Cleanup(func() { serveProbeEnabled = prevProbe })

	h, ok := probeServe(config.Config{}, "/unused")
	if !ok {
		t.Fatal("expected serve holder")
	}
	if h.Kind != "serve" {
		t.Errorf("kind = %q", h.Kind)
	}
}

// TestScanLiveLeasesMissingTable: pre-lease DB (no engagement_lease) is quiet.
func TestScanLiveLeasesMissingTable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(`DROP TABLE engagement_lease`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	_ = db.Close()
	holders, err := scanLiveLeases(path)
	if err != nil {
		t.Fatalf("missing table should be quiet: %v", err)
	}
	if len(holders) != 0 {
		t.Fatalf("want no holders, got %v", holders)
	}
}
