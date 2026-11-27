//go:build integration && unix

package tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gittest"
	sub "github.com/bobmcallan/satelle/internal/substrate"
)

const repoFindingsGolden = "testdata/repo_findings.golden"

const (
	findingsTreeVar  = "SATELLE_FINDINGS_TREE"
	findingsBinVar   = "SATELLE_FINDINGS_BIN"
	findingsWriteVar = "SATELLE_FINDINGS_WRITE"
)

const findingsCaptureCommand = findingsTreeVar + "=<base worktree> " + findingsBinVar + "=<binary built from it> " + findingsWriteVar + "=1 go test -tags integration -run '^TestRepoFindingsMatchTheRecordedBase$' ./tests/"

var machineChecks = []string{"versions", "upgrade", "sync", "seats"}

var contextBytes = regexp.MustCompile(`session-start context is [0-9]+ bytes`)

func TestRepoFindingsMatchTheRecordedBase(t *testing.T) {
	tree := os.Getenv(findingsTreeVar)
	if tree == "" {
		tree = ".."
	}
	bin := satelleBin
	if b := os.Getenv(findingsBinVar); b != "" {
		bin = b
	}
	write := os.Getenv(findingsWriteVar) == "1"

	s := newSandbox(t)
	s.path = append([]string{filepath.Dir(bin)}, s.path...)
	copyCheckout(t, tree, s.dir)
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "this repo's tree")
	standInAgents(t, s)

	got := repoFindings(t, s, bin)
	if write {
		head := gittest.Git(t, tree, "rev-parse", "HEAD")
		body := "# base: " + head + "\n# captured by: " + findingsCaptureCommand + "\n" + strings.Join(got, "\n") + "\n"
		if err := os.WriteFile(repoFindingsGolden, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s: %d lines", repoFindingsGolden, len(got))
		return
	}
	want := readGolden(t)
	if !slices.Equal(got, want) {
		t.Fatalf("this repo's findings differ from %s:\n%s", repoFindingsGolden, lineDiff(want, got))
	}

	t.Run("dated key is live", func(t *testing.T) {
		const probe = ".satelle/skills/zz-dated-probe.md"
		s.write(probe, "---\nname: zz-dated-probe\ntype: skill\ndescription: A dated history line.\n---\n\n- 2026-11-08: released v1.0.57\n")
		if got := repoFindings(t, s, bin); !slices.Equal(got, want) {
			t.Fatalf("with this repo's volatile.toml the dated probe changed the findings:\n%s", lineDiff(want, got))
		}
		vt := filepath.Join(s.dir, ".satelle", "volatile.toml")
		b, err := os.ReadFile(vt)
		if err != nil {
			t.Fatal(err)
		}
		var kept []string
		removed := false
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "dated =") {
				removed = true
				continue
			}
			kept = append(kept, l)
		}
		if !removed {
			t.Fatal("this repo's volatile.toml has no dated key")
		}
		s.write(".satelle/volatile.toml", strings.Join(kept, "\n"))
		v, d := validateJSON(t, s, bin), doctorJSON(t, s, bin)
		if !slices.ContainsFunc(v.Volatile, func(f sub.Finding) bool { return f.File == probe }) {
			t.Errorf("dated removed: validate lists no volatile finding in %s: %+v", probe, v.Volatile)
		}
		if !slices.ContainsFunc(d.Issues, func(is doctorIssueJSON) bool {
			return is.Check == sub.VolatileField && strings.HasPrefix(is.Problem, probe+":")
		}) {
			t.Errorf("dated removed: doctor reports no volatile issue in %s: %+v", probe, d.Issues)
		}
	})
}

func TestRepoHasNoUnreferencedSkill(t *testing.T) {
	s := newSandbox(t)
	copyCheckout(t, "..", s.dir)
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "this repo's tree")

	v := validateJSON(t, s, satelleBin)
	if !v.Valid || v.code != 0 || len(v.SkillWarnings) != 0 {
		t.Fatalf("validate on this repo: valid %v, exit %d, skill_warnings %+v, problems %v, substrate %+v", v.Valid, v.code, v.SkillWarnings, v.Problems, v.Substrate)
	}

	s.write(".satelle/skills/zz-orphan-probe.md", "---\nname: zz-orphan-probe\ntype: skill\ndescription: A skill nothing names.\n---\n\nbody\n")
	v = validateJSON(t, s, satelleBin)
	if !v.Valid || v.code != 0 || len(v.SkillWarnings) != 1 || v.SkillWarnings[0].Skill != "zz-orphan-probe" {
		t.Fatalf("validate with an unnamed probe skill: valid %v, exit %d, skill_warnings %+v", v.Valid, v.code, v.SkillWarnings)
	}

	for _, name := range []string{"land", "satelle-lessons", "satelle-design-review", "satelle-integration-review", "satelle-story-release-review"} {
		p := filepath.Join("..", ".satelle", "skills", name+".md")
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s exists (%v), want it deleted", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join("..", ".satelle", "skills", "README.md")); err != nil {
		t.Errorf("the skills README: %v", err)
	}
}

