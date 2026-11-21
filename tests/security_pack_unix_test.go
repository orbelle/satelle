//go:build integration && unix

package tests

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
	"github.com/bobmcallan/satelle/internal/workflow"
)

const (
	securitySkill    = "satelle-security-review"
	securityEntry    = "satelle-security-reviewer"
	securitySkillRel = ".satelle/skills/" + securitySkill + "/" + workflow.SkillFile
	securityEntryDir = ".satelle/skills/" + securityEntry
)

var securityPackFiles = []string{
	securitySkillRel,
	securityEntryDir + "/" + workflow.SkillFile,
	securityEntryDir + "/" + workflow.PackFile,
}

func securityRepoValuePatterns(t *testing.T) map[string]*regexp.Regexp {
	t.Helper()
	out := map[string]*regexp.Regexp{
		"a principle link":           regexp.MustCompile(`\[\[[a-z0-9-]+\]\]`),
		"a path under .satelle":      regexp.MustCompile(`\.satelle/(tasks|workflows|principles|constitution)/`),
		"a path under internal/":     regexp.MustCompile(`\binternal/`),
		"an item id":                 regexp.MustCompile(`(sto|exe)_[0-9a-f]{8}`),
		"the state name in_progress": regexp.MustCompile(`in_progress`),
	}
	var agents struct {
		Agents map[string]toml.Primitive `toml:"agents"`
	}
	if _, err := toml.Decode(repoText(t, ".satelle/agents.toml"), &agents); err != nil {
		t.Fatal(err)
	}
	for name := range agents.Agents {
		out["the agent "+name] = regexp.MustCompile(regexp.QuoteMeta(name))
	}
	files, err := filepath.Glob("../.satelle/workflows/*.toml")
	if err != nil || len(files) == 0 {
		t.Fatalf("workflows: %v %v", files, err)
	}
	for _, f := range files {
		var wf struct {
			States map[string]toml.Primitive `toml:"states"`
			Gates  map[string]toml.Primitive `toml:"gates"`
		}
		if _, err := toml.DecodeFile(f, &wf); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for name := range wf.Gates {
			q := regexp.QuoteMeta(name)
			out["the gate "+name] = regexp.MustCompile("`" + q + "`|gates\\." + q + `\b|"` + q + `"`)
			if strings.Contains(name, "-") {
				out["the hyphenated gate "+name] = regexp.MustCompile(`(^|[^A-Za-z0-9_-])` + q + `($|[^A-Za-z0-9_-])`)
			}
		}
		for name := range wf.States {
			q := regexp.QuoteMeta(name)
			out["the state "+name] = regexp.MustCompile("`" + q + "`|\"" + q + `"|` + q + `\s*→|→\s*` + q + `\b`)
		}
	}
	return out
}

