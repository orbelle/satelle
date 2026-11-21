package cli

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func remove(t *testing.T, ref string, extra ...string) (string, error) {
	t.Helper()
	return run(t, "", append([]string{"pack", "remove", ref}, extra...)...)
}

func recordedRefs(t *testing.T, dir string) []string {
	t.Helper()
	var rec struct{ Pack []struct{ Ref string } }
	if _, err := toml.DecodeFile(filepath.Join(dir, ".satelle", "packs.toml"), &rec); err != nil {
		t.Fatal(err)
	}
	var refs []string
	for _, p := range rec.Pack {
		refs = append(refs, p.Ref)
	}
	return refs
}

func noReviewSkills(t *testing.T, dir string) {
	t.Helper()
	for _, d := range []string{"review", "review-lint"} {
		if _, err := os.Lstat(filepath.Join(dir, ".satelle", "skills", d)); !os.IsNotExist(err) {
			t.Errorf(".satelle/skills/%s still exists (%v)", d, err)
		}
	}
}

func TestPackRemoveUneditedPack(t *testing.T) {
	dir, srv, _ := packRepo(t)
	must(t, "pack", "install", "acme/review", "--version", "1.2.3", "--server", srv.URL, "--session", "s-1")

	out, err := remove(t, "acme/review", "--session", "s-1")
	if err != nil {
		t.Fatalf("remove: %v\n%s", err, out)
	}
	if want := "removed acme/review@1.2.3 (4 file(s) from review, review-lint); dropped from .satelle/packs.toml"; strings.TrimSpace(out) != want {
		t.Errorf("output %q, want %q", out, want)
	}
	noReviewSkills(t, dir)
	if refs := recordedRefs(t, dir); len(refs) != 0 {
		t.Errorf("packs.toml still records %v", refs)
	}
	noStage(t, dir)
	must(t, "validate")

	must(t, "pack", "install", "acme/review", "--version", "1.2.3", "--server", srv.URL, "--session", "s-1")
	raw, err := remove(t, "acme/review", "--session", "s-1", "--json")
	if err != nil {
		t.Fatalf("remove --json: %v\n%s", err, raw)
	}
	var got struct {
		Ref, Version, Record          string
		Skills, Removed, Edited, Kept []string
		Forced                        bool
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("json %v:\n%s", err, raw)
	}
	wantRemoved := []string{".satelle/skills/review-lint/SKILL.md", ".satelle/skills/review-lint/scripts/check.sh", ".satelle/skills/review/SKILL.md", ".satelle/skills/review/pack.toml"}
	if got.Ref != "acme/review" || got.Version != "1.2.3" || got.Record != ".satelle/packs.toml" || got.Forced ||
		!slices.Equal(got.Skills, []string{"review", "review-lint"}) || !slices.Equal(got.Removed, wantRemoved) ||
		len(got.Edited) != 0 || len(got.Kept) != 0 {
		t.Errorf("json %+v", got)
	}
	noReviewSkills(t, dir)

	if _, err := remove(t, "acme/review", "--session", "s-1"); err == nil || !strings.Contains(err.Error(), "acme/review is not installed") {
		t.Errorf("remove of a pack not installed: %v", err)
	}
}

func TestPackRemoveRefusesEditedFile(t *testing.T) {
	dir, srv, _ := packRepo(t)
	must(t, "pack", "install", "acme/review", "--version", "1.2.3", "--server", srv.URL, "--session", "s-1")
	if err := os.WriteFile(filepath.Join(dir, ".satelle", "skills", "review", "SKILL.md"), append(skillBody("review"), "mine\n"...), 0o644); err != nil {
		t.Fatal(err)
	}
	skills := filepath.Join(dir, ".satelle", "skills")
	before := tree(t, skills)
	record, _ := os.ReadFile(filepath.Join(dir, ".satelle", "packs.toml"))

	out, err := remove(t, "acme/review", "--session", "s-1")
	if err == nil {
		t.Fatalf("remove of an edited pack succeeded:\n%s", out)
	}
	if !strings.Contains(err.Error()+out, ".satelle/skills/review/SKILL.md") {
		t.Errorf("refusal does not name the edited file: %v\n%s", err, out)
	}
	if after := tree(t, skills); !maps.Equal(before, after) {
		t.Errorf("a refused remove changed .satelle/skills:\n%v\nwant\n%v", after, before)
	}
	if after, _ := os.ReadFile(filepath.Join(dir, ".satelle", "packs.toml")); string(after) != string(record) {
		t.Error("a refused remove rewrote packs.toml")
	}
	noStage(t, dir)
}

