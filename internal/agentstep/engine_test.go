package agentstep

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/logfile"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/testutil"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/wfdot"
	"github.com/bobmcallan/satelle/internal/wfgovern"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// A lifecycle is a DERIVED ROUTE — done.toml + step.toml — and there is no DOT
// front end left to author one with (sty_d953c5d8). The fixtures below are route
// sources, but the ~50 `fakeDocs{workflow: …}` call sites pass ONE body, so
// wfDoc packs the two halves with a sentinel and fakeDocs splits them back into
// the two docs the front door reads. The packing is a test-harness convenience;
// the bytes on each side of it are ordinary route grammar.
const routeHalfSplit = "\n@@@step@@@\n"

// wfDoc packs a fixture route in the conformant workflow-doc envelope a
// Gate-path fixture needs: the gate's structure guard refuses a governing
// workflow that fails its deterministic check (sty_d0d6bb67), so any workflow a
// test drives Gate under must be a well-formed doc, exactly like the authored
// substrate. done is the category body (obligations, park, cancel); step is the
// step catalogue. Both halves are TOML with a `[meta]` header (sty_81bb0dde).
func wfDoc(done, step string) string {
	head := func(name, what string) string {
		return "[meta]\nname = \"" + name + "\"\ntype = \"workflow\"\ndescription = \"" +
			what + "\"\nscope = \"system\"\n\n"
	}
	return head("done", "test declaration of done") + done + routeHalfSplit +
		head("step", "test step catalogue") + testutil.UngatedStep(step)
}

// tomlList renders a CSV fixture field as a TOML array.
func tomlList(csv string) string {
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, `"`+p+`"`)
		}
	}
	return "[" + strings.Join(out, ", ") + "]"
}

// roleRef renders a fixture's "state @gate" shorthand as the inline table a
// route source declares a park/cancel role with.
func roleRef(spec string) string {
	state, gate, _ := strings.Cut(spec, "@")
	fields := []string{`state = "` + strings.TrimSpace(state) + `"`}
	if gate = strings.TrimSpace(gate); gate != "" {
		fields = append(fields, `gate = "`+gate+`"`)
	}
	return "{ " + strings.Join(fields, ", ") + " }"
}

// spineWF is the fixture shorthand most cases want: a wildcard lane whose steps
// run in the given order, each discharging an obligation named for it. steps are
// "status|agent|skill|reviewers(csv)|reviewer_agent|parallel|extra" (extra is one
// raw step line, e.g. "bundle = true"); a trailing step is
// terminal. An omitted parallel leaves the route's own default (concurrent above
// one reviewer); "0" asks for sequential first-reject.
// park and cancel are "state @gate" (empty to omit).
//
// A step TABLE is keyed by the obligation it discharges, and its `status` is the
// stage name — so the spec's first field names the status and the key is derived
// from it. That is the file's own model, not a fixture convention.
func spineWF(park, cancel string, gates string, steps ...string) string {
	var cat strings.Builder
	obligations := []string{`"raised"`}
	cat.WriteString("[raised]\nstatus = \"backlog\"\nstart = true\n\n")
	prev := "raised"
	for i, spec := range steps {
		f := strings.Split(spec, "|")
		for len(f) < 7 {
			f = append(f, "")
		}
		status, agent, skill, reviewers, ragent, par, extra := f[0], f[1], f[2], f[3], f[4], f[5], f[6]
		ob := "ob-" + status
		obligations = append(obligations, `"`+ob+`"`)
		cat.WriteString("[" + ob + "]\nstatus = \"" + status + "\"\n")
		if agent != "" {
			cat.WriteString("agent = \"" + agent + "\"\n")
		}
		if skill != "" {
			cat.WriteString("skills = " + tomlList(skill) + "\n")
		}
		if reviewers != "" {
			cat.WriteString("reviewers = " + tomlList(reviewers) + "\n")
		}
		if ragent != "" {
			cat.WriteString("reviewer_agent = \"" + ragent + "\"\n")
		}
		if par != "" {
			cat.WriteString("parallel = " + par + "\n")
		}
		if extra != "" { // a raw step line, e.g. "bundle = true" (sty_23e10d92)
			cat.WriteString(extra + "\n")
		}
		if i == len(steps)-1 {
			cat.WriteString("terminal = true\n")
		}
		cat.WriteString("requires = [\"" + prev + "\"]\n\n")
		prev = ob
	}
	// A repo done.toml overlays the shipped one by name (sty_a4603ea2), so the
	// shipped categories stay in force unless the fixture declares them. The lane
	// is declared under each, keeping every category on the fixture's route as it
	// was when the fixture replaced the shipped file wholesale.
	var done strings.Builder
	for _, cat := range []string{`"*"`, "docs", "epic-parent", "parent", "execution", "task"} {
		done.WriteString("[" + cat + "]\nobligations = [" + strings.Join(obligations, ", ") + "]\n")
		if park != "" {
			done.WriteString("park = " + roleRef(park) + "\n")
		}
		if cancel != "" {
			done.WriteString("cancel = " + roleRef(cancel) + "\n")
		}
		done.WriteString("\n")
	}
	cat.WriteString(gates)
	return wfDoc(done.String(), cat.String())
}

// fixtureSpec builds the Spec a packed route fixture derives, so a test can
// assert on edge resolution through the surviving front door (specReviewerSkills)
// rather than a retired text parser.
func fixtureSpec(t *testing.T, packed string) wfdot.Spec {
	t.Helper()
	done, step, ok := strings.Cut(packed, routeHalfSplit)
	if !ok {
		t.Fatal("fixture is not a packed route source")
	}
	spec, err := wfdot.ParseRoute(done, step, "*", nil)
	if err != nil {
		t.Fatalf("derive fixture route: %v", err)
	}
	return spec
}

// firstObligation is the obligation name spineWF gives a step — fixtures that
// assert on route shape use it rather than re-deriving the convention.
func obligationOf(step string) string { return "ob-" + step }

// conformantSkill wraps a bare rubric in the conformant skill-doc envelope for
// the same reason (the gate refuses an invalid PRESENT reviewer skill); a body
// already carrying frontmatter is returned as-is so fixtures with authored
// frontmatter (e.g. a check: skill) stay verbatim. The envelope includes a
// minimal verdict-contract phrase so the reviewer skill contract check (design
// §6.3 / sty_e21cbc08) passes for test stubs that only supply a short rubric.
func conformantSkill(name, rubric string) string {
	if strings.HasPrefix(rubric, "---\n") {
		return rubric
	}
	return "---\nname: " + name + "\ntype: skill\ndescription: test rubric\n---\n\n" +
		rubric + "\n\nReturn JSON {\"decision\": \"accept\"|\"reject\", \"notes\": \"…\"}.\n"
}

// baselineWorkflow is an arbitrary workflow DOC NAME these fixtures author their
// own DOT under. It used to be the engine's by-name order-zero fallback; the
// fallback is now the derived route the binary ships (sty_3795e7f6), so the name
// carries no mechanism — it is only a fixture label, kept so the fixtures stay
// recognisable.
const baselineWorkflow = "satelle-baseline-workflow"

var testWorkflow = spineWF("", "cancelled", "",
	"in_progress|executor||satelle-story-intent-review",
	"done|||satelle-story-done-review")

type fakeRunner struct {
	out string
	err error
	got agentcli.Request
	// opened is the material files the work item named, read during Run,
	// before a successful review deletes the scratch directory.
	opened map[string][]byte
}

func (f *fakeRunner) Name() string    { return "fake" }
func (f *fakeRunner) Command() string { return "fake -p --append-system-prompt {system}" }
func (f *fakeRunner) Run(_ context.Context, req agentcli.Request) ([]byte, error) {
	f.got = req
	f.opened = openPayloadFiles(req.Payload)
	return []byte(f.out), f.err
}

// openPayloadFiles reads every path the referenced work item names. A payload
// that is still the capped inline struct has none, and the map stays empty.
func openPayloadFiles(payload string) map[string][]byte {
	out := map[string][]byte{}
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &top); err != nil {
		return out
	}
	read := func(p string) {
		if p == "" {
			return
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return
		}
		out[p] = b
	}
	if raw, ok := top["docs"]; ok {
		var docs []struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(raw, &docs) == nil {
			for _, d := range docs {
				read(d.Path)
			}
		}
	}
	if raw, ok := top["diff"]; ok {
		var d struct {
			PatchPath string `json:"patch_path"`
		}
		if json.Unmarshal(raw, &d) == nil {
			read(d.PatchPath)
		}
	}
	for _, key := range []string{"messages", "prior_verdicts", "definition_edits", "deferred", "waivers", "citations", "ledger"} {
		raw, ok := top[key]
		if !ok {
			continue
		}
		var p struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(raw, &p) == nil {
			read(p.Path)
		}
	}
	return out
}

func payloadFieldPath(t *testing.T, payload, key string) string {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &top); err != nil {
		t.Fatalf("payload: %v", err)
	}
	raw, ok := top[key]
	if !ok {
		t.Fatalf("payload missing %q", key)
	}
	if key == "diff" {
		var d struct {
			PatchPath string `json:"patch_path"`
		}
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatal(err)
		}
		return d.PatchPath
	}
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	return p.Path
}

func openedAt(t *testing.T, opened map[string][]byte, payload, key string) []byte {
	t.Helper()
	p := payloadFieldPath(t, payload, key)
	b, ok := opened[p]
	if !ok {
		t.Fatalf("material %s was not captured from %s", key, p)
	}
	return b
}

func openedDocs(t *testing.T, payload string, opened map[string][]byte) map[string]string {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &top); err != nil {
		t.Fatal(err)
	}
	raw, ok := top["docs"]
	if !ok {
		t.Fatal("payload missing docs")
	}
	var docs []struct {
		Name string `json:"name"`
		Path string `json:"path"`
		Body string `json:"body"`
	}
	if err := json.Unmarshal(raw, &docs); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, d := range docs {
		if d.Body != "" {
			t.Errorf("doc %s still carries an inline body", d.Name)
		}
		b, ok := opened[d.Path]
		if !ok {
			t.Fatalf("doc %s was not captured from %s", d.Name, d.Path)
		}
		out[d.Name] = string(b)
	}
	return out
}

// capCheckSkill is a functional check. Its stdin is today's capped JSON.
// No frontmatter: conformantSkill stamps the skill name the workflow asked for.
const capCheckSkill = "Reads the capped transition on stdin.\n\n```check\ncat\n```\n"

// cappedGateStdin runs the same resolvers through a check fence and returns
// the stdin the check received.
func cappedGateStdin(t *testing.T, wf string, item workitem.Item, to string, setup func(*Engine)) string {
	t.Helper()
	g, r := newEngine(t, `{"decision":"reject"}`, fakeDocs{workflow: wf, skillBody: capCheckSkill, skillFound: true})
	if setup != nil {
		setup(g)
	}
	var stdin string
	g.check = func(_ context.Context, _, _, payload string) (string, error) {
		stdin = payload
		return "ok\n", nil
	}
	if _, err := g.Gate(context.Background(), item, to); err != nil {
		t.Fatal(err)
	}
	if r.got.SystemPrompt != "" {
		t.Fatal("a functional check must not start the reviewer")
	}
	if stdin == "" {
		t.Fatal("check received no stdin")
	}
	return stdin
}

type fakeDocs struct {
	workflow   string
	skillBody  string
	skillFound bool
	// extraWorkflows are returned by List in addition to the baseline — used to
	// exercise category→workflow selection via applies_to.
	extraWorkflows []docindex.Doc
	// extraSkills are returned by List("skills") and resolved by Get("skills",…) —
	// used to exercise the always-on system reviewer layer (tagged reviewer:always)
	// and per-skill reviewer bodies.
	extraSkills []docindex.Doc
	// extraPrinciples are returned by List("principles") — used to exercise the
	// always-resident principle injection into the reviewer system prompt.
	extraPrinciples []docindex.Doc
}

// routeHalves splits a packed fixture into the done.md and step.md docs.
func (d fakeDocs) routeHalves() []docindex.Doc {
	done, step, ok := strings.Cut(d.workflow, routeHalfSplit)
	if !ok {
		return nil
	}
	return []docindex.Doc{
		{Kind: "workflows", Name: "done", Body: done},
		{Kind: "workflows", Name: "step", Body: step},
	}
}

func (d fakeDocs) Get(_ context.Context, kind, name string) (docindex.Doc, error) {
	switch kind {
	case "workflows":
		for _, h := range d.routeHalves() {
			if h.Name == name {
				return h, nil
			}
		}
		for _, w := range d.extraWorkflows {
			if w.Name == name {
				return w, nil
			}
		}
	case "skills":
		for _, s := range d.extraSkills {
			if s.Name == name {
				return s, nil
			}
		}
		if d.skillFound {
			return docindex.Doc{Kind: kind, Name: name, Body: conformantSkill(name, d.skillBody)}, nil
		}
		return docindex.Doc{}, docindex.ErrNotFound
	case "principles":
		for _, p := range d.extraPrinciples {
			if p.Name == name {
				return p, nil
			}
		}
	}
	return docindex.Doc{}, docindex.ErrNotFound
}

func (d fakeDocs) List(_ context.Context, kind string) ([]docindex.Doc, error) {
	switch kind {
	case "workflows":
		return append(d.routeHalves(), d.extraWorkflows...), nil
	case "skills":
		return d.extraSkills, nil
	case "principles":
		return d.extraPrinciples, nil
	}
	return nil, nil
}

const alwaysPrincipleDoc = `---
name: satelle-test-belief
kind: principle
tags: [kind:principle, principles:session]
---
# Test belief

This resident belief MUST be visible to every reviewer.`

// secondAlwaysDoc is a SECOND principles:session principle (not the operating one)
// — proves the reviewer injects the full session SET, matching SessionStart, not
// just config.OperatingPrinciple. Its prose deliberately omits the literal tag so
// the frontmatter-stripped assertion below still holds.
const secondAlwaysDoc = `---
name: satelle-second-resident
type: principle
tags: [type:principle, principles:session]
---
# Second belief

The full resident SET must be injected, not just the operating principle.`

// TestReviewerSystemPromptInjectsPrinciplesAndCTA: a reviewer's system prompt
// carries the always-resident principles, the read-only call-to-action (teaching
// it to resolve substrate via the satelle CLI), and its own rubric.
func TestReviewerSystemPromptInjectsPrinciplesAndCTA(t *testing.T) {
	docs := fakeDocs{
		workflow:   testWorkflow,
		skillBody:  "rubric body",
		skillFound: true,
		extraPrinciples: []docindex.Doc{
			// The full principles:session SET is injected (operating principle + any
			// other session-tagged principle); a non-tagged principle is not.
			{Kind: "principles", Name: config.OperatingPrinciple, Body: alwaysPrincipleDoc},
			{Kind: "principles", Name: "satelle-second-resident", Body: secondAlwaysDoc},
			{Kind: "principles", Name: "satelle-not-resident", Body: "---\nname: x\ntype: principle\n---\nnot resident"},
		},
	}
	g, r := newEngine(t, `{"decision":"accept"}`, docs)
	if _, err := g.Gate(context.Background(), workitem.Item{ID: "sty_1", Status: "in_progress"}, "done"); err != nil {
		t.Fatal(err)
	}
	sp := r.got.SystemPrompt
	if !strings.Contains(sp, "This resident belief MUST be visible") {
		t.Errorf("operating always-resident principle not injected:\n%s", sp)
	}
	if !strings.Contains(sp, "The full resident SET must be injected") {
		t.Errorf("the full principles:session SET not injected (second resident missing):\n%s", sp)
	}
	if strings.Contains(sp, "not resident") {
		t.Errorf("a non-resident principle must NOT be injected:\n%s", sp)
	}
	if !strings.Contains(sp, "read-only") || !strings.Contains(sp, ".satelle/") {
		t.Errorf("call-to-action (read-only, reads materialised .satelle substrate) missing:\n%s", sp)
	}
	if !strings.Contains(sp, "rubric body") {
		t.Errorf("the reviewer's own rubric must still ride in the prompt:\n%s", sp)
	}
	// Frontmatter of the injected principle must be stripped (no raw tags line).
	if strings.Contains(sp, "principles:session") {
		t.Errorf("injected principle frontmatter should be stripped:\n%s", sp)
	}
}

// TestReviewerSystemPromptOmitsPrinciplesWhenDisabled: the agents-layer toggle
// (default ON) omits the resident principles when turned off, while the reviewer's
// own rubric and the call-to-action still ride (sty_46a40208).
func TestReviewerSystemPromptOmitsPrinciplesWhenDisabled(t *testing.T) {
	docs := fakeDocs{
		workflow:   testWorkflow,
		skillBody:  "rubric body",
		skillFound: true,
		extraPrinciples: []docindex.Doc{
			{Kind: "principles", Name: config.OperatingPrinciple, Body: alwaysPrincipleDoc},
		},
	}
	g, r := newEngine(t, `{"decision":"accept"}`, docs)
	g.SetInjectPrinciples(false) // disable injection for this agent
	if _, err := g.Gate(context.Background(), workitem.Item{ID: "sty_1", Status: "in_progress"}, "done"); err != nil {
		t.Fatal(err)
	}
	sp := r.got.SystemPrompt
	if strings.Contains(sp, "This resident belief MUST be visible") {
		t.Errorf("principles injected despite the toggle being off:\n%s", sp)
	}
	if !strings.Contains(sp, "rubric body") || !strings.Contains(sp, "read-only") {
		t.Errorf("rubric + call-to-action must still ride when injection is off:\n%s", sp)
	}
}

func TestStripFrontmatter(t *testing.T) {
	got := stripFrontmatter("---\nname: x\ntags: [a]\n---\n# Body\n\ntext")
	if strings.Contains(got, "name:") || !strings.Contains(got, "# Body") {
		t.Errorf("stripFrontmatter = %q", got)
	}
	if got := stripFrontmatter("no frontmatter here"); got != "no frontmatter here" {
		t.Errorf("body without frontmatter should pass through, got %q", got)
	}
}

// diagnosisPrincipleName names the fixture session principle.
const diagnosisPrincipleName = "satelle-diagnosis-is-hypothesis"

// diagnosisProofPhrase sits in the principle body and not in the description or
// the first heading, so a compact index line cannot satisfy the assertion.
const diagnosisProofPhrase = "unproven until discovery proves"

// diagnosisPrincipleFixture is a self-contained session-tagged principle. The
// test builds it in a temp repo; it does not read this repo's gitignored
// substrate. The proof phrase sits in the body only, never in the description
// or the first heading.
const diagnosisPrincipleFixture = `---
name: satelle-diagnosis-is-hypothesis
type: principle
tags: [type:principle, principles:session]
applies_to: ["*"]
description: A fixture principle whose body carries a phrase the index line cannot copy.
---

# satelle-diagnosis-is-hypothesis — a fixture heading

What you perceive about the code is unproven until discovery proves it. Record
it as intent and evidence.
`

func diagnosisParts(body string) (desc, heading, prose string) {
	prose = stripFrontmatter(body)
	s := strings.TrimLeft(body, "\n")
	if strings.HasPrefix(s, "---") {
		rest := s[len("---"):]
		if i := strings.Index(rest, "\n---"); i >= 0 {
			for _, ln := range strings.Split(rest[:i], "\n") {
				t := strings.TrimSpace(ln)
				if strings.HasPrefix(t, "description:") {
					desc = strings.Trim(strings.TrimSpace(strings.TrimPrefix(t, "description:")), `"'`)
				}
			}
		}
	}
	for _, ln := range strings.Split(prose, "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "#") {
			heading = strings.TrimSpace(strings.TrimLeft(t, "#"))
			break
		}
	}
	return desc, heading, prose
}

// TestDispatchedSeatInlinesDiagnosisPrinciple: a dispatched seat's system prompt
// is built from sessionPrinciples after the fixture principle is written into a temp
// repo and reindexed. sessionPrinciples joins stripped bodies and does not prefix
// the doc name, so the name has to sit in the body — the heading carries
// satelle-diagnosis-is-hypothesis. Both sessionPrinciples and the system prompt
// must contain that name and the body phrase. The phrase is not in the
// description or the first heading, so an index line cannot satisfy it.
func TestDispatchedSeatInlinesDiagnosisPrinciple(t *testing.T) {
	raw := []byte(diagnosisPrincipleFixture)
	body := diagnosisPrincipleFixture
	if !strings.Contains(body, "name: "+diagnosisPrincipleName) {
		t.Fatalf("principle file is not named %s", diagnosisPrincipleName)
	}
	if !hasSessionTag(body) {
		t.Fatal("principle is not tagged principles:session")
	}
	desc, heading, prose := diagnosisParts(body)
	if !strings.Contains(prose, diagnosisProofPhrase) {
		t.Fatalf("principle body missing proof phrase %q", diagnosisProofPhrase)
	}
	if strings.Contains(desc, diagnosisProofPhrase) || strings.Contains(heading, diagnosisProofPhrase) {
		t.Fatalf("proof phrase is on an index input (description %q, heading %q)", desc, heading)
	}

	repo := t.TempDir()
	dir := filepath.Join(repo, ".satelle", "principles")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, diagnosisPrincipleName+".md"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "satelle.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.DocIndex.Sync(context.Background(), map[string]string{"principles": dir}, time.Now().UTC()); err != nil {
		t.Fatalf("reindex principles: %v", err)
	}

	listed, err := db.DocIndex.List(context.Background(), "principles")
	if err != nil {
		t.Fatal(err)
	}
	var indexed docindex.Doc
	for _, d := range listed {
		if d.Name == diagnosisPrincipleName {
			indexed = d
			break
		}
	}
	if indexed.Name != diagnosisPrincipleName {
		t.Fatal("reindex did not keep the principle under its name")
	}
	stripped := stripFrontmatter(indexed.Body)
	if !hasSessionTag(indexed.Body) || !strings.Contains(stripped, diagnosisPrincipleName) || !strings.Contains(stripped, diagnosisProofPhrase) {
		t.Fatalf("indexed body, frontmatter stripped, must carry the name %q and phrase %q", diagnosisPrincipleName, diagnosisProofPhrase)
	}

	resident := sessionPrinciples(context.Background(), db.DocIndex)
	g := New(&fakeRunner{}, db.DocIndex, repo, "")
	req, err := g.buildRequest(context.Background(), invocation{
		charter:    executorCharter("coder", "in_progress", "default"),
		rubric:     "RUBRIC-BODY",
		principles: config.PrinciplesSession,
		payload:    map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The name is in the body heading, so it survives stripFrontmatter and reaches
	// the seat. The phrase is absent from the description and the first heading,
	// so an index line cannot satisfy it.
	for _, label := range []struct{ name, got string }{
		{"sessionPrinciples", resident},
		{"system prompt", req.SystemPrompt},
	} {
		if !strings.Contains(label.got, diagnosisPrincipleName) || !strings.Contains(label.got, diagnosisProofPhrase) {
			t.Errorf("%s missing principle name %q or body phrase %q:\n%s", label.name, diagnosisPrincipleName, diagnosisProofPhrase, label.got)
		}
	}
}

// TestDispatchedSeatInlinesNoDriftingDocsPrinciple: the shipped session principle
// reaches a dispatched seat from the embedded overlay alone — .satelle/principles
// is empty, so no on-disk copy can satisfy it (sty_9fa1023d).
func TestDispatchedSeatInlinesNoDriftingDocsPrinciple(t *testing.T) {
	const name = "satelle-no-drifting-docs"
	repo := t.TempDir()
	dir := filepath.Join(repo, ".satelle", "principles")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "satelle.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.DocIndex.Sync(context.Background(), map[string]string{"principles": dir}, time.Now().UTC()); err != nil {
		t.Fatalf("reindex principles: %v", err)
	}

	resident := sessionPrinciples(context.Background(), db.DocIndex)
	g := New(&fakeRunner{}, db.DocIndex, repo, "")
	req, err := g.buildRequest(context.Background(), invocation{
		charter:    executorCharter("coder", "in_progress", "default"),
		rubric:     "RUBRIC-BODY",
		principles: config.PrinciplesSession,
		payload:    map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, label := range []struct{ name, got string }{
		{"sessionPrinciples", resident},
		{"system prompt", req.SystemPrompt},
	} {
		if !strings.Contains(label.got, name) {
			t.Errorf("%s missing the embedded principle %q:\n%s", label.name, name, label.got)
		}
	}
}

func skillDoc(name string) docindex.Doc {
	return docindex.Doc{Kind: "skills", Name: name, Body: conformantSkill(name, "rubric body")}
}

// engageDOT is a valid DOT workflow whose start state is backlog. Its path to done
// runs through an executor step (commit_push) with an @skill: prompt — the thing
// the engagement guard resolves.
var engageDOT = spineWF("", "cancelled @satelle-story-cancel-review", "",
	"in_progress|executor||satelle-story-intent-review",
	"commit_push|executor|commit-push",
	"done|||satelle-story-done-review")

// TestEngagementBlockedWhenExecutorSkillMissing: engaging under a workflow whose
// path to done has an executor step with an unresolvable skill is rejected up
// front (deterministically, no agent), naming the missing skill.
func TestEngagementBlockedWhenExecutorSkillMissing(t *testing.T) {
	// Only the intent + done reviewers resolve; the executor skill commit-push does NOT.
	docs := fakeDocs{workflow: engageDOT, extraSkills: []docindex.Doc{
		skillDoc("satelle-story-intent-review"),
		skillDoc("satelle-story-done-review"),
	}}
	g, _ := newEngine(t, `{"decision":"accept"}`, docs)
	dec, err := g.Gate(context.Background(), workitem.Item{ID: "sty_1", Status: "backlog"}, "in_progress")
	if err != nil {
		t.Fatal(err)
	}
	if dec.Accept {
		t.Fatal("expected engagement blocked (commit-push missing), got accept")
	}
	if dec.Skill != "satelle-workflow-skill-check" {
		t.Errorf("blocking skill = %q, want satelle-workflow-skill-check", dec.Skill)
	}
	if !strings.Contains(dec.Notes, "commit-push") {
		t.Errorf("reject notes should name the missing executor skill: %q", dec.Notes)
	}
	if strings.Contains(dec.Notes, "satelle-story-cancel-review") {
		t.Errorf("a reviewer gate on the cancel exit must NOT be required: %q", dec.Notes)
	}
}

// codeSkillDOT is a workflow whose in_progress executor names @skill:code —
// the post-removal scenario where the repo has no disk copy either (sty_01f49dd5 AC5).
var codeSkillDOT = spineWF("", "cancelled @satelle-story-cancel-review", "",
	"in_progress|executor|code|satelle-story-intent-review",
	"done|||satelle-story-done-review")

// TestEngagementBlockedWhenCodeSkillMissing (sty_01f49dd5 AC5): a workflow that
// still references @skill:code with no disk and no embed copy degrades with a
// clear missing-skill message naming `code`, not a crash.
func TestEngagementBlockedWhenCodeSkillMissing(t *testing.T) {
	docs := fakeDocs{workflow: codeSkillDOT, extraSkills: []docindex.Doc{
		skillDoc("satelle-story-intent-review"),
		skillDoc("satelle-story-done-review"),
	}}
	g, _ := newEngine(t, `{"decision":"accept"}`, docs)
	dec, err := g.Gate(context.Background(), workitem.Item{ID: "sty_1", Status: "backlog"}, "in_progress")
	if err != nil {
		t.Fatal(err)
	}
	if dec.Accept {
		t.Fatal("expected engagement blocked (code skill missing), got accept")
	}
	if dec.Skill != "satelle-workflow-skill-check" {
		t.Errorf("blocking skill = %q, want satelle-workflow-skill-check", dec.Skill)
	}
	if !strings.Contains(dec.Notes, "code") {
		t.Errorf("reject notes should name the missing skill code: %q", dec.Notes)
	}
}

// TestEngagementProceedsWhenExecutorSkillsResolve: when every executor skill on
// the path to done resolves, the guard passes and the edge proceeds normally.
func TestEngagementProceedsWhenExecutorSkillsResolve(t *testing.T) {
	docs := fakeDocs{workflow: engageDOT, skillBody: "rubric", skillFound: true}
	g, _ := newEngine(t, `{"decision":"accept"}`, docs)
	dec, err := g.Gate(context.Background(), workitem.Item{ID: "sty_1", Status: "backlog"}, "in_progress")
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Accept {
		t.Fatalf("expected engagement to proceed (executor skills resolve), got %+v", dec)
	}
}

// TestEngagementGuardSkippedOffEngagementEdge: the executor guard fires only on
// the engagement edge — a later transition (in_progress->done) does not run it.
func TestEngagementGuardSkippedOffEngagementEdge(t *testing.T) {
	// commit-push is missing; if the guard ran off-edge it would block. It must not.
	docs := fakeDocs{workflow: engageDOT, skillBody: "rubric", skillFound: true}
	g, _ := newEngine(t, `{"decision":"accept"}`, docs)
	dec, err := g.Gate(context.Background(), workitem.Item{ID: "sty_1", Status: "in_progress"}, "commit_push")
	if err != nil {
		t.Fatal(err)
	}
	if dec.Skill == "satelle-workflow-skill-check" {
		t.Errorf("executor guard must not run off the engagement edge; skill=%q", dec.Skill)
	}
}

// Surface-scoped design gate for skip-telemetry tests (sty_dcce86d5 AC4).
var scopedAppliesDOT = spineWF("", "",
	`[[gate]]
skill = "design-review"
agent = "reviewer"
on = ["in_progress"]
applies_to = ["surface:ui"]

[[gate]]
skill = "satelle-estimate-actual-review"
agent = "reviewer"
on = ["in_progress"]
`,
	"in_progress|executor|code|satelle-story-intent-review",
	"done|||satelle-story-done-review")

// TestScopedGateSkippedTelemetry: applies_to filter emits scoped-gate-skipped
// when tags miss, and does not when tags match (sty_dcce86d5 AC4).
func TestScopedGateSkippedTelemetry(t *testing.T) {
	docs := fakeDocs{workflow: scopedAppliesDOT, skillBody: "rubric", skillFound: true}
	g, _ := newEngine(t, `{"decision":"accept"}`, docs)
	recs := captureTelemetry(g)

	// Miss: surface:cli should skip design-review
	_, err := g.Gate(context.Background(), workitem.Item{
		ID: "sty_cli", Status: "backlog", Tags: []string{"surface:cli"},
	}, "in_progress")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range *recs {
		if r.kind == "scoped-gate-skipped" {
			found = true
			if r.data["skill"] != "design-review" {
				t.Errorf("skip skill = %v", r.data["skill"])
			}
			if r.data["reason"] != "applies_to" {
				t.Errorf("reason = %v", r.data["reason"])
			}
		}
	}
	if !found {
		t.Fatalf("expected scoped-gate-skipped telemetry, got %#v", *recs)
	}

	// Hit: surface:ui should not skip design
	*recs = nil
	_, err = g.Gate(context.Background(), workitem.Item{
		ID: "sty_ui", Status: "backlog", Tags: []string{"surface:ui"},
	}, "in_progress")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range *recs {
		if r.kind == "scoped-gate-skipped" {
			t.Errorf("must not skip when tags match: %#v", r)
		}
	}
}

// Augmentation DOT: code-ui is only required for surface:ui (sty_8225d8a5 AC5).
var engageAugDOT = spineWF("", "",
	`[[gate]]
skill = "code-ui"
agent = "executor"
on = ["in_progress"]
applies_to = ["surface:ui"]
`,
	"in_progress|executor|code|satelle-story-intent-review",
	"done|||satelle-story-done-review")

// TestEngagementAugmentationSurfaceAware: missing code-ui blocks surface:ui
// engagement and does NOT block surface:cli (sty_8225d8a5).
func TestEngagementAugmentationSurfaceAware(t *testing.T) {
	docs := fakeDocs{workflow: engageAugDOT, extraSkills: []docindex.Doc{
		skillDoc("satelle-story-intent-review"),
		skillDoc("satelle-story-done-review"),
		skillDoc("code"),
		// code-ui deliberately missing
	}}
	g, _ := newEngine(t, `{"decision":"accept"}`, docs)

	ui, err := g.Gate(context.Background(), workitem.Item{
		ID: "sty_ui", Status: "backlog", Tags: []string{"surface:ui"},
	}, "in_progress")
	if err != nil {
		t.Fatal(err)
	}
	if ui.Accept {
		t.Fatal("surface:ui must block when code-ui missing")
	}
	if !strings.Contains(ui.Notes, "code-ui") {
		t.Errorf("notes should name code-ui: %q", ui.Notes)
	}

	cli, err := g.Gate(context.Background(), workitem.Item{
		ID: "sty_cli", Status: "backlog", Tags: []string{"surface:cli"},
	}, "in_progress")
	if err != nil {
		t.Fatal(err)
	}
	if !cli.Accept {
		t.Fatalf("surface:cli must NOT require code-ui, got %+v", cli)
	}
}

func newEngine(t *testing.T, out string, docs fakeDocs) (*Engine, *fakeRunner) {
	t.Helper()
	r := &fakeRunner{out: out}
	return New(r, docs, "/repo", ""), r
}

func openEngineLedger(t *testing.T) *ledger.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "satelle.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db.Ledger
}

var errFakeAgent = errors.New("fake agent failure")

// scriptedRunner returns a queued sequence of (out, err) — one per Run call — to
// exercise transient reviewer failures (sty_d71b0791). Once exhausted it repeats
// the last result.
type scriptedRunner struct {
	results []struct {
		out string
		err error
	}
	calls int
}

func (s *scriptedRunner) Name() string    { return "scripted" }
func (s *scriptedRunner) Command() string { return "scripted" }
func (s *scriptedRunner) Run(_ context.Context, _ agentcli.Request) ([]byte, error) {
	i := s.calls
	s.calls++
	if i >= len(s.results) {
		i = len(s.results) - 1
	}
	return []byte(s.results[i].out), s.results[i].err
}

// A transient no-verdict reviewer result (an empty/garbled/rate-limited subprocess
// under concurrent load) is RETRIED with backoff, so the gate still advances
// rather than failing on the first shot (sty_d71b0791).
func TestGate_retriesTransientNoVerdictThenAdvances(t *testing.T) {
	docs := fakeDocs{workflow: testWorkflow, skillBody: "rubric body", skillFound: true}
	r := &scriptedRunner{results: []struct {
		out string
		err error
	}{
		{out: "rate limited, please retry"}, // no verdict → transient
		{err: errFakeAgent},                 // agent error → transient
		{out: `{"decision":"accept"}`},      // verdict on the 3rd try
	}}
	g := New(r, docs, "/repo", "")
	g.backoff = func(int) time.Duration { return 0 } // no real waits in the test

	dec, err := g.Gate(context.Background(), workitem.Item{ID: "sty_1", Status: "in_progress"}, "done")
	if err != nil {
		t.Fatalf("gate should advance after a transient retry, got err: %v", err)
	}
	if !dec.Accept {
		t.Fatalf("gate should accept once the reviewer returns a verdict: %+v", dec)
	}
	if r.calls != 3 {
		t.Fatalf("expected 3 reviewer attempts (2 transient + 1 verdict), got %d", r.calls)
	}
}

// When every attempt fails to produce a verdict, the gate surfaces a CLEAR error
// (naming the retry exhaustion) rather than a silent non-advance — the transition
// is deterministic (sty_d71b0791).
func TestGate_clearErrorWhenNoVerdictAfterRetries(t *testing.T) {
	docs := fakeDocs{workflow: testWorkflow, skillBody: "rubric body", skillFound: true}
	r := &scriptedRunner{results: []struct {
		out string
		err error
	}{{out: "still no verdict"}}}
	g := New(r, docs, "/repo", "")
	g.backoff = func(int) time.Duration { return 0 }

	_, err := g.Gate(context.Background(), workitem.Item{ID: "sty_1", Status: "in_progress"}, "done")
	if err == nil {
		t.Fatal("expected a clear error when the reviewer never returns a verdict")
	}
	if !strings.Contains(err.Error(), "no verdict after") {
		t.Errorf("error should name the no-verdict retry exhaustion, got: %v", err)
	}
	if r.calls != defaultReviewerAttempts {
		t.Errorf("expected %d attempts, got %d", defaultReviewerAttempts, r.calls)
	}
}

// summaryWorkflow declares a MANDATORY step-summary node, so Summarise runs.
var summaryWorkflow = spineWF("", "cancelled",
	`[[gate]]
skill = "satelle-step-summary"
agent = "reviewer"
mandatory = true
`,
	"in_progress|executor||satelle-story-intent-review",
	"done|||satelle-story-done-review")

// TestParseProseDecision covers the prose-verdict fallback (sty_9485d47e): a
// reviewer that states its conclusion in prose (no JSON object) still yields a
// decision; ambiguous or marker-less output does not.
func TestParseProseDecision(t *testing.T) {
	cases := []struct {
		name       string
		out        string
		wantOK     bool
		wantAccept bool
	}{
		{"prose reject", "Verdict: **reject**. The sweep missed .claude/settings.json:6 — fix and resubmit.", true, false},
		{"prose accept", "All criteria verified in the tree.\n\nVerdict: accept", true, true},
		{"decision is form", "My decision is reject — AC2 unmet.", true, false},
		{"rubric echo is not a verdict", "Reject when a criterion is unmet; accept when all pass.", false, false},
		{"conflicting markers", "Verdict: accept. Wait — verdict: reject.", false, false},
		{"no marker", "rate limited, please retry", false, false},
		{"empty", "", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dec, ok := parseProseDecision([]byte(c.out))
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v (dec %+v)", ok, c.wantOK, dec)
			}
			if ok && dec.Accept != c.wantAccept {
				t.Errorf("accept = %v, want %v", dec.Accept, c.wantAccept)
			}
			if ok && dec.Notes == "" {
				t.Error("prose verdict should carry the prose as notes")
			}
		})
	}
}

