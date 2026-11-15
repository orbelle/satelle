package hosted

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	fakeResource = "https://api.example/"
	fakeClientID = "client-abc"
)

type oauthFakes struct {
	mu           sync.Mutex
	resourceHits []string
	challenge    string
	authQueries  []url.Values
	tokenForms   []url.Values
	authRedirect string
	asHits       []string

	stateEcho  string
	forceState string
	forceError string
	noClientID bool
	openIDOnly bool
	badIssuer  bool

	resourceURL string
	asURL       string
}

func newOAuthFakes(t *testing.T) *oauthFakes {
	t.Helper()
	f := &oauthFakes{}

	asMux := http.NewServeMux()
	metadata := func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.asHits = append(f.asHits, r.URL.Path)
		openIDOnly, badIssuer := f.openIDOnly, f.badIssuer
		f.mu.Unlock()
		if openIDOnly && strings.Contains(r.URL.Path, "oauth-authorization-server") {
			http.NotFound(w, r)
			return
		}
		issuer := f.asURL
		if badIssuer {
			issuer = "https://elsewhere.example"
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 issuer,
			"authorization_endpoint": f.asURL + "/authorize",
			"token_endpoint":         f.asURL + "/token",
		})
	}
	asMux.HandleFunc("/.well-known/oauth-authorization-server", metadata)
	asMux.HandleFunc("/.well-known/openid-configuration", metadata)
	asMux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f.mu.Lock()
		f.asHits = append(f.asHits, r.URL.Path)
		f.challenge = q.Get("code_challenge")
		f.authRedirect = q.Get("redirect_uri")
		f.authQueries = append(f.authQueries, q)
		forceError, forceState := f.forceError, f.forceState
		f.mu.Unlock()
		if q.Get("code_challenge_method") != "S256" {
			http.Error(w, "only S256", http.StatusBadRequest)
			return
		}
		vals := url.Values{}
		redirect := q.Get("redirect_uri")
		if forceError != "" {
			vals.Set("error", forceError)
			vals.Set("state", q.Get("state"))
			http.Redirect(w, r, redirect+"?"+vals.Encode(), http.StatusFound)
			return
		}
		echo := q.Get("state")
		if forceState != "" {
			echo = forceState
		}
		vals.Set("code", "auth-code-123")
		vals.Set("state", echo)
		http.Redirect(w, r, redirect+"?"+vals.Encode(), http.StatusFound)
	})
	asMux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		f.asHits = append(f.asHits, r.URL.Path)
		f.tokenForms = append(f.tokenForms, r.PostForm)
		n := len(f.tokenForms)
		f.mu.Unlock()
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  fmt.Sprintf("access-%d", n),
			"refresh_token": fmt.Sprintf("refresh-%d", n),
			"token_type":    "Bearer",
			"expires_in":    3600,
			"scope":         "satelle",
		})
	})
	as := httptest.NewServer(asMux)
	t.Cleanup(as.Close)
	f.asURL = otherHost(t, as.URL)

	rsMux := http.NewServeMux()
	rsMux.HandleFunc("/.well-known/oauth-protected-resource/api", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.resourceHits = append(f.resourceHits, r.URL.Path)
		noClientID := f.noClientID
		f.mu.Unlock()
		prm := map[string]any{
			"resource":               fakeResource,
			"authorization_servers":  []string{f.asURL, "https://unused.example"},
			"satelle_client_id":      fakeClientID,
			"bearer_methods_support": []string{"header"},
		}
		if noClientID {
			delete(prm, "satelle_client_id")
		}
		_ = json.NewEncoder(w).Encode(prm)
	})
	rsMux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.resourceHits = append(f.resourceHits, r.URL.Path)
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"resource":              "https://mcp.example/",
			"authorization_servers": []string{"https://mcp-as.example"},
		})
	})
	rs := httptest.NewServer(rsMux)
	t.Cleanup(rs.Close)
	f.resourceURL = rs.URL
	requireLoopback(t, f.resourceURL, f.asURL)
	return f
}

func browserGET(t *testing.T) func(string) error {
	return func(authURL string) error {
		go func() {
			resp, err := http.Get(authURL)
			if err == nil {
				resp.Body.Close()
			}
		}()
		return nil
	}
}

func (f *oauthFakes) login(t *testing.T, opts LoginOptions) (Credential, error) {
	t.Helper()
	if opts.OpenBrowser == nil {
		opts.OpenBrowser = browserGET(t)
	}
	if opts.Timeout == 0 {
		opts.Timeout = 5 * time.Second
	}
	return Login(context.Background(), guardedClient(t), f.resourceURL, opts)
}

