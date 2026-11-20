package kernel

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

func TestInitShipsAutoModeGuidance(t *testing.T) {
	root, c := initVolatile(t)
	shipped, err := Shipped(AutoModeFile)
	if err != nil || len(shipped) == 0 {
		t.Fatalf("Shipped(%s): %v", AutoModeFile, err)
	}
	written, err := os.ReadFile(filepath.Join(root, AutoModeFile))
	if err != nil {
		t.Fatalf("init wrote no %s: %v", AutoModeFile, err)
	}
	if string(written) != string(shipped) {
		t.Fatalf("init %s differs from shipped template:\ngot:\n%s\nwant:\n%s", AutoModeFile, written, shipped)
	}
	if errs := Validate(root); len(errs) != 0 {
		t.Fatalf("fresh init does not validate: %v", errs)
	}
	if got, err := c.VolatileFindings(); err != nil || len(got) != 0 {
		t.Fatalf("fresh init volatile findings: %v (err %v)", got, err)
	}
	if err := os.Remove(filepath.Join(root, AutoModeFile)); err != nil {
		t.Fatal(err)
	}
	plan, err := InitPlan(root)
	if err != nil || !slices.Contains(plan, AutoModeFile) {
		t.Fatalf("InitPlan after removing %s = %v (%v), want %s listed", AutoModeFile, plan, err, AutoModeFile)
	}
}

func absentWorkflowStepHits(text string, forbidden []string) []string {
	var hits []string
	seen := map[string]bool{}
	for _, term := range forbidden {
		candidates := []string{term}
		if strings.ContainsAny(term, "_-") {
			spaced := strings.NewReplacer("_", " ", "-", " ").Replace(term)
			if spaced != term {
				candidates = append(candidates, spaced)
			}
		}
		for _, c := range candidates {
			re := regexp.MustCompile(`(?i)(^|[^a-z0-9_-])` + regexp.QuoteMeta(c) + `([^a-z0-9_-]|$)`)
			if re.MatchString(text) {
				if !seen[term] {
					seen[term] = true
					hits = append(hits, term)
				}
				break
			}
		}
	}
	sort.Strings(hits)
	return hits
}