// A JSON decision object always wins over prose wording in the same output —
// parseDecision runs first and the fallback is never consulted (sty_9485d47e).
func TestGate_jsonVerdictPrecedesProse(t *testing.T) {
	g, _ := newEngine(t, `verdict: reject — but formally: {"decision":"accept","notes":"fine"}`,
		fakeDocs{workflow: testWorkflow, skillBody: "rubric body", skillFound: true})
	dec, err := g.Gate(context.Background(), workitem.Item{ID: "sty_1", Status: "in_progress"}, "done")
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if !dec.Accept {
		t.Fatalf("JSON accept must win over prose wording: %+v", dec)
	}
}

// A prose-only REJECT verdict blocks the transition immediately — one attempt,
// no transient retries — and its reasons reach the caller as notes (sty_9485d47e).
func TestGate_proseRejectBlocksWithReasons(t *testing.T) {
	r := &scriptedRunner{results: []struct {
		out string
		err error
	}{{out: "Verdict: **reject**. No reason recorded for cancelling — add one first."}}}
	g := New(r, fakeDocs{workflow: testWorkflow, skillBody: "rubric body", skillFound: true}, "/repo", "")
	g.backoff = func(int) time.Duration { return 0 }

	dec, err := g.Gate(context.Background(), workitem.Item{ID: "sty_1", Status: "in_progress"}, "done")
	if err != nil {
		t.Fatalf("a prose verdict is a decision, not an error: %v", err)
	}
	if dec.Accept {
		t.Fatalf("prose reject must block: %+v", dec)
	}
	if !strings.Contains(dec.Notes, "No reason recorded") {
		t.Errorf("the prose reasons must reach the caller as notes, got: %q", dec.Notes)
	}
	if r.calls != 1 {
		t.Errorf("a prose verdict must not consume retries; got %d attempts", r.calls)
	}
}

// A prose-only ACCEPT verdict advances the gate on the first attempt (sty_9485d47e).
func TestGate_proseAcceptAdvances(t *testing.T) {
	r := &scriptedRunner{results: []struct {
		out string
		err error
	}{{out: "Everything checks out.\nVerdict: accept."}}}
	g := New(r, fakeDocs{workflow: testWorkflow, skillBody: "rubric body", skillFound: true}, "/repo", "")
	g.backoff = func(int) time.Duration { return 0 }

	dec, err := g.Gate(context.Background(), workitem.Item{ID: "sty_1", Status: "in_progress"}, "done")
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if !dec.Accept || r.calls != 1 {
		t.Fatalf("prose accept should advance on the first attempt: accept=%v calls=%d", dec.Accept, r.calls)
	}
}

// On a genuine no-verdict, reviewer.log captures the subprocess's FULL output
// (not a 300-char tail) and the surfaced error names where to find it; a prose
// fallback writes its own observability record (sty_9485d47e).
func TestReviewerLog_fullOutputAndProseFallback(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("x", 400) + " the real reason is at the very end"
	r := &scriptedRunner{results: []struct {
		out string
		err error
	}{{out: "garbled " + long}}}
	g := New(r, fakeDocs{workflow: testWorkflow, skillBody: "rubric body", skillFound: true}, "/repo", "")
	g.backoff = func(int) time.Duration { return 0 }
	g.SetLogDir(dir, logfile.Config{})

	_, err := g.Gate(context.Background(), workitem.Item{ID: "sty_1", Status: "in_progress"}, "done")
	if err == nil {
		t.Fatal("expected a no-verdict error")
	}
	if !strings.Contains(err.Error(), "reviewer.log") {
		t.Errorf("error should name where the full output is logged: %v", err)
	}
	logBytes, rerr := os.ReadFile(filepath.Join(dir, "reviewer.log"))
	if rerr != nil {
		t.Fatalf("reviewer.log not written: %v", rerr)
	}
	if !strings.Contains(string(logBytes), "the real reason is at the very end") {
		t.Error("reviewer.log must carry the FULL output, not a truncated tail")
	}
	if !strings.Contains(string(logBytes), "no verdict in reviewer output") {
		t.Errorf("no-verdict-with-output should be labelled as such:\n%s", logBytes)
	}

	// A prose verdict writes a fallback record instead of a failure line.
	r2 := &scriptedRunner{results: []struct {
		out string
		err error
	}{{out: "Verdict: accept."}}}
	g2 := New(r2, fakeDocs{workflow: testWorkflow, skillBody: "rubric body", skillFound: true}, "/repo", "")
	g2.SetLogDir(dir, logfile.Config{})
	if _, err := g2.Gate(context.Background(), workitem.Item{ID: "sty_2", Status: "in_progress"}, "done"); err != nil {
		t.Fatalf("prose accept gate: %v", err)
	}
	logBytes, _ = os.ReadFile(filepath.Join(dir, "reviewer.log"))
	if !strings.Contains(string(logBytes), "prose-verdict fallback") {
		t.Errorf("prose fallback should be recorded for observability:\n%s", logBytes)
	}
}

// blockingRunner blocks until its context is done — a wedged nested agent.
type blockingRunner struct{ calls int }

func (b *blockingRunner) Name() string    { return "blocking" }
func (b *blockingRunner) Command() string { return "blocking" }
func (b *blockingRunner) Run(ctx context.Context, _ agentcli.Request) ([]byte, error) {
	b.calls++
	<-ctx.Done()
	return nil, ctx.Err()
}

// A running gate emits progress so a slow-but-working reviewer is visibly
// distinct from a hang (sty_6c88ca10).
func TestGate_emitsProgress(t *testing.T) {
	g, _ := newEngine(t, `{"decision":"accept"}`,
		fakeDocs{workflow: testWorkflow, skillBody: "rubric body", skillFound: true})
	var msgs []string
	g.SetProgress(func(m string) { msgs = append(msgs, m) })
	if _, err := g.Gate(context.Background(), workitem.Item{ID: "sty_1", Status: "in_progress"}, "done"); err != nil {
		t.Fatalf("gate: %v", err)
	}
	if len(msgs) == 0 || !strings.Contains(msgs[0], "running reviewer") {
		t.Errorf("expected a 'running reviewer …' progress line, got %v", msgs)
	}
}

// A gated edge stamps structured activity (label + index/total) for seat
// observability (sty_598a8e1b AC1).
func TestGate_emitsActivity(t *testing.T) {
	g, _ := newEngine(t, `{"decision":"accept"}`,
		fakeDocs{workflow: testWorkflow, skillBody: "rubric body", skillFound: true})
	var acts []Activity
	g.SetActivity(func(id string, a Activity) {
		if id != "sty_1" {
			t.Errorf("itemID = %q, want sty_1", id)
		}
		acts = append(acts, a)
	})
	if _, err := g.Gate(context.Background(), workitem.Item{ID: "sty_1", Status: "in_progress"}, "done"); err != nil {
		t.Fatalf("gate: %v", err)
	}
	if len(acts) < 2 {
		t.Fatalf("expected gates start + at least one reviewer activity, got %v", acts)
	}
	// First stamp is the gate set (index 0); subsequent stamps have index >= 1.
	if acts[0].Total < 1 {
		t.Errorf("total must be positive: %v", acts[0])
	}
	var sawReviewer bool
	for _, a := range acts {
		if a.Index >= 1 && a.Label != "" && a.Label != "gates" {
			sawReviewer = true
		}
	}
	if !sawReviewer {
		t.Errorf("expected a per-reviewer activity stamp: %v", acts)
	}
}

// TestDefaultHardTimeoutIsUnset pins the fallback hard ceiling on one nested
// agent invocation at UNSET (sty_752c4ef2): a wall-clock cap killed working
// agents mid-implementation (sty_87b86044 at 10m, sty_7069bced at 20m), so the
// default no longer bounds a progressing dispatch by elapsed time alone — the
// idle-stall detector (TestDefaultIdleTimeoutIsFiveMinutes) does that job. An
// explicit binding timeout= still wins.
func TestDefaultHardTimeoutIsUnset(t *testing.T) {
	if defaultHardTimeout != 0 {
		t.Fatalf("defaultHardTimeout = %v, want 0 (unset)", defaultHardTimeout)
	}
	g := New(nil, fakeDocs{}, "/repo", "")
	if g.agentTimeout != 0 {
		t.Errorf("New() seeded agentTimeout = %v, want 0 (unset)", g.agentTimeout)
	}
	got, err := config.AgentBinding{}.TimeoutDuration(defaultHardTimeout)
	if err != nil {
		t.Fatalf("TimeoutDuration: %v", err)
	}
	if got != 0 {
		t.Errorf("no-timeout binding resolved to %v, want 0 (no cap)", got)
	}
	if explicit, err := (config.AgentBinding{Timeout: "5m"}).TimeoutDuration(defaultHardTimeout); err != nil || explicit != 5*time.Minute {
		t.Errorf("explicit timeout should still win, got %v, err %v", explicit, err)
	}
}

// TestDefaultIdleTimeoutIsFiveMinutes pins the shipped idle-stall default
// (sty_752c4ef2): long enough for a slow model's thinking pause between tool
// calls, short enough to catch a genuinely hung process. An explicit binding
// idle_timeout= still wins.
func TestDefaultIdleTimeoutIsFiveMinutes(t *testing.T) {
	if defaultIdleTimeout != 5*time.Minute {
		t.Fatalf("defaultIdleTimeout = %v, want 5m", defaultIdleTimeout)
	}
	g := New(nil, fakeDocs{}, "/repo", "")
	if g.idleTimeout != 5*time.Minute {
		t.Errorf("New() seeded idleTimeout = %v, want 5m", g.idleTimeout)
	}
	got, err := config.AgentBinding{}.IdleTimeoutDuration(defaultIdleTimeout)
	if err != nil {
		t.Fatalf("IdleTimeoutDuration: %v", err)
	}
	if got != 5*time.Minute {
		t.Errorf("no-idle-timeout binding resolved to %v, want 5m", got)
	}
	if explicit, err := (config.AgentBinding{IdleTimeout: "90s"}).IdleTimeoutDuration(defaultIdleTimeout); err != nil || explicit != 90*time.Second {
		t.Errorf("explicit idle_timeout should still win, got %v, err %v", explicit, err)
	}
}

// A wedged reviewer subprocess is BOUNDED by the per-invocation deadline: the
// gate fails fast with a legible timeout (no blind retries of another full
// window) and does not enact (sty_6c88ca10).
func TestGate_agentTimeoutBoundsAWedgedReviewer(t *testing.T) {
	r := &blockingRunner{}
	g := New(r, fakeDocs{workflow: testWorkflow, skillBody: "rubric body", skillFound: true}, "/repo", "")
	g.agentTimeout = 20 * time.Millisecond
	g.backoff = func(int) time.Duration { return 0 }

	start := time.Now()
	_, err := g.Gate(context.Background(), workitem.Item{ID: "sty_1", Status: "in_progress"}, "done")
	if err == nil {
		t.Fatal("a timed-out gate must surface an error, not enact")
	}
	if !strings.Contains(err.Error(), "timed out") || !strings.Contains(err.Error(), "NOT enacted") {
		t.Errorf("timeout error should be legible and name the non-enactment: %v", err)
	}
	if r.calls != 1 {
		t.Errorf("a deadline expiry must fail fast, not retry; got %d attempts", r.calls)
	}
	// time-subject: the 20ms agent timeout is the subject. The reviewer blocks until
	// cancelled, so an unbounded gate would never return; the ceiling only has to
	// be a value no loaded runner needs to enforce a 20ms deadline.
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Errorf("gate was not bounded: took %v", elapsed)
	}
}

func TestGateAcceptEnacts(t *testing.T) {
	g, r := newEngine(t, `the story is ready {"decision":"accept","notes":"looks good"} done`,
		fakeDocs{workflow: testWorkflow, skillBody: "rubric body", skillFound: true})
	dec, err := g.Gate(context.Background(), workitem.Item{ID: "sty_1", Status: "in_progress"}, "done")
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Gated || !dec.Accept {
		t.Fatalf("want gated accept, got %+v", dec)
	}
	if dec.Skill != "satelle-story-done-review" {
		t.Errorf("skill = %q", dec.Skill)
	}
	if !strings.Contains(r.got.SystemPrompt, "rubric body") {
		t.Errorf("skill body should ride in the system prompt, got %q", r.got.SystemPrompt)
	}
	if r.got.Dir != "/repo" {
		t.Errorf("reviewer should run in repo root, got %q", r.got.Dir)
	}
}

// TestGateRecordsSystemPromptAndPayloadBytes pins AC3 (sty_363eaf55): the
// decision's byte counts must equal what the engine actually built and sent —
// not a stubbed value plumbed in at the verb layer.
func TestGateRecordsSystemPromptAndPayloadBytes(t *testing.T) {
	g, r := newEngine(t, `{"decision":"accept","notes":"looks good"}`,
		fakeDocs{workflow: testWorkflow, skillBody: "rubric body", skillFound: true})
	dec, err := g.Gate(context.Background(), workitem.Item{ID: "sty_1", Status: "in_progress"}, "done")
	if err != nil {
		t.Fatal(err)
	}
	if dec.SystemPromptBytes != len(r.got.SystemPrompt) {
		t.Errorf("SystemPromptBytes = %d, want len(sent system prompt) = %d", dec.SystemPromptBytes, len(r.got.SystemPrompt))
	}
	if dec.PayloadBytes != len(r.got.Payload) {
		t.Errorf("PayloadBytes = %d, want len(sent payload) = %d", dec.PayloadBytes, len(r.got.Payload))
	}
	if dec.SystemPromptBytes == 0 || dec.PayloadBytes == 0 {
		t.Fatalf("byte counts must be non-zero for a real gate run: %+v", dec)
	}
}

func TestGateRejectBlocks(t *testing.T) {
	g, _ := newEngine(t, `{"decision":"reject","notes":"no acceptance criteria"}`,
		fakeDocs{workflow: testWorkflow, skillBody: "rubric", skillFound: true})
	dec, err := g.Gate(context.Background(), workitem.Item{Status: "in_progress"}, "done")
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Gated || dec.Accept {
		t.Fatalf("want gated reject, got %+v", dec)
	}
	if dec.Notes != "no acceptance criteria" {
		t.Errorf("notes = %q", dec.Notes)
	}
}

func TestUngatedEdgeIsAdvisory(t *testing.T) {
	// backlog→cancelled has no reviewer_skill — must not gate (and must not run).
	g, r := newEngine(t, `{"decision":"reject"}`,
		fakeDocs{workflow: testWorkflow, skillBody: "rubric", skillFound: true})
	dec, err := g.Gate(context.Background(), workitem.Item{Status: "backlog"}, "cancelled")
	if err != nil {
		t.Fatal(err)
	}
	if dec.Gated {
		t.Errorf("ungated edge should report Gated=false, got %+v", dec)
	}
	if r.got.SystemPrompt != "" {
		t.Errorf("reviewer must not run on an ungated edge")
	}
}

func TestNamedSkillButRubricAbsentIsAdvisory(t *testing.T) {
	// Workflow names a reviewer skill, but its rubric is not installed — advisory
	// until it ships (keeps fresh repos / pre-A4 working).
	g, r := newEngine(t, `{"decision":"reject"}`,
		fakeDocs{workflow: testWorkflow, skillFound: false})
	dec, err := g.Gate(context.Background(), workitem.Item{Status: "in_progress"}, "done")
	if err != nil {
		t.Fatal(err)
	}
	if dec.Gated {
		t.Errorf("absent rubric should be advisory, got %+v", dec)
	}
	if r.got.SystemPrompt != "" {
		t.Errorf("reviewer must not run without a rubric")
	}
}

func TestBadDecisionErrors(t *testing.T) {
	g, _ := newEngine(t, `no json here`,
		fakeDocs{workflow: testWorkflow, skillBody: "rubric", skillFound: true})
	if _, err := g.Gate(context.Background(), workitem.Item{Status: "in_progress"}, "done"); err == nil {
		t.Fatal("expected error on unparseable reviewer output")
	}
}

func TestReviewerSkillsFor(t *testing.T) {
	spec := fixtureSpec(t, testWorkflow)
	if got, _, _, declared, _, _ := specReviewerSkills(spec, "in_progress", "done"); len(got) != 1 || got[0] != "satelle-story-done-review" || !declared {
		t.Errorf("in_progress→done = (%v, %v), want ([done-review], true)", got, declared)
	}
	if got, _, _, declared, _, _ := specReviewerSkills(spec, "backlog", "cancelled"); len(got) != 0 || !declared {
		t.Errorf("declared ungated edge = (%v, %v), want (nil, true)", got, declared)
	}
	if got, _, _, declared, _, _ := specReviewerSkills(spec, "backlog", "nowhere"); len(got) != 0 || declared {
		t.Errorf("undeclared edge = (%v, %v), want (nil, false)", got, declared)
	}
	// An ordered list: reviewer_skills takes precedence and preserves order.
	multi := "transitions:\n  - {from: deployed, to: done, reviewer_skills: [first-review, second-review]}\n"
	if got, _, _, declared := reviewerSkillsFor(multi, "deployed", "done"); len(got) != 2 || got[0] != "first-review" || got[1] != "second-review" || !declared {
		t.Errorf("reviewer_skills list = (%v, %v), want ([first-review second-review], true)", got, declared)
	}
}

// mapRunner returns a verdict keyed by the review_skill in the reviewer payload
// and records the order skills were invoked — so a test can drive distinct
// per-reviewer verdicts and assert run order / short-circuit.
type mapRunner struct {
	verdict map[string]string
	seen    []string
}

func (m *mapRunner) Name() string    { return "map" }
func (m *mapRunner) Command() string { return "map -p --append-system-prompt {system}" }
func (m *mapRunner) Run(_ context.Context, req agentcli.Request) ([]byte, error) {
	var p struct {
		ReviewSkill string `json:"review_skill"`
	}
	_ = json.Unmarshal([]byte(req.Payload), &p)
	m.seen = append(m.seen, p.ReviewSkill)
	out := m.verdict[p.ReviewSkill]
	if out == "" {
		out = `{"decision":"accept"}`
	}
	return []byte(out), nil
}

func TestGateMultipleReviewersAllAccept(t *testing.T) {
	wf := spineWF("", "", "",
		"in_progress|executor",
		"done|||rev-a, rev-b, rev-c|reviewer|0")
	mr := &mapRunner{}
	g := New(mr, fakeDocs{workflow: wf, skillBody: "rubric", skillFound: true}, "/repo", "")
	dec, err := g.Gate(context.Background(), workitem.Item{Status: "in_progress"}, "done")
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Gated || !dec.Accept {
		t.Fatalf("want gated accept, got %+v", dec)
	}
	if len(dec.Reviewers) != 3 {
		t.Fatalf("want 3 reviewer verdicts, got %d: %+v", len(dec.Reviewers), dec.Reviewers)
	}
	for i, want := range []string{"rev-a", "rev-b", "rev-c"} {
		rv := dec.Reviewers[i]
		if rv.Skill != want || rv.Order != i || !rv.Accept || rv.System {
			t.Errorf("reviewer[%d] = %+v, want skill %s order %d accept non-system", i, rv, want, i)
		}
	}
	if len(mr.seen) != 3 {
		t.Errorf("all reviewers should run when all accept, ran %v", mr.seen)
	}
}

