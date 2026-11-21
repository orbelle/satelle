package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/workflow"
)

func TestValidateValidPack(t *testing.T) {
	dir := isolate(t)
	must(t, "init")

	writeFile(t, filepath.Join(dir, ".satelle", "skills", "quality-pack", "SKILL.md"), "---\nname: quality-pack\ntype: skill\ndescription: wiring guide\n---\n\nWiring guide.\n")
	writeFile(t, filepath.Join(dir, ".satelle", "skills", "quality-pack", "pack.toml"), `
name = "quality-pack"
version = "1.0.0"
skills = ["pack-check", "pack-judge"]

[needs]
scripts = ["sh"]
agents = ["reviewer"]
network = ["api.example.com"]
secrets = ["API_TOKEN"]
`)
	writeFile(t, filepath.Join(dir, ".satelle", "skills", "pack-check", "SKILL.md"), "---\nname: pack-check\ntype: check\ndescription: check\n---\n\n```sh\nsh scripts/check.sh\n```\n")
	writeFile(t, filepath.Join(dir, ".satelle", "skills", "pack-check", "scripts", "check.sh"), "exit 0\n")
	writeFile(t, filepath.Join(dir, ".satelle", "skills", "pack-judge", "SKILL.md"), "---\nname: pack-judge\ntype: skill\ndescription: judge\n---\n\nJudge.\n")
	writeFile(t, filepath.Join(dir, ".satelle", "skills", "stray", "SKILL.md"), "---\nname: stray\ntype: skill\ndescription: stray\n---\n\nStray.\n")

	out := must(t, "validate")
	if !strings.Contains(out, "configuration valid") {
		t.Fatalf("validate output:\n%s", out)
	}
	for _, name := range []string{"quality-pack", "pack-check", "pack-judge"} {
		if strings.Contains(out, "skill "+name+":") {
			t.Errorf("validate reports pack skill %s as unreached:\n%s", name, out)
		}
	}
	if !strings.Contains(out, "skill stray:") {
		t.Errorf("validate does not report the unreached skill stray:\n%s", out)
	}
	if strings.Contains(out, "unknown keys") {
		t.Fatalf("validate reported unexpected unknown keys:\n%s", out)
	}

	raw := must(t, "validate", "--json")
	var doc struct {
		Valid         bool                  `json:"valid"`
		Problems      []string              `json:"problems"`
		UnknownKeys   []workflow.UnknownKey `json:"unknown_keys"`
		SkillWarnings []struct {
			Skill string `json:"skill"`
		} `json:"skill_warnings"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("validate --json: %v\n%s", err, raw)
	}
	if !doc.Valid || len(doc.Problems) != 0 || doc.UnknownKeys == nil || len(doc.UnknownKeys) != 0 {
		t.Fatalf("validate --json = %+v, want valid with empty problems and empty unknown_keys", doc)
	}
	if len(doc.SkillWarnings) != 1 || doc.SkillWarnings[0].Skill != "stray" {
		t.Fatalf("validate --json skill_warnings = %+v, want only stray", doc.SkillWarnings)
	}
}

func TestValidatePackUnknownKeysReportedAndValid(t *testing.T) {
	dir := isolate(t)
	hideSatelled(t)
	must(t, "init", "--hooks=claude")

	writeFile(t, filepath.Join(dir, ".satelle", "skills", "my-pack", "SKILL.md"), "---\nname: my-pack\ntype: skill\ndescription: wiring guide\n---\n\nWiring guide.\n")
	writeFile(t, filepath.Join(dir, ".satelle", "skills", "my-pack", "pack.toml"), `
name = "my-pack"
version = "1.0.0"
skills = ["member-a"]
author = "orbelle"

[needs]
scripts = ["sh"]
workflows = ["delivery"]
`)
	writeFile(t, filepath.Join(dir, ".satelle", "skills", "member-a", "SKILL.md"), "---\nname: member-a\ntype: skill\ndescription: member a\n---\n\nMember A.\n")

	want := []workflow.UnknownKey{
		{File: ".satelle/skills/my-pack/pack.toml", Key: "author", Value: `"orbelle"`},
		{File: ".satelle/skills/my-pack/pack.toml", Table: "needs", Key: "workflows", Value: `["delivery"]`},
	}

	valText := must(t, "validate")
	if !strings.Contains(valText, "configuration valid") {
		t.Fatalf("validate text:\n%s", valText)
	}
	for _, u := range want {
		if n := strings.Count(valText, u.String()); n != 1 {
			t.Errorf("validate text contains %q %d times, want 1:\n%s", u.String(), n, valText)
		}
	}

	raw := must(t, "validate", "--json")
	var vdoc struct {
		Valid       bool                  `json:"valid"`
		Problems    []string              `json:"problems"`
		UnknownKeys []workflow.UnknownKey `json:"unknown_keys"`
	}
	if err := json.Unmarshal([]byte(raw), &vdoc); err != nil {
		t.Fatalf("validate --json: %v\n%s", err, raw)
	}
	if !vdoc.Valid || len(vdoc.Problems) != 0 {
		t.Fatalf("validate --json invalid: %+v", vdoc)
	}
	if !slices.Equal(vdoc.UnknownKeys, want) {
		t.Fatalf("validate --json unknown_keys:\ngot:  %+v\nwant: %+v", vdoc.UnknownKeys, want)
	}

	docRaw, docErr := run(t, "", "doctor", "--json")
	if ExitCodeOf(docErr) != 1 {
		t.Fatalf("doctor --json exit=%d (%v)\n%s", ExitCodeOf(docErr), docErr, docRaw)
	}
	var docRep doctorReport
	if err := json.Unmarshal([]byte(docRaw), &docRep); err != nil {
		t.Fatalf("doctor --json: %v\n%s", err, docRaw)
	}
	var gotUnknown []string
	for _, is := range docRep.Issues {
		if is.Check == "config" {
			t.Errorf("doctor reported config issue for unknown key in pack.toml: %+v", is)
		}
		if is.Check == checkUnknownKeysName {
			gotUnknown = append(gotUnknown, is.Problem)
		}
	}
	wantLines := []string{want[0].String(), want[1].String()}
	if !slices.Equal(gotUnknown, wantLines) {
		t.Fatalf("doctor unknown-keys:\ngot:  %v\nwant: %v", gotUnknown, wantLines)
	}
}

func TestValidatePackProblemsRefused(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, dir string)
		wants []string
	}{
		{
			name: "missing name",
			setup: func(t *testing.T, dir string) {
				writeFile(t, filepath.Join(dir, ".satelle", "skills", "member-a", "SKILL.md"), "---\nname: member-a\ntype: skill\n---\n\nA.\n")
				writeFile(t, filepath.Join(dir, ".satelle", "skills", "my-pack", "SKILL.md"), "---\nname: my-pack\ntype: skill\n---\n\nGuide.\n")
				writeFile(t, filepath.Join(dir, ".satelle", "skills", "my-pack", "pack.toml"), "version = \"1.0.0\"\nskills = [\"member-a\"]\n")
			},
			wants: []string{"skills/my-pack/pack.toml", "name"},
		},
		{
			name: "missing version",
			setup: func(t *testing.T, dir string) {
				writeFile(t, filepath.Join(dir, ".satelle", "skills", "member-a", "SKILL.md"), "---\nname: member-a\ntype: skill\n---\n\nA.\n")
				writeFile(t, filepath.Join(dir, ".satelle", "skills", "my-pack", "SKILL.md"), "---\nname: my-pack\ntype: skill\n---\n\nGuide.\n")
				writeFile(t, filepath.Join(dir, ".satelle", "skills", "my-pack", "pack.toml"), "name = \"my-pack\"\nskills = [\"member-a\"]\n")
			},
			wants: []string{"skills/my-pack/pack.toml", "version"},
		},
		{
			name: "missing skills",
			setup: func(t *testing.T, dir string) {
				writeFile(t, filepath.Join(dir, ".satelle", "skills", "my-pack", "SKILL.md"), "---\nname: my-pack\ntype: skill\n---\n\nGuide.\n")
				writeFile(t, filepath.Join(dir, ".satelle", "skills", "my-pack", "pack.toml"), "name = \"my-pack\"\nversion = \"1.0.0\"\n")
			},
			wants: []string{"skills/my-pack/pack.toml", "skills"},
		},
		{
			name: "non-existent member skill",
			setup: func(t *testing.T, dir string) {
				writeFile(t, filepath.Join(dir, ".satelle", "skills", "my-pack", "SKILL.md"), "---\nname: my-pack\ntype: skill\n---\n\nGuide.\n")
				writeFile(t, filepath.Join(dir, ".satelle", "skills", "my-pack", "pack.toml"), "name = \"my-pack\"\nversion = \"1.0.0\"\nskills = [\"no-such-skill\"]\n")
			},
			wants: []string{"skills/my-pack/pack.toml", "no-such-skill"},
		},
		{
			name: "single-file member skill",
			setup: func(t *testing.T, dir string) {
				writeFile(t, filepath.Join(dir, ".satelle", "skills", "flat.md"), "---\nname: flat\ntype: skill\n---\n\nFlat.\n")
				writeFile(t, filepath.Join(dir, ".satelle", "skills", "my-pack", "SKILL.md"), "---\nname: my-pack\ntype: skill\n---\n\nGuide.\n")
				writeFile(t, filepath.Join(dir, ".satelle", "skills", "my-pack", "pack.toml"), "name = \"my-pack\"\nversion = \"1.0.0\"\nskills = [\"flat\"]\n")
			},
			wants: []string{"skills/my-pack/pack.toml", "flat", "single-file skill"},
		},
		{
			name: "pack.toml without SKILL.md",
			setup: func(t *testing.T, dir string) {
				writeFile(t, filepath.Join(dir, ".satelle", "skills", "member-a", "SKILL.md"), "---\nname: member-a\ntype: skill\n---\n\nA.\n")
				writeFile(t, filepath.Join(dir, ".satelle", "skills", "no-entry", "pack.toml"), "name = \"no-entry\"\nversion = \"1.0.0\"\nskills = [\"member-a\"]\n")
			},
			wants: []string{"skills/no-entry/pack.toml", "SKILL.md"},
		},
		{
			name: "missing required field alongside unknown key",
			setup: func(t *testing.T, dir string) {
				writeFile(t, filepath.Join(dir, ".satelle", "skills", "member-a", "SKILL.md"), "---\nname: member-a\ntype: skill\n---\n\nA.\n")
				writeFile(t, filepath.Join(dir, ".satelle", "skills", "my-pack", "SKILL.md"), "---\nname: my-pack\ntype: skill\n---\n\nGuide.\n")
				writeFile(t, filepath.Join(dir, ".satelle", "skills", "my-pack", "pack.toml"), "name = \"my-pack\"\nskills = [\"member-a\"]\nextra_key = \"val\"\n")
			},
			wants: []string{"skills/my-pack/pack.toml", "version"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := isolate(t)
			must(t, "init")
			tc.setup(t, dir)

			out, err := run(t, "", "validate")
			if err == nil {
				t.Fatalf("validate succeeded:\n%s", out)
			}
			msg := filepath.ToSlash(out + "\n" + err.Error())
			for _, w := range tc.wants {
				if !strings.Contains(msg, w) {
					t.Errorf("validate error lacks %q:\n%s", w, msg)
				}
			}

			raw, jerr := run(t, "", "validate", "--json")
			if jerr == nil {
				t.Fatalf("validate --json succeeded:\n%s", raw)
			}
			var rep struct {
				Valid    bool     `json:"valid"`
				Problems []string `json:"problems"`
			}
			if uerr := json.Unmarshal([]byte(raw), &rep); uerr != nil {
				t.Fatalf("validate --json unmarshal: %v\n%s", uerr, raw)
			}
			if rep.Valid {
				t.Errorf("validate --json reported valid=true")
			}
			joined := filepath.ToSlash(strings.Join(rep.Problems, "\n"))
			for _, w := range tc.wants {
				if !strings.Contains(joined, w) {
					t.Errorf("validate --json problems lack %q:\n%s", w, joined)
				}
			}
		})
	}
}

func TestPackMemberSkillEditedRunsGateWithoutPacksToml(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh check script")
	}
	dir := isolate(t)
	must(t, "init")

	writeFile(t, filepath.Join(dir, ".satelle", "workflows", "default.toml"), `name = "default"
[states.backlog]
start = true
[states.done]
terminal = true

[gates.pack-check]
skill = "pack-check"

[[transition]]
from = ["backlog"]
to = "done"
groups = [{ mode = "sequential", gates = ["pack-check"] }]
`)
	writeFile(t, filepath.Join(dir, ".satelle", "skills", "my-pack", "SKILL.md"), "---\nname: my-pack\ntype: skill\ndescription: wiring guide\n---\n\nWiring guide.\n")
	writeFile(t, filepath.Join(dir, ".satelle", "skills", "my-pack", "pack.toml"), `
name = "my-pack"
version = "1.0.0"
skills = ["pack-check"]

[needs]
scripts = ["sh"]
`)
	writeFile(t, filepath.Join(dir, ".satelle", "skills", "pack-check", "SKILL.md"), "---\nname: pack-check\ntype: check\ndescription: check v1\n---\n\n```sh\nsh scripts/check.sh\n```\n")
	writeFile(t, filepath.Join(dir, ".satelle", "skills", "pack-check", "scripts", "check.sh"), "echo PACK_GATE_MARKER_V1\n")

	if _, err := os.Stat(filepath.Join(dir, ".satelle", "packs.toml")); !os.IsNotExist(err) {
		t.Fatalf("expected no .satelle/packs.toml, got err=%v", err)
	}
	if out := must(t, "validate"); !strings.Contains(out, "configuration valid") {
		t.Fatalf("validate v1:\n%s", out)
	}

	id1 := createID(t, "story", "create", "--title", "v1", "--criteria", "c")
	must(t, "story", "set", id1, "--status", "done")
	led1 := must(t, "ledger", "list", id1, "--json")
	if !strings.Contains(led1, "PACK_GATE_MARKER_V1") {
		t.Fatalf("v1 ledger lacks PACK_GATE_MARKER_V1:\n%s", led1)
	}

	writeFile(t, filepath.Join(dir, ".satelle", "skills", "pack-check", "SKILL.md"), "---\nname: pack-check\ntype: check\ndescription: check v2 edited\n---\n\nEdited locally.\n\n```sh\nsh scripts/check.sh\n```\n")
	writeFile(t, filepath.Join(dir, ".satelle", "skills", "pack-check", "scripts", "check.sh"), "echo PACK_GATE_MARKER_V2_EDITED\n")

	if _, err := os.Stat(filepath.Join(dir, ".satelle", "packs.toml")); !os.IsNotExist(err) {
		t.Fatalf("expected no .satelle/packs.toml after edit, got err=%v", err)
	}
	if out := must(t, "validate"); !strings.Contains(out, "configuration valid") {
		t.Fatalf("validate v2:\n%s", out)
	}
	must(t, "status")

	id2 := createID(t, "story", "create", "--title", "v2", "--criteria", "c")
	must(t, "story", "set", id2, "--status", "done")
	led2 := must(t, "ledger", "list", id2, "--json")
	if !strings.Contains(led2, "PACK_GATE_MARKER_V2_EDITED") {
		t.Fatalf("v2 ledger lacks PACK_GATE_MARKER_V2_EDITED:\n%s", led2)
	}
}
