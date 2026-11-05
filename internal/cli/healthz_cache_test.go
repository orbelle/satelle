package cli

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/testutil"
)

// countingHealthz is a service whose /healthz answers ok and counts each probe.
func countingHealthz(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			hits.Add(1)
			w.Header().Set(HeaderSatelleInstance, config.CurrentInstanceID())
			_, _ = w.Write([]byte("ok"))
		case "/ingest/snapshot", "/ingest/change":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// A port with nothing listening is not recorded, so a service started a moment
// later is seen by the very next run rather than after the TTL.
func TestHealthzCacheDoesNotRecordAnUnansweredProbe(t *testing.T) {
	testutil.IsolateHome(t)
	srv, hits := countingHealthz(t)
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL + "/healthz"
	dead.Close()
	if healthzOK(deadURL) {
		t.Fatal("a closed port read as live")
	}
	if healthzOK(deadURL) {
		t.Fatal("a closed port read as live on the second run")
	}
	if !healthzOK(srv.URL + "/healthz") {
		t.Fatal("live service read as not answering")
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("probes = %d, want 1", n)
	}
}

// sty_a003a459 AC3: satelled's reseed child runs `workspace add` with
// SATELLE_SERVER_ENDPOINT set (serve.Reconciler.Endpoint), and posts its
// snapshot there without one /healthz probe.
func TestWorkspaceAddWithEndpointEnvProbesNothing(t *testing.T) {
	srv, hits := countingHealthz(t)
	tempRepo(t)
	t.Setenv(EnvServerEndpoint, srv.URL)
	if out, err := runRoot(t, "workspace", "add"); err != nil {
		t.Fatalf("workspace add: %v\n%s", err, out)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("the reseed child's workspace add sent %d /healthz probes, want 0", n)
	}
}
