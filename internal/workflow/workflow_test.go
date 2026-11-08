package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/item"
)

var testSkills = map[string]Skill{
	"judge":  {Name: "judge", Meta: map[string]string{}},
	"script": {Name: "script", Meta: map[string]string{"type": TypeCheck}, Script: "true\n"},
	"work":   {Name: "work", Meta: map[string]string{}},
}

func skillOf(n string) (Skill, bool) { s, ok := testSkills[n]; return s, ok }
func agentOf(n string) bool          { return n == "a1" || n == "a2" }

func parse(t *testing.T, body string) (*Workflow, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "wf.toml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return Parse(p)
}

const valid = `
[states.s0]
start = true
[states.s1]
performer = { agent = "a1", skill = "work" }
[states.hold]
park = true
resumes = true
[states.end]
terminal = true
enqueue = ["que_00000001", "que_00000002"]

[gates.g1]
skill = "judge"
panel = ["a1", "a2"]
combine = "majority"
paths = ["**/*.go"]
carry = true
[gates.g2]
skill = "script"
trigger = "script"
after = ["script"]
timeout = "30m"

[[transition]]
from = ["s0"]
to = "s1"
groups = [{ mode = "sequential", gates = ["g2", "g1"] }, { mode = "parallel", gates = ["g1", "g2"] }]
[[transition]]
from = ["*"]
to = "hold"
[[transition]]
from = ["hold"]
to = "@prior"
[[transition]]
from = ["s1"]
to = "end"
`

func TestValidWorkflow(t *testing.T) {
	w, err := parse(t, valid)
	if err != nil {
		t.Fatal(err)
	}
	if errs := w.Validate(skillOf, agentOf); len(errs) > 0 {
		t.Fatalf("unexpected problems: %v", errs)
	}
	if got := w.States["end"].Enqueue; strings.Join(got, ",") != "que_00000001,que_00000002" {
		t.Fatalf("enqueue %v", got)
	}
	if strings.Join(w.Gates["g2"].After, ",") != "script" {
		t.Fatalf("after %v", w.Gates["g2"].After)
	}
	if d, err := w.Gates["g2"].Bound(); err != nil || d != 30*time.Minute {
		t.Fatalf("g2 bound %v %v", d, err)
	}
	if d, err := w.Gates["g1"].Bound(); err != nil || d != 0 {
		t.Fatalf("g1 bound %v %v", d, err)
	}
	if w.Name != "wf" || w.StartState() != "s0" {
		t.Fatalf("name %q start %q", w.Name, w.StartState())
	}
	if tr, err := w.Find("s1", "hold", ""); err != nil || tr.From[0] != "*" {
		t.Fatalf("find wildcard: %v", err)
	}
	if _, err := w.Find("end", "hold", ""); err == nil {
		t.Fatal("terminal matched *")
	}
	if tr, err := w.Find("hold", "s1", "s1"); err != nil || tr.To != Prior {
		t.Fatalf("find prior: %v", err)
	}
	if _, err := w.Find("s0", "end", ""); err == nil {
		t.Fatal("found a transition that does not exist")
	}
	if got := w.Targets("s1", ""); strings.Join(got, ",") != "hold,end" {
		t.Fatalf("targets %v", got)
	}
}

