//go:build integration && unix

package tests

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/cli"
	"github.com/bobmcallan/satelle/internal/workflow"
)

// checkSandbox is a repo holding every check skill of this repo, a route
// "checks" that presents each one on a move out of "open", a task under
// "standing", and fakes for satelle (a recording wrapper) and gh.
type checkSandbox struct {
	*sandbox
	task   string   // the task whose runs are the execution fixtures
	checks []string // every check skill in the sandbox, sorted
	log    string   // the wrapper's call log
}

// recordingSatelle appends one NUL-separated record per call to
// $CHECKS_CALL_LOG: SATELLE_TO, SATELLE_ITEM, the argument count, then each
// argument. It then runs the real binary with stdin, stdout and the exit
// status untouched.
const recordingSatelle = `#!/usr/bin/env bash
printf '%s\0' "${SATELLE_TO:-}" "${SATELLE_ITEM:-}" "$#" "$@" >>"$CHECKS_CALL_LOG"
exec "$CHECKS_REAL_SATELLE" "$@"
`

// newCheckSandbox builds the fixture; extra (path -> body) is written after
// the repo's check skills, before the route is generated.
func newCheckSandbox(t *testing.T, extra map[string]string) *checkSandbox {
	t.Helper()
	for _, tool := range []string{"bash", "sh", "jq", "git", "awk"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s is required by the check skills and is not on PATH", tool)
		}
	}
	s := newSandbox(t)
	c := &checkSandbox{sandbox: s}
	s.env["SATELLE_SESSION"] = "checks-it"
	s.git("branch", "-M", "main")
	origin := t.TempDir()
	if r := s.exec("git", "", "init", "-q", "--bare", origin); r.code != 0 {
		t.Fatalf("%s", gitFailure([]string{"init", "--bare"}, r))
	}
	s.git("remote", "add", "origin", origin)
	s.must("init")
	s.write(".satelle/satelle.toml", `[workflows]
default = "checks"
[[workflows.rule]]
workflow = "standing"
kind = "task"
[[workflows.rule]]
workflow = "checks"
kind = "execution"
`)
	s.write(".satelle/workflows/standing.toml", `name = "standing"
[states.defined]
start = true
[states.retired]
terminal = true
[[transition]]
from = ["defined"]
to = "retired"
`)
	s.write(".satelle/skills/fixture-accept.md", "---\nname: fixture-accept\ntype: check\n---\n\n```sh\nexit 0\n```\n")
	for _, name := range repoCheckSkills(t) {
		s.write(".satelle/skills/"+name+".md", repoText(t, ".satelle/skills/"+name+".md"))
	}
	for p, body := range extra {
		s.write(p, body)
	}
	skills, err := workflow.LoadSkills(filepath.Join(s.dir, ".satelle/skills"))
	if err != nil {
		t.Fatal(err)
	}
	for name, sk := range skills {
		if sk.IsCheck() && name != "fixture-accept" {
			c.checks = append(c.checks, name)
		}
	}
	sort.Strings(c.checks)
	s.write(".satelle/workflows/checks.toml", checksRoute(t, c.checks))
	s.write(".version", "satelle.version: 0.0.1\nsatelled.version: 0.0.1\nsatelle.build: "+time.Now().UTC().Format(time.RFC3339)+"\n")
	s.write("CHANGELOG.md", "# Changelog\n\n## [0.0.1] - 2026-11-08\n\n### Fixed\n\n- A fixture entry.\n")
	s.must("validate")
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "checks fixture")
	s.git("push", "-q", "origin", "main")
	c.task = s.create("task", "--title", "fixture", "--input-kind", "story").ID

	bin := t.TempDir()
	writeFile(t, filepath.Join(bin, "gh"), "#!/bin/sh\necho '[]'\n", 0o755)
	writeFile(t, filepath.Join(bin, "satelle"), recordingSatelle, 0o755)
	s.path = append([]string{bin}, s.path...)
	c.log = filepath.Join(t.TempDir(), "calls")
	s.env["CHECKS_CALL_LOG"] = c.log
	s.env["CHECKS_REAL_SATELLE"] = satelleBin
	for _, k := range []string{"SATELLE_RELEASE_API", "SATELLE_RELEASE_LIST_API", "SATELLE_RELEASE_BASE"} {
		s.env[k] = "http://127.0.0.1:1"
	}
	return c
}

