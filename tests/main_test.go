// Package tests is satelle's integration suite. TestMain builds the real
// satelle and satelled binaries once; each test drives them as a user would,
// in its own home, credentials dir and fresh git repo. Nothing here reaches
// past loopback.
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
	"testing"

	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/item"
)

// Built binaries, set by TestMain.
var (
	satelleBin  string
	satelledBin string
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
	for bin, pkg := range map[string]string{satelleBin: "../cmd/satelle", satelledBin: "../cmd/satelled"} {
		cmd := exec.Command("go", "build", "-o", bin, pkg)
		// Static, so the docker-local test can mount it into a small image.
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "build %s: %v\n%s", pkg, err, out)
			return 1
		}
	}
	return m.Run()
}

// sandbox is one user's machine: a home, a repo, and the environment every
// command of the test runs in.
type sandbox struct {
	t    *testing.T
	home string
	dir  string // the repo, the working directory of every command
	env  map[string]string
	path []string // prepended to PATH
}

// scrubbed are inherited variables that would leak the host's satelle,
// session or harness into a test.
var scrubbed = []string{"HOME", "USERPROFILE", "XDG_", "SATELLE_", "CLAUDE_", "GIT_"}

// newSandbox makes a fresh home and an empty, committed git repo.
func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	home := realDir(t, t.TempDir())
	s := &sandbox{t: t, home: home, dir: realDir(t, t.TempDir()), env: map[string]string{
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
	s.git("init", "-q")
	s.git("commit", "-q", "--allow-empty", "-m", "init")
	return s
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
	path := append(append([]string{}, s.path...), filepath.Dir(satelleBin), os.Getenv("PATH"))
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

// result is one finished command.
type result struct {
	out, err string
	code     int
}

// exec runs bin with args in the repo, stdin on its input.
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
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			s.t.Fatalf("%s %v: %v", bin, args, err)
		}
		r.code = ee.ExitCode()
	}
	return r
}

// run runs satelle.
func (s *sandbox) run(args ...string) result {
	s.t.Helper()
	return s.exec(satelleBin, "", args...)
}

// must runs satelle and fails the test unless it exits 0.
func (s *sandbox) must(args ...string) string {
	s.t.Helper()
	r := s.run(args...)
	if r.code != 0 {
		s.t.Fatalf("satelle %s: exit %d\n%s%s", strings.Join(args, " "), r.code, r.out, r.err)
	}
	return r.out
}

// mustJSON runs satelle with --json appended and decodes its output into v.
func (s *sandbox) mustJSON(v any, args ...string) {
	s.t.Helper()
	out := s.must(append(args, "--json")...)
	if err := json.Unmarshal([]byte(out), v); err != nil {
		s.t.Fatalf("satelle %s --json: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// create creates an item and returns it.
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
		s.t.Fatalf("git %v: %s%s", args, r.out, r.err)
	}
	return r.out
}

// write writes a file under the repo.
func (s *sandbox) write(rel, body string) {
	s.t.Helper()
	writeFile(s.t, filepath.Join(s.dir, rel), body, 0o644)
}

func writeFile(t *testing.T, p, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

// event is one ledger event as `ledger list --json` prints it.
type event struct {
	Item    string          `json:"item"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// gateRun is a gate.run payload.
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
}

// ledger returns the events of id (every item's when id is "") of one kind
// (every kind when kind is "").
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

// gateRuns returns id's gate.run payloads, oldest first.
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