func TestLoginHappyPath(t *testing.T) {
	f := newOAuthFakes(t)
	cred, err := f.login(t, LoginOptions{})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if cred.AccessToken != "access-1" || cred.RefreshToken != "refresh-1" {
		t.Fatalf("tokens not captured: %+v", cred)
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.resourceHits) != 1 || f.resourceHits[0] != "/.well-known/oauth-protected-resource/api" {
		t.Errorf("resource server hits = %v, want only the /api PRM", f.resourceHits)
	}
	if len(f.authQueries) != 1 || len(f.tokenForms) != 1 {
		t.Fatalf("authorize=%d token=%d, want 1 and 1", len(f.authQueries), len(f.tokenForms))
	}
	form := f.tokenForms[0]
	if form.Get("grant_type") != "authorization_code" {
		t.Errorf("grant_type = %q", form.Get("grant_type"))
	}
	if form.Get("client_id") != fakeClientID || f.authQueries[0].Get("client_id") != fakeClientID {
		t.Errorf("client_id = %q / %q, want %q", f.authQueries[0].Get("client_id"), form.Get("client_id"), fakeClientID)
	}
	if f.authQueries[0].Get("resource") != fakeResource || form.Get("resource") != fakeResource {
		t.Errorf("resource = %q / %q, want %q", f.authQueries[0].Get("resource"), form.Get("resource"), fakeResource)
	}
	if got := f.authQueries[0].Get("scope"); got != "openid email profile offline_access" {
		t.Errorf("authorize scope = %q, want %q", got, "openid email profile offline_access")
	}
	if cred.TokenEndpoint != f.asURL+"/token" || cred.ClientID != fakeClientID || cred.Resource != fakeResource {
		t.Errorf("discovery fields not stamped on the credential: %+v", cred)
	}
	if form.Get("redirect_uri") != f.authRedirect {
		t.Errorf("redirect_uri mismatch: token=%q authorize=%q", form.Get("redirect_uri"), f.authRedirect)
	}
	if !strings.HasPrefix(form.Get("redirect_uri"), "http://127.0.0.1:") {
		t.Errorf("redirect_uri not loopback: %q", form.Get("redirect_uri"))
	}
	verifier := form.Get("code_verifier")
	if n := len(verifier); n < 43 || n > 128 {
		t.Errorf("verifier length %d out of range", n)
	}
	sum := sha256.Sum256([]byte(verifier))
	if got := base64.RawURLEncoding.EncodeToString(sum[:]); got != f.challenge {
		t.Errorf("S256(verifier) %q != challenge %q", got, f.challenge)
	}
}

func TestLoginDiscoveryFallsBackToOpenIDConfiguration(t *testing.T) {
	f := newOAuthFakes(t)
	f.openIDOnly = true
	if _, err := f.login(t, LoginOptions{}); err != nil {
		t.Fatalf("Login: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.asHits) < 2 || f.asHits[0] != "/.well-known/oauth-authorization-server" || f.asHits[1] != "/.well-known/openid-configuration" {
		t.Errorf("authorization server hits = %v, want RFC 8414 then OIDC", f.asHits)
	}
}

func TestLoginDiscoveryFailuresAbortBeforeBrowser(t *testing.T) {
	cases := map[string]func(*oauthFakes){
		"no client id":    func(f *oauthFakes) { f.noClientID = true },
		"issuer mismatch": func(f *oauthFakes) { f.badIssuer = true },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newOAuthFakes(t)
			mutate(f)
			opened := false
			_, err := f.login(t, LoginOptions{OpenBrowser: func(string) error { opened = true; return nil }})
			if err == nil {
				t.Fatal("expected a discovery error")
			}
			if opened {
				t.Error("the browser must not open when discovery fails")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.authQueries) != 0 || len(f.tokenForms) != 0 {
				t.Errorf("authorize=%d token=%d, want 0 and 0", len(f.authQueries), len(f.tokenForms))
			}
		})
	}
}

func TestLoginFixedCallbackPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	f := newOAuthFakes(t)
	if _, err := f.login(t, LoginOptions{Port: port}); err != nil {
		t.Fatalf("Login: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if want := fmt.Sprintf("http://127.0.0.1:%d/callback", port); f.authRedirect != want {
		t.Errorf("redirect_uri = %q, want %q", f.authRedirect, want)
	}
}

func TestLoginStateMismatchAbortsBeforeExchange(t *testing.T) {
	f := newOAuthFakes(t)
	f.forceState = "wrong-state"
	_, err := f.login(t, LoginOptions{Timeout: 3 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "state mismatch") {
		t.Fatalf("expected state mismatch error, got %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.tokenForms) != 0 {
		t.Fatalf("token endpoint must NOT be called on state mismatch, got %d calls", len(f.tokenForms))
	}
}

func TestLoginErrorParamNoExchange(t *testing.T) {
	f := newOAuthFakes(t)
	f.forceError = "access_denied"
	_, err := f.login(t, LoginOptions{Timeout: 3 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("expected authorization error, got %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.tokenForms) != 0 {
		t.Fatalf("token endpoint must NOT be called on error redirect, got %d calls", len(f.tokenForms))
	}
}

func TestNewPKCEShape(t *testing.T) {
	pk, err := newPKCE()
	if err != nil {
		t.Fatal(err)
	}
	if n := len(pk.verifier); n < 43 || n > 128 {
		t.Errorf("verifier length %d out of RFC range", n)
	}
	if _, err := base64.RawURLEncoding.DecodeString(pk.verifier); err != nil {
		t.Errorf("verifier not base64url: %v", err)
	}
	sum := sha256.Sum256([]byte(pk.verifier))
	if want := base64.RawURLEncoding.EncodeToString(sum[:]); want != pk.challenge {
		t.Errorf("challenge != S256(verifier)")
	}
}

func TestRefreshGrantCarriesResourceAndClientID(t *testing.T) {
	prov := newFakeProvider(t)
	cred := prov.credential("https://hosted.example")
	if _, err := refreshGrant(context.Background(), guardedClient(t), cred); err != nil {
		t.Fatal(err)
	}
	prov.wantRefresh(t, 0, "refresh-1")
}
