package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/workflow"
)

func hideSatelled(t *testing.T) {
	t.Helper()
	var kept []string
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if _, err := os.Stat(filepath.Join(dir, "satelled")); err == nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "satelled.exe")); err == nil {
			continue
		}
		kept = append(kept, dir)
	}
	t.Setenv("PATH", strings.Join(kept, string(os.PathListSeparator)))
}

func TestUnknownKeysLenientConfigFilesLoadAndReport(t *testing.T) {
	dir := isolate(t)
	hideSatelled(t)
	must(t, "init", "--hooks=claude")

	cleanRaw := must(t, "validate", "--json")
	var cleanDoc struct {
		Valid       bool                  `json:"valid"`
		Problems    []string              `json:"problems"`
		UnknownKeys []workflow.UnknownKey `json:"unknown_keys"`
	}
	if err := json.Unmarshal([]byte(cleanRaw), &cleanDoc); err != nil {
		t.Fatalf("clean validate --json: %v\n%s", err, cleanRaw)
	}
	if !cleanDoc.Valid || len(cleanDoc.Problems) != 0 || cleanDoc.UnknownKeys == nil || len(cleanDoc.UnknownKeys) != 0 {
		t.Fatalf("clean validate --json = %+v, want valid with empty unknown_keys []", cleanDoc)
	}

	writeFile(t, filepath.Join(dir, ".satelle", "agents.toml"), "[agents.dev]\ncommand = \"session\"\n")
	writeFile(t, filepath.Join(dir, ".satelle", "skills", "work.md"), "---\nname: work\ntype: skill\ndescription: Work.\n---\nWork.\n")
	writeFile(t, filepath.Join(dir, ".satelle", "workflows", "default.toml"), `name = "default"
[states.backlog]
start = true
[states.work]
performer = { agent = "dev", skill = "work" }
[states.done]
terminal = true
[[transition]]
from = ["backlog"]
to = "work"
[[transition]]
from = ["work"]
to = "done"
`)
	writeFile(t, filepath.Join(dir, ".satelle", "satelle.toml"), `
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
	writeFile(t, filepath.Join(dir, ".satelle", "doctor.toml"), `
[resolve]
kind = "story"
tag = "substrate-fix"

[[items]]
name = "first"
kind = "story"
tag = "t1"
fix = "satelle story get {id}"
key_prefix = "sto"

[[items]]
name = "second"
kind = "story"
tag = "t2"
fix = "satelle story get {id}"
key_prefix = "epi"
owner_tag = "ops"
`)
	writeFile(t, filepath.Join(dir, ".satelle", "init.toml"), "category = \"substrate\"\nextra_init = \"hi\"\n")
	writeFile(t, filepath.Join(dir, ".satelle", "volatile.toml"), "extra_vol = 3\n")
	writeFile(t, filepath.Join(dir, ".satelle", "ui.toml"), "[tag_colours]\nsurface = \"warn\"\n[colours]\nsurface = \"ok\"\n")
	writeFile(t, filepath.Join(dir, ".satelle", "harness.toml"), "[claude]\ncontext_limit_bytes = 999999\nlimit = 7\n")

	id := createID(t, "story", "create", "--title", "works with unknown keys", "--criteria", "c")
	must(t, "story", "set", id, "--status", "work")
	must(t, "story", "seat", id, "--session", "s-work")
	if deny, reason := hookGate(t, dir, "s-work", "Edit", "hello.txt", ""); deny {
		t.Fatalf("hook gate denied edit while seated with lenient unknown keys: %s", reason)
	}
	must(t, "story", "set", id, "--status", "done")
	must(t, "status")
	must(t, "story", "list")
	must(t, "story", "get", id)
	if initOut := must(t, "init"); !strings.Contains(initOut, "configuration valid") {
		t.Errorf("init output: %s", initOut)
	}

	want := []workflow.UnknownKey{
		{File: ".satelle/doctor.toml", Table: "items[0]", Key: "key_prefix", Value: `"sto"`},
		{File: ".satelle/doctor.toml", Table: "items[1]", Key: "key_prefix", Value: `"epi"`},
		{File: ".satelle/doctor.toml", Table: "items[1]", Key: "owner_tag", Value: `"ops"`},
		{File: ".satelle/harness.toml", Table: "claude", Key: "limit", Value: "7"},
		{File: ".satelle/init.toml", Key: "extra_init", Value: `"hi"`},
		{File: ".satelle/satelle.toml", Table: "hook", Key: "refreshh", Value: "false"},
		{File: ".satelle/satelle.toml", Table: "items", Key: "extra_item", Value: `"x"`},
		{File: ".satelle/satelle.toml", Table: "ledger", Key: "extra_ledger", Value: "42"},
		{File: ".satelle/satelle.toml", Table: "service", Key: "extra_port", Value: "9999"},
		{File: ".satelle/satelle.toml", Table: "sync", Key: "extra_sync", Value: "true"},
		{File: ".satelle/ui.toml", Table: "colours", Key: "surface", Value: `"ok"`},
		{File: ".satelle/volatile.toml", Key: "extra_vol", Value: "3"},
	}

	valText := must(t, "validate")
	if !strings.Contains(valText, "configuration valid") || !strings.Contains(valText, "unknown keys in") {
		t.Fatalf("validate text:\n%s", valText)
	}
	for _, u := range want {
		if n := strings.Count(valText, u.String()); n != 1 {
			t.Errorf("validate text contains %q %d times, want 1:\n%s", u.String(), n, valText)
		}
	}

	valJSON := must(t, "validate", "--json")
	var vdoc struct {
		Valid       bool                  `json:"valid"`
		Problems    []string              `json:"problems"`
		UnknownKeys []workflow.UnknownKey `json:"unknown_keys"`
	}
	if err := json.Unmarshal([]byte(valJSON), &vdoc); err != nil {
		t.Fatalf("validate --json: %v\n%s", err, valJSON)
	}
	if !vdoc.Valid || len(vdoc.Problems) != 0 {
		t.Fatalf("validate --json invalid: %+v", vdoc)
	}
	if !slices.Equal(vdoc.UnknownKeys, want) {
		t.Fatalf("validate --json unknown_keys:\ngot:  %+v\nwant: %+v", vdoc.UnknownKeys, want)
	}

	docText, docTextErr := run(t, "", "doctor")
	if ExitCodeOf(docTextErr) != 1 {
		t.Fatalf("doctor text exit = %d (%v), want 1:\n%s", ExitCodeOf(docTextErr), docTextErr, docText)
	}
	docRaw, docErr := run(t, "", "doctor", "--json")
	if ExitCodeOf(docErr) != 1 {
		t.Fatalf("doctor --json exit = %d (%v), want 1:\n%s", ExitCodeOf(docErr), docErr, docRaw)
	}
	var docRep doctorReport
	if err := json.Unmarshal([]byte(docRaw), &docRep); err != nil {
		t.Fatalf("doctor --json: %v\n%s", err, docRaw)
	}
	var gotUnknown []string
	for _, is := range docRep.Issues {
		if is.Check == checkUnknownKeysName {
			if is.Fix != unknownKeyFix || is.Resolve != "" {
				t.Errorf("unknown-keys issue has unexpected fix/resolve: %+v", is)
			}
			gotUnknown = append(gotUnknown, is.Problem)
		}
	}
	wantLines := make([]string, len(want))
	for i, u := range want {
		wantLines[i] = u.String()
		if n := strings.Count(docText, u.String()); n != 1 {
			t.Errorf("doctor text contains %q %d times, want 1:\n%s", u.String(), n, docText)
		}
	}
	if !slices.Equal(gotUnknown, wantLines) {
		t.Fatalf("doctor unknown-keys issues:\ngot:  %v\nwant: %v", gotUnknown, wantLines)
	}

	resRaw, resErr := run(t, "", "doctor", "--resolve", "--json")
	if ExitCodeOf(resErr) != 1 {
		t.Fatalf("doctor --resolve --json exit = %d (%v), want 1:\n%s", ExitCodeOf(resErr), resErr, resRaw)
	}
	var resRep resolveReport
	if err := json.Unmarshal([]byte(resRaw), &resRep); err != nil {
		t.Fatalf("doctor --resolve --json: %v\n%s", err, resRaw)
	}
	if !slices.ContainsFunc(resRep.Actions, func(a resolveAction) bool {
		return a.Check == "seats" && a.Result == resultReleased && strings.Contains(a.Problem, id)
	}) {
		t.Errorf("doctor --resolve did not release terminal seat on %s: actions=%+v left=%+v", id, resRep.Actions, resRep.Left)
	}
	var leftUnknown []string
	for _, l := range resRep.Left {
		if l.Check == checkUnknownKeysName {
			if l.Why != leftNoFix {
				t.Errorf("unknown-keys left why = %q, want %q", l.Why, leftNoFix)
			}
			leftUnknown = append(leftUnknown, l.Problem)
		}
	}
	if !slices.Equal(leftUnknown, wantLines) {
		t.Errorf("doctor --resolve left unknown-keys:\ngot:  %v\nwant: %v", leftUnknown, wantLines)
	}
}

func TestStrictConfigFilesRefuseUnknownKeys(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, dir string)
		file  string
		key   string
	}{
		{
			name: "workflow unknown transition key",
			setup: func(t *testing.T, dir string) {
				p := filepath.Join(dir, ".satelle", "workflows", "default.toml")
				b, _ := os.ReadFile(p)
				writeFile(t, p, string(b)+"\ncolour = 1\n")
			},
			file: "default.toml",
			key:  "transition.colour",
		},
		{
			name: "workflow misspelt group",
			setup: func(t *testing.T, dir string) {
				p := filepath.Join(dir, ".satelle", "workflows", "default.toml")
				b, _ := os.ReadFile(p)
				writeFile(t, p, string(b)+"\ngroup = []\n")
			},
			file: "default.toml",
			key:  "transition.group",
		},
		{
			name: "queue misspelt filter",
			setup: func(t *testing.T, dir string) {
				writeFile(t, filepath.Join(dir, ".satelle", "queues", "rel.toml"), "title = \"Rel\"\n[filter]\nstatuz = \"done\"\n")
			},
			file: "rel.toml",
			key:  "filter.statuz",
		},
		{
			name: "agents.toml misspelt denies",
			setup: func(t *testing.T, dir string) {
				writeFile(t, filepath.Join(dir, ".satelle", "agents.toml"), "[agents.dev]\ncommand = \"claude\"\ndenies = [\"Write\"]\n")
			},
			file: "agents.toml",
			key:  "agents.dev.denies",
		},
		{
			name: "agents.local.toml misspelt denies",
			setup: func(t *testing.T, dir string) {
				writeFile(t, filepath.Join(dir, ".satelle", "agents.toml"), "[agents.dev]\ncommand = \"claude\"\n")
				writeFile(t, filepath.Join(dir, ".satelle", "agents.local.toml"), "[agents.dev]\ndenies = [\"Write\"]\n")
			},
			file: "agents.local.toml",
			key:  "agents.dev.denies",
		},
		{
			name: "satelle.toml [review] unknown key",
			setup: func(t *testing.T, dir string) {
				p := filepath.Join(dir, ".satelle", "satelle.toml")
				b, _ := os.ReadFile(p)
				writeFile(t, p, string(b)+"\n[review]\ntier = \"risk\"\n")
			},
			file: "satelle.toml",
			key:  "review.tier",
		},
		{
			name: "satelle.toml [[workflows.rule]] misspelt tags",
			setup: func(t *testing.T, dir string) {
				p := filepath.Join(dir, ".satelle", "satelle.toml")
				b, _ := os.ReadFile(p)
				writeFile(t, p, string(b)+"\n[[workflows.rule]]\nworkflow = \"default\"\nkind = \"story\"\ntags = \"epic\"\n")
			},
			file: "satelle.toml",
			key:  "workflows.rule.tags",
		},
		{
			name: "satelle.toml [gate] misspelt locked_path",
			setup: func(t *testing.T, dir string) {
				p := filepath.Join(dir, ".satelle", "satelle.toml")
				b, _ := os.ReadFile(p)
				writeFile(t, p, string(b)+"\n[gate]\nlocked_path = [\"a\"]\n")
			},
			file: "satelle.toml",
			key:  "gate.locked_path",
		},
		{
			name: "satelle.toml top-level scalar",
			setup: func(t *testing.T, dir string) {
				p := filepath.Join(dir, ".satelle", "satelle.toml")
				b, _ := os.ReadFile(p)
				writeFile(t, p, "stray = 1\n"+string(b))
			},
			file: "satelle.toml",
			key:  "stray",
		},
		{
			name: "satelle.toml top-level [gates]",
			setup: func(t *testing.T, dir string) {
				p := filepath.Join(dir, ".satelle", "satelle.toml")
				b, _ := os.ReadFile(p)
				writeFile(t, p, string(b)+"\n[gates]\nlocked_paths = [\"a\"]\n")
			},
			file: "satelle.toml",
			key:  "gates.locked_paths",
		},
		{
			name: "satelle.toml empty top-level [gates]",
			setup: func(t *testing.T, dir string) {
				p := filepath.Join(dir, ".satelle", "satelle.toml")
				b, _ := os.ReadFile(p)
				writeFile(t, p, string(b)+"\n[gates]\n")
			},
			file: "satelle.toml",
			key:  "gates",
		},
		{
			name: "satelle.toml top-level [reviews]",
			setup: func(t *testing.T, dir string) {
				p := filepath.Join(dir, ".satelle", "satelle.toml")
				b, _ := os.ReadFile(p)
				writeFile(t, p, string(b)+"\n[reviews]\ntier_tag = \"risk\"\n")
			},
			file: "satelle.toml",
			key:  "reviews.tier_tag",
		},
		{
			name: "satelle.toml top-level [[workflow.rule]]",
			setup: func(t *testing.T, dir string) {
				p := filepath.Join(dir, ".satelle", "satelle.toml")
				b, _ := os.ReadFile(p)
				writeFile(t, p, string(b)+"\n[[workflow.rule]]\nworkflow = \"default\"\ncategory = \"x\"\n")
			},
			file: "satelle.toml",
			key:  "workflow.rule",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := isolate(t)
			hideSatelled(t)
			must(t, "init", "--hooks=claude")
			tc.setup(t, dir)

			for _, args := range [][]string{{"validate"}, {"status"}, {"story", "list"}} {
				out, err := run(t, "", args...)
				if err == nil {
					t.Errorf("satelle %s succeeded:\n%s", strings.Join(args, " "), out)
					continue
				}
				msg := out + "\n" + err.Error()
				if !strings.Contains(msg, tc.file) || !strings.Contains(msg, tc.key) {
					t.Errorf("satelle %s error lacks %q and %q:\n%s", strings.Join(args, " "), tc.file, tc.key, msg)
				}
			}

			deny, reason := hookGate(t, dir, "s-any", "Edit", "foo.txt", "")
			if !deny || !strings.Contains(reason, "configuration is invalid") || !strings.Contains(reason, tc.file) || !strings.Contains(reason, tc.key) {
				t.Errorf("hook gate deny=%v reason=%q, want invalid config naming %q and %q", deny, reason, tc.file, tc.key)
			}

			docRaw, docErr := run(t, "", "doctor", "--json")
			if ExitCodeOf(docErr) != 1 {
				t.Fatalf("doctor --json exit=%d (%v)\n%s", ExitCodeOf(docErr), docErr, docRaw)
			}
			var rep doctorReport
			if err := json.Unmarshal([]byte(docRaw), &rep); err != nil {
				t.Fatalf("doctor --json: %v\n%s", err, docRaw)
			}
			hasConfig := false
			for _, is := range rep.Issues {
				if is.Check == checkUnknownKeysName {
					t.Errorf("strict unknown key reported under %s: %+v", checkUnknownKeysName, is)
				}
				if is.Check == "config" && strings.Contains(is.Problem, tc.file) && strings.Contains(is.Problem, tc.key) {
					hasConfig = true
				}
			}
			if !hasConfig {
				t.Errorf("doctor --json lacks config issue naming %q and %q: %+v", tc.file, tc.key, rep.Issues)
			}
		})
	}
}

func TestRequiredKeysAndKnownValuesStillFailAlongsideUnknownKeys(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, dir string)
		wants []string
	}{
		{
			name: "missing default workflow",
			setup: func(t *testing.T, dir string) {
				writeFile(t, filepath.Join(dir, ".satelle", "satelle.toml"), "[hook]\nrefreshh = false\n")
			},
			wants: []string{"default"},
		},
		{
			name: "retired [rules]",
			setup: func(t *testing.T, dir string) {
				p := filepath.Join(dir, ".satelle", "satelle.toml")
				b, _ := os.ReadFile(p)
				writeFile(t, p, string(b)+"\n[sync]\nrefreshh = false\n[rules]\nreviewer_differs_from_performer = true\n")
			},
			wants: []string{"satelle.toml", "reviewer_differs_from_performer"},
		},
		{
			name: "bad [ledger] store",
			setup: func(t *testing.T, dir string) {
				p := filepath.Join(dir, ".satelle", "satelle.toml")
				b, _ := os.ReadFile(p)
				writeFile(t, p, string(b)+"\n[sync]\nrefreshh = false\n[ledger]\nstore = \"s3\"\n")
			},
			wants: []string{"satelle.toml", "store"},
		},
		{
			name: "unknown agent in agents.local.toml",
			setup: func(t *testing.T, dir string) {
				p := filepath.Join(dir, ".satelle", "satelle.toml")
				b, _ := os.ReadFile(p)
				writeFile(t, p, string(b)+"\n[sync]\nrefreshh = false\n")
				writeFile(t, filepath.Join(dir, ".satelle", "agents.local.toml"), "[agents.ghost]\nmodel = \"m\"\n")
			},
			wants: []string{kernel.AgentsLocalFile, "agents.ghost"},
		},
		{
			name: "doctor.toml [[items]] missing fix alongside unknown key",
			setup: func(t *testing.T, dir string) {
				writeFile(t, filepath.Join(dir, ".satelle", "doctor.toml"), "[[items]]\nname = \"a\"\ntag = \"t\"\nkey_prefix = \"sto\"\n")
			},
			wants: []string{kernel.DoctorFile, "has no fix"},
		},
		{
			name: "queues/broken.toml missing title",
			setup: func(t *testing.T, dir string) {
				p := filepath.Join(dir, ".satelle", "satelle.toml")
				b, _ := os.ReadFile(p)
				writeFile(t, p, string(b)+"\n[sync]\nrefreshh = false\n")
				writeFile(t, filepath.Join(dir, ".satelle", "queues", "broken.toml"), "[filter]\nkind = \"story\"\n")
			},
			wants: []string{"broken.toml", "title"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := isolate(t)
			must(t, "init")
			tc.setup(t, dir)
			for _, args := range [][]string{{"validate"}, {"status"}} {
				out, err := run(t, "", args...)
				if err == nil {
					t.Errorf("satelle %s succeeded:\n%s", strings.Join(args, " "), out)
					continue
				}
				msg := out + "\n" + err.Error()
				for _, w := range tc.wants {
					if !strings.Contains(msg, w) {
						t.Errorf("satelle %s error lacks %q:\n%s", strings.Join(args, " "), w, msg)
					}
				}
			}
		})
	}
}

func TestSessionStartDoctorBlockListsUnknownKeys(t *testing.T) {
	cases := []struct {
		name       string
		setup      func(t *testing.T, dir string)
		wantLine   string
		wantNotice string
	}{
		{
			name: "doctor.toml [[items]] unknown keys",
			setup: func(t *testing.T, dir string) {
				writeFile(t, filepath.Join(dir, ".satelle", "doctor.toml"), `
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
			},
			wantLine:   `unknown-keys (2): .satelle/doctor.toml: [items] key_prefix = "sto" — fix: ` + unknownKeyFix,
			wantNotice: "unknown-keys (2)",
		},
		{
			name: "satelle.toml [hook] unknown key",
			setup: func(t *testing.T, dir string) {
				p := filepath.Join(dir, ".satelle", "satelle.toml")
				b, _ := os.ReadFile(p)
				writeFile(t, p, string(b)+"\nrefreshh = false\n")
			},
			wantLine:   `unknown-keys (1): .satelle/satelle.toml: [hook] refreshh = false — fix: ` + unknownKeyFix,
			wantNotice: "unknown-keys (1)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := isolate(t)
			hideSatelled(t)
			must(t, "init", "--hooks=claude")
			tc.setup(t, dir)

			payload, _ := json.Marshal(map[string]any{"session_id": "s-ctx", "cwd": dir})
			raw, err := run(t, string(payload), "hook", "context", "--harness", "claude")
			if err != nil {
				t.Fatalf("hook context: %v\n%s", err, raw)
			}
			var env struct {
				HookSpecificOutput struct {
					AdditionalContext string `json:"additionalContext"`
				} `json:"hookSpecificOutput"`
				SystemMessage string `json:"systemMessage"`
			}
			if err := json.Unmarshal([]byte(raw), &env); err != nil {
				t.Fatalf("hook context json: %v\n%s", err, raw)
			}
			ctx := env.HookSpecificOutput.AdditionalContext
			hasNone(t, "additionalContext", ctx, "configuration is invalid")
			hasAll(t, "additionalContext", ctx, "satelle doctor:", tc.wantLine, "ask what it is for")
			hasAll(t, "systemMessage", env.SystemMessage, tc.wantNotice)
		})
	}
}