func TestGateMultipleReviewersRejectAttributedAndShortCircuits(t *testing.T) {
	wf := spineWF("", "", "",
		"in_progress|executor",
		"done|||rev-a, rev-b, rev-c|reviewer|0")
	mr := &mapRunner{verdict: map[string]string{"rev-b": `{"decision":"reject","notes":"b says no"}`}}
	g := New(mr, fakeDocs{workflow: wf, skillBody: "rubric", skillFound: true}, "/repo", "")
	dec, err := g.Gate(context.Background(), workitem.Item{Status: "in_progress"}, "done")
	if err != nil {
		t.Fatal(err)
	}
	if dec.Accept {
		t.Fatalf("a reject must block the edge, got %+v", dec)
	}
	if dec.Skill != "rev-b" || dec.Notes != "b says no" {
		t.Errorf("reject should be attributed to rev-b, got skill=%q notes=%q", dec.Skill, dec.Notes)
	}
	if len(dec.Reviewers) != 2 || !dec.Reviewers[0].Accept || dec.Reviewers[1].Accept {
		t.Fatalf("want [accept(rev-a), reject(rev-b)], got %+v", dec.Reviewers)
	}
	if len(mr.seen) != 2 || mr.seen[1] != "rev-b" {
		t.Errorf("rev-c must not run after rev-b rejected; ran %v", mr.seen)
	}
}

// scopedGateWF is the always-on-gate fixture shape: a spine plus one edge-less
// gate scoped to named steps (sty_d953c5d8 — declared in the route grammar now,
// not as a DOT node).

func TestGateScopedReviewerRunsLast(t *testing.T) {
	// A DECLARED scoped reviewer node (edge-less, on="done") runs AFTER the
	// edge-named reviewer — last in order, flagged System. This replaces the removed
	// reviewer:always tag layer: the DOT, not a skill tag, declares the gate
	// (sty_ca9f675f).
	wf := spineWF("", "",
		`[[gate]]
skill = "satelle-estimate-actual"
agent = "reviewer"
on = ["done"]
`,
		"in_progress|executor",
		"done|||satelle-story-done-review")
	mr := &mapRunner{}
	g := New(mr, fakeDocs{workflow: wf, skillBody: "rubric", skillFound: true}, "/repo", "")
	dec, err := g.Gate(context.Background(), workitem.Item{Status: "in_progress"}, "done")
	if err != nil {
		t.Fatal(err)
	}
	if len(dec.Reviewers) != 2 {
		t.Fatalf("want 2 reviewers (edge + scoped), got %+v", dec.Reviewers)
	}
	first, last := dec.Reviewers[0], dec.Reviewers[1]
	if first.Skill != "satelle-story-done-review" || first.System {
		t.Errorf("first reviewer should be the edge-named one, got %+v", first)
	}
	if last.Skill != "satelle-estimate-actual" || !last.System || last.Order != 1 {
		t.Errorf("scoped reviewer should run last and be flagged System, got %+v", last)
	}
}

func TestScopedReviewerByOnList(t *testing.T) {
	// A scoped reviewer node (on="done") joins the close edge but is skipped on an
	// unlisted edge — declared in the DOT, so it costs nothing in between and the
	// workflow remains the sole gating authority.
	wf := spineWF("", "",
		`[[gate]]
skill = "satelle-estimate-actual"
agent = "reviewer"
on = ["done"]
`,
		"in_progress|executor",
		"reviewed|executor||satelle-story-code-review",
		"done|||satelle-story-done-review")
	docs := fakeDocs{workflow: wf, skillBody: "rubric", skillFound: true}

	// to=reviewed is NOT in the scoped node's on-list → only the edge reviewer runs.
	g1 := New(&mapRunner{}, docs, "/repo", "")
	dec1, err := g1.Gate(context.Background(), workitem.Item{Status: "in_progress"}, "reviewed")
	if err != nil {
		t.Fatal(err)
	}
	if len(dec1.Reviewers) != 1 || dec1.Reviewers[0].Skill != "satelle-story-code-review" {
		t.Fatalf("reviewed edge should run only the edge reviewer, got %+v", dec1.Reviewers)
	}

	// to=done IS in the on-list → the scoped reviewer joins, last.
	g2 := New(&mapRunner{}, docs, "/repo", "")
	dec2, err := g2.Gate(context.Background(), workitem.Item{Status: "reviewed"}, "done")
	if err != nil {
		t.Fatal(err)
	}
	if len(dec2.Reviewers) != 2 || !dec2.Reviewers[1].System || dec2.Reviewers[1].Skill != "satelle-estimate-actual" {
		t.Fatalf("done edge should add the scoped reviewer last, got %+v", dec2.Reviewers)
	}
}

func TestUngovernedDoesNotMatchBrokenRoute(t *testing.T) {
	if ungoverned(wfgovern.ErrRouteSourceBroken) {
		t.Fatal("ErrRouteSourceBroken must not be treated as ungoverned")
	}
	if !ungoverned(wfgovern.ErrNoWorkflow) {
		t.Fatal("ErrNoWorkflow must stay the ungoverned case")
	}
}

func TestGateRefusesUnknownParkAgentKey(t *testing.T) {
	broken := wfDoc(
		`["*"]
obligations = ["raised", "coded", "closed"]
park = { state = "blocked", agent = "reviewer", gate = "blocked-review" }
`,
		`[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
requires = ["raised"]

[closed]
status = "done"
terminal = true
requires = ["coded"]
`)
	g, r := newEngine(t, `{"decision":"accept"}`, fakeDocs{workflow: broken, skillBody: "rubric", skillFound: true})
	_, err := g.Gate(context.Background(), workitem.Item{ID: "sty_1", Status: "in_progress", Category: "feature"}, "done")
	if err == nil {
		t.Fatal("want the close refused under a done.toml with park.agent")
	}
	if r.got.SystemPrompt != "" {
		t.Error("no reviewer may run under a broken route source")
	}
	var ref wfgovern.Refusal
	if !errors.As(err, &ref) {
		if !errors.Is(err, wfgovern.ErrRouteSourceBroken) {
			t.Fatalf("want Refusal or ErrRouteSourceBroken, got %T: %v", err, err)
		}
		return
	}
	if ref.Rule != wfgovern.RuleStructureGuard {
		t.Errorf("rule = %q; want %q", ref.Rule, wfgovern.RuleStructureGuard)
	}
}

// TestGateRefusesBrokenWorkflowStructure: a governing workflow that fails its
// deterministic structure check must never gate work — the transition is
// refused with the problems (sty_d0d6bb67), instead of silently proceeding
// under a broken definition.
func TestGateRefusesBrokenWorkflowStructure(t *testing.T) {
	// A route source that RESOLVES but fails its structure check: the done half
	// carries no scope, so the deterministic check rejects it.
	broken := strings.Replace(spineWF("", "", "", "in_progress|executor", "done"),
		"description = \"test declaration of done\"\nscope = \"system\"\n", "", 1)
	g, r := newEngine(t, `{"decision":"accept"}`, fakeDocs{workflow: broken, skillBody: "rubric", skillFound: true})
	_, err := g.Gate(context.Background(), workitem.Item{ID: "sty_1", Status: "backlog"}, "in_progress")
	if err == nil {
		t.Fatal("want the gate refused under a structurally broken workflow")
	}
	// sty_d6e209aa AC2: and the two things a silent embedded fallback would hide —
	// which embedded route would otherwise govern, and that its gates are not the
	// repository's.
	for _, want := range []string{"done", "structure validation", "embedded default route", "gates would not be the repository's gates"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal should carry %q: %v", want, err)
		}
	}
	if r.got.SystemPrompt != "" {
		t.Error("no reviewer may run under a broken workflow definition")
	}
	// sty_39e2d9df AC3: structured, and honest about having NO alternative — a
	// broken workflow governs no legal move, so the refusal offers a remedy instead.
	var ref wfgovern.Refusal
	if !errors.As(err, &ref) {
		t.Fatalf("refusal must be a wfgovern.Refusal, got %T: %v", err, err)
	}
	if ref.Rule != wfgovern.RuleStructureGuard {
		t.Errorf("rule = %q; want %q", ref.Rule, wfgovern.RuleStructureGuard)
	}
	if len(ref.Alternatives) != 0 {
		t.Errorf("alternatives = %v; a broken workflow leaves none", ref.Alternatives)
	}
	if !strings.Contains(ref.Remedy, "satelle workflow validate") {
		t.Errorf("remedy = %q; want the command that fixes the substrate", ref.Remedy)
	}
}

// TestGateRefusesBrokenReviewerSkill: a PRESENT reviewer skill that fails its
// structure check refuses the gate (an absent one stays advisory by design).
func TestGateRefusesBrokenReviewerSkill(t *testing.T) {
	docs := fakeDocs{workflow: testWorkflow, skillFound: true, extraSkills: []docindex.Doc{
		// present but broken: frontmatter carries no type/description.
		{Kind: "skills", Name: "satelle-story-done-review", Body: "---\nname: satelle-story-done-review\n---\nrubric\n"},
	}}
	g, r := newEngine(t, `{"decision":"accept"}`, docs)
	_, err := g.Gate(context.Background(), workitem.Item{ID: "sty_1", Status: "in_progress"}, "done")
	if err == nil {
		t.Fatal("want the gate refused under a structurally broken reviewer skill")
	}
	for _, want := range []string{"satelle-story-done-review", "structure validation"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal should carry %q: %v", want, err)
		}
	}
	if r.got.SystemPrompt != "" {
		t.Error("a broken reviewer skill must not run")
	}
}

// dispatchWF allocates the plan step to the NAMED agent "architect" with a
// rubric — the executor-dispatch fixture (sty_fd427546).
var dispatchWF = spineWF("", "", "",
	"plan|architect|architecture-alignment",
	"in_progress|executor",
	"done")

const contractedDispatchSkill = `---
name: architecture-alignment
type: skill
description: contracted test performer
output_name: design
output_type: design-note
output_required: true
output_schema: body
output_ac_coverage: true
---
Return a structured artifact.`

func TestDispatchExecutorValidatesAndAttachesContractedOutput(t *testing.T) {
	docs := fakeDocs{workflow: dispatchWF, skillBody: contractedDispatchSkill, skillFound: true}
	g, _ := newEngine(t, "", docs)
	r := &fakeRunner{out: `{"artifact":{"body":"# Design\n\n## AC1\ncovered\n\n## AC2\ncovered"}}`}
	g.newRunner = func(string, string) (agentcli.Runner, error) { return r, nil }
	g.SetNamedAgents(func(string) (config.AgentBinding, bool) {
		return config.AgentBinding{Command: "fake -p {system}", Tools: "read_file,grep,list_dir"}, true
	})
	var gotName, gotType, gotBody string
	g.SetArtifactAttacher(func(_ context.Context, _ workitem.Item, name, typ, body string) (string, string, error) {
		gotName, gotType, gotBody = name, typ, body
		return name, typ, nil
	})
	res, err := g.DispatchExecutor(context.Background(), workitem.Item{
		ID: "sty_contract", Status: "backlog", AcceptanceCriteria: "1. first\n2. second",
	}, "plan")
	if err != nil {
		t.Fatal(err)
	}
	if gotName != "design" || gotType != "design-note" || !strings.Contains(gotBody, "AC2") {
		t.Fatalf("attached = %q %q %q", gotName, gotType, gotBody)
	}
	if res.ArtifactName != "design" || res.ArtifactType != "design-note" {
		t.Fatalf("dispatch result = %#v", res)
	}
	if strings.Contains(r.got.AllowedTools, "Bash") {
		t.Fatalf("contracted read-only performer received mutation CLI grant: %q", r.got.AllowedTools)
	}
}

func TestDispatchExecutorContractFailuresRefuseBeforeAttach(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want string
	}{
		{"malformed", "not json", "no structured"},
		{"missing criterion", `{"artifact":{"body":"## AC1\ncovered"}}`, "criterion 2"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			docs := fakeDocs{workflow: dispatchWF, skillBody: contractedDispatchSkill, skillFound: true}
			g, _ := newEngine(t, "", docs)
			r := &fakeRunner{out: tc.out}
			g.newRunner = func(string, string) (agentcli.Runner, error) { return r, nil }
			g.SetNamedAgents(func(string) (config.AgentBinding, bool) {
				return config.AgentBinding{Command: "fake -p {system}", Tools: "read_file"}, true
			})
			attached := false
			g.SetArtifactAttacher(func(context.Context, workitem.Item, string, string, string) (string, string, error) {
				attached = true
				return "", "", nil
			})
			_, err := g.DispatchExecutor(context.Background(), workitem.Item{
				ID: "sty_bad", Status: "backlog", AcceptanceCriteria: "1. first\n2. second",
			}, "plan")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if attached {
				t.Fatal("invalid output reached attachment writer")
			}
		})
	}
}

func TestDispatchExecutorAttachmentFailureAndRetry(t *testing.T) {
	docs := fakeDocs{workflow: dispatchWF, skillBody: contractedDispatchSkill, skillFound: true}
	g, _ := newEngine(t, "", docs)
	r := &fakeRunner{out: `{"artifact":{"body":"## AC1\ncovered"}}`}
	g.newRunner = func(string, string) (agentcli.Runner, error) { return r, nil }
	g.SetNamedAgents(func(string) (config.AgentBinding, bool) {
		return config.AgentBinding{Command: "fake -p {system}", Tools: "read_file"}, true
	})
	attempts := 0
	g.SetArtifactAttacher(func(_ context.Context, _ workitem.Item, name, typ, _ string) (string, string, error) {
		attempts++
		if attempts == 1 {
			return "", "", errors.New("disk full")
		}
		return name, typ, nil
	})
	item := workitem.Item{ID: "sty_retry", Status: "backlog", AcceptanceCriteria: "1. first"}
	if _, err := g.DispatchExecutor(context.Background(), item, "plan"); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("first err = %v", err)
	}
	res, err := g.DispatchExecutor(context.Background(), item, "plan")
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || res.ArtifactName != "design" {
		t.Fatalf("retry attempts=%d result=%#v", attempts, res)
	}
}

func TestDispatchExecutorCancellationDoesNotAttach(t *testing.T) {
	docs := fakeDocs{workflow: dispatchWF, skillBody: contractedDispatchSkill, skillFound: true}
	g, _ := newEngine(t, "", docs)
	r := &fakeRunner{err: context.Canceled}
	g.newRunner = func(string, string) (agentcli.Runner, error) { return r, nil }
	g.SetNamedAgents(func(string) (config.AgentBinding, bool) {
		return config.AgentBinding{Command: "fake -p {system}", Tools: "read_file"}, true
	})
	attached := false
	g.SetArtifactAttacher(func(context.Context, workitem.Item, string, string, string) (string, string, error) {
		attached = true
		return "", "", nil
	})
	if _, err := g.DispatchExecutor(context.Background(), workitem.Item{ID: "sty_cancel", Status: "backlog"}, "plan"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if attached {
		t.Fatal("cancelled run attached an artifact")
	}
}

// TestDispatchExecutorRunsNamedBinding: entering a named-agent state spawns the
// binding's harness with the item payload, the node's rubric, and the binding's
// tools/model — nothing hardcoded (sty_fd427546).
func TestDispatchExecutorRunsNamedBinding(t *testing.T) {
	docs := fakeDocs{workflow: dispatchWF, skillBody: "alignment rubric", skillFound: true}
	g, _ := newEngine(t, "", docs)
	r := &fakeRunner{out: "did the work"}
	g.SetNamedAgents(func(name string) (config.AgentBinding, bool) {
		if name != "architect" {
			return config.AgentBinding{}, false
		}
		// A backlog-dispatched agent is realistically READ-ONLY (like the planner) —
		// a code-editing grant here would trip the edit-gate timing lock (a
		// code-writer dispatched from a non-performing state), covered by
		// TestDispatchExecutorCodeWriterFromNonPerformingRefused.
		return config.AgentBinding{Command: "fake -p {system}", Tools: "Read,Grep,Glob,Bash(satelle:*)", Model: "fable"}, true
	})
	g.newRunner = func(_iface, cmd string) (agentcli.Runner, error) {
		if cmd != "fake -p {system}" {
			t.Errorf("runner built from %q, want the binding's command", cmd)
		}
		return r, nil
	}
	res, err := g.DispatchExecutor(context.Background(),
		workitem.Item{ID: "sty_1", Title: "Align the stories", Status: "backlog"}, "plan")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Dispatched || res.Agent != "architect" || res.Skill != "architecture-alignment" {
		t.Fatalf("result = %+v, want dispatched by architect under architecture-alignment", res)
	}
	if r.got.AllowedTools != "Read,Grep,Glob,Bash(satelle:*)" || r.got.Model != "fable" {
		t.Errorf("binding grant not applied: tools=%q model=%q", r.got.AllowedTools, r.got.Model)
	}
	// The resolved model is carried on the result so the ledger can record WHICH
	// model ran the step — the audit signal for per-step model mixing (sty_5d48317b).
	if res.Model != "fable" {
		t.Errorf("resolved model not carried on the dispatch result: %q", res.Model)
	}
	if !strings.Contains(r.got.SystemPrompt, "alignment rubric") {
		t.Errorf("node rubric missing from the system prompt:\n%s", r.got.SystemPrompt)
	}
	if !strings.Contains(r.got.Payload, "Align the stories") {
		t.Errorf("item payload missing from stdin: %q", r.got.Payload)
	}
	// No reviewer-only framing may leak into a dispatched EXECUTOR's context
	// (sty_f4c1bd90): the read-only call-to-action is reviewer-specific — a
	// performing agent is not "an isolated satelle reviewer" and is not told it
	// cannot modify the repository.
	for _, banned := range []string{"isolated satelle reviewer", "You judge only", "return your verdict"} {
		if strings.Contains(r.got.SystemPrompt, banned) {
			t.Errorf("reviewer-only context %q leaked into the executor dispatch prompt:\n%s", banned, r.got.SystemPrompt)
		}
	}
}

// TestDispatchExecutorRecordsSystemPromptAndPayloadBytes pins AC3 (sty_363eaf55):
// a dispatch's recorded byte counts must equal what the engine actually built
// and sent, not a stubbed value plumbed in at the verb layer.
func TestDispatchExecutorRecordsSystemPromptAndPayloadBytes(t *testing.T) {
	docs := fakeDocs{workflow: dispatchWF, skillBody: "alignment rubric", skillFound: true}
	g, r := newEngine(t, "did the work", docs)
	g.SetNamedAgents(func(name string) (config.AgentBinding, bool) {
		if name != "architect" {
			return config.AgentBinding{}, false
		}
		return config.AgentBinding{Command: "fake -p {system}", Tools: "Read,Grep,Glob,Bash(satelle:*)", Model: "fable"}, true
	})
	g.newRunner = func(string, string) (agentcli.Runner, error) { return r, nil }
	res, err := g.DispatchExecutor(context.Background(),
		workitem.Item{ID: "sty_1", Title: "Align the stories", Status: "backlog"}, "plan")
	if err != nil {
		t.Fatal(err)
	}
	if res.SystemPromptBytes != len(r.got.SystemPrompt) {
		t.Errorf("SystemPromptBytes = %d, want len(sent system prompt) = %d", res.SystemPromptBytes, len(r.got.SystemPrompt))
	}
	if res.PayloadBytes != len(r.got.Payload) {
		t.Errorf("PayloadBytes = %d, want len(sent payload) = %d", res.PayloadBytes, len(r.got.Payload))
	}
	if res.SystemPromptBytes == 0 || res.PayloadBytes == 0 {
		t.Fatalf("byte counts must be non-zero for a real dispatch run: %+v", res)
	}
}

// TestDispatchExecutorDualPayloadArgvAndStdin proves dual delivery through the
// real agentstep dispatch path: a harness template with {payload} receives the
// same work-item JSON on argv and on stdin (sty_5cf4a1fb / sty_cc35cd0b).
func TestDispatchExecutorDualPayloadArgvAndStdin(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "claude-harness.sh")
	// $1 is {payload}; stdin is the dual channel. Echo both for the test to compare.
	body := "#!/bin/sh\nprintf 'ARGV:%s\\nSTDIN:' \"$1\"\ncat\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	docs := fakeDocs{workflow: dispatchWF, skillBody: "dual-payload rubric", skillFound: true}
	g, _ := newEngine(t, "", docs)
	g.repoRoot = dir // real cwd for the subprocess (newEngine defaults to /repo)
	harness := script + " {payload}"
	g.SetNamedAgents(func(name string) (config.AgentBinding, bool) {
		if name != "architect" {
			return config.AgentBinding{}, false
		}
		return config.AgentBinding{Command: harness, Tools: "Read,Bash(satelle:*)"}, true
	})
	// Real agentcli.RunnerFromBinding (default) — not a fakeRunner.
	g.newRunner = agentcli.RunnerFromBinding
	res, err := g.DispatchExecutor(context.Background(),
		workitem.Item{ID: "sty_dual", Title: "Dual payload item", Status: "backlog", Body: "body-here"}, "plan")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Dispatched {
		t.Fatal("expected dispatch")
	}
	out := res.Output
	if !strings.HasPrefix(out, "ARGV:") {
		t.Fatalf("argv channel missing: %q", out)
	}
	argvPart, stdinPart, ok := strings.Cut(out, "\nSTDIN:")
	if !ok {
		t.Fatalf("stdin channel missing: %q", out)
	}
	argvPayload := strings.TrimPrefix(argvPart, "ARGV:")
	if argvPayload == "" || argvPayload != stdinPart {
		t.Errorf("dual payload mismatch:\n argv=%q\nstdin=%q", argvPayload, stdinPart)
	}
	if !strings.Contains(argvPayload, "sty_dual") || !strings.Contains(argvPayload, "Dual payload item") {
		t.Errorf("payload missing work item fields: %q", argvPayload)
	}
	// Evidence keeps the placeholder unexpanded.
	if !strings.Contains(res.Command, "{payload}") {
		t.Errorf("Command evidence must keep {payload} literal: %q", res.Command)
	}
	if strings.Contains(res.Command, "sty_dual") {
		t.Errorf("Command evidence must not expand payload body: %q", res.Command)
	}
}

// TestDispatchExecutorAppliesBindingEnv pins AC2: a named binding's (already
// ${VAR}-resolved) Env reaches the dispatched agent's Request, so runProcess can
// layer it onto the child env — how a step points at an alternate backend
// (sty_001558ce).
func TestDispatchExecutorAppliesBindingEnv(t *testing.T) {
	docs := fakeDocs{workflow: dispatchWF, skillBody: "rubric", skillFound: true}
	g, _ := newEngine(t, "", docs)
	r := &fakeRunner{out: "ok"}
	env := map[string]string{
		"ANTHROPIC_BASE_URL":   "https://api.z.ai/api/anthropic",
		"ANTHROPIC_AUTH_TOKEN": "sk-resolved",
	}
	g.SetNamedAgents(func(name string) (config.AgentBinding, bool) {
		return config.AgentBinding{Command: "fake -p {system}", Tools: "Read,Bash(satelle:*)", Env: env}, true
	})
	g.newRunner = func(string, string) (agentcli.Runner, error) { return r, nil }
	if _, err := g.DispatchExecutor(context.Background(),
		workitem.Item{ID: "sty_1", Title: "T", Status: "backlog"}, "plan"); err != nil {
		t.Fatal(err)
	}
	if r.got.Env["ANTHROPIC_AUTH_TOKEN"] != "sk-resolved" || r.got.Env["ANTHROPIC_BASE_URL"] != "https://api.z.ai/api/anthropic" {
		t.Errorf("binding env not threaded to the Request: %v", r.got.Env)
	}
}

// TestDispatchExecutorAppliesBindingSettings pins AC2/AC3: a binding's Settings
// table threads through to the built Request as pre-marshalled JSON — the
// {settings} materialisation the retrospective GLM fix depends on.
func TestDispatchExecutorAppliesBindingSettings(t *testing.T) {
	docs := fakeDocs{workflow: dispatchWF, skillBody: "rubric", skillFound: true}
	g, _ := newEngine(t, "", docs)
	r := &fakeRunner{out: "ok"}
	settings := map[string]any{
		"env": map[string]any{"ANTHROPIC_BASE_URL": "https://api.z.ai/api/anthropic"},
	}
	g.SetNamedAgents(func(name string) (config.AgentBinding, bool) {
		return config.AgentBinding{Command: "fake -p {system} --settings {settings}", Tools: "Read,Bash(satelle:*)", Settings: settings}, true
	})
	g.newRunner = func(string, string) (agentcli.Runner, error) { return r, nil }
	if _, err := g.DispatchExecutor(context.Background(),
		workitem.Item{ID: "sty_1", Title: "T", Status: "backlog"}, "plan"); err != nil {
		t.Fatal(err)
	}
	want := `{"env":{"ANTHROPIC_BASE_URL":"https://api.z.ai/api/anthropic"}}`
	if r.got.Settings != want {
		t.Errorf("binding settings not threaded to the Request: got %q, want %q", r.got.Settings, want)
	}
}

// TestDispatchExecutorMissingBindingRefuses: agent=<name> with no [<name>]
// binding in agents.toml is a broken definition — the dispatch errors (the
// transition is refused), never silently falling back in-loop.
func TestDispatchExecutorMissingBindingRefuses(t *testing.T) {
	g, r := newEngine(t, "", fakeDocs{workflow: dispatchWF, skillBody: "rubric", skillFound: true})
	g.SetNamedAgents(func(string) (config.AgentBinding, bool) { return config.AgentBinding{}, false })
	_, err := g.DispatchExecutor(context.Background(), workitem.Item{ID: "sty_1", Status: "backlog"}, "plan")
	if err == nil {
		t.Fatal("want refusal for a missing named-agent binding")
	}
	for _, want := range []string{"architect", "agents.toml"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should carry %q: %v", want, err)
		}
	}
	if r.got.SystemPrompt != "" {
		t.Error("nothing may run without a binding")
	}
}

// TestDispatchExecutorInLoopStatesUnchanged: agent=executor (and agent-less)
// states dispatch nothing — the in-loop orchestrator performs, today's
// behaviour.
func TestDispatchExecutorInLoopStatesUnchanged(t *testing.T) {
	g, _ := newEngine(t, "", fakeDocs{workflow: dispatchWF, skillBody: "rubric", skillFound: true})
	called := false
	g.SetNamedAgents(func(string) (config.AgentBinding, bool) {
		called = true
		return config.AgentBinding{}, false
	})
	res, err := g.DispatchExecutor(context.Background(), workitem.Item{ID: "sty_1", Status: "plan"}, "in_progress")
	if err != nil {
		t.Fatal(err)
	}
	if res.Dispatched || called {
		t.Errorf("agent=executor must stay in-loop: %+v (resolver called: %v)", res, called)
	}
}

// TestDispatchExecutorRunFailureSurfaces: a failed named-agent run errors so the
// caller refuses the transition (status unchanged).
func TestDispatchExecutorRunFailureSurfaces(t *testing.T) {
	docs := fakeDocs{workflow: dispatchWF, skillBody: "rubric", skillFound: true}
	g, _ := newEngine(t, "", docs)
	g.SetNamedAgents(func(string) (config.AgentBinding, bool) {
		return config.AgentBinding{Command: "fake -p {system}", Tools: "Read,Bash(satelle:*)"}, true
	})
	g.newRunner = func(string, string) (agentcli.Runner, error) { return &fakeRunner{err: errFakeAgent}, nil }
	res, err := g.DispatchExecutor(context.Background(), workitem.Item{ID: "sty_1", Status: "backlog"}, "plan")
	if err == nil {
		t.Fatal("want the run failure surfaced")
	}
	if !res.Dispatched || !strings.Contains(err.Error(), "architect") {
		t.Errorf("failure should be attributed to the named agent: res=%+v err=%v", res, err)
	}
}

// TestDispatchExecutorRefusesWithoutSatelleCLI: an executor binding whose grant
// omits the read-only satelle CLI is refused at dispatch — an isolated agent cannot
// PULL the story/documents/ledger without it (sty_47d31300), so satelle names the
// fix rather than run a context-starved agent.
func TestDispatchExecutorRefusesWithoutSatelleCLI(t *testing.T) {
	docs := fakeDocs{workflow: dispatchWF, skillBody: "rubric", skillFound: true}
	g, _ := newEngine(t, "", docs)
	fr := &fakeRunner{out: "ok"}
	g.newRunner = func(string, string) (agentcli.Runner, error) { return fr, nil }

	// No context channel (Read is claude's tool, not this unknown adapter's) → refused, agent not run.
	g.SetNamedAgents(func(string) (config.AgentBinding, bool) {
		return config.AgentBinding{Command: "fake -p {system}", Tools: "Read,Grep,Glob"}, true
	})
	_, err := g.DispatchExecutor(context.Background(), workitem.Item{ID: "sty_1", Status: "backlog"}, "plan")
	if err == nil || !strings.Contains(err.Error(), "context channel") {
		t.Fatalf("want refusal naming the context-channel fix, got %v", err)
	}
	if fr.got.SystemPrompt != "" {
		t.Error("must refuse BEFORE running the agent")
	}

	// With Bash(satelle:*) CLI channel → the agent runs.
	g.SetNamedAgents(func(string) (config.AgentBinding, bool) {
		return config.AgentBinding{Command: "fake -p {system}", Tools: "Read,Bash(satelle:*)"}, true
	})
	if _, err := g.DispatchExecutor(context.Background(), workitem.Item{ID: "sty_1", Status: "backlog"}, "plan"); err != nil {
		t.Fatalf("a granted binding should dispatch: %v", err)
	}
	if fr.got.SystemPrompt == "" {
		t.Error("a granted binding must reach the run")
	}
}

