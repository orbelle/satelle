package hosted

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

type loopbackOnly struct {
	t    *testing.T
	base http.RoundTripper
}

func (l loopbackOnly) RoundTrip(req *http.Request) (*http.Response, error) {
	if !isLoopbackURL(req.URL.String()) {
		l.t.Errorf("test request left the loopback: %s", req.URL)
		return nil, fmt.Errorf("non-loopback request refused: %s", req.URL)
	}
	if l.base == nil {
		return http.DefaultTransport.RoundTrip(req)
	}
	return l.base.RoundTrip(req)
}

func guardedClient(t *testing.T) *http.Client {
	t.Helper()
	return &http.Client{Transport: loopbackOnly{t: t}}
}

func requireLoopback(t *testing.T, urls ...string) {
	t.Helper()
	for _, u := range urls {
		if !isLoopbackURL(u) {
			t.Fatalf("fake URL %q is not loopback", u)
		}
	}
}

func otherHost(t *testing.T, serverURL string) string {
	t.Helper()
	u, err := url.Parse(serverURL)
	if err != nil {
		t.Fatal(err)
	}
	u.Host = "localhost:" + u.Port()
	requireLoopback(t, u.String())
	return u.String()
}

type fakeProvider struct {
	ts *httptest.Server

	mu    sync.Mutex
	forms []url.Values
	deny  bool
}

func newFakeProvider(t *testing.T) *fakeProvider {
	t.Helper()
	p := &fakeProvider{}
	p.ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		p.mu.Lock()
		p.forms = append(p.forms, r.PostForm)
		n, deny := len(p.forms), p.deny
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if deny {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  fmt.Sprintf("access-%d", n+1),
			"refresh_token": fmt.Sprintf("refresh-%d", n+1),
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	}))
	t.Cleanup(p.ts.Close)
	requireLoopback(t, p.ts.URL)
	return p
}

func (p *fakeProvider) tokenURL() string { return p.ts.URL + "/token" }

func (p *fakeProvider) credential(server string) Credential {
	return Credential{
		ServerURL: server, AccessToken: "access-1", RefreshToken: "refresh-1",
		TokenEndpoint: p.tokenURL(), ClientID: "client-abc", Resource: "https://api.example/",
	}
}

func (p *fakeProvider) refreshes() []url.Values {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]url.Values(nil), p.forms...)
}

func (p *fakeProvider) wantRefresh(t *testing.T, i int, token string) {
	t.Helper()
	got := p.refreshes()
	if len(got) <= i {
		t.Fatalf("provider saw %d refresh(es), want index %d", len(got), i)
	}
	f := got[i]
	if f.Get("grant_type") != "refresh_token" || f.Get("refresh_token") != token {
		t.Errorf("refresh %d = %v, want refresh_token %q", i, f, token)
	}
	if f.Get("client_id") != "client-abc" || f.Get("resource") != "https://api.example/" {
		t.Errorf("refresh %d lost client id or resource: %v", i, f)
	}
}

func noPath(t *testing.T, mux *http.ServeMux, patterns ...string) {
	t.Helper()
	for _, p := range patterns {
		mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {
			t.Errorf("satelle server must not receive %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusInternalServerError)
		})
	}
}
