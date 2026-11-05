package agentstep

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/structure"
)

const (
	embeddedSkillsRel = "internal/config/substrate/skills/"
	overrideSkillsRel = ".satelle/skills/"
)

// A reviewer skill must not tell a later round to verify prior findings: the
// re-presented-edge rules live in the shared principle. The plan descriptions
// and the architecture heading sit outside any one section, so
// the whole normalised body is checked, or "verify prior findings" and "then
// verify" stay in the skill and the test still passes.
type leftoverSkill struct {
	rel     string
	phrases []string
}

var leftoverSkills = []leftoverSkill{
	{"internal/config/substrate/skills/satelle-story-plan-review.md", []string{
		"later rounds verify prior findings",
		"verify prior findings",
		"number your findings so the next round can answer each one",
	}},
	{".satelle/skills/satelle-story-architecture-review.md", []string{
		"every blocker first, then verify",
		"then verify",
		"verify prior findings",
		"verify each prior finding",
		"acceptance criterion that changed (open definition_edits.path)",
	}},
	{".satelle/skills/satelle-story-intent-review.md", []string{
		"changed definition field",
		"verify every prior finding",
	}},
}

// sharedVerdictPhrases are the passages of the shared reviewer-verdict
// principle: the re-presented-edge rules and the `reviewed`-required paragraph.
// An embedded reviewer skill states neither (it owns only its verdict JSON
// shape); the principle holds them and every judging dispatch is handed them.
var sharedVerdictPhrases = []string{"quotation comparison governs", "is required on every judgment"}

// embeddedPrincipleDocs is the embedded principles as a DocGetter, the way a
// repo with no override resolves them.
func embeddedPrincipleDocs() fakeDocs {
	var ps []docindex.Doc
	for _, d := range config.EmbeddedDefaults() {
		if d.Kind == "principles" {
			ps = append(ps, docindex.Doc{Kind: "principles", Name: d.Name, Body: d.Body, Embedded: true})
		}
	}
	return fakeDocs{extraPrinciples: ps}
}

// recordingTB counts the errors a check reports instead of failing the test, so
// a check can be shown to flag a bad skill.
type recordingTB struct {
	testing.TB
	errs []string
}

func (r *recordingTB) Errorf(format string, args ...any) {
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}

// assertFunctionalOverrides holds each named functional-check override under
// dir to the single-source rule: it restates no quotation rule and is not in
// the reviewer set. .satelle/skills is operator substrate, so a named override
// absent from it is logged and skipped; only a present one is judged.
func assertFunctionalOverrides(tb testing.TB, dir string, names, selected []string) {
	tb.Helper()
	for _, name := range names {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if os.IsNotExist(err) {
			tb.Logf("functional check %s absent from operator substrate; skipped", name)
			continue
		}
		if err != nil {
			tb.Errorf("functional check %s: %v", name, err)
			continue
		}
		if strings.Contains(string(body), "quotation comparison governs") {
			tb.Errorf("functional check %s must be unchanged", name)
		}
		for _, path := range selected {
			if filepath.Base(path) == name {
				tb.Errorf("functional check %s must not be selected", path)
			}
		}
	}
}

// TestAssertFunctionalOverridesFlagsRestatement proves an absent functional
// check is skipped while a present one that restates the quotation rule is
// still reported.
func TestAssertFunctionalOverridesFlagsRestatement(t *testing.T) {
	names := []string{"satelle-story-deploy-review.md", "satelle-story-integration-review.md"}
	run := func(deploy string) []string {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, names[0]), []byte(deploy), 0o600); err != nil {
			t.Fatal(err)
		}
		rec := &recordingTB{TB: t}
		assertFunctionalOverrides(rec, dir, names, nil)
		return rec.errs
	}
	t.Run("present restating override is flagged", func(t *testing.T) {
		if errs := run("# Deploy\n\nThe quotation comparison governs a re-presented edge.\n"); len(errs) != 1 {
			t.Errorf("want exactly one error for the restating deploy-review, got %v", errs)
		}
	})
	t.Run("present clean override passes", func(t *testing.T) {
		if errs := run("# Deploy\n\nRun the deploy check.\n"); len(errs) != 0 {
			t.Errorf("a clean deploy-review with integration-review absent must pass, got %v", errs)
		}
	})
}

