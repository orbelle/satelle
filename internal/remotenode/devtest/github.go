package devtest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// GitHub is a stand-in for the part of the GitHub API a github-app installation
// token is judged by, and the pull requests, reviews and commit statuses a PR
// approval reads and posts, with the commits, comparisons and combined statuses a
// release approval reads, for tests. GET /repos/{owner}/{name} answers 200 for
// a token whose repo set holds owner/name, 404 for one that does not (GitHub
// hides a repo the token cannot read), and 401 for a token it never minted or has
// revoked; every /repos/{owner}/{name}/… route is fenced the same way. DELETE
// /installation/token revokes the bearer, as GitHub does. It is for tests only:
// nothing in the product imports it.
type GitHub struct {
	// URL is the base URL to call the API at.
	URL string
	// ReviewerLogin is the author every review posted to the fake is recorded
	// under: the App the minted token belongs to.
	ReviewerLogin string
	// FailEvent, when set, is a review event ("APPROVE", "COMMENT") every post of
	// which the fake answers 500, recording nothing.
	FailEvent string

	srv *httptest.Server

	mu       sync.Mutex
	tokens   map[string]map[string]bool // token → owner/name it may read
	revoked  map[string]bool
	prs      map[string]*PR // "owner/name#n" → PR
	reviews  []PostedReview
	statuses []PostedStatus
	commits  map[string]*commit // "owner/name@sha" → commit
	seq      int
	seeded   []PostedStatus
}

// commit is a commit the fake serves.
type commit struct {
	sha     string
	parents []string
	files   []string
	seq     int
}

// The caps GitHub lists a commit's or a comparison's commits and files under.
const (
	filesCap   = 300
	commitsCap = 250
)

// PR is a pull request the fake serves.
type PR struct {
	Repo    string
	Number  int
	State   string // "open" when empty
	HeadRef string
	HeadSHA string
	// BaseSHA is the tip of the branch the PR merges into.
	BaseSHA string
	Body    string
	Diff    string
	// Author is the login that opened the PR; an APPROVE posted while it is
	// ReviewerLogin is refused with 422, as GitHub refuses the PR's own author.
	Author string
}

// PostedReview is a review a caller posted.
type PostedReview struct {
	ID       int64
	Repo     string
	Number   int
	CommitID string
	Event    string
	Body     string
	// Token is the bearer the review was posted with.
	Token string
}

// PostedStatus is a commit status a caller posted.
type PostedStatus struct {
	Repo, SHA                   string
	State, Context, Description string
	// Token is the bearer the status was posted with.
	Token string
}

// NewGitHub starts the fake for the life of t.
func NewGitHub(t testing.TB) *GitHub {
	t.Helper()
	g := &GitHub{tokens: map[string]map[string]bool{}, revoked: map[string]bool{}, prs: map[string]*PR{}, commits: map[string]*commit{}, ReviewerLogin: "satelle-reviewer[bot]"}
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

// AddPR serves pr (open unless its State says otherwise).
func (g *GitHub) AddPR(pr PR) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if pr.State == "" {
		pr.State = "open"
	}
	g.prs[prKey(pr.Repo, pr.Number)] = &pr
}

// SetHead moves PR n of repo to sha, as a push to its branch does.
func (g *GitHub) SetHead(repo string, n int, sha string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if p := g.prs[prKey(repo, n)]; p != nil {
		p.HeadSHA = sha
	}
}

// SetBody replaces PR n's body.
func (g *GitHub) SetBody(repo string, n int, body string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if p := g.prs[prKey(repo, n)]; p != nil {
		p.Body = body
	}
}

// AddCommit serves commit sha of repo with parents (first parent first) and
// the files it changes against its first parent.
func (g *GitHub) AddCommit(repo, sha string, parents []string, files ...string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.seq++
	g.commits[repo+"@"+sha] = &commit{sha: sha, parents: append([]string(nil), parents...), files: append([]string(nil), files...), seq: g.seq}
}

// SeedStatus puts a status on commit sha of repo that no caller posted, as a
// status posted before the test began; Statuses and Posts do not count it.
func (g *GitHub) SeedStatus(repo, sha, context, state string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.seeded = append(g.seeded, PostedStatus{Repo: repo, SHA: sha, State: state, Context: context})
}

// Reviews are the reviews posted, in order.
func (g *GitHub) Reviews() []PostedReview {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]PostedReview(nil), g.reviews...)
}

// Statuses are the commit statuses posted, in order.
func (g *GitHub) Statuses() []PostedStatus {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]PostedStatus(nil), g.statuses...)
}

// Posts is how many reviews and statuses were posted.
func (g *GitHub) Posts() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.reviews) + len(g.statuses)
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

