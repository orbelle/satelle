package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
)

func TestInitShipsThePackSkillAndValidates(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	if _, err := os.Stat(filepath.Join(dir, ".satelle", "skills", "satelle-pack.md")); err != nil {
		t.Fatalf("init did not write the pack skill: %v", err)
	}
	if out := must(t, "validate"); !strings.Contains(out, "configuration valid") {
		t.Fatalf("validate after init:\n%s", out)
	}
	if out := must(t, "validate", "--json"); strings.Contains(out, "satelle-pack") {
		t.Errorf("validate --json lists the shipped pack skill:\n%s", out)
	}
}

const proposedState = `
[states.in_progress]
performer = { agent = "executor", skill = "satelle-pack" }

[[transition]]
from = ["backlog"]
to = "in_progress"

[[transition]]
from = ["in_progress"]
to = "done"
`

const proposedAgent = `[agents.executor]
command = "session"
`

const starterWorkflow = `name = "default"

[states.backlog]
start = true

[states.done]
terminal = true

[[transition]]
from = ["backlog"]
to = "done"
`

const packSkillTemplate = "../../internal/kernel/template/skills/satelle-pack.md"

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPackSkillProposesATreeStateBeforeInstall(t *testing.T) {
	skill := readFile(t, packSkillTemplate)
	start := strings.Index(skill, "## 3. Install")
	end := strings.Index(skill, "## 4.")
	if start < 0 || end < start {
		t.Fatalf("no install section in the skill:\n%s", skill)
	}
	section := skill[start:end]
	for _, want := range []string{"satelle workflow show", "may change the tree", ".satelle/workflows/", ".satelle/agents.toml", "confirm"} {
		if !strings.Contains(section, want) {
			t.Errorf("install section lacks %q:\n%s", want, section)
		}
	}
	trimmed := map[string]bool{}
	for line := range strings.SplitSeq(section, "\n") {
		trimmed[strings.TrimSpace(line)] = true
	}
	for line := range strings.SplitSeq(proposedState+proposedAgent, "\n") {
		if l := strings.TrimSpace(line); l != "" && !trimmed[l] {
			t.Errorf("install section does not show %q", l)
		}
	}
	installAt := strings.Index(section, "satelle pack install")
	if installAt < 0 {
		t.Fatalf("install section does not run satelle pack install:\n%s", section)
	}
	for _, before := range []string{"satelle workflow show", "confirm"} {
		if i := strings.Index(section, before); i < 0 || i > installAt {
			t.Errorf("%q does not come before satelle pack install", before)
		}
	}
}

func TestPackInstallOnAFreshRepoWithTheProposedState(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	m := newMachine(t)
	seedPack(srv, "acme/docs", "0.1.0", singlePack("docs", "0.1.0"))
	record := filepath.Join(m.dir, ".satelle", "packs.toml")

	id := createID(t, "story", "create", "--title", "take a pack")
	must(t, "story", "seat", id, "--session", "s-1")
	if out, err := install(t, srv, "acme/docs", "0.1.0", "--session", "s-1"); err == nil {
		t.Fatalf("install on the starter workflow was allowed:\n%s", out)
	}
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Fatalf("refused install wrote .satelle/packs.toml (%v)", err)
	}

	wf := filepath.Join(m.dir, ".satelle", "workflows", "default.toml")
	if err := os.WriteFile(wf, []byte(readFile(t, wf)+proposedState), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.dir, ".satelle", "agents.toml"), []byte(proposedAgent), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := must(t, "validate"); !strings.Contains(out, "configuration valid") {
		t.Fatalf("validate with the proposed state:\n%s", out)
	}
	must(t, "story", "set", id, "--status", "in_progress")
	must(t, "story", "seat", id, "--session", "s-1")

	out, err := install(t, srv, "acme/docs", "0.1.0", "--session", "s-1")
	if err != nil {
		t.Fatalf("install on the proposed state: %v\n%s", err, out)
	}
	if !strings.Contains(out, "installed acme/docs@0.1.0") {
		t.Errorf("install output:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(m.dir, ".satelle", "skills", "docs", "SKILL.md")); err != nil {
		t.Errorf("docs skill not installed: %v", err)
	}
	if !strings.Contains(readFile(t, record), "acme/docs") {
		t.Errorf("packs.toml does not name acme/docs:\n%s", readFile(t, record))
	}
}

func TestInitStarterWorkflowUnchanged(t *testing.T) {
	if tmpl := readFile(t, "../../internal/kernel/template/workflows/default.toml"); tmpl != starterWorkflow {
		t.Errorf("template starter workflow changed:\n%s", tmpl)
	}
	dir := isolate(t)
	must(t, "init")
	if got := readFile(t, filepath.Join(dir, ".satelle", "workflows", "default.toml")); got != starterWorkflow {
		t.Errorf("init's starter workflow changed:\n%s", got)
	}
}

func TestRepoPackSkillMatchesTemplate(t *testing.T) {
	tmpl := readFile(t, packSkillTemplate)
	if repo := readFile(t, "../../.satelle/skills/satelle-pack.md"); repo != tmpl {
		t.Error(".satelle/skills/satelle-pack.md differs from the shipped template")
	}
	dir := isolate(t)
	must(t, "init")
	if got := readFile(t, filepath.Join(dir, ".satelle", "skills", "satelle-pack.md")); got != tmpl {
		t.Error("init does not write the template's satelle-pack skill")
	}
}
