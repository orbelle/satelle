package agentstep

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// The wiring guard (sty_f141c77f): a performer is never dispatched ungated into
// a linked worktree. Refuse by default, run visibly ungated only where the
// repository declares fail-open, for step dispatches and driving sessions.

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// mainAndLinked makes a repository and a linked worktree of it.
func mainAndLinked(t *testing.T) (mainRoot, linked string) {
	t.Helper()
	mainRoot, _ = filepath.EvalSymlinks(t.TempDir())
	gitIn(t, mainRoot, "init", "-q")
	gitIn(t, mainRoot, "commit", "-q", "--allow-empty", "-m", "init")
	linked = filepath.Join(filepath.Dir(mainRoot), filepath.Base(mainRoot)+"-wt")
	gitIn(t, mainRoot, "worktree", "add", "-q", linked, "-b", "wt")
	t.Cleanup(func() { _ = os.RemoveAll(linked) })
	return mainRoot, linked
}

func writeIn(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// wiringRunner records whether the performer was started.
type wiringRunner struct {
	mu   sync.Mutex
	runs int
}

func (c *wiringRunner) Name() string    { return "counting" }
func (c *wiringRunner) Command() string { return "fake -p {system}" }
func (c *wiringRunner) Run(context.Context, agentcli.Request) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.runs++
	return nil, nil
}

type guardRig struct {
	g      *Engine
	warn   *bytes.Buffer
	rows   *[]map[string]any
	runner *wiringRunner
	opens  *int
}

// newGuardRig is an engine rooted at root whose named binding runs command, with
// the wiring guard built from cfg through the same constructor the CLI uses.
func newGuardRig(t *testing.T, root, command string, cfg config.Config) guardRig {
	t.Helper()
	runner := &wiringRunner{}
	g := New(runner, fakeDocs{workflow: dispatchWF}, root, "")
	g.newRunner = func(string, string) (agentcli.Runner, error) { return runner, nil }
	opens := 0
	g.newOpener = func(string, string) (agentcli.SessionOpener, error) {
		return func(context.Context, agentcli.Request, agentcli.PermissionPolicy) (agentcli.Session, error) {
			opens++
			return closedSess{}, nil
		}, nil
	}
	g.SetNamedAgents(func(string) (config.AgentBinding, bool) {
		return config.AgentBinding{Interface: "stream", Command: command, Tools: "Read,read,read_file,grep,list_dir"}, true
	})
	warn := &bytes.Buffer{}
	g.SetWarnWriter(warn)
	var rows []map[string]any
	g.SetInvocationRecorder(func(_ context.Context, _ string, p map[string]any) error {
		rows = append(rows, p)
		return nil
	})
	g.SetWiringGuard(WiringGuardFrom(cfg))
	return guardRig{g: g, warn: warn, rows: &rows, runner: runner, opens: &opens}
}

func (r guardRig) dispatch() error {
	_, err := r.g.DispatchExecutor(context.Background(), workitem.Item{ID: "sty_w", Status: "backlog"}, "plan")
	return err
}

func (r guardRig) drive() error {
	sess, err := r.g.OpenSessionAsWithModel(context.Background(), "coder", SessionRoleDriving,
		workitem.Item{ID: "sty_w", Status: "in_progress"}, nil, nil, "")
	if sess != nil {
		_ = sess.Close()
	}
	return err
}

func (r guardRig) started() int { return r.runner.runs + *r.opens }

func (r guardRig) ungatedRows() int {
	n := 0
	for _, row := range *r.rows {
		if row["phase"] == "ungated_dispatch" {
			n++
		}
	}
	return n
}

func failOpen() config.Config {
	var c config.Config
	c.Worktree.AbsentWiring = config.AbsentWiringFailOpen
	return c
}

// One case per adapter. wanted is what a refusal must name: the harness and each
// missing path.
var guardAdapters = []struct {
	name, command string
	wanted        []string
}{
	{"claude", "claude -p {system}", []string{"harness claude", ".claude/settings.json"}},
	{"grok", "grok -p {system}", []string{"harness grok", ".grok/hooks/satelle.json"}},
	{"pi", "/usr/local/bin/pi -p {system}", []string{"harness pi", ".pi/extensions/satelle.ts"}},
	{"unknown", "mybot -p {system}", []string{"harness unknown", `executable "mybot"`, "no gate wiring declared"}},
}

