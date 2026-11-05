package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/mirror"
)

// TestWorkspaceAddHelpNamesStaleRecovery (sty_e6e467fe AC7): the verb that
// re-seeds a stale mirror must SAY that is what it is for. Recovery used to be
// an undocumented side effect of a join verb.
func TestWorkspaceAddHelpNamesStaleRecovery(t *testing.T) {
	var add *cobra.Command
	for _, c := range NewRootCmd().Commands() {
		if c.Name() != "workspace" {
			continue
		}
		for _, sub := range c.Commands() {
			if sub.Name() == "add" {
				add = sub
			}
		}
	}
	if add == nil {
		t.Fatal("workspace add command not registered")
	}
	for _, want := range []string{"stale", "re-request", "last-ingest"} {
		if !strings.Contains(add.Long, want) {
			t.Errorf("workspace add help missing %q:\n%s", want, add.Long)
		}
	}
}

// TestWorkspaceAddRegistersAndSeeds (sty_805bee9c AC1): with [server] endpoint,
// workspace add registers the repo and POSTs exactly one snapshot.
func TestWorkspaceAddRegistersAndSeeds(t *testing.T) {
	var snapHits atomic.Int32
	var lastSnap []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ingest/snapshot" {
			snapHits.Add(1)
			lastSnap, _ = io.ReadAll(r.Body)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	repo := tempRepo(t)
	t.Setenv(EnvServerEndpoint, srv.URL)

	// Seed a story so the snapshot body is non-empty.
	if out, err := runRoot(t, "story", "create",
		"--title", "Workspace Seed",
		"--body", "join seeds mirror",
		"--acceptance", "1. in snapshot",
		"--category", "chore",
	); err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}

	snapHits.Store(0)
	out, err := runRoot(t, "workspace", "add")
	if err != nil {
		t.Fatalf("workspace add: %v\n%s", err, out)
	}
	if !strings.Contains(out, "workspace add: ok") {
		t.Fatalf("expected ok line, got:\n%s", out)
	}
	if snapHits.Load() != 1 {
		t.Fatalf("snapshot posts = %d, want 1", snapHits.Load())
	}
	abs, _ := filepath.Abs(repo)
	gc, err := config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range gc.Workspace.Repos {
		if r == abs {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("registry missing %s: %v", abs, gc.Workspace.Repos)
	}
	var snap struct {
		Stories []json.RawMessage `json:"stories"`
	}
	if err := json.Unmarshal(lastSnap, &snap); err != nil {
		t.Fatalf("snap json: %v", err)
	}
	if len(snap.Stories) == 0 {
		t.Fatal("snapshot had no stories")
	}
}

// TestWorkspaceAddSeedsFromDerivedServicePort (sty_0122610a / sty_21a7d16d):
// no explicit endpoint, but a live serve on the global service port with
// matching X-Satelle-Instance → seed once, do not write satelle.local.toml.
func TestWorkspaceAddBootstrapsEndpointFromServicePort(t *testing.T) {
	var snapHits atomic.Int32
	// tempRepo isolates SATELLE_HOME first so CurrentInstanceID matches healthz.
	repo := tempRepo(t)
	wantInst := config.CurrentInstanceID()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			w.Header().Set(HeaderSatelleInstance, wantInst)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		case "/ingest/snapshot":
			snapHits.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}

	// tempRepo already isolated SATELLE_HOME; pin service port to httptest's
	// and clear the hermetic none-switch so the derived endpoint is used.
	t.Setenv(EnvServerEndpoint, "")
	_ = os.Unsetenv(EnvServerEndpoint)
	if err := config.SaveGlobal(config.GlobalConfig{
		Service: config.ServiceConfig{Port: port},
	}); err != nil {
		t.Fatal(err)
	}

	out, err := runRoot(t, "workspace", "add")
	if err != nil {
		t.Fatalf("workspace add: %v\n%s", err, out)
	}
	if strings.Contains(out, "bootstrapped") {
		t.Fatalf("must not write repo [server] endpoint:\n%s", out)
	}
	if !strings.Contains(out, "workspace add: ok") {
		t.Fatalf("expected ok line:\n%s", out)
	}
	if snapHits.Load() != 1 {
		t.Fatalf("snapshot posts = %d, want 1", snapHits.Load())
	}
	localPath := filepath.Join(repo, ".satelle", "satelle.local.toml")
	if _, err := os.Stat(localPath); err == nil {
		t.Fatal("satelle.local.toml must not be written for machine-scope endpoint")
	}
}

