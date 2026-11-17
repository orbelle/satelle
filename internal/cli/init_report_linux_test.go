//go:build linux

package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/kernel"
)

const fixtureCategory = "cat sub's"

func writeCarryWorkflow(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, ".satelle", "agents.toml"), "[agents.agent-x9]\ncommand = \"session\"\n")
	writeFile(t, filepath.Join(dir, ".satelle", "workflows", "carry.toml"), `name = "carry"
[states.st-one]
start = true
[states.st-two]
performer = { agent = "agent-x9", skill = "work" }
[states.st-three]
terminal = true
[[transition]]
from = ["st-one"]
to = "st-two"
[[transition]]
from = ["st-two"]
to = "st-three"
`)
	writeFile(t, filepath.Join(dir, ".satelle", "skills", "work.md"), "---\nname: work\n---\nDo it.\n")
	p := filepath.Join(dir, ".satelle", "satelle.toml")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, p, string(b)+fmt.Sprintf("\n[[workflows.rule]]\ncategory = %q\nworkflow = \"carry\"\n", fixtureCategory))
}

func setInit(t *testing.T, dir, text string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, ".satelle", kernel.InitFile), text)
}

func dropInit(t *testing.T, dir string) {
	t.Helper()
	if err := os.Remove(filepath.Join(dir, ".satelle", kernel.InitFile)); err != nil {
		t.Fatal(err)
	}
}

func categoryLine(cat string) string { return fmt.Sprintf("category = %q\n", cat) }

func commitAll(t *testing.T, dir, msg string) {
	t.Helper()
	gittest.Git(t, dir, "add", "-A")
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", msg)
}

func seatStory(t *testing.T, session, state string, createArgs ...string) string {
	t.Helper()
	id := createID(t, append([]string{"story", "create"}, createArgs...)...)
	must(t, "story", "seat", id, "--session", session)
	must(t, "story", "set", id, "--status", state)
	return id
}

var placeholder = regexp.MustCompile(`<[^>]*>`)

func printedCommand(t *testing.T, text, session string) []string {
	t.Helper()
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "satelle ") {
		t.Fatalf("printed command does not start with satelle: %q", text)
	}
	text = strings.ReplaceAll(text, "<session>", session)
	if p := placeholder.FindString(text); p != "" {
		t.Fatalf("printed command keeps the placeholder %s: %q", p, text)
	}
	out, err := exec.Command("/bin/sh", "-c", `eval "set -- $1"; for a; do printf '%s\0' "$a"; done`, "sh", text).Output()
	if err != nil {
		t.Fatalf("/bin/sh could not split %q: %v", text, err)
	}
	words := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	return words[1:]
}

func storyJSON(t *testing.T, id string) item.Item {
	t.Helper()
	var it item.Item
	if err := json.Unmarshal([]byte(must(t, "story", "get", id, "--json")), &it); err != nil {
		t.Fatal(err)
	}
	return it
}

func runPrinted(t *testing.T, words []string, files ...string) {
	t.Helper()
	id := createID(t, words...)
	it := storyJSON(t, id)
	if it.Category != fixtureCategory || it.Workflow != "carry" {
		t.Errorf("the printed command created category %q workflow %q, want %q carry", it.Category, it.Workflow, fixtureCategory)
	}
	for _, f := range files {
		if !slices.ContainsFunc(it.Criteria, func(c string) bool { return strings.Contains(c, ".satelle/"+f) }) {
			t.Errorf("criteria %v do not name .satelle/%s", it.Criteria, f)
		}
	}
}

func storyLine(out string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "  satelle story create") {
			return l
		}
	}
	return ""
}

func configuredFixture(t *testing.T) (dir, data string) {
	t.Helper()
	dir = isolate(t)
	must(t, "init")
	writeCarryWorkflow(t, dir)
	setInit(t, dir, categoryLine(fixtureCategory))
	commitAll(t, dir, "configured")
	data = filepath.Join(dir, ".satelle")
	for _, f := range []string{kernel.SessionFile, kernel.VolatileFile} {
		if err := os.Remove(filepath.Join(data, f)); err != nil {
			t.Fatal(err)
		}
	}
	return dir, data
}

