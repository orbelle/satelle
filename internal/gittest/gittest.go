// Package gittest is the test suite's one git helper. Every test that runs git
// does so through Git or Command, and every package whose code under test runs
// git calls Main from its TestMain, so git never reads the developer's own
// global or system configuration (an ignore file excluding .satelle/, a
// signing key, an identity). It also serves a local smart-HTTP git server over
// TLS — git's own http-backend behind net/http/cgi — so a test can fetch over
// https with no network beyond loopback. It is not used by anything but tests.
package gittest

import (
	"encoding/pem"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Server is one served repository.
type Server struct {
	// URL is the https URL of the repository, tool.git.
	URL string
	// Bare is the bare repository's path on disk.
	Bare string
	// Tagged is the commit the tag v1 names; Head is the commit main names, one
	// later.
	Tagged, Head string
	// Env makes git trust the server and ignore the host's git configuration.
	Env []string
	// CAFile is the server's certificate, PEM.
	CAFile string

	root, base string // the served project root and the server's URL
}

// Env is the one list of variables that isolate a test's git from the host:
// no system or global configuration, core.excludesFile pinned to the null
// device (git otherwise falls back to $XDG_CONFIG_HOME/git/ignore even with no
// global config), and a fixed identity, since a nulled global config has none.
// GIT_CONFIG_GLOBAL needs git 2.32 and GIT_CONFIG_COUNT 2.31; older git ignores
// both, which is no worse than not isolating.
func Env() []string {
	return []string{
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=core.excludesFile",
		"GIT_CONFIG_VALUE_0=" + os.DevNull,
		"GIT_AUTHOR_NAME=t",
		"GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t",
		"GIT_COMMITTER_EMAIL=t@t",
	}
}

// Command is an isolated git command in dir ("" is the current directory),
// for a caller that must inspect a failure or add variables.
func Command(t testing.TB, dir string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), Env()...)
	return cmd
}

// Main isolates every git the test binary starts, including git run by the
// code under test, then runs the tests: func TestMain(m *testing.M) {
// os.Exit(gittest.Main(m)) }.
func Main(m *testing.M) int {
	for _, kv := range Env() {
		k, v, _ := strings.Cut(kv, "=")
		os.Setenv(k, v)
	}
	return m.Run()
}

// Git runs an isolated git in dir and returns its trimmed output, failing the
// test on error.
func Git(t testing.TB, dir string, args ...string) string {
	t.Helper()
	out, err := Command(t, dir, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// New serves a repository for the life of t, or skips t when git (or its
// http-backend) is not installed.
func New(t testing.TB) *Server {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	backend := filepath.Join(Git(t, t.TempDir(), "--exec-path"), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("git-http-backend is not installed")
	}

	work := t.TempDir()
	Git(t, work, "init", "-q", "-b", "main")
	write(t, filepath.Join(work, "README"), "one\n")
	Git(t, work, "add", "README")
	Git(t, work, "commit", "-q", "-m", "one")
	Git(t, work, "tag", "v1")
	s := &Server{Tagged: Git(t, work, "rev-parse", "HEAD")}
	write(t, filepath.Join(work, "README"), "two\n")
	Git(t, work, "commit", "-q", "-am", "two")
	s.Head = Git(t, work, "rev-parse", "HEAD")

	root := t.TempDir()
	s.Bare = filepath.Join(root, "tool.git")
	Git(t, root, "clone", "-q", "--bare", work, s.Bare)

	srv := httptest.NewTLSServer(&cgi.Handler{
		Path: backend, Root: "/git",
		Env:        append([]string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "GIT_HTTP_MAX_REQUEST_BUFFER=100M"}, Env()...),
		InheritEnv: []string{"PATH", "HOME"},
	})
	t.Cleanup(srv.Close)

	s.CAFile = filepath.Join(t.TempDir(), "ca.pem")
	write(t, s.CAFile, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})))
	s.root, s.base = root, srv.URL
	s.URL = srv.URL + "/git/tool.git"
	s.Env = append(Env(), "GIT_SSL_CAINFO="+s.CAFile)
	return s
}

// Also serves a second bare repository, name.git, holding the same commits as
// tool.git (main at Head, v1 at Tagged), and returns its https URL.
func (s *Server) Also(t testing.TB, name string) string {
	t.Helper()
	Git(t, s.root, "clone", "-q", "--bare", s.Bare, filepath.Join(s.root, name+".git"))
	return s.base + "/git/" + name + ".git"
}

func write(t testing.TB, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