func TestInvalidWorkflows(t *testing.T) {
	base := "[states.a]\nstart = true\n[states.z]\nterminal = true\n[[transition]]\nfrom = [\"a\"]\nto = \"z\"\n"
	cases := []struct {
		name, body, want string
	}{
		{"unknown key", base + "colour = 1\n", "unknown key transition.colour"},
		{"no start", "[states.z]\nterminal = true\n", "exactly one start"},
		{"two starts", base + "[states.b]\nstart = true\n", "exactly one start"},
		{"no terminal", "[states.a]\nstart = true\n", "at least one terminal"},
		{"missing to", base + "[[transition]]\nfrom = [\"a\"]\nto = \"q\"\n", `to state "q" does not exist`},
		{"missing from", base + "[[transition]]\nfrom = [\"q\"]\nto = \"z\"\n", `from state "q" does not exist`},
		{"missing gate", base + "[[transition]]\nfrom = [\"a\"]\nto = \"a\"\ngroups = [{gates = [\"nope\"]}]\n", `gate "nope" is not defined`},
		{"missing skill", base + "[gates.g]\nskill = \"nope\"\nagent = \"a1\"\n", `skill "nope"`},
		{"missing agent", base + "[gates.g]\nskill = \"judge\"\nagent = \"zz\"\n", `agent "zz"`},
		{"judge without agent", base + "[gates.g]\nskill = \"judge\"\n", "neither agent nor panel"},
		{"check with agent", base + "[gates.g]\nskill = \"script\"\nagent = \"a1\"\n", "a check runs no agent"},
		{"bad combine", base + "[gates.g]\nskill = \"judge\"\npanel = [\"a1\"]\ncombine = \"most\"\n", "combine"},
		{"bad mode", base + "[gates.g]\nskill = \"script\"\n[[transition]]\nfrom = [\"a\"]\nto = \"a\"\ngroups = [{mode = \"x\", gates = [\"g\"]}]\n", `mode "x"`},
		{"prior from non-park", base + "[[transition]]\nfrom = [\"a\"]\nto = \"@prior\"\n", "only allowed from a resuming park"},
		{"prior from non-resuming park", base + "[states.p]\npark = true\n[[transition]]\nfrom = [\"a\"]\nto = \"p\"\n[[transition]]\nfrom = [\"p\"]\nto = \"@prior\"\n", "only allowed from a resuming park"},
		{"unreachable", base + "[states.island]\n", `"island" is not reachable`},
		{"terminal unreachable", "[states.a]\nstart = true\n[states.z]\nterminal = true\n", "no terminal state is reachable"},
		{"performer agent", base + "[states.b]\nperformer = { agent = \"zz\", skill = \"work\" }\n[[transition]]\nfrom = [\"a\"]\nto = \"b\"\n", "performer agent"},
		{"duplicate", base + "[[transition]]\nfrom = [\"a\"]\nto = \"z\"\n", "duplicates"},
		{"from terminal", base + "[[transition]]\nfrom = [\"z\"]\nto = \"a\"\n", "is terminal"},
		{"empty enqueue", base + "[states.e]\nenqueue = [\"\"]\n", "empty queue name"},
		{"duplicate enqueue", base + "[states.e]\nenqueue = [\"que_1\", \"que_1\"]\n", "enqueues into que_1 twice"},
		{"after missing", base + "[gates.g]\nskill = \"script\"\nafter = [\"nope\"]\n", `after "nope" is not a check skill`},
		{"after not a check", base + "[gates.g]\nskill = \"script\"\nafter = [\"judge\"]\n", `after "judge" is not a check skill`},
		{"after duplicate", base + "[gates.g]\nskill = \"script\"\nafter = [\"script\", \"script\"]\n", `after lists "script" twice`},
		{"after second not a check", base + "[gates.g]\nskill = \"script\"\nafter = [\"script\", \"judge\"]\n", `after "judge" is not a check skill`},
		{"after not a string", base + "[gates.g]\nskill = \"script\"\nafter = 3\n", "is not a skill name"},
		{"after list of non-strings", base + "[gates.g]\nskill = \"script\"\nafter = [\"script\", 3]\n", "is not a skill name"},
		{"after as a string not a check", base + "[gates.g]\nskill = \"script\"\nafter = \"judge\"\n", `after "judge" is not a check skill`},
		{"timeout not a duration", base + "[gates.g]\nskill = \"script\"\ntimeout = \"soon\"\n", `timeout "soon" is not a duration`},
		{"timeout zero", base + "[gates.g]\nskill = \"script\"\ntimeout = \"0s\"\n", `timeout "0s" must be positive`},
		{"timeout negative", base + "[gates.g]\nskill = \"script\"\ntimeout = \"-1m\"\n", `timeout "-1m" must be positive`},
		{"timeout on judge", base + "[gates.g]\nskill = \"judge\"\nagent = \"a1\"\ntimeout = \"5m\"\n", "this gate runs none"},
		{"resumes without park", base + "[states.a2]\nresumes = true\n", "not a park state"},
		{"operator with agent", base + "[states.b]\nperformer = { operator = \"x\", agent = \"a1\" }\n[[transition]]\nfrom = [\"a\"]\nto = \"b\"\n", `state "b" performer is the operator; it takes no agent or skill`},
		{"operator with skill", base + "[states.b]\nperformer = { operator = \"x\", skill = \"work\" }\n[[transition]]\nfrom = [\"a\"]\nto = \"b\"\n", "it takes no agent or skill"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, err := parse(t, c.body)
			var msgs []string
			if err != nil {
				msgs = append(msgs, err.Error())
			} else {
				for _, e := range w.Validate(skillOf, agentOf) {
					msgs = append(msgs, e.Error())
				}
			}
			joined := strings.Join(msgs, "\n")
			if !strings.Contains(joined, c.want) {
				t.Fatalf("want %q in:\n%s", c.want, joined)
			}
		})
	}
}