func TestInitInAConfiguredRepoReportsAndWritesNothing(t *testing.T) {
	check := func(t *testing.T, data string, args ...string) string {
		t.Helper()
		before := digest(t, data)
		out := must(t, append([]string{"init"}, args...)...)
		for _, w := range []string{"nothing written under .satelle/", ".satelle/session.md", ".satelle/volatile.toml", "substrate story", "the user's decision", "satelle init --print"} {
			if !strings.Contains(out, w) {
				t.Errorf("init %v lacks %q:\n%s", args, w, out)
			}
		}
		if !strings.HasPrefix(storyLine(out), "  satelle story create --category ") {
			t.Errorf("init %v prints no story command:\n%s", args, out)
		}
		if d := sameDigest(before, digest(t, data)); len(d) != 0 {
			t.Fatalf("init %v wrote under .satelle/: %v", args, d)
		}
		return out
	}
	t.Run("no session", func(t *testing.T) {
		_, data := configuredFixture(t)
		check(t, data)
	})
	t.Run("no seat", func(t *testing.T) {
		_, data := configuredFixture(t)
		check(t, data, "--session", "s-1")
	})
	t.Run("tree seat", func(t *testing.T) {
		_, data := configuredFixture(t)
		seatStory(t, "s-1", "st-two", "--title", "seated", "--category", fixtureCategory)
		check(t, data, "--session", "s-1")
	})
	t.Run("the printed command runs", func(t *testing.T) {
		_, data := configuredFixture(t)
		out := check(t, data)
		runPrinted(t, printedCommand(t, storyLine(out), "s-1"), kernel.SessionFile, kernel.VolatileFile)
	})
	t.Run("exempt path", func(t *testing.T) {
		dir, data := configuredFixture(t)
		p := filepath.Join(data, "satelle.toml")
		b, _ := os.ReadFile(p)
		writeFile(t, p, string(b)+"\n[gate]\nedit_exempt_paths = [\".satelle/**\"]\n")
		if deny, why := hookGate(t, dir, "s-1", "Edit", ".satelle/session.md", ""); deny {
			t.Fatalf("the exempt path was denied: %s", why)
		}
		check(t, data, "--session", "s-1")
	})
	t.Run("no category", func(t *testing.T) {
		dir, data := configuredFixture(t)
		for _, tc := range []struct {
			name  string
			setup func()
			files int
		}{
			{"blank", func() { setInit(t, dir, "category = \"  \"\n") }, 2},
			{"absent", func() { dropInit(t, dir) }, 3},
		} {
			tc.setup()
			before := digest(t, data)
			out := must(t, "init")
			for _, w := range []string{categoryUnsetProblem(tc.files), "category in .satelle/init.toml", fmt.Sprintf("%q", fixtureCategory)} {
				if !strings.Contains(out, w) {
					t.Errorf("%s: init lacks %q:\n%s", tc.name, w, out)
				}
			}
			if strings.Contains(out, "story create") || strings.Contains(out, "<category>") {
				t.Errorf("%s: init prints a story command with no configured category:\n%s", tc.name, out)
			}
			if d := sameDigest(before, digest(t, data)); len(d) != 0 {
				t.Fatalf("%s: init wrote under .satelle/: %v", tc.name, d)
			}
		}
	})
	t.Run("hooks", func(t *testing.T) {
		dir, data := configuredFixture(t)
		before := digest(t, data)
		must(t, "init", "--hooks=claude")
		if d := sameDigest(before, digest(t, data)); len(d) != 0 {
			t.Fatalf("init --hooks wrote under .satelle/: %v", d)
		}
		if _, err := os.Stat(filepath.Join(dir, ".claude", "settings.json")); err != nil {
			t.Fatalf("init --hooks installed no claude hooks: %v", err)
		}
	})
	t.Run("complete repo", func(t *testing.T) {
		dir := isolate(t)
		must(t, "init")
		data := filepath.Join(dir, ".satelle")
		before := digest(t, data)
		if out := must(t, "init"); !strings.Contains(out, "nothing to add") {
			t.Errorf("init in a complete repo:\n%s", out)
		}
		if d := sameDigest(before, digest(t, data)); len(d) != 0 {
			t.Fatalf("init wrote under .satelle/: %v", d)
		}
	})
	t.Run("invalid configuration", func(t *testing.T) {
		_, data := configuredFixture(t)
		p := filepath.Join(data, "agents.toml")
		b, _ := os.ReadFile(p)
		writeFile(t, p, string(b)+"bogus_key = 1\n")
		before := digest(t, data)
		out, err := run(t, "", "init")
		if ExitCodeOf(err) != 1 || !strings.Contains(out, ".satelle/session.md") {
			t.Errorf("init with an invalid configuration: exit %d %v\n%s", ExitCodeOf(err), err, out)
		}
		if d := sameDigest(before, digest(t, data)); len(d) != 0 {
			t.Fatalf("init wrote under .satelle/: %v", d)
		}
	})
	t.Run("first init", func(t *testing.T) {
		dir := isolate(t)
		must(t, "init")
		pol := kernel.InitPolicy(filepath.Join(dir, ".satelle"))
		if !pol.CategorySet() {
			t.Fatalf("a first init wrote no category: %+v", pol)
		}
	})
}