// AC1: refused before it starts, naming the harness and each missing path; both
// entry points (step dispatch and driving session).
func TestWiringGuardRefusesAbsentWiring(t *testing.T) {
	for _, ad := range guardAdapters {
		for _, path := range []string{"dispatch", "driving session"} {
			t.Run(ad.name+"/"+path, func(t *testing.T) {
				_, linked := mainAndLinked(t)
				r := newGuardRig(t, linked, ad.command, config.Config{})
				var err error
				if path == "dispatch" {
					err = r.dispatch()
				} else {
					err = r.drive()
				}
				if err == nil {
					t.Fatal("a performer in a worktree without its wiring must be refused")
				}
				for _, w := range append(ad.wanted, "dispatch refused", linked) {
					if !strings.Contains(err.Error(), w) {
						t.Errorf("refusal %q does not name %q", err, w)
					}
				}
				if r.started() != 0 {
					t.Error("the performer started despite the refusal")
				}
			})
		}
	}
}

// AC1: under a declared fail-open it proceeds, and says it is ungated.
func TestWiringGuardFailOpenRunsVisiblyUngated(t *testing.T) {
	for _, ad := range guardAdapters {
		for _, path := range []string{"dispatch", "driving session"} {
			t.Run(ad.name+"/"+path, func(t *testing.T) {
				_, linked := mainAndLinked(t)
				r := newGuardRig(t, linked, ad.command, failOpen())
				var err error
				if path == "dispatch" {
					err = r.dispatch()
				} else {
					err = r.drive()
				}
				if err != nil {
					t.Fatalf("fail-open must let the dispatch proceed: %v", err)
				}
				if r.started() != 1 {
					t.Fatalf("started = %d, want 1", r.started())
				}
				out := r.warn.String()
				for _, w := range []string{"UNGATED", "fail-open", "without edit/commit gates"} {
					if !strings.Contains(out, w) {
						t.Errorf("output %q does not name %q", out, w)
					}
				}
				if r.ungatedRows() != 1 {
					t.Errorf("want one ungated_dispatch ledger row, got %v", *r.rows)
				}
			})
		}
	}
}

// Wiring present: no refusal and nothing ungated is reported; the main tree and
// a read-only consult session are out of scope.
func TestWiringGuardPassesWhenWiringPresentOrOutOfScope(t *testing.T) {
	mainRoot, linked := mainAndLinked(t)
	writeIn(t, linked, ".claude/settings.json", "{}")
	r := newGuardRig(t, linked, "claude -p {system}", config.Config{})
	if err := r.dispatch(); err != nil {
		t.Fatalf("present wiring: %v", err)
	}
	if r.warn.Len() != 0 || r.ungatedRows() != 0 {
		t.Errorf("present wiring must be silent, got %q %v", r.warn.String(), *r.rows)
	}

	m := newGuardRig(t, mainRoot, "claude -p {system}", config.Config{})
	if err := m.dispatch(); err != nil {
		t.Fatalf("the main tree is covered by drift detection, not the guard: %v", err)
	}

	c := newGuardRig(t, linked, "mybot -p {system}", config.Config{})
	sess, err := c.g.OpenSessionAsWithModel(context.Background(), "coder", SessionRoleConsult,
		workitem.Item{ID: "sty_w", Status: "in_progress"}, nil, nil, "")
	if err != nil {
		t.Fatalf("a consult session is read-only and unguarded: %v", err)
	}
	_ = sess.Close()
}

// The hook wrapper a present wiring file calls must exist where the call points.
func TestWiringGuardChecksTheWrapperTheWiringCalls(t *testing.T) {
	_, linked := mainAndLinked(t)
	holder, _ := filepath.EvalSymlinks(t.TempDir())
	wrapper := filepath.Join(holder, ".satelle", "hooks", "satelle-hook.sh")
	writeIn(t, linked, ".claude/settings.json",
		`{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"sh `+wrapper+` gate claude"}]}]}}`)

	r := newGuardRig(t, linked, "claude -p {system}", config.Config{})
	err := r.dispatch()
	if err == nil || !strings.Contains(err.Error(), wrapper) {
		t.Fatalf("a missing wrapper must be named, got %v", err)
	}
	if r.started() != 0 {
		t.Error("the performer started with a missing wrapper")
	}

	writeIn(t, holder, ".satelle/hooks/satelle-hook.sh", "#!/bin/sh\n")
	r = newGuardRig(t, linked, "claude -p {system}", config.Config{})
	if err := r.dispatch(); err != nil {
		t.Fatalf("with the wrapper present: %v", err)
	}
}

// A comment that merely mentions the wrapper is documentation, not a call; the
// call itself is the quoted absolute path.
func TestWiringGuardIgnoresCommentedWrapperMention(t *testing.T) {
	_, linked := mainAndLinked(t)
	holder, _ := filepath.EvalSymlinks(t.TempDir())
	writeIn(t, holder, ".satelle/hooks/satelle-hook.sh", "#!/bin/sh\n")
	wrapper := filepath.Join(holder, ".satelle", "hooks", "satelle-hook.sh")
	writeIn(t, linked, ".pi/extensions/satelle.ts",
		"// PreToolUse. `sh .satelle/hooks/satelle-hook.sh <gate> pi`.\nconst WRAPPER: string = \""+wrapper+"\";\n")
	r := newGuardRig(t, linked, "pi -p {system}", config.Config{})
	if err := r.dispatch(); err != nil {
		t.Fatalf("a commented mention must not demand a wrapper in the worktree: %v", err)
	}
}

