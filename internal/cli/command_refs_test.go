package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/substrate"
)

const (
	fixtureRulePath  = ".satelle/principles/fixture-rule.md"
	fixtureSkillPath = ".satelle/skills/fixture-skill.md"
	fixtureHelpBlock = "\nPrinciples and skills keyed to this command:\n" +
		"  fixture-rule  " + fixtureRulePath + "\n      a rule for new cards\n" +
		"  fixture-skill  " + fixtureSkillPath + "\n      a skill for cards\n"
	fixtureSeeRule  = "see fixture-rule: " + fixtureRulePath + "\n"
	fixtureSeeSkill = "see fixture-skill: " + fixtureSkillPath + "\n"
)

func keyedFixture(t *testing.T, ruleCommands, skillCommands string) string {
	t.Helper()
	dir := isolate(t)
	must(t, "init")
	writeFile(t, filepath.Join(dir, fixtureRulePath), "---\nname: fixture-rule\ndescription: a rule for new cards\ncommands: "+ruleCommands+"\n---\n\nbody of the rule\n")
	writeFile(t, filepath.Join(dir, fixtureSkillPath), "---\nname: fixture-skill\ndescription: a skill for cards\ncommands: "+skillCommands+"\n---\n\nbody of the skill\n")
	return dir
}

func TestHelpListsKeyedFiles(t *testing.T) {
	dir := keyedFixture(t, "story create", "satelle story create, story seat")
	for _, args := range [][]string{{"story", "create", "--help"}, {"help", "story", "create"}} {
		out := must(t, args...)
		if !strings.HasSuffix(out, fixtureHelpBlock) || !strings.Contains(out, "satelle story create --title T") {
			t.Errorf("%v does not end with the keyed files after the usage:\n%s", args, out)
		}
		if strings.Contains(out, "body of the") {
			t.Errorf("%v prints a file's body:\n%s", args, out)
		}
	}
	if out := must(t, "story", "seat", "--help"); !strings.HasSuffix(out, "\nPrinciples and skills keyed to this command:\n  fixture-skill  "+fixtureSkillPath+"\n      a skill for cards\n") {
		t.Errorf("story seat --help does not end with the one file keyed to it:\n%s", out)
	}
	if out := must(t, "story", "--help"); strings.Contains(out, "keyed to this command") {
		t.Errorf("story --help lists a file keyed to its subcommand:\n%s", out)
	}
	t.Chdir(filepath.Dir(dir))
	for _, args := range [][]string{
		{"-C", dir, "story", "create", "--help"}, {"-C", filepath.Base(dir), "story", "create", "--help"},
		{"-C", dir, "help", "story", "create"}, {"-C", filepath.Base(dir), "help", "story", "create"},
	} {
		if out := must(t, args...); !strings.HasSuffix(out, fixtureHelpBlock) {
			t.Errorf("%v from another directory does not end with the keyed files:\n%s", args, out)
		}
	}
}

func TestRunNamesKeyedFiles(t *testing.T) {
	keyedFixture(t, "story create", "satelle story create, story seat")
	stdout, stderr, err := runSplit(t, "", "story", "create", "--title", "t")
	if err != nil {
		t.Fatalf("story create: %v\n%s%s", err, stdout, stderr)
	}
	if strings.Count(stdout, "\n") != 1 || !strings.Contains(stdout, " created (") {
		t.Errorf("stdout is not the created line alone:\n%s", stdout)
	}
	if stderr != fixtureSeeRule+fixtureSeeSkill {
		t.Errorf("stderr is not the two keyed files, principle first:\n%s", stderr)
	}
	id, _, _ := strings.Cut(stdout, " ")
	if _, stderr, err := runSplit(t, "", "story", "seat", id, "--session", "s-ref"); err != nil || stderr != fixtureSeeSkill {
		t.Errorf("story seat: stderr %q (%v), want the one file keyed to it", stderr, err)
	}
	if _, stderr, err := runSplit(t, "", "story", "list"); err != nil || stderr != "" {
		t.Errorf("story list, keyed by no file: stderr %q (%v)", stderr, err)
	}
}

func TestRunKeepsJSONOneDocument(t *testing.T) {
	keyedFixture(t, "story create", "satelle story create, story seat")
	stdout, stderr, err := runSplit(t, "", "story", "create", "--title", "t", "--json")
	if err != nil {
		t.Fatalf("story create --json: %v\n%s%s", err, stdout, stderr)
	}
	dec := json.NewDecoder(strings.NewReader(stdout))
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil || doc["id"] == nil {
		t.Fatalf("stdout does not open with the item document (%v):\n%s", err, stdout)
	}
	var more any
	if err := dec.Decode(&more); err != io.EOF {
		t.Errorf("stdout holds more than one JSON document (%v):\n%s", err, stdout)
	}
	if stderr != fixtureSeeRule+fixtureSeeSkill {
		t.Errorf("stderr is not the two keyed files:\n%s", stderr)
	}
}

