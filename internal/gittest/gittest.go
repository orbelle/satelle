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

type Server struct {
	URL          string
	Bare         string
	Tagged, Head string
	Env          []string
	CAFile       string

	root, base string
}

func Env() []string {
	return []string{
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CONFIG_COUNT=3",
		"GIT_CONFIG_KEY_0=core.excludesFile",
		"GIT_CONFIG_VALUE_0=" + os.DevNull,
		"GIT_CONFIG_KEY_1=gc.auto",
		"GIT_CONFIG_VALUE_1=0",
		"GIT_CONFIG_KEY_2=maintenance.auto",
		"GIT_CONFIG_VALUE_2=false",
		"GIT_AUTHOR_NAME=t",
		"GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t",
		"GIT_COMMITTER_EMAIL=t@t",
	}
}

func Command(t testing.TB, dir string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), Env()...)
	return cmd
}

func Main(m *testing.M) int {
	for _, kv := range Env() {
		k, v, _ := strings.Cut(kv, "=")
		os.Setenv(k, v)
	}
	return m.Run()
}

func Git(t testing.TB, dir string, args ...string) string {
	t.Helper()
	out, err := Command(t, dir, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

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
