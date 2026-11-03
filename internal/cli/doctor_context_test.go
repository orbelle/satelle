package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/agentstep"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/doctor"
	"github.com/bobmcallan/satelle/internal/health"
)

func checksFor(f *ctxFixture) health.Findings {
	return contextChecks(context.Background(), f.repo, f.dataDir)
}

func byID(fs health.Findings, id string) health.Findings {
	var out health.Findings
	for _, f := range fs {
		if f.ID == id {
			out = append(out, f)
		}
	}
	return out
}

// seatOnly keeps the findings about exactly this seat label. mentioning matches a
// substring, so "reviewer[claude/command]" would also pick up ready-reviewer's.
func seatOnly(fs health.Findings, label string) health.Findings {
	var out health.Findings
	for _, f := range fs {
		if strings.HasPrefix(f.Detail, label) {
			out = append(out, f)
		}
	}
	return out
}

func mentioning(fs health.Findings, parts ...string) health.Findings {
	var out health.Findings
next:
	for _, f := range fs {
		for _, p := range parts {
			if !strings.Contains(f.Detail, p) {
				continue next
			}
		}
		out = append(out, f)
	}
	return out
}

// --- AC1: SessionStart fit per bound harness --------------------------------

// A 9500-byte harness and a resident set between 9500 and 16384 bytes: the set
// the old largest-harness ceiling passed. Doctor now names the harness, its
// limit, the rendered size and each document omitted to a read pointer.
func TestDoctorReportsSessionOverflowForTheBoundHarness(t *testing.T) {
	f := residentFixture(t)
	got := byID(checksFor(f), health.IDContextSessionOverflow)
	if len(got) != 1 {
		t.Fatalf("want one overflow finding (claude is the only bound harness), got %+v", got)
	}
	fd := got[0]
	if fd.Severity != health.SeverityWarn {
		t.Errorf("severity = %s, want warn", fd.Severity)
	}
	content, omitted := renderSessionContext(f.dataDir, harnessBinding{Name: "claude", Limit: 9500, Unit: "bytes"})
	if len(omitted) == 0 {
		t.Fatal("fixture must overflow")
	}
	for _, want := range append([]string{"harness claude", "limit of 9500 bytes", "renders " + strconv.Itoa(len(content)) + " bytes"}, omitted...) {
		if !strings.Contains(fd.Detail, want) {
			t.Errorf("finding must name %q, got: %s", want, fd.Detail)
		}
	}
}

