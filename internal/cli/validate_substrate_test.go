package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/help"
	"github.com/bobmcallan/satelle/internal/substrate"
)

// In any repo satelle init made, satelle validate reports each substrate miss
// (a command the binary lacks, a dead link) with its file and line, and fails;
// the kernel still opens; fixing both makes validate pass (sto_a1681009 AC2).
func TestValidateReportsSubstrateDrift(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	skill := filepath.Join(dir, ".satelle", "skills", "stale.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(body string) {
		if err := os.WriteFile(skill, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("---\nname: stale\ntype: skill\ndescription: Names what the binary lacks.\n---\n\nRun `satelle nosuchcmd <id>`.\n\nSee [[missing]].\n")

	out, err := run(t, "", "validate")
	if err == nil {
		t.Fatalf("validate passed on a drifted substrate:\n%s", out)
	}
	for _, w := range []string{".satelle/skills/stale.md:7: command: satelle nosuchcmd — ", ".satelle/skills/stale.md:9: link: missing — "} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("validate error lacks %q:\n%v", w, err)
		}
	}

	out, err = run(t, "", "validate", "--json")
	if err == nil {
		t.Fatalf("validate --json passed on a drifted substrate:\n%s", out)
	}
	var rep struct {
		Valid     bool                `json:"valid"`
		Problems  []string            `json:"problems"`
		Substrate []substrate.Finding `json:"substrate"`
	}
	if jerr := json.Unmarshal([]byte(out), &rep); jerr != nil {
		t.Fatalf("validate --json: %v\n%s", jerr, out)
	}
	if rep.Valid || len(rep.Problems) != 0 || len(rep.Substrate) != 2 {
		t.Fatalf("want valid:false, no problems and two substrate entries:\n%s", out)
	}
	if f := rep.Substrate[0]; f.Kind != substrate.KindCommand || f.File != ".satelle/skills/stale.md" || f.Line != 7 {
		t.Errorf("first entry %+v", f)
	}
	if f := rep.Substrate[1]; f.Kind != substrate.KindLink || f.File != ".satelle/skills/stale.md" || f.Line != 9 || f.Subject != "missing" {
		t.Errorf("second entry %+v", f)
	}

	// The kernel still opens: a drifted substrate is not a configuration problem.
	must(t, "story", "list")

	write("---\nname: stale\ntype: skill\ndescription: Names what the binary has.\n---\n\nRun `satelle story get <id>`.\n\nSee [[stale]].\n")
	out = must(t, "validate", "--json")
	if !strings.Contains(out, `"substrate": []`) || !strings.Contains(out, `"valid": true`) {
		t.Fatalf("fixed substrate: want valid and substrate []:\n%s", out)
	}
}

// repoLinks are the names a [[link]] in this repo's own documents may name:
// its principles and skills.
func repoLinks(t *testing.T) map[string]bool {
	t.Helper()
	links := map[string]bool{}
	for _, d := range []string{"principles", "skills"} {
		ents, err := os.ReadDir(filepath.Join("..", "..", ".satelle", d))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range ents {
			links[strings.TrimSuffix(e.Name(), ".md")] = true
		}
	}
	return links
}

// Every embedded help topic names only commands, flags and topics this binary
// has, and links only principles or skills of this repo: the help topics ship
// in the binary, so they are checked here rather than by satelle validate.
func TestHelpTopicsNameTheBinary(t *testing.T) {
	links := repoLinks(t)
	tree := substrateTree(NewRootCmd())
	for _, tp := range help.List() {
		for _, f := range substrate.CheckText("internal/help/topics/"+tp.Name+".md", "md", tp.Body, tree, links) {
			t.Error(f.String())
		}
	}
}

// reReadmeTarget is a markdown link's target: [text](target).
var reReadmeTarget = regexp.MustCompile(`\]\(([^)\s]+)\)`)

// README.md is the first thing a new user's agent reads, and it is outside
// the substrate satelle validate checks. It names only commands, flags and
// help topics this binary has, and every relative link in it names a path in
// this repo (sto_33132ea7).
func TestReadmeNamesTheBinaryAndRepo(t *testing.T) {
	root := filepath.Join("..", "..")
	b, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range substrate.CheckText("README.md", "md", string(b), substrateTree(NewRootCmd()), repoLinks(t)) {
		t.Error(f.String())
	}
	for n, line := range strings.Split(string(b), "\n") {
		for _, m := range reReadmeTarget.FindAllStringSubmatch(line, -1) {
			target := m[1]
			if strings.Contains(target, "://") || strings.HasPrefix(target, "#") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			target, _, _ = strings.Cut(target, "#")
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(target))); err != nil {
				t.Errorf("README.md:%d: path: %s — not in this repo", n+1, target)
			}
		}
	}
}
