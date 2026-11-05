package hosted

import (
	"context"
	"errors"
	"testing"

	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

// memStore is an in-memory Store.
type memStore struct {
	cred  Credential
	set   bool
	saves int
}

func (m *memStore) Load(string) (Credential, error) {
	if !m.set {
		return Credential{}, ErrNoCredential
	}
	return m.cred, nil
}
func (m *memStore) Save(c Credential) error     { m.cred, m.set = c, true; m.saves++; return nil }
func (m *memStore) Delete(string) error         { m.set = false; return nil }
func (m *memStore) with(c Credential) *memStore { m.cred, m.set = c, true; return m }

func dialT(t *testing.T, server string, store Store) *Client {
	t.Helper()
	c, err := Dial(server, store, Options{HTTP: guardedClient(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func authsOf(srv *fakeserver.Server) []string {
	var out []string
	for _, c := range srv.Calls() {
		out = append(out, c.Auth)
	}
	return out
}

// The stored login's access token is the bearer of every call.
func TestStoredTokenIsTheBearer(t *testing.T) {
	isolateHome(t)
	t.Setenv(SessionTokenEnv, "")
	srv := fakeserver.Start(t, "access-1")
	c := dialT(t, srv.URL, (&memStore{}).with(Credential{ServerURL: srv.URL, AccessToken: "access-1"}))
	me, err := c.Account.GetMe(context.Background(), &satellepb.GetMeRequest{})
	if err != nil || me.GetEmail() != "ada@example.com" {
		t.Fatalf("GetMe = %v, %v", me, err)
	}
	if got := authsOf(srv); len(got) != 1 || got[0] != "access-1" {
		t.Errorf("bearers = %v, want [access-1]", got)
	}
}

// SATELLE_TOKEN replaces the stored login and is never refreshed.
func TestSessionTokenWinsAndIsNotRefreshed(t *testing.T) {
	isolateHome(t)
	p := newFakeProvider(t)
	srv := fakeserver.Start(t, "session-tok")
	if err := (FileStore{}).Save(p.credential(srv.URL)); err != nil {
		t.Fatal(err)
	}
	t.Setenv(SessionTokenEnv, "session-tok")
	c := dialT(t, srv.URL, DefaultStore())
	if _, err := c.Account.GetMe(context.Background(), &satellepb.GetMeRequest{}); err != nil {
		t.Fatal(err)
	}
	if got := authsOf(srv); got[0] != "session-tok" {
		t.Errorf("bearer = %q, want the session token", got[0])
	}
	srv.RevokeToken("session-tok")
	_, err := c.Account.GetMe(context.Background(), &satellepb.GetMeRequest{})
	if !errors.Is(err, ErrSessionTokenRefused) {
		t.Errorf("refused session token = %v, want ErrSessionTokenRefused", err)
	}
	if n := len(p.refreshes()); n != 0 {
		t.Errorf("a session token was refreshed %d time(s)", n)
	}
}

// UNAUTHENTICATED refreshes the stored login once through the OAuth token
// endpoint, persists the rotated pair, and retries the call.
func TestUnauthenticatedRefreshesOnceAndRetries(t *testing.T) {
	isolateHome(t)
	t.Setenv(SessionTokenEnv, "")
	p := newFakeProvider(t)
	srv := fakeserver.Start(t, "access-2")
	store := (&memStore{}).with(p.credential(srv.URL))
	c := dialT(t, srv.URL, store)
	if _, err := c.Projects.ListProjects(context.Background(), &satellepb.ListProjectsRequest{}); err != nil {
		t.Fatalf("ListProjects after refresh: %v", err)
	}
	p.wantRefresh(t, 0, "refresh-1")
	if store.cred.AccessToken != "access-2" || store.cred.RefreshToken != "refresh-2" || store.cred.TokenEndpoint != p.tokenURL() {
		t.Errorf("persisted %+v, want the rotated pair with the token endpoint kept", store.cred)
	}
	if got := authsOf(srv); len(got) != 2 || got[0] != "access-1" || got[1] != "access-2" {
		t.Errorf("bearers = %v, want the refused token then the rotated one", got)
	}

	// A second refusal after a refresh is not refreshed again.
	srv.RevokeToken("access-2")
	_, err := c.Projects.ListProjects(context.Background(), &satellepb.ListProjectsRequest{})
	if !errors.Is(err, ErrLoginRequired) {
		t.Errorf("err = %v, want ErrLoginRequired", err)
	}
	if n := len(p.refreshes()); n != 2 {
		t.Errorf("refreshes = %d, want one per refused call", n)
	}
}

// With no credential nothing is sent.
func TestNoCredentialSendsNothing(t *testing.T) {
	isolateHome(t)
	t.Setenv(SessionTokenEnv, "")
	srv := fakeserver.Start(t)
	c := dialT(t, srv.URL, &memStore{})
	if _, err := c.Account.GetMe(context.Background(), &satellepb.GetMeRequest{}); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("err = %v, want ErrLoginRequired", err)
	}
	if n := len(srv.Calls()); n != 0 {
		t.Errorf("%d call(s) sent without a credential", n)
	}
}

func TestGRPCTarget(t *testing.T) {
	for in, want := range map[string]struct {
		addr   string
		secure bool
	}{
		"https://satelle.dev":   {"satelle.dev:443", true},
		"https://x.test:8443":   {"x.test:8443", true},
		"http://127.0.0.1:9000": {"127.0.0.1:9000", false},
		"127.0.0.1:9000":        {"127.0.0.1:9000", false},
		"localhost:9000":        {"localhost:9000", false},
		"grpc.example.test:443": {"grpc.example.test:443", true},
	} {
		addr, secure, err := grpcTarget(in)
		if err != nil || addr != want.addr || secure != want.secure {
			t.Errorf("grpcTarget(%q) = %q, %v, %v; want %q, %v", in, addr, secure, err, want.addr, want.secure)
		}
	}
}