// TestDispatchExecutorAcceptsGrokReadFileChannel: a write-capable grok-shaped
// grant with read_file (disk context channel, sty_565a0202) dispatches; a
// write-only / channel-less grant still refuses loudly.
func TestDispatchExecutorAcceptsGrokReadFileChannel(t *testing.T) {
	docs := fakeDocs{workflow: dispatchWF, skillBody: "rubric", skillFound: true}
	g, _ := newEngine(t, "", docs)
	fr := &fakeRunner{out: "ok"}
	g.newRunner = func(string, string) (agentcli.Runner, error) { return fr, nil }

	// Grok write grant WITH read_file → context channel present → dispatch.
	g.SetNamedAgents(func(string) (config.AgentBinding, bool) {
		return config.AgentBinding{
			Command: "fake -p {system}",
			Tools:   "read_file,grep,list_dir,write,search_replace",
		}, true
	})
	if _, err := g.DispatchExecutor(context.Background(), workitem.Item{ID: "sty_1", Status: "backlog"}, "plan"); err != nil {
		t.Fatalf("read_file context channel should dispatch: %v", err)
	}
	if fr.got.SystemPrompt == "" {
		t.Error("read_file grant must reach the run")
	}

	// Channel-less write grant → still refuses before run.
	fr.got = agentcli.Request{}
	g.SetNamedAgents(func(string) (config.AgentBinding, bool) {
		return config.AgentBinding{
			Command: "fake -p {system}",
			Tools:   "write,search_replace,grep,list_dir",
		}, true
	})
	_, err := g.DispatchExecutor(context.Background(), workitem.Item{ID: "sty_1", Status: "backlog"}, "plan")
	if err == nil || !strings.Contains(err.Error(), "context channel") {
		t.Fatalf("want channel-less refusal, got %v", err)
	}
	if fr.got.SystemPrompt != "" {
		t.Error("channel-less grant must refuse BEFORE running the agent")
	}
}

// TestDispatchExecutorClaudeReadChannel: Claude's own `Read` tool is its disk-read
// context channel, so a shell-free read-only claude grant dispatches; a claude grant
// with no read tool and no satelle shell is refused naming `Read`, never read_file.
func TestDispatchExecutorClaudeReadChannel(t *testing.T) {
	docs := fakeDocs{workflow: dispatchWF, skillBody: "rubric", skillFound: true}
	g, _ := newEngine(t, "", docs)
	fr := &fakeRunner{out: "ok"}
	g.newRunner = func(string, string) (agentcli.Runner, error) { return fr, nil }

	g.SetNamedAgents(func(string) (config.AgentBinding, bool) {
		return config.AgentBinding{Command: "claude -p {system}", Tools: "Read,Grep,Glob"}, true
	})
	if _, err := g.DispatchExecutor(context.Background(), workitem.Item{ID: "sty_1", Status: "backlog"}, "plan"); err != nil {
		t.Fatalf("claude Read should be a context channel: %v", err)
	}
	if fr.got.SystemPrompt == "" {
		t.Error("claude Read grant must reach the run")
	}

	fr.got = agentcli.Request{}
	g.SetNamedAgents(func(string) (config.AgentBinding, bool) {
		return config.AgentBinding{Command: "claude -p {system}", Tools: "Grep,Glob"}, true
	})
	_, err := g.DispatchExecutor(context.Background(), workitem.Item{ID: "sty_1", Status: "backlog"}, "plan")
	if err == nil || !strings.Contains(err.Error(), "context channel") {
		t.Fatalf("want channel-less refusal, got %v", err)
	}
	if !strings.Contains(err.Error(), "`Read` for disk reads") || strings.Contains(err.Error(), "read_file") {
		t.Errorf("refusal must name `Read`, not read_file: %v", err)
	}
	if fr.got.SystemPrompt != "" {
		t.Error("channel-less grant must refuse BEFORE running the agent")
	}
}

// coderWF is a repo opting `in_progress` into a dispatched code-writing agent
// (agent=coder) — the sty_f5bd176f opt-in. The coder is reached from `plan`, a
// PERFORMING node, so the story is engaged while the coder edits.
var coderWF = spineWF("", "", "",
	"plan|planner|plan",
	"in_progress|coder|code",
	"done")

// TestDispatchExecutorCodeWriterFromNonPerformingProceeds: with the engagement
// lease (sty_8426b9c0) acquired at-start for the TARGET engaging state, edit
// gates no longer depend on the FROM state being performing — so a code-writing
// named agent may dispatch from backlog into plan. The prior FROM-performing
// band-aid (sty_f5bd176f) is removed.
func TestDispatchExecutorCodeWriterFromNonPerformingProceeds(t *testing.T) {
	docs := fakeDocs{workflow: dispatchWF, skillBody: "rubric", skillFound: true}
	g, _ := newEngine(t, "", docs)
	fr := &fakeRunner{out: "ok"}
	g.newRunner = func(string, string) (agentcli.Runner, error) { return fr, nil }
	g.SetNamedAgents(func(string) (config.AgentBinding, bool) {
		return config.AgentBinding{Command: "fake -p {system}", Tools: "Read,Edit,Write,Bash(satelle:*)"}, true
	})
	res, err := g.DispatchExecutor(context.Background(), workitem.Item{ID: "sty_1", Status: "backlog"}, "plan")
	if err != nil {
		t.Fatalf("code-writer from non-performing FROM should proceed under lease model: %v", err)
	}
	if !res.Dispatched {
		t.Fatalf("expected dispatched, got %+v", res)
	}
	if fr.got.SystemPrompt == "" {
		t.Error("the agent must reach the run")
	}
}

// TestDispatchExecutorCodeWriterFromPerformingProceeds: the same code-writing grant
// dispatched from a PERFORMING FROM state (plan → in_progress in coderWF, where
// `plan` performs) is allowed — the story is legitimately engaged while the coder
// edits, so the lock does not fire.
func TestDispatchExecutorCodeWriterFromPerformingProceeds(t *testing.T) {
	docs := fakeDocs{workflow: coderWF, skillBody: "code rubric", skillFound: true}
	g, _ := newEngine(t, "", docs)
	fr := &fakeRunner{out: "built the slice"}
	g.newRunner = func(string, string) (agentcli.Runner, error) { return fr, nil }
	g.SetNamedAgents(func(name string) (config.AgentBinding, bool) {
		if name != "coder" {
			return config.AgentBinding{}, false
		}
		return config.AgentBinding{Command: "fake -p {system}", Tools: "Read,Edit,Write,Bash(satelle:*)"}, true
	})
	res, err := g.DispatchExecutor(context.Background(), workitem.Item{ID: "sty_1", Status: "plan"}, "in_progress")
	if err != nil {
		t.Fatalf("a code-writer from a performing state should dispatch: %v", err)
	}
	if !res.Dispatched || res.Agent != "coder" {
		t.Fatalf("result = %+v, want dispatched by coder", res)
	}
	if fr.got.SystemPrompt == "" {
		t.Error("the coder must reach the run")
	}
}

// parkEntryWF: a reviewer park node whose name is "parked" (not "blocked") so
// the mechanism has no state-name dependence.
var parkEntryWF = spineWF("parked @park-gate", "", "",
	"in_progress|executor",
	"done")

// TestParkEntryDispatchesNothing replaces TestDispatchOnEnterAgentFromPerforming
// (sty_5cabe26f), which asserted that entering a park node fired its
// on_enter_agent once. Flat dispatch retires that (sty_05a5e203): the
// orchestrator is the sole scheduler, so entering a state NEVER dispatches an
// agent of its own. Same fixture, inverted assertion — the honest record that the
// behaviour was removed rather than merely untested.
func TestParkEntryDispatchesNothing(t *testing.T) {
	docs := fakeDocs{workflow: parkEntryWF, skillBody: "triage rubric", skillFound: true}
	g, _ := newEngine(t, "", docs)
	fr := &fakeRunner{out: "triaged"}
	g.newRunner = func(string, string) (agentcli.Runner, error) { return fr, nil }
	g.SetNamedAgents(func(string) (config.AgentBinding, bool) {
		t.Error("no binding may be resolved: entry dispatches nothing")
		return config.AgentBinding{}, false
	})
	res, err := g.DispatchExecutor(context.Background(), workitem.Item{ID: "sty_1", Status: "in_progress"}, "parked")
	if err != nil {
		t.Fatalf("entering a park node must be a no-op, not an error: %v", err)
	}
	if res.Dispatched {
		t.Errorf("result = %+v, want nothing dispatched on entry", res)
	}
	if fr.got.SystemPrompt != "" {
		t.Error("no agent may run on entry to a state")
	}
}

// TestTerminalEntryDispatchesNothing: the other live on-enter dispatch was the
// retrospective on `done`. Under flat dispatch the orchestrator runs it
// (satelle story retrospect) — reaching a terminal state fires nothing.
func TestTerminalEntryDispatchesNothing(t *testing.T) {
	g, _ := newEngine(t, "", fakeDocs{workflow: parkEntryWF, skillBody: "rubric", skillFound: true})
	g.SetNamedAgents(func(string) (config.AgentBinding, bool) {
		t.Error("no binding may be resolved on entry to a terminal state")
		return config.AgentBinding{}, false
	})
	res, err := g.DispatchExecutor(context.Background(), workitem.Item{ID: "sty_1", Status: "in_progress"}, "done")
	if err != nil || res.Dispatched {
		t.Errorf("done entry: res=%+v err=%v; want no dispatch and no error", res, err)
	}
}

// TestDispatchPayloadCarriesIdNotDocuments: AC2 (sty_47d31300) — the dispatch
// payload is the fetch HANDLE (the item record with its id), not a PUSH of
// documents or the ledger. The agent pulls those by id; they are never marshalled
// into stdin.
func TestDispatchPayloadCarriesIdNotDocuments(t *testing.T) {
	docs := fakeDocs{workflow: dispatchWF, skillBody: "rubric", skillFound: true}
	g, _ := newEngine(t, "", docs)
	fr := &fakeRunner{out: "ok"}
	g.newRunner = func(string, string) (agentcli.Runner, error) { return fr, nil }
	g.SetNamedAgents(func(string) (config.AgentBinding, bool) {
		return config.AgentBinding{Command: "fake -p {system}", Tools: "Read,Bash(satelle:*)"}, true
	})
	if _, err := g.DispatchExecutor(context.Background(), workitem.Item{ID: "sty_pull1", Status: "backlog", Title: "T"}, "plan"); err != nil {
		t.Fatal(err)
	}
	p := fr.got.Payload
	if !strings.Contains(p, `"sty_pull1"`) {
		t.Errorf("payload must carry the story id as the fetch handle: %s", p)
	}
	for _, forbidden := range []string{`"documents"`, `"ledger"`, `"attachments"`, `"plan_body"`} {
		if strings.Contains(p, forbidden) {
			t.Errorf("payload must NOT push %s (pull-context, not push): %s", forbidden, p)
		}
	}
}

// streamingFakeRunner emits normalized events DURING Run, standing in for a real
// subprocess that streams incremental output.
type streamingFakeRunner struct {
	lines []string
	got   agentcli.Request
}

func (s *streamingFakeRunner) Name() string    { return "streaming-fake" }
func (s *streamingFakeRunner) Command() string { return "streaming-fake" }
func (s *streamingFakeRunner) Run(_ context.Context, req agentcli.Request) ([]byte, error) {
	s.got = req
	for _, l := range s.lines {
		if req.OnEvent != nil {
			req.OnEvent(agentcli.Event{Kind: agentcli.EventMessage, Text: l})
		}
	}
	return []byte("ok"), nil
}