// A relative wrapper reference is resolved under the dispatch tree.
func TestWiringGuardResolvesRelativeWrapperUnderTheTree(t *testing.T) {
	_, linked := mainAndLinked(t)
	writeIn(t, linked, ".claude/settings.json", `{"command":"sh .satelle/hooks/satelle-hook.sh gate claude"}`)
	r := newGuardRig(t, linked, "claude -p {system}", config.Config{})
	if err := r.dispatch(); err == nil || !strings.Contains(err.Error(), ".satelle/hooks/satelle-hook.sh") {
		t.Fatalf("relative wrapper absent from the tree must be named, got %v", err)
	}
	writeIn(t, linked, ".satelle/hooks/satelle-hook.sh", "#!/bin/sh\n")
	r = newGuardRig(t, linked, "claude -p {system}", config.Config{})
	if err := r.dispatch(); err != nil {
		t.Fatal(err)
	}
}

// loadMain loads the main tree's satelle.toml as the process of record.
func loadMain(t *testing.T, mainRoot, body string) config.Config {
	t.Helper()
	writeIn(t, mainRoot, ".satelle/satelle.toml", body)
	cfg, _, err := config.Load(filepath.Join(mainRoot, ".satelle", "satelle.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// AC3: only the main tree's configuration changes the outcome, with no code
// change — which paths a harness requires and the absent-wiring policy.
func TestWiringGuardOutcomeFollowsMainTreeConfiguration(t *testing.T) {
	mainRoot, linked := mainAndLinked(t)
	writeIn(t, linked, ".mybot/hooks.json", "{}") // present in the worktree

	type outcome struct {
		refused  bool
		ungated  bool
		mentions string
	}
	run := func(cfg config.Config) outcome {
		r := newGuardRig(t, linked, "mybot -p {system}", cfg)
		err := r.dispatch()
		o := outcome{refused: err != nil, ungated: strings.Contains(r.warn.String(), "UNGATED")}
		if err != nil {
			o.mentions = err.Error()
		}
		return o
	}

	// (a) a declared path that is present proceeds, silently.
	cfg := loadMain(t, mainRoot, "[harness.unknown]\ngate_wiring = [\".mybot/hooks.json\"]\n")
	if o := run(cfg); o.refused || o.ungated {
		t.Errorf("(a) declared and present: %+v", o)
	}
	// (b) the same declaration naming an absent path is refused, naming it.
	cfg = loadMain(t, mainRoot, "[harness.unknown]\ngate_wiring = [\".mybot/other.json\"]\n")
	if o := run(cfg); !o.refused || !strings.Contains(o.mentions, ".mybot/other.json") {
		t.Errorf("(b) declared and absent: %+v", o)
	}
	// (c) an explicit refuse policy refuses.
	cfg = loadMain(t, mainRoot, "[worktree]\nabsent_wiring = \"refuse\"\n")
	if o := run(cfg); !o.refused || o.ungated {
		t.Errorf("(c) refuse: %+v", o)
	}
	// (d) fail-open proceeds, visibly.
	cfg = loadMain(t, mainRoot, "[worktree]\nabsent_wiring = \"fail-open\"\n")
	if o := run(cfg); o.refused || !o.ungated {
		t.Errorf("(d) fail-open: %+v", o)
	}
	// (e) fail-open declared only in the worktree's own configuration is not
	// consulted: the guard is built from the main tree's configuration.
	writeIn(t, linked, ".satelle/satelle.toml", "[worktree]\nabsent_wiring = \"fail-open\"\n")
	own, _, err := config.Load(filepath.Join(linked, ".satelle", "satelle.toml"))
	if err != nil || own.AbsentWiringPolicy() != config.AbsentWiringFailOpen {
		t.Fatalf("worktree config did not load as expected: %v %q", err, own.AbsentWiringPolicy())
	}
	cfg = loadMain(t, mainRoot, "")
	if o := run(cfg); !o.refused {
		t.Errorf("(e) worktree-only fail-open must not change the outcome: %+v", o)
	}
}

// An engine nobody wired a guard onto dispatches as before (tests, non-CLI use).
func TestWiringGuardUnwiredEngineDoesNotGuard(t *testing.T) {
	_, linked := mainAndLinked(t)
	r := newGuardRig(t, linked, "claude -p {system}", config.Config{})
	r.g.wiring = nil
	if err := r.dispatch(); err != nil {
		t.Fatal(err)
	}
}
