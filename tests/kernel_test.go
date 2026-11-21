//go:build integration

package tests

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestInitWalksAStoryToDone(t *testing.T) {
	t.Parallel()
	s := newSandbox(t)
	if out := s.must("init"); !strings.Contains(out, "configuration valid") {
		t.Fatalf("init: %s", out)
	}
	wf, err := os.ReadFile(filepath.Join(s.dir, ".satelle", "workflows", "default.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[states.backlog]", "start = true", "[states.done]", "terminal = true", `to = "done"`} {
		if !strings.Contains(string(wf), want) {
			t.Errorf("default workflow lacks %q:\n%s", want, wf)
		}
	}
	s.must("validate")

	it := s.create("story", "--title", "first", "--criteria", "it works")
	if it.Status != "backlog" || it.Workflow != "default" || !strings.HasPrefix(it.ID, "sto_") {
		t.Fatalf("created %+v", it)
	}
	if out := s.must("story", "set", it.ID, "--status", "done"); !strings.Contains(out, "backlog -> done") {
		t.Errorf("set: %s", out)
	}
	if got := s.get(it.ID); got.Status != "done" {
		t.Fatalf("status %q, want done", got.Status)
	}
	if out := s.must("ledger", "verify"); !strings.Contains(out, "1 chains intact") {
		t.Errorf("verify: %s", out)
	}
	kinds := []string{}
	for _, e := range s.ledger(it.ID, "") {
		kinds = append(kinds, e.Kind)
	}
	if strings.Join(kinds, " ") != "item.created item.transitioned" {
		t.Errorf("ledger kinds = %v", kinds)
	}
	if r := s.run("story", "set", it.ID, "--status", "backlog"); r.code == 0 {
		t.Error("a move out of a terminal state with no transition was accepted")
	}
}

func TestRulesSectionRefusedNamingTheCheck(t *testing.T) {
	t.Parallel()
	s := newSandbox(t)
	s.must("init")
	s.write(".satelle/agents.toml", `
[agents.maker]
command = "claude"
model = "same-model"
tools = ["Read", "Edit"]
[agents.checker]
command = "claude"
model = "same-model"
tools = ["Read"]
`)
	s.write(".satelle/skills/work.md", "Work.\n")
	s.write(".satelle/workflows/default.toml", `name = "default"
[states.backlog]
start = true
[states.doing]
performer = { agent = "maker", skill = "work" }
[states.done]
terminal = true
[gates.look]
skill = "work"
agent = "checker"
[[transition]]
from = ["backlog"]
to = "doing"
[[transition]]
from = ["doing"]
to = "done"
groups = [{ gates = ["look"] }]
`)
	s.must("validate")
	toml, err := os.ReadFile(filepath.Join(s.dir, ".satelle", "satelle.toml"))
	if err != nil {
		t.Fatal(err)
	}
	s.write(".satelle/satelle.toml", string(toml)+"\n[rules]\nreviewer_differs_from_performer = true\n")
	for _, args := range [][]string{{"validate"}, {"story", "list"}} {
		r := s.run(args...)
		if r.code == 0 || !strings.Contains(r.err, "reviewer_differs_from_performer") || !strings.Contains(r.err, "satelle-judge-differs-from-performer-check") {
			t.Errorf("%v with [rules]: exit %d\n%s%s", args, r.code, r.out, r.err)
		}
	}
}

func TestValidateRefusesBrokenConfiguration(t *testing.T) {
	t.Parallel()
	s := newSandbox(t)
	s.must("init")
	id := s.create("story", "--title", "before").ID

	s.write(".satelle/agents.toml", "[agents.watcher]\ncommand = \"session\"\ntools = [\"Read\"]\n")
	s.write(".satelle/skills/look.md", "Look.\n")
	s.write(".satelle/workflows/default.toml", `name = "default"
[states.backlog]
start = true
[states.done]
terminal = true
[gates.look]
skill = "look"
agent = "watcher"
[[transition]]
from = ["backlog"]
to = "done"
groups = [{ gates = ["look"] }]
`)
	r := s.run("validate")
	if r.code == 0 || !strings.Contains(r.err, `agent "watcher" may not judge`) || !strings.Contains(r.err, "does not enforce tool grants") {
		t.Fatalf("validate with a judge on the session adapter: exit %d\n%s%s", r.code, r.out, r.err)
	}

	s.write(".satelle/workflows/default.toml", "name = \"default\"\n[states.backlog]\nstart = true\n")
	s.write(".satelle/satelle.toml", "[workflows]\ndefault = \"default\"\n[mystery]\nx = 1\n")
	r = s.run("validate")
	if r.code != 1 || !strings.Contains(r.err, "mystery") {
		t.Fatalf("validate with an unknown key: exit %d\n%s%s", r.code, r.out, r.err)
	}
	for _, args := range [][]string{{"story", "list"}, {"story", "get", id}, {"story", "create", "--title", "x"}, {"status"}} {
		if r := s.run(args...); r.code == 0 {
			t.Errorf("satelle %s ran under a broken configuration:\n%s", strings.Join(args, " "), r.out)
		}
	}
}

func TestValidateRefusesV0Skills(t *testing.T) {
	t.Parallel()
	s := newSandbox(t)
	s.must("init")

	s.write(".satelle/skills/oldcheck.md", "---\ntype: skill\n---\n# Old check\n\n```check\nexit 0\n```\n")
	r := s.run("validate")
	if r.code == 0 || !strings.Contains(r.err, "oldcheck") || !strings.Contains(r.err, "not check") {
		t.Fatalf("validate with a ```check block in a type: skill: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if err := os.Remove(filepath.Join(s.dir, ".satelle", "skills", "oldcheck.md")); err != nil {
		t.Fatal(err)
	}

	s.write(".satelle/agents.toml", "[agents.reviewer]\ncommand = \"claude\"\ntools = [\"Read\", \"Grep\", \"Glob\"]\n")
	s.write(".satelle/skills/look.md", "# Look\n\n## Verdict\n\n```json\n{\"decision\": \"accept\", \"notes\": \"\"}\n```\n")
	s.write(".satelle/workflows/default.toml", `name = "default"
[states.backlog]
start = true
[states.done]
terminal = true
[gates.look]
skill = "look"
agent = "reviewer"
[[transition]]
from = ["backlog"]
to = "done"
groups = [{ gates = ["look"] }]
`)
	r = s.run("validate")
	if r.code == 0 || !strings.Contains(r.err, `skill "look"`) || !strings.Contains(r.err, "verdict") {
		t.Fatalf("validate with a judge answering decision: exit %d\n%s%s", r.code, r.out, r.err)
	}

	s.write(".satelle/skills/look.md", "# Look\n\n## Verdict\n\n```json\n{\"verdict\": \"accept\", \"notes\": \"\"}\n```\n")
	if r := s.run("validate"); r.code != 0 {
		t.Fatalf("validate with a judge answering verdict: exit %d\n%s%s", r.code, r.out, r.err)
	}
}

func TestUnknownConfigKeysLoadAndReport(t *testing.T) {
	t.Parallel()
	s := newSandbox(t)
	s.must("init", "--hooks=claude")

	s.write(".satelle/satelle.toml", `
[workflows]
default = "default"

[hook]
inject = [".satelle/skills/satelle-workflow-standard.md", ".satelle/principles/satelle-reference-not-restate.md"]
refreshh = false

[items]
extra_item = "x"

[service]
extra_port = 9999

[sync]
extra_sync = true

[ledger]
extra_ledger = 42
`)
	s.write(".satelle/doctor.toml", `
[resolve]
kind = "story"
tag = "substrate-fix"

[[items]]
name = "drift"
kind = "story"
tag = "drift"
fix = "satelle story get {id}"
key_prefix = "sto"
owner_tag = "ops"
`)
	s.write(".satelle/init.toml", "category = \"substrate\"\nextra_init = \"hi\"\n")
	s.write(".satelle/volatile.toml", "extra_vol = 3\n")
	s.write(".satelle/ui.toml", "[colours]\nsurface = \"ok\"\n")
	s.write(".satelle/harness.toml", "[claude]\ncontext_limit_bytes = 999999\nlimit = 7\n")

	it := s.create("story", "--title", "runs with unknown keys", "--criteria", "works")
	s.must("story", "seat", it.ID, "--session", "s-it")
	s.must("story", "set", it.ID, "--status", "done")
	s.must("status")
	s.must("story", "list")
	s.must("story", "get", it.ID)

	valOut := s.must("validate")
	if !strings.Contains(valOut, "configuration valid") {
		t.Fatalf("validate output:\n%s", valOut)
	}
	wantLines := []string{
		`.satelle/doctor.toml: [items] key_prefix = "sto"`,
		`.satelle/doctor.toml: [items] owner_tag = "ops"`,
		`.satelle/harness.toml: [claude] limit = 7`,
		`.satelle/init.toml: extra_init = "hi"`,
		`.satelle/satelle.toml: [hook] refreshh = false`,
		`.satelle/satelle.toml: [items] extra_item = "x"`,
		`.satelle/satelle.toml: [ledger] extra_ledger = 42`,
		`.satelle/satelle.toml: [service] extra_port = 9999`,
		`.satelle/satelle.toml: [sync] extra_sync = true`,
		`.satelle/ui.toml: [colours] surface = "ok"`,
		`.satelle/volatile.toml: extra_vol = 3`,
	}
	for _, line := range wantLines {
		if n := strings.Count(valOut, line); n != 1 {
			t.Errorf("validate output has %q %d times, want 1:\n%s", line, n, valOut)
		}
	}

	var vdoc struct {
		Valid       bool `json:"valid"`
		UnknownKeys []struct {
			File  string `json:"file"`
			Table string `json:"table"`
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"unknown_keys"`
	}
	s.mustJSON(&vdoc, "validate")
	if !vdoc.Valid || len(vdoc.UnknownKeys) != len(wantLines) {
		t.Fatalf("validate --json = %+v", vdoc)
	}

	doc := s.run("doctor", "--json")
	if doc.code != 1 {
		t.Fatalf("doctor --json exit = %d\n%s%s", doc.code, doc.out, doc.err)
	}
	var ddoc struct {
		Issues []struct {
			Check   string `json:"check"`
			Problem string `json:"problem"`
			Fix     string `json:"fix"`
		} `json:"issues"`
	}
	if err := json.Unmarshal([]byte(doc.out), &ddoc); err != nil {
		t.Fatalf("doctor --json: %v\n%s", err, doc.out)
	}
	var got []string
	for _, is := range ddoc.Issues {
		if is.Check == "unknown-keys" {
			got = append(got, is.Problem)
		}
	}
	if !slices.Equal(got, wantLines) {
		t.Fatalf("doctor unknown-keys:\ngot:  %v\nwant: %v", got, wantLines)
	}
}

func TestStrictConfigFilesRefuseUnknownKeys(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		setup func(s *sandbox)
		file  string
		key   string
	}{
		{
			name: "workflows",
			setup: func(s *sandbox) {
				s.write(".satelle/workflows/default.toml", "name = \"default\"\n[states.backlog]\nstart = true\n[states.done]\nterminal = true\n[[transition]]\nfrom = [\"backlog\"]\nto = \"done\"\ncolour = 1\n")
			},
			file: "default.toml",
			key:  "transition.colour",
		},
		{
			name: "queues",
			setup: func(s *sandbox) {
				s.write(".satelle/queues/rel.toml", "title = \"Rel\"\n[filter]\nstatuz = \"done\"\n")
			},
			file: "rel.toml",
			key:  "filter.statuz",
		},
		{
			name: "agents.toml",
			setup: func(s *sandbox) {
				s.write(".satelle/agents.toml", "[agents.dev]\ncommand = \"claude\"\ndenies = [\"Write\"]\n")
			},
			file: "agents.toml",
			key:  "agents.dev.denies",
		},
		{
			name: "agents.local.toml",
			setup: func(s *sandbox) {
				s.write(".satelle/agents.toml", "[agents.dev]\ncommand = \"claude\"\n")
				s.write(".satelle/agents.local.toml", "[agents.dev]\ndenies = [\"Write\"]\n")
			},
			file: "agents.local.toml",
			key:  "agents.dev.denies",
		},
		{
			name: "satelle.toml [review]",
			setup: func(s *sandbox) {
				s.write(".satelle/satelle.toml", "[workflows]\ndefault = \"default\"\n[review]\ntier = \"risk\"\n")
			},
			file: "satelle.toml",
			key:  "review.tier",
		},
		{
			name: "satelle.toml [workflows]",
			setup: func(s *sandbox) {
				s.write(".satelle/satelle.toml", "[workflows]\ndefault = \"default\"\n[[workflows.rule]]\nworkflow = \"default\"\nkind = \"story\"\ntags = \"epic\"\n")
			},
			file: "satelle.toml",
			key:  "workflows.rule.tags",
		},
		{
			name: "satelle.toml [gate]",
			setup: func(s *sandbox) {
				s.write(".satelle/satelle.toml", "[workflows]\ndefault = \"default\"\n[gate]\nlocked_path = [\"a\"]\n")
			},
			file: "satelle.toml",
			key:  "gate.locked_path",
		},
		{
			name: "satelle.toml top-level [gates]",
			setup: func(s *sandbox) {
				s.write(".satelle/satelle.toml", "[workflows]\ndefault = \"default\"\n[gates]\nlocked_paths = [\"a\"]\n")
			},
			file: "satelle.toml",
			key:  "gates.locked_paths",
		},
		{
			name: "satelle.toml top-level [reviews]",
			setup: func(s *sandbox) {
				s.write(".satelle/satelle.toml", "[workflows]\ndefault = \"default\"\n[reviews]\ntier_tag = \"risk\"\n")
			},
			file: "satelle.toml",
			key:  "reviews.tier_tag",
		},
		{
			name: "satelle.toml top-level [[workflow.rule]]",
			setup: func(s *sandbox) {
				s.write(".satelle/satelle.toml", "[workflows]\ndefault = \"default\"\n[[workflow.rule]]\nworkflow = \"default\"\ncategory = \"x\"\n")
			},
			file: "satelle.toml",
			key:  "workflow.rule",
		},
		{
			name: "satelle.toml top-level scalar",
			setup: func(s *sandbox) {
				s.write(".satelle/satelle.toml", "stray = 1\n[workflows]\ndefault = \"default\"\n")
			},
			file: "satelle.toml",
			key:  "stray",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newSandbox(t)
			s.must("init")
			tc.setup(s)

			for _, args := range [][]string{{"validate"}, {"status"}, {"story", "list"}} {
				r := s.run(args...)
				msg := r.out + "\n" + r.err
				if r.code == 0 || !strings.Contains(msg, tc.file) || !strings.Contains(msg, tc.key) {
					t.Errorf("%v: exit %d, want failure naming %q and %q:\n%s", args, r.code, tc.file, tc.key, msg)
				}
			}

			doc := s.run("doctor", "--json")
			if doc.code != 1 {
				t.Fatalf("doctor --json exit = %d\n%s%s", doc.code, doc.out, doc.err)
			}
			var ddoc struct {
				Issues []struct {
					Check   string `json:"check"`
					Problem string `json:"problem"`
				} `json:"issues"`
			}
			if err := json.Unmarshal([]byte(doc.out), &ddoc); err != nil {
				t.Fatalf("doctor --json: %v\n%s", err, doc.out)
			}
			hasConfig := false
			for _, is := range ddoc.Issues {
				if is.Check == "unknown-keys" {
					t.Errorf("reported under unknown-keys: %+v", is)
				}
				if is.Check == "config" && strings.Contains(is.Problem, tc.file) && strings.Contains(is.Problem, tc.key) {
					hasConfig = true
				}
			}
			if !hasConfig {
				t.Errorf("doctor --json lacks config issue naming %q and %q: %+v", tc.file, tc.key, ddoc.Issues)
			}
		})
	}
}
