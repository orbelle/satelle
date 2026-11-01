package agentstep

import (
	"context"
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
var leftoverSkills = []struct {
	rel     string
	phrases []string
}{
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

// TestEmbeddedReviewerSkillsQuotationComparison (AC3) walks the embedded skill
// root, which every checkout has. A skill is in the set when its frontmatter
// tags include type:reviewer, do not include type:functional-check, and the
// body has no ```check fence. The verdict rules live once, in the shared
// principle: no selected skill body restates them, yet the judging prompt each
// is composed into carries all of them.
func TestEmbeddedReviewerSkillsQuotationComparison(t *testing.T) {
	root := moduleRoot(t)
	selected := quotationReviewerPaths(t, filepath.Join(root, "internal", "config", "substrate", "skills"))
	assertOutsideReviewerSet(t, selected)

	verdict := ReviewerVerdictDoc(context.Background(), embeddedPrincipleDocs())
	if verdict == "" {
		t.Fatalf("embedded principle %s must resolve", config.ReviewerVerdictPrinciple)
	}
	assertQuotationRules(t, "principle "+config.ReviewerVerdictPrinciple, verdict)
	for _, path := range selected {
		assertDelegatesVerdictRules(t, path, verdict)
	}
	for _, name := range []string{"satelle-story-scope-review.md", "satelle-story-done-review.md"} {
		var hits []string
		for _, path := range selected {
			if filepath.Base(path) == name {
				hits = append(hits, path)
			}
		}
		if len(hits) != 1 {
			t.Errorf("%s must be selected once under the embedded skills root, got %v", name, hits)
		}
	}
	assertNoLeftoverPhrases(t, root, embeddedSkillsRel)
}

// TestOverrideReviewerSkillsQuotationComparison (AC7) holds the repo's live
// override skills to the same single-source rule as the embedded ones: the
// verdict rules live in the shared principle and no override restates them.
// .satelle/ is gitignored operator substrate, so a clean checkout and CI do not
// have it and the test skips.
func TestOverrideReviewerSkillsQuotationComparison(t *testing.T) {
	root := moduleRoot(t)
	dir := filepath.Join(root, ".satelle", "skills")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Skip(".satelle/skills is gitignored operator substrate; not present in this checkout")
	} else if err != nil {
		t.Fatal(err)
	}
	selected := quotationReviewerPaths(t, dir)
	assertOutsideReviewerSet(t, selected)
	for _, name := range []string{"satelle-story-deploy-review.md", "satelle-story-integration-review.md"} {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "quotation comparison governs") {
			t.Errorf("functional check %s must be unchanged", name)
		}
		for _, path := range selected {
			if filepath.Base(path) == name {
				t.Errorf("functional check %s must not be selected", path)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "satelle-integration-review.md")); err != nil {
		t.Fatal(err)
	}
	var sawIntegration bool
	for _, path := range selected {
		if filepath.Base(path) == "satelle-integration-review.md" {
			sawIntegration = true
		}
	}
	if !sawIntegration {
		t.Error("satelle-integration-review is type:reviewer only and must delegate to " + config.ReviewerVerdictPrinciple)
	}
	for _, name := range []string{"satelle-story-scope-review.md", "satelle-story-done-review.md"} {
		for _, path := range selected {
			if filepath.Base(path) == name {
				t.Errorf("%s must be edited only under the embedded skills root, got %s", name, path)
			}
		}
	}
	verdict := ReviewerVerdictDoc(context.Background(), embeddedPrincipleDocs())
	if verdict == "" {
		t.Fatalf("embedded principle %s must resolve", config.ReviewerVerdictPrinciple)
	}
	for _, path := range selected {
		assertDelegatesVerdictRules(t, path, verdict)
	}
	assertNoLeftoverPhrases(t, root, overrideSkillsRel)
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

// TestAssertDelegatesVerdictRules proves the shared single-source check fails a
// skill that restates the verdict rules and passes one that delegates them.
func TestAssertDelegatesVerdictRules(t *testing.T) {
	verdict := ReviewerVerdictDoc(context.Background(), embeddedPrincipleDocs())
	if verdict == "" {
		t.Fatalf("embedded principle %s must resolve", config.ReviewerVerdictPrinciple)
	}
	skill := func(extra string) string {
		return "---\nname: tmp-review\ntags: [type:reviewer]\n" +
			"description: A temporary reviewer. A re-presented edge follows [[" + config.ReviewerVerdictPrinciple + "]]\n---\n\n" +
			"# Review\n\nReply `{\"verdict\": \"accept\", \"reviewed\": \"<quoted words>\"}`.\n" + extra
	}
	run := func(body string) []string {
		path := filepath.Join(t.TempDir(), "tmp-review.md")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		rec := &recordingTB{TB: t}
		assertDelegatesVerdictRules(rec, path, verdict)
		return rec.errs
	}
	t.Run("restating override is flagged", func(t *testing.T) {
		if errs := run(skill("\nQuotation comparison governs a re-presented edge.\n")); len(errs) == 0 {
			t.Error("a skill restating the verdict rules must be reported")
		}
	})
	t.Run("delegating override passes", func(t *testing.T) {
		if errs := run(skill("")); len(errs) != 0 {
			t.Errorf("a delegating skill must pass, got %v", errs)
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
func assertNoLeftoverPhrases(t *testing.T, root, prefix string) {
	t.Helper()
	for _, item := range leftoverSkills {
		if !strings.HasPrefix(item.rel, prefix) {
			continue
		}
		body, err := os.ReadFile(filepath.Join(root, item.rel))
		if err != nil {
			t.Fatal(err)
		}
		norm := strings.ToLower(normaliseLaterRound(string(body)))
		for _, phrase := range item.phrases {
			if strings.Contains(norm, phrase) {
				t.Errorf("%s still tells a later round to verify prior findings: %q", item.rel, phrase)
			}
		}
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