// The same resident set on a roomy bound harness is silent.
func TestDoctorSessionFitQuietWhenEveryBoundHarnessFits(t *testing.T) {
	f := residentFixture(t)
	f.write("satelle.toml", "[harness.claude]\ncontext_limit_bytes = 1000000\n")
	if got := byID(checksFor(f), health.IDContextSessionOverflow); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

// A grok seat delivers on a tool event, so it is judged in characters against
// tool_context_limit_chars — the same unit and limit the hook applies.
func TestDoctorSessionFitUsesTheToolEventClipForGrok(t *testing.T) {
	f := residentFixture(t)
	f.write("workflows/agents.toml", "[executor]\ncommand = \"in-loop\"\n\n[planner]\nrole = \"agent\"\ncommand = \""+grokSeatCmd+"\"\n")
	f.write("satelle.toml", "[harness.claude]\ncontext_limit_bytes = 1000000\n\n[harness.grok]\ntool_context_limit_chars = 2000\n")
	got := mentioning(byID(checksFor(f), health.IDContextSessionOverflow), "harness grok")
	if len(got) != 1 || !strings.Contains(got[0].Detail, "2000 characters") || !strings.Contains(got[0].Remediation, "tool_context_limit_chars") {
		t.Fatalf("want a grok overflow in characters naming its own key, got %+v", got)
	}
}

// --- AC7: unavailable rows worded after the lookup that failed ---------------

func TestDoctorHarnessUnavailableIsWordedAfterTheFailedLookup(t *testing.T) {
	f := newCtxFixture(t, "[executor]\ncommand = \"in-loop\"\n\n[reviewer]\nrole = \"reviewer\"\ncommand = \"mystery-cli -p {system}\"\n")
	got := byID(checksFor(f), health.IDContextSessionUnavailable)
	seat := mentioning(got, "seat [reviewer]", "not detected by any adapter")
	if len(seat) != 1 || seat[0].Severity != health.SeverityInfo || !strings.Contains(seat[0].Detail, "unavailable") {
		t.Fatalf("want an INFO unavailable row for the unrecognised seat, got %+v", got)
	}
}

func TestDoctorSessionUnavailableWhenAHarnessHasNoTable(t *testing.T) {
	_, gap := harnessBudget(config.Config{}, "zed")
	if gap == nil {
		t.Fatal("expected a gap")
	}
	f := &contextAudit{gaps: []harnessGap{*gap}}
	got := f.sessionFindings()
	if len(got) != 1 || got[0].ID != health.IDContextSessionUnavailable || !strings.Contains(got[0].Detail, "no [harness.zed] table") {
		t.Fatalf("got %+v", got)
	}
}

// --- sty_58a7bdfe AC4/AC5: no seat is sized against a budget -----------------

// The per-prompt reminder is judged only against its harness's declared clip
// (a channel limit, like the SessionStart cap), and only an overrun is reported.
func TestDoctorPromptReminderOverTheHarnessClip(t *testing.T) {
	f := newCtxFixture(t, "")
	f.write("workflows/agents.toml", f.claudeAgents())
	if got := mentioning(checksFor(f), "reminder[claude]"); len(got) != 0 {
		t.Fatalf("a reminder with no declared clip is not reported, got %+v", got)
	}
	f.write("satelle.toml", "[harness.claude]\ntool_context_limit_chars = 10\n")
	got := mentioning(byID(checksFor(f), health.IDContextReminderOverClip), "reminder[claude]", "tool_context_limit_chars = 10")
	if len(got) != 1 || got[0].Severity != health.SeverityWarn {
		t.Fatalf("want a WARN against the harness clip, got %+v", got)
	}
	f.write("satelle.toml", "[harness.claude]\ntool_context_limit_chars = 1000000\n")
	if got := mentioning(checksFor(f), "reminder[claude]"); len(got) != 0 {
		t.Fatalf("a reminder within its clip is quiet, got %+v", got)
	}
}

// --- AC4: verbatim duplication ------------------------------------------------

const fixtureNamingPassage = "A substrate artifact name encodes its type so owner and kind read from the name alone: " +
	"every reviewer follows object function and a bare object review is that object's structure reviewer."

func duplicateFixture(t *testing.T, constitution string) *ctxFixture {
	t.Helper()
	f := newCtxFixture(t, "")
	f.write("workflows/agents.toml", f.claudeAgents())
	f.principle("satelle-fixture-naming", "# Naming\n\n"+fixtureNamingPassage, false)
	f.write("constitution.md", "# Constitution\n\n"+constitution+"\n")
	return f
}

func TestDoctorFindsConstitutionCopyOfAnUnlinkedOnDemandPrinciple(t *testing.T) {
	f := duplicateFixture(t, "## Substrate naming\n\n"+fixtureNamingPassage)
	got := mentioning(byID(checksFor(f), health.IDContextDuplicateVerbatim), "satelle-fixture-naming")
	if len(got) != 1 {
		t.Fatalf("want exactly the constitution/principle pair, got %+v", got)
	}
	d := got[0].Detail
	for _, want := range []string{".satelle/constitution.md", ".satelle/principles/satelle-fixture-naming.md", "encodes its type so owner and kind read from the name alone"} {
		if !strings.Contains(d, want) {
			t.Errorf("must name both documents and quote the passage; missing %q in: %s", want, d)
		}
	}
	if got[0].Severity != health.SeverityWarn {
		t.Errorf("severity = %s, want warn", got[0].Severity)
	}
}

func TestDoctorIgnoresAReferenceToThePrinciple(t *testing.T) {
	f := duplicateFixture(t, "## Substrate naming\n\nA name encodes the artifact's type — see [[satelle-fixture-naming]].")
	if got := mentioning(byID(checksFor(f), health.IDContextDuplicateVerbatim), "satelle-fixture-naming"); len(got) != 0 {
		t.Fatalf("a reference by [[name]] is not duplication, got %+v", got)
	}
}

// The threshold is configuration, and a repo overrides the embedded default.
func TestDoctorDuplicateThresholdIsConfiguration(t *testing.T) {
	f := duplicateFixture(t, "## Substrate naming\n\n"+fixtureNamingPassage)
	f.write("satelle.toml", "[doctor]\nduplicate_min_words = 500\n")
	if got := mentioning(byID(checksFor(f), health.IDContextDuplicateVerbatim), "satelle-fixture-naming"); len(got) != 0 {
		t.Fatalf("a passage shorter than the configured threshold must not be reported, got %+v", got)
	}
}

// --- AC5: references -----------------------------------------------------------

func referenceFixture(t *testing.T) *ctxFixture {
	t.Helper()
	f := newCtxFixture(t, "")
	f.write("workflows/agents.toml", f.claudeAgents())
	f.write("skills/satelle-orphan-skill.md", "---\nname: satelle-orphan-skill\ntype: skill\n---\n# Orphan\n\nNothing wires me.\n")
	f.write("documents/real.md", "# real\n")
	f.write("documents/index.md", "---\nokf_version: \"0.1\"\n---\n\n# Documents\n\n* [Real](real.md)\n* [Gone](deleted-doc.md) - removed\n")
	f.write("constitution.md", "# Constitution\n\n"+
		"Unresolved: `satelle-ghost-skill`. Retired: the `satelle-skill-review` reviewer. "+
		"Dangling: [[satelle-missing-link]]. Unwired: `satelle-orphan-skill`. "+
		"Task-wired: `satelle-context-audit`. Live: [[satelle-agent-goals]].\n")
	return f
}

func TestDoctorReportsUnresolvedRetiredUnwiredAndStaleIndexReferences(t *testing.T) {
	got := checksFor(referenceFixture(t))

	unresolved := byID(got, health.IDContextRefUnresolved)
	for _, ref := range []string{"`satelle-ghost-skill`", "`satelle-skill-review`", "[[satelle-missing-link]]"} {
		m := mentioning(unresolved, ".satelle/constitution.md", ref)
		if len(m) != 1 || m[0].Severity != health.SeverityWarn {
			t.Errorf("want one WARN naming the referring file and %s, got %+v", ref, m)
		}
	}
	if m := mentioning(unresolved, "`satelle-skill-review`", "retired in"); len(m) != 1 {
		t.Errorf("a retired name is reported as retired, got %+v", m)
	}

	unwired := byID(got, health.IDContextRefUnwired)
	if m := mentioning(unwired, ".satelle/constitution.md", "`satelle-orphan-skill`"); len(m) != 1 || m[0].Severity != health.SeverityInfo {
		t.Errorf("want the orphan skill reported unwired, got %+v", unwired)
	}
	// A skill a task wires (tsk_context-audit → satelle-context-audit) is not.
	if m := mentioning(got, "satelle-context-audit"); len(m) != 0 {
		t.Errorf("a task-wired skill must not be reported, got %+v", m)
	}
	// A live link reports nothing.
	if m := mentioning(got, "[[satelle-agent-goals]]"); len(m) != 0 {
		t.Errorf("a resolving link must not be reported, got %+v", m)
	}

	stale := byID(got, health.IDContextIndexStale)
	if len(stale) != 1 || !strings.Contains(stale[0].Detail, "deleted-doc.md") {
		t.Errorf("want the deleted index entry, got %+v", stale)
	}
}

// A skill reachable from the route is read for references even though no
// context carries it.
func TestDoctorReadsReferencesInSkillsTheRouteWires(t *testing.T) {
	f := newCtxFixture(t, "")
	f.write("workflows/agents.toml", f.claudeAgents())
	var wired string
	for _, d := range config.EmbeddedDefaults() {
		if d.Kind == "workflows" && d.Name == "step" {
			for _, s := range agentstep.ReferencedWorkflowSkills(d.Body) {
				for _, e := range config.EmbeddedDefaults() {
					if e.Kind == "skills" && e.Name == s {
						wired = s
					}
				}
			}
		}
	}
	if wired == "" {
		t.Skip("the embedded route wires no embedded skill")
	}
	f.write("skills/"+wired+".md", "---\nname: "+wired+"\ntype: skill\ndescription: override\ntags: [type:skill]\n---\n# Override\n\nCalls `satelle-not-a-thing`.\n")
	got := mentioning(byID(checksFor(f), health.IDContextRefUnresolved), ".satelle/skills/"+wired+".md", "`satelle-not-a-thing`")
	if len(got) != 1 {
		t.Fatalf("a wired skill's references are checked, got %+v", got)
	}
}

// --- AC6: the opt-in semantic review -------------------------------------------

type fakeReviewRunner struct {
	mu    sync.Mutex
	reqs  []agentcli.Request
	reply string
	err   error
}

func (r *fakeReviewRunner) Name() string    { return "fake" }
func (r *fakeReviewRunner) Command() string { return "fake" }
func (r *fakeReviewRunner) Run(_ context.Context, req agentcli.Request) ([]byte, error) {
	r.mu.Lock()
	r.reqs = append(r.reqs, req)
	r.mu.Unlock()
	return []byte(r.reply), r.err
}

func (r *fakeReviewRunner) calls() []agentcli.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]agentcli.Request(nil), r.reqs...)
}