// TestDispatchExecutorWiresLiveSink pins normalized event logging.
func TestDispatchExecutorWiresLiveSink(t *testing.T) {
	dir := t.TempDir()
	docs := fakeDocs{workflow: dispatchWF, skillBody: "rubric", skillFound: true}
	g, _ := newEngine(t, "", docs)
	g.SetLogDir(dir, logfile.Config{})
	r := &streamingFakeRunner{lines: []string{"first line\n", "second line\n"}}
	g.SetNamedAgents(func(string) (config.AgentBinding, bool) {
		return config.AgentBinding{Command: "fake -p {system}", Tools: "Read,Bash(satelle:*)"}, true
	})
	g.newRunner = func(string, string) (agentcli.Runner, error) { return r, nil }

	if _, err := g.DispatchExecutor(context.Background(),
		workitem.Item{ID: "sty_1", Status: "backlog"}, "plan"); err != nil {
		t.Fatal(err)
	}
	if r.got.OnEvent == nil {
		t.Fatal("DispatchExecutor did not pass an OnEvent handler through to the runner's Request")
	}
	if r.got.Sink != nil {
		t.Fatal("raw Sink should be disabled unless SATELLE_AGENT_TRACE_RAW is opted in")
	}
	matches, err := filepath.Glob(filepath.Join(dir, "dispatch", "dispatch-*.log"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected exactly one dispatch log file, got %v (err %v)", matches, err)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "\tmessage\tfirst line") || !strings.Contains(string(data), "\tmessage\tsecond line") {
		t.Errorf("dispatch log missing streamed lines: %q", data)
	}
}

func TestDispatchExecutorRawTraceRequiresOptIn(t *testing.T) {
	t.Setenv("SATELLE_AGENT_TRACE_RAW", "1")
	dir := t.TempDir()
	docs := fakeDocs{workflow: dispatchWF, skillBody: "rubric", skillFound: true}
	g, _ := newEngine(t, "", docs)
	g.SetLogDir(dir, logfile.Config{})
	r := &streamingFakeRunner{lines: []string{"normalized"}}
	g.SetNamedAgents(func(string) (config.AgentBinding, bool) {
		return config.AgentBinding{Command: "fake -p {system}", Tools: "Read,Bash(satelle:*)"}, true
	})
	g.newRunner = func(string, string) (agentcli.Runner, error) { return r, nil }

	if _, err := g.DispatchExecutor(context.Background(),
		workitem.Item{ID: "sty_raw", Status: "backlog"}, "plan"); err != nil {
		t.Fatal(err)
	}
	if r.got.Sink == nil {
		t.Fatal("raw Sink should be present after explicit opt-in")
	}
	matches, err := filepath.Glob(filepath.Join(dir, "dispatch", "dispatch-*-raw.log"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected one opt-in raw log, got %v (err %v)", matches, err)
	}
}

// TestDispatchExecutorNoSinkWithoutLogDir pins AC2's other backward-compat half:
// when no log dir is configured (as in most engine tests, and any embedder that
// never called SetLogDir), the Request carries a nil Sink — the pre-existing
// callers are unaffected by this change.
func TestDispatchExecutorNoSinkWithoutLogDir(t *testing.T) {
	docs := fakeDocs{workflow: dispatchWF, skillBody: "rubric", skillFound: true}
	g, _ := newEngine(t, "", docs)
	r := &fakeRunner{out: "ok"}
	g.SetNamedAgents(func(string) (config.AgentBinding, bool) {
		return config.AgentBinding{Command: "fake -p {system}", Tools: "Read,Bash(satelle:*)"}, true
	})
	g.newRunner = func(string, string) (agentcli.Runner, error) { return r, nil }
	if _, err := g.DispatchExecutor(context.Background(),
		workitem.Item{ID: "sty_1", Status: "backlog"}, "plan"); err != nil {
		t.Fatal(err)
	}
	if r.got.Sink != nil {
		t.Error("no log dir is configured — the Request should carry a nil Sink")
	}
}

func TestGateRefusesUndeclaredEdge(t *testing.T) {
	// in_progress→integrated is NOT a declared edge in testWorkflow. The gate must
	// refuse it (error) so a story cannot skip a gate by jumping across an
	// undeclared edge — and the reviewer must not run.
	g, r := newEngine(t, `{"decision":"accept"}`,
		fakeDocs{workflow: testWorkflow, skillBody: "rubric", skillFound: true})
	_, err := g.Gate(context.Background(), workitem.Item{Status: "in_progress"}, "integrated")
	if err == nil {
		t.Fatal("expected an error refusing the undeclared in_progress→integrated edge")
	}
	if r.got.SystemPrompt != "" {
		t.Errorf("reviewer must not run on an undeclared edge")
	}
	// sty_39e2d9df AC3: the refusal carries the rule, why it applied here, and the
	// legal moves — the answer an operator used to get by opening the graph.
	var ref wfgovern.Refusal
	if !errors.As(err, &ref) {
		t.Fatalf("refusal must be a wfgovern.Refusal, got %T: %v", err, err)
	}
	if ref.Rule != wfgovern.RuleUndeclaredEdge {
		t.Errorf("rule = %q; want %q", ref.Rule, wfgovern.RuleUndeclaredEdge)
	}
	if ref.From != "in_progress" || ref.To != "integrated" {
		t.Errorf("refusal = %+v; want the refused edge named", ref)
	}
	if strings.TrimSpace(ref.Why) == "" {
		t.Error("a structured refusal must say WHY the rule applied here")
	}
	if len(ref.Alternatives) == 0 && ref.Remedy == "" {
		t.Error("a refusal must leave the operator somewhere to go: alternatives or a remedy")
	}
}

// TestParkResumeRefusalIsStructured (sty_39e2d9df AC3): leaving a park state for
// anywhere but the recorded origin refuses with the origin named as the legal
// move — the rule that would otherwise be invisible under a derived graph.
func TestParkResumeOriginlessWithLedgerIsUngated(t *testing.T) {
	parkWorkflow := spineWF("blocked", "cancelled", "",
		"in_progress|executor",
		"done")
	g, _ := newEngine(t, `{"decision":"accept"}`,
		fakeDocs{workflow: parkWorkflow, skillBody: "rubric", skillFound: true})
	// Empty column + last transition into blocked from a performing state.
	db := openEngineLedger(t)
	verb.SetLedgerStore(db)
	t.Cleanup(func() { verb.SetLedgerStore(nil) })
	_, err := db.Append(context.Background(), ledger.AppendInput{
		StoryID: "sty_7bc2de55", Kind: ledger.KindStatusTransition,
		Body: "in_progress → blocked",
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	dec, err := g.Gate(context.Background(),
		workitem.Item{ID: "sty_7bc2de55", Status: "blocked", ParkOrigin: ""}, "in_progress")
	if err != nil {
		t.Fatalf("origin-less park with ledger must resume: %v", err)
	}
	if dec.Gated {
		t.Error("resume-to-origin is ungated")
	}
}

func TestParkResumeOriginlessEmptyLedgerRefusesParkNotSkip(t *testing.T) {
	parkWorkflow := spineWF("blocked", "cancelled", "",
		"in_progress|executor",
		"done")
	g, _ := newEngine(t, `{"decision":"accept"}`,
		fakeDocs{workflow: parkWorkflow, skillBody: "rubric", skillFound: true})
	_, err := g.Gate(context.Background(),
		workitem.Item{ID: "sty_empty", Status: "blocked", ParkOrigin: ""}, "in_progress")
	if err == nil {
		t.Fatal("origin-less park with no ledger must refuse resume")
	}
	var ref wfgovern.Refusal
	if !errors.As(err, &ref) {
		t.Fatalf("got %T: %v", err, err)
	}
	if ref.Rule != wfgovern.RuleParkResume {
		t.Errorf("rule = %q; want park-resume, not skipped-step", ref.Rule)
	}
	if !strings.Contains(ref.Why, "no recorded origin") {
		t.Errorf("why = %q; want the origin-less wording", ref.Why)
	}
}

func TestParkResumeRefusalIsStructured(t *testing.T) {
	parkWorkflow := spineWF("blocked", "cancelled", "",
		"in_progress|executor",
		"release|executor",
		"done")
	g, _ := newEngine(t, `{"decision":"accept"}`,
		fakeDocs{workflow: parkWorkflow, skillBody: "rubric", skillFound: true})
	_, err := g.Gate(context.Background(),
		workitem.Item{ID: "sty_p", Status: "blocked", ParkOrigin: "in_progress"}, "release")
	if err == nil {
		t.Fatal("expected a refusal resuming from park to a state that is not the origin")
	}
	var ref wfgovern.Refusal
	if !errors.As(err, &ref) {
		t.Fatalf("refusal must be a wfgovern.Refusal, got %T: %v", err, err)
	}
	if ref.Rule != wfgovern.RuleParkResume {
		t.Errorf("rule = %q; want %q", ref.Rule, wfgovern.RuleParkResume)
	}
	if len(ref.Alternatives) == 0 || ref.Alternatives[0] != "in_progress" {
		t.Errorf("alternatives = %v; want the park origin first", ref.Alternatives)
	}
}

// The create check is ALWAYS gated (structure is the one thing satelle enforces
// on creation) and never depends on a rubric being installed or an agent runner.
func TestReviewCreateAlwaysGatedNoRunner(t *testing.T) {
	g, r := newEngine(t, "", fakeDocs{skillFound: false})
	dec, err := g.ReviewCreate(context.Background(), verb.CreateDraft{Kind: "story", Title: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Gated || dec.Accept {
		t.Errorf("want deterministic gated reject, got %+v", dec)
	}
	if r.got.SystemPrompt != "" {
		t.Errorf("create check must not run an agent")
	}
}

// validDraft is structurally conformant (clear goal + a numbered AC) so the
// content reviewer is reached.
var validDraft = verb.CreateDraft{Kind: "story", Title: "Add X", Body: "Make the thing do X", AcceptanceCriteria: "1. it does X", Category: "feature"}

// createWF is a wildcard workflow that DECLARES a content/alignment create
// reviewer via its create_review frontmatter (sty_b031b29f) — the binding lives on
// the workflow, not a hardcoded constant.
var createWF = func() string {
	// The create hook is workflow FRONTMATTER, and a derived route declares it on
	// its declaration of done (sty_9835070d).
	base := spineWF("", "", "", "in_progress|executor", "done")
	return strings.Replace(base, "scope = \"system\"\n",
		"scope = \"system\"\ncreate_review = \"my-create-review\"\n", 1)
}()

// plainWF is a wildcard workflow with NO create_review declaration.
var plainWF = spineWF("", "", "", "in_progress|executor", "done")

// When the active workflow declares create_review, a structurally-valid draft is
// judged by that reviewer; its reject blocks creation with the notes.
func TestReviewCreateContentReject(t *testing.T) {
	g, _ := newEngine(t, `{"decision":"reject","notes":"ACs do not match the goal"}`,
		fakeDocs{workflow: createWF, skillBody: "content/alignment rubric", skillFound: true})
	dec, err := g.ReviewCreate(context.Background(), validDraft)
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Gated || dec.Accept {
		t.Fatalf("want gated reject from content review, got %+v", dec)
	}
	if dec.Skill != "my-create-review" || dec.Notes == "" {
		t.Errorf("want reject by the workflow-declared skill with notes, got %+v", dec)
	}
}

// The declared reviewer's accept persists (structure + content both pass).
func TestReviewCreateContentAccept(t *testing.T) {
	g, _ := newEngine(t, `{"decision":"accept","notes":"aligned"}`,
		fakeDocs{workflow: createWF, skillBody: "content/alignment rubric", skillFound: true})
	dec, err := g.ReviewCreate(context.Background(), validDraft)
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Gated || !dec.Accept || dec.Skill != "my-create-review" {
		t.Fatalf("want gated accept by the workflow-declared skill, got %+v", dec)
	}
}

// Empty criteria are not a structural failure. With create_review declared, the
// content reviewer is reached (system prompt set). That is the regression that
// would hide a skill still treating absence as a reject; it is not the
// shipped-path accept.
func TestReviewCreateEmptyCriteriaReachesContentReviewer(t *testing.T) {
	g, r := newEngine(t, `{"decision":"accept","notes":"absence is readiness"}`,
		fakeDocs{workflow: createWF, skillBody: "content/alignment rubric", skillFound: true})
	draft := verb.CreateDraft{Kind: "story", Title: "Add X", Body: "Make the thing do X", Category: "feature"}
	dec, err := g.ReviewCreate(context.Background(), draft)
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Gated || !dec.Accept || dec.Skill != "my-create-review" {
		t.Fatalf("empty criteria must reach the declared content reviewer, got %+v", dec)
	}
	if r.got.SystemPrompt == "" {
		t.Fatal("content reviewer system prompt must be set when criteria are empty")
	}
}

// stepWF declares a step-summary node; stepWFOptional declares a non-mandatory
// one; the bare baselineWorkflow body (testWorkflow) declares none.
var stepWF = spineWF("", "",
	`[[gate]]
skill = "satelle-step-summary"
agent = "reviewer"
mandatory = true
`,
	"in_progress|executor",
	"done|||satelle-story-done-review")

var stepWFOptional = spineWF("", "",
	`[[gate]]
skill = "satelle-step-summary"
agent = "reviewer"
`,
	"in_progress|executor",
	"done|||satelle-story-done-review")

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestParseDecisionStrict(t *testing.T) {
	for _, in := range []string{`{"decision":"maybe"}`, `{"notes":"x"}`, ``, `no json`} {
		if _, err := parseDecision([]byte(in)); err == nil {
			t.Errorf("parseDecision(%q) should error", in)
		}
	}
}

func TestParseDecisionLenient(t *testing.T) {
	cases := []struct {
		in     string
		accept bool
		notes  string
	}{
		// prose around the verdict
		{`I judged it. {"decision":"accept","notes":""} Done.`, true, ""},
		// wrapping braces — the brittle case dogfooding hit
		{`{{"decision":"reject","notes":"add criteria"}}`, false, "add criteria"},
		// a code-fenced example before the real verdict
		{"```json\n{\"decision\": \"accept\"}\n```\nFinal: {\"decision\":\"reject\",\"notes\":\"no\"}", false, "no"},
		// a brace inside the notes string must not unbalance extraction
		{`{"decision":"reject","notes":"missing the {foo} block"}`, false, "missing the {foo} block"},
		// trailing object with no decision key must not steal the verdict
		// (historical typesafe_raw shape; sty_5f69cd89 / sty_e3eca0b7).
		{"{\"decision\":\"accept\",\"notes\":\"ok\",\"reasoning\":\"ok\"}\n{\"typesafe_raw\":{\"answers\":{\"verdict\":{\"choice\":\"reject\",\"confidence\":0.9}}}}", true, "ok"},
	}
	for _, c := range cases {
		dec, err := parseDecision([]byte(c.in))
		if err != nil {
			t.Errorf("parseDecision(%q): %v", c.in, err)
			continue
		}
		if dec.Accept != c.accept || dec.Notes != c.notes {
			t.Errorf("parseDecision(%q) = {accept:%v notes:%q}, want {accept:%v notes:%q}", c.in, dec.Accept, dec.Notes, c.accept, c.notes)
		}
	}
}

// webWorkflow is a category-specific workflow (applies_to: ["web"]) whose
// in_progress→done edge names a different reviewer than the baseline.
// categoryWF is one route declaring a `## web` section beside the wildcard: a
// category-specific lane is a SECTION now, not a second workflow file
// (sty_d953c5d8). Both lanes select from the same catalogue.
var categoryWF = wfDoc(
	`["*"]
obligations = ["raised", "coded", "closed"]

[web]
obligations = ["raised", "coded", "web-closed"]
`,
	`[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
agent = "executor"
requires = ["raised"]

[closed]
status = "done"
reviewers = ["satelle-story-done-review"]
terminal = true
requires = ["coded"]

[web-closed]
status = "done"
reviewers = ["satelle-web-done-review"]
terminal = true
requires = ["coded"]
`)

func TestActiveWorkflowSelectByCategory(t *testing.T) {
	docs := fakeDocs{
		workflow:   categoryWF,
		skillBody:  "rubric body",
		skillFound: true,
	}
	cases := []struct {
		name     string
		category string
		want     string
	}{
		{"specific category section wins", "web", "satelle-web-done-review"},
		{"no section falls back to the wildcard", "infra", "satelle-story-done-review"},
		{"empty category uses the wildcard", "", "satelle-story-done-review"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, _ := newEngine(t, `{"decision":"accept","notes":""}`, docs)
			dec, err := g.Gate(context.Background(),
				workitem.Item{ID: "sty_1", Status: "in_progress", Category: tc.category}, "done")
			if err != nil {
				t.Fatal(err)
			}
			if dec.Skill != tc.want {
				t.Errorf("category %q → skill %q, want %q", tc.category, dec.Skill, tc.want)
			}
		})
	}
}

func TestFrontmatterListForms(t *testing.T) {
	inline := frontmatterList("---\napplies_to: [\"*\", web]\n---\nx", "applies_to")
	if len(inline) != 2 || inline[0] != "*" || inline[1] != "web" {
		t.Fatalf("inline: %v", inline)
	}
	block := frontmatterList("---\nname: w\napplies_to:\n  - web\n  - infra\nother: y\n---\nx", "applies_to")
	if len(block) != 2 || block[1] != "infra" {
		t.Fatalf("block: %v", block)
	}
	if frontmatterList("no frontmatter", "applies_to") != nil {
		t.Fatalf("want nil without frontmatter")
	}
}

const checkSkill = "---\nname: satelle-story-integration-review\ntype: skill\ndescription: integration functional check\ncheck: \"run-the-suite\"\n---\n# Integration gate\nRuns the suite.\n"

func TestFunctionalCheckGate(t *testing.T) {
	// A workflow edge whose reviewer skill carries a `check:` runs deterministically.
	wf := spineWF("", "", "",
		"in_progress|executor",
		"integrated|executor||satelle-story-integration-review",
		"done")

	t.Run("pass accepts, agent not run", func(t *testing.T) {
		g, r := newEngine(t, `{"decision":"reject"}`, fakeDocs{workflow: wf, skillBody: checkSkill, skillFound: true})
		var ran string
		g.check = func(_ context.Context, dir, command, payload string) (string, error) {
			ran = command
			if !strings.Contains(payload, `"to":"integrated"`) {
				t.Errorf("check payload missing the transition: %q", payload)
			}
			return "ok\n", nil
		}
		dec, err := g.Gate(context.Background(), workitem.Item{Status: "in_progress"}, "integrated")
		if err != nil {
			t.Fatal(err)
		}
		if !dec.Gated || !dec.Accept {
			t.Fatalf("want gated accept, got %+v", dec)
		}
		if ran != "run-the-suite" {
			t.Errorf("check command = %q, want run-the-suite", ran)
		}
		if r.got.SystemPrompt != "" {
			t.Errorf("LLM reviewer must not run for a functional-check gate")
		}
	})

	// A passing check keeps its bounded stdout in the verdict notes — generic,
	// for every check — while a silent check keeps the one-line notes.
	t.Run("TestPassingCheckNotesCarryScriptOutput", func(t *testing.T) {
		run := func(t *testing.T, out string) string {
			t.Helper()
			g, _ := newEngine(t, ``, fakeDocs{workflow: wf, skillBody: checkSkill, skillFound: true})
			g.check = func(_ context.Context, dir, command, payload string) (string, error) { return out, nil }
			dec, err := g.Gate(context.Background(), workitem.Item{Status: "in_progress"}, "integrated")
			if err != nil {
				t.Fatal(err)
			}
			if !dec.Accept {
				t.Fatalf("want accept, got %+v", dec)
			}
			return dec.Notes
		}
		notes := run(t, "COMPARABLES sty_x ratio 0.43\n")
		if !strings.Contains(notes, "functional check passed") || !strings.Contains(notes, "COMPARABLES sty_x ratio 0.43") {
			t.Errorf("accept notes must carry the passed prefix and the script output: %q", notes)
		}
		if silent := run(t, "  \n"); strings.Contains(silent, "\n") {
			t.Errorf("a silent check should keep one-line notes: %q", silent)
		}
	})

	t.Run("fail rejects with output tail", func(t *testing.T) {
		g, _ := newEngine(t, ``, fakeDocs{workflow: wf, skillBody: checkSkill, skillFound: true})
		g.check = func(_ context.Context, dir, command, payload string) (string, error) {
			return "FAIL tests\n2 failures\n", errFakeExit
		}
		dec, err := g.Gate(context.Background(), workitem.Item{Status: "in_progress"}, "integrated")
		if err != nil {
			t.Fatal(err)
		}
		if !dec.Gated || dec.Accept {
			t.Fatalf("want gated reject, got %+v", dec)
		}
		if !strings.Contains(dec.Notes, "2 failures") {
			t.Errorf("reject notes should carry the check output tail, got %q", dec.Notes)
		}
	})

	t.Run("fail routes output through the wired log compressor (sty_ef930f81)", func(t *testing.T) {
		g, _ := newEngine(t, ``, fakeDocs{workflow: wf, skillBody: checkSkill, skillFound: true})
		g.check = func(_ context.Context, dir, command, payload string) (string, error) {
			return "raw check output\n", errFakeExit
		}
		var gotItemID, gotLog string
		g.SetCheckLogCompressor(func(_ context.Context, itemID, log string) string {
			gotItemID, gotLog = itemID, log
			return "COMPRESSED NOTES"
		})
		dec, err := g.Gate(context.Background(), workitem.Item{ID: "sty_abc", Status: "in_progress"}, "integrated")
		if err != nil {
			t.Fatal(err)
		}
		if dec.Accept {
			t.Fatalf("want reject, got %+v", dec)
		}
		if !strings.Contains(dec.Notes, "COMPRESSED NOTES") {
			t.Errorf("reject notes should be the wired compressor's output, got %q", dec.Notes)
		}
		if gotItemID != "sty_abc" {
			t.Errorf("compressor itemID = %q, want sty_abc", gotItemID)
		}
		if gotLog != "raw check output\n" {
			t.Errorf("compressor log = %q, want the raw check output", gotLog)
		}
	})

	t.Run("pass leaves notes untouched by any wired compressor", func(t *testing.T) {
		g, _ := newEngine(t, `{"decision":"reject"}`, fakeDocs{workflow: wf, skillBody: checkSkill, skillFound: true})
		g.check = func(_ context.Context, dir, command, payload string) (string, error) {
			return "ok\n", nil
		}
		called := false
		g.SetCheckLogCompressor(func(_ context.Context, itemID, log string) string {
			called = true
			return "should not be used"
		})
		dec, err := g.Gate(context.Background(), workitem.Item{Status: "in_progress"}, "integrated")
		if err != nil {
			t.Fatal(err)
		}
		if !dec.Accept {
			t.Fatalf("want accept, got %+v", dec)
		}
		if called {
			t.Errorf("a passing check must never invoke the log compressor")
		}
		if !strings.Contains(dec.Notes, "functional check passed") {
			t.Errorf("passing notes unexpectedly changed: %q", dec.Notes)
		}
	})
}

// sty_e2b78a8d: a functional check's verdict notes carry the check's own output
// and a short label — never the script body, which stays readable in the skill.
func TestFunctionalCheckNotes_CarryOutputNotScript(t *testing.T) {
	const sentinel = "SCRIPT-SENTINEL-77c1"
	wf := spineWF("", "", "",
		"in_progress|executor",
		"integrated|executor||satelle-story-integration-review",
		"done")
	engine := func(t *testing.T, script string) *Engine {
		body := "---\nname: satelle-story-integration-review\ntype: skill\ndescription: integration functional check\n---\n# Gate\n\n```check\n# " +
			sentinel + "\n" + script + "\n```\n"
		g, _ := newEngine(t, ``, fakeDocs{workflow: wf, skillBody: body, skillFound: true})
		g.repoRoot = t.TempDir() // the check runs in the repo root — use a real dir
		return g
	}

	t.Run("accept: label, skill and the check's result lines", func(t *testing.T) {
		g := engine(t, "echo ok-line")
		dec, err := g.Gate(context.Background(), workitem.Item{Status: "in_progress"}, "integrated")
		if err != nil {
			t.Fatal(err)
		}
		if !dec.Accept {
			t.Fatalf("want accept, got %+v", dec)
		}
		for _, want := range []string{"functional check passed", "satelle-story-integration-review", "ok-line"} {
			if !strings.Contains(dec.Notes, want) {
				t.Errorf("accept notes lack %q: %q", want, dec.Notes)
			}
		}
		if strings.Contains(dec.Notes, sentinel) || strings.Contains(dec.Notes, "echo ok-line") {
			t.Errorf("accept notes carry the check script: %q", dec.Notes)
		}
	})

	t.Run("accept of a silent check is the label alone", func(t *testing.T) {
		g := engine(t, "true")
		dec, err := g.Gate(context.Background(), workitem.Item{Status: "in_progress"}, "integrated")
		if err != nil {
			t.Fatal(err)
		}
		if want := "functional check passed (satelle-story-integration-review)"; dec.Notes != want {
			t.Errorf("notes = %q, want %q", dec.Notes, want)
		}
	})

	t.Run("reject: exit code, the full failure output, no script", func(t *testing.T) {
		g := engine(t, "echo first-failure; echo second-failure; echo third-failure >&2; exit 3")
		dec, err := g.Gate(context.Background(), workitem.Item{Status: "in_progress"}, "integrated")
		if err != nil {
			t.Fatal(err)
		}
		if dec.Accept {
			t.Fatalf("want reject, got %+v", dec)
		}
		for _, want := range []string{"functional check failed", "exit 3", "first-failure", "second-failure", "third-failure"} {
			if !strings.Contains(dec.Notes, want) {
				t.Errorf("reject notes lack %q: %q", want, dec.Notes)
			}
		}
		if strings.Contains(dec.Notes, sentinel) || strings.Contains(dec.Notes, "echo first-failure") {
			t.Errorf("reject notes carry the check script: %q", dec.Notes)
		}
	})
}

var errFakeExit = errFake("exit status 1")

type errFake string

func (e errFake) Error() string { return string(e) }

func TestBodyCheckBlock_selfContained(t *testing.T) {
	// A skill carrying an embedded ```check block is self-contained — no external
	// file. skillCheck must extract the block (preferred over a frontmatter check).
	body := "---\nname: x\ncheck: \"frontmatter-fallback\"\n---\n# Gate\n\n```check\n#!/usr/bin/env bash\nset -e\necho hello\n```\n\ntrailing prose\n"
	got := skillCheck(body)
	if got == "frontmatter-fallback" {
		t.Fatal("body ```check block should win over frontmatter check:")
	}
	if !strings.Contains(got, "echo hello") || strings.Contains(got, "```") {
		t.Fatalf("extracted block wrong: %q", got)
	}
	// No block → frontmatter fallback still works.
	if skillCheck("---\ncheck: \"only-frontmatter\"\n---\nbody") != "only-frontmatter" {
		t.Fatal("frontmatter check: fallback broken")
	}
}

func TestOrderedWorkflowsPriority(t *testing.T) {
	sysWild := docindex.Doc{Name: "satelle-baseline-workflow", Embedded: true,
		Body: "---\nscope: system\napplies_to: [\"*\"]\n---\n"}
	repoWild := docindex.Doc{Name: "satelle-workflow", Embedded: false,
		Body: "---\nscope: project\napplies_to: [\"*\"]\n---\n"}
	repoSpec := docindex.Doc{Name: "satelle-web-workflow", Embedded: false,
		Body: "---\nscope: project\napplies_to: [\"web\"]\n---\n"}
	all := []docindex.Doc{sysWild, repoWild, repoSpec}

	// No category: project wildcard beats the embedded system default.
	got := OrderedWorkflows(all, "")
	if len(got) != 2 || got[0].Name != "satelle-workflow" || got[1].Name != "satelle-baseline-workflow" {
		t.Fatalf("wildcard order = %v, want [satelle-workflow, satelle-baseline-workflow]", names(got))
	}
	// Category 'web': the category-specific repo workflow leads.
	got = OrderedWorkflows(all, "web")
	if got[0].Name != "satelle-web-workflow" {
		t.Fatalf("category-web head = %s, want satelle-web-workflow", got[0].Name)
	}
}

// TestExecutionResolvesToTaskExecutionWorkflow asserts the kind-aware resolution
// (sty_ef08ce2a, extended by sty_3c1a2a9d): an execution resolves by its KIND
// ("execution") and a task HEADER by its kind ("task") to a workflow declaring
// applies_to:["execution","task"], and NEITHER falls through to the wildcard
// story workflow — a header's authored category ("substrate", "docs", …) is
// never the resolution key. A story still resolves by category to the wildcard.
func TestExecutionResolvesToTaskExecutionWorkflow(t *testing.T) {
	storyWild := docindex.Doc{Name: "satelle-project-workflow", Embedded: false,
		Body: "---\nscope: project\napplies_to: [\"*\"]\n---\n"}
	taskExec := docindex.Doc{Name: "satelle-task-workflow", Embedded: false,
		Body: "---\nscope: project\napplies_to: [\"execution\", \"task\"]\n---\n"}
	all := []docindex.Doc{storyWild, taskExec}

	// The resolution key for an execution is its kind, not its (empty) category.
	if got := workflowCategory(workitem.Item{Kind: workitem.KindExecution}); got != "execution" {
		t.Fatalf("workflowCategory(execution) = %q, want \"execution\"", got)
	}
	// The resolution key for a task header is its kind, not its authored category.
	if got := workflowCategory(workitem.Item{Kind: workitem.KindTask, Category: "substrate"}); got != "task" {
		t.Fatalf("workflowCategory(task) = %q, want \"task\"", got)
	}
	// An execution resolves to the task-execution workflow, not the story wildcard.
	got := OrderedWorkflows(all, workflowCategory(workitem.Item{Kind: workitem.KindExecution}))
	if len(got) == 0 || got[0].Name != "satelle-task-workflow" {
		t.Fatalf("execution head = %v, want satelle-task-workflow (not the story workflow)", names(got))
	}
	// An unstamped task header ALSO resolves to the task workflow (sty_3c1a2a9d):
	// its authored category matches no workflow, and falling through to the
	// wildcard story workflow was the misrouting that ran headers through story gates.
	tk := OrderedWorkflows(all, workflowCategory(workitem.Item{Kind: workitem.KindTask, Category: "substrate"}))
	if len(tk) == 0 || tk[0].Name != "satelle-task-workflow" {
		t.Fatalf("task-header head = %v, want satelle-task-workflow (not the story workflow)", names(tk))
	}
	// A story keeps resolving by category to the wildcard project workflow.
	sk := OrderedWorkflows(all, workflowCategory(workitem.Item{Kind: workitem.KindStory, Category: "feature"}))
	if len(sk) == 0 || sk[0].Name != "satelle-project-workflow" {
		t.Fatalf("story head = %v, want satelle-project-workflow", names(sk))
	}
}

// TestOrderedWorkflowsParentCategories asserts a category-specific repo workflow
// listing several categories (applies_to ["epic-parent","parent"]) leads for EACH
// of them, overriding the project wildcard — the selection that makes
// satelle-parent-workflow the active lifecycle for container stories.
func TestOrderedWorkflowsParentCategories(t *testing.T) {
	repoWild := docindex.Doc{Name: "satelle-project-workflow", Embedded: false,
		Body: "---\nscope: project\napplies_to: [\"*\"]\n---\n"}
	repoParent := docindex.Doc{Name: "satelle-parent-workflow", Embedded: false,
		Body: "---\nscope: project\napplies_to: [\"epic-parent\", \"parent\"]\n---\n"}
	all := []docindex.Doc{repoWild, repoParent}

	for _, cat := range []string{"epic-parent", "parent"} {
		got := OrderedWorkflows(all, cat)
		if len(got) == 0 || got[0].Name != "satelle-parent-workflow" {
			t.Errorf("category %q head = %v, want satelle-parent-workflow first", cat, names(got))
		}
	}
	// A non-container category still resolves to the wildcard project workflow.
	if got := OrderedWorkflows(all, "feature"); len(got) == 0 || got[0].Name != "satelle-project-workflow" {
		t.Errorf("category feature head = %v, want satelle-project-workflow", names(got))
	}
}

func names(ds []docindex.Doc) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.Name
	}
	return out
}

var dotWF = spineWF("", "", "",
	"in_progress|executor",
	"committed|||satelle-commit-push-reviewer",
	"done")

func TestReviewerSkillsForDOT(t *testing.T) {
	spec := fixtureSpec(t, dotWF)
	skills, model, _, declared, _, _ := specReviewerSkills(spec, "in_progress", "committed")
	if !declared || len(skills) != 1 || skills[0] != "satelle-commit-push-reviewer" || model != "" {
		t.Fatalf("in_progress->committed: skills=%v model=%q declared=%v", skills, model, declared)
	}
	if _, _, _, declared, _, _ := specReviewerSkills(spec, "in_progress", "nope"); declared {
		t.Errorf("an undeclared edge should report declared=false")
	}
	if skills, _, _, declared, _, _ := specReviewerSkills(spec, "committed", "done"); !declared || len(skills) != 0 {
		t.Errorf("committed->done should be declared and ungated: skills=%v declared=%v", skills, declared)
	}
}

// A container close gate is judged from the children SATELLE injects into the
// payload (resolved from the DB), not any on-disk story mirror (sty_fa1e02e1).
func TestGatePayloadIncludesChildren(t *testing.T) {
	g, r := newEngine(t, `{"decision":"accept"}`, fakeDocs{workflow: testWorkflow, skillBody: "rubric", skillFound: true})
	g.SetChildrenResolver(func(_ context.Context, item workitem.Item) []ChildState {
		if item.ID != "sty_parent" {
			t.Errorf("resolver called with %q, want sty_parent", item.ID)
		}
		return []ChildState{{ID: "sty_child1", Status: "done"}, {ID: "sty_child2", Status: "in_progress"}}
	})
	if _, err := g.Gate(context.Background(), workitem.Item{ID: "sty_parent", Status: "in_progress", Category: "epic-parent"}, "done"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.got.Payload, `"children"`) || !strings.Contains(r.got.Payload, "sty_child2") {
		t.Errorf("close-gate payload must carry the children:\n%s", r.got.Payload)
	}
}

// TestGatePayloadIncludesDocs (sty_58fa970e): attachments ride in the payload
// so a Bash-less reviewer can judge without any disk path.
func TestGatePayloadIncludesDocs(t *testing.T) {
	g, r := newEngine(t, `{"decision":"accept"}`, fakeDocs{workflow: testWorkflow, skillBody: "rubric", skillFound: true})
	g.SetDocsResolver(func(_ context.Context, itemID string) []DocState {
		if itemID != "sty_docs" {
			t.Errorf("docs resolver itemID = %q", itemID)
		}
		return []DocState{
			{Name: "plan", Type: "plan", Body: "# plan body for AC1"},
			{Name: "step-summary-x", Type: "step-summary", Body: "did the thing"},
		}
	})
	if _, err := g.Gate(context.Background(), workitem.Item{ID: "sty_docs", Status: "in_progress"}, "done"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"docs"`, `"name":"plan"`, `"path":`} {
		if !strings.Contains(r.got.Payload, want) {
			t.Errorf("payload missing %q:\n%s", want, r.got.Payload)
		}
	}
	if strings.Contains(r.got.Payload, "plan body for AC1") {
		t.Error("the plan body must be a file, not an inline docs body")
	}
	docs := openedDocs(t, r.got.Payload, r.opened)
	if docs["plan"] != "# plan body for AC1" || docs["step-summary-x"] != "did the thing" {
		t.Errorf("doc files = %#v", docs)
	}
}

// planEdgeWorkflow gates plan → in_progress, the edge sty_0f5e600c's prior-verdict
// injection is authored against.
var planEdgeWorkflow = spineWF("", "cancelled", "",
	"plan|||satelle-story-intent-review",
	"in_progress|executor||satelle-story-plan-review",
	"done|||satelle-story-done-review")

// TestPriorVerdictFromCarriesStaleness (sty_0225fc2f): the converter copies the
// stale marker and the evidence list, and a fresh verdict keeps its quotation.
func TestPriorVerdictFromCarriesStaleness(t *testing.T) {
	stale := PriorVerdictFrom(verb.PriorVerdict{
		Skill: "s", Decision: "reject", ReviewedStale: true,
		EvidenceSince: []verb.EvidenceSince{{Kind: "telemetry_event", Event: "plan-consumed", CreatedAt: "2026-10-11T00:00:00Z"}},
	})
	if !stale.ReviewedStale || stale.Reviewed != "" || len(stale.EvidenceSince) != 1 || stale.EvidenceSince[0].Event != "plan-consumed" {
		t.Fatalf("stale conversion = %+v", stale)
	}
	raw, err := json.Marshal(stale)
	if err != nil || !strings.Contains(string(raw), `"reviewed_stale":true`) || !strings.Contains(string(raw), `"evidence_since"`) {
		t.Fatalf("payload json = %s (%v)", raw, err)
	}
	fresh := PriorVerdictFrom(verb.PriorVerdict{Skill: "s", Decision: "reject", Reviewed: "words"})
	raw, _ = json.Marshal(fresh)
	if fresh.Reviewed != "words" || strings.Contains(string(raw), "reviewed_stale") || strings.Contains(string(raw), "evidence_since") {
		t.Fatalf("fresh conversion = %s", raw)
	}
}

// TestGatePayloadIncludesPriorVerdicts (sty_0f5e600c AC1/AC2): the edge's earlier
// verdicts ride the gate payload oldest-first, numbered from 1 over the FULL
// history and windowed to the most recent five; a first attempt carries no key at
// all, and the resolver is asked for THIS edge only.
func TestGatePayloadIncludesPriorVerdicts(t *testing.T) {
	verdict := func(i int, decision string) PriorVerdict {
		return PriorVerdict{
			Skill:     "satelle-story-plan-review",
			Decision:  decision,
			Notes:     fmt.Sprintf("PV-NOTE-%d", i),
			CreatedAt: fmt.Sprintf("2026-08-24T0%d:00:00Z", i),
		}
	}
	gateOnce := func(t *testing.T, resolver func(ctx context.Context, itemID, from, to string, ex verb.StaleExemption) []PriorVerdict) (string, map[string][]byte) {
		t.Helper()
		g, r := newEngine(t, `{"decision":"accept"}`, fakeDocs{workflow: planEdgeWorkflow, skillBody: "rubric", skillFound: true})
		if resolver != nil {
			g.SetPriorVerdictsResolver(resolver)
		}
		if _, err := g.Gate(context.Background(), workitem.Item{ID: "sty_pv", Status: "plan"}, "in_progress"); err != nil {
			t.Fatal(err)
		}
		return r.got.Payload, r.opened
	}

	t.Run("three rejects then an accept ride in order", func(t *testing.T) {
		var gotFrom, gotTo, gotID string
		history := []PriorVerdict{verdict(1, "reject"), verdict(2, "reject"), verdict(3, "reject"), verdict(4, "accept")}
		payload, opened := gateOnce(t, func(_ context.Context, itemID, from, to string, _ verb.StaleExemption) []PriorVerdict {
			gotID, gotFrom, gotTo = itemID, from, to
			return history
		})
		// AC2 (unit half): the engine asks for the edge under review, so a
		// resolver filtering on from/to cannot be handed another edge's verdicts.
		if gotID != "sty_pv" || gotFrom != "plan" || gotTo != "in_progress" {
			t.Errorf("resolver called with (%q, %q, %q), want (sty_pv, plan, in_progress)", gotID, gotFrom, gotTo)
		}
		if !strings.Contains(payload, `"prior_verdicts"`) {
			t.Fatalf("payload missing prior_verdicts:\n%s", payload)
		}
		file := string(openedAt(t, opened, payload, "prior_verdicts"))
		var last int
		for i := 1; i <= 4; i++ {
			at := strings.Index(file, fmt.Sprintf("PV-NOTE-%d", i))
			if at < 0 {
				t.Fatalf("file missing PV-NOTE-%d:\n%s", i, file)
			}
			if at < last {
				t.Errorf("PV-NOTE-%d is out of order (oldest first expected)", i)
			}
			last = at
		}
		for _, want := range []string{`"attempt":1`, `"attempt":4`, `"decision":"reject"`, `"decision":"accept"`, `"skill":"satelle-story-plan-review"`, `"created_at":"2026-08-24T01:00:00Z"`} {
			if !strings.Contains(file, want) {
				t.Errorf("file missing %q:\n%s", want, file)
			}
		}
	})

	t.Run("the file keeps the full history; the check stdin keeps the window", func(t *testing.T) {
		all := make([]PriorVerdict, 0, 8)
		for i := 1; i <= 8; i++ {
			all = append(all, verdict(i, "reject"))
		}
		payload, opened := gateOnce(t, func(_ context.Context, _, _, _ string, _ verb.StaleExemption) []PriorVerdict { return all })
		file := string(openedAt(t, opened, payload, "prior_verdicts"))
		for i := 1; i <= 8; i++ {
			if !strings.Contains(file, fmt.Sprintf("PV-NOTE-%d", i)) {
				t.Errorf("verdict %d missing from the full file", i)
			}
		}
		if !strings.Contains(file, `"attempt":1`) || !strings.Contains(file, `"attempt":8`) {
			t.Errorf("attempts must be numbered over the full history:\n%s", file)
		}
		stdin := cappedGateStdin(t, planEdgeWorkflow, workitem.Item{ID: "sty_pv", Status: "plan"}, "in_progress", func(g *Engine) {
			g.SetPriorVerdictsResolver(func(context.Context, string, string, string, verb.StaleExemption) []PriorVerdict { return all })
		})
		for i := 1; i <= 3; i++ {
			if strings.Contains(stdin, fmt.Sprintf("PV-NOTE-%d", i)) {
				t.Errorf("verdict %d is outside the most-recent-5 window but rode the check stdin", i)
			}
		}
		for i := 4; i <= 8; i++ {
			if !strings.Contains(stdin, fmt.Sprintf("PV-NOTE-%d", i)) {
				t.Errorf("verdict %d missing from the check window", i)
			}
		}
		for _, want := range []string{`"attempt":4`, `"attempt":8`} {
			if !strings.Contains(stdin, want) {
				t.Errorf("check stdin missing %q", want)
			}
		}
		if strings.Contains(stdin, `"attempt":1`) {
			t.Error("windowed verdicts must not be renumbered from 1")
		}
	})

	t.Run("first attempt carries no key", func(t *testing.T) {
		if payload, _ := gateOnce(t, nil); strings.Contains(payload, "prior_verdicts") {
			t.Errorf("unwired resolver must inject nothing:\n%s", payload)
		}
		empty, _ := gateOnce(t, func(_ context.Context, _, _, _ string, _ verb.StaleExemption) []PriorVerdict { return nil })
		if strings.Contains(empty, "prior_verdicts") {
			t.Errorf("a first attempt (no prior verdicts) must omit the key:\n%s", empty)
		}
	})
}

// TestPriorVerdictsBoundedAndDoNotStarveDocs (sty_0f5e600c AC3): a paragraph-long
// verdict is excerpted, and the prior-verdict budget is independent of
// docsPayloadCeiling — the plan a reviewer must judge is never starved by the
// verdicts about it.
func TestPriorVerdictsBoundedAndDoNotStarveDocs(t *testing.T) {
	const marker = "PVBIGNOTE"
	huge := strings.Repeat(marker, (20<<10)/len(marker)) // ~20 KiB
	planBody := "PVPLANMARKER plan body the reviewer must still receive in full"

	g, r := newEngine(t, `{"decision":"accept"}`, fakeDocs{workflow: planEdgeWorkflow, skillBody: "rubric", skillFound: true})
	g.SetDocsResolver(func(_ context.Context, _ string) []DocState {
		return []DocState{{Name: "plan", Type: "plan", Body: planBody}}
	})
	g.SetPriorVerdictsResolver(func(_ context.Context, _, _, _ string, _ verb.StaleExemption) []PriorVerdict {
		return []PriorVerdict{{Skill: "satelle-story-plan-review", Decision: "reject", Notes: huge}}
	})
	if _, err := g.Gate(context.Background(), workitem.Item{ID: "sty_pvbig", Status: "plan"}, "in_progress"); err != nil {
		t.Fatal(err)
	}
	payload := r.got.Payload
	docs := openedDocs(t, payload, r.opened)
	if docs["plan"] != planBody {
		t.Errorf("plan file = %q, want the full body", docs["plan"])
	}
	file := string(openedAt(t, r.opened, payload, "prior_verdicts"))
	if !strings.Contains(file, huge) {
		t.Error("the verdict file must keep the full note")
	}
	if strings.Contains(payload, huge) {
		t.Error("the work item must not carry the note inline")
	}
	stdin := cappedGateStdin(t, planEdgeWorkflow, workitem.Item{ID: "sty_pvbig", Status: "plan"}, "in_progress", func(g *Engine) {
		g.SetDocsResolver(func(context.Context, string) []DocState {
			return []DocState{{Name: "plan", Type: "plan", Body: planBody}}
		})
		g.SetPriorVerdictsResolver(func(context.Context, string, string, string, verb.StaleExemption) []PriorVerdict {
			return []PriorVerdict{{Skill: "satelle-story-plan-review", Decision: "reject", Notes: huge}}
		})
	})
	if !strings.Contains(stdin, planBody) {
		t.Error("the check stdin must still carry the plan body")
	}
	if strings.Contains(stdin, `"name":"plan"`) && strings.Contains(stdin, `"truncated":true`) && !strings.Contains(stdin, planBody) {
		t.Error("prior verdicts must not consume the docs budget")
	}
	if !strings.Contains(stdin, "[truncated]") {
		t.Error("an over-cap note must be marked as excerpted on the check stdin")
	}
	if got, cap := strings.Count(stdin, marker), (priorVerdictNotesCeiling/len(marker))+1; got > cap {
		t.Errorf("note excerpt carries %d markers, want at most %d (%d-byte cap)", got, cap, priorVerdictNotesCeiling)
	}
}

// TestBinaryExcludedFromGatePayload (sty_40e5a305 AC4/AC5): a binary larger
// than docsPayloadCeiling must not starve plan/step-summary, and neither raw
// bytes nor their base64 may appear in the serialized gate payload.
func TestBinaryExcludedFromGatePayload(t *testing.T) {
	plant := []byte("BIN40E5A305-PAYLOAD-POISON-MARKER\x00\xff")
	big := make([]byte, 300<<10) // 300 KiB > 128 KiB ceiling
	copy(big, plant)
	// Distinctive ASCII substring (plant without the binary tail) for payload scan.
	plantASCII := "BIN40E5A305-PAYLOAD-POISON-MARKER"

	planBody := "# plan body for binary payload test"
	summaryBody := "step summary must remain fully present"

	run := func(t *testing.T, order []DocState) (string, map[string][]byte) {
		t.Helper()
		g, r := newEngine(t, `{"decision":"accept"}`, fakeDocs{workflow: testWorkflow, skillBody: "rubric", skillFound: true})
		g.SetDocsResolver(func(_ context.Context, itemID string) []DocState {
			return order
		})
		if _, err := g.Gate(context.Background(), workitem.Item{ID: "sty_bin", Status: "in_progress"}, "done"); err != nil {
			t.Fatal(err)
		}
		return r.got.Payload, r.opened
	}

	binDoc := DocState{
		Name: "shot.png", Type: "screenshot", Binary: true,
		ContentType: "image/png", Size: int64(len(big)),
		// Even if a buggy resolver inlined the body, the engine must drop it.
		Body: string(big),
	}
	planDoc := DocState{Name: "plan", Type: "plan", Body: planBody}
	sumDoc := DocState{Name: "step-summary-x", Type: "step-summary", Body: summaryBody}

	// Binary first — skip must continue, not break.
	payload, opened := run(t, []DocState{binDoc, planDoc, sumDoc})
	docs := openedDocs(t, payload, opened)
	if docs["plan"] != planBody {
		t.Error("plan file must be the full body when binary is listed first")
	}
	if docs["step-summary-x"] != summaryBody {
		t.Error("step-summary file must be the full body when binary is listed first")
	}
	if strings.Contains(payload, plantASCII) {
		t.Error("raw binary plant must not appear in gate payload")
	}
	if strings.Contains(payload, `"name":"shot.png"`) {
		t.Error("binary doc must not be listed in gate payload docs (excluded like type:change)")
	}
	if _, ok := docs["shot.png"]; ok {
		t.Error("binary doc must not be written as reviewer material")
	}

	// Binary last — same guarantees.
	payload2, opened2 := run(t, []DocState{planDoc, sumDoc, binDoc})
	docs2 := openedDocs(t, payload2, opened2)
	if docs2["plan"] != planBody || docs2["step-summary-x"] != summaryBody {
		t.Error("plan and step-summary must remain when binary is listed last")
	}
	if strings.Contains(payload2, plantASCII) {
		t.Error("raw binary plant must not appear (binary last)")
	}
}

// TestChangeRecordExcludedFromGatePayload (sty_948ad5df AC4): type:change
// patches are disk retention only and must not ride the gate docs payload.
func TestChangeRecordExcludedFromGatePayload(t *testing.T) {
	g, r := newEngine(t, `{"decision":"accept"}`, fakeDocs{workflow: testWorkflow, skillBody: "rubric", skillFound: true})
	g.SetDocsResolver(func(_ context.Context, itemID string) []DocState {
		return []DocState{
			{Name: "plan", Type: "plan", Body: "# plan body"},
			{Name: "change-in_progress-integration", Type: "change", Body: "diff --git a/secret.go\n+planted-secret-in-change-attachment"},
			{Name: "step-summary-x", Type: "step-summary", Body: "did the thing"},
		}
	})
	if _, err := g.Gate(context.Background(), workitem.Item{ID: "sty_change", Status: "in_progress"}, "done"); err != nil {
		t.Fatal(err)
	}
	docs := openedDocs(t, r.got.Payload, r.opened)
	if docs["plan"] != "# plan body" {
		t.Error("plan doc must still be handed to the reviewer")
	}
	if strings.Contains(r.got.Payload, "planted-secret-in-change-attachment") {
		t.Error("type:change body must not appear in gate payload")
	}
	if strings.Contains(r.got.Payload, `"name":"change-in_progress-integration"`) {
		t.Error("type:change doc must not be listed in gate payload docs")
	}
}

// TestGatePayloadIncludesDiffWhenBaselineExists (sty_a125b440 AC1): the
// engagement slice rides the gate payload when the resolver returns one.
func TestGatePayloadIncludesDiffWhenBaselineExists(t *testing.T) {
	g, r := newEngine(t, `{"decision":"accept"}`, fakeDocs{workflow: testWorkflow, skillBody: "rubric", skillFound: true})
	g.SetDiffResolver(func(_ context.Context, itemID string) *DiffState {
		if itemID != "sty_diff" {
			t.Errorf("diff resolver itemID = %q", itemID)
		}
		return &DiffState{
			Baseline: "abc123",
			Dirty:    true,
			Files:    []string{"foo.go", "foo_test.go"},
			Stat:     " 2 files changed, 10 insertions(+)",
			Patch:    "diff --git a/foo.go b/foo.go\n+hello",
			Note:     "enumeration only — no pass/fail; gates decide scope",
			Source:   "live",
		}
	})
	if _, err := g.Gate(context.Background(), workitem.Item{ID: "sty_diff", Status: "in_progress"}, "done"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"diff"`, `"baseline_sha":"abc123"`, `"baseline_dirty":true`,
		`"patch_path":`, `"source":"live"`,
	} {
		if !strings.Contains(r.got.Payload, want) {
			t.Errorf("payload missing %q:\n%s", want, r.got.Payload)
		}
	}
	if strings.Contains(r.got.Payload, `"no_baseline"`) {
		t.Error("present baseline must not set no_baseline")
	}
	if strings.Contains(r.got.Payload, `"patch":`) || strings.Contains(r.got.Payload, "foo.go") {
		t.Error("files, stat, and patch must not ride inline")
	}
	diffJSON := string(openedAt(t, r.opened, r.got.Payload, "diff"))
	for _, want := range []string{`"foo.go"`, `"foo_test.go"`, "2 files changed", "diff --git a/foo.go"} {
		if !strings.Contains(diffJSON, want) {
			t.Errorf("diff file missing %q:\n%s", want, diffJSON)
		}
	}
}

// TestGatePayloadDiffMarksNoBaseline (sty_a125b440 AC1): a resolver that found
// no baseline injects a marker, never omits in a way that looks like an
// unwired resolver, and never fails the gate.
func TestGatePayloadDiffMarksNoBaseline(t *testing.T) {
	g, r := newEngine(t, `{"decision":"accept"}`, fakeDocs{workflow: testWorkflow, skillBody: "rubric", skillFound: true})
	g.SetDiffResolver(func(_ context.Context, _ string) *DiffState {
		return &DiffState{NoBaseline: true, Note: "no engagement baseline recorded"}
	})
	dec, err := g.Gate(context.Background(), workitem.Item{ID: "sty_nobase", Status: "in_progress"}, "done")
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Accept {
		t.Fatalf("missing baseline must not refuse the gate: %+v", dec)
	}
	if !strings.Contains(r.got.Payload, `"no_baseline":true`) {
		t.Errorf("payload missing no_baseline marker:\n%s", r.got.Payload)
	}
	if strings.Contains(r.got.Payload, `"patch":`) {
		t.Error("no-baseline marker must not carry a patch")
	}
}

// TestGatePayloadDiffAdversarialPatchStillAccepts (sty_a125b440 AC1): a huge
// or hostile patch is truncated; the stub reviewer still accepts — the binary
// did not reject on diff contents.
func TestGatePayloadDiffAdversarialPatchStillAccepts(t *testing.T) {
	huge := strings.Repeat("A", diffPayloadCeiling+4096)
	g, r := newEngine(t, `{"decision":"accept"}`, fakeDocs{workflow: testWorkflow, skillBody: "rubric", skillFound: true})
	g.SetDiffResolver(func(_ context.Context, _ string) *DiffState {
		return &DiffState{Baseline: "deadbeef", Files: []string{"x.go"}, Patch: huge}
	})
	dec, err := g.Gate(context.Background(), workitem.Item{ID: "sty_adv", Status: "in_progress"}, "done")
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Accept {
		t.Fatalf("adversarial patch must not refuse the gate: %+v", dec)
	}
	if strings.Count(r.got.Payload, "A") > 16 {
		t.Error("the work item must not carry the patch")
	}
	diffJSON := openedAt(t, r.opened, r.got.Payload, "diff")
	if !bytes.Contains(diffJSON, []byte(huge)) {
		t.Error("the diff file must keep the full patch")
	}
	stdin := cappedGateStdin(t, testWorkflow, workitem.Item{ID: "sty_adv", Status: "in_progress"}, "done", func(g *Engine) {
		g.SetDiffResolver(func(context.Context, string) *DiffState {
			return &DiffState{Baseline: "deadbeef", Files: []string{"x.go"}, Patch: huge}
		})
	})
	if !strings.Contains(stdin, `"truncated":true`) {
		t.Error("the check stdin must still mark an over-ceiling patch truncated")
	}
	if strings.Count(stdin, "A") > diffPayloadCeiling+16 {
		t.Error("the check stdin patch still exceeds the ceiling")
	}
}

// TestDiffDoesNotConsumeDocsCeiling (sty_a125b440 AC2): a patch larger than
// docsPayloadCeiling must not starve the plan.
func TestDiffDoesNotConsumeDocsCeiling(t *testing.T) {
	planBody := "DIFFPLANMARKER plan body the reviewer must still receive in full"
	huge := strings.Repeat("P", docsPayloadCeiling+1024)
	g, r := newEngine(t, `{"decision":"accept"}`, fakeDocs{workflow: testWorkflow, skillBody: "rubric", skillFound: true})
	g.SetDocsResolver(func(_ context.Context, _ string) []DocState {
		return []DocState{{Name: "plan", Type: "plan", Body: planBody}}
	})
	g.SetDiffResolver(func(_ context.Context, _ string) *DiffState {
		return &DiffState{Baseline: "abc", Files: []string{"z.go"}, Patch: huge}
	})
	if _, err := g.Gate(context.Background(), workitem.Item{ID: "sty_diffdocs", Status: "in_progress"}, "done"); err != nil {
		t.Fatal(err)
	}
	docs := openedDocs(t, r.got.Payload, r.opened)
	if docs["plan"] != planBody {
		t.Errorf("plan file = %q, want the full body beside a huge patch", docs["plan"])
	}
	if !bytes.Contains(openedAt(t, r.opened, r.got.Payload, "diff"), []byte(huge)) {
		t.Error("the diff file must keep the full patch")
	}
	if strings.Contains(r.got.Payload, huge[:64]) {
		t.Error("the work item must not carry the patch")
	}
	stdin := cappedGateStdin(t, testWorkflow, workitem.Item{ID: "sty_diffdocs", Status: "in_progress"}, "done", func(g *Engine) {
		g.SetDocsResolver(func(context.Context, string) []DocState {
			return []DocState{{Name: "plan", Type: "plan", Body: planBody}}
		})
		g.SetDiffResolver(func(context.Context, string) *DiffState {
			return &DiffState{Baseline: "abc", Files: []string{"z.go"}, Patch: huge}
		})
	})
	if !strings.Contains(stdin, planBody) {
		t.Error("the check stdin must still carry the plan body")
	}
	if !strings.Contains(stdin, `"truncated":true`) {
		t.Error("the check stdin must still mark the over-cap patch truncated")
	}
}

// inventedEdgeWorkflow uses status names that are not this repo's spine, so
// injection cannot be accidentally keyed on integration/release/done.
var inventedEdgeWorkflow = spineWF("", "cancelled", "",
	"draft|executor||satelle-story-intent-review",
	"shipped|||satelle-story-done-review")

// TestDiffInjectedOnArbitraryEdge (sty_a125b440 AC2): fillDiff runs on the
// gate payload with no status-name switch.
func TestDiffInjectedOnArbitraryEdge(t *testing.T) {
	g, r := newEngine(t, `{"decision":"accept"}`, fakeDocs{workflow: inventedEdgeWorkflow, skillBody: "rubric", skillFound: true})
	g.SetDiffResolver(func(_ context.Context, _ string) *DiffState {
		return &DiffState{Baseline: "abc", Files: []string{"a.go"}, Patch: "+x"}
	})
	if _, err := g.Gate(context.Background(), workitem.Item{ID: "sty_invented", Status: "draft"}, "shipped"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.got.Payload, `"diff"`) || !strings.Contains(r.got.Payload, `"patch_path"`) {
		t.Errorf("diff must ride an invented-status edge:\n%s", r.got.Payload)
	}
	if !strings.Contains(string(openedAt(t, r.opened, r.got.Payload, "diff")), `"a.go"`) {
		t.Error("the diff file must carry the file list")
	}
}

// TestGatePayloadDiffUnwiredOmitsKey (sty_a125b440): an unwired resolver
// leaves the payload byte-identical on the diff key.
func TestGatePayloadDiffUnwiredOmitsKey(t *testing.T) {
	g, r := newEngine(t, `{"decision":"accept"}`, fakeDocs{workflow: testWorkflow, skillBody: "rubric", skillFound: true})
	if _, err := g.Gate(context.Background(), workitem.Item{ID: "sty_nodiff", Status: "in_progress"}, "done"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.got.Payload, `"diff"`) {
		t.Errorf("unwired resolver must inject no diff:\n%s", r.got.Payload)
	}
}

// diffOffloadStub is a minimal in-memory content store standing in for
// internal/retrieve.Store in the diff-compressor/backstop tests (sty_918e2086).
type diffOffloadStub struct {
	blobs map[string][]byte
}

func newDiffOffloadStub() *diffOffloadStub { return &diffOffloadStub{blobs: map[string][]byte{}} }

// diffPayloadProbe decodes just enough of a gate payload to inspect the
// injected diff — JSON-decoding rather than substring-matching the raw
// payload, since json.Marshal HTML-escapes the '<'/'>' in a retrieve marker.
type diffPayloadProbe struct {
	Diff struct {
		Files     []string `json:"files"`
		Stat      string   `json:"stat"`
		Patch     string   `json:"patch"`
		Truncated bool     `json:"truncated"`
	} `json:"diff"`
}

func decodeDiffProbe(t *testing.T, payload string) diffPayloadProbe {
	t.Helper()
	var p diffPayloadProbe
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		t.Fatalf("decode payload: %v\n%s", err, payload)
	}
	return p
}

// extractMarker pulls the first <<ccr:...>> marker out of s.
func extractMarker(t *testing.T, s string) string {
	t.Helper()
	start := strings.Index(s, "<<ccr:")
	if start < 0 {
		t.Fatalf("no marker found in:\n%s", s)
	}
	end := strings.Index(s[start:], ">>")
	if end < 0 {
		t.Fatalf("unterminated marker in:\n%s", s)
	}
	return s[start : start+end+2]
}

func TestSetReviewerModel(t *testing.T) {
	g := New(nil, nil, "", "")
	if g.model != "" {
		t.Fatalf("default model = %q, want empty (inherits the agent CLI default)", g.model)
	}
	g.SetReviewerModel("sonnet")
	if g.model != "sonnet" {
		t.Errorf("after override model = %q, want sonnet", g.model)
	}
	g.SetReviewerModel("")
	if g.model != "sonnet" {
		t.Errorf("empty override should be a no-op, model = %q", g.model)
	}
}

// TestGateNamedReviewerBinding (sty_a476a2f8 / sty_68dafd5f): edge agent=<name>
// resolves that binding's model AND builds its harness via newRunner (not
// g.runner); agent=reviewer / omitted uses [reviewer] + bootstrap runner.
func TestGateNamedReviewerBinding(t *testing.T) {
	wf := spineWF("", "", "", "done|||rev-a|reviewer-deep")
	g, r := newEngine(t, `{"decision":"accept","notes":"ok"}`, fakeDocs{
		workflow: wf, skillBody: "rubric", skillFound: true,
	})
	g.SetReviewerModel("grok-4.5")
	const deepCmd = "deep-reviewer-cli -p --append-system-prompt {system}"
	g.SetNamedAgents(func(name string) (config.AgentBinding, bool) {
		if name == "reviewer-deep" {
			return config.AgentBinding{
				Command: deepCmd, Tools: "Read,Grep", Model: "opus", Role: "reviewer",
			}, true
		}
		return config.AgentBinding{}, false
	})
	var sawCmd string
	newRunnerCalls := 0
	g.newRunner = func(iface, cmd string) (agentcli.Runner, error) {
		newRunnerCalls++
		sawCmd = cmd
		return r, nil // hermetic: same fake runner, built for the named binding
	}
	dec, err := g.Gate(context.Background(), workitem.Item{ID: "sty_x", Status: "backlog"}, "done")
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Gated || !dec.Accept {
		t.Fatalf("gate = %+v", dec)
	}
	if r.got.Model != "opus" {
		t.Errorf("runner Request.Model = %q, want opus (named reviewer-deep)", r.got.Model)
	}
	if sawCmd != deepCmd {
		t.Errorf("newRunner command = %q, want named binding command %q", sawCmd, deepCmd)
	}
	if newRunnerCalls != 1 {
		t.Errorf("newRunner calls = %d, want 1 for named gate", newRunnerCalls)
	}

	// Default [reviewer] when agent omitted/reviewer — uses g.runner, not newRunner.
	wf2 := spineWF("", "", "", "done|||rev-a|reviewer")
	g2, r2 := newEngine(t, `{"decision":"accept","notes":"ok"}`, fakeDocs{
		workflow: wf2, skillBody: "rubric", skillFound: true,
	})
	g2.SetReviewerModel("grok-4.5")
	g2.newRunner = func(iface, cmd string) (agentcli.Runner, error) {
		t.Errorf("default [reviewer] must not call newRunner (got iface=%q cmd=%q)", iface, cmd)
		return r2, nil
	}
	if _, err := g2.Gate(context.Background(), workitem.Item{ID: "sty_x", Status: "backlog"}, "done"); err != nil {
		t.Fatal(err)
	}
	if r2.got.Model != "grok-4.5" {
		t.Errorf("default reviewer model = %q, want grok-4.5", r2.got.Model)
	}
}

// TestReviewerSkillsForDOTAgent: edge agent= is returned alongside skills.
func TestReviewerSkillsForDOTAgent(t *testing.T) {
	spec := fixtureSpec(t, spineWF("", "", "", "b|executor||rev|reviewer-deep"))
	skills, agent, _, declared, _, _ := specReviewerSkills(spec, "backlog", "b")
	if !declared || len(skills) != 1 || skills[0] != "rev" || agent != "reviewer-deep" {
		t.Fatalf("skills=%v agent=%q declared=%v", skills, agent, declared)
	}
}

func TestSetReviewerTools(t *testing.T) {
	g := New(nil, nil, "", "")
	if g.tools != defaultTools {
		t.Fatalf("default tools = %q, want %q", g.tools, defaultTools)
	}
	g.SetReviewerTools("Read,Edit,Write")
	if g.tools != "Read,Edit,Write" {
		t.Errorf("after override tools = %q, want Read,Edit,Write", g.tools)
	}
	g.SetReviewerTools("")
	if g.tools != "Read,Edit,Write" {
		t.Errorf("empty override should be a no-op, tools = %q", g.tools)
	}
}

func TestSetRunner(t *testing.T) {
	g := New(nil, nil, "", "")
	r, _ := agentcli.NewRunner("grok")
	g.SetRunner(r)
	if g.runner == nil || g.runner.Name() != "grok" {
		t.Fatalf("SetRunner should override the runner, got %v", g.runner)
	}
	g.SetRunner(nil) // nil is ignored
	if g.runner == nil || g.runner.Name() != "grok" {
		t.Errorf("a nil runner must be ignored")
	}
}

// TestStampedWorkflowName reads the workflow:<name> stamp from an item's tags.
func TestStampedWorkflowName(t *testing.T) {
	if got := stampedWorkflowName(workitem.Item{Tags: []string{"a", "workflow:my-wf", "b"}}); got != "my-wf" {
		t.Errorf("stampedWorkflowName = %q, want my-wf", got)
	}
	if got := stampedWorkflowName(workitem.Item{Tags: []string{"a", "b"}}); got != "" {
		t.Errorf("un-stamped item = %q, want empty", got)
	}
}

// TestActiveWorkflowPreferringStampWins: the STAMPED workflow governs the story,
// overriding category selection; an un-stamped item resolves by category; a stamp
// that no longer resolves falls back to category (sty_3800ac23).
func TestActiveWorkflowPreferringStampWins(t *testing.T) {
	wfFeature := docindex.Doc{Kind: "workflows", Name: "wf-feature",
		Body: "---\nname: wf-feature\ntype: workflow\napplies_to: [\"feature\"]\n---\n# f\n"}
	wfChore := docindex.Doc{Kind: "workflows", Name: "wf-chore",
		Body: "---\nname: wf-chore\ntype: workflow\napplies_to: [\"chore\"]\n---\n# c\n"}
	g, _ := newEngine(t, "", fakeDocs{workflow: plainWF, extraWorkflows: []docindex.Doc{wfFeature, wfChore}})
	ctx := context.Background()

	// Category "feature" alone selects wf-feature.
	if doc, err := g.activeWorkflowPreferring(ctx, "feature", ""); err != nil || doc.Name != "wf-feature" {
		t.Fatalf("category feature → %q,%v; want wf-feature", doc.Name, err)
	}
	// A stamp for wf-chore WINS even though the category is feature.
	if doc, err := g.activeWorkflowPreferring(ctx, "feature", "wf-chore"); err != nil || doc.Name != "wf-chore" {
		t.Fatalf("stamped wf-chore → %q,%v; want wf-chore (stamp wins over category)", doc.Name, err)
	}
	// A stamp that no longer resolves falls back to category selection.
	if doc, err := g.activeWorkflowPreferring(ctx, "feature", "gone"); err != nil || doc.Name != "wf-feature" {
		t.Fatalf("stale stamp → %q,%v; want wf-feature (fallback)", doc.Name, err)
	}
}

// TestWorkflowConsistency: two REPO workflows claiming the same wildcard is
// flagged (over-configuration); an unresolved referenced skill is flagged; a
// clean set and embedded-only ties are not (sty_4c0c7246).
func TestWorkflowConsistency(t *testing.T) {
	repoWild := func(name string) docindex.Doc {
		return docindex.Doc{Name: name, Embedded: false,
			Body: "---\nname: " + name + "\ntype: workflow\napplies_to: [\"*\"]\n---\n# w\n"}
	}
	// (1) Two repo wildcards → ambiguity flagged.
	probs := WorkflowConsistency([]docindex.Doc{repoWild("a"), repoWild("b")}, func(string) bool { return true })
	if len(probs) == 0 || !strings.Contains(strings.Join(probs, "\n"), "same precedence") {
		t.Errorf("two repo wildcards should be flagged ambiguous, got %v", probs)
	}
	// Embedded ties are NOT flagged (the canonical defaults are the single source).
	emb := func(name string) docindex.Doc {
		return docindex.Doc{Name: name, Embedded: true,
			Body: "---\nname: " + name + "\ntype: workflow\napplies_to: [\"*\"]\n---\n# w\n"}
	}
	if p := WorkflowConsistency([]docindex.Doc{emb("e1"), emb("e2")}, nil); len(p) != 0 {
		t.Errorf("embedded ties must not be flagged, got %v", p)
	}
	// (2) An unresolved referenced skill is flagged; resolved → clean.
	wfSkill := docindex.Doc{Name: "x", Embedded: false,
		Body: "[meta]\nname = \"x\"\ntype = \"workflow\"\napplies_to = [\"feature\"]\n\n" +
			"[coded]\nstatus = \"in_progress\"\nagent = \"executor\"\n" +
			"reviewers = [\"missing-skill\"]\nrequires = [\"raised\"]\n"}
	miss := WorkflowConsistency([]docindex.Doc{wfSkill}, func(s string) bool { return s != "missing-skill" })
	if len(miss) == 0 || !strings.Contains(strings.Join(miss, "\n"), "missing-skill") {
		t.Errorf("unresolved referenced skill should be flagged, got %v", miss)
	}
	if ok := WorkflowConsistency([]docindex.Doc{wfSkill}, func(string) bool { return true }); len(ok) != 0 {
		t.Errorf("a resolved referenced skill is clean, got %v", ok)
	}
	// (3) An unresolved create_review binding is flagged (sty_51ad783b): it would
	// silently degrade creation to deterministic-only; resolved -> clean.
	wfCreate := docindex.Doc{Name: "y", Embedded: false,
		Body: "---\nname: y\ntype: workflow\napplies_to: [\"fix\"]\ncreate_review: my-create-review\n---\n# w\n"}
	crMiss := WorkflowConsistency([]docindex.Doc{wfCreate}, func(s string) bool { return s != "my-create-review" })
	if len(crMiss) == 0 || !strings.Contains(strings.Join(crMiss, "\n"), "create_review") {
		t.Errorf("unresolved create_review should be flagged, got %v", crMiss)
	}
	if ok := WorkflowConsistency([]docindex.Doc{wfCreate}, func(string) bool { return true }); len(ok) != 0 {
		t.Errorf("a resolved create_review is clean, got %v", ok)
	}
}

// estimateActualCheckBody returns the embedded estimate/actual skill's body,
// failing the test if it is missing its self-contained check block.
func estimateActualCheckBody(t *testing.T) string {
	t.Helper()
	var body string
	for _, d := range config.EmbeddedDefaults() {
		if d.Kind == "skills" && d.Name == "satelle-estimate-actual-review" {
			body = d.Body
		}
	}
	if body == "" || !strings.Contains(body, "```check") {
		t.Fatalf("embedded estimate skill must carry a self-contained check block")
	}
	return body
}

// TestCodedEstimateGate runs the EMBEDDED estimate/actual skill's self-contained
// check end-to-end — real bash, the transition payload on stdin (sty_f804caaa,
// sty_8eae81ac): entering in_progress without any estimate tag rejects, any
// accepted unit accepts; entering done NEVER rejects — an actual is no longer a
// tag this gate requires, since the mechanism computes and persists it on the
// terminal commit itself (architecture revision A4) — and other edges pass.
// The agent is never invoked.
func TestCodedEstimateGate(t *testing.T) {
	body := estimateActualCheckBody(t)
	g, r := newEngine(t, `{"decision":"reject"}`, fakeDocs{skillBody: body, skillFound: true})
	g.repoRoot = t.TempDir() // the check runs in the repo root — use a real dir

	cases := []struct {
		name   string
		to     string
		tags   []string
		accept bool
		want   string // substring of the reject notes
	}{
		{"in_progress without estimate rejects", "in_progress", []string{"cli"}, false, "no plan estimate recorded"},
		{"in_progress with legacy minutes accepts", "in_progress", []string{"estimate-minutes:30"}, true, ""},
		{"in_progress with legacy tokens accepts", "in_progress", []string{"estimate-tokens:5000"}, true, ""},
		{"in_progress with usd rejects", "in_progress", []string{"estimate-usd:2.5"}, false, "no plan estimate recorded"},
		{"in_progress with fresh-input accepts", "in_progress", []string{"estimate-fresh-input:100000"}, true, ""},
		{"done with no estimate never rejects", "done", nil, true, ""},
		{"done with a huge overrun never rejects", "done", []string{"estimate-tokens:1"}, true, ""},
		{"other edges pass", "integration", nil, true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dec, err := g.runReviewer(context.Background(),
				workitem.Item{ID: "sty_x", Kind: workitem.KindStory, Status: "backlog", Tags: c.tags},
				c.to, "satelle-estimate-actual-review", "")
			if err != nil {
				t.Fatal(err)
			}
			if !dec.Gated || dec.Accept != c.accept {
				t.Fatalf("decision = %+v, want gated accept=%v", dec, c.accept)
			}
			if c.want != "" && !strings.Contains(dec.Notes, c.want) {
				t.Errorf("notes = %q, want substring %q", dec.Notes, c.want)
			}
		})
	}
	if r.got.SystemPrompt != "" {
		t.Error("the coded gate must never invoke the agent")
	}
}

// TestCodedEstimateGateEmptyCategoryNeedsNoEstimate runs the embedded check on
// the engine's real payload: a story of category empty enters in_progress with
// no estimate tag; a feature story with the same tags is still rejected.
func TestCodedEstimateGateEmptyCategoryNeedsNoEstimate(t *testing.T) {
	body := estimateActualCheckBody(t)
	g, _ := newEngine(t, `{"decision":"reject"}`, fakeDocs{skillBody: body, skillFound: true})
	g.repoRoot = t.TempDir()
	for _, c := range []struct {
		category string
		accept   bool
	}{{workitem.EmptyCategory, true}, {"feature", false}} {
		dec, err := g.runReviewer(context.Background(),
			workitem.Item{ID: "sty_x", Kind: workitem.KindStory, Status: "plan", Category: c.category},
			"in_progress", "satelle-estimate-actual-review", "")
		if err != nil {
			t.Fatal(err)
		}
		if !dec.Gated || dec.Accept != c.accept {
			t.Errorf("category %q: decision = %+v, want gated accept=%v", c.category, dec, c.accept)
		}
	}
}

// TestCodedEstimateGateDonePrintsUnitAwareSummary asserts on the check's own
// STDOUT (never dec.Notes, which on accept carries only the check's source,
// see Engine.runCheck) — pinning AC5: the shipped check prints a legacy token
// estimate labelled as such, never as dollars or fresh input, beside the
// ledger-computed measured_actual it always receives generically on the gate
// payload (Engine.fillMeasuredActual), and accepts regardless of the size of
// the overrun.
func TestCodedEstimateGateDonePrintsUnitAwareSummary(t *testing.T) {
	body := estimateActualCheckBody(t)
	command := skillCheck(body)
	if command == "" {
		t.Fatal("embedded estimate skill must resolve a check command")
	}
	g, _ := newEngine(t, "", fakeDocs{})
	g.repoRoot = t.TempDir()

	payload := `{"story":{"tags":["estimate-tokens:200000"]},"from":"release","to":"done",` +
		`"measured_actual":{"story_id":"sty_x",` +
		`"own":{"cost_usd":0.10,"cost_rows":1,"fresh_input":1000,"output":200,"cache_read":500,"cache_write":10},` +
		`"total":{"cost_usd":50.5,"cost_rows":9,"cost_unavailable_rows":2,"fresh_input":9000000,"output":100000,"cache_read":1000000,"cache_write":50000}}}`

	out, err := g.check(context.Background(), g.repoRoot, command, payload)
	if err != nil {
		t.Fatalf("check rejected a huge overrun: %v\n%s", err, out)
	}
	if !strings.Contains(out, "200000 tokens") {
		t.Errorf("stdout = %q, want the legacy estimate labelled in tokens", out)
	}
	if !strings.Contains(out, "legacy unit") {
		t.Errorf("stdout = %q, want the legacy estimate marked as a legacy unit", out)
	}
	for _, want := range []string{"fresh 9000000", "output 100000", "cache read 1000000", "cache write 50000"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want %q", out, want)
		}
	}
	if strings.Contains(out, "$50.5") || strings.Contains(out, "$") {
		t.Errorf("stdout = %q, must not print a dollar figure", out)
	}
}

