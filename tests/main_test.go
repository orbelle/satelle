//go:build integration

package tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/item"
)

var (
	satelleBin     string
	satelledBin    string
	hookHarnessBin string
)

func TestMain(m *testing.M) {
	os.Exit(buildAndRun(m))
}

func buildAndRun(m *testing.M) int {
	dir, err := os.MkdirTemp("", "satelle-it-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)
	exe := ""
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	satelleBin = filepath.Join(dir, "satelle"+exe)
	satelledBin = filepath.Join(dir, "satelled"+exe)
	hookHarnessBin = filepath.Join(dir, "hookharness"+exe)
	for bin, pkg := range map[string]string{satelleBin: "../cmd/satelle", satelledBin: "../cmd/satelled", hookHarnessBin: "./testdata/hookharness"} {
		cmd := exec.Command("go", "build", "-o", bin, pkg)
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "build %s: %v\n%s", pkg, err, out)
			return 1
		}
	}
	return m.Run()
}

type sandbox struct {
	t    *testing.T
	home string
	dir  string
	env  map[string]string
	path []string
}

var scrubbed = []string{"HOME", "USERPROFILE", "XDG_", "SATELLE_", "CLAUDE_", "GIT_", "CLAUDECODE", "CURSOR_", "PI_", "GROK_", "ANTIGRAVITY_", "DBUS_"}

func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	return newSandboxAt(t, realDir(t, t.TempDir()))
}