func TestDoctorOpensNoItemForMissingShippedFiles(t *testing.T) {
	f := newDoctorFixture(t)
	noTerminal(t)
	writeCarryWorkflow(t, f.dir)
	setInit(t, f.dir, categoryLine(fixtureCategory))
	writeFile(t, filepath.Join(f.dir, "FAIL"), "skills/a.md:1: a finding\n")
	commitAll(t, f.dir, "fixture")
	data := filepath.Join(f.dir, ".satelle")
	for _, rel := range []string{kernel.SessionFile, kernel.VolatileFile} {
		if err := os.Remove(filepath.Join(data, rel)); err != nil {
			t.Fatal(err)
		}
	}
	before0 := itemIDs(t)
	before := digest(t, data)
	rep, text, _ := doctorJSON(t)
	var inits []doctorIssue
	for _, is := range rep.Issues {
		if is.Check == "init" {
			inits = append(inits, is)
		}
	}
	if len(inits) != 2 {
		t.Fatalf("init issues %+v, want two", inits)
	}
	for i, rel := range []string{kernel.SessionFile, kernel.VolatileFile} {
		is := inits[i]
		_, cmd, ok := strings.Cut(is.Fix, "Raise it with: ")
		if !strings.Contains(is.Problem, ".satelle/"+rel) || is.Resolve != resolveUserDecision || !strings.Contains(is.Fix, "the user's decision") ||
			!strings.Contains(is.Fix, "satelle init --print .satelle/"+rel) || !ok || !strings.HasPrefix(cmd, "satelle story create --category ") {
			t.Errorf("init issue for %s: %+v", rel, is)
		}
	}
	if !strings.Contains(text, "  fix: adding it changes the substrate") {
		t.Errorf("doctor text lacks the init fix line:\n%s", text)
	}
	ctx, _ := run(t, fmt.Sprintf(`{"session_id":"s-ctx","cwd":%q}`, f.dir), "hook", "context")
	if !strings.Contains(ctx, "should I raise and implement a satelle story to update and fix substrate issues") || !strings.Contains(ctx, "satelle story create --category ") {
		t.Errorf("hook context lacks the substrate question or the command:\n%s", ctx)
	}
	if got := itemIDs(t); !slices.Equal(got, before0) {
		t.Fatalf("doctor opened an item: %v", got)
	}
	res, out, err := resolveJSON(t)
	if ExitCodeOf(err) != 1 {
		t.Fatalf("--resolve exit %d, want 1\n%s", ExitCodeOf(err), out)
	}
	left := 0
	for _, l := range res.Left {
		if l.Check == "init" && l.Why == leftUserDecision {
			left++
		}
	}
	if left != 2 {
		t.Errorf("left %+v, want both init findings as the user's decision", res.Left)
	}
	for _, a := range res.Actions {
		if a.Check == "init" {
			t.Errorf("--resolve acted on an init finding: %+v", a)
		}
	}
	fixes := newItems(t, before0)
	if len(fixes) != 1 || strings.Contains(fixes[0].Body, "session.md") || strings.Contains(fixes[0].Body, "volatile.toml") {
		t.Fatalf("--resolve opened %+v, want one fix item for the check finding alone", fixes)
	}
	if d := sameDigest(before, digest(t, data)); len(d) != 0 {
		t.Fatalf("doctor wrote under .satelle/: %v", d)
	}
	_, cmd, _ := strings.Cut(inits[0].Fix, "Raise it with: ")
	runPrinted(t, printedCommand(t, cmd, "s-1"), kernel.SessionFile, kernel.VolatileFile)

	t.Run("no category", func(t *testing.T) {
		setInit(t, f.dir, "category = \"  \"\n")
		must(t, "validate")
		rep, text, _ := doctorJSON(t)
		var inits []doctorIssue
		for _, is := range rep.Issues {
			if is.Check == "init" {
				inits = append(inits, is)
			}
		}
		if len(inits) != 3 || inits[0].Problem != categoryUnsetProblem(2) || !strings.Contains(inits[0].Fix, "category in .satelle/init.toml") ||
			!strings.Contains(inits[0].Fix, fmt.Sprintf("%q", fixtureCategory)) {
			t.Fatalf("init issues with no category: %+v", inits)
		}
		res, _ := run(t, "", "doctor", "--resolve")
		rout, _ := run(t, "", "doctor", "--resolve", "--json")
		ctx, _ := run(t, fmt.Sprintf(`{"session_id":"s-ctx","cwd":%q}`, f.dir), "hook", "context")
		js, _ := json.Marshal(rep)
		for _, s := range []string{text, string(js), res, rout, ctx} {
			if strings.Contains(s, "story create") || strings.Contains(s, "<category>") {
				t.Errorf("an output holds a story command with no configured category:\n%s", s)
			}
		}
		createID(t, "story", "create", "--category", fixtureCategory, "--title", "the user's own", "--session", "s-1")
		setInit(t, f.dir, categoryLine(fixtureCategory))
		rep, _, _ = doctorJSON(t)
		n := 0
		for _, is := range rep.Issues {
			if is.Check == "init" {
				n++
				if !strings.Contains(is.Fix, "satelle story create --category ") {
					t.Errorf("issue lacks the command once the category is set: %+v", is)
				}
			}
		}
		if n != 2 {
			t.Errorf("init issues once the category is set: %d, want 2", n)
		}
	})
}

