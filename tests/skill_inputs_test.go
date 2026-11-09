//go:build integration

package tests

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/workflow"
)

// wiredSkill is a judge or performer skill a workflow in this repo names.
type wiredSkill struct {
	skill workflow.Skill
	judge bool // a gate dispatches it to an agent or a panel
	// performer is set when a state's performer runs it; session when that
	// performer's binding is the interactive session (nothing is dispatched).
	performer, session bool
}

// judgeAndPerformerSkills derives the skill set from .satelle/workflows: every
// gate with judges contributes its skill, every state with an agent performer
// its skill. A skill wired later is covered without editing this test.
func judgeAndPerformerSkills(t *testing.T) []wiredSkill {
	t.Helper()
	root := filepath.Join("..", ".satelle")
	flows, err := workflow.Load(filepath.Join(root, "workflows"))
	if err != nil {
		t.Fatalf("load workflows: %v", err)
	}
	skills, err := workflow.LoadSkills(filepath.Join(root, "skills"))
	if err != nil {
		t.Fatalf("load skills: %v", err)
	}
	cat, err := agent.LoadCatalog(filepath.Join(root, "agents.toml"))
	if err != nil {
		t.Fatalf("load agents: %v", err)
	}
	set := map[string]*wiredSkill{}
	add := func(name string) *wiredSkill {
		w, ok := set[name]
		if !ok {
			s, found := skills[name]
			if !found {
				t.Fatalf("a workflow names skill %q, which has no .satelle/skills/%s.md", name, name)
			}
			w = &wiredSkill{skill: s}
			set[name] = w
		}
		return w
	}
	for _, f := range flows {
		for _, g := range f.Gates {
			if len(g.Judges()) > 0 {
				add(g.Skill).judge = true
			}
		}
		for _, st := range f.States {
			if p := st.Agent(); p != nil {
				w := add(p.Skill)
				w.performer = true
				if b, ok := cat[p.Agent]; ok && agent.IsSession(b) {
					w.session = true
				}
			}
		}
	}
	for _, want := range []string{"plan", "coder", "task-run", "satelle-story-plan-review", "satelle-binary-scope-review",
		"satelle-story-architecture-review", "satelle-code-ac-review", "satelle-outcome-review", "satelle-story-done-review",
		"satelle-story-cancel-review", "satelle-task-validate-after-review"} {
		if _, ok := set[want]; !ok {
			t.Fatalf("the derived judge and performer skills miss %s; the derivation from .satelle/workflows is broken", want)
		}
	}
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]wiredSkill, 0, len(names))
	for _, n := range names {
		out = append(out, *set[n])
	}
	return out
}

// skillLines maps each line of a skill's file to its text: the body's lines at
// their file line numbers, and the description at line 0 (where frontmatter
// that may wrap is reported).
func skillLines(t *testing.T, s workflow.Skill) map[int]string {
	t.Helper()
	raw, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatalf("read %s: %v", s.Path, err)
	}
	body := strings.Split(s.Body, "\n")
	offset := len(strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")) - len(body)
	out := map[int]string{0: s.Meta["description"]}
	for i, line := range body {
		out[offset+i+1] = line
	}
	return out
}

// v0Pattern is text a v1 judge or performer skill must not hold, with why.
type v0Pattern struct {
	re     *regexp.Regexp
	reason string
}

