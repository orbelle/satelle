package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted/syncpb"
)

// areaScopes parses `sync scopes` tabwriter output into area -> scope,
// ignoring indented "shared: <path>" detail lines.
func areaScopes(out string) map[string]string {
	scopes := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 2 {
			scopes[fields[0]] = fields[1]
		}
	}
	return scopes
}

// TestSyncScopesDefaultsToLocal: with no [sync] table, every area prints local
// — the safe default, nothing syncs without opt-in (AC1).
func TestSyncScopesDefaultsToLocal(t *testing.T) {
	tempRepo(t)
	out, err := runRoot(t, "sync", "scopes")
	if err != nil {
		t.Fatalf("sync scopes: %v\n%s", err, out)
	}
	scopes := areaScopes(out)
	for _, area := range []string{"documents", "workflows", "principles", "skills", "constitution", "agents", "tasks", "stories", "ledger", "executions"} {
		if scopes[area] != "local" {
			t.Errorf("area %q scope = %q, want local (full output:\n%s)", area, scopes[area], out)
		}
	}
}

// TestSyncDefaultAllLocalNothingToSync: bare `satelle sync` with no [sync] table
// prints one "nothing to sync" line and contacts no server (default-action AC2).
func TestSyncDefaultAllLocalNothingToSync(t *testing.T) {
	tempRepo(t)
	out, err := runRoot(t, "sync")
	if err != nil {
		t.Fatalf("sync: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Nothing to sync") {
		t.Fatalf("expected all-local nothing-to-sync message, got: %q", out)
	}
	// No per-verb skip noise leaks through the aggregate path.
	if strings.Contains(out, "scope is local — skipping") || strings.Contains(out, "every work-state area is local") {
		t.Errorf("all-local sync leaked a per-verb skip message:\n%s", out)
	}
}

type rehydrateSnapGRPC struct {
	syncpb.UnimplementedSyncServer
	record []byte
}

func (s *rehydrateSnapGRPC) Snapshot(_ context.Context, _ *syncpb.SnapshotRequest) (*syncpb.SnapshotResponse, error) {
	return &syncpb.SnapshotResponse{Items: []*syncpb.WorkItem{{
		Id: "sty_rehy1", Kind: "story", Type: "stories", Status: "backlog",
		Title: "Rehydrated", Origin: "cli-sync", Record: s.record,
	}}}, nil
}

// TestSyncScopesInvalidScopeRefuses: a per-area key refuses the command whatever
// its value, rather than silently coercing to local.
func TestSyncScopesInvalidScopeRefuses(t *testing.T) {
	repo := tempRepo(t)
	cfgPath := filepath.Join(repo, ".satelle", "satelle.toml")
	committed := "[sync]\ntasks = \"sometimes\"\n"
	if err := os.WriteFile(cfgPath, []byte(committed), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runRoot(t, "sync", "scopes"); err == nil {
		t.Error("sync scopes with an invalid configured scope did not error")
	}
}

// TestSyncScopesListsSharedFiles: under a personal scope a file with
// `shared: true` frontmatter is listed by the command (AC2, AC4).
func TestSyncScopesListsSharedFiles(t *testing.T) {
	repo := tempRepo(t)
	signInTo(t, config.DefaultHostedServer)
	cfgPath := filepath.Join(repo, ".satelle", "satelle.toml")
	if err := os.WriteFile(cfgPath, []byte("[sync]\nproject = \"probe\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	skillsDir := filepath.Join(repo, ".satelle", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	shared := "---\ntype: skill\nshared: true\n---\nbody\n"
	unshared := "---\ntype: skill\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(skillsDir, "shared-one.md"), []byte(shared), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "private-one.md"), []byte(unshared), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runRoot(t, "sync", "scopes")
	if err != nil {
		t.Fatalf("sync scopes: %v\n%s", err, out)
	}
	if !strings.Contains(out, "shared-one.md") {
		t.Errorf("sync scopes should list shared-one.md as shared:\n%s", out)
	}
	if strings.Contains(out, "private-one.md") {
		t.Errorf("sync scopes should NOT list private-one.md:\n%s", out)
	}
}

// TestSyncRehydrateLocalOnly: when the account resolves the bound project local
// (not a member of its team), rehydrate runs each step as a local skip / empty
// deploy and exits 0 (order:4 AC4).
func TestSyncRehydrateLocalOnly(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	repo := tempRepo(t)
	useScope(t, kindForeign)
	// Bound project so deploy can resolve; fake server returns empty manifest.
	cfgPath := filepath.Join(repo, ".satelle", "satelle.toml")
	if err := os.WriteFile(cfgPath, []byte("[hosted]\nproject = \"probe\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/workspaces", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]string{
			{"id": "ws-personal", "kind": "personal", "name": "personal"},
		})
	})
	mux.HandleFunc("GET /api/v1/projects/{project}/config", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]any{})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	seedCred(t, ts.URL)

	out, err := runRoot(t, "sync", "rehydrate", "--server", ts.URL)
	if err != nil {
		t.Fatalf("rehydrate: %v\n%s", err, out)
	}
	for _, want := range []string{"rehydrate: config deploy", "rehydrate: documents pull", "rehydrate: workstate pull", "rehydrate: done"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "scope: local — "+testAccountEmail+" is not a member of project probe's team") {
		t.Errorf("expected the resolver's local reason:\n%s", out)
	}
	if !strings.Contains(out, "settings: not restored") {
		t.Errorf("expected truthful done summary:\n%s", out)
	}
}

// rehydrateEmptyTree binds a bare tree to project probe and rehydrates it from a
// server whose settings snapshot is hostedToml, returning rehydrate's output and
// the satelle.toml it left. The story the server holds must be restored.
func rehydrateEmptyTree(t *testing.T, hostedToml string) (out, gotToml string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("SATELLE_HOME", t.TempDir()) // home-keyed runtime (sty_c36c211f)
	// Bare tree: no satelle.toml until bind.
	repo := t.TempDir()
	t.Chdir(repo)
	_ = os.Unsetenv("SATELLE_CONFIG")

	// Seed agents so store open after deploy succeeds if deploy writes agents.
	// Fake server serves config (satelle.toml with personal scopes + agents),
	// empty docs changes, and workstate items/ledger from a prior push shape.
	const project = "probe"
	agentsToml := "[executor]\nharness = \"in-loop\"\n"
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/workspaces", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]string{
			{"id": "ws-personal", "kind": "personal", "name": "personal"},
		})
	})
	mux.HandleFunc("GET /api/v1/projects/{project}/config", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"path": "satelle.toml", "version": 1},
			{"path": "agents.toml", "version": 1},
		})
	})
	mux.HandleFunc("GET /api/v1/projects/{project}/config/{path...}", func(w http.ResponseWriter, r *http.Request) {
		p := r.PathValue("path")
		switch p {
		case "satelle.toml":
			w.Header().Set("ETag", "sha-toml")
			_, _ = w.Write([]byte(hostedToml))
		case "agents.toml":
			w.Header().Set("ETag", "sha-agents")
			_, _ = w.Write([]byte(agentsToml))
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("GET /api/v1/projects/{project}/documents", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}, "cursor": ""})
	})
	itemRec := map[string]any{
		"id": "sty_rehy1", "kind": "story", "status": "backlog", "title": "Rehydrated",
		"body": "from hosted", "acceptance_criteria": "1. ok",
	}
	recBytes, _ := json.Marshal(itemRec)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	attachTestGRPC(t, &rehydrateSnapGRPC{record: recBytes})
	seedCred(t, ts.URL)

	// Bind creates minimal toml.
	cmd, buf := testCmd()
	if err := runProjectBind(cmd, project); err != nil {
		t.Fatalf("bind: %v\n%s", err, buf.String())
	}
	// Point config at the bound file for subsequent CLI.
	t.Setenv("SATELLE_CONFIG", filepath.Join(repo, ".satelle", "satelle.toml"))

	out, err := runRoot(t, "sync", "rehydrate", "--server", ts.URL)
	if err != nil {
		t.Fatalf("rehydrate: %v\n%s", err, out)
	}
	for _, want := range []string{"rehydrate: config deploy", "rehydrate: documents pull", "rehydrate: workstate pull", "rehydrate: done"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	// AC6 (sty_2cb4e1f6): rehydrate's work-state pull names its pass; this server
	// does not page, so it is a single page and issues no token.
	if !strings.Contains(out, "(full pass, 1 page(s))") {
		t.Errorf("rehydrate's pull should name its pass kind and page count:\n%s", out)
	}
	// The settings snapshot is restored; the scope is the account's.
	if !strings.Contains(out, "settings: satelle.toml restored") {
		t.Errorf("expected settings restored line:\n%s", out)
	}
	if !strings.Contains(out, "[sync] project preserved") {
		t.Errorf("expected binding preservation line:\n%s", out)
	}
	if !strings.Contains(out, "rehydrate: done — scope: personal") {
		t.Errorf("rehydrate's closing line must name the account's scope:\n%s", out)
	}
	// On-disk satelle.toml preserves the bind project.
	gotBytes, rerr := os.ReadFile(filepath.Join(repo, ".satelle", "satelle.toml"))
	if rerr != nil {
		t.Fatalf("read satelle.toml: %v", rerr)
	}
	if !strings.Contains(string(gotBytes), `project = "probe"`) {
		t.Errorf("local binding not preserved: %s", gotBytes)
	}
	// Story materialised.
	got, gerr := runRoot(t, "story", "get", "sty_rehy1")
	if gerr != nil {
		t.Fatalf("story get after rehydrate: %v\n%s\nrehydrate out:\n%s", gerr, got, out)
	}
	if !strings.Contains(got, "Rehydrated") {
		t.Errorf("story not restored: %s", got)
	}
	return out, string(gotBytes)
}