func TestDoctorNamesNoRuleCategories(t *testing.T) {
	session := mustShipped(t, kernel.SessionFile)
	dir := isolate(t)
	must(t, "init")
	setInit(t, dir, "category = \"\"\n")
	if err := os.Remove(filepath.Join(dir, ".satelle", kernel.SessionFile)); err != nil {
		t.Fatal(err)
	}
	out, _ := run(t, "", "doctor", "--json")
	if !strings.Contains(out, "No workflow rule") {
		t.Errorf("doctor with no rule categories:\n%s", out)
	}
	writeFile(t, filepath.Join(dir, ".satelle", kernel.SessionFile), string(session))
	if n := initIssueCount(t); n != 0 {
		t.Errorf("an unset category with no missing file gave %d init issue(s)", n)
	}
}

func initIssueCount(t *testing.T) int {
	t.Helper()
	out, _ := run(t, "", "doctor", "--json")
	var rep doctorReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("doctor --json: %v\n%s", err, out)
	}
	n := 0
	for _, is := range rep.Issues {
		if is.Check == "init" {
			n++
		}
	}
	return n
}

func mustShipped(t *testing.T, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "kernel", "template", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestInitPrintGivesTheShippedContent(t *testing.T) {
	src, err := filepath.Abs(filepath.Join("..", "kernel", "template"))
	if err != nil {
		t.Fatal(err)
	}
	shipped := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(src, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	dir, data := configuredFixture(t)
	commitAll(t, dir, "removed")
	files := []string{kernel.SessionFile, kernel.VolatileFile, kernel.InitFile}
	printAll := func(t *testing.T, extra ...string) {
		t.Helper()
		for _, rel := range files {
			for _, arg := range []string{".satelle/" + rel, rel} {
				before := digest(t, data)
				out := must(t, append([]string{"init", "--print", arg}, extra...)...)
				if out != shipped(rel) {
					t.Errorf("init --print %s is not the template source:\n%s", arg, out)
				}
				if d := sameDigest(before, digest(t, data)); len(d) != 0 {
					t.Fatalf("init --print wrote under .satelle/: %v", d)
				}
				if st := gittest.Git(t, dir, "status", "--porcelain"); strings.TrimSpace(st) != "" {
					t.Fatalf("git status after init --print:\n%s", st)
				}
			}
		}
	}
	printAll(t)
	printAll(t, "--session", "s-fix")
	if deny, _ := hookGate(t, dir, "s-none", "Edit", ".satelle/session.md", ""); !deny {
		t.Error("a seatless edit of a shipped file was allowed")
	}
	seatStory(t, "s-fix", "st-two", "--title", "add them", "--category", fixtureCategory)
	printAll(t, "--session", "s-fix")
	before := digest(t, data)
	must(t, "init", "--session", "s-fix")
	if d := sameDigest(before, digest(t, data)); len(d) != 0 {
		t.Fatalf("init from a tree seat wrote under .satelle/: %v", d)
	}
	if deny, why := hookGate(t, dir, "s-fix", "Edit", ".satelle/session.md", ""); deny {
		t.Fatalf("the seated session's edit of a shipped file was denied: %s", why)
	}
	for _, rel := range []string{kernel.SessionFile, kernel.VolatileFile} {
		writeFile(t, filepath.Join(data, rel), must(t, "init", "--print", ".satelle/"+rel))
	}
	if n := initIssueCount(t); n != 0 {
		t.Errorf("doctor still reports %d init issue(s) after the session's write", n)
	}
	if out := must(t, "init"); !strings.Contains(out, "nothing to add") {
		t.Errorf("init after the session's write:\n%s", out)
	}
	out, err := run(t, "", "init", "--print", ".satelle/nope.md")
	if ExitCodeOf(err) != 1 || !strings.Contains(err.Error(), ".satelle/nope.md") || strings.Contains(out, "---") {
		t.Errorf("init --print of an unshipped path: exit %d %v\n%s", ExitCodeOf(err), err, out)
	}
	if _, err := run(t, "", "init", "--print", ".satelle/session.md", "--hooks"); ExitCodeOf(err) != 1 {
		t.Errorf("init --print --hooks: %v", err)
	}
	bare := t.TempDir()
	t.Chdir(bare)
	if out := must(t, "init", "--print", ".satelle/session.md"); out != shipped(kernel.SessionFile) {
		t.Errorf("init --print outside a repo:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(bare, ".satelle")); !os.IsNotExist(err) {
		t.Errorf("init --print made .satelle outside a repo: %v", err)
	}
}

func commitRemoval(t *testing.T, dir string, rels ...string) {
	t.Helper()
	text := categoryLine(fixtureCategory) + "removed = ["
	for i, rel := range rels {
		gittest.Git(t, dir, "rm", "-q", ".satelle/"+rel)
		if i > 0 {
			text += ", "
		}
		text += fmt.Sprintf("%q", rel)
	}
	setInit(t, dir, text+"]\n")
	commitAll(t, dir, "remove shipped files")
	if st := gittest.Git(t, dir, "status", "--porcelain", "--untracked-files=all"); strings.TrimSpace(st) != "" {
		t.Fatalf("the removal left the tree dirty:\n%s", st)
	}
	for _, rel := range rels {
		if ls := gittest.Git(t, dir, "ls-files", "--", ".satelle/"+rel); strings.TrimSpace(ls) != "" {
			t.Fatalf("%s is still tracked", rel)
		}
	}
	if show := gittest.Git(t, dir, "show", "HEAD:.satelle/init.toml"); !strings.Contains(show, "removed = [") {
		t.Fatalf("HEAD's init.toml holds no removed line:\n%s", show)
	}
}

func cleanRepo(t *testing.T) string {
	t.Helper()
	dir := isolate(t)
	must(t, "init")
	writeCarryWorkflow(t, dir)
	setInit(t, dir, categoryLine(fixtureCategory))
	commitAll(t, dir, "configured")
	return dir
}

func TestRecordedRemovalIsNeitherAddedNorReported(t *testing.T) {
	t.Run("a skill", func(t *testing.T) {
		dir := cleanRepo(t)
		skill := "skills/satelle-workflow-standard.md"
		commitRemoval(t, dir, skill)
		gittest.Git(t, dir, "rm", "-q", ".satelle/"+kernel.VolatileFile)
		commitAll(t, dir, "unrecorded")
		out, _ := run(t, "", "doctor", "--json")
		if !strings.Contains(out, initProblem(kernel.VolatileFile)) || strings.Contains(out, initProblem(skill)) {
			t.Errorf("doctor with a recorded skill removal:\n%s", out)
		}
		text := must(t, "init")
		if !strings.Contains(text, ".satelle/"+kernel.VolatileFile) || strings.Contains(text, skill) {
			t.Errorf("init with a recorded skill removal:\n%s", text)
		}
		if _, err := os.Stat(filepath.Join(dir, ".satelle", filepath.FromSlash(skill))); !os.IsNotExist(err) {
			t.Errorf("the recorded skill was re-added: %v", err)
		}
		if st := gittest.Git(t, dir, "status", "--porcelain", "--untracked-files=all"); strings.TrimSpace(st) != "" {
			t.Errorf("git status:\n%s", st)
		}
	})
	t.Run("session.md", func(t *testing.T) {
		dir := cleanRepo(t)
		commitRemoval(t, dir, kernel.SessionFile)
		ctx, _ := run(t, fmt.Sprintf(`{"session_id":"s-ctx","cwd":%q}`, dir), "hook", "context")
		if strings.Contains(ctx, "session.md is missing") {
			t.Errorf("hook context reports the recorded session.md:\n%s", ctx)
		}
		if n := initIssueCount(t); n != 0 {
			t.Errorf("doctor reports %d init issue(s)", n)
		}
		if out := must(t, "init"); !strings.Contains(out, "nothing to add") {
			t.Errorf("init:\n%s", out)
		}
		if st := gittest.Git(t, dir, "status", "--porcelain", "--untracked-files=all"); strings.TrimSpace(st) != "" {
			t.Errorf("git status:\n%s", st)
		}
	})
	t.Run("session.md unrecorded", func(t *testing.T) {
		dir := cleanRepo(t)
		gittest.Git(t, dir, "rm", "-q", ".satelle/"+kernel.SessionFile)
		commitAll(t, dir, "unrecorded")
		ctx, _ := run(t, fmt.Sprintf(`{"session_id":"s-ctx","cwd":%q}`, dir), "hook", "context")
		if !strings.Contains(ctx, ".satelle/session.md is missing") || !strings.Contains(ctx, "substrate story") {
			t.Errorf("hook context for an unrecorded removal:\n%s", ctx)
		}
	})
	t.Run("never shipped", func(t *testing.T) {
		dir := cleanRepo(t)
		setInit(t, dir, categoryLine(fixtureCategory)+"removed = [\"skills/not-shipped.md\"]\n")
		out, err := run(t, "", "validate")
		if ExitCodeOf(err) != 1 || !strings.Contains(out+err.Error(), "skills/not-shipped.md") {
			t.Errorf("validate with an unshipped removal: %v\n%s", err, out)
		}
	})
}

func TestThisRepoNamesItsSubstrateCategory(t *testing.T) {
	root := filepath.Join("..", "..", ".satelle")
	cfg, err := kernel.LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Init.CategorySet() {
		t.Fatalf("this repo's %s names no category", kernel.InitFile)
	}
	if !slices.Contains(cfg.Settings.Workflows.Categories(), cfg.Init.Category) {
		t.Errorf("category %q is not one this repo's workflow rules select: %v", cfg.Init.Category, cfg.Settings.Workflows.Categories())
	}
	if plan, _ := kernel.InitPlan(root); len(plan) != 0 {
		t.Errorf("this repo lacks shipped files: %v", plan)
	}
}