// repoCheckSkills names every check skill in this repo's .satelle/skills.
func repoCheckSkills(t *testing.T) []string {
	t.Helper()
	skills, err := workflow.LoadSkills("../.satelle/skills")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for name, sk := range skills {
		if sk.IsCheck() {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		t.Fatal("this repo has no check skill")
	}
	return out
}

// checksRoute is the route "checks": open (start) -> done (terminal), and for
// each check skill a state of its name reached from open through a gate of
// its name. A skill this repo's routes use as a trigger or an after check
// keeps that role in its gate; any other is the gate's skill.
func checksRoute(t *testing.T, checks []string) string {
	t.Helper()
	routes, err := workflow.Load("../.satelle/workflows")
	if err != nil {
		t.Fatal(err)
	}
	triggers, afters := map[string]bool{}, map[string]bool{}
	for _, w := range routes {
		for _, g := range w.Gates {
			if g.Trigger != "" {
				triggers[g.Trigger] = true
			}
			if g.After != "" {
				afters[g.After] = true
			}
		}
	}
	var b strings.Builder
	b.WriteString("name = \"checks\"\n[states.open]\nstart = true\n[states.done]\nterminal = true\n")
	for _, name := range checks {
		fmt.Fprintf(&b, "[states.%s]\n", name)
	}
	b.WriteString("\n[[transition]]\nfrom = [\"open\"]\nto = \"done\"\n")
	for _, name := range checks {
		fmt.Fprintf(&b, "\n[gates.%s]\n", name)
		switch {
		case triggers[name]:
			fmt.Fprintf(&b, "skill = \"fixture-accept\"\ntrigger = %q\n", name)
		case afters[name]:
			fmt.Fprintf(&b, "skill = \"fixture-accept\"\nafter = %q\n", name)
		default:
			fmt.Fprintf(&b, "skill = %q\n", name)
		}
		b.WriteString("timeout = \"2m\"\n")
		fmt.Fprintf(&b, "\n[[transition]]\nfrom = [\"open\"]\nto = %q\ngroups = [{ mode = \"sequential\", gates = [%q] }]\n", name, name)
	}
	return b.String()
}

// call is one satelle invocation a check script made.
type call struct {
	to, item string
	args     []string
}

func (c call) String() string { return "satelle " + strings.Join(c.args, " ") }

// calls decodes the wrapper's log, dropping records with no SATELLE_ITEM
// (not made by a check), and empties it.
func (c *checkSandbox) calls() []call {
	c.t.Helper()
	b, err := os.ReadFile(c.log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		c.t.Fatal(err)
	}
	if err := os.Remove(c.log); err != nil {
		c.t.Fatal(err)
	}
	fields := strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
	var out []call
	for i := 0; i < len(fields); {
		if i+3 > len(fields) {
			c.t.Fatalf("truncated call record at field %d of %q", i, b)
		}
		n, err := strconv.Atoi(fields[i+2])
		if err != nil || i+3+n > len(fields) {
			c.t.Fatalf("bad call record at field %d of %q", i, b)
		}
		rec := call{to: fields[i], item: fields[i+1], args: fields[i+3 : i+3+n]}
		i += 3 + n
		if rec.item != "" {
			out = append(out, rec)
		}
	}
	return out
}

// present moves id (of kind) to the state named after check and returns the
// result; it fails the test unless the kernel recorded a run of that gate.
func (c *checkSandbox) present(kind, id, check string) result {
	c.t.Helper()
	r := c.run(kind, "set", id, "--status", check)
	if r.code != 0 && r.code != 2 {
		c.t.Errorf("%s on %s %s: exit %d, want 0 or 2\n%s%s", check, kind, id, r.code, r.out, r.err)
	}
	ran := false
	for _, g := range c.gateRuns(id) {
		ran = ran || g.Gate == check
	}
	if !ran {
		c.t.Errorf("%s on %s %s: the kernel recorded no run of gate %s\n%s%s", check, kind, id, check, r.out, r.err)
	}
	return r
}

// resolveCall reports whether args name a command of satelle's own command
// tree with flags it has and an arity it accepts.
func resolveCall(args []string) error {
	root := cli.NewRootCmd()
	c, rest, err := root.Find(args)
	if err != nil {
		return fmt.Errorf("satelle %s: %v", strings.Join(args, " "), err)
	}
	if !c.Runnable() {
		for _, a := range rest {
			if !strings.HasPrefix(a, "-") {
				return fmt.Errorf("satelle %s: %q is not a command of %s", strings.Join(args, " "), a, c.CommandPath())
			}
		}
	}
	if err := c.ParseFlags(rest); err != nil {
		return fmt.Errorf("satelle %s: %v", strings.Join(args, " "), err)
	}
	if err := c.ValidateArgs(c.Flags().Args()); err != nil {
		return fmt.Errorf("satelle %s: %v", strings.Join(args, " "), err)
	}
	return nil
}

// Every check skill of this repo runs through a real transition on a story and
// on an execution, reads no item id from stdin, and every satelle call it makes
// names a command, flags and an arity the binary has.
func TestEveryCheckSkillRunsOnTheKernel(t *testing.T) {
	t.Parallel()
	c := newCheckSandbox(t, nil)
	skills, err := workflow.LoadSkills("../.satelle/skills")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range c.checks {
		if sc := skills[name].Script; strings.Contains(sc, `"id":`) || strings.Contains(sc, "sty_[") {
			t.Errorf("%s reads the item id from the stdin payload; read $SATELLE_ITEM", name)
		}
	}
	recorded := 0
	for _, name := range c.checks {
		story := c.create("story", "--title", name).ID
		var exe struct {
			ID string `json:"id"`
		}
		c.mustJSON(&exe, "task", "run", c.task)
		for _, it := range []struct{ kind, id string }{{"story", story}, {"execution", exe.ID}} {
			c.present(it.kind, it.id, name)
			for _, cl := range c.calls() {
				recorded++
				if err := resolveCall(cl.args); err != nil {
					t.Errorf("%s (presented as %s on %s %s): %v", name, cl.to, it.kind, cl.item, err)
				}
			}
		}
	}
	if recorded == 0 {
		t.Fatal("no check called satelle through the recording wrapper; is it on PATH?")
	}
}

// A call to a removed verb or flag is caught even when the binary exits 0 on it
// (satelle story docs prints the group's help).
func TestCheckCallResolverCatchesRemovedCommands(t *testing.T) {
	t.Parallel()
	c := newCheckSandbox(t, map[string]string{
		".satelle/skills/fixture-removed.md": "---\nname: fixture-removed\ntype: check\n---\n\n```sh\n" +
			"satelle story docs \"$SATELLE_ITEM\" || true\n" +
			"satelle story get \"$SATELLE_ITEM\" --json >/dev/null\n" +
			"satelle story diff \"$SATELLE_ITEM\" --recorded --json || true\n" +
			"exit 0\n```\n",
	})
	id := c.create("story", "--title", "removed").ID
	c.present("story", id, "fixture-removed")
	var bad []string
	calls := c.calls()
	for _, cl := range calls {
		if err := resolveCall(cl.args); err != nil {
			bad = append(bad, cl.String())
		}
	}
	want := []string{
		"satelle story docs " + id,
		"satelle story diff " + id + " --recorded --json",
	}
	if strings.Join(bad, "\n") != strings.Join(want, "\n") {
		t.Fatalf("resolver rejected %q, want %q (calls %v)", bad, want, calls)
	}
}

// The static audit: drift-check.sh finds no command or flag miss in any check
// skill, which covers branches and fix-it lines the fixture run does not reach.
func TestCheckSkillsNameOnlyRealCommands(t *testing.T) {
	t.Parallel()
	for _, tool := range []string{"bash", "jq", "git", "awk"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s is required by scripts/drift-check.sh and is not on PATH", tool)
		}
	}
	s := newSandbox(t)
	s.env["SATELLE_BIN"] = satelleBin
	s.must("init")
	for _, name := range repoCheckSkills(t) {
		s.write(".satelle/skills/"+name+".md", repoText(t, ".satelle/skills/"+name+".md"))
	}
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "check skills")
	r := driftCheck(t, s)
	if r.code == 2 {
		t.Fatalf("drift-check could not run: exit 2\n%s%s", r.out, r.err)
	}
	for _, line := range strings.Split(r.out, "\n") {
		if strings.Contains(line, ": command: ") || strings.Contains(line, ": flag: ") {
			t.Errorf("check skill drift: %s", line)
		}
	}
}

