package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bobmcallan/satelle/internal/buildinfo"
	"github.com/bobmcallan/satelle/internal/condense"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/retrieve"
	"github.com/bobmcallan/satelle/internal/store"
)

// The sty_df21b2db fit tests. The session set is built from the embedded
// defaults the binary ships plus the repo-authored resident principles at the
// sizes `satelle validate` measured for them, with a seat held and the
// version-drift advisory present, and is delivered through the real hook.

// residentSpec is one repo-authored session principle and its measured size.
type residentSpec struct {
	name string
	size int
}

// measuredResident is the repo-authored session set the fixture holds: this
// repo's own resident principle, at the body size measured on this checkout. The
// embedded session principles (agent-goals, edits-require-a-story,
// no-drifting-docs, cross-repo-containment) come from the binary.
var measuredResident = []residentSpec{
	{"satelle-discovery-criteria", 1810},
}

const measuredConstitution = 540

// fixtureProse is n bytes of rule-like prose: paragraphs whose second and third
// sentences hold a negation (the lead rung keeps them) and whose first and last
// are plain.
func fixtureProse(tag string, n int) string {
	var ps []string
	total := 0
	for i := 0; total < n; i++ {
		p := fmt.Sprintf("%s covers part %d of the practice in plain words. Never skip the %s check for part %d. "+
			"Do not reorder the %s steps within part %d. A closing sentence adds colour a reader can skip.",
			tag, i, tag, i, tag, i)
		ps = append(ps, p)
		total += len(p) + 2
	}
	text := strings.Join(ps, "\n\n")
	cut := strings.LastIndex(text[:n], " ")
	return strings.TrimSpace(text[:cut]) + "."
}

func principleFile(name, body string) string {
	return "---\nname: " + name + "\ntype: principle\ndescription: fixture " + name +
		"\ntags: [type:principle, principles:session]\n---\n\n" + body + "\n"
}

