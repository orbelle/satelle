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