// TestCodedEstimateGateMeasuredActualUnavailable pins revision 2 point 3: when
// the payload carries no measured_actual block at all (a computation
// failure), the check says so plainly rather than inventing a $0/fresh-0
// figure nothing measured, and still accepts.
func TestCodedEstimateGateMeasuredActualUnavailable(t *testing.T) {
	body := estimateActualCheckBody(t)
	command := skillCheck(body)
	if command == "" {
		t.Fatal("embedded estimate skill must resolve a check command")
	}
	g, _ := newEngine(t, "", fakeDocs{})
	g.repoRoot = t.TempDir()

	payload := `{"story":{"tags":["estimate-minutes:30"]},"from":"release","to":"done"}`

	out, err := g.check(context.Background(), g.repoRoot, command, payload)
	if err != nil {
		t.Fatalf("check rejected with no measured_actual: %v\n%s", err, out)
	}
	if !strings.Contains(out, "measured actual unavailable") {
		t.Errorf("stdout = %q, want an explicit unavailable notice", out)
	}
	if strings.Contains(out, "$0") || strings.Contains(out, "fresh 0") {
		t.Errorf("stdout = %q, must never invent a $0/fresh-0 figure when nothing was measured", out)
	}
}

// TestCodedEstimateGateAllUnpricedRows pins that a measured_actual whose
// rows are unpriced still prints the token figures and never a dollar figure,
// and that a stored estimate-usd tag is not treated as the printed estimate.
func TestCodedEstimateGateAllUnpricedRows(t *testing.T) {
	body := estimateActualCheckBody(t)
	command := skillCheck(body)
	if command == "" {
		t.Fatal("embedded estimate skill must resolve a check command")
	}
	g, _ := newEngine(t, "", fakeDocs{})
	g.repoRoot = t.TempDir()

	payload := `{"story":{"tags":["estimate-usd:5"]},"from":"release","to":"done",` +
		`"measured_actual":{"story_id":"sty_x",` +
		`"own":{"cost_usd":0,"cost_rows":0,"cost_unavailable_rows":1,"fresh_input":0,"output":0},` +
		`"total":{"cost_usd":0,"cost_rows":0,"cost_unavailable_rows":3,"fresh_input":0,"output":0}}}`

	out, err := g.check(context.Background(), g.repoRoot, command, payload)
	if err != nil {
		t.Fatalf("check rejected an all-unpriced actual: %v\n%s", err, out)
	}
	for _, want := range []string{"fresh 0", "output 0", "cache read 0", "cache write 0"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want %q", out, want)
		}
	}
	if strings.Contains(out, "$") {
		t.Errorf("stdout = %q, must not print a dollar figure for an unpriced actual", out)
	}
	if strings.Contains(out, "estimate: $5") || strings.Contains(out, "estimate-usd") {
		t.Errorf("stdout = %q, must not treat a dollar tag as the printed estimate", out)
	}
}

