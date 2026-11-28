package kernel

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/cache"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/workflow"
)

func ptr[T any](v T) *T { return &v }

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInitTwoStateEndToEnd(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	k, err := Open(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	it, err := k.Create(item.Fields{Kind: ptr("note"), Title: ptr("first"), Criteria: ptr([]string{"works"})}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if it.Status != "backlog" || it.Workflow != "default" || !strings.HasPrefix(it.ID, "not_") {
		t.Fatalf("created %+v", it)
	}
	res, err := k.Transition(context.Background(), it.ID, "done", "tester")
	if err != nil || !res.Moved || res.Item.Status != "done" || len(res.Gates) != 0 {
		t.Fatalf("transition %+v %v", res, err)
	}
	if _, err := k.Transition(context.Background(), it.ID, "backlog", "tester"); err == nil {
		t.Fatal("moved out of a terminal state")
	}
	if got, _ := k.List(cache.Query{Status: "done"}); len(got) != 1 || got[0].ID != it.ID {
		t.Fatalf("list %+v", got)
	}
	if err := k.Ledger.Verify(); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Create(item.Fields{Kind: ptr("note"), Title: ptr("x"), Status: ptr("done")}, ""); err == nil {
		t.Fatal("status accepted at create")
	}
	if _, err := k.Update(it.ID, item.Fields{Status: ptr("backlog")}, ""); err == nil {
		t.Fatal("status accepted at update")
	}
	up, err := k.Update(it.ID, item.Fields{Title: ptr("renamed"), Tags: ptr([]string{"t1"})}, "")
	if err != nil || up.Title != "renamed" || up.Status != "done" {
		t.Fatalf("update %+v %v", up, err)
	}
}

func TestConfigRefusals(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "satelle.toml"), "[workflows]\ndefault = \"default\"\nmystery = 1\n")
	if _, err := Open(Options{Root: root}); err == nil || !strings.Contains(err.Error(), "workflows.mystery") {
		t.Fatalf("unknown key: %v", err)
	}
	write(t, filepath.Join(root, "satelle.toml"), "[workflows]\ndefault = \"default\"\n")
	write(t, filepath.Join(root, "agents.toml"), `
[agents.writer]
command = "claude"
model = "m1"
tools = ["Read", "Edit"]
[agents.same]
command = "claude"
model = "m1"
tools = ["Read"]
[agents.unenforced]
command = "session"
[agents.pishell]
command = "pi"
tools = ["Bash"]
[agents.cursorjudge]
command = "cursor-agent"
model = "cj"
tools = ["Read", "Grep", "Glob"]
[agents.cursorwriter]
command = "cursor-agent"
model = "cw"
tools = ["Read", "Grep", "Glob", "Edit", "Write"]
[agents.cursorshell]
command = "cursor-agent"
tools = ["Read", "Grep", "Glob", "Edit", "Write", "Bash"]
`)
	write(t, filepath.Join(root, "skills", "s.md"), "judge\n")
	write(t, filepath.Join(root, "workflows", "x.toml"), `
[states.a]
start = true
performer = { agent = "same", skill = "s" }
[states.b]
performer = { agent = "pishell", skill = "s" }
[states.z]
terminal = true
[gates.w]
skill = "s"
agent = "writer"
[gates.u]
skill = "s"
agent = "unenforced"
[gates.m]
skill = "s"
agent = "same"
[gates.cj]
skill = "s"
agent = "cursorjudge"
[gates.cw]
skill = "s"
agent = "cursorwriter"
[[transition]]
from = ["a"]
to = "b"
groups = [{ gates = ["w", "u", "m", "cj", "cw"] }]
[[transition]]
from = ["b"]
to = "z"
`)
	problems := Validate(root)
	joined := ""
	for _, p := range problems {
		joined += p.Error() + "\n"
	}
	for _, want := range []string{
		`agent "writer" may not judge`,
		`agent "unenforced" may not judge`,
		`performer "pishell": pi adapter cannot express the scoped deny`,
		`agent "cursorwriter" may not judge`,
		`agents.cursorshell: cursor-agent cannot hold a shell`,
		`grant no Bash`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "cursorjudge") {
		t.Errorf("a read-only cursor-agent judge was refused:\n%s", joined)
	}
	if strings.Contains(joined, "runs model") {
		t.Errorf("validate compared a judge's model with its performer's:\n%s", joined)
	}
}