// stubReviewer installs a runner for the reviewer binding and returns it with the
// number of times the factory was asked for one.
func stubReviewer(t *testing.T, reply string, err error) (*fakeReviewRunner, *atomic.Int32) {
	t.Helper()
	r := &fakeReviewRunner{reply: reply, err: err}
	var built atomic.Int32
	contextReviewRunner = func(string, string) (agentcli.Runner, error) {
		built.Add(1)
		return r, nil
	}
	t.Cleanup(func() { contextReviewRunner = nil })
	return r, &built
}

const cannedVerdict = `{"findings":[{"part":"constitution/constitution","restates":"principles/satelle-fixture-naming","kind":"paraphrase","quote":"names carry their type so you can read owner and kind off them"}]}`

func reviewFixture(t *testing.T) *ctxFixture {
	t.Helper()
	return duplicateFixture(t, "## Substrate naming\n\nNames carry their type so you can read owner and kind off them.")
}

func TestContextReviewReportsTheReviewersRestatements(t *testing.T) {
	r, _ := stubReviewer(t, cannedVerdict, nil)
	f := reviewFixture(t)
	got := contextReview(context.Background(), f.repo, f.dataDir)

	hits := byID(got, health.IDContextDuplicateSemantic)
	if len(hits) != 1 {
		t.Fatalf("a restatement two contexts both carry is reported once, got %+v", got)
	}
	d := hits[0].Detail
	for _, want := range []string{".satelle/constitution.md", ".satelle/principles/satelle-fixture-naming.md", "names carry their type"} {
		if !strings.Contains(d, want) {
			t.Errorf("must name both documents and the guidance; missing %q in: %s", want, d)
		}
	}
	if hits[0].Severity != health.SeverityWarn {
		t.Errorf("severity = %s", hits[0].Severity)
	}

	// The payload is the context plus the WHOLE corpus: the on-demand principle
	// the context never receives is in it, with its residency and body.
	reqs := r.calls()
	if len(reqs) == 0 {
		t.Fatal("the reviewer was never run")
	}
	var p reviewPayload
	if err := json.Unmarshal([]byte(reqs[0].Payload), &p); err != nil {
		t.Fatalf("payload is not the documented shape: %v", err)
	}
	var found bool
	for _, d := range p.Corpus {
		if d.Source == "principles/satelle-fixture-naming" {
			found = d.Residency == "ondemand" && strings.Contains(d.Body, "encodes its type")
		}
	}
	if !found {
		t.Errorf("the corpus index must carry the on-demand principle with residency and body")
	}
	var sawEmbedded bool
	for _, d := range p.Corpus {
		if strings.HasPrefix(d.Where, "embedded:") {
			sawEmbedded = true
		}
	}
	if !sawEmbedded {
		t.Error("the corpus index must carry the embedded defaults too")
	}
	if !reqs[0].ReadOnly {
		t.Error("the review must be read-only")
	}
}