func TestRules(t *testing.T) {
	r := Rules{Default: "d", Rule: []Rule{{Workflow: "w1", Category: "c"}, {Workflow: "w2", Tag: "t"}, {Workflow: "w3", Kind: "k"}}}
	for _, c := range []struct {
		it   item.Item
		want string
	}{
		{item.Item{Category: "c", Tags: []string{"t"}}, "w1"},
		{item.Item{Tags: []string{"t"}}, "w2"},
		{item.Item{Kind: "k"}, "w3"},
		{item.Item{Kind: "other"}, "d"},
	} {
		if got := r.Select(c.it); got != c.want {
			t.Fatalf("%+v: got %s want %s", c.it, got, c.want)
		}
	}
	errs := Rules{Default: "x", Rule: []Rule{{Workflow: "d"}}}.Validate(func(n string) bool { return n == "d" })
	if len(errs) != 2 {
		t.Fatalf("rule errors %v", errs)
	}
}

func TestSkills(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "c.md"), []byte("---\ntype: check\ndescription: x\n---\n# C\n\n```sh\necho hi\nexit 0\n```\n\n```sh\nnot this\n```\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "j.md"), []byte("# Judge\nRead the diff.\n"), 0o644)
	skills, err := LoadSkills(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := skills["c"]
	if !c.IsCheck() || c.Script != "echo hi\nexit 0\n" || !strings.HasPrefix(c.Body, "# C") {
		t.Fatalf("check skill %+v", c)
	}
	if skills["j"].IsCheck() || skills["j"].Body != "# Judge\nRead the diff.\n" {
		t.Fatalf("judge skill %+v", skills["j"])
	}
	os.WriteFile(filepath.Join(dir, "bad.md"), []byte("---\ntype: check\n---\nno script\n"), 0o644)
	if _, err := LoadSkills(dir); err == nil {
		t.Fatal("check without script accepted")
	}
}

func TestValidateHold(t *testing.T) {
	const states = "[states.a]\nstart = true\n[states.p]\npark = true\nresumes = true\n[states.q]\npark = true\n[states.z]\nterminal = true\n" +
		"[gates.g]\nskill = \"judge\"\nagent = \"a1\"\n" +
		"[[transition]]\nfrom = [\"a\"]\nto = \"z\"\ngroups = [{ gates = [\"g\"] }]\n" +
		"[[transition]]\nfrom = [\"p\"]\nto = \"@prior\"\n" +
		"[[transition]]\nfrom = [\"a\"]\nto = \"q\"\n"
	toP := "[[transition]]\nfrom = [\"a\"]\nto = \"p\"\n"
	hold := func(rounds int, to string) string {
		return fmt.Sprintf("[states.a.hold]\nrounds = %d\nto = %q\n", rounds, to)
	}
	valid := states + toP + hold(2, "p")
	w, err := parse(t, valid)
	if err != nil {
		t.Fatal(err)
	}
	if errs := w.Validate(skillOf, agentOf); len(errs) > 0 {
		t.Fatalf("valid hold refused: %v", errs)
	}
	if h := w.States["a"].Hold; h == nil || h.Rounds != 2 || h.To != "p" {
		t.Fatalf("hold decoded as %+v", h)
	}
	cases := []struct{ name, body, want string }{
		{"rounds zero", states + toP + hold(0, "p"), "rounds 0 must be at least 1"},
		{"to missing", states + toP + "[states.a.hold]\nrounds = 2\n", `to "" is not a park state`},
		{"to not park", states + toP + hold(2, "z"), `to "z" is not a park state`},
		{"to unknown", states + toP + hold(2, "nope"), `to "nope" is not a park state`},
		{"on a park state", states + toP + "[states.q.hold]\nrounds = 2\nto = \"p\"\n", "a terminal or park state cannot be held"},
		{"on a terminal state", states + toP + "[states.z.hold]\nrounds = 2\nto = \"p\"\n", "a terminal or park state cannot be held"},
		{"no declared move", states + hold(2, "p"), `no transition is declared from "a" to "p"`},
		{"gated move", states + "[[transition]]\nfrom = [\"a\"]\nto = \"p\"\ngroups = [{ gates = [\"g\"] }]\n" + hold(2, "p"), "carries gates; the move into a hold must be gateless"},
		{"gated wildcard move", states + "[[transition]]\nfrom = [\"*\"]\nto = \"p\"\ngroups = [{ gates = [\"g\"] }]\n" + hold(2, "p"), "carries gates"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, err := parse(t, c.body)
			if err != nil {
				t.Fatal(err)
			}
			var msgs []string
			for _, e := range w.Validate(skillOf, agentOf) {
				msgs = append(msgs, e.Error())
			}
			if joined := strings.Join(msgs, "\n"); !strings.Contains(joined, c.want) {
				t.Fatalf("want %q in:\n%s", c.want, joined)
			}
		})
	}
}