func TestSecurityReviewSkillHoldsNoRepoValue(t *testing.T) {
	strip := strings.NewReplacer(
		"satelle-judge-differs-from-performer-check", "",
		".satelle/skills/satelle-pack.md", "",
		securityEntry, "",
		securitySkill, "",
	)
	patterns := securityRepoValuePatterns(t)
	for _, rel := range securityPackFiles {
		text := strip.Replace(repoText(t, rel))
		for what, re := range patterns {
			for _, m := range re.FindAllString(text, -1) {
				t.Errorf("%s holds %s: %q", rel, what, m)
			}
		}
	}

	flat := ".satelle/skills/" + securitySkill + ".md"
	if _, err := os.Stat(filepath.Join("..", flat)); !os.IsNotExist(err) {
		t.Errorf("the single-file skill %s is still present: %v", flat, err)
	}
	skills, err := workflow.LoadSkills("../.satelle/skills")
	if err != nil {
		t.Fatal(err)
	}
	sk, ok := skills[securitySkill]
	if !ok || !sk.IsDir() {
		t.Fatalf("%s: ok=%v, not a skill directory", securitySkill, ok)
	}
	if probs := sk.Problems(); len(probs) != 0 {
		t.Errorf("%s problems: %v", securitySkill, probs)
	}
	for _, w := range []string{
		"1. **Secrets and credentials.**", "2. **Injection and unsafe input.**", "3. **Authentication and authorization.**",
		"4. **Process controls.**", "5. **Network and supply chain.**", "6. **Writes outside the work.**",
		`{"verdict": "accept", "notes": "<what you judged: for each check 1-6, the file, line or plan section you read and why it is safe, or that the work does not touch it>"}`,
		"changed paths and a diff", "attachments/plan",
	} {
		if !strings.Contains(sk.Body, w) {
			t.Errorf("%s does not hold %q", securitySkill, w)
		}
	}

	type gateDef struct {
		Skill   string   `toml:"skill"`
		Panel   []string `toml:"panel"`
		Combine string   `toml:"combine"`
		After   []string `toml:"after"`
	}
	var bound []gateDef
	for _, rel := range []string{".satelle/workflows/delivery.toml", ".satelle/workflows/chore.toml"} {
		var wf struct {
			Gates map[string]gateDef `toml:"gates"`
		}
		if _, err := toml.Decode(repoText(t, rel), &wf); err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		g, ok := wf.Gates["security"]
		if !ok || g.Skill != securitySkill || len(g.Panel) == 0 || g.Combine != "all" || len(g.After) == 0 {
			t.Errorf("%s gates.security = %+v (present %v)", rel, g, ok)
		}
		bound = append(bound, g)
	}
	if len(bound) == 2 && (!slices.Equal(bound[0].Panel, bound[1].Panel) || !slices.Equal(bound[0].After, bound[1].After)) {
		t.Errorf("the two security gates differ: %+v vs %+v", bound[0], bound[1])
	}

	s := newSandbox(t)
	named, err := gittest.Command(t, "..", "grep", "-l", "-F", "skills/"+securitySkill+".md").Output()
	if ee, isExit := err.(*exec.ExitError); err != nil && (!isExit || ee.ExitCode() != 1) {
		t.Fatalf("git grep: %v", err)
	}
	for _, f := range strings.Fields(string(named)) {
		if f != "CHANGELOG.md" {
			t.Errorf("%s names the removed single-file skill", f)
		}
	}

	copyCheckout(t, "..", s.dir)
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "this repo's tree")
	r := s.exec(satelleBin, "", "validate", "--json")
	var v struct {
		Valid         bool              `json:"valid"`
		Problems      []string          `json:"problems"`
		SkillWarnings []json.RawMessage `json:"skill_warnings"`
	}
	if err := json.Unmarshal([]byte(r.out), &v); err != nil {
		t.Fatalf("validate --json: exit %d: %v\n%s%s", r.code, err, r.out, r.err)
	}
	if !v.Valid || r.code != 0 || len(v.Problems) != 0 || len(v.SkillWarnings) != 0 {
		t.Errorf("validate on this repo: valid %v, exit %d, problems %v, skill_warnings %s", v.Valid, r.code, v.Problems, v.SkillWarnings)
	}
}

func TestSecurityReviewSkillJudgesThePlanMoveAndTheLandingMove(t *testing.T) {
	t.Parallel()
	s := newSandbox(t)
	s.env["SATELLE_SESSION"] = "sec-modes-it"
	fake := fakeClaude(t, s)
	s.must("init")
	s.write(".satelle/agents.toml", "[agents.sec-judge]\ncommand = \"claude\"\nmodel = \"sm-1\"\ntools = [\"Read\", \"Grep\", \"Glob\"]\n")
	s.write(".satelle/workflows/modes.toml", `name = "modes"
[states.planned]
start = true
[states.building]
[states.landed]
[states.done]
terminal = true

[gates.plan-sec]
skill = "satelle-security-review"
panel = ["sec-judge"]
combine = "all"

[gates.land-sec]
skill = "satelle-security-review"
panel = ["sec-judge"]
combine = "all"

[[transition]]
from = ["planned"]
to = "building"
groups = [{ mode = "sequential", gates = ["plan-sec"] }]

[[transition]]
from = ["building"]
to = "landed"
groups = [{ mode = "sequential", gates = ["land-sec"] }]

[[transition]]
from = ["landed"]
to = "done"
`)
	s.write(securitySkillRel, repoText(t, securitySkillRel))
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "baseline")
	s.mustConfigValid()
	skills, err := workflow.LoadSkills(filepath.Join(s.dir, ".satelle", "skills"))
	if err != nil {
		t.Fatal(err)
	}
	body := strings.TrimSpace(skills[securitySkill].Body)
	if body == "" {
		t.Fatal("the moved skill has no body")
	}

	id := s.create("story", "--title", "modes", "--workflow", "modes").ID
	s.must("story", "seat", id)
	for _, step := range []struct{ to, gate string }{{"building", "plan-sec"}, {"landed", "land-sec"}} {
		if step.to == "landed" {
			s.write("tools/run.sh", "#!/bin/sh\necho ok\n")
			s.git("add", "-A")
			s.git("commit", "-q", "-m", "a script")
		}
		s.must("story", "set", id, "--status", step.to)
		runs := argvs(t, fake, "sm-1")
		if len(runs) != 1 {
			t.Fatalf("%s dispatched the judge %d time(s), want 1", step.gate, len(runs))
		}
		if sys := strings.Join(runs[0], "\n"); !strings.Contains(sys, "--append-system-prompt\n"+body) {
			t.Errorf("%s: the judge's system prompt is not the moved skill body", step.gate)
		}
		if g := latestRun(s, id, step.gate); g.Result != "accept" {
			t.Errorf("%s result = %q, want accept (%s)", step.gate, g.Result, g.Notes)
		}
		if st := s.get(id).Status; st != step.to {
			t.Errorf("status = %q, want %q", st, step.to)
		}
	}
}