// assertDelegatesVerdictRules holds one reviewer skill to the single-source
// rule: the verdict rules live in the shared principle, so the skill restates
// none of them, points at the principle from its description, carries no
// re-presented-edge or later-rounds section, owns its verdict JSON including
// the reviewed field, and is composed into a judging prompt that carries the
// rules exactly once.
func assertDelegatesVerdictRules(t testing.TB, path, verdict string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, phrase := range sharedVerdictPhrases {
		if strings.Contains(strings.ToLower(body), phrase) {
			t.Errorf("%s restates %q, which belongs to %s alone", path, phrase, config.ReviewerVerdictPrinciple)
		}
	}
	if !strings.Contains(body, `"reviewed":`) {
		t.Errorf("%s must still own its verdict JSON shape, including the reviewed field", path)
	}
	// The pointer lives in the frontmatter description; a body section for it
	// would be the same run of words in every skill, which doctor reports.
	if desc := descriptionOf(body); !strings.Contains(desc, "[["+config.ReviewerVerdictPrinciple+"]]") {
		t.Errorf("%s description must point at %s", path, config.ReviewerVerdictPrinciple)
	}
	for _, heading := range []string{"## re-presented edge", "later rounds"} {
		if strings.Contains(strings.ToLower(body), heading) {
			t.Errorf("%s carries a %q section; the rules live in %s", path, heading, config.ReviewerVerdictPrinciple)
		}
	}
	seat := BuildSeatPayload(SeatPayloadInput{
		Charter: reviewerCharter(), Rubric: body, Judging: true, Verdict: verdict,
	})
	assertQuotationPhrases(t, path+" (composed judging prompt)", seat)
	if n := strings.Count(seat, sharedVerdictPhrases[0]); n != 1 {
		t.Errorf("%s: the composed judging prompt carries the re-presented-edge rules %d times, want once", path, n)
	}
}

// quotationReviewerPaths returns the reviewer skills under dir.
func quotationReviewerPaths(t *testing.T, dir string) []string {
	t.Helper()
	paths, err := reviewerSkillMarkdown(dir)
	if err != nil {
		t.Fatal(err)
	}
	var selected []string
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if quotationReviewer(string(body)) {
			selected = append(selected, path)
		}
	}
	if len(selected) == 0 {
		t.Fatalf("no reviewer skills selected under %s", dir)
	}
	return selected
}

// assertQuotationPhrases asserts body states the quotation rules and its own
// verdict JSON carries the reviewed field.
func assertQuotationPhrases(t testing.TB, path, body string) {
	t.Helper()
	assertQuotationRules(t, path, body)
	if !strings.Contains(body, `"reviewed":`) {
		t.Errorf("%s verdict JSON omits the reviewed field", path)
	}
}

// assertQuotationRules asserts body states the re-presented-edge rules and the
// reviewed-required rule.
func assertQuotationRules(t testing.TB, path, body string) {
	t.Helper()
	lower := strings.ToLower(body)
	for _, phrase := range []string{
		"quotation comparison governs",
		"re-issue",
		"does not clear a standing rejection",
		"reviewed_truncated",
		"do not cite an older",
		// A judgment records the quotation. The differ branch alone
		// never stores one, so a later round has nothing to cite.
		"the first verdict",
		"sets `reviewed`",
		"re-judge the words this verdict would rest on and set `reviewed`",
		"the same `reviewed` string",
	} {
		if !strings.Contains(lower, phrase) {
			t.Errorf("%s missing %q", path, phrase)
		}
	}
}