// TestSyncRehydrateEmptyTreeHappyPath: bind-created toml + deploy restores the
// settings snapshot, the documents path and the workstate items (order:4 AC5
// hermetic).
func TestSyncRehydrateEmptyTreeHappyPath(t *testing.T) {
	_, gotToml := rehydrateEmptyTree(t, "[sync]\nstale_after = \"6h\"\n\n[hosted]\nproject = \"probe\"\n")
	if !strings.Contains(gotToml, `stale_after = "6h"`) {
		t.Errorf("settings snapshot not restored: %s", gotToml)
	}
}

// TestSyncRehydrateIgnoresHostedSyncScope (AC6): a hosted satelle.toml whose
// [sync] block still carries scope keys — here saying local — decides nothing.
// The account resolves personal, so the work state is still restored; the keys
// are dropped from the restored file and named, and the file loads.
func TestSyncRehydrateIgnoresHostedSyncScope(t *testing.T) {
	out, gotToml := rehydrateEmptyTree(t, "[sync]\nall = \"local\"\nstories = \"local\"\nstale_after = \"6h\"\n\n[hosted]\nproject = \"probe\"\n")
	if strings.Contains(gotToml, `all = `) || strings.Contains(gotToml, `stories = `) {
		t.Errorf("the hosted scope keys were restored: %s", gotToml)
	}
	if !strings.Contains(gotToml, `stale_after = "6h"`) {
		t.Errorf("the rest of the hosted [sync] block was not restored: %s", gotToml)
	}
	if !strings.Contains(out, "removed [sync] scope keys all, stories") {
		t.Errorf("rehydrate must name the scope keys it dropped:\n%s", out)
	}
	if _, _, err := config.Load(""); err != nil {
		t.Errorf("the restored satelle.toml does not load: %v", err)
	}
}