// residentRepo builds the held-seat repo with the measured resident set, and
// returns the path of each authored file it wrote.
func residentRepo(t *testing.T, pad func(spec residentSpec, body string) string) (repo string, files map[string]string) {
	t.Helper()
	repo, _ = liveSeatRepo(t)
	files = map[string]string{}
	write := func(rel, body string) string {
		p := filepath.Join(repo, ".satelle", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	files["constitution"] = write("constitution.md", "---\ntype: constitution\ntitle: Project constitution\n---\n\n# fixture constitution\n\n"+fixtureProse("The constitution", measuredConstitution)+"\n")
	for _, spec := range measuredResident {
		body := fixtureProse(spec.name, spec.size)
		if pad != nil {
			body = pad(spec, body)
		}
		files[spec.name] = write("principles/"+spec.name+".md", principleFile(spec.name, body))
	}
	// Index the authored principles the way the SessionStart reindex does.
	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DocIndex.Sync(context.Background(), map[string]string{"principles": filepath.Join(repo, ".satelle", "principles")}, time.Now().UTC()); err != nil {
		_ = db.Close()
		t.Fatalf("index principles: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// A stamp behind the binary: the version-drift advisory renders.
	old := buildinfo.Version
	buildinfo.Version = "0.0.900"
	t.Cleanup(func() { buildinfo.Version = old })
	write("deployed.version", "satelle.version: 0.0.626\n")
	return repo, files
}

// deliver runs the real hook for harness the way that harness's session start
// does, and returns what it delivered with the unit and limit it is held to.
func deliver(t *testing.T, harness string, n int) (content string, size, limit int, unit, stderr string) {
	t.Helper()
	cfg := config.Config{}
	channel := cfg.SessionContextEvent(harness)
	var raw []byte
	unit, limit = "bytes", cfg.ContextLimit(harness)
	if channel != "" {
		raw = grokDualPayload(channel, channel, fmt.Sprintf("sess-%s-%d", harness, n))
		if !strings.EqualFold(channel, "SessionStart") {
			unit, limit = "characters", cfg.ToolContextLimitChars(harness)
		}
	}
	var out, errb bytes.Buffer
	if err := runHookContext(&out, &errb, harness, raw); err != nil {
		t.Fatalf("%s: hook must fail open, got %v", harness, err)
	}
	var doc struct {
		Hook struct {
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
		Cursor string `json:"additional_context"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("%s: hook emitted %q: %v", harness, out.String(), err)
	}
	content = doc.Hook.Context + doc.Cursor
	size = len(content)
	if unit == "characters" {
		size = utf8.RuneCountInString(content)
	}
	return content, size, limit, unit, errb.String()
}

// authoredHashes maps the content hash of every authored document the session set
// is built from (the files this test wrote and the principles the binary ships)
// to its exact bytes.
func authoredHashes(t *testing.T, files map[string]string) map[string]string {
	t.Helper()
	out := map[string]string{}
	add := func(text string) { out[retrieve.Hash([]byte(text))] = text }
	for _, p := range files {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		add(string(b))
	}
	for _, d := range config.EmbeddedDefaults() {
		if d.Kind == "principles" {
			add(d.Body)
		}
	}
	return out
}

// assertEveryHarnessFits is the AC1 assertion set, shared with the padding test.
func assertEveryHarnessFits(t *testing.T, files map[string]string) {
	t.Helper()
	known := authoredHashes(t, files)
	harnesses := config.EmbeddedHarness()
	if len(harnesses) < 5 {
		t.Fatalf("expected claude, grok, pi, cursor and unknown, got %v", harnesses)
	}
	for h := range harnesses {
		t.Run(h, func(t *testing.T) {
			content, size, limit, unit, stderr := deliver(t, h, len(files))
			if content == "" {
				t.Fatal("nothing was delivered")
			}
			if strings.Contains(stderr, "exceeded") {
				t.Errorf("the hook reported an overflow: %s", stderr)
			}
			if strings.Contains(content, "OMITTED FOR THE") {
				t.Errorf("a resident item was replaced by a read pointer:\n%s", content)
			}
			if size > limit {
				t.Errorf("delivered %d %s against a limit of %d", size, unit, limit)
			}
			t.Logf("%s: %d of %d %s", h, size, limit, unit)
			if h == "claude" {
				for _, ln := range strings.Split(content, "\n") {
					if strings.HasPrefix(ln, "### ") || strings.HasPrefix(ln, "[Retrieve") {
						t.Log(ln)
					}
				}
			}
			if !strings.Contains(content, "seat held by") || !strings.Contains(content, "binary 0.0.900") {
				t.Errorf("the fixture must carry the held seat and the drift advisory:\n%s", content)
			}
			// Every condensed body carries a marker that returns the authored text.
			db, err := store.Open(runtimeDBPath(t))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			for _, hash := range retrieve.FindHashes(content) {
				got, err := db.Retrieve.Get(context.Background(), hash)
				if err != nil {
					t.Fatalf("marker %s: %v", hash, err)
				}
				want, ok := known[hash]
				if !ok || string(got) != want {
					t.Errorf("marker %s does not return an authored document byte for byte", hash)
				}
			}
			if h == "claude" && !strings.Contains(content, "Retrieve more") {
				t.Error("the claude fixture is built to need the ladder, yet nothing was condensed")
			}
		})
	}
}

func TestSessionContextFitsEveryHarnessWithoutPointers(t *testing.T) {
	_, files := residentRepo(t, nil)
	assertEveryHarnessFits(t, files)
}

// neutralSentences carries none of the loaded drop phrases or protected
// patterns, so padding with them cannot change what the ladder keeps.
var neutralSentences = []string{
	"Careful readers revisit this passage whenever wording feels unfamiliar.",
	"Similar ideas appear in several places across this document set.",
	"Authors tend to explain things twice when they care about them.",
}

func hash256(b []byte) [32]byte { return sha256.Sum256(b) }

// TestAuthoredPrincipleMayGrowWithoutLosingTheFit pads one resident principle
// about 30% — extra neutral sentences inside its paragraphs and a new neutral
// paragraph — and asserts every harness still gets the full set, and that the
// authored file is not touched by assembly.
func TestAuthoredPrincipleMayGrowWithoutLosingTheFit(t *testing.T) {
	rules, err := condense.ParseRules(config.EmbeddedCondense())
	if err != nil {
		t.Fatal(err)
	}
	inline := strings.Join(neutralSentences, " ")
	para := strings.Repeat(neutralSentences[0]+" "+neutralSentences[1]+" ", 2)
	// The padding must be neutral: no drop phrase, no protected pattern.
	for _, pad := range []string{inline, para} {
		for _, p := range rules.ProtectedPatterns {
			if regexp.MustCompile(p).MatchString(pad) {
				t.Fatalf("padding matches protected pattern %q: %q", p, pad)
			}
		}
		for _, d := range rules.DropPhrases {
			if regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(d) + `\b`).MatchString(pad) {
				t.Fatalf("padding holds drop phrase %q: %q", d, pad)
			}
		}
	}
	const target = "satelle-discovery-criteria"
	grown := 0
	_, files := residentRepo(t, func(spec residentSpec, body string) string {
		if spec.name != target {
			return body
		}
		paragraphs := strings.Split(body, "\n\n")
		for i := range paragraphs[:4] {
			// inside an existing paragraph, after its second sentence
			parts := strings.SplitN(paragraphs[i], ". ", 3)
			if len(parts) == 3 {
				paragraphs[i] = parts[0] + ". " + parts[1] + ". " + neutralSentences[i%len(neutralSentences)] + " " + parts[2]
			}
		}
		out := strings.Join(paragraphs, "\n\n") + "\n\n" + para
		grown = len(out) - len(body)
		return out
	})
	if grown < 400 || grown > 700 {
		t.Fatalf("padding grew %s by %d bytes, want about 30%% of %d", target, grown, 1655)
	}

	before, err := os.ReadFile(files[target])
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(files[target])
	if err != nil {
		t.Fatal(err)
	}
	assertEveryHarnessFits(t, files)
	after, err := os.ReadFile(files[target])
	if err != nil {
		t.Fatal(err)
	}
	st2, err := os.Stat(files[target])
	if err != nil {
		t.Fatal(err)
	}
	if hash256(before) != hash256(after) || !st.ModTime().Equal(st2.ModTime()) {
		t.Error("assembling the context must leave the authored file untouched")
	}
}

// A repo overrides the rules under .satelle/config/ and the delivered context
// changes with them.
func TestCondenseRepoOverrideChangesOutput(t *testing.T) {
	repo, _ := residentRepo(t, nil)
	base, _, _, _, _ := deliver(t, "claude", 1)
	if !strings.Contains(base, "Retrieve more") {
		t.Fatalf("precondition: the default rules condense the claude set:\n%s", base)
	}
	override := filepath.Join(repo, ".satelle", filepath.FromSlash(config.CondenseOverrideRel))
	if err := os.MkdirAll(filepath.Dir(override), 0o755); err != nil {
		t.Fatal(err)
	}
	// A ladder with no condensing rung: the oversize set can only be pointered.
	if err := os.WriteFile(override, []byte("ladder = [\"full\", \"pointer\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	over, _, _, _, _ := deliver(t, "claude", 2)
	if !strings.Contains(over, "OMITTED FOR THE CLAUDE") || strings.Contains(over, "Retrieve more") {
		t.Errorf("the override must change the output:\n%s", over)
	}
}

func TestCondenseRulesFallBackWhenTheOverrideDoesNotParse(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(config.CondenseOverrideRel)), []byte("ladder = \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rules, err := condenseRules(dir)
	if err == nil || len(rules.Ladder) == 0 {
		t.Errorf("a broken override must cost the override, not the context: rules=%v err=%v", rules.Ladder, err)
	}
}

// The SessionStart render and the report's driver rows are one function: the
// fit lives in condense, and the hook renders what Fit returns.
func TestHookRenderUsesFit(t *testing.T) {
	rules, err := condenseRules("")
	if err != nil {
		t.Fatal(err)
	}
	docs := selectAlwaysDocs([]docindex.Doc{
		{Kind: "principles", Name: "p-a", Body: principleFile("p-a", fixtureProse("p-a", 3000))},
		{Kind: "principles", Name: "p-b", Body: principleFile("p-b", fixtureProse("p-b", 3000))},
	})
	got, omitted := renderAlwaysContent("constitution body.", docs, alwaysRender{Budget: 5600, Harness: "claude", Rules: rules})
	if len(got) > 5600 || len(omitted) != 0 {
		t.Fatalf("the rendered set must fit by condensing: %d bytes, omitted %v", len(got), omitted)
	}
	items := []condense.Item{{Name: "constitution", Heading: "# Project constitution", Body: "constitution body.",
		Pointer: "- `constitution` — the project constitution — read: the file `.satelle/constitution.md`"}}
	for _, d := range docs {
		items = append(items, condense.Item{
			Name: d.Name, Group: "# Always-resident principles (satelle)", Heading: "### " + d.Name,
			Body: strings.TrimSpace(stripFrontmatter(d.Body)), Original: d.Body, Pointer: principleIndexLine(d),
		})
	}
	want, _, err := condense.Fit(items, condense.Frame{
		PointerHeader: "OMITTED FOR THE CLAUDE CONTEXT LIMIT (5600 bytes): the following are not inlined — read each now with the command shown, before doing any work.",
		Trailer:       alwaysIndexInstruction,
	}, 5600, func(s string) int { return len(s) }, rules, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("renderAlwaysContent must only render condense.Fit's result:\n got %q\nwant %q", got, want)
	}
}

// sty_58a7bdfe AC5: `review score --seat-budget` is removed with the per-seat
// budget it fitted the judging seat to.
func TestReviewScoreHasNoSeatBudgetFlag(t *testing.T) {
	score, _, err := NewRootCmd().Find([]string{"review", "score"})
	if err != nil || score.Name() != "score" {
		t.Fatalf("review score: %v", err)
	}
	if score.Flags().Lookup("seat-budget") != nil {
		t.Error("review score still defines --seat-budget")
	}
}

// No rung is chosen outside internal/condense: neither the hook nor the seat
// payload builder names a rung other than to read which items were pointered.
func TestNoLadderLogicOutsideCondense(t *testing.T) {
	files := []string{"cmd_hook.go", "../agentstep/footprint.go", "../agentstep/invoke.go", "../agentstep/engine.go"}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"condense.RungFull", "condense.RungFiller", "condense.RungLead", ".Ladder", "dropFiller", "formsFor"} {
			if strings.Contains(string(src), banned) {
				t.Errorf("%s names %q: the ladder belongs to internal/condense", f, banned)
			}
		}
	}
}