// descriptionOf is the frontmatter description line of a skill, "" when absent.
func descriptionOf(body string) string {
	head := strings.TrimSuffix(strings.TrimSuffix(body, stripFrontmatter(body)), "\n")
	for _, line := range strings.Split(head, "\n") {
		if v, ok := strings.CutPrefix(line, "description:"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func assertOutsideReviewerSet(t *testing.T, selected []string) {
	t.Helper()
	for _, name := range []string{
		"plan.md", "commit.md", "satelle-lessons.md", "satelle-step-summary.md",
		"satelle-workflow-advisor.md", "README.md",
	} {
		for _, path := range selected {
			if filepath.Base(path) == name {
				t.Errorf("%s is outside the reviewer set", path)
			}
		}
	}
}

// assertNoLeftoverPhrases checks the leftover phrases for each skill whose rel
// path starts with prefix.
func assertNoLeftoverPhrases(tb testing.TB, root, prefix string) {
	tb.Helper()
	var items []leftoverSkill
	for _, item := range leftoverSkills {
		if strings.HasPrefix(item.rel, prefix) {
			items = append(items, item)
		}
	}
	assertNoLeftoverPhrasesIn(tb, root, items)
}

// assertNoLeftoverPhrasesIn checks each item's leftover phrases under root. A
// .satelle/ override is operator substrate and may be absent, so an absent
// file is logged and skipped; a present one is always judged.
func assertNoLeftoverPhrasesIn(tb testing.TB, root string, items []leftoverSkill) {
	tb.Helper()
	for _, item := range items {
		body, err := os.ReadFile(filepath.Join(root, item.rel))
		if os.IsNotExist(err) {
			tb.Logf("%s absent; leftover-phrase check skipped", item.rel)
			continue
		}
		if err != nil {
			tb.Errorf("%s: %v", item.rel, err)
			continue
		}
		norm := strings.ToLower(normaliseLaterRound(string(body)))
		for _, phrase := range item.phrases {
			if strings.Contains(norm, phrase) {
				tb.Errorf("%s still tells a later round to verify prior findings: %q", item.rel, phrase)
			}
		}
	}
}

// TestAssertNoLeftoverPhrasesSkipsAbsent proves an absent override is skipped,
// not fatal, while a present one carrying a leftover phrase is still reported.
func TestAssertNoLeftoverPhrasesSkipsAbsent(t *testing.T) {
	root := t.TempDir()
	skills := filepath.Join(root, ".satelle", "skills")
	if err := os.MkdirAll(skills, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skills, "a.md"), []byte("# A\n\nLater rounds verify prior findings.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	phrases := []string{"verify prior findings"}
	rec := &recordingTB{TB: t}
	assertNoLeftoverPhrasesIn(rec, root, []leftoverSkill{
		{".satelle/skills/a.md", phrases},
		{".satelle/skills/b.md", phrases},
	})
	if len(rec.errs) != 1 {
		t.Errorf("want exactly one error for the present a.md, got %v", rec.errs)
	}
}

// quotationReviewer is the tag rule: type:reviewer, not type:functional-check,
// and no ```check fence. It does not treat "has no check" as the definition.
func quotationReviewer(body string) bool {
	tags := frontmatterList(body, "tags")
	var reviewer, functional bool
	for _, tag := range tags {
		switch tag {
		case "type:reviewer":
			reviewer = true
		case "type:functional-check":
			functional = true
		}
	}
	if !reviewer || functional {
		return false
	}
	return structure.CheckFence(body) == ""
}

func reviewerSkillMarkdown(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == "README.md" || !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		out = append(out, path)
		return nil
	})
	return out, err
}

// normaliseLaterRound strips markdown emphasis and collapses whitespace.
// An underscore that sits inside an identifier is kept, so the same text can
// still be checked for the token definition_edits.
func normaliseLaterRound(s string) string {
	s = strings.ReplaceAll(s, "**", "")
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '_' {
			b.WriteByte(s[i])
			continue
		}
		prev := i > 0 && isIdentByte(s[i-1])
		next := i+1 < len(s) && isIdentByte(s[i+1])
		if prev && next {
			b.WriteByte('_')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func isIdentByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