func TestPackRemoveForce(t *testing.T) {
	dir, srv, _ := packRepo(t)
	must(t, "pack", "install", "acme/review", "--version", "1.2.3", "--server", srv.URL, "--session", "s-1")
	skills := filepath.Join(dir, ".satelle", "skills")
	if err := os.WriteFile(filepath.Join(skills, "review", "SKILL.md"), append(skillBody("review"), "mine\n"...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skills, "review-lint", "local.md"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := remove(t, "acme/review", "--force", "--session", "s-1")
	if err != nil {
		t.Fatalf("remove --force: %v\n%s", err, out)
	}
	for _, w := range []string{
		"removed acme/review@1.2.3 (4 file(s) from review, review-lint); dropped from .satelle/packs.toml",
		"removed edited .satelle/skills/review/SKILL.md",
		"kept .satelle/skills/review-lint/local.md: not recorded by the pack",
	} {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}
	if _, err := os.Lstat(filepath.Join(skills, "review")); !os.IsNotExist(err) {
		t.Errorf(".satelle/skills/review still exists (%v)", err)
	}
	got := tree(t, filepath.Join(skills, "review-lint"))
	if want := map[string]string{".": "d---------", "local.md": "local\n"}; !maps.Equal(got, want) {
		t.Errorf("review-lint holds %v, want only local.md", got)
	}
	if refs := recordedRefs(t, dir); len(refs) != 0 {
		t.Errorf("packs.toml still records %v", refs)
	}
	noStage(t, dir)
}

func TestPackRemoveRequiresTreeSeat(t *testing.T) {
	dir, srv, _ := packRepo(t)
	must(t, "pack", "install", "acme/review", "--version", "1.2.3", "--server", srv.URL, "--session", "s-1")
	plan := createID(t, "story", "create", "--title", "planning", "--workflow", "planned")
	must(t, "story", "set", plan, "--status", "st-plan")
	must(t, "story", "seat", plan, "--session", "s-3")
	skills := filepath.Join(dir, ".satelle", "skills")
	before := tree(t, skills)
	record, _ := os.ReadFile(filepath.Join(dir, ".satelle", "packs.toml"))
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"unseated", []string{"--session", "s-2"}, "Seat one with"},
		{"attachments-only seat", []string{"--session", "s-3"}, "produces an attachment"},
		{"no session", nil, "no session id"},
	} {
		_, err := remove(t, "acme/review", c.args...)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
		if after := tree(t, skills); !maps.Equal(before, after) {
			t.Errorf("%s: .satelle/skills changed", c.name)
		}
		if after, _ := os.ReadFile(filepath.Join(dir, ".satelle", "packs.toml")); string(after) != string(record) {
			t.Errorf("%s: packs.toml changed", c.name)
		}
		noStage(t, dir)
	}
}

func TestValidateReportsWorkflowNamingRemovedPackSkill(t *testing.T) {
	dir, srv, _ := packRepo(t)
	must(t, "pack", "install", "acme/review", "--version", "1.2.3", "--server", srv.URL, "--session", "s-1")
	workflow := `name = "planned"
[states.st-one]
start = true
[states.st-plan]
performer = { agent = "agent-x9", skill = "work" }
edits = "attachments"
[states.st-build]
performer = { agent = "agent-x9", skill = "work" }
[states.st-done]
terminal = true
[gates.pack-lint]
skill = "review-lint"
agent = "judge-x9"
[[transition]]
from = ["st-one"]
to = "st-plan"
[[transition]]
from = ["st-plan"]
to = "st-build"
[[transition]]
from = ["st-build"]
to = "st-done"
groups = [{ gates = ["pack-lint"] }]
`
	if err := os.WriteFile(filepath.Join(dir, ".satelle", "workflows", "planned.toml"), []byte(workflow), 0o644); err != nil {
		t.Fatal(err)
	}
	agents := filepath.Join(dir, ".satelle", "agents.toml")
	ab, err := os.ReadFile(agents)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agents, append(ab, "\n[agents.judge-x9]\ncommand = \"claude\"\n"...), 0o644); err != nil {
		t.Fatal(err)
	}
	must(t, "validate")

	if out, err := remove(t, "acme/review", "--session", "s-1"); err != nil {
		t.Fatalf("remove: %v\n%s", err, out)
	}
	out, err := run(t, "", "validate")
	if err == nil {
		t.Fatalf("validate passed with a workflow naming a removed skill:\n%s", out)
	}
	for _, w := range []string{"workflow planned", "pack-lint", `skill "review-lint" is not in skills/`} {
		if !strings.Contains(err.Error()+out, w) {
			t.Errorf("validate does not report %q: %v\n%s", w, err, out)
		}
	}
	raw, _ := run(t, "", "validate", "--json")
	var got struct {
		Valid    bool
		Problems []string
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("json %v:\n%s", err, raw)
	}
	if got.Valid || !slices.ContainsFunc(got.Problems, func(p string) bool {
		return strings.Contains(p, "planned") && strings.Contains(p, "review-lint")
	}) {
		t.Errorf("validate --json %s", raw)
	}
}
