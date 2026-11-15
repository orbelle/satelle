package hosted

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultLoginTimeout = 3 * time.Minute

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
}

type oauthError struct {
	Code        string `json:"error"`
	Description string `json:"error_description"`
}

func (e *oauthError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Description)
	}
	return e.Code
}

type LoginOptions struct {
	OpenBrowser func(authURL string) error
	Timeout     time.Duration
	Out         io.Writer
	Port        int
}

type Flow struct {
	server      string
	ep          endpoints
	redirectURI string
	pk          pkce
	state       string
}

func newFlow(server string, ep endpoints, redirectURI string) (*Flow, error) {
	server = normalizeServerURL(server)
	if server == "" {
		return nil, fmt.Errorf("hosted: server URL not set")
	}
	pk, err := newPKCE()
	if err != nil {
		return nil, err
	}
	state, err := randomURLToken(16)
	if err != nil {
		return nil, err
	}
	return &Flow{server: server, ep: ep, redirectURI: redirectURI, pk: pk, state: state}, nil
}

func (f *Flow) State() string { return f.state }

func (f *Flow) AuthorizeURL() string {
	return buildAuthorizeURL(f.ep, f.redirectURI, f.pk.challenge, f.state)
}

func (f *Flow) Exchange(ctx context.Context, httpClient *http.Client, code string) (Credential, error) {
	tok, err := exchangeCode(ctx, httpClient, f.ep, code, f.redirectURI, f.pk.verifier)
	if err != nil {
		return Credential{}, err
	}
	cred := credentialFromToken(f.server, tok)
	cred.TokenEndpoint, cred.ClientID, cred.Resource = f.ep.TokenEndpoint, f.ep.ClientID, f.ep.Resource
	return cred, nil
}

type pkce struct {
	verifier  string
	challenge string
}

func newPKCE() (pkce, error) {
	v, err := randomURLToken(32)
	if err != nil {
		return pkce{}, err
	}
	sum := sha256.Sum256([]byte(v))
	return pkce{verifier: v, challenge: base64.RawURLEncoding.EncodeToString(sum[:])}, nil
}

func randomURLToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("hosted: random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func Login(ctx context.Context, httpClient *http.Client, server string, opts LoginOptions) (Credential, error) {
	server = normalizeServerURL(server)
	if server == "" {
		return Credential{}, fmt.Errorf("hosted: server URL not set (pass --server or set [hosted] server in ~/.satelle/config.toml)")
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultLoginTimeout
	}

	ep, err := discover(ctx, httpClient, server)
	if err != nil {
		return Credential{}, err
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", opts.Port))
	if err != nil {
		return Credential{}, fmt.Errorf("hosted: open loopback listener: %w", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	flow, err := newFlow(server, ep, redirectURI)
	if err != nil {
		return Credential{}, err
	}
	state := flow.State()

	type cbResult struct {
		code string
		err  error
	}
	resultCh := make(chan cbResult, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Get("error") != "":
			writeCallbackPage(w, "Authentication failed: "+q.Get("error"))
			resultCh <- cbResult{err: fmt.Errorf("hosted: authorization error: %s", q.Get("error"))}
		case q.Get("state") != state:
			writeCallbackPage(w, "Authentication failed: state mismatch.")
			resultCh <- cbResult{err: fmt.Errorf("hosted: state mismatch (possible CSRF) — aborted before token exchange")}
		case q.Get("code") == "":
			writeCallbackPage(w, "Authentication failed: no authorization code.")
			resultCh <- cbResult{err: fmt.Errorf("hosted: no authorization code in callback")}
		default:
			writeCallbackPage(w, "Authentication complete — you may close this tab.")
			resultCh <- cbResult{code: q.Get("code")}
		}
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	authURL := flow.AuthorizeURL()
	if opts.Out != nil {
		fmt.Fprintf(opts.Out, "Open this URL to authenticate:\n\n  %s\n\n", authURL)
	}
	if opts.OpenBrowser != nil {
		if err := opts.OpenBrowser(authURL); err != nil && opts.Out != nil {
			fmt.Fprintf(opts.Out, "(could not open a browser automatically: %v)\n", err)
		}
	}

	var code string
	select {
	case res := <-resultCh:
		if res.err != nil {
			return Credential{}, res.err
		}
		code = res.code
	case <-time.After(timeout):
		return Credential{}, fmt.Errorf("hosted: timed out after %s waiting for browser approval", timeout)
	case <-ctx.Done():
		return Credential{}, ctx.Err()
	}

	return flow.Exchange(ctx, httpClient, code)
}

const loginScope = "openid email profile offline_access"

func buildAuthorizeURL(ep endpoints, redirectURI, challenge, state string) string {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", ep.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("resource", ep.Resource)
	q.Set("scope", loginScope)
	sep := "?"
	if strings.Contains(ep.AuthorizationEndpoint, "?") {
		sep = "&"
	}
	return ep.AuthorizationEndpoint + sep + q.Encode()
}

func exchangeCode(ctx context.Context, httpClient *http.Client, ep endpoints, code, redirectURI, verifier string) (tokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("client_id", ep.ClientID)
	form.Set("code_verifier", verifier)
	form.Set("resource", ep.Resource)
	return postToken(ctx, httpClient, ep.TokenEndpoint, form)
}

func refreshGrant(ctx context.Context, httpClient *http.Client, cred Credential) (tokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", cred.RefreshToken)
	form.Set("client_id", cred.ClientID)
	form.Set("resource", cred.Resource)
	return postToken(ctx, httpClient, cred.TokenEndpoint, form)
}

func postToken(ctx context.Context, httpClient *http.Client, tokenEndpoint string, form url.Values) (tokenResponse, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("hosted: token request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		var oe oauthError
		if json.Unmarshal(body, &oe) == nil && oe.Code != "" {
			return tokenResponse{}, &oe
		}
		return tokenResponse{}, fmt.Errorf("hosted: token endpoint HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var tok tokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return tokenResponse{}, fmt.Errorf("hosted: decode token response: %w", err)
	}
	if tok.AccessToken == "" || tok.RefreshToken == "" {
		return tokenResponse{}, fmt.Errorf("hosted: token response missing access or refresh token")
	}
	return tok, nil
}

func credentialFromToken(server string, tok tokenResponse) Credential {
	c := Credential{
		ServerURL:    server,
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		TokenType:    tok.TokenType,
		Scope:        tok.Scope,
	}
	if tok.ExpiresIn > 0 {
		c.ExpiresAt = nowUTC().Add(time.Duration(tok.ExpiresIn) * time.Second).Format(time.RFC3339)
	}
	c.CreatedAt = nowUTC().Format(time.RFC3339)
	return c
}