func prKey(repo string, n int) string { return repo + "#" + strconv.Itoa(n) }

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
	if r.Method == http.MethodDelete && r.URL.Path == "/installation/token" {
		g.Revoke(token)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/repos/")
	parts := strings.Split(rest, "/")
	if !ok || len(parts) < 2 {
		reply(w, http.StatusNotFound, `{"message":"Not Found"}`)
		return
	}
	repo := parts[0] + "/" + parts[1]
	if !repos[repo] {
		reply(w, http.StatusNotFound, `{"message":"Not Found"}`)
		return
	}
	switch sub := parts[2:]; {
	case len(sub) == 0 && r.Method == http.MethodGet:
		reply(w, http.StatusOK, `{}`)
	case len(sub) == 1 && sub[0] == "pulls" && r.Method == http.MethodGet:
		g.listPulls(w, repo, r.URL.Query().Get("state"), r.URL.Query().Get("head"))
	case len(sub) == 2 && sub[0] == "pulls" && r.Method == http.MethodGet:
		g.getPull(w, r, repo, sub[1])
	case len(sub) == 3 && sub[0] == "pulls" && sub[2] == "reviews" && r.Method == http.MethodPost:
		g.postReview(w, r, repo, sub[1], token)
	case len(sub) == 2 && sub[0] == "statuses" && r.Method == http.MethodPost:
		g.postStatus(w, r, repo, sub[1], token)
	case len(sub) == 3 && sub[0] == "commits" && sub[2] == "status" && r.Method == http.MethodGet:
		g.combinedStatus(w, repo, sub[1])
	case len(sub) == 2 && sub[0] == "commits" && r.Method == http.MethodGet:
		g.getCommit(w, repo, sub[1])
	case len(sub) == 2 && sub[0] == "compare" && r.Method == http.MethodGet:
		g.compare(w, repo, sub[1])
	default:
		reply(w, http.StatusNotFound, `{"message":"Not Found"}`)
	}
}

func pullJSON(p PR) map[string]any {
	return map[string]any{
		"number": p.Number, "state": p.State, "body": p.Body,
		"html_url": "https://github.com/" + p.Repo + "/pull/" + strconv.Itoa(p.Number),
		"head":     map[string]any{"ref": p.HeadRef, "sha": p.HeadSHA},
		"base":     map[string]any{"sha": p.BaseSHA},
	}
}

// combinedStatus answers GET /repos/{repo}/commits/{sha}/status: the latest
// status of each context, seeded or posted.
func (g *GitHub) combinedStatus(w http.ResponseWriter, repo, sha string) {
	g.mu.Lock()
	all := append(append([]PostedStatus(nil), g.seeded...), g.statuses...)
	g.mu.Unlock()
	seen := map[string]bool{}
	out := []map[string]any{}
	for i := len(all) - 1; i >= 0; i-- {
		s := all[i]
		if s.Repo != repo || s.SHA != sha || seen[s.Context] {
			continue
		}
		seen[s.Context] = true
		out = append(out, map[string]any{"context": s.Context, "state": s.State, "description": s.Description})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sha": sha, "statuses": out})
}

func commitJSON(c *commit) map[string]any {
	parents := []map[string]any{}
	for _, p := range c.parents {
		parents = append(parents, map[string]any{"sha": p})
	}
	return map[string]any{"sha": c.sha, "parents": parents}
}

func filesJSON(paths []string) []map[string]any {
	if len(paths) > filesCap {
		paths = paths[:filesCap]
	}
	out := []map[string]any{}
	for _, p := range paths {
		out = append(out, map[string]any{"filename": p})
	}
	return out
}

// getCommit answers GET /repos/{repo}/commits/{sha}: its parents and, as
// GitHub lists them, at most filesCap of the files it changes.
func (g *GitHub) getCommit(w http.ResponseWriter, repo, sha string) {
	g.mu.Lock()
	c, ok := g.commits[repo+"@"+sha]
	g.mu.Unlock()
	if !ok {
		reply(w, http.StatusNotFound, `{"message":"No commit found for SHA: `+sha+`"}`)
		return
	}
	out := commitJSON(c)
	out["files"] = filesJSON(c.files)
	writeJSON(w, http.StatusOK, out)
}

// reach is the set of commits of repo reachable from sha, sha included.
// The caller holds g.mu.
func (g *GitHub) reach(repo, sha string) map[string]bool {
	seen := map[string]bool{}
	for todo := []string{sha}; len(todo) > 0; {
		s := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		if seen[s] {
			continue
		}
		seen[s] = true
		if c := g.commits[repo+"@"+s]; c != nil {
			todo = append(todo, c.parents...)
		}
	}
	return seen
}

// compare answers GET /repos/{repo}/compare/{base}...{head}: head's status
// relative to base, the commits reachable from head and not from base (at most
// commitsCap of total_commits), and at most filesCap of the files they change.
func (g *GitHub) compare(w http.ResponseWriter, repo, basehead string) {
	base, head, ok := strings.Cut(basehead, "...")
	g.mu.Lock()
	defer g.mu.Unlock()
	if !ok || g.commits[repo+"@"+base] == nil || g.commits[repo+"@"+head] == nil {
		reply(w, http.StatusNotFound, `{"message":"Not Found"}`)
		return
	}
	fromHead, fromBase := g.reach(repo, head), g.reach(repo, base)
	status := "diverged"
	switch {
	case base == head:
		status = "identical"
	case fromHead[base]:
		status = "ahead"
	case fromBase[head]:
		status = "behind"
	}
	var span []*commit
	for s := range fromHead {
		if c := g.commits[repo+"@"+s]; c != nil && !fromBase[s] {
			span = append(span, c)
		}
	}
	sort.Slice(span, func(i, j int) bool { return span[i].seq < span[j].seq })
	changed := map[string]bool{}
	var files []string
	commits := []map[string]any{}
	for i, c := range span {
		for _, f := range c.files {
			if !changed[f] {
				changed[f] = true
				files = append(files, f)
			}
		}
		if i < commitsCap {
			commits = append(commits, commitJSON(c))
		}
	}
	sort.Strings(files)
	writeJSON(w, http.StatusOK, map[string]any{"status": status, "total_commits": len(span), "commits": commits, "files": filesJSON(files)})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, _ := json.Marshal(v)
	reply(w, status, string(b))
}

// listPulls answers GET /repos/{repo}/pulls, filtered by state and by head
// (owner:branch), as GitHub filters it.
func (g *GitHub) listPulls(w http.ResponseWriter, repo, state, head string) {
	owner, _, _ := strings.Cut(repo, "/")
	g.mu.Lock()
	var out []map[string]any
	var nums []int
	byNum := map[int]PR{}
	for _, p := range g.prs {
		if p.Repo != repo || (state != "" && state != "all" && p.State != state) || (head != "" && head != owner+":"+p.HeadRef) {
			continue
		}
		nums = append(nums, p.Number)
		byNum[p.Number] = *p
	}
	g.mu.Unlock()
	sort.Ints(nums)
	out = []map[string]any{}
	for _, n := range nums {
		out = append(out, pullJSON(byNum[n]))
	}
	writeJSON(w, http.StatusOK, out)
}

// getPull answers GET /repos/{repo}/pulls/{n}: JSON, or the diff when asked for
// the diff media type.
func (g *GitHub) getPull(w http.ResponseWriter, r *http.Request, repo, num string) {
	n, _ := strconv.Atoi(num)
	g.mu.Lock()
	p, ok := g.prs[prKey(repo, n)]
	var pr PR
	if ok {
		pr = *p
	}
	g.mu.Unlock()
	if !ok {
		reply(w, http.StatusNotFound, `{"message":"Not Found"}`)
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "diff") {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(pr.Diff))
		return
	}
	writeJSON(w, http.StatusOK, pullJSON(pr))
}