func TestRulesSectionIsRefusedNamingItsReplacement(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	for _, rules := range []string{
		"[rules]\nreviewer_differs_from_performer = true\n",
		"[rules]\nreviewer_differs_from_performer = false\n",
		"[rules]\n",
	} {
		write(t, filepath.Join(root, "satelle.toml"), "[workflows]\ndefault = \"default\"\n"+rules)
		_, serr := LoadSettings(root)
		_, cerr := LoadConfig(root)
		_, oerr := Open(Options{Root: root})
		for name, err := range map[string]error{"LoadSettings": serr, "LoadConfig": cerr, "Open": oerr} {
			if err == nil {
				t.Errorf("%q: %s accepted [rules]", rules, name)
				continue
			}
			for _, want := range []string{"reviewer_differs_from_performer", "satelle-judge-differs-from-performer-check", "satelle help workflows"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("%q: %s error %q lacks %q", rules, name, err, want)
				}
			}
		}
	}
	write(t, filepath.Join(root, "satelle.toml"), "[workflows]\ndefault = \"default\"\nmystery = 1\n")
	if _, err := LoadSettings(root); err == nil || !strings.Contains(err.Error(), "unknown key workflows.mystery") || strings.Contains(err.Error(), "reviewer_differs_from_performer") {
		t.Errorf("an unknown key: %v", err)
	}
}

func TestInitShipsTheJudgeDiffersCheck(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, SkillsDir, "satelle-judge-differs-from-performer-check.md")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("init did not write the check: %v", err)
	}
	skills, err := workflow.LoadSkills(filepath.Join(root, SkillsDir))
	if err != nil {
		t.Fatal(err)
	}
	sk, ok := skills["satelle-judge-differs-from-performer-check"]
	if !ok || !sk.IsCheck() || strings.TrimSpace(sk.Script) == "" {
		t.Fatalf("the shipped check does not load as a check with a script: %+v", sk)
	}
	for _, want := range []string{"session", "not compared", "FAMILIES"} {
		if !strings.Contains(sk.Body, want) {
			t.Errorf("the check's body lacks %q", want)
		}
	}
	if p := Validate(root); len(p) != 0 {
		t.Errorf("init's output does not validate: %v", p)
	}
}

func TestInitWritesTheRunsIgnore(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil || !strings.Contains(string(b), "runs/") {
		t.Fatalf(".satelle/.gitignore = %q, %v", b, err)
	}
}

func TestInitShipsTheUpgradeSkill(t *testing.T) {
	const upgradeSkill = "satelle-upgrade"
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "skills", upgradeSkill+".md")); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	sk, ok := c.Skills[upgradeSkill]
	if !ok || sk.Meta["name"] != upgradeSkill || sk.IsCheck() {
		t.Fatalf("skill = %+v, %v", sk.Meta, ok)
	}
	if probs := Validate(root); len(probs) != 0 {
		t.Fatalf("validate: %v", probs)
	}
	for _, tok := range []string{
		"[[gate]]", "on", "for", "when", "applies_to", "mandatory", "parallel",
		"reviewer_agent", "propose = true", "after_children", "waits_on_children", "advise",
		"interface", "park", "cancel", "recover",
		"session", "claude", "pi", "grok", "read-only",
		"@prior", "resumes = true", "satelle workflow show --session <session>",
		"type: check", "story attachment <id> <name>", "story attachments <id>",
		"ledger list <id>", "--file -",
		"SATELLE_HOME", "--json", "<kind>/<status>",
	} {
		if !strings.Contains(sk.Body, "`"+tok+"`") {
			t.Errorf("the upgrade skill does not name `%s`", tok)
		}
	}
}

func TestInitShipsThePackSkill(t *testing.T) {
	const packSkill = "satelle-pack"
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "skills", packSkill+".md")); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	sk, ok := c.Skills[packSkill]
	if !ok || sk.Meta["name"] != packSkill || sk.IsCheck() {
		t.Fatalf("skill = %+v, %v", sk.Meta, ok)
	}
	if probs := Validate(root); len(probs) != 0 {
		t.Fatalf("validate: %v", probs)
	}
	for _, tok := range []string{
		"`satelle pack install <ref> --version <v> --session <session>`",
		"`.satelle/skills/<entry>/SKILL.md`", "guide", "`[needs]`",
		"`satelle workflow show <name> --session <session>`",
		"`.satelle/workflows/<name>.toml`", "`[gates.<gate>]`", "user's directions",
		"`satelle validate --session <session>`",
		"landing route", "security reviewer", "security reviewer pack", "Change no workflow",
	} {
		if !strings.Contains(sk.Body, tok) {
			t.Errorf("the pack skill does not name %s", tok)
		}
	}
}