// The output contract lives in the rubric, not in Go, and an authored rubric
// replaces the embedded one.
func TestContextReviewResolvesTheRubricThroughSkillBodyResolver(t *testing.T) {
	r, _ := stubReviewer(t, `{"findings":[]}`, nil)
	f := reviewFixture(t)

	contextReview(context.Background(), f.repo, f.dataDir)
	embedded := r.calls()
	if len(embedded) == 0 || !strings.Contains(embedded[0].SystemPrompt, "Doctor payload") ||
		!strings.Contains(embedded[0].SystemPrompt, `"restates"`) {
		t.Fatalf("the embedded rubric carries the default output contract, got prompt starting %.200q", embedded[0].SystemPrompt)
	}

	f.write("skills/satelle-context-audit.md", "---\nname: satelle-context-audit\ntype: skill\ndescription: authored\ntags: [type:skill]\n---\n# Authored\n\nAUTHORED-RUBRIC-MARKER reply {\"findings\": []}.\n")
	r2, _ := stubReviewer(t, `{"findings":[]}`, nil)
	contextReview(context.Background(), f.repo, f.dataDir)
	authored := r2.calls()
	if len(authored) == 0 || !strings.Contains(authored[0].SystemPrompt, "AUTHORED-RUBRIC-MARKER") ||
		strings.Contains(authored[0].SystemPrompt, "Doctor payload") {
		t.Fatalf("the authored rubric must replace the embedded one")
	}
}

