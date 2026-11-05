package hosted

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

)

const testSessionToken = "sat_pat_session_secret_value"

// sessionEnv isolates the home, seeds a credentials file for a DIFFERENT login
// (a stale stored login must never be used while the token is set), sets
// SATELLE_TOKEN, and returns the EnvStore plus the credentials file path.
func sessionEnv(t *testing.T, server string) (Store, string) {
	t.Helper()
	isolateHome(t)
	credPath, err := CredentialsPath()
	if err != nil {
		t.Fatal(err)
	}
	file := FileStore{Path: credPath}
	if err := file.Save(Credential{ServerURL: server, AccessToken: "stale-login", RefreshToken: "stale-refresh"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(SessionTokenEnv, testSessionToken)
	return EnvStore{File: file}, credPath
}

func TestEnvStoreSessionOverridesFileAndNeverPersists(t *testing.T) {
	store, credPath := sessionEnv(t, "http://hosted.example")
	before, _ := os.ReadFile(credPath)
	cred, err := store.Load("http://hosted.example/")
	if err != nil {
		t.Fatal(err)
	}
	if !cred.IsSession() || cred.AccessToken != testSessionToken || cred.RefreshToken != "" {
		t.Fatalf("session credential = %+v", cred)
	}
	if err := store.Save(Credential{ServerURL: "http://hosted.example", AccessToken: "a", RefreshToken: "r"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete("http://hosted.example"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(credPath)
	if string(before) != string(after) {
		t.Fatalf("credentials file changed under a session token:\n%s\n---\n%s", before, after)
	}
}

func TestEnvStoreUnsetDelegatesToFile(t *testing.T) {
	isolateHome(t)
	t.Setenv(SessionTokenEnv, "")
	credPath, _ := CredentialsPath()
	file := FileStore{Path: credPath}
	if err := file.Save(Credential{ServerURL: "http://hosted.example", AccessToken: "a", RefreshToken: "r"}); err != nil {
		t.Fatal(err)
	}
	cred, err := (EnvStore{File: file}).Load("http://hosted.example")
	if err != nil || cred.AccessToken != "a" || cred.IsSession() {
		t.Fatalf("Load = %+v, %v", cred, err)
	}
}

func TestSessionTokenIsBearerOnREST(t *testing.T) {
	var got string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("content"))
	}))
	t.Cleanup(ts.Close)
	store, _ := sessionEnv(t, ts.URL)
	c := NewClient(ts.URL, store, ts.Client())
	if _, _, err := c.ConfigFileContent(context.Background(), "proj", "a.md", 0); err != nil {
		t.Fatal(err)
	}
	if got != "Bearer "+testSessionToken {
		t.Fatalf("Authorization = %q", got)
	}
}

func TestSessionTokenIsBearerOnGRPCApply(t *testing.T) {
	store, _ := sessionEnv(t, "http://hosted.example")
	stub := &stubSync{}
	c := newTestGRPCClient(t, stub, store, fatalHTTP(t))
	if _, err := c.Apply(context.Background(), "probe", WorkstateIngest{}); err != nil {
		t.Fatal(err)
	}
	if len(stub.auths) != 1 || stub.auths[0] != "Bearer "+testSessionToken {
		t.Fatalf("auth = %v", stub.auths)
	}
}

func TestSessionTokenRESTRefusedIsNotRefreshed(t *testing.T) {
	var tokenHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) { tokenHits.Add(1) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", http.StatusUnauthorized) })
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	store, credPath := sessionEnv(t, ts.URL)
	before, _ := os.ReadFile(credPath)
	c := NewClient(ts.URL, store, ts.Client())
	c.SetLocation("loc_session_test_aaaa")
	_, err := c.RegisterLocationPrincipal(context.Background(), "x")
	if !errors.Is(err, ErrSessionTokenRefused) {
		t.Fatalf("err = %v, want ErrSessionTokenRefused", err)
	}
	if !strings.Contains(err.Error(), "refused") || !strings.Contains(err.Error(), "revoked") {
		t.Errorf("message does not say the token was refused: %v", err)
	}
	if tokenHits.Load() != 0 {
		t.Errorf("refresh grant hit %d times", tokenHits.Load())
	}
	after, _ := os.ReadFile(credPath)
	if string(before) != string(after) {
		t.Errorf("credentials file changed")
	}
}

func TestSessionTokenGRPCRefusedIsNotRefreshed(t *testing.T) {
	store, credPath := sessionEnv(t, "http://hosted.example")
	before, _ := os.ReadFile(credPath)
	stub := &stubSync{fail: []error{status.Error(codes.Unauthenticated, "bad token")}}
	c := newTestGRPCClient(t, stub, store, fatalHTTP(t))
	_, err := c.Apply(context.Background(), "probe", WorkstateIngest{})
	if !errors.Is(err, ErrSessionTokenRefused) {
		t.Fatalf("err = %v, want ErrSessionTokenRefused", err)
	}
	if stub.refreshes != 0 || stub.applies != 1 {
		t.Errorf("refreshes=%d applies=%d, want 0 and 1", stub.refreshes, stub.applies)
	}
	after, _ := os.ReadFile(credPath)
	if string(before) != string(after) {
		t.Errorf("credentials file changed")
	}
}

func TestSessionTokenNotFoundIsScope(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(ts.Close)
	store, _ := sessionEnv(t, ts.URL)
	c := NewClient(ts.URL, store, ts.Client())
	c.SetLocation("loc_session_test_aaaa")
	if _, err := c.RegisterLocationPrincipal(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "not in this session token's scope") {
		t.Errorf("REST 404 err = %v", err)
	}

	stub := &stubSync{fail: []error{status.Error(codes.NotFound, "no such project")}}
	g := newTestGRPCClient(t, stub, store, fatalHTTP(t))
	if _, err := g.Apply(context.Background(), "other", WorkstateIngest{}); err == nil || !strings.Contains(err.Error(), "not in this session token's scope") {
		t.Errorf("gRPC NotFound err = %v", err)
	}
}

func TestSessionPrincipalLearnedFromRegistrationAndCachedByFingerprint(t *testing.T) {
	withLocationState(t)
	var meHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/me", func(w http.ResponseWriter, r *http.Request) { meHits.Add(1) })
	mux.HandleFunc("POST /api/v1/locations", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "loc", "user_id": "u_123"})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	store, _ := sessionEnv(t, ts.URL)
	c := NewClient(ts.URL, store, ts.Client())

	cred, _ := store.Load(ts.URL)
	if _, known := SessionPrincipal(cred); known {
		t.Fatal("principal known before resolution")
	}
	id, err := ResolveSessionPrincipal(context.Background(), c, t.TempDir(), ts.URL)
	if err != nil || id != "u_123" {
		t.Fatalf("ResolveSessionPrincipal = %q, %v", id, err)
	}
	if got, known := SessionPrincipal(cred); !known || got != "u_123" {
		t.Fatalf("SessionPrincipal = %q, %v", got, known)
	}
	// A different token never resolves to the cached principal.
	t.Setenv(SessionTokenEnv, "sat_pat_another_token")
	other, _ := store.Load(ts.URL)
	if _, known := SessionPrincipal(other); known {
		t.Error("a different token fingerprint resolved to the earlier principal")
	}
	// The cache holds the fingerprint, never the token.
	p, _ := SessionIdentityPath()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), testSessionToken) || !strings.Contains(string(b), SessionFingerprint(cred)) {
		t.Errorf("session-identity file leaks the token or lacks the fingerprint:\n%s", b)
	}
	if filepath.Base(p) == "credentials.toml" {
		t.Error("session identity shares the credentials file")
	}
	if meHits.Load() != 0 {
		t.Errorf("/api/v1/me hit %d times", meHits.Load())
	}
}