func outsideTempDir(t *testing.T) string {
	t.Helper()
	const base = "/var/tmp"
	if st, err := os.Stat(base); err != nil || !st.IsDir() {
		t.Skipf("%s is not a directory: no sandbox outside the temp dir", base)
	}
	if real, err := filepath.EvalSymlinks(base); err != nil || real == "/tmp" || strings.HasPrefix(real, "/tmp/") {
		t.Skipf("%s resolves inside /tmp: no sandbox outside the temp dir", base)
	}
	dir, err := os.MkdirTemp(base, "satelle-it-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return realDir(t, dir)
}

func newSandboxOutsideTemp(t *testing.T) *sandbox {
	t.Helper()
	s := newSandboxAt(t, outsideTempDir(t))
	tmp := filepath.Join(s.home, "tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	s.env["TMPDIR"] = tmp
	return s
}

func newSandboxAt(t *testing.T, dir string) *sandbox {
	t.Helper()
	home := realDir(t, t.TempDir())
	s := &sandbox{t: t, home: home, dir: dir, env: map[string]string{
		"HOME":                    home,
		"USERPROFILE":             home,
		"SATELLE_HOME":            filepath.Join(home, ".satelle"),
		"XDG_CONFIG_HOME":         filepath.Join(home, "xdg", "config"),
		"XDG_DATA_HOME":           filepath.Join(home, "xdg", "data"),
		"XDG_STATE_HOME":          filepath.Join(home, "xdg", "state"),
		"SATELLE_SERVER_ENDPOINT": "none",
		"USER":                    "tester",
	}}
	for _, kv := range gittest.Env() {
		k, v, _ := strings.Cut(kv, "=")
		s.env[k] = v
	}
	guard := filepath.Join(home, ".guard")
	if err := os.MkdirAll(guard, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"launchctl", "systemctl"} {
		if err := os.WriteFile(filepath.Join(guard, name), []byte("#!/bin/sh\necho '"+name+" is refused in tests' >&2\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s.initRepo("git")
	return s
}

const setupAttempts = 3

func (s *sandbox) initRepo(gitBin string) {
	s.t.Helper()
	steps := [][]string{{"init", "-q"}, {"commit", "-q", "--allow-empty", "-m", "init"}}
	for n := 1; ; n++ {
		var failed []string
		var r result
		for _, args := range steps {
			if r = s.exec(gitBin, "", args...); r.code != 0 || r.signaled {
				failed = args
				break
			}
		}
		if failed == nil {
			return
		}
		if !r.signaled || n == setupAttempts {
			s.t.Fatalf("%s", gitFailure(failed, r))
		}
		s.t.Logf("sandbox setup: %s; retrying (attempt %d/%d)", gitFailure(failed, r), n+1, setupAttempts)
		if err := os.RemoveAll(filepath.Join(s.dir, ".git")); err != nil {
			s.t.Fatal(err)
		}
	}
}

func realDir(t *testing.T, d string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(d)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (s *sandbox) environ() []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == "PATH" || slicesHasPrefix(k, scrubbed) {
			continue
		}
		out = append(out, kv)
	}
	path := append(append([]string{}, s.path...), filepath.Dir(satelleBin), filepath.Join(s.home, ".guard"), os.Getenv("PATH"))
	out = append(out, "PATH="+strings.Join(path, string(os.PathListSeparator)))
	for k, v := range s.env {
		out = append(out, k+"="+v)
	}
	return out
}

func slicesHasPrefix(k string, prefixes []string) bool {
	for _, p := range prefixes {
		if k == p || strings.HasPrefix(k, p) && strings.HasSuffix(p, "_") {
			return true
		}
	}
	return false
}

type result struct {
	out, err string
	code     int
	state    string
	signaled bool
}

func (s *sandbox) exec(bin, stdin string, args ...string) result {
	s.t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = s.dir
	cmd.Env = s.environ()
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	r := result{out: out.String(), err: errb.String()}
	if ps := cmd.ProcessState; ps != nil {
		r.state = ps.String()
		r.signaled = !ps.Exited()
	}
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			s.t.Fatalf("%s %v: %v", bin, args, err)
		}
		r.code = ee.ExitCode()
	}
	return r
}

func (s *sandbox) run(args ...string) result {
	s.t.Helper()
	return s.exec(satelleBin, "", args...)
}

func (s *sandbox) must(args ...string) string {
	s.t.Helper()
	r := s.run(args...)
	if r.code != 0 {
		s.t.Fatalf("satelle %s: exit %d\n%s%s", strings.Join(args, " "), r.code, r.out, r.err)
	}
	return r.out
}

func (s *sandbox) mustJSON(v any, args ...string) {
	s.t.Helper()
	out := s.must(append(args, "--json")...)
	if err := json.Unmarshal([]byte(out), v); err != nil {
		s.t.Fatalf("satelle %s --json: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func (s *sandbox) create(kind string, args ...string) item.Item {
	s.t.Helper()
	var it item.Item
	s.mustJSON(&it, append([]string{kind, "create"}, args...)...)
	if it.ID == "" {
		s.t.Fatalf("%s create returned no id", kind)
	}
	return it
}

func (s *sandbox) get(id string) item.Item {
	s.t.Helper()
	var it item.Item
	s.mustJSON(&it, "story", "get", id)
	return it
}

func (s *sandbox) git(args ...string) string {
	s.t.Helper()
	r := s.exec("git", "", args...)
	if r.code != 0 {
		s.t.Fatalf("%s", gitFailure(args, r))
	}
	return r.out
}

func gitFailure(args []string, r result) string {
	stream := func(s string) string {
		if s == "" {
			return "(empty)"
		}
		return s
	}
	state := r.state
	if state == "" {
		state = fmt.Sprintf("exit %d", r.code)
	}
	return fmt.Sprintf("git %v: %s\nstdout: %s\nstderr: %s", args, state, stream(r.out), stream(r.err))
}

func (s *sandbox) write(rel, body string) {
	s.t.Helper()
	writeFile(s.t, filepath.Join(s.dir, rel), body, 0o644)
}

func writeFile(t *testing.T, p, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	exe := mode&0o111 != 0
	if exe {
		syscall.ForkLock.RLock()
	}
	err := os.WriteFile(p, []byte(body), mode)
	if exe {
		syscall.ForkLock.RUnlock()
	}
	if err != nil {
		t.Fatal(err)
	}
}

type event struct {
	Item    string          `json:"item"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

type gateRun struct {
	Transition string   `json:"transition"`
	Gate       string   `json:"gate"`
	Binding    string   `json:"binding"`
	Result     string   `json:"result"`
	Notes      string   `json:"notes"`
	TokensIn   int      `json:"tokens_in"`
	TokensOut  int      `json:"tokens_out"`
	Changed    []string `json:"changed"`
	Warning    string   `json:"warning"`
	Answer     string   `json:"answer"`
}

func (s *sandbox) ledger(id, kind string) []event {
	s.t.Helper()
	args := []string{"ledger", "list"}
	if id != "" {
		args = append(args, id)
	}
	if kind != "" {
		args = append(args, "--kind", kind)
	}
	var evs []event
	s.mustJSON(&evs, args...)
	return evs
}

func (s *sandbox) gateRuns(id string) []gateRun {
	s.t.Helper()
	var out []gateRun
	for _, e := range s.ledger(id, "gate.run") {
		var g gateRun
		if err := json.Unmarshal(e.Payload, &g); err != nil {
			s.t.Fatal(err)
		}
		out = append(out, g)
	}
	return out
}

func (s *sandbox) mustConfigValid() {
	s.t.Helper()
	r := s.run("validate", "--json")
	var v struct {
		Problems []string `json:"problems"`
	}
	if err := json.Unmarshal([]byte(r.out), &v); err != nil || len(v.Problems) != 0 {
		s.t.Fatalf("satelle validate: exit %d, problems %v (%v)\n%s%s", r.code, v.Problems, err, r.out, r.err)
	}
}
