package devtest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// GitHub is a stand-in for the part of the GitHub API a github-app installation
// token is judged by, for tests: GET /repos/{owner}/{name} answers 200 for a token
// whose repo set holds owner/name, 404 for one that does not (GitHub hides a repo
// the token cannot read), and 401 for a token it never minted or has revoked.
// DELETE /installation/token revokes the bearer, as GitHub does. It is for tests
// only: nothing in the product imports it.
type GitHub struct {
	// URL is the base URL to call the API at.
	URL string

	srv *httptest.Server

	mu      sync.Mutex
	tokens  map[string]map[string]bool // token → owner/name it may read
	revoked map[string]bool
}

// NewGitHub starts the fake for the life of t.
func NewGitHub(t testing.TB) *GitHub {
	t.Helper()
	g := &GitHub{tokens: map[string]map[string]bool{}, revoked: map[string]bool{}}
	g.srv = httptest.NewServer(http.HandlerFunc(g.serve))
	g.URL = g.srv.URL
	t.Cleanup(g.srv.Close)
	return g
}

// Client is an HTTP client for the fake's own server; a GitHub made with only its
// URL (in another process than the fake's) calls it with the default client.
func (g *GitHub) Client() *http.Client {
	if g.srv == nil {
		return http.DefaultClient
	}
	return g.srv.Client()
}

// Mint registers token as an installation token that may read exactly repos
// (each "owner/name"), as the server's github-app adapter mints one scoped to a
// definition's [[repo]] entries.
func (g *GitHub) Mint(token string, repos ...string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	set := map[string]bool{}
	for _, r := range repos {
		set[r] = true
	}
	g.tokens[token] = set
	return token
}

// Revoke ends token: every later call with it is a 401.
func (g *GitHub) Revoke(token string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.revoked[token] = true
}

// RepoStatus is the status GET /repos/{repo} answers with token as bearer.
func (g *GitHub) RepoStatus(token, repo string) (int, error) {
	req, err := http.NewRequest(http.MethodGet, g.URL+"/repos/"+repo, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := g.Client().Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

func (g *GitHub) serve(w http.ResponseWriter, r *http.Request) {
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	g.mu.Lock()
	repos, minted := g.tokens[token]
	live := minted && !g.revoked[token]
	g.mu.Unlock()
	if !live {
		reply(w, http.StatusUnauthorized, `{"message":"Bad credentials"}`)
		return
	}
	switch {
	case r.Method == http.MethodDelete && r.URL.Path == "/installation/token":
		g.Revoke(token)
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/"):
		if !repos[strings.TrimPrefix(r.URL.Path, "/repos/")] {
			reply(w, http.StatusNotFound, `{"message":"Not Found"}`)
			return
		}
		reply(w, http.StatusOK, `{}`)
	default:
		reply(w, http.StatusNotFound, `{"message":"Not Found"}`)
	}
}
