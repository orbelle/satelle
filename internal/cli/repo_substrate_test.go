package cli

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/substrate"
	"github.com/bobmcallan/satelle/internal/workflow"
)

const (
	repoSatelleReadme = ".satelle/README.md"
	repoSkillsReadme  = ".satelle/skills/README.md"
	repoTasksReadme   = ".satelle/tasks/README.md"
)

var (
	reRepoSatellePath = regexp.MustCompile(`\.satelle/[A-Za-z0-9_/.-]*[A-Za-z0-9_/-]`)
	reRepoBareMd      = regexp.MustCompile(`(?:^|[^A-Za-z0-9_./-])([a-z0-9-]+\.md)`)
	reRepoSpan        = regexp.MustCompile("`([^`]+)`")
	reRepoBareMdSpan  = regexp.MustCompile(`^[a-z0-9-]+\.md$`)
	reRepoKeySpan     = regexp.MustCompile(`^([a-z_]+):`)
	reRepoFence       = regexp.MustCompile("``+[A-Za-z]*")
)

func repoTree() string { return filepath.Join("..", "..") }

func repoSubstrateFiles(t *testing.T) []string {
	t.Helper()
	wfs, err := filepath.Glob(filepath.Join(repoTree(), ".satelle", "workflows", "*.toml"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, p := range wfs {
		rel, err := filepath.Rel(repoTree(), p)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, filepath.ToSlash(rel))
	}
	if len(out) == 0 {
		t.Fatal("this repo has no workflow in .satelle/workflows")
	}
	sort.Strings(out)
	return append(out, repoSatelleReadme, repoSkillsReadme, repoTasksReadme)
}

func TestRepoWorkflowsAndReadmesNameWhatExists(t *testing.T) {
	files := repoSubstrateFiles(t)
	tree := substrateTree(NewRootCmd())
	found, err := substrate.Check(filepath.Join(repoTree(), ".satelle"), tree)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range found {
		if slices.Contains(files, f.File) {
			t.Errorf("%s: command, flag, topic or link: %s", f.File, f.String())
		}
	}
	readme, err := os.ReadFile(filepath.Join(repoTree(), filepath.FromSlash(repoSatelleReadme)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range substrate.CheckText(repoSatelleReadme, "md", string(readme), tree, repoLinks(t)) {
		t.Errorf("%s: command, flag, topic or link: %s", repoSatelleReadme, f.String())
	}
	exists := func(rel string) bool {
		_, err := os.Stat(filepath.Join(repoTree(), filepath.FromSlash(rel)))
		return err == nil
	}
	for _, file := range files {
		b, err := os.ReadFile(filepath.Join(repoTree(), filepath.FromSlash(file)))
		if err != nil {
			t.Fatal(err)
		}
		isWorkflow := strings.HasSuffix(file, ".toml")
		for n, line := range strings.Split(string(b), "\n") {
			for _, tok := range reRepoSatellePath.FindAllString(line, -1) {
				if !exists(tok) {
					t.Errorf("%s:%d: .satelle path: %s does not exist", file, n+1, tok)
				}
			}
			rest := reRepoSatellePath.ReplaceAllString(line, " ")
			if isWorkflow {
				for _, m := range reRepoBareMd.FindAllStringSubmatch(rest, -1) {
					if !exists(".satelle/skills/" + m[1]) {
						t.Errorf("%s:%d: .md file: %s is not in .satelle/skills", file, n+1, m[1])
					}
				}
				continue
			}
			for _, m := range reRepoSpan.FindAllStringSubmatch(reRepoFence.ReplaceAllString(rest, " "), -1) {
				span := m[1]
				if reRepoBareMdSpan.MatchString(span) && !exists(path.Join(path.Dir(file), span)) {
					t.Errorf("%s:%d: .md file: %s is not in %s", file, n+1, span, path.Dir(file))
				}
				if file != repoSkillsReadme {
					continue
				}
				if k := reRepoKeySpan.FindStringSubmatch(span); k != nil && !slices.Contains(workflow.FrontmatterKeys, k[1]) {
					t.Errorf("%s:%d: frontmatter key: %s is not a key v1 reads (%s)", file, n+1, k[1], strings.Join(workflow.FrontmatterKeys, ", "))
				}
			}
		}
	}
}

func TestRepoSkillFrontmatterIsV1(t *testing.T) {
	skills, err := workflow.LoadSkills(filepath.Join(repoTree(), ".satelle", "skills"))
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) == 0 {
		t.Fatal("this repo has no skills")
	}
	for _, s := range skills {
		for key := range s.Meta {
			if !slices.Contains(workflow.FrontmatterKeys, key) {
				t.Errorf("%s: frontmatter key %s is not a key v1 reads (%s)", s.Path, key, strings.Join(workflow.FrontmatterKeys, ", "))
			}
		}
		for _, key := range []string{workflow.KeyName, workflow.KeyType, workflow.KeyDescription} {
			if _, ok := s.Meta[key]; !ok {
				t.Errorf("%s: frontmatter has no %s", s.Path, key)
			}
		}
	}
}
