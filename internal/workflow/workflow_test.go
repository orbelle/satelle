package workflow

import (
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
after = "script"
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
	if w.Gates["g2"].After != "script" {
		t.Fatalf("after %q", w.Gates["g2"].After)
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
		{"after missing", base + "[gates.g]\nskill = \"script\"\nafter = \"nope\"\n", `after "nope" is not a check skill`},
		{"after not a check", base + "[gates.g]\nskill = \"script\"\nafter = \"judge\"\n", `after "judge" is not a check skill`},
		{"timeout not a duration", base + "[gates.g]\nskill = \"script\"\ntimeout = \"soon\"\n", `timeout "soon" is not a duration`},
		{"timeout zero", base + "[gates.g]\nskill = \"script\"\ntimeout = \"0s\"\n", `timeout "0s" must be positive`},
		{"timeout negative", base + "[gates.g]\nskill = \"script\"\ntimeout = \"-1m\"\n", `timeout "-1m" must be positive`},
		{"timeout on judge", base + "[gates.g]\nskill = \"judge\"\nagent = \"a1\"\ntimeout = \"5m\"\n", "this gate runs none"},
		{"resumes without park", base + "[states.a2]\nresumes = true\n", "not a park state"},
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
