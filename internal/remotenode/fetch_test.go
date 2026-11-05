package remotenode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/remotedef"
)

// testDefName is the name a test's definition goes by in a refusal.
const testDefName = "remote.toml at version test"

// fetchDeclared fetches repos under a definition that declares exactly them.
func fetchDeclared(dir string, repos []remotedef.Repo, env []string) ([]FetchedRepo, error) {
	return FetchRepos(context.Background(), dir, remotedef.Definition{Repos: repos}, testDefName, repos, env)
}

// AC5: the node fetches a declared repo at its ref over https into a clone whose
// push URL refuses. The server is a local smart-HTTP git server (git's
// http-backend behind net/http/cgi), so the fetch needs no network beyond it.
func TestFetchReposOverHTTPSIsReadOnly(t *testing.T) {
	g := gittest.New(t)
	dir := t.TempDir()

	got, err := fetchDeclared(dir, []remotedef.Repo{{URL: g.URL, Ref: "main"}}, g.Env)
	if err != nil {
		t.Fatalf("FetchRepos: %v", err)
	}
	if len(got) != 1 || got[0].Name != "tool" || got[0].Commit != g.Head || got[0].Ref != "main" {
		t.Fatalf("fetched = %+v, want tool at %s", got, g.Head)
	}
	clone := filepath.Join(dir, "tool")
	if b, _ := os.ReadFile(filepath.Join(clone, "README")); string(b) != "two\n" {
		t.Errorf("working tree README = %q", b)
	}
	if gittest.Git(t, clone, "rev-parse", "HEAD") != g.Head {
		t.Error("the clone is not at the fetched commit")
	}

	// A push refuses, and the served repository is untouched.
	gittest.Git(t, clone, "commit", "-q", "--allow-empty", "-m", "local change")
	cmd := gittest.Command(t, clone, "push", "origin", "HEAD:refs/heads/pushed")
	cmd.Env = append(cmd.Env, g.Env...)
	out, perr := cmd.CombinedOutput()
	if perr == nil {
		t.Fatalf("a push from the node's clone must fail:\n%s", out)
	}
	if !strings.Contains(string(out), "refused") {
		t.Errorf("the push must fail on the clone's own refusing push URL, not on the server:\n%s", out)
	}
	if refs := gittest.Git(t, g.Bare, "for-each-ref", "refs/heads/"); strings.Contains(refs, "pushed") {
		t.Errorf("the push reached the served repo:\n%s", refs)
	}
	if pushURL := gittest.Git(t, clone, "remote", "get-url", "--push", "origin"); pushURL != refusedPushURL {
		t.Errorf("push URL = %q, want the refusing one", pushURL)
	}

	// A tag pins an older commit than the branch tip.
	got, err = fetchDeclared(t.TempDir(), []remotedef.Repo{{URL: g.URL, Ref: "v1"}}, g.Env)
	if err != nil || len(got) != 1 || got[0].Commit != g.Tagged {
		t.Fatalf("fetching v1 = %+v, %v; want commit %s", got, err, g.Tagged)
	}
}

func TestFetchReposRefusesWhatIsNotPlainHTTPS(t *testing.T) {
	for _, tc := range []struct{ name, url, ref string }{
		{"http", "http://git.example.com/a/b.git", "main"},
		{"ssh", "ssh://git@git.example.com/a/b.git", "main"},
		{"file", "file:///tmp/x.git", "main"},
		{"credential", "https://user:secret@git.example.com/a/b.git", "main"},
		{"option ref", "https://git.example.com/a/b.git", "--upload-pack=x"},
		{"no ref", "https://git.example.com/a/b.git", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			_, err := fetchDeclared(dir, []remotedef.Repo{{URL: tc.url, Ref: tc.ref}}, nil)
			if err == nil {
				t.Fatal("must refuse")
			}
			if strings.Contains(err.Error(), "secret") {
				t.Errorf("the error echoes the credential: %v", err)
			}
			if entries, _ := os.ReadDir(dir); len(entries) > 0 {
				t.Errorf("a refused repo must not create a clone: %v", entries)
			}
		})
	}
}

func TestFetchReposWithNoReposDoesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repos")
	if got, err := FetchRepos(context.Background(), dir, remotedef.Definition{}, testDefName, nil, nil); err != nil || got != nil {
		t.Errorf("= %v, %v", got, err)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Error("no repos, no directory")
	}
}

// A repo, or a declared repo at a ref, that the definition does not declare is
// refused naming the repo and the definition, and nothing is fetched — not even a
// declared repo asked for alongside it.
func TestFetchReposRefusesWhatTheDefinitionDoesNotDeclare(t *testing.T) {
	g := gittest.New(t)
	other := g.Also(t, "other")
	def := remotedef.Definition{Repos: []remotedef.Repo{{URL: g.URL, Ref: "main"}}}
	for _, tc := range []struct {
		name string
		want []remotedef.Repo
		url  string
	}{
		{"undeclared repo", []remotedef.Repo{{URL: other, Ref: "main"}}, other},
		{"undeclared ref", []remotedef.Repo{{URL: g.URL, Ref: "v1"}}, g.URL},
		{"declared then undeclared", []remotedef.Repo{{URL: g.URL, Ref: "main"}, {URL: other, Ref: "main"}}, other},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "repos")
			got, err := FetchRepos(context.Background(), dir, def, testDefName, tc.want, g.Env)
			if err == nil {
				t.Fatalf("must refuse; fetched %+v", got)
			}
			for _, s := range []string{tc.url, testDefName, "not declared"} {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("the refusal %q does not name %q", err, s)
				}
			}
			if _, serr := os.Stat(dir); serr == nil {
				t.Error("a refused fetch must create no clone")
			}
		})
	}
	if err := CheckDeclared(def, testDefName, g.URL, "main"); err != nil {
		t.Errorf("the declared repo at its ref is refused: %v", err)
	}
}

// Once checked out, a clone's origin answers no further fetch: another ref of the
// same repo is refused before it reaches the server, and HEAD stays put.
func TestFetchedCloneRefusesALaterFetch(t *testing.T) {
	g := gittest.New(t)
	dir := t.TempDir()
	if _, err := fetchDeclared(dir, []remotedef.Repo{{URL: g.URL, Ref: "main"}}, g.Env); err != nil {
		t.Fatalf("FetchRepos: %v", err)
	}
	clone := filepath.Join(dir, "tool")
	cmd := gittest.Command(t, clone, "fetch", "origin", "v1")
	cmd.Env = append(cmd.Env, g.Env...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("a later fetch from the node's clone must fail:\n%s", out)
	}
	if !strings.Contains(string(out), "refused") {
		t.Errorf("the fetch must fail on the clone's own refusing fetch URL:\n%s", out)
	}
	if head := gittest.Git(t, clone, "rev-parse", "HEAD"); head != g.Head {
		t.Errorf("HEAD = %s after a refused fetch, want %s", head, g.Head)
	}
	if fetchURL := gittest.Git(t, clone, "remote", "get-url", "origin"); fetchURL != refusedFetchURL {
		t.Errorf("fetch URL = %q, want the refusing one", fetchURL)
	}
}