func TestOperatorPerformer(t *testing.T) {
	w, err := parse(t, `
[states.a]
start = true
[states.deploy]
performer = { operator = "deploy-record" }
[states.work]
performer = { agent = "a1", skill = "work" }
[states.z]
terminal = true
[[transition]]
from = ["a"]
to = "deploy"
[[transition]]
from = ["deploy"]
to = "work"
[[transition]]
from = ["work"]
to = "z"
`)
	if err != nil {
		t.Fatal(err)
	}
	if errs := w.Validate(skillOf, agentOf); len(errs) > 0 {
		t.Fatalf("unexpected problems: %v", errs)
	}
	if typ, ok := w.States["deploy"].Operated(); !ok || typ != "deploy-record" {
		t.Fatalf("Operated() = %q, %v", typ, ok)
	}
	if p := w.States["deploy"].Agent(); p != nil {
		t.Fatalf("an operator state has no agent performer, got %+v", p)
	}
	if _, ok := w.States["work"].Operated(); ok {
		t.Fatal("an agent state is not the operator's")
	}
	if p := w.States["work"].Agent(); p == nil || p.Agent != "a1" {
		t.Fatalf("Agent() = %+v", p)
	}
	if _, ok := w.States["a"].Operated(); ok || w.States["a"].Agent() != nil {
		t.Fatal("a state with no performer is neither")
	}
}

func TestSkillProblems(t *testing.T) {
	cases := []struct {
		name, text string
		bad        bool
	}{
		{"oldcheck", "---\ntype: skill\n---\n# Old\n\n```check\nexit 0\n```\n", true},
		{"untyped", "# Old\n\n  ```check\nexit 0\n```\n", true},
		{"check", "---\ntype: check\n---\n# C\n\n```check\nexit 0\n```\n", false},
		{"procedure", "---\ntype: skill\n---\n# Release\n\n```sh\nmake release\n```\n\n```sh\ngit tag\n```\n", false},
		{"prose", "---\ntype: skill\n---\nA check is a fenced ```check block.\n", false},
	}
	for _, c := range cases {
		s, err := ParseSkill(c.name, c.text)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		probs := s.Problems()
		if got := len(probs) > 0; got != c.bad {
			t.Errorf("%s: problems %v, want bad=%v", c.name, probs, c.bad)
			continue
		}
		if c.bad && (!strings.Contains(probs[0].Error(), "skill "+c.name) || !strings.Contains(probs[0].Error(), "not check")) {
			t.Errorf("%s: problem %q does not name the skill and its type", c.name, probs[0])
		}
	}
}

// The form before after took a list still loads: a string is a one-entry
// list, an empty string none, and the gate validates as the list form does.
func TestAfterStringFormLoads(t *testing.T) {
	base := "[states.a]\nstart = true\n[states.z]\nterminal = true\n[[transition]]\nfrom = [\"a\"]\nto = \"z\"\n"
	for body, want := range map[string]string{
		"after = \"script\"\n":              "script",
		"after = [\"script\"]\n":            "script",
		"after = [\"script\", \"other\"]\n": "script,other",
		"after = \"\"\n":                    "",
		"":                                  "",
	} {
		w, err := parse(t, base+"[gates.g]\nskill = \"script\"\n"+body)
		if err != nil {
			t.Fatalf("%q: %v", body, err)
		}
		if got := strings.Join(w.Gates["g"].After, ","); got != want {
			t.Errorf("%q: after %q, want %q", body, got, want)
		}
		if want == "" && len(w.Gates["g"].After) != 0 {
			t.Errorf("%q: after %q, want none", body, w.Gates["g"].After)
		}
		if want == "script" {
			if errs := w.Validate(skillOf, agentOf); len(errs) > 0 {
				t.Errorf("%q: %v", body, errs)
			}
		}
	}
}