// postReview answers POST /repos/{repo}/pulls/{n}/reviews and records it.
func (g *GitHub) postReview(w http.ResponseWriter, r *http.Request, repo, num, token string) {
	n, _ := strconv.Atoi(num)
	var in struct {
		CommitID string `json:"commit_id"`
		Event    string `json:"event"`
		Body     string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		reply(w, http.StatusUnprocessableEntity, `{"message":"Problems parsing JSON"}`)
		return
	}
	g.mu.Lock()
	p, ok := g.prs[prKey(repo, n)]
	if !ok {
		g.mu.Unlock()
		reply(w, http.StatusNotFound, `{"message":"Not Found"}`)
		return
	}
	if g.FailEvent != "" && in.Event == g.FailEvent {
		g.mu.Unlock()
		reply(w, http.StatusInternalServerError, `{"message":"Server Error"}`)
		return
	}
	if in.Event == "APPROVE" && p.Author != "" && p.Author == g.ReviewerLogin {
		g.mu.Unlock()
		reply(w, http.StatusUnprocessableEntity, `{"message":"Unprocessable Entity","errors":["Can not approve your own pull request"]}`)
		return
	}
	rv := PostedReview{ID: int64(1000 + len(g.reviews)), Repo: repo, Number: n, CommitID: in.CommitID, Event: in.Event, Body: in.Body, Token: token}
	g.reviews = append(g.reviews, rv)
	login := g.ReviewerLogin
	g.mu.Unlock()
	state := in.Event
	if state == "APPROVE" {
		state = "APPROVED"
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": rv.ID, "commit_id": rv.CommitID, "state": state, "user": map[string]any{"login": login}})
}

// postStatus answers POST /repos/{repo}/statuses/{sha} and records it.
func (g *GitHub) postStatus(w http.ResponseWriter, r *http.Request, repo, sha, token string) {
	var in struct {
		State       string `json:"state"`
		Context     string `json:"context"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		reply(w, http.StatusUnprocessableEntity, `{"message":"Problems parsing JSON"}`)
		return
	}
	g.mu.Lock()
	g.statuses = append(g.statuses, PostedStatus{Repo: repo, SHA: sha, State: in.State, Context: in.Context, Description: in.Description, Token: token})
	g.mu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]any{"state": in.State, "context": in.Context})
}