var v0Patterns = []v0Pattern{
	// v0 inputs: a judge's input is its prompt and the record folder it names.
	{regexp.MustCompile(`sty_`), "a v0 story id; v1 ids are sto_ and the item is in the prompt"},
	{regexp.MustCompile(`\{story, from, to\}`), "the v0 transition payload; the move is in the prompt"},
	{regexp.MustCompile(`on stdin`), "a judge or performer gets nothing on stdin; its input is the prompt"},
	{regexp.MustCompile(`(?i)payload`), "the v0 transition payload; the input is the prompt and the record folder"},
	{regexp.MustCompile(`patch_path`), "the v0 diff file; the diff is in the prompt"},
	{regexp.MustCompile("`docs`"), "the v0 payload docs list; attachments are in the record folder"},
	{regexp.MustCompile(`prior_verdicts`), "a v1 judge gets no earlier verdict"},
	{regexp.MustCompile(`review_mode`), "a v0 payload field"},
	{regexp.MustCompile(`criteria_source`), "a v0 payload field"},
	{regexp.MustCompile(`\[\[satelle-reviewer-verdict\]\]`), "the v0 re-presentation rule; a v1 judge gets no earlier verdict"},
	{regexp.MustCompile(`/stories/`), "the v0 story files; the item is in the prompt and the record folder"},
	// Removed commands: none is in the released binary's help tree.
	{regexp.MustCompile(`story docs?\b`), "satelle story doc(s) is removed; use satelle story attachment(s)"},
	{regexp.MustCompile(`story log\b`), "satelle story log is removed; use satelle ledger list <id>"},
	{regexp.MustCompile(`story messages`), "satelle story messages is removed"},
	{regexp.MustCompile(`story tidy`), "satelle story tidy is removed"},
	{regexp.MustCompile(`stop-request`), "satelle story stop-request is removed"},
	{regexp.MustCompile(`definition-edits`), "a removed command"},
	{regexp.MustCompile(`--recorded`), "satelle story diff has no --recorded"},
	{regexp.MustCompile(`ledger list --story`), "satelle ledger list takes the id as an argument"},
	{regexp.MustCompile(`--body`), "satelle <kind> attach has no --body; use --file"},
	{regexp.MustCompile(`satelle reindex`), "satelle reindex is removed"},
	{regexp.MustCompile(`workflow validate`), "satelle workflow validate is removed; use satelle validate"},
	{regexp.MustCompile(`skill validate`), "satelle skill validate is removed; use satelle validate"},
	{regexp.MustCompile(`init --all`), "satelle init has no --all"},
	{regexp.MustCompile(`init --yes`), "satelle init has no --yes"},
	// Claims no v1 code backs.
	{regexp.MustCompile(`apply_criteria`), "nothing applies a plan's criteria to the item"},
	{regexp.MustCompile(`route applies`), "nothing applies a plan's criteria to the item"},
	{regexp.MustCompile(`--criteria`), "no judge or performer writes criteria; set --criteria without --replace refuses a drop"},
	{regexp.MustCompile(`parks the story as`), "a performer's answer parks nothing"},
	{regexp.MustCompile(`before committing the transition`), "the artifact is attached when the run ends, not at a transition"},
}

// TestJudgeAndPerformerSkillsReadV1Inputs proves no judge or performer skill a
// workflow names reads v0 inputs, calls a removed command, or makes a claim no
// v1 code backs. Every hit is reported with its file and line, so one run
// lists them all.
func TestJudgeAndPerformerSkillsReadV1Inputs(t *testing.T) {
	for _, w := range judgeAndPerformerSkills(t) {
		lines := skillLines(t, w.skill)
		nums := make([]int, 0, len(lines))
		for n := range lines {
			nums = append(nums, n)
		}
		sort.Ints(nums)
		for _, n := range nums {
			for _, p := range v0Patterns {
				if p.re.MatchString(lines[n]) {
					t.Errorf(".satelle/skills/%s.md:%d: %s (%s)", w.skill.Name, n, p.re, p.reason)
				}
			}
		}
	}
}

var sessionAttachment = regexp.MustCompile(`satelle (story|execution|task) attachment`)

// TestJudgeAndPerformerSkillsNameTheRecord proves every judge and dispatched
// performer reads attachments from the record folder its prompt names, every
// session performer fetches them with satelle <kind> attachment(s), and the
// skills that judge acceptance criteria take them from the plan's section.
func TestJudgeAndPerformerSkillsNameTheRecord(t *testing.T) {
	need := map[string][]string{
		"satelle-story-plan-review":          {"## Acceptance criteria"},
		"satelle-code-ac-review":             {"## Acceptance criteria"},
		"satelle-outcome-review":             {"## Acceptance criteria"},
		"satelle-story-done-review":          {"## Acceptance criteria"},
		"plan":                               {"performer.run"},
		"task-run":                           {"--name task"},
		"satelle-task-validate-after-review": {"attachments/task"},
	}
	for _, w := range judgeAndPerformerSkills(t) {
		// Whitespace is folded, so a phrase wrapped across lines still counts.
		body := strings.Join(strings.Fields(w.skill.Meta["description"]+" "+w.skill.Body), " ")
		name := w.skill.Name
		if (w.judge || (w.performer && !w.session)) && !strings.Contains(body, "record folder") {
			t.Errorf(".satelle/skills/%s.md: a judge or dispatched performer, but it never names the record folder its prompt names", name)
		}
		if w.session && !sessionAttachment.MatchString(body) {
			t.Errorf(".satelle/skills/%s.md: a session performer, but it never fetches attachments with satelle <kind> attachment(s)", name)
		}
		for _, s := range need[name] {
			if !strings.Contains(body, s) {
				t.Errorf(".satelle/skills/%s.md: does not contain %q", name, s)
			}
		}
		delete(need, name)
	}
	for name := range need {
		t.Errorf("%s is not a judge or performer skill of .satelle/workflows", name)
	}
}