func TestFailedRunNamesNoFile(t *testing.T) {
	keyedFixture(t, "story get", "story get")
	id := createID(t, "story", "create", "--title", "t")
	if _, stderr, err := runSplit(t, "", "story", "get", id); err != nil || stderr != fixtureSeeRule+fixtureSeeSkill {
		t.Fatalf("story get of a real item: stderr %q (%v), want the keyed files", stderr, err)
	}
	stdout, stderr, err := runSplit(t, "", "story", "get", "nosuch-id")
	if err == nil {
		t.Fatalf("story get nosuch-id succeeded:\n%s", stdout)
	}
	if strings.Contains(stdout+stderr, "see fixture-") {
		t.Errorf("a failed command named a keyed file:\n%s%s", stdout, stderr)
	}
}

func TestUnkeyedCommandIsUnchanged(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	for _, d := range substrate.RuleDirs {
		if err := os.RemoveAll(filepath.Join(dir, ".satelle", d)); err != nil {
			t.Fatal(err)
		}
	}
	runs := [][]string{{"story", "list"}, {"story", "create", "--help"}, {"validate"}, {"status"}}
	capture := func() []string {
		t.Helper()
		var got []string
		for _, args := range runs {
			stdout, stderr, err := runSplit(t, "", args...)
			got = append(got, fmt.Sprintf("%s\x00%s\x00%v", stdout, stderr, err))
		}
		return got
	}
	writeFile(t, filepath.Join(dir, ".satelle", "principles", "scoped.md"), "---\nname: scoped\ndescription: scoped to everything\n---\n\nbody\n")
	writeFile(t, filepath.Join(dir, fixtureSkillPath), "---\nname: fixture-skill\ndescription: a skill for cards\n---\n\nbody\n")
	bare := capture()
	writeFile(t, filepath.Join(dir, ".satelle", "principles", "scoped.md"), "---\nname: scoped\ndescription: scoped to everything\napplies_to: [\"*\"]\n---\n\nbody\n")
	writeFile(t, filepath.Join(dir, fixtureSkillPath), "---\nname: fixture-skill\ndescription: a skill for cards\ncommands: story seat\n---\n\nbody\n")
	keyed := capture()
	for i, args := range runs {
		if bare[i] != keyed[i] {
			t.Errorf("%v changed once files keyed to other commands exist:\n%q\n--\n%q", args, bare[i], keyed[i])
		}
	}
}

func TestValidateNamesUnknownKeyedCommand(t *testing.T) {
	dir := keyedFixture(t, "story nosuch", "story seat")
	const line = fixtureRulePath + ":4: command: satelle story nosuch — nosuch is not a subcommand of satelle story"
	out, err := run(t, "", "validate")
	if err == nil || !strings.Contains(err.Error(), line) {
		t.Fatalf("validate did not fail naming the file and the unknown command (%v):\n%s", err, out)
	}
	if regexp.MustCompile(regexp.QuoteMeta(fixtureSkillPath) + ":[0-9]+:").MatchString(out + err.Error()) {
		t.Errorf("validate reported a substrate finding in the file keyed to a real command (%v):\n%s", err, out)
	}
	out, _, _ = runSplit(t, "", "validate", "--json")
	var doc struct {
		Substrate []substrate.Finding `json:"substrate"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("validate --json: %v\n%s", err, out)
	}
	if len(doc.Substrate) != 1 || doc.Substrate[0].String() != line {
		t.Errorf("validate --json substrate list: %+v", doc.Substrate)
	}
	writeFile(t, filepath.Join(dir, fixtureRulePath), "---\nname: fixture-rule\ndescription: a rule for new cards\ncommands: story create\n---\n\nbody\n")
	must(t, "validate")
}

func TestInitKeysNoCommand(t *testing.T) {
	isolate(t)
	must(t, "init")
	r, err := findRepo("")
	if err != nil {
		t.Fatal(err)
	}
	refs, err := substrate.References(r.DataDir)
	if err != nil || len(refs) != 0 {
		t.Fatalf("a repo fresh from init keys %d file(s) to commands (%v): %+v", len(refs), err, refs)
	}
	if _, stderr, err := runSplit(t, "", "story", "create", "--title", "t"); err != nil || stderr != "" {
		t.Errorf("story create in a fresh repo: stderr %q (%v)", stderr, err)
	}
}