// satelle-ac-evidence-check reads the story from SATELLE_ITEM and its
// attachments from the binary: it rejects a story without ac-evidence, naming
// it, and accepts once the attachment is there.
func TestAcEvidenceCheckReadsTheItem(t *testing.T) {
	t.Parallel()
	const check = "satelle-ac-evidence-check"
	c := newCheckSandbox(t, nil)
	id := c.create("story", "--title", "evidence").ID
	last := func() gateRun {
		var out gateRun
		for _, g := range c.gateRuns(id) {
			if g.Gate == check {
				out = g
			}
		}
		return out
	}
	if r := c.present("story", id, check); r.code != 2 {
		t.Fatalf("no ac-evidence: exit %d, want 2\n%s%s", r.code, r.out, r.err)
	}
	if g := last(); g.Result != "reject" || !strings.Contains(g.Notes, id) || !strings.Contains(g.Notes, "ac-evidence") {
		t.Fatalf("no ac-evidence: gate.run %+v, want a reject naming %s and ac-evidence", g, id)
	}
	if r := c.exec(satelleBin, "n/a", "story", "attach", id, "--name", "ac-evidence", "--file", "-"); r.code != 0 {
		t.Fatalf("attach: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if r := c.present("story", id, check); r.code != 0 {
		t.Fatalf("with ac-evidence: exit %d, want 0\n%s%s", r.code, r.out, r.err)
	}
	if g := last(); g.Result != "accept" {
		t.Fatalf("with ac-evidence: gate.run %+v, want accept", g)
	}
	if st := c.get(id).Status; st != check {
		t.Fatalf("with ac-evidence: status %q, want %q", st, check)
	}
}
