package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/syncstate"
)

// The bound-repo fixture the work-state push/pull tests share. The hosted server
// has no hold surface any more (sty_40853d02): noHoldRoutes answers every hold
// route (checkout, release, takeover, checkout-log, the per-item GET) by failing
// the test, so any code path that still dials one is caught wherever the
// fixture is used.

const (
	otherLoc   = "loc_other_aaaaaaaa"
	holdNoGit  = "[sync]\nhold_unpushed = false\n\n[hosted]\nproject = \"probe\"\n"
	pullSyncTm = "[sync]\n\n[hosted]\nproject = \"probe\"\n"
)

// noHoldRoutes is the route set of a hosted server without holds: location
// registration answers, and a request to any hold route fails the test.
func noHoldRoutes(t *testing.T) func(mux *http.ServeMux, f *fakeWorkstateServer) {
	return func(mux *http.ServeMux, f *fakeWorkstateServer) {
		mux.HandleFunc("POST /api/v1/locations", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"id":"ok"}`))
		})
		mux.HandleFunc("/api/v1/projects/{project}/workstate/items/", func(w http.ResponseWriter, r *http.Request) {
			t.Errorf("a hold route was called: %s %s", r.Method, r.URL.Path)
			http.Error(w, "no hold surface", http.StatusGone)
		})
	}
}

// holdFixture is a bound repo whose route lets `story set --status in_progress`
// engage with no reviewer, against the fake hosted server.
type holdFixture struct {
	ts   *httptest.Server
	f    *fakeWorkstateServer
	repo string
	loc  string
	// home and cfg are the machine's identity (SATELLE_HOME, SATELLE_CONFIG): a
	// second machine on the same server is a second fixture, and use switches to it.
	home, cfg string
	// session, when set, is the SATELLE_SESSION the machine runs under.
	session string
}

func newHoldFixture(t *testing.T, syncToml string) *holdFixture {
	t.Helper()
	ts, f := newFakeWorkstateServerWith(t, noHoldRoutes(t))
	return newHoldMachine(t, ts, f, syncToml)
}

// newHoldMachine sets up one more machine — its own home, repo and location —
// against the server ts. It leaves the process on that machine.
func newHoldMachine(t *testing.T, ts *httptest.Server, f *fakeWorkstateServer, syncToml string) *holdFixture {
	t.Helper()
	seedCred(t, ts.URL)
	repo := workstateRepo(t, syncToml)
	t.Chdir(repo)
	writeRoute(t, filepath.Join(repo, ".satelle", "workflows"),
		`["*"]
obligations = ["raised", "coded", "closed"]
`,
		`[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
agent = "executor"
requires = ["raised"]

[closed]
status = "done"
terminal = true
requires = ["coded"]
`)
	// The server this machine dials is a machine setting, not a repo one.
	global := "[hosted]\nserver = \"" + ts.URL + "\"\n"
	if err := os.WriteFile(config.GlobalConfigPath(), []byte(global), 0o644); err != nil {
		t.Fatal(err)
	}
	loc, err := hosted.LocationID(repo)
	if err != nil {
		t.Fatal(err)
	}
	return &holdFixture{ts: ts, f: f, repo: repo, loc: loc, home: os.Getenv("SATELLE_HOME"), cfg: os.Getenv("SATELLE_CONFIG")}
}

// use switches the process to this machine: its home, config and working tree.
func (fx *holdFixture) use(t *testing.T) {
	t.Helper()
	t.Setenv("SATELLE_HOME", fx.home)
	t.Setenv("SATELLE_CONFIG", fx.cfg)
	t.Setenv(config.SessionEnv, fx.session)
	t.Chdir(fx.repo)
}

func (fx *holdFixture) pull(t *testing.T) string {
	t.Helper()
	out, err := runRoot(t, "sync", "workstate", "pull", "--server", fx.ts.URL)
	if err != nil {
		t.Fatalf("pull: %v\n%s", err, out)
	}
	return out
}

func engageStory(t *testing.T, id string) (string, error) {
	t.Helper()
	return runRoot(t, "story", "set", id, "--status", "in_progress")
}

func (fx *holdFixture) status(t *testing.T, id string) string {
	t.Helper()
	out, err := runRoot(t, "story", "get", id)
	if err != nil {
		t.Fatalf("get %s: %v\n%s", id, err, out)
	}
	return jsonField(t, out, "status")
}

func (fx *holdFixture) push(t *testing.T) string {
	t.Helper()
	out, err := runRoot(t, "sync", "workstate", "push", "--server", fx.ts.URL)
	if err != nil {
		t.Fatalf("push must exit zero: %v\n%s", err, out)
	}
	return out
}

func snapshotIn(t *testing.T, fx *holdFixture) string {
	t.Helper()
	out, err := runRoot(t, "sync", "workstate", "snapshot", "--server", fx.ts.URL)
	if err != nil {
		t.Fatalf("snapshot: %v\n%s", err, out)
	}
	return out
}

func (fx *holdFixture) conflicts(t *testing.T) map[string]syncstate.StoryConflict {
	t.Helper()
	st, _, err := syncstate.Load(config.GlobalDir(), fx.repo)
	if err != nil {
		t.Fatal(err)
	}
	return st.StoryConflicts
}