func TestWorkflowFor(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "workflows", "bugflow.toml"), []byte("[states.triage]\nstart = true\n[states.done]\nterminal = true\n[[transition]]\nfrom = [\"triage\"]\nto = \"done\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	toml, _ := os.ReadFile(filepath.Join(root, "satelle.toml"))
	if err := os.WriteFile(filepath.Join(root, "satelle.toml"), append(toml, []byte("\n[[workflows.rule]]\nworkflow = \"bugflow\"\ncategory = \"bug\"\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	k, err := Open(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	for _, c := range []struct {
		it          item.Item
		name, start string
	}{
		{item.Item{Kind: "story", Category: "bug"}, "bugflow", "triage"},
		{item.Item{Kind: "story"}, "default", "backlog"},
		{item.Item{Kind: "story", Category: "bug", Workflow: "default"}, "default", "backlog"},
	} {
		name, start, err := k.WorkflowFor(c.it)
		if err != nil || name != c.name || start != c.start {
			t.Errorf("WorkflowFor(%+v) = %s %s %v, want %s %s", c.it, name, start, err, c.name, c.start)
		}
	}
	if _, _, err := k.WorkflowFor(item.Item{Kind: "story", Workflow: "nope"}); err == nil {
		t.Error("an unknown workflow was accepted")
	}
}

func TestOpenLogsIsConfigLogsDir(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	for _, rt := range []string{filepath.Join(t.TempDir(), "rt"), ""} {
		k, err := Open(Options{Root: root, Runtime: rt})
		if err != nil {
			t.Fatal(err)
		}
		want := rt
		if want == "" {
			want = filepath.Join(root, "runtime")
		}
		if got := k.Logs; got != config.LogsDir(want) {
			t.Errorf("runtime %q: Logs = %q, want %q", rt, got, config.LogsDir(want))
		}
		k.Close()
	}
}

func TestSettingsTableSplitCoversSchema(t *testing.T) {
	strictSettingsTables := []string{"workflows", "gate", "review", "models", "dispatch"}
	ty := reflect.TypeOf(Settings{})
	var tags []string
	for i := range ty.NumField() {
		tag := ty.Field(i).Tag.Get("toml")
		if tag == "" || tag == "-" {
			continue
		}
		tags = append(tags, tag)
	}
	seen := map[string]string{}
	for _, name := range strictSettingsTables {
		if prev, dup := seen[name]; dup {
			t.Errorf("%q classified twice (%s and strict)", name, prev)
		}
		seen[name] = "strict"
	}
	for _, name := range lenientSettingsTables {
		if prev, dup := seen[name]; dup {
			t.Errorf("%q classified twice (%s and lenient)", name, prev)
		}
		seen[name] = "lenient"
	}
	for _, tag := range tags {
		if _, ok := seen[tag]; !ok {
			t.Errorf("Settings toml tag %q is not classified in strictSettingsTables or lenientSettingsTables", tag)
		}
	}
	for name := range seen {
		if !slices.Contains(tags, name) {
			t.Errorf("classified table %q is not a toml tag on Settings", name)
		}
	}
}

func TestSatelleTomlStrictAndLenientSplit(t *testing.T) {
	strictCases := []struct {
		name, body, want, forbid string
	}{
		{"workflows unknown key", "[workflows]\ndefault = \"default\"\nmystery = 1\n", "workflows.mystery", ""},
		{"workflows rule misspelt tags", "[workflows]\ndefault = \"default\"\n[[workflows.rule]]\nworkflow = \"default\"\nkind = \"story\"\ntags = \"epic\"\n", "workflows.rule.tags", ""},
		{"workflows empty subtable", "[workflows]\ndefault = \"default\"\n[workflows.extra]\n", "workflows.extra", ""},
		{"gate misspelt locked_path", "[workflows]\ndefault = \"default\"\n[gate]\nlocked_path = [\"a\"]\n", "gate.locked_path", ""},
		{"gate misspelt edit_exempt_path", "[workflows]\ndefault = \"default\"\n[gate]\nedit_exempt_path = [\"a\"]\n", "gate.edit_exempt_path", ""},
		{"gate empty subtable", "[workflows]\ndefault = \"default\"\n[gate.extra]\n", "gate.extra", ""},
		{"top-level scalar", "foo = 1\n[workflows]\ndefault = \"default\"\n", "unknown key foo", ""},
		{"top-level gates table", "[workflows]\ndefault = \"default\"\n[gates]\nlocked_paths = [\"a\"]\n", "gates.locked_paths", ""},
		{"top-level empty gates table", "[workflows]\ndefault = \"default\"\n[gates]\n", "unknown key gates", ""},
		{"top-level reviews table", "[workflows]\ndefault = \"default\"\n[reviews]\ntier_tag = \"risk\"\n", "reviews.tier_tag", ""},
		{"top-level workflow.rule", "[workflows]\ndefault = \"default\"\n[[workflow.rule]]\nworkflow = \"default\"\ncategory = \"x\"\n", "workflow.rule.category", ""},
		{"top-level hooks table", "[workflows]\ndefault = \"default\"\n[hooks]\nrefresh = false\n", "hooks.refresh", ""},
		{"quoted dotted table", "[workflows]\ndefault = \"default\"\n[\"hook.extra\"]\nx = 1\n", `"hook.extra".x`, ""},
		{"strict alongside lenient hook", "[workflows]\ndefault = \"default\"\n[gate]\nlocked_path = [\"a\"]\n[hook]\nrefreshh = false\n", "gate.locked_path", "refreshh"},
	}
	for _, tc := range strictCases {
		t.Run("strict/"+tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), ".satelle")
			if err := Init(root); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(root, SettingsFile), tc.body)
			_, serr := LoadSettings(root)
			_, cerr := LoadConfig(root)
			_, oerr := Open(Options{Root: root})
			vprobs := Validate(root)
			var vmsg []string
			for _, e := range vprobs {
				vmsg = append(vmsg, e.Error())
			}
			checks := map[string]string{
				"LoadSettings": "",
				"LoadConfig":   "",
				"Open":         "",
				"Validate":     strings.Join(vmsg, "\n"),
			}
			if serr != nil {
				checks["LoadSettings"] = serr.Error()
			}
			if cerr != nil {
				checks["LoadConfig"] = cerr.Error()
			}
			if oerr != nil {
				checks["Open"] = oerr.Error()
			}
			for fn, msg := range checks {
				if msg == "" {
					t.Errorf("%s accepted strict unknown key:\n%s", fn, tc.body)
					continue
				}
				if !strings.Contains(msg, SettingsFile) || !strings.Contains(msg, tc.want) {
					t.Errorf("%s error %q lacks %q and %q", fn, msg, SettingsFile, tc.want)
				}
				if tc.forbid != "" && strings.Contains(msg, tc.forbid) {
					t.Errorf("%s error %q leaked lenient key %q", fn, msg, tc.forbid)
				}
			}
		})
	}

	t.Run("lenient advisory tables", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), ".satelle")
		if err := Init(root); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(root, SettingsFile), `
[workflows]
default = "default"

[hook]
refresh = false
extra_hook = "hi"
[hook.sub]
depth = 2

[items]
prefix = { story = "sty" }
extra_items = true

[service]
port = 9090
extra_service = 7

[sync]
enabled = true
url = "https://example.invalid"
extra_sync = "v"

[ledger]
store = "local"
extra_ledger = ["a", "b"]
`)
		st, err := LoadSettings(root)
		if err != nil {
			t.Fatalf("LoadSettings: %v", err)
		}
		if st.Hook.Refreshes() || st.Prefix("story") != "sty" || st.Service.Port != 9090 || !st.Sync.Enabled || st.Sync.URL != "https://example.invalid" || st.Ledger.Store.Kind != "local" {
			t.Fatalf("known settings lost: %+v", st)
		}
		c, err := LoadConfig(root)
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if errs := Validate(root); len(errs) != 0 {
			t.Fatalf("Validate: %v", errs)
		}
		k, err := Open(Options{Root: root})
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		k.Close()
		want := []workflow.UnknownKey{
			{File: ".satelle/satelle.toml", Table: "hook", Key: "extra_hook", Value: `"hi"`},
			{File: ".satelle/satelle.toml", Table: "hook.sub", Key: "depth", Value: "2"},
			{File: ".satelle/satelle.toml", Table: "items", Key: "extra_items", Value: "true"},
			{File: ".satelle/satelle.toml", Table: "ledger", Key: "extra_ledger", Value: `["a", "b"]`},
			{File: ".satelle/satelle.toml", Table: "service", Key: "extra_service", Value: "7"},
			{File: ".satelle/satelle.toml", Table: "sync", Key: "extra_sync", Value: `"v"`},
		}
		if !slices.Equal(c.UnknownKeys, want) {
			t.Fatalf("UnknownKeys:\ngot:  %+v\nwant: %+v", c.UnknownKeys, want)
		}
	})
}
