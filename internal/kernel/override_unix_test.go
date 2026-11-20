//go:build !windows

package kernel

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/item"
)

const overrideAgents = `
[agents.doer]
command = "claude"
model = "big"
tools = ["Read", "Edit"]
[agents.r1]
command = "grok"
model = "m1"
tools = ["Read", "Grep", "Glob"]
`

func overrideRoot(t *testing.T, settings, local string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".satelle")
	write(t, filepath.Join(root, "satelle.toml"), "[workflows]\ndefault = \"flow\"\n[items]\nprefix = { thing = \"th\" }\n"+settings)
	write(t, filepath.Join(root, "agents.toml"), overrideAgents)
	write(t, filepath.Join(root, "skills", "review.md"), "Review the change.\n")
	write(t, filepath.Join(root, "skills", "do.md"), "Do the work.\n")
	write(t, filepath.Join(root, "workflows", "flow.toml"), `
[states.open]
start = true
[states.work]
performer = { agent = "doer", skill = "do" }
[states.closed]
terminal = true
[gates.review]
skill = "review"
agent = "r1"
[[transition]]
from = ["open"]
to = "work"
[[transition]]
from = ["work"]
to = "closed"
groups = [{ gates = ["review"] }]
`)
	if local != "" {
		write(t, filepath.Join(root, AgentsLocalFile), local)
	}
	return root
}

func TestNoLocalFileIsTheTrackedCatalog(t *testing.T) {
	root := overrideRoot(t, "", "")
	cat, ovs, err := LoadAgents(root)
	if err != nil || len(ovs) != 0 {
		t.Fatalf("LoadAgents: %v, overrides %v", err, ovs)
	}
	want, err := agent.LoadCatalog(filepath.Join(root, AgentsFile))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cat, want) {
		t.Errorf("with no local file the catalog %+v differs from agents.toml's %+v", cat, want)
	}
	if p := Validate(root); len(p) != 0 {
		t.Errorf("validate: %v", p)
	}
}

func TestLocalOverrideDispatchesAndIsRecorded(t *testing.T) {
	root := overrideRoot(t, "", `[agents.doer]
model = "small"
[agents.r1]
command = "cursor-agent"
model = "gpt-x"
`)
	k, err := Open(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	d := &fakeDispatcher{answers: map[string]string{"doer": "did it", "r1": accept}}
	k.Dispatcher = d
	ctx := context.Background()
	it, err := k.Create(item.Fields{Kind: ptr("thing"), Title: ptr("t")}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Transition(ctx, it.ID, "work", "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Perform(ctx, it.ID, "me"); err != nil {
		t.Fatal(err)
	}
	if res, err := k.Transition(ctx, it.ID, "closed", "me"); err != nil || !res.Moved {
		t.Fatalf("close: %v %+v", err, res)
	}
	d.mu.Lock()
	reqs := append([]agent.Request{}, d.reqs...)
	d.mu.Unlock()
	var perf, judge agent.Binding
	var judgeReq agent.Request
	for _, r := range reqs {
		switch r.Binding.Name {
		case "doer":
			perf = r.Binding
		case "r1":
			judge = r.Binding
			judgeReq = r
		}
	}
	if perf.Command != "claude" || perf.Model != "small" || !reflect.DeepEqual(perf.Tools, []string{"Read", "Edit"}) {
		t.Errorf("performer dispatched with %+v, want claude, the overriding model small and the tracked tools", perf)
	}
	if judge.Command != "cursor-agent" || judge.Model != "gpt-x" || !reflect.DeepEqual(judge.Tools, []string{"Read", "Grep", "Glob"}) {
		t.Errorf("judge dispatched with %+v, want the overriding command and model and the tracked tools", judge)
	}
	ev, err := k.Ledger.Read(it.ID)
	if err != nil {
		t.Fatal(err)
	}
	var run PerformerRun
	var res gate.Result
	for _, e := range ev {
		switch e.Kind {
		case KindPerformerRun:
			_ = e.Decode(&run)
		case gate.KindRun:
			_ = e.Decode(&res)
		}
	}
	if run.Command != "claude" || run.Model != "small" {
		t.Errorf("performer.run records command %q model %q, want claude and small", run.Command, run.Model)
	}
	if res.Command != "cursor-agent" || res.Model != "gpt-x" || res.Effort != "" || res.PromptBytes != len(judgeReq.Prompt) {
		t.Errorf("gate.run records command %q model %q effort %q prompt_bytes %d, want cursor-agent, gpt-x, empty effort and %d",
			res.Command, res.Model, res.Effort, res.PromptBytes, len(judgeReq.Prompt))
	}
}

func TestGateRunRecordsEffectiveEffortAndPromptBytes(t *testing.T) {
	for _, c := range []struct {
		name       string
		tracked    string
		local      string
		wantEffort string
	}{
		{"tracked effort", "effort = \"low\"\n", "", "low"},
		{"overridden effort", "effort = \"low\"\n", "[agents.r1]\neffort = \"high\"\n", "high"},
		{"cleared effort", "effort = \"low\"\n", "[agents.r1]\neffort = \"\"\n", ""},
		{"unset effort", "", "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := overrideRoot(t, "", c.local)
			if c.tracked != "" {
				write(t, filepath.Join(root, AgentsFile), overrideAgents+c.tracked)
			}
			k, err := Open(Options{Root: root})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { k.Close() })
			d := &fakeDispatcher{answers: map[string]string{"doer": "did it", "r1": accept}}
			k.Dispatcher = d
			ctx := context.Background()
			it, err := k.Create(item.Fields{Kind: ptr("thing"), Title: ptr("t")}, "me")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := k.Transition(ctx, it.ID, "work", "me"); err != nil {
				t.Fatal(err)
			}
			if res, err := k.Transition(ctx, it.ID, "closed", "me"); err != nil || !res.Moved {
				t.Fatalf("close: %v %+v", err, res)
			}
			d.mu.Lock()
			reqs := append([]agent.Request{}, d.reqs...)
			d.mu.Unlock()
			var judgeReq agent.Request
			foundReq := false
			for _, r := range reqs {
				if r.Binding.Name == "r1" {
					judgeReq, foundReq = r, true
				}
			}
			if !foundReq || judgeReq.Prompt == "" {
				t.Fatalf("no judge request captured: %+v", reqs)
			}
			ev, err := k.Ledger.Read(it.ID)
			if err != nil {
				t.Fatal(err)
			}
			var res gate.Result
			var raw json.RawMessage
			for _, e := range ev {
				if e.Kind == gate.KindRun {
					if err := e.Decode(&res); err != nil {
						t.Fatal(err)
					}
					raw = e.Payload
				}
			}
			if len(raw) == 0 {
				t.Fatal("no gate.run event recorded")
			}
			if res.Effort != c.wantEffort {
				t.Errorf("gate.run effort = %q, want %q", res.Effort, c.wantEffort)
			}
			if res.PromptBytes != len(judgeReq.Prompt) {
				t.Errorf("gate.run prompt_bytes = %d, want %d", res.PromptBytes, len(judgeReq.Prompt))
			}
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			if c.wantEffort == "" {
				if _, has := m["effort"]; has {
					t.Errorf("gate.run JSON has effort key when unset: %s", raw)
				}
			} else if m["effort"] != c.wantEffort {
				t.Errorf("gate.run JSON effort = %v, want %q", m["effort"], c.wantEffort)
			}
			if m["prompt_bytes"] != float64(len(judgeReq.Prompt)) {
				t.Errorf("gate.run JSON prompt_bytes = %v, want %d", m["prompt_bytes"], len(judgeReq.Prompt))
			}
		})
	}
}