func TestContextReviewUnparseableOutputIsUnavailableNamingTheAdapter(t *testing.T) {
	stubReviewer(t, "I found nothing to report.", nil)
	f := reviewFixture(t)
	got := byID(contextReview(context.Background(), f.repo, f.dataDir), health.IDContextReviewUnavailable)
	if len(got) == 0 || got[0].Severity != health.SeverityInfo ||
		!strings.Contains(got[0].Detail, "unavailable: claude adapter") || !strings.Contains(got[0].Detail, "did not parse") {
		t.Fatalf("got %+v", got)
	}
}

func TestContextReviewSpawnFailureIsUnavailableNamingTheAdapter(t *testing.T) {
	contextReviewRunner = func(string, string) (agentcli.Runner, error) { return nil, errors.New("exec: \"claude\": not found") }
	t.Cleanup(func() { contextReviewRunner = nil })
	f := reviewFixture(t)
	got := byID(contextReview(context.Background(), f.repo, f.dataDir), health.IDContextReviewUnavailable)
	if len(got) == 0 || !strings.Contains(got[0].Detail, "unavailable: claude adapter") || !strings.Contains(got[0].Detail, "not found") {
		t.Fatalf("got %+v", got)
	}
}

// Ordinary doctor spawns no agent: the reviewer binding is never even built.
func TestPlainDoctorSpawnsNoReviewer(t *testing.T) {
	_, built := stubReviewer(t, cannedVerdict, nil)
	f := reviewFixture(t)
	rep := doctor.Check(context.Background(), doctor.Opts{
		RepoRoot: f.repo, DataDir: f.dataDir,
		ContextChecks: contextChecks, ContextReviewer: contextReview,
	})
	if n := built.Load(); n != 0 {
		t.Fatalf("plain doctor built %d reviewer runner(s), want none", n)
	}
	if got := byID(rep.Findings, health.IDContextDuplicateSemantic); len(got) != 0 {
		t.Fatalf("no semantic finding without the flag, got %+v", got)
	}
	var ran bool
	for _, fd := range rep.Findings {
		ran = ran || strings.HasPrefix(fd.ID, "context.")
	}
	if !ran {
		t.Error("the deterministic context checks must run in plain doctor")
	}

	rep = doctor.Check(context.Background(), doctor.Opts{
		RepoRoot: f.repo, DataDir: f.dataDir, ContextReview: true,
		ContextChecks: contextChecks, ContextReviewer: contextReview,
	})
	if built.Load() == 0 {
		t.Fatal("--context-review must run the reviewer")
	}
	if got := byID(rep.Findings, health.IDContextDuplicateSemantic); len(got) != 1 {
		t.Fatalf("the flag must surface the reviewer's restatement, got %+v", got)
	}
	if !rep.OK {
		t.Errorf("every context finding is advisory; the report must stay OK: %+v", rep.Findings.WithSeverity(health.SeverityError))
	}
}

func TestDoctorCommandDeclaresTheContextReviewFlag(t *testing.T) {
	out, err := runRoot(t, "doctor", "--help")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "--context-review") {
		t.Fatalf("doctor --help must list --context-review:\n%s", out)
	}
}