func copyCheckout(t *testing.T, tree, dst string) {
	t.Helper()
	out, err := gittest.Command(t, tree, "ls-files", "-z", "--cached", "--others", "--exclude-standard").Output()
	if err != nil {
		t.Fatalf("git ls-files in %s: %v", tree, err)
	}
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel == "" {
			continue
		}
		src := filepath.Join(tree, filepath.FromSlash(rel))
		fi, err := os.Lstat(src)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		to := filepath.Join(dst, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			t.Fatal(err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(src)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(link, to); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if !fi.Mode().IsRegular() {
			continue
		}
		b, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, to, string(b), fi.Mode().Perm())
	}
}

type validateReport struct {
	Valid         bool          `json:"valid"`
	Problems      []string      `json:"problems"`
	Substrate     []sub.Finding `json:"substrate"`
	Volatile      []sub.Finding `json:"volatile"`
	SkillWarnings []struct {
		Skill string `json:"skill"`
		File  string `json:"file"`
	} `json:"skill_warnings"`
	code int
}

type doctorIssueJSON struct {
	Check   string `json:"check"`
	Problem string `json:"problem"`
	Fix     string `json:"fix"`
	Resolve string `json:"resolve"`
}

type doctorReportJSON struct {
	Issues    []doctorIssueJSON `json:"issues"`
	Unchecked []string          `json:"unchecked"`
	Notes     []string          `json:"notes"`
}

func validateJSON(t *testing.T, s *sandbox, bin string) validateReport {
	t.Helper()
	r := s.exec(bin, "", "validate", "--json")
	var v validateReport
	if err := json.Unmarshal([]byte(r.out), &v); err != nil {
		t.Fatalf("validate --json: exit %d: %v\n%s%s", r.code, err, r.out, r.err)
	}
	v.code = r.code
	return v
}

func doctorJSON(t *testing.T, s *sandbox, bin string, args ...string) doctorReportJSON {
	t.Helper()
	r := s.exec(bin, "", append([]string{"doctor", "--json"}, args...)...)
	var d doctorReportJSON
	if err := json.Unmarshal([]byte(r.out), &d); err != nil {
		t.Fatalf("doctor --json: exit %d: %v\n%s%s", r.code, err, r.out, r.err)
	}
	return d
}

func repoFindings(t *testing.T, s *sandbox, bin string) []string {
	t.Helper()
	paths := strings.NewReplacer(s.dir, "<repo>", s.home, "<home>", filepath.Dir(bin), "<bin>").Replace
	norm := func(l string) string {
		return contextBytes.ReplaceAllString(paths(l), "session-start context is <n> bytes")
	}
	group := func(prefix string, lines []string) []string {
		out := make([]string, 0, len(lines))
		for _, l := range lines {
			out = append(out, norm(prefix+l))
		}
		slices.Sort(out)
		return out
	}

	v := validateJSON(t, s, bin)
	out := []string{fmt.Sprintf("validate exit %d", v.code), fmt.Sprintf("validate valid %t", v.Valid)}
	out = append(out, group("validate problem ", v.Problems)...)
	var sub, vol []string
	for _, f := range v.Substrate {
		sub = append(sub, f.String())
	}
	for _, f := range v.Volatile {
		vol = append(vol, f.String())
	}
	out = append(out, group("validate substrate ", sub)...)
	out = append(out, group("validate volatile ", vol)...)

	d := doctorJSON(t, s, bin, "--no-probe")
	if len(d.Unchecked) != 0 {
		t.Fatalf("doctor could not finish checks %v; the comparison needs every check", d.Unchecked)
	}
	var issues, notes []string
	for _, is := range d.Issues {
		if slices.Contains(machineChecks, is.Check) {
			continue
		}
		issues = append(issues, fmt.Sprintf("%s | %s | fix: %s | resolve: %s", is.Check, is.Problem, is.Fix, is.Resolve))
	}
	for _, n := range d.Notes {
		if slices.ContainsFunc(machineChecks, func(c string) bool { return strings.HasPrefix(n, c+":") }) {
			continue
		}
		notes = append(notes, n)
	}
	out = append(out, group("doctor issue ", issues)...)
	out = append(out, group("doctor note ", notes)...)
	return out
}

var agentCommand = regexp.MustCompile(`(?m)^command\s*=\s*"([^"]+)"`)

func standInAgents(t *testing.T, s *sandbox) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(s.dir, ".satelle", "agents.toml"))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.home, "agents")
	for _, m := range agentCommand.FindAllStringSubmatch(string(b), -1) {
		f := strings.Fields(m[1])
		if len(f) == 0 || f[0] == "session" {
			continue
		}
		writeFile(t, filepath.Join(dir, f[0]), "#!/bin/sh\necho \"$0 is a stand-in agent that the findings run never starts\" >&2\nexit 1\n", 0o755)
	}
	s.path = append([]string{dir}, s.path...)
}

func readGolden(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(repoFindingsGolden)
	if err != nil {
		t.Fatalf("%v (capture it: %s)", err, findingsCaptureCommand)
	}
	var out []string
	for _, l := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		if strings.HasPrefix(l, "#") {
			continue
		}
		out = append(out, l)
	}
	return out
}

func lineDiff(want, got []string) string {
	var b bytes.Buffer
	for _, l := range want {
		if !slices.Contains(got, l) {
			fmt.Fprintf(&b, "- %s\n", l)
		}
	}
	for _, l := range got {
		if !slices.Contains(want, l) {
			fmt.Fprintf(&b, "+ %s\n", l)
		}
	}
	if b.Len() == 0 {
		fmt.Fprintf(&b, "(same lines, different order or count: want %d, got %d)\n", len(want), len(got))
	}
	return b.String()
}