// TestWorkspaceAddNoServeSkipsSeedExit0 (sty_5aa08259 AC3): no endpoint and no
// serve → still registers, seed skipped, exit 0.
func TestWorkspaceAddNoServeSkipsSeedExit0(t *testing.T) {
	repo := tempRepo(t)
	// Pin service port to something nothing is listening on.
	if err := config.SaveGlobal(config.GlobalConfig{
		Service: config.ServiceConfig{Port: 1},
	}); err != nil {
		t.Fatal(err)
	}

	out, err := runRoot(t, "workspace", "add")
	if err != nil {
		t.Fatalf("expected exit 0 with seed skipped, got err: %v\n%s", err, out)
	}
	if !strings.Contains(out, "seed skipped") {
		t.Fatalf("expected seed skipped notice:\n%s", out)
	}
	abs, _ := filepath.Abs(repo)
	gc, gerr := config.LoadGlobal()
	if gerr != nil {
		t.Fatal(gerr)
	}
	found := false
	for _, r := range gc.Workspace.Repos {
		if r == abs {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("registration must survive seed skip: %v", gc.Workspace.Repos)
	}
}

// TestWorkspaceAddForeignInstanceSkipsSeed (sty_5aa08259): live serve without
// matching X-Satelle-Instance is not auto-adopted.
func TestWorkspaceAddForeignInstanceSkipsSeed(t *testing.T) {
	var snapHits atomic.Int32
	_ = tempRepo(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			w.Header().Set(HeaderSatelleInstance, "foreign-instance-id")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		case "/ingest/snapshot":
			snapHits.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	t.Setenv(EnvServerEndpoint, "")
	_ = os.Unsetenv(EnvServerEndpoint)
	if err := config.SaveGlobal(config.GlobalConfig{
		Service: config.ServiceConfig{Port: port},
	}); err != nil {
		t.Fatal(err)
	}

	out, err := runRoot(t, "workspace", "add")
	if err != nil {
		t.Fatalf("expected exit 0: %v\n%s", err, out)
	}
	if !strings.Contains(out, "seed skipped") {
		t.Fatalf("expected seed skipped:\n%s", out)
	}
	if snapHits.Load() != 0 {
		t.Fatalf("foreign serve must not receive snapshot, got %d", snapHits.Load())
	}
}

// TestWorkspaceAddEnvNoneDisablesSeed (sty_5aa08259): SATELLE_SERVER_ENDPOINT=none
// disables discovery even when a matching serve answers.
func TestWorkspaceAddEnvNoneDisablesSeed(t *testing.T) {
	var snapHits atomic.Int32
	_ = tempRepo(t)
	wantInst := config.CurrentInstanceID()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			w.Header().Set(HeaderSatelleInstance, wantInst)
			_, _ = w.Write([]byte("ok"))
		case "/ingest/snapshot":
			snapHits.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	_ = config.SaveGlobal(config.GlobalConfig{Service: config.ServiceConfig{Port: port}})
	t.Setenv(EnvServerEndpoint, "none")

	out, err := runRoot(t, "workspace", "add")
	if err != nil {
		t.Fatalf("expected exit 0: %v\n%s", err, out)
	}
	if !strings.Contains(out, "seed skipped") {
		t.Fatalf("expected seed skipped:\n%s", out)
	}
	if snapHits.Load() != 0 {
		t.Fatalf("none must not seed, got %d posts", snapHits.Load())
	}
}

// TestWorkspaceAddRespectsConfiguredEndpoint (sty_0122610a / sty_21a7d16d):
// explicit machine [service] endpoint → no probe, no local.toml write.
func TestWorkspaceAddRespectsConfiguredEndpoint(t *testing.T) {
	var snapHits atomic.Int32
	var healthzHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			healthzHits.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		case "/ingest/snapshot":
			snapHits.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	repo := tempRepo(t)
	if err := config.SaveGlobal(config.GlobalConfig{
		Service: config.ServiceConfig{Endpoint: srv.URL},
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvServerEndpoint, "")
	_ = os.Unsetenv(EnvServerEndpoint)

	out, err := runRoot(t, "workspace", "add")
	if err != nil {
		t.Fatalf("workspace add: %v\n%s", err, out)
	}
	if strings.Contains(out, "bootstrapped") {
		t.Fatalf("must not bootstrap when endpoint configured:\n%s", out)
	}
	if snapHits.Load() != 1 {
		t.Fatalf("snapshot posts = %d, want 1", snapHits.Load())
	}
	if healthzHits.Load() != 0 {
		t.Fatalf("healthz probes = %d, want 0 when endpoint configured", healthzHits.Load())
	}
	localPath := filepath.Join(repo, ".satelle", "satelle.local.toml")
	if _, err := os.Stat(localPath); err == nil {
		t.Fatalf("local.toml must not be created when endpoint already configured")
	}
}

// TestWorkspaceAddPushFailureKeepsRegistration (AC1): 500 after register.
func TestWorkspaceAddPushFailureKeepsRegistration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	repo := tempRepo(t)
	t.Setenv(EnvServerEndpoint, srv.URL)

	out, err := runRoot(t, "workspace", "add")
	if err == nil {
		t.Fatalf("expected push failure, got ok:\n%s", out)
	}
	abs, _ := filepath.Abs(repo)
	gc, gerr := config.LoadGlobal()
	if gerr != nil {
		t.Fatal(gerr)
	}
	found := false
	for _, r := range gc.Workspace.Repos {
		if r == abs {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("registration must survive push failure: %v", gc.Workspace.Repos)
	}
}

// TestWorkspaceRemovePurgesPartition (sty_eb61be02): remove posts /ingest/remove.
func TestWorkspaceRemovePurgesPartition(t *testing.T) {
	var removeHits atomic.Int32
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ingest/remove" && r.Method == http.MethodPost {
			removeHits.Add(1)
			var ev map[string]string
			_ = json.NewDecoder(r.Body).Decode(&ev)
			gotKey = ev["repo_key"]
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	repo := tempRepo(t)
	t.Setenv(EnvServerEndpoint, srv.URL)

	// Register first.
	if out, err := runRoot(t, "workspace", "add"); err != nil {
		// add may try snapshot and fail — still may register. Force register via add with none then set endpoint.
		_ = out
	}
	// Ensure registered.
	abs, _ := filepath.Abs(repo)
	gc, _ := config.LoadGlobal()
	gc.Workspace.AddRepo(abs)
	_ = config.SaveGlobal(gc)

	out, err := runRoot(t, "workspace", "remove", abs)
	if err != nil {
		t.Fatalf("workspace remove: %v\n%s", err, out)
	}
	if removeHits.Load() != 1 {
		t.Fatalf("ingest/remove hits = %d, want 1; out:\n%s", removeHits.Load(), out)
	}
	wantKey := config.RepoKey(abs)
	if gotKey != wantKey {
		t.Fatalf("repo_key = %q, want %q", gotKey, wantKey)
	}
	if !strings.Contains(out, "purged mirror partition") {
		t.Fatalf("expected purge line:\n%s", out)
	}
	gc, _ = config.LoadGlobal()
	for _, r := range gc.Workspace.Repos {
		if r == abs {
			t.Fatalf("still registered after remove: %v", gc.Workspace.Repos)
		}
	}
}

// TestWorkspacePruneUnknownRepoKey (sty_eb61be02 AC5).
func TestWorkspacePruneUnknownRepoKey(t *testing.T) {
	_ = tempRepo(t) // isolate home + empty mirror
	out, err := runRoot(t, "workspace", "prune", "no-such-repo-key-zzzz")
	if err == nil {
		t.Fatalf("expected error for unknown key, got:\n%s", out)
	}
	combined := out + err.Error()
	if !strings.Contains(combined, "unknown repo_key") {
		t.Fatalf("expected unknown repo_key message:\n%s", combined)
	}
}

// TestWorkspacePruneRemovesOrphan (sty_eb61be02): local delete when path gone.
func TestWorkspacePruneRemovesOrphan(t *testing.T) {
	_ = tempRepo(t)
	ms, err := mirrorOpenTest(t)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if _, err := ms.TouchPartition(ctx, "orphan-deadbeef", "orphan", time.Now()); err != nil {
		t.Fatal(err)
	}
	_ = ms.Close()

	out, err := runRoot(t, "workspace", "prune", "orphan-deadbeef")
	if err != nil {
		t.Fatalf("prune: %v\n%s", err, out)
	}
	if !strings.Contains(out, "pruned") {
		t.Fatalf("expected pruned:\n%s", out)
	}
	ms2, err := mirrorOpenTest(t)
	if err != nil {
		t.Fatal(err)
	}
	defer ms2.Close()
	parts, err := ms2.ListPartitions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range parts {
		if p.RepoKey == "orphan-deadbeef" {
			t.Fatal("partition still present after prune")
		}
	}
}

func mirrorOpenTest(t *testing.T) (*mirror.Store, error) {
	t.Helper()
	return mirror.Open(mirror.DefaultPath(config.GlobalDir()))
}