// TestCodedEstimateGateRepoToleranceGate pins that a repo may author its own
// gate, in its own unit, over the same generically-attached measured_actual
// block — the shipped gate never compares across units, but a repo one may
// compare usd-to-usd (AC5).
func TestCodedEstimateGateRepoToleranceGate(t *testing.T) {
	repoCheck := `
IN=$(cat)
est=$(printf '%s' "$IN" | grep -o '"estimate-usd:[^"]*"' | head -1)
est=${est#\"estimate-usd:}; est=${est%\"}
actual=$(printf '%s' "$IN" | grep -o '"cost_usd":[0-9.]*' | tail -1 | cut -d: -f2)
limit=$(awk -v e="$est" 'BEGIN{print e*1.5}')
ok=$(awk -v a="$actual" -v l="$limit" 'BEGIN{print (a<=l)?1:0}')
if [ "$ok" = "1" ]; then
  echo "within tolerance: \$$actual <= \$$limit"
  exit 0
fi
echo "over tolerance: \$$actual > \$$limit (estimate \$$est x1.5)"
exit 1
`
	g, _ := newEngine(t, "", fakeDocs{})
	g.repoRoot = t.TempDir()

	within := `{"story":{"tags":["estimate-usd:10"]},"to":"done","measured_actual":{"total":{"cost_usd":12.0}}}`
	if out, err := g.check(context.Background(), g.repoRoot, repoCheck, within); err != nil {
		t.Fatalf("expected accept within tolerance: %v\n%s", err, out)
	}

	over := `{"story":{"tags":["estimate-usd:10"]},"to":"done","measured_actual":{"total":{"cost_usd":100.0}}}`
	if out, err := g.check(context.Background(), g.repoRoot, repoCheck, over); err == nil {
		t.Fatalf("expected reject over tolerance, accepted: %s", out)
	}
}

func TestChatPayloadFromEqualsTo(t *testing.T) {
	g, _ := newEngine(t, "", fakeDocs{})
	g.SetMessagesResolver(func(_ context.Context, itemID string, addrs []string) []MessageState {
		if itemID != "sty_chat" {
			t.Errorf("item %s", itemID)
		}
		if len(addrs) != 1 || addrs[0] != "coder" {
			t.Errorf("addrs = %v", addrs)
		}
		return []MessageState{{From: "human", To: "coder", Body: "hi"}}
	})
	tp, err := g.ChatPayload(context.Background(), workitem.Item{ID: "sty_chat", Status: "in_progress"}, "coder")
	if err != nil {
		t.Fatal(err)
	}
	if tp.From != "in_progress" || tp.To != "in_progress" {
		t.Fatalf("from/to = %q/%q", tp.From, tp.To)
	}
	if len(tp.Messages) != 1 || tp.Messages[0].Body != "hi" {
		t.Fatalf("messages = %#v", tp.Messages)
	}
}

// TestChatPayloadAddressesChosenBinding (sty_a0372443 AC3): --agent reviewer
// asks for the REVIEWER address set, and only messages addressed to that role
// or to "*" are delivered — a message to another role is not.
func TestChatPayloadAddressesChosenBinding(t *testing.T) {
	g, _ := newEngine(t, "", fakeDocs{})
	all := []MessageState{
		{From: "developer-agent", To: "reviewer", Body: "why did you reject"},
		{From: "developer-agent", To: "*", Body: "broadcast"},
		{From: "human", To: "orchestrator", Body: "for the console only"},
	}
	var gotAddrs []string
	g.SetMessagesResolver(func(_ context.Context, _ string, addrs []string) []MessageState {
		gotAddrs = addrs
		// Mirror verb.MessagesSince: the named addresses UNION "*".
		want := map[string]bool{"*": true}
		for _, a := range addrs {
			want[a] = true
		}
		var out []MessageState
		for _, m := range all {
			if want[m.To] {
				out = append(out, m)
			}
		}
		return out
	})
	tp, err := g.ChatPayload(context.Background(), workitem.Item{ID: "sty_chat", Status: "integration"}, "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if len(gotAddrs) != 1 || gotAddrs[0] != "reviewer" {
		t.Fatalf("addrs = %v, want [reviewer]", gotAddrs)
	}
	if len(tp.Messages) != 2 {
		t.Fatalf("messages = %#v, want the reviewer row and the * row", tp.Messages)
	}
	for _, m := range tp.Messages {
		if m.To == "orchestrator" {
			t.Fatalf("a message addressed to another role was delivered: %#v", m)
		}
	}
}

// TestOpenSessionConsultingReviewer (sty_a0372443 AC1/AC4): a live-capable
// non-orchestrator binding opens, is told in its charter that it is CONSULTING
// rather than judging, and runs on the BINDING's own (read-only) tool grant.
func TestOpenSessionConsultingReviewer(t *testing.T) {
	g, _ := newEngine(t, "", fakeDocs{})
	g.SetNamedAgents(func(name string) (config.AgentBinding, bool) {
		if name != "reviewer" {
			return config.AgentBinding{}, false
		}
		return config.AgentBinding{
			Role: "reviewer", Interface: "stream", Tools: "Read,Grep,Glob",
			Command: "claude -p --input-format stream-json --output-format stream-json --allowedTools {tools}",
		}, true
	})
	var got agentcli.Request
	g.newOpener = func(iface, _ string) (agentcli.SessionOpener, error) {
		if iface != "stream" {
			t.Errorf("iface = %q", iface)
		}
		return func(_ context.Context, req agentcli.Request, _ agentcli.PermissionPolicy) (agentcli.Session, error) {
			got = req
			return closedSess{}, nil
		}, nil
	}
	sess, err := g.OpenSessionAs(context.Background(), "reviewer", SessionRoleConsult, workitem.Item{ID: "sty_1", Status: "integration"}, nil, nil)
	if err != nil {
		t.Fatalf("a live-capable [reviewer] must open: %v", err)
	}
	_ = sess
	if !strings.Contains(got.SystemPrompt, "CONSULTED on this story") ||
		!strings.Contains(got.SystemPrompt, "consulting, NOT judging") ||
		!strings.Contains(got.SystemPrompt, "CONTEXT, not a verdict") {
		t.Fatalf("consulting charter missing from system prompt: %q", got.SystemPrompt)
	}
	if strings.Contains(got.SystemPrompt, "isolated satelle executor agent") {
		t.Fatalf("a consulted binding must not get the executor charter: %q", got.SystemPrompt)
	}
	if got.AllowedTools != "Read,Grep,Glob" {
		t.Fatalf("tools = %q, want the binding's own read-only grant", got.AllowedTools)
	}
}

// TestOpenSessionDrivingKeepsExecutorCharter: a DRIVING session is not
// consulting — charter selection is by the stated role, not the binding name.
func TestOpenSessionDrivingKeepsExecutorCharter(t *testing.T) {
	g, _ := newEngine(t, "", fakeDocs{})
	g.SetNamedAgents(func(name string) (config.AgentBinding, bool) {
		if name != "coder" {
			return config.AgentBinding{}, false
		}
		return config.AgentBinding{Interface: "stream", Tools: "Read,Grep,Glob,Bash(satelle:*)", Command: "claude -p {tools}"}, true
	})
	var got agentcli.Request
	g.newOpener = func(string, string) (agentcli.SessionOpener, error) {
		return func(_ context.Context, req agentcli.Request, _ agentcli.PermissionPolicy) (agentcli.Session, error) {
			got = req
			return closedSess{}, nil
		}, nil
	}
	if _, err := g.OpenSessionAs(context.Background(), "coder", SessionRoleDriving, workitem.Item{ID: "sty_1", Status: "in_progress"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.SystemPrompt, "isolated satelle executor agent") {
		t.Fatalf("a driving session must keep the executor charter: %q", got.SystemPrompt)
	}
	if strings.Contains(got.SystemPrompt, "consulting, NOT judging") {
		t.Fatalf("a driving session performs; it is not consulted: %q", got.SystemPrompt)
	}
}

// TestOpenSessionRefusesUnnamedBinding (sty_6f9ba7ca): there is no default
// console binding any more — an empty name is refused, not resolved to an
// [orchestrator].
func TestOpenSessionRefusesUnnamedBinding(t *testing.T) {
	g, _ := newEngine(t, "", fakeDocs{})
	g.SetNamedAgents(func(name string) (config.AgentBinding, bool) {
		return config.AgentBinding{Interface: "stream", Command: "claude -p {tools}"}, true
	})
	_, err := g.OpenSessionAs(context.Background(), "", SessionRoleDriving, workitem.Item{ID: "sty_1", Status: "in_progress"}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "no binding named") {
		t.Fatalf("want a no-binding-named refusal, got %v", err)
	}
}

func TestOpenSessionInjectsSessionEnv(t *testing.T) {
	t.Setenv("SATELLE_SESSION", "sess-chat")
	g, _ := newEngine(t, "", fakeDocs{})
	g.SetNamedAgents(func(name string) (config.AgentBinding, bool) {
		if name != "coder" {
			return config.AgentBinding{}, false
		}
		return config.AgentBinding{Interface: "stream", Command: "claude -p --input-format stream-json --output-format stream-json --allowedTools {tools}"}, true
	})
	var gotEnv map[string]string
	g.newOpener = func(iface, command string) (agentcli.SessionOpener, error) {
		if iface != "stream" {
			t.Errorf("iface = %q", iface)
		}
		return func(_ context.Context, req agentcli.Request, _ agentcli.PermissionPolicy) (agentcli.Session, error) {
			gotEnv = req.Env
			return closedSess{}, nil
		}, nil
	}
	handler := func(agentcli.Event) {}
	var gotOnEvent agentcli.EventHandler
	g.newOpener = func(iface, command string) (agentcli.SessionOpener, error) {
		return func(_ context.Context, req agentcli.Request, _ agentcli.PermissionPolicy) (agentcli.Session, error) {
			gotEnv = req.Env
			gotOnEvent = req.OnEvent
			return closedSess{}, nil
		}, nil
	}
	sess, err := g.OpenSessionAs(context.Background(), "coder", SessionRoleDriving, workitem.Item{ID: "sty_1", Status: "in_progress"}, nil, handler)
	if err != nil {
		t.Fatal(err)
	}
	_ = sess
	if gotEnv["SATELLE_SESSION"] != "sess-chat" {
		t.Fatalf("env = %#v, want SATELLE_SESSION=sess-chat", gotEnv)
	}
	if gotOnEvent == nil {
		t.Fatal("OpenSessionAs must install the caller's OnEvent on the request (synchronous transcript sink)")
	}
}

// TestOpenSessionAsResolvesLiveInterfaceFromSetLiveNamedAgents (AC1, AC6,
// epic:model-selection child 2): a binding with no interface= that
// SetLiveNamedAgents resolves via AgentsConfig.LiveBinding opens with the
// CLI's best live transport — stream for a claude command, acp for any other
// spawn line — covering the rework relay's coder seat, its rework.consult
// binding and any other live use alike, since they all open through this one
// method.
func TestOpenSessionAsResolvesLiveInterfaceFromSetLiveNamedAgents(t *testing.T) {
	for _, tc := range []struct {
		name        string
		binding     string
		command     string
		wantIface   string
		wantCommand string
		// wantErrIs, when set, is the error OpenSessionAs must fail with —
		// otherwise it must succeed and gotIface/gotCommand must match.
		wantErrIs string
	}{
		// An already-authored, stream-SHAPED command stays exactly as written —
		// EffectiveBinding only fills a DEFAULT command when none is authored
		// (AC1); here it just flips the resolved interface to the CLI's live
		// transport, and the session actually opens (AC6).
		{name: "coder seat (claude, stream-shaped)", binding: "coder",
			command:     "claude -p --input-format stream-json --output-format stream-json --allowedTools {tools}",
			wantIface:   "stream",
			wantCommand: "claude -p --input-format stream-json --output-format stream-json --allowedTools {tools}"},
		{name: "rework consult (claude, stream-shaped)", binding: "reviewer-consult",
			command:     "claude -p --input-format stream-json --output-format stream-json --allowedTools {tools}",
			wantIface:   "stream",
			wantCommand: "claude -p --input-format stream-json --output-format stream-json --allowedTools {tools}"},
		{name: "rework coder (acp spawn)", binding: "coder",
			command: "gemini --experimental-acp", wantIface: "acp", wantCommand: "gemini --experimental-acp"},
		// No command authored yet: every live transport is a candidate, the
		// shipped default order picks stream, and stream's default command line
		// is filled in (AC1's "a resolved default supplies the matching default
		// command line when command is also unset").
		{name: "no command yet is refused", binding: "coder",
			wantErrIs: "no command and no profile"},
		// An authored ONE-SHOT claude command (the [reviewer]/DefaultReviewerCommand
		// shape: `-p --output-format json … {system}`) cannot serve stream — it has
		// no --input-format stream-json and carries {system}, which the real
		// stream/acp construction rejects (system/payload ride the live protocol,
		// not argv). This must NOT be waved through as "stream (live use)" just
		// because the first token is claude (the AC6 regression) — it resolves to
		// command and is refused as not-live-capable, exactly like an explicit
		// interface=command would be.
		{name: "coder seat (claude, one-shot-shaped) cannot serve a live use", binding: "coder",
			command:   "claude -p --output-format json --append-system-prompt {system} --allowedTools {tools}",
			wantErrIs: "not live-capable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, _ := newEngine(t, "", fakeDocs{})
			raw := config.AgentBinding{Role: "agent", Command: tc.command}
			g.SetNamedAgents(func(string) (config.AgentBinding, bool) { return raw, true })
			g.SetLiveNamedAgents(func(name string) (config.AgentBinding, bool) {
				var ac config.AgentsConfig
				return ac.EffectiveBinding(raw, config.UseLive), true
			})
			var gotIface, gotCommand string
			g.newOpener = func(iface, command string) (agentcli.SessionOpener, error) {
				// Run the REAL construction the runtime opens with — a fake that
				// accepts any (iface, command) pair would hide exactly the bug this
				// test guards against: an authored one-shot claude command being
				// waved through as stream-capable.
				if _, err := agentcli.OpenerFromBinding(iface, command); err != nil {
					return nil, err
				}
				gotIface, gotCommand = iface, command
				return func(_ context.Context, _ agentcli.Request, _ agentcli.PermissionPolicy) (agentcli.Session, error) {
					return closedSess{}, nil
				}, nil
			}
			_, err := g.OpenSessionAs(context.Background(), tc.binding, SessionRoleConsult,
				workitem.Item{ID: "sty_1", Status: "in_progress"}, nil, nil)
			if tc.wantErrIs != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrIs) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErrIs)
				}
				return
			}
			if err != nil {
				t.Fatalf("OpenSessionAs: %v", err)
			}
			if gotIface != tc.wantIface {
				t.Errorf("interface = %q, want %q", gotIface, tc.wantIface)
			}
			if gotCommand != tc.wantCommand {
				t.Errorf("command = %q, want %q", gotCommand, tc.wantCommand)
			}
		})
	}
}

// TestOpenSessionAsFallsBackToNamedAgentsWithoutLiveResolver: a caller that
// never wires SetLiveNamedAgents keeps today's behaviour — an unset interface=
// still resolves to command and is refused as not-live-capable, byte-identical
// to before epic:model-selection child 2.
func TestOpenSessionAsFallsBackToNamedAgentsWithoutLiveResolver(t *testing.T) {
	g, _ := newEngine(t, "", fakeDocs{})
	g.SetNamedAgents(func(string) (config.AgentBinding, bool) {
		return config.AgentBinding{Role: "agent", Command: "claude -p --output-format json {system}"}, true
	})
	_, err := g.OpenSessionAs(context.Background(), "coder", SessionRoleConsult,
		workitem.Item{ID: "sty_1", Status: "in_progress"}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "not live-capable") {
		t.Fatalf("want not-live-capable refusal, got %v", err)
	}
}

type closedSess struct{}

func (closedSess) Send(context.Context, agentcli.Turn) error { return nil }
func (closedSess) Events() <-chan agentcli.Event             { return nil }
func (closedSess) Cancel() error                             { return nil }
func (closedSess) Close() error                              { return nil }
func (closedSess) Captured() []byte                          { return nil }

// --- AC2 dispatch-outcome telemetry (sty_b73c3236) --------------------------
//
// The dispatch engine records STRUCTURED, queryable outcomes for every agent
// failure mode a wrapping verb never sees — a killed/timed-out/no-verdict
// subprocess: classifyOutcome names WHY, and each reviewer/summariser/executor
// retry/failure/timeout site emits a telemetry event via the wired sink. These
// pin that wiring (the gap satelle-code-ac-review flagged).

type telemetryRec struct {
	storyID, actor, kind string
	data                 map[string]any
}

// captureTelemetry wires g's telemetry sink to append to the returned slice, so
// a test can assert exactly which events the engine emitted, with what outcome.
func captureTelemetry(g *Engine) *[]telemetryRec {
	recs := &[]telemetryRec{}
	g.SetTelemetry(func(_ context.Context, storyID, actor, kind string, data map[string]any) {
		*recs = append(*recs, telemetryRec{storyID, actor, kind, data})
	})
	return recs
}

func countKind(recs []telemetryRec, kind string) int {
	n := 0
	for _, r := range recs {
		if r.kind == kind {
			n++
		}
	}
	return n
}

func hasOutcome(recs []telemetryRec, kind, want string) bool {
	for _, r := range recs {
		if r.kind == kind {
			if o, _ := r.data["outcome"].(string); o == want {
				return true
			}
		}
	}
	return false
}

// assertCauses requires every agent-retry/agent-failure row to carry a non-empty
// cause (sty_9bc496e1) and at least one error-outcome row to carry wantErrText.
func assertCauses(t *testing.T, recs []telemetryRec, wantErrText string) {
	t.Helper()
	found := false
	for _, rec := range recs {
		cause, _ := rec.data["cause"].(string)
		if cause == "" {
			t.Errorf("%s row carries no cause: %v", rec.kind, rec.data)
		}
		if o, _ := rec.data["outcome"].(string); o == "error" && strings.Contains(cause, wantErrText) {
			found = true
		}
	}
	if !found {
		t.Errorf("no error-outcome row carries a cause containing %q", wantErrText)
	}
}

// classifyOutcome names WHY a dispatched invocation failed — the label that
// makes the telemetry queryable rather than a free-text body string.
func TestClassifyOutcome(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil is a parsed no-verdict, not a run error", nil, "no-verdict"},
		{"a bounded deadline is a timeout", context.DeadlineExceeded, "timeout"},
		{"a killed subprocess surfaces signal:killed", errors.New("agentcli: claude: signal: killed"), "signal:killed"},
		{"anything else is a generic error", errors.New("boom"), "error"},
	}
	for _, c := range cases {
		if got := classifyOutcome(c.err); got != c.want {
			t.Errorf("%s: classifyOutcome(%v) = %q, want %q", c.name, c.err, got, c.want)
		}
	}
}

// A reviewer that never returns a verdict records one agent-retry per transient
// attempt (carrying the classified outcome) and one terminal agent-failure — the
// structured capture AC2 asks for, not only a reviewer.log line.
func TestGate_recordsRetryAndFailureTelemetry(t *testing.T) {
	docs := fakeDocs{workflow: testWorkflow, skillBody: "rubric body", skillFound: true}
	r := &scriptedRunner{results: []struct {
		out string
		err error
	}{
		{out: "no verdict here"}, // transient no-verdict → agent-retry outcome=no-verdict
		{err: errFakeAgent},      // transient run error   → agent-retry outcome=error
	}}
	g := New(r, docs, "/repo", "")
	g.backoff = func(int) time.Duration { return 0 }
	recs := captureTelemetry(g)

	if _, err := g.Gate(context.Background(), workitem.Item{ID: "sty_1", Status: "in_progress"}, "done"); err == nil {
		t.Fatal("gate should fail once every attempt is a transient non-verdict")
	}
	if got := countKind(*recs, "agent-retry"); got != defaultReviewerAttempts {
		t.Errorf("want %d agent-retry events (one per attempt), got %d", defaultReviewerAttempts, got)
	}
	if got := countKind(*recs, "agent-failure"); got != 1 {
		t.Errorf("want exactly 1 terminal agent-failure event, got %d", got)
	}
	if !hasOutcome(*recs, "agent-retry", "no-verdict") {
		t.Error("a no-verdict attempt should record outcome=no-verdict")
	}
	if !hasOutcome(*recs, "agent-retry", "error") {
		t.Error("an errored attempt should record outcome=error")
	}
	assertCauses(t, *recs, errFakeAgent.Error())
	for _, rec := range *recs {
		if rec.actor != "reviewer" {
			t.Errorf("reviewer telemetry actor = %q, want reviewer", rec.actor)
		}
		if s, _ := rec.data["skill"].(string); s == "" {
			t.Errorf("telemetry event %q missing the skill field", rec.kind)
		}
	}
}

// A reviewer deadline is a bound, not contention — it records a single
// agent-timeout and fails fast (no retry, no agent-failure).
func TestGate_recordsTimeoutTelemetry(t *testing.T) {
	docs := fakeDocs{workflow: testWorkflow, skillBody: "rubric body", skillFound: true}
	r := &scriptedRunner{results: []struct {
		out string
		err error
	}{{err: context.DeadlineExceeded}}}
	g := New(r, docs, "/repo", "")
	g.backoff = func(int) time.Duration { return 0 }
	recs := captureTelemetry(g)

	if _, err := g.Gate(context.Background(), workitem.Item{ID: "sty_1", Status: "in_progress"}, "done"); err == nil {
		t.Fatal("a reviewer deadline should surface an error")
	}
	if got := countKind(*recs, "agent-timeout"); got != 1 {
		t.Errorf("want exactly 1 agent-timeout event, got %d", got)
	}
	if got := countKind(*recs, "agent-failure"); got != 0 {
		t.Errorf("a fail-fast timeout must not also record agent-failure, got %d", got)
	}
	if r.calls != 1 {
		t.Errorf("a deadline must fail fast (1 attempt), got %d", r.calls)
	}
}