func TestSecurityReviewerPack(t *testing.T) {
	packs, _, err := workflow.LoadPacks("../.satelle/skills")
	if err != nil {
		t.Fatal(err)
	}
	pk, ok := packs[securityEntry]
	if !ok {
		t.Fatalf("no pack has entry %s: %v", securityEntry, packs)
	}
	if pk.Version != "1.0.0" || !slices.Equal(pk.Skills, []string{securitySkill}) {
		t.Errorf("pack %s = version %q members %v, want 1.0.0 [%s]", securityEntry, pk.Version, pk.Skills, securitySkill)
	}
	skills, err := workflow.LoadSkills("../.satelle/skills")
	if err != nil {
		t.Fatal(err)
	}
	if errs := pk.Validate(func(name string) (workflow.Skill, bool) { sk, ok := skills[name]; return sk, ok }); len(errs) != 0 {
		t.Errorf("pack %s: %v", securityEntry, errs)
	}
	if entry, ok := skills[securityEntry]; !ok || !entry.IsDir() || len(entry.Problems()) != 0 {
		t.Errorf("entry skill %s: ok=%v", securityEntry, ok)
	}

	check := exec.Command("sh", append([]string{"scripts/comments.sh", "check"}, securityPackFiles...)...)
	check.Dir = ".."
	if out, err := check.CombinedOutput(); err != nil {
		t.Errorf("the pack's files hold comments (%v):\n%s", err, out)
	}

	guide := repoText(t, securityEntryDir+"/"+workflow.SkillFile)
	for _, w := range []string{
		"skills/satelle-security-review/SKILL.md",
		"plan review", "code review", ".satelle/",
		"panel", ".satelle/agents.toml", "read-only",
		"another model family", "satelle-judge-differs-from-performer-check",
		"trigger", "satelle story diff", "exit 0", "exit 1", "scripts",
		"satelle pack install", ".satelle/skills/satelle-pack.md",
	} {
		if !strings.Contains(guide, w) {
			t.Errorf("the pack guide does not say %q", w)
		}
	}
}