func TestInitAutoModeNamesNoStepAbsentFromTemplateWorkflows(t *testing.T) {
	freshRoot := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(freshRoot); err != nil {
		t.Fatal(err)
	}
	tplCfg, err := LoadConfig(freshRoot)
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Join("..", "..", ".satelle")
	repoCfg, err := LoadConfig(repoRoot)
	if err != nil {
		t.Fatal(err)
	}

	if len(tplCfg.Workflows) != 1 || tplCfg.Workflows["default"] == nil {
		t.Fatalf("template workflows = %v, want only default", sortedKeys(tplCfg.Workflows))
	}
	def := tplCfg.Workflows["default"]
	if got := sortedKeys(def.States); !slices.Equal(got, []string{"backlog", "done"}) {
		t.Fatalf("template default workflow states = %v, want [backlog done]", got)
	}

	tplStates := map[string]bool{}
	tplGates := map[string]bool{}
	tplPerformers := map[string]bool{}
	tplQueues := map[string]bool{}
	tplRemotes := map[string]bool{}
	for _, w := range tplCfg.Workflows {
		for s, st := range w.States {
			tplStates[s] = true
			if p := st.Agent(); p != nil {
				tplPerformers[p.Agent] = true
				tplPerformers[p.Skill] = true
			}
		}
		for g := range w.Gates {
			tplGates[g] = true
		}
	}
	for q := range tplCfg.Queues {
		tplQueues[q] = true
	}
	if entries, err := os.ReadDir(filepath.Join(freshRoot, RemotesDir)); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				tplRemotes[e.Name()] = true
			}
		}
	}
	if len(tplGates) != 0 || len(tplPerformers) != 0 || len(tplQueues) != 0 || len(tplRemotes) != 0 {
		t.Fatalf("template workflow unexpectedly has gates=%d performers=%d queues=%d remotes=%d",
			len(tplGates), len(tplPerformers), len(tplQueues), len(tplRemotes))
	}

	forbiddenSet := map[string]bool{}
	addForbidden := func(terms ...string) {
		for _, term := range terms {
			if term = strings.TrimSpace(term); term != "" {
				forbiddenSet[term] = true
			}
		}
	}
	for _, w := range repoCfg.Workflows {
		for s, st := range w.States {
			if !tplStates[s] {
				addForbidden(s)
			}
			if p := st.Agent(); p != nil {
				if !tplPerformers[p.Agent] {
					addForbidden(p.Agent)
				}
				if !tplPerformers[p.Skill] {
					addForbidden(p.Skill)
				}
			}
		}
		for g := range w.Gates {
			if !tplGates[g] {
				addForbidden(g)
			}
		}
	}
	for q := range repoCfg.Queues {
		if !tplQueues[q] {
			addForbidden(q)
		}
	}
	if entries, err := os.ReadDir(filepath.Join(repoRoot, RemotesDir)); err != nil {
		t.Fatalf("read repo remotes: %v", err)
	} else {
		for _, e := range entries {
			if e.IsDir() && !tplRemotes[e.Name()] {
				addForbidden(e.Name())
			}
		}
	}
	if len(tplGates) == 0 {
		addForbidden("gate", "gates", "review", "reviewer", "reviewers")
	}
	if len(tplPerformers) == 0 {
		addForbidden("performer", "performers", "dispatch", "dispatched", "seat", "seats", "seating")
	}
	if len(tplQueues) == 0 {
		addForbidden("queue", "queues", "enqueue")
	}
	if len(tplRemotes) == 0 {
		addForbidden("remote", "remotes", "sprite", "sprites", "docker")
	}
	addForbidden("pull request", "pr", "github", "gh", "merge", "push", "commit", "rebase", "release", "deploy", "publish", "ci/cd", "pipeline")

	forbidden := sortedKeys(forbiddenSet)
	if len(forbidden) == 0 {
		t.Fatal("forbidden term list is empty")
	}

	tplBytes, err := os.ReadFile(filepath.Join(freshRoot, AutoModeFile))
	if err != nil {
		t.Fatal(err)
	}
	tplText := string(tplBytes)
	nonEmpty := 0
	for i, l := range strings.Split(tplText, "\n") {
		trim := strings.TrimSpace(l)
		if trim == "" {
			continue
		}
		nonEmpty++
		if strings.HasPrefix(trim, "#") {
			t.Errorf("template %s line %d is a heading: %q", AutoModeFile, i+1, l)
		}
		if trim == "---" {
			t.Errorf("template %s line %d is YAML frontmatter delimiter", AutoModeFile, i+1)
		}
		if trim == "$defaults" {
			t.Errorf("template %s line %d must not include $defaults", AutoModeFile, i+1)
		}
	}
	if nonEmpty == 0 {
		t.Fatalf("template %s has no non-empty lines", AutoModeFile)
	}
	if hits := absentWorkflowStepHits(tplText, forbidden); len(hits) != 0 {
		t.Errorf("template %s names workflow steps absent from template workflows: %v", AutoModeFile, hits)
	}

	controls := []struct {
		name  string
		text  string
		wants []string
	}{
		{
			name:  "absent states",
			text:  "Moving a story from plan to in_progress is routine.",
			wants: []string{"plan", "in_progress"},
		},
		{
			name:  "absent gates",
			text:  "Running the plan-review gate before code-ac is routine.",
			wants: []string{"plan-review", "gate", "code-ac"},
		},
		{
			name:  "absent queue and remote",
			text:  "Firing the release queue and integration remote is routine.",
			wants: []string{"release", "queue", "integration", "remote"},
		},
		{
			name:  "forge step",
			text:  "Opening a pull request via gh is routine.",
			wants: []string{"pull request", "gh"},
		},
	}
	for _, tc := range controls {
		hits := absentWorkflowStepHits(tc.text, forbidden)
		for _, want := range tc.wants {
			if !slices.Contains(hits, want) {
				t.Errorf("control %q: hits = %v, want %q reported", tc.name, hits, want)
			}
		}
	}

	repoAutoMode, err := os.ReadFile(filepath.Join(repoRoot, AutoModeFile))
	if err != nil {
		t.Fatalf("read repo %s: %v", AutoModeFile, err)
	}
	if hits := absentWorkflowStepHits(string(repoAutoMode), forbidden); len(hits) == 0 {
		t.Errorf("control: repo %s drew 0 hits against template workflows, expected > 0", AutoModeFile)
	}
}

func TestAutoModeTemplateIsNotAGoLiteral(t *testing.T) {
	b, err := Shipped(AutoModeFile)
	if err != nil || len(b) == 0 {
		t.Fatalf("Shipped(%s): %v", AutoModeFile, err)
	}
	checked, _ := principleSentences(string(b))
	if len(checked) == 0 {
		t.Fatalf("template %s has no checked sentence", AutoModeFile)
	}
	hits, read := goFilesHolding(t, filepath.Join("..", ".."), checked)
	if read == 0 {
		t.Fatal("the scan read no Go file")
	}
	for p, ss := range hits {
		for _, s := range ss {
			t.Errorf("%s holds the auto-mode template sentence %q", p, s)
		}
	}
}