// A gate agent that fails on the command transport leaves WHY on its telemetry
// rows (sty_9bc496e1): the exit status and stderr head the transport returned,
// redacted and bounded — not only outcome=error. The runner is the real command
// transport, so the asserted text is the text agentcli builds, not a stub's.
func TestAgentFailureRecordsBoundedCause_CommandTransport(t *testing.T) {
	cases := []struct {
		name, stderr string
		exit         int
		check        func(t *testing.T, cause string)
	}{
		{"exit status and stderr", "rate limited", 2, func(t *testing.T, cause string) {
			for _, want := range []string{"exit status 2", "rate limited"} {
				if !strings.Contains(cause, want) {
					t.Errorf("cause %q lacks %q", cause, want)
				}
			}
		}},
		{"over-long stderr is truncated on a rune boundary", strings.Repeat("é", 2000), 1, func(t *testing.T, cause string) {
			if !utf8.ValidString(cause) {
				t.Errorf("cause is not valid UTF-8: %q", cause)
			}
			if n := utf8.RuneCountInString(cause); n > 241 {
				t.Errorf("cause is %d runes, want at most 241 (240 + ellipsis)", n)
			}
			if !strings.HasPrefix(cause, "agentcli:") || !strings.HasSuffix(cause, "…") {
				t.Errorf("cause should keep its head and end in an ellipsis, got %q", cause)
			}
		}},
		{"a secret in stderr is redacted", "token=abc123", 3, func(t *testing.T, cause string) {
			if !strings.Contains(cause, "token=[REDACTED]") || strings.Contains(cause, "abc123") {
				t.Errorf("cause should redact the token, got %q", cause)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "claude-failing-agent")
			script := "#!/bin/sh\ncat > /dev/null\nprintf '%s' '" + tc.stderr + "' >&2\nexit " + fmt.Sprint(tc.exit) + "\n"
			if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			runner, err := agentcli.RunnerFromBinding("command", path+" -p {payload}")
			if err != nil {
				t.Fatal(err)
			}
			g := New(&fakeRunner{}, fakeDocs{}, t.TempDir(), "")
			g.backoff = func(int) time.Duration { return 0 }
			g.SetWarnWriter(nil)
			recs := captureTelemetry(g)
			res := g.Invoke(context.Background(), InvokeRequest{
				Binding: config.AgentBinding{Tools: "Read,Grep,Glob", Principles: config.PrinciplesNone},
				Section: "reviewer", Rubric: "judge", Payload: map[string]string{"story": "sty_1"},
				Expect: ExpectVerdict, Runner: runner, Skill: "test-skill", StoryID: "sty_1", Step: "done", Actor: "reviewer",
			})
			if res.Err == nil {
				t.Fatal("a runner that exits non-zero should fail the invocation")
			}
			if got := countKind(*recs, "agent-retry"); got != defaultReviewerAttempts {
				t.Fatalf("want %d agent-retry rows, got %d", defaultReviewerAttempts, got)
			}
			if got := countKind(*recs, "agent-failure"); got != 1 {
				t.Fatalf("want 1 agent-failure row, got %d", got)
			}
			for _, rec := range *recs {
				if rec.kind != "agent-retry" && rec.kind != "agent-failure" {
					continue
				}
				cause, _ := rec.data["cause"].(string)
				if cause == "" {
					t.Fatalf("%s row carries no cause: %v", rec.kind, rec.data)
				}
				tc.check(t, cause)
			}
		})
	}
}

// A named binding's own timeout bounds its dispatch (sty_446c38b7): a 1ms bound on
// a runner that blocks until cancelled surfaces DeadlineExceeded fast — proof the
// binding's timeout is applied, not the engine's 20m default (which would hang the
// test). The default-when-unset path is covered by config.TestTimeoutDuration.
func TestDispatchExecutor_honorsBindingTimeout(t *testing.T) {
	docs := fakeDocs{workflow: dispatchWF, skillBody: "alignment rubric", skillFound: true}
	g, _ := newEngine(t, "", docs)
	g.SetNamedAgents(func(name string) (config.AgentBinding, bool) {
		if name != "architect" {
			return config.AgentBinding{}, false
		}
		return config.AgentBinding{Command: "fake -p {system}", Tools: "Read,Grep,Glob,Bash(satelle:*)", Model: "fable", Timeout: "1ms"}, true
	})
	g.newRunner = func(string, string) (agentcli.Runner, error) { return &blockingRunner{}, nil }

	done := make(chan error, 1)
	go func() {
		_, err := g.DispatchExecutor(context.Background(), workitem.Item{ID: "sty_1", Status: "backlog"}, "plan")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
			t.Fatalf("want a deadline-exceeded dispatch failure from the 1ms binding timeout, got %v", err)
		}
	case <-time.After(testutil.WaitBudget):
		t.Fatal("the binding's 1ms timeout was not applied — dispatch did not return (fell back to the long default)")
	}
}

// A named executor whose dispatch runner errors records a structured
// agent-failure naming the agent, step, and classified outcome (AC2) — the
// coded capture of a killed sub-process only the binary observes.
func TestDispatchExecutor_recordsFailureTelemetry(t *testing.T) {
	docs := fakeDocs{workflow: dispatchWF, skillBody: "alignment rubric", skillFound: true}
	g, _ := newEngine(t, "", docs)
	g.SetNamedAgents(func(name string) (config.AgentBinding, bool) {
		if name != "architect" {
			return config.AgentBinding{}, false
		}
		return config.AgentBinding{Command: "fake -p {system}", Tools: "Read,Grep,Glob,Bash(satelle:*)", Model: "fable"}, true
	})
	g.newRunner = func(string, string) (agentcli.Runner, error) {
		return &fakeRunner{err: errors.New("agentcli: claude: signal: killed")}, nil
	}
	recs := captureTelemetry(g)

	if _, err := g.DispatchExecutor(context.Background(), workitem.Item{ID: "sty_1", Status: "backlog"}, "plan"); err == nil {
		t.Fatal("dispatch should fail when the binding runner errors")
	}
	if got := countKind(*recs, "agent-failure"); got != 1 {
		t.Fatalf("want 1 executor agent-failure event, got %d", got)
	}
	rec := (*recs)[0]
	if rec.actor != "executor" {
		t.Errorf("executor telemetry actor = %q, want executor", rec.actor)
	}
	if a, _ := rec.data["agent"].(string); a != "architect" {
		t.Errorf("telemetry agent = %q, want architect", a)
	}
	if s, _ := rec.data["step"].(string); s != "plan" {
		t.Errorf("telemetry step = %q, want plan", s)
	}
	if o, _ := rec.data["outcome"].(string); o != "signal:killed" {
		t.Errorf("telemetry outcome = %q, want signal:killed", o)
	}
	if c, _ := rec.data["cause"].(string); !strings.Contains(c, "signal: killed") {
		t.Errorf("telemetry cause = %q, want the runner error text", c)
	}
}

func TestGate_inLoopReviewerFailsLoud(t *testing.T) {
	g, _ := newEngine(t, `{"decision":"accept"}`, fakeDocs{workflow: testWorkflow, skillBody: "rubric body", skillFound: true})
	g.SetReviewerBinding(config.AgentBinding{Command: "in-loop", Role: config.RoleReviewer, Principles: config.PrinciplesNone})
	g.SetRunner(nil) // force re-check; Invoke also needs runner, but we fail earlier
	// Put a dummy runner so we hit the in-loop check before nil-runner.
	g.SetRunner(&fakeRunner{out: `{"decision":"accept"}`})
	dec, err := g.Gate(context.Background(), workitem.Item{ID: "sty_x", Status: "backlog"}, "in_progress")
	if err == nil {
		t.Fatalf("expected in-loop reviewer to fail, got accept=%v", dec.Accept)
	}
	if !strings.Contains(err.Error(), "in-loop") || !strings.Contains(err.Error(), "isolated verdict") {
		t.Errorf("error should name in-loop mechanism failure: %v", err)
	}
}

func TestIsNamedPerformer(t *testing.T) {
	if isNamedPerformer("planner", config.AgentBinding{Role: config.RoleAgent}) != true {
		t.Error("planner role=agent should perform")
	}
	if isNamedPerformer("strict", config.AgentBinding{Role: config.RoleReviewer}) != false {
		t.Error("named role=reviewer must not perform")
	}
	if isNamedPerformer("reviewer", config.AgentBinding{}) != false {
		t.Error("DSL reviewer token must not perform")
	}
}

// parkWF declares from="*" on blocked so parking works from every performing
// state; no resume edges — resume is origin-enforced (sty_f75286dc).
var parkWF = spineWF("blocked @park-gate", "cancelled @cancel", "",
	"in_progress|executor|code",
	"integration|executor|integrate",
	"release|executor|release",
	"done")

// TestGateParkResumeToOrigin (sty_f75286dc): parked from integration resumes
// only to integration (ungated); release is refused — no gate wormhole.
func TestGateParkResumeToOrigin(t *testing.T) {
	g := New(&fakeRunner{}, fakeDocs{workflow: parkWF, skillFound: false}, "/repo", "")
	item := workitem.Item{
		ID: "sty_park", Status: "blocked", ParkOrigin: "integration",
		Kind: workitem.KindStory, Tags: []string{},
	}
	// Resume to origin: ungated accept.
	dec, err := g.Gate(context.Background(), item, "integration")
	if err != nil {
		t.Fatalf("resume to origin: %v", err)
	}
	if dec.Gated {
		t.Error("resume must be ungated (no re-run of gates)")
	}
	// Wormhole to release: refused.
	_, err = g.Gate(context.Background(), item, "release")
	if err == nil {
		t.Fatal("expected refuse park→release")
	}
	if !strings.Contains(err.Error(), "origin") {
		t.Errorf("error should name origin, got: %v", err)
	}
	// Cancel still allowed (declared non-performing exit).
	_, err = g.Gate(context.Background(), item, "cancelled")
	if err != nil {
		t.Fatalf("park→cancelled should be allowed: %v", err)
	}
}

// TestGateParkFromIntegration (sty_f75286dc): parking from integration is a
// declared edge (materialized from from=*) — not "not a declared edge".
func TestGateParkFromIntegration(t *testing.T) {
	g := New(&fakeRunner{}, fakeDocs{workflow: parkWF, skillFound: false}, "/repo", "")
	item := workitem.Item{
		ID: "sty_park2", Status: "integration",
		Kind: workitem.KindStory, Tags: []string{},
	}
	_, err := g.Gate(context.Background(), item, "blocked")
	if err != nil {
		t.Fatalf("integration→blocked should be declared: %v", err)
	}
}

// --- parallel multi-reviewer gates (sty_4f0a15db) ---

// parallelWF is the two-reviewer entry gate. The cap belongs on the step the
// reviewers admit entry to; an empty attr leaves the route's own default, which
// is CONCURRENT above one reviewer (all-must-accept, no short-circuit).
func parallelWF(parallelAttr string) string {
	return spineWF("", "", "",
		"in_progress|executor||rev-a, rev-b|reviewer|"+parallelAttr,
		"done")
}

func parallelSkill(name string) string {
	return "---\nname: " + name + "\nscope: system\ntype: skill\ntags: [type:skill, type:reviewer]\ndescription: parallel test gate\n---\n\n# " + name + "\n\nReturn a verdict:\n\n```json\n{\"decision\": \"accept\", \"notes\": \"\"}\n```\n"
}

// concurrentMapRunner records max concurrency; returns per-skill verdicts. With
// rendezvous > 0 each run holds until that many runs have been in flight at once
// (bounded by testutil.WaitBudget), so overlap is guaranteed by construction and
// does not depend on how closely a slow machine starts the goroutines.
type concurrentMapRunner struct {
	mu         sync.Mutex
	inflight   int
	maxConc    int
	verdict    map[string]string
	calls      map[string]int
	rendezvous int
}

func newConcurrentMapRunner(verdict map[string]string) *concurrentMapRunner {
	return &concurrentMapRunner{verdict: verdict, calls: map[string]int{}}
}

func (r *concurrentMapRunner) Name() string    { return "conc" }
func (r *concurrentMapRunner) Command() string { return "conc" }
func (r *concurrentMapRunner) Run(_ context.Context, req agentcli.Request) ([]byte, error) {
	sk := "unknown"
	for _, name := range []string{"rev-a", "rev-b", "rev-c"} {
		if strings.Contains(req.SystemPrompt, name) {
			sk = name
			break
		}
	}
	r.mu.Lock()
	r.calls[sk]++
	r.inflight++
	if r.inflight > r.maxConc {
		r.maxConc = r.inflight
	}
	r.mu.Unlock()
	if r.rendezvous > 0 {
		deadline := time.Now().Add(testutil.WaitBudget)
		for time.Now().Before(deadline) {
			r.mu.Lock()
			met := r.maxConc >= r.rendezvous
			r.mu.Unlock()
			if met {
				break
			}
			time.Sleep(time.Millisecond) // poll tick
		}
	}
	r.mu.Lock()
	r.inflight--
	dec := r.verdict[sk]
	if dec == "" {
		dec = "accept"
	}
	r.mu.Unlock()
	return []byte(`{"decision":"` + dec + `","notes":"` + sk + `"}`), nil
}

func TestGateParallel_CollectsAllNoShortCircuit(t *testing.T) {
	docs := fakeDocs{
		workflow: parallelWF(""),
		extraSkills: []docindex.Doc{
			{Kind: "skills", Name: "rev-a", Body: parallelSkill("rev-a")},
			{Kind: "skills", Name: "rev-b", Body: parallelSkill("rev-b")},
		},
	}
	runner := newConcurrentMapRunner(map[string]string{"rev-b": "reject"})
	runner.rendezvous = 2
	g := New(runner, docs, "/repo", "")
	g.backoff = func(int) time.Duration { return 0 }

	dec, err := g.Gate(context.Background(), workitem.Item{ID: "sty_p", Status: "backlog", Category: "feature"}, "in_progress")
	if err != nil {
		t.Fatalf("Gate: %v", err)
	}
	if len(dec.Reviewers) != 2 {
		t.Fatalf("parallel must collect all N verdicts, got %d: %+v", len(dec.Reviewers), dec.Reviewers)
	}
	if dec.Reviewers[0].Order != 0 || dec.Reviewers[1].Order != 1 {
		t.Errorf("deterministic order broken: %+v", dec.Reviewers)
	}
	if dec.Reviewers[0].Skill != "rev-a" || dec.Reviewers[1].Skill != "rev-b" {
		t.Errorf("skills order: %+v", dec.Reviewers)
	}
	if dec.Reviewers[1].Accept {
		t.Error("rev-b should reject")
	}
	runner.mu.Lock()
	maxc := runner.maxConc
	callsA, callsB := runner.calls["rev-a"], runner.calls["rev-b"]
	runner.mu.Unlock()
	if callsA != 1 || callsB != 1 {
		t.Errorf("each skill once: a=%d b=%d", callsA, callsB)
	}
	if maxc < 2 {
		t.Errorf("expected concurrent in-flight ≥2, got maxConc=%d", maxc)
	}
}

func TestGateSerial_ShortCircuitFirstReject(t *testing.T) {
	docs := fakeDocs{
		// parallel: 0 is how a route asks for sequential first-reject; unset means
		// concurrent, which is the shape's default above one reviewer.
		workflow: parallelWF("0"),
		extraSkills: []docindex.Doc{
			{Kind: "skills", Name: "rev-a", Body: parallelSkill("rev-a")},
			{Kind: "skills", Name: "rev-b", Body: parallelSkill("rev-b")},
		},
	}
	runner := newConcurrentMapRunner(map[string]string{"rev-a": "reject"})
	g := New(runner, docs, "/repo", "")
	g.backoff = func(int) time.Duration { return 0 }

	dec, err := g.Gate(context.Background(), workitem.Item{ID: "sty_s", Status: "backlog", Category: "feature"}, "in_progress")
	if err != nil {
		t.Fatalf("Gate: %v", err)
	}
	if len(dec.Reviewers) != 1 {
		t.Fatalf("serial short-circuit should stop after first reject, got %d", len(dec.Reviewers))
	}
	if dec.Reviewers[0].Accept {
		t.Error("expected reject")
	}
	runner.mu.Lock()
	callsB := runner.calls["rev-b"]
	runner.mu.Unlock()
	if callsB != 0 {
		t.Errorf("rev-b must not run after rev-a reject, calls=%d", callsB)
	}
}

func TestReviewerSkillsFor_Parallel(t *testing.T) {
	// Unset parallel with two or more reviewers defaults to the cap: concurrency
	// is the shape's rule, not an authored opt-in.
	skills, _, par, declared, _, _ := specReviewerSkills(fixtureSpec(t, parallelWF("")), "backlog", "in_progress")
	if !declared || len(skills) != 2 {
		t.Fatalf("skills=%v declared=%v", skills, declared)
	}
	if par != wfdot.DefaultParallelCap {
		t.Errorf("unset parallel → cap %d, want %d", par, wfdot.DefaultParallelCap)
	}
	_, _, par2, _, _, _ := specReviewerSkills(fixtureSpec(t, parallelWF("2")), "backlog", "in_progress")
	if par2 != 2 {
		t.Errorf("parallel=2 → %d", par2)
	}
	body3 := parallelWF("")
	_, _, par3, _ := reviewerSkillsFor(body3, "backlog", "in_progress")
	if par3 != 0 {
		t.Errorf("absent → %d", par3)
	}
}

func TestGateRefusesPerformerRoleOnEdge(t *testing.T) {
	wf := spineWF("", "", "", "done|||rev-a|coder-x")
	g, _ := newEngine(t, `{"decision":"accept","notes":"ok"}`, fakeDocs{
		workflow: wf, skillBody: "rubric", skillFound: true,
	})
	g.SetNamedAgents(func(name string) (config.AgentBinding, bool) {
		if name == "coder-x" {
			return config.AgentBinding{Command: agentcli.DefaultClaudeCommand, Tools: "Read", Role: "agent"}, true
		}
		return config.AgentBinding{}, false
	})
	_, err := g.Gate(context.Background(), workitem.Item{ID: "sty_x", Status: "backlog"}, "done")
	if err == nil || !strings.Contains(err.Error(), "role=") {
		t.Fatalf("want role refusal, got %v", err)
	}
}

func TestDispatchRefusesReviewerRoleOnPerformingNode(t *testing.T) {
	wf := spineWF("", "", "",
		"work|reviewer-deep|code",
		"done")
	g, r := newEngine(t, `{"decision":"accept","notes":"ok"}`, fakeDocs{
		workflow: wf, skillBody: "rubric", skillFound: true,
	})
	called := false
	g.SetNamedAgents(func(name string) (config.AgentBinding, bool) {
		if name == "reviewer-deep" {
			return config.AgentBinding{Command: agentcli.DefaultClaudeCommand, Tools: "Read", Role: "reviewer", Model: "opus"}, true
		}
		return config.AgentBinding{}, false
	})
	// Replace runner to detect invoke
	orig := r
	_ = orig
	_, err := g.DispatchExecutor(context.Background(), workitem.Item{ID: "sty_x", Status: "backlog"}, "work")
	if err == nil || !strings.Contains(err.Error(), "role=reviewer") {
		t.Fatalf("want reviewer-on-performing refuse, got %v", err)
	}
	_ = called
}

// TestGateUnknownInterfaceRejected (sty_e3eca0b7): a gate whose named agent
// uses a retired/unknown interface (e.g. "typesafe") fails with the generic
// RunnerFromBinding unknown-interface error — no engine special case.
func TestGateUnknownInterfaceRejected(t *testing.T) {
	wf := spineWF("", "",
		`[[gate]]
skill = "satelle-estimate-actual-review"
agent = "reviewer-unknown"
on = ["in_progress"]
`,
		"in_progress|executor|code|satelle-story-intent-review",
		"done|||satelle-story-done-review")

	docs := fakeDocs{
		workflow:   wf,
		skillFound: true,
		extraSkills: []docindex.Doc{
			skillDoc("satelle-story-intent-review"),
			skillDoc("satelle-estimate-actual-review"),
			skillDoc("code"),
		},
	}
	g, _ := newEngine(t, `{"decision":"accept"}`, docs)
	g.newRunner = LookupRunner
	g.SetNamedAgents(func(name string) (config.AgentBinding, bool) {
		if name == "reviewer-unknown" {
			// Non-in-loop command so the engine reaches RunnerFromBinding; true is on PATH.
			return config.AgentBinding{
				Interface: "typesafe",
				Command:   "true -p {system}",
				Role:      config.RoleReviewer,
				Tools:     "Read,Grep,Glob",
			}, true
		}
		return config.AgentBinding{}, false
	})

	_, err := g.Gate(context.Background(), workitem.Item{
		ID: "sty_plain", Status: "backlog", Tags: []string{"surface:cli"},
	}, "in_progress")
	if err == nil || !strings.Contains(err.Error(), "unknown interface") {
		t.Fatalf("want unknown-interface error, got %v", err)
	}
	if !strings.Contains(err.Error(), "typesafe") {
		t.Fatalf("error should name the interface, got %v", err)
	}
}

// TestGateBindingEmptyDegradesToReviewer pins sty_4cebc624 AC4: an omitted
// agent= on a gate (Agent == "") resolves to [reviewer] rather than erroring.
func TestGateBindingEmptyDegradesToReviewer(t *testing.T) {
	g := New(&fakeRunner{}, fakeDocs{}, "/repo", "opus")
	g.reviewerBinding = config.AgentBinding{Command: "claude", Model: "opus"}
	for _, sec := range []string{"", "reviewer", "  "} {
		b, name, err := g.gateBinding(sec)
		if err != nil {
			t.Fatalf("gateBinding(%q) err: %v", sec, err)
		}
		if name != "reviewer" {
			t.Errorf("gateBinding(%q) name=%q, want reviewer", sec, name)
		}
		if b.Model != "opus" {
			t.Errorf("gateBinding(%q) model=%q, want opus", sec, b.Model)
		}
	}
}

// TestCodedCheckScopedGateAgentNeutral pins sty_4cebc624 AC4: a coded-check
// scoped on= node with agent= and the same node without agent= produce
// byte-identical GateDecision JSON for both accept and reject check scripts.
// The early return before gateBinding means agent= is never read.
func TestCodedCheckScopedGateAgentNeutral(t *testing.T) {
	codedSkill := `---
name: fixture-coded
type: skill
description: coded check fixture
---

# Fixture

` + "```check\n#!/bin/sh\nexit 0\n```\n"

	mkWF := func(agentAttr string) string {
		// agentAttr is either `agent = "reviewer"` or empty.
		return spineWF("", "",
			`[[gate]]
skill = "fixture-coded"
`+agentAttr+"on = [\"done\"]\n\n",
			"in_progress|executor|code",
			"done")
	}

	run := func(t *testing.T, wf string, checkOK bool) verb.GateDecision {
		t.Helper()
		docs := fakeDocs{
			workflow:   wf,
			skillFound: true,
			extraSkills: []docindex.Doc{
				{Kind: "skills", Name: "fixture-coded", Body: codedSkill},
				{Kind: "skills", Name: "code", Body: conformantSkill("code", "implement")},
			},
		}
		g, r := newEngine(t, `{"decision":"accept"}`, docs)
		g.check = func(_ context.Context, dir, command, payload string) (string, error) {
			if checkOK {
				return "ok\n", nil
			}
			return "FAIL\n", errFakeExit
		}
		dec, err := g.Gate(context.Background(), workitem.Item{ID: "sty_t", Status: "in_progress"}, "done")
		if err != nil {
			t.Fatal(err)
		}
		if r.got.SystemPrompt != "" {
			t.Error("LLM must not run for coded-check gate")
		}
		return dec
	}

	for _, ok := range []bool{true, false} {
		name := "accept"
		if !ok {
			name = "reject"
		}
		t.Run(name, func(t *testing.T) {
			with := run(t, mkWF("agent = \"reviewer\"\n"), ok)
			without := run(t, mkWF(""), ok)
			if with.Accept != without.Accept || with.Gated != without.Gated {
				t.Fatalf("accept/gated differ: with=%+v without=%+v", with, without)
			}
			// Notes for functional checks include the command / output; require equal.
			bw, err := json.Marshal(with)
			if err != nil {
				t.Fatal(err)
			}
			bo, err := json.Marshal(without)
			if err != nil {
				t.Fatal(err)
			}
			if string(bw) != string(bo) {
				t.Fatalf("GateDecision not byte-identical:\nwith:    %s\nwithout: %s", bw, bo)
			}
			if ok && !with.Accept {
				t.Fatal("want accept")
			}
			if !ok && with.Accept {
				t.Fatal("want reject")
			}
		})
	}
}

// TestStructureRefusalNamesTheOpenDiagnosis (sty_88d40a60 AC2): when the
// indexer has already raised a story about the document that will not parse, the
// refusal names it — the operator is handed the diagnosis instead of hunting the
// symptom. With no such story (an unwired resolver) the refusal is unchanged.
func TestStructureRefusalNamesTheOpenDiagnosis(t *testing.T) {
	broken := strings.Replace(spineWF("", "", "", "in_progress|executor", "done"),
		"description = \"test declaration of done\"\nscope = \"system\"\n", "", 1)
	item := workitem.Item{ID: "sty_1", Status: "backlog"}

	// Unwired: the refusal fires and names no story.
	g, _ := newEngine(t, `{"decision":"accept"}`, fakeDocs{workflow: broken, skillBody: "rubric", skillFound: true})
	_, err := g.Gate(context.Background(), item, "in_progress")
	if err == nil {
		t.Fatal("want the gate refused under a structurally broken workflow")
	}
	silent := err.Error()
	if strings.Contains(silent, "already diagnosed") {
		t.Fatalf("no resolver wired, so no story may be named:\n%s", silent)
	}

	// Wired: the same refusal points at the open story for that document.
	var askedKind, askedName string
	g2, _ := newEngine(t, `{"decision":"accept"}`, fakeDocs{workflow: broken, skillBody: "rubric", skillFound: true})
	g2.SetTrackingStoryResolver(func(_ context.Context, kind, name string) string {
		askedKind, askedName = kind, name
		return "sty_906f59df"
	})
	_, err = g2.Gate(context.Background(), item, "in_progress")
	if err == nil {
		t.Fatal("want the gate refused under a structurally broken workflow")
	}
	named := err.Error()
	for _, want := range []string{"structure validation", "sty_906f59df", "satelle story get sty_906f59df"} {
		if !strings.Contains(named, want) {
			t.Errorf("refusal missing %q:\n%s", want, named)
		}
	}
	if askedKind != "workflows" || askedName == "" {
		t.Errorf("resolver asked for (%q, %q), want the workflow document being judged", askedKind, askedName)
	}
	var ref wfgovern.Refusal
	if !errors.As(err, &ref) || ref.TrackingStory != "sty_906f59df" {
		t.Errorf("typed refusal must carry the story id, got %+v", ref)
	}
}

// TestDispatchExecutorPayloadCarriesChildren (sty_b91dc1ac): a dispatched
// performer's stdin payload carries the container's resolved children, on the
// plain invoke path and on the artifact-attempt path, through a generic command
// binding; with no resolver (or no children) the payload has no children key.
func TestDispatchExecutorPayloadCarriesChildren(t *testing.T) {
	attemptSkill := strings.Replace(contractedDispatchSkill, "output_ac_coverage: true", "output_ac_coverage: true\nattempt_repair_max: 1", 1)
	resolved := []ChildState{{ID: "sty_child1", Status: "done"}, {ID: "sty_child2", Status: "backlog"}}
	two := func(context.Context, workitem.Item) []ChildState { return resolved }
	tests := []struct {
		name     string
		skill    string
		out      string
		resolver func(context.Context, workitem.Item) []ChildState
		want     []ChildState
	}{
		{"plain invoke", "alignment rubric", "did the work", two, resolved},
		{"attempt policy", attemptSkill, `{"artifact":{"body":"## AC1\ncovered\n\n## AC2\ncovered"}}`, two, resolved},
		{"no resolver", "alignment rubric", "did the work", nil, nil},
		{"no children", "alignment rubric", "did the work", func(context.Context, workitem.Item) []ChildState { return nil }, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			docs := fakeDocs{workflow: dispatchWF, skillBody: tc.skill, skillFound: true}
			g, _ := newEngine(t, "", docs)
			r := &fakeRunner{out: tc.out}
			g.newRunner = func(string, string) (agentcli.Runner, error) { return r, nil }
			g.SetNamedAgents(func(string) (config.AgentBinding, bool) {
				return config.AgentBinding{Command: "fake -p {system}", Tools: "read_file,grep,list_dir"}, true
			})
			g.SetArtifactAttacher(func(_ context.Context, _ workitem.Item, name, typ, _ string) (string, string, error) {
				return name, typ, nil
			})
			if tc.resolver != nil {
				g.SetChildrenResolver(tc.resolver)
			}
			if _, err := g.DispatchExecutor(context.Background(), workitem.Item{
				ID: "sty_epic", Status: "backlog", Category: "epic-parent", AcceptanceCriteria: "1. first\n2. second",
			}, "plan"); err != nil {
				t.Fatal(err)
			}
			var raw map[string]any
			if err := json.Unmarshal([]byte(r.got.Payload), &raw); err != nil {
				t.Fatalf("payload is not JSON: %v\n%s", err, r.got.Payload)
			}
			if tc.want == nil {
				if _, ok := raw["children"]; ok {
					t.Fatalf("payload must carry no children key:\n%s", r.got.Payload)
				}
				return
			}
			var got struct {
				Children []ChildState `json:"children"`
			}
			if err := json.Unmarshal([]byte(r.got.Payload), &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Children, tc.want) {
				t.Fatalf("children = %+v, want %+v", got.Children, tc.want)
			}
		})
	}
}