func securityPackTree(t *testing.T) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	root := filepath.Join("..", ".satelle", "skills")
	for _, name := range []string{securityEntry, securitySkill} {
		err := filepath.WalkDir(filepath.Join(root, name), func(p string, d os.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() {
				return err
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			b, err := os.ReadFile(p)
			out[filepath.ToSlash(rel)] = b
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func publishSecurityPack(t *testing.T) *fakeserver.Server {
	t.Helper()
	srv := fakeserver.Start(t, devToken)
	srv.SetPackAdmins("ws_sec", devToken)
	s := newSandbox(t)
	s.env["SATELLE_TOKEN"] = devToken
	copyCheckout(t, "..", s.dir)
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "this repo's tree")
	s.must("pack", "publish", securityEntry, "--workspace", "ws_sec", "--visibility", "public", "--server", srv.URL, "--json")
	return srv
}

func TestSecurityReviewerPackPublishesThisRepoFiles(t *testing.T) {
	t.Parallel()
	srv := publishSecurityPack(t)
	pubs := srv.Published()
	if len(pubs) != 1 {
		t.Fatalf("published %d request(s), want 1", len(pubs))
	}
	req := pubs[0]
	man := req.GetManifest()
	if req.GetVisibility() != satellepb.PackVisibility_PACK_VISIBILITY_PUBLIC || man.GetName() != securityEntry || man.GetVersion() != "1.0.0" {
		t.Fatalf("published %s@%s visibility %v, want %s@1.0.0 public", man.GetName(), man.GetVersion(), req.GetVisibility(), securityEntry)
	}
	want := map[string]string{}
	repo := securityPackTree(t)
	for p, b := range repo {
		sum := sha256.Sum256(b)
		want[p] = hex.EncodeToString(sum[:])
	}
	got := map[string]string{}
	for _, r := range man.GetFiles() {
		got[r.GetPath()] = r.GetSha256()
	}
	if !maps.Equal(got, want) {
		t.Errorf("manifest files %v, want %v", got, want)
	}
	sent := 0
	for _, f := range req.GetFiles() {
		sent++
		if string(f.GetBody()) != string(repo[f.GetPath()]) {
			t.Errorf("sent %s differs from this repo's file", f.GetPath())
		}
	}
	if sent != len(repo) {
		t.Errorf("sent %d file(s), want %d", sent, len(repo))
	}
}

func TestSecurityReviewerPackInstallsAndGatesARoute(t *testing.T) {
	t.Parallel()
	srv := publishSecurityPack(t)
	s := signedIn(t)
	s.env["SATELLE_SESSION"] = "sec-pack-it"
	fake := fakeClaude(t, s)
	if _, err := os.Stat(filepath.Join(s.dir, ".satelle", "skills", securitySkill)); !os.IsNotExist(err) {
		t.Fatalf("the second repo already has %s: %v", securitySkill, err)
	}
	s.write(".satelle/agents.toml", "[agents.sec-judge]\ncommand = \"claude\"\nmodel = \"sj-1\"\ntools = [\"Read\", \"Grep\", \"Glob\"]\n\n[agents.builder]\ncommand = \"session\"\n")
	s.write(".satelle/skills/work.md", "---\nname: work\n---\nBuild it.\n")
	route := func(gate string) string {
		return `name = "taker"
[states.in_progress]
start = true
performer = { agent = "builder", skill = "work" }
[states.landed]
[states.done]
terminal = true
` + gate + `
[[transition]]
from = ["in_progress"]
to = "landed"
groups = [{ mode = "sequential", gates = ["security"] }]

[[transition]]
from = ["landed"]
to = "done"
`
	}
	s.write(".satelle/workflows/taker.toml", strings.Replace(route(""), `groups = [{ mode = "sequential", gates = ["security"] }]`+"\n", "", 1))
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "baseline")

	setup := s.create("story", "--title", "take the security pack", "--workflow", "taker").ID
	s.must("story", "seat", setup)
	s.must("pack", "install", "ws_sec/"+securityEntry, "--version", "1.0.0", "--server", srv.URL)
	rec := readSandbox(t, s, ".satelle/packs.toml")
	for _, name := range []string{securityEntry, securitySkill} {
		if !strings.Contains(rec, `"`+name+`"`) {
			t.Errorf("packs.toml does not record %s:\n%s", name, rec)
		}
	}
	if got := readSandbox(t, s, securitySkillRel); got != repoText(t, securitySkillRel) {
		t.Fatal("the installed security review skill differs from this repo's file")
	}

	s.write(".satelle/workflows/taker.toml", route(`
[gates.security]
skill = "satelle-security-review"
panel = ["sec-judge"]
combine = "all"
trigger = "script-trigger"
`))
	s.write(".satelle/skills/script-trigger.md", `---
name: script-trigger
type: check
description: Exits 0 when the story's diff touches a script, 1 when it touches none.
---

# Script trigger

`+"```check"+`
#!/usr/bin/env bash
set -uo pipefail
sid=${SATELLE_ITEM:-}
case $sid in
  sto_*) ;;
  *) echo "$sid is not a story"; exit 1 ;;
esac
diff=$(satelle story diff "$sid" --json) || { echo "no diff for $sid"; exit 2; }
paths=$(printf '%s' "$diff" | jq -r '.paths // [] | .[]') || exit 2
if printf '%s\n' "$paths" | grep -qE '\.sh$|(^|/)scripts/'; then
  echo "scripts changed"
  exit 0
fi
echo "no script changed"
exit 1
`+"```"+`
`)
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "wire the security gate")
	s.mustConfigValid()
	installed, err := workflow.LoadSkills(filepath.Join(s.dir, ".satelle", "skills"))
	if err != nil {
		t.Fatal(err)
	}
	body := strings.TrimSpace(installed[securitySkill].Body)

	s.write("tools/run.sh", "#!/bin/sh\necho ok\n")
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "a script")
	s.must("story", "set", setup, "--status", "landed")
	runs := argvs(t, fake, "sj-1")
	if len(runs) != 1 {
		t.Fatalf("security dispatched the judge %d time(s) on a script change, want 1", len(runs))
	}
	if sys := strings.Join(runs[0], "\n"); body == "" || !strings.Contains(sys, "--append-system-prompt\n"+body) {
		t.Error("the judge's system prompt is not the installed skill body")
	}
	if g := latestRun(s, setup, "security"); g.Result != "accept" {
		t.Errorf("security on a script change = %q, want accept (%s)", g.Result, g.Notes)
	}
	if st := s.get(setup).Status; st != "landed" {
		t.Errorf("status = %q, want landed", st)
	}

	docs := s.create("story", "--title", "docs only", "--workflow", "taker").ID
	s.must("story", "seat", docs)
	s.write("docs/x.md", "# x\n")
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "docs")
	s.must("story", "set", docs, "--status", "landed")
	if n := len(argvs(t, fake, "sj-1")); n != 0 {
		t.Errorf("security dispatched the judge %d time(s) on a docs change, want 0", n)
	}
	if g := latestRun(s, docs, "security"); g.Result != "skipped" {
		t.Errorf("security on a docs change = %q, want skipped (%s)", g.Result, g.Notes)
	}
}