// TestSyncScopesReportsWithheldFiles (sty_698e70b6 AC5): scopes lists withheld
// .local files under non-local areas.
func TestSyncScopesReportsWithheldFiles(t *testing.T) {
	repo := tempRepo(t)
	signInTo(t, config.DefaultHostedServer)
	cfgPath := filepath.Join(repo, ".satelle", "satelle.toml")
	if err := os.WriteFile(cfgPath, []byte("[sync]\nproject = \"probe\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	skillsDir := filepath.Join(repo, ".satelle", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "keep.md"), []byte("---\ntype: skill\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "secret.local.md"), []byte("SECRET\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runRoot(t, "sync", "scopes")
	if err != nil {
		t.Fatalf("sync scopes: %v\n%s", err, out)
	}
	if !strings.Contains(out, "withheld:") || !strings.Contains(out, "secret.local.md") {
		t.Errorf("scopes should list withheld secret.local.md:\n%s", out)
	}
}

// TestSyncNoopMakesNoContentUploads (sty_88e83180 AC7): after a successful
// workstate push, a second workstate push issues no POST. Config/docs legs are
// covered by their own skip tests; this pins the workstate half of the no-op.
func TestSyncNoopMakesNoContentUploads(t *testing.T) {
	ts, f := newFakeWorkstateServer(t)
	seedCred(t, ts.URL)
	workstateRepo(t, "[sync]\n\n[hosted]\nproject = \"probe\"\n")
	out, err := runRoot(t, "story", "create",
		"--title", "Noop", "--body", "b", "--acceptance", "1. ok", "--category", "chore")
	if err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	if out, err = runRoot(t, "sync", "workstate", "push", "--server", ts.URL); err != nil {
		t.Fatalf("first: %v\n%s", err, out)
	}
	n1 := f.postCount("probe")
	if n1 == 0 {
		t.Fatal("first push posted nothing")
	}
	if out, err = runRoot(t, "sync", "workstate", "push", "--server", ts.URL); err != nil {
		t.Fatalf("second: %v\n%s", err, out)
	}
	if f.postCount("probe") != n1 {
		t.Fatalf("noop workstate posts = %d, want %d", f.postCount("probe"), n1)
	}
}