func TestLocalOverrideRefusals(t *testing.T) {
	for _, c := range []struct{ local, want string }{
		{"[agents.ghost]\nmodel = \"m\"\n", "agents.local.toml: agents.ghost: agents.toml defines no such agent"},
		{"[agents.r1]\ncolour = 1\n", "agents.local.toml: "},
		{"[agents.r1]\ntools = [\"Delete\"]\n", "agents.local.toml: agents.r1: tool \"Delete\""},
	} {
		root := overrideRoot(t, "", c.local)
		var msgs []string
		for _, e := range Validate(root) {
			msgs = append(msgs, e.Error())
		}
		all := strings.Join(msgs, "\n")
		if !strings.Contains(all, c.want) {
			t.Errorf("%q: validate %q, want %q", c.local, all, c.want)
		}
		if _, err := Open(Options{Root: root}); err == nil {
			t.Errorf("%q: the kernel opened", c.local)
		}
	}
	root := overrideRoot(t, "", "[agents.r1]\ncolour = 1\n")
	if p := Validate(root); len(p) == 0 || !strings.Contains(p[0].Error(), "unknown key agents.r1.colour") {
		t.Errorf("unknown field: %v", p)
	}
	if p := Validate(overrideRoot(t, "", "[agents.r1]\ntools = [\"Delete\"]\n")); len(p) == 0 || !strings.Contains(p[0].Error(), "(overriding tools)") {
		t.Errorf("an override that makes a binding invalid names the field: %v", p)
	}
}

func TestOverrideToThePerformersCommandAndModelValidates(t *testing.T) {
	for _, local := range []string{
		"[agents.r1]\ncommand = \"claude\"\nmodel = \"big\"\n",
		"[agents.doer]\ncommand = \"grok\"\nmodel = \"m1\"\n",
	} {
		var msgs []string
		for _, e := range Validate(overrideRoot(t, "", local)) {
			msgs = append(msgs, e.Error())
		}
		if p := strings.Join(msgs, "\n"); p != "" {
			t.Errorf("%q: validate %q, want no problem", local, p)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestInitIgnoresTheLocalAgentsFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	b := readFile(t, filepath.Join(root, ".gitignore"))
	if !strings.Contains(b, "runs/\n") || !strings.Contains(b, "agents.local.toml\n") {
		t.Errorf("a fresh init's .gitignore %q lacks runs/ or agents.local.toml", b)
	}
	other := filepath.Join(t.TempDir(), ".satelle")
	write(t, filepath.Join(other, ".gitignore"), "runs/\nmine\n")
	if err := Init(other); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(other, ".gitignore")); got != "runs/\nmine\n" {
		t.Errorf("init changed an existing .gitignore: %q", got)
	}
}
