package agentstep

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// sty_23e10d92: an edge that declares `bundle` runs the reviewer gates that can
// share a session as ONE session returning a verdict per rubric.

var bundleHeading = regexp.MustCompile(`### Rubric: (\S+)`)

// bundleRunner answers a bundled session with a verdicts object and a lone gate
// with a single decision, recording every request it saw. verdict maps a skill
// to accept|reject|omit (or any other word, sent as the decision verbatim).
type bundleRunner struct {
	mu      sync.Mutex
	reqs    []agentcli.Request
	verdict map[string]string
	raw     string // when set, returned verbatim for every call
}

func (r *bundleRunner) Name() string    { return "bundle-fake" }
func (r *bundleRunner) Command() string { return "bundle-fake -p {system}" }
func (r *bundleRunner) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.reqs)
}
func (r *bundleRunner) Run(_ context.Context, req agentcli.Request) ([]byte, error) {
	r.mu.Lock()
	r.reqs = append(r.reqs, req)
	r.mu.Unlock()
	if r.raw != "" {
		return []byte(r.raw), nil
	}
	return []byte(r.answer(req)), nil
}

func (r *bundleRunner) answer(req agentcli.Request) string {
	var names []string
	for _, m := range bundleHeading.FindAllStringSubmatch(req.SystemPrompt, -1) {
		names = append(names, m[1])
	}
	if len(names) == 0 { // a lone gate: its skill is the one whose heading it carries
		for _, n := range []string{"rev-a", "rev-b", "rev-c", "rev-d"} {
			if strings.Contains(req.SystemPrompt, "# "+n+"\n") {
				v := r.verdict[n]
				if v == "" {
					v = "accept"
				}
				return `{"decision":"` + v + `","notes":"single ` + n + `"}`
			}
		}
		return `{"decision":"accept","notes":"single"}`
	}
	var entries []map[string]string
	for _, n := range names {
		v := r.verdict[n]
		if v == "" {
			v = "accept"
		}
		if v == "omit" {
			continue
		}
		entries = append(entries, map[string]string{"skill": n, "decision": v, "notes": "bundled " + n})
	}
	b, _ := json.Marshal(map[string]any{"verdicts": entries})
	return string(b)
}

func bundleSkillDocs(names ...string) []docindex.Doc {
	var out []docindex.Doc
	for _, n := range names {
		out = append(out, docindex.Doc{Kind: "skills", Name: n, Body: parallelSkill(n)})
	}
	return out
}

func bundleEdgeWF(reviewers, extra string) string {
	return spineWF("", "", "", "in_progress|executor||"+reviewers+"|reviewer||"+extra, "done")
}

func bundleGate(t *testing.T, g *Engine) (verb.GateDecision, error) {
	t.Helper()
	g.backoff = func(int) time.Duration { return 0 }
	return g.Gate(context.Background(), workitem.Item{ID: "sty_b", Status: "backlog", Category: "feature"}, "in_progress")
}

func TestBundle_AbsentKeyRunsSeparateSessions(t *testing.T) {
	docs := fakeDocs{workflow: bundleEdgeWF("rev-a,rev-b,rev-c", ""), extraSkills: bundleSkillDocs("rev-a", "rev-b", "rev-c")}
	r := &bundleRunner{}
	dec, err := bundleGate(t, New(r, docs, "/repo", ""))
	if err != nil {
		t.Fatal(err)
	}
	if r.calls() != 3 {
		t.Errorf("without `bundle` three gates must be three sessions, got %d", r.calls())
	}
	if len(dec.Reviewers) != 3 {
		t.Fatalf("reviewers = %+v", dec.Reviewers)
	}
	for _, rv := range dec.Reviewers {
		if rv.BundleID != "" || len(rv.BundleSkills) != 0 {
			t.Errorf("an unbundled verdict must carry no bundle identity: %+v", rv)
		}
	}
}

func TestBundle_ExplicitFalseRunsSeparateSessions(t *testing.T) {
	docs := fakeDocs{workflow: bundleEdgeWF("rev-a,rev-b", "bundle = false"), extraSkills: bundleSkillDocs("rev-a", "rev-b")}
	r := &bundleRunner{}
	if _, err := bundleGate(t, New(r, docs, "/repo", "")); err != nil {
		t.Fatal(err)
	}
	if r.calls() != 2 {
		t.Errorf("bundle = false must keep separate sessions, got %d calls", r.calls())
	}
}

func TestBundle_OneSessionOneVerdictPerRubric(t *testing.T) {
	docs := fakeDocs{workflow: bundleEdgeWF("rev-a,rev-b,rev-c", "bundle = true"), extraSkills: bundleSkillDocs("rev-a", "rev-b", "rev-c")}
	r := &bundleRunner{verdict: map[string]string{"rev-b": "reject"}}
	dec, err := bundleGate(t, New(r, docs, "/repo", ""))
	if err != nil {
		t.Fatal(err)
	}
	if r.calls() != 1 {
		t.Fatalf("a bundle is ONE session, got %d calls", r.calls())
	}
	if len(dec.Reviewers) != 3 {
		t.Fatalf("one verdict per rubric, got %+v", dec.Reviewers)
	}
	want := []struct {
		skill  string
		accept bool
	}{{"rev-a", true}, {"rev-b", false}, {"rev-c", true}}
	for i, w := range want {
		rv := dec.Reviewers[i]
		if rv.Skill != w.skill || rv.Accept != w.accept || rv.Order != i {
			t.Errorf("verdict %d = %+v, want %s accept=%v", i, rv, w.skill, w.accept)
		}
		if rv.Notes != "bundled "+w.skill {
			t.Errorf("verdict %d notes = %q, want its own", i, rv.Notes)
		}
		if rv.BundleID == "" || rv.BundleID != dec.Reviewers[0].BundleID {
			t.Errorf("every verdict of one session shares its bundle id: %+v", rv)
		}
		if strings.Join(rv.BundleSkills, ",") != "rev-a,rev-b,rev-c" {
			t.Errorf("bundle skills = %v", rv.BundleSkills)
		}
	}
	if dec.Accept {
		t.Error("any reject rejects the edge")
	}

	// Nothing merged or dropped: every rubric rides the session verbatim under
	// its own heading, and the payload names them.
	r.mu.Lock()
	req := r.reqs[0]
	r.mu.Unlock()
	for _, n := range []string{"rev-a", "rev-b", "rev-c"} {
		if !strings.Contains(req.SystemPrompt, parallelSkill(n)) {
			t.Errorf("rubric %s is not verbatim in the bundled prompt", n)
		}
		if !strings.Contains(req.SystemPrompt, "### Rubric: "+n) {
			t.Errorf("rubric %s has no heading", n)
		}
	}
	if !strings.Contains(req.Payload, `"review_skills":["rev-a","rev-b","rev-c"]`) {
		t.Errorf("payload must name the bundled rubrics, got %s", req.Payload)
	}
}

func TestBundle_AllAcceptAdvances(t *testing.T) {
	docs := fakeDocs{workflow: bundleEdgeWF("rev-a,rev-b", "bundle = true"), extraSkills: bundleSkillDocs("rev-a", "rev-b")}
	dec, err := bundleGate(t, New(&bundleRunner{}, docs, "/repo", ""))
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Gated || !dec.Accept || len(dec.Reviewers) != 2 {
		t.Fatalf("all accept = %+v", dec)
	}
}

func TestBundle_MissingVerdictFailsClosed(t *testing.T) {
	cases := []struct {
		name    string
		verdict map[string]string
	}{
		{"omitted", map[string]string{"rev-b": "omit"}},
		{"invalid decision value", map[string]string{"rev-b": "maybe"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			docs := fakeDocs{workflow: bundleEdgeWF("rev-a,rev-b,rev-c", "bundle = true"), extraSkills: bundleSkillDocs("rev-a", "rev-b", "rev-c")}
			r := &bundleRunner{verdict: tc.verdict}
			dec, err := bundleGate(t, New(r, docs, "/repo", ""))
			if err != nil {
				t.Fatal(err)
			}
			if r.calls() != 1 {
				t.Errorf("a missing verdict is not retried: %d calls", r.calls())
			}
			if len(dec.Reviewers) != 3 {
				t.Fatalf("reviewers = %+v", dec.Reviewers)
			}
			if !dec.Reviewers[0].Accept || !dec.Reviewers[2].Accept {
				t.Errorf("the rubrics that answered keep their verdicts: %+v", dec.Reviewers)
			}
			b := dec.Reviewers[1]
			if b.Accept || !strings.Contains(b.Notes, "no verdict for rev-b") || !strings.Contains(b.Notes, "fail-closed") {
				t.Errorf("rev-b must fail closed, got %+v", b)
			}
			if dec.Accept {
				t.Error("a rubric with no verdict rejects the edge")
			}
		})
	}
}

func TestBundle_UnnamedAndUnknownVerdictsAreIgnored(t *testing.T) {
	docs := fakeDocs{workflow: bundleEdgeWF("rev-a,rev-b", "bundle = true"), extraSkills: bundleSkillDocs("rev-a", "rev-b")}
	// An echoed format example, an unknown skill, and a verdict with no skill are
	// none of them a verdict for a requested rubric.
	r := &bundleRunner{raw: "example {\"decision\":\"accept\",\"notes\":\"\"} and " +
		`{"verdicts":[{"skill":"rev-zzz","decision":"accept"},{"skill":"rev-a","decision":"accept","notes":"a ok"}]}`}
	dec, err := bundleGate(t, New(r, docs, "/repo", ""))
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Reviewers[0].Accept || dec.Reviewers[1].Accept {
		t.Errorf("only rev-a answered: %+v", dec.Reviewers)
	}
}

func TestBundle_UnparseableResponseFailsEveryRubricClosed(t *testing.T) {
	docs := fakeDocs{workflow: bundleEdgeWF("rev-a,rev-b", "bundle = true"), extraSkills: bundleSkillDocs("rev-a", "rev-b")}
	r := &bundleRunner{raw: "I looked at it and it seems fine."}
	g := New(r, docs, "/repo", "")
	dec, err := bundleGate(t, g)
	if err != nil {
		t.Fatalf("an unparseable response fails the rubrics closed, it does not error: %v", err)
	}
	if r.calls() != g.attempts {
		t.Errorf("the shared repair loop must run: %d calls, %d attempts", r.calls(), g.attempts)
	}
	if len(dec.Reviewers) != 2 || dec.Accept {
		t.Fatalf("decision = %+v", dec)
	}
	for _, rv := range dec.Reviewers {
		if rv.Accept || !strings.Contains(rv.Notes, "fail-closed") {
			t.Errorf("every rubric must fail closed: %+v", rv)
		}
	}
}

func TestBundle_RunnerFailureIsAnErrorNotAReject(t *testing.T) {
	docs := fakeDocs{workflow: bundleEdgeWF("rev-a,rev-b", "bundle = true"), extraSkills: bundleSkillDocs("rev-a", "rev-b")}
	g := New(&fakeRunner{err: errFakeAgent}, docs, "/repo", "")
	if _, err := bundleGate(t, g); err == nil {
		t.Fatal("a runner failure must refuse the transition, exactly as it does for a lone gate")
	}
}

const bundleCheckSkill = "---\nname: chk\ntype: skill\ndescription: functional check\ncheck: \"run-chk\"\n---\n# chk\nCHK-UNIQUE-MARKER runs the suite.\n"

func TestBundle_FunctionalCheckNeverEntersTheSession(t *testing.T) {
	skills := append(bundleSkillDocs("rev-a", "rev-b"), docindex.Doc{Kind: "skills", Name: "chk", Body: bundleCheckSkill})
	docs := fakeDocs{workflow: bundleEdgeWF("rev-a,rev-b,chk", "bundle = true"), extraSkills: skills}
	r := &bundleRunner{}
	g := New(r, docs, "/repo", "")
	ran := 0
	g.check = func(_ context.Context, _, command, _ string) (string, error) {
		ran++
		if command != "run-chk" {
			t.Errorf("check command = %q", command)
		}
		return "ok\n", nil
	}
	dec, err := bundleGate(t, g)
	if err != nil {
		t.Fatal(err)
	}
	if ran != 1 {
		t.Errorf("the check runs exactly once, ran %d", ran)
	}
	if r.calls() != 1 {
		t.Errorf("the two plain rubrics share ONE session, got %d calls", r.calls())
	}
	r.mu.Lock()
	prompt := r.reqs[0].SystemPrompt
	r.mu.Unlock()
	if strings.Contains(prompt, "CHK-UNIQUE-MARKER") || strings.Contains(prompt, "### Rubric: chk") {
		t.Error("a functional-check rubric must not be in the bundled prompt")
	}
	if len(dec.Reviewers) != 3 {
		t.Fatalf("reviewers = %+v", dec.Reviewers)
	}
	chk := dec.Reviewers[2]
	if chk.Skill != "chk" || !chk.Accept || chk.Command != "" || chk.BundleID != "" {
		t.Errorf("the check's verdict comes from the command alone: %+v", chk)
	}
	if !strings.Contains(chk.Notes, "functional check passed") {
		t.Errorf("check notes = %q", chk.Notes)
	}
	for _, rv := range dec.Reviewers[:2] {
		if rv.BundleID == "" {
			t.Errorf("the plain rubrics are bundled: %+v", rv)
		}
	}
}

func TestBundle_FailingCheckStillRejectsTheEdge(t *testing.T) {
	skills := append(bundleSkillDocs("rev-a", "rev-b"), docindex.Doc{Kind: "skills", Name: "chk", Body: bundleCheckSkill})
	docs := fakeDocs{workflow: bundleEdgeWF("rev-a,rev-b,chk", "bundle = true"), extraSkills: skills}
	g := New(&bundleRunner{}, docs, "/repo", "")
	g.check = func(context.Context, string, string, string) (string, error) { return "2 failures\n", errFakeExit }
	dec, err := bundleGate(t, g)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Accept || dec.Reviewers[2].Accept || !dec.Reviewers[0].Accept {
		t.Errorf("only the check rejects: %+v", dec.Reviewers)
	}
}

const bundleIndependentSkill = "---\nname: rev-c\nscope: system\ntype: skill\ntags: [type:skill, type:reviewer]\nindependent: true\ndescription: must judge alone\n---\n\n# rev-c\n\nReturn a verdict:\n\n```json\n{\"decision\": \"accept\", \"notes\": \"\"}\n```\n"

func TestBundle_IndependentRubricStaysItsOwnSession(t *testing.T) {
	skills := append(bundleSkillDocs("rev-a", "rev-b"), docindex.Doc{Kind: "skills", Name: "rev-c", Body: bundleIndependentSkill})
	docs := fakeDocs{workflow: bundleEdgeWF("rev-a,rev-b,rev-c", "bundle = true"), extraSkills: skills}
	r := &bundleRunner{}
	dec, err := bundleGate(t, New(r, docs, "/repo", ""))
	if err != nil {
		t.Fatal(err)
	}
	if r.calls() != 2 {
		t.Fatalf("rev-a+rev-b in one session, rev-c alone: want 2 calls, got %d", r.calls())
	}
	if dec.Reviewers[0].BundleID == "" || dec.Reviewers[0].BundleID != dec.Reviewers[1].BundleID {
		t.Errorf("rev-a and rev-b share a session: %+v", dec.Reviewers)
	}
	if dec.Reviewers[2].BundleID != "" {
		t.Errorf("an independent rubric is not bundled: %+v", dec.Reviewers[2])
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, req := range r.reqs {
		if strings.Contains(req.SystemPrompt, "### Rubric: rev-c") {
			t.Error("the independent rubric leaked into a bundled prompt")
		}
	}
}

// TestBundle_SeatKeyDecidesWhoShares is AC5's table: gates bundle only when they
// share binding, model, effort and tool grant. Each case names the seat of a
// second gate against a baseline seat and counts the sessions the edge runs.
func TestBundle_SeatKeyDecidesWhoShares(t *testing.T) {
	base := config.AgentBinding{Command: "seat-cmd -p {system}", Tools: "Read,Grep,Glob", Model: "opus", Effort: "high", Role: "reviewer"}
	cases := []struct {
		name  string
		agent string // the binding rev-c's gate names
		other func(b config.AgentBinding) config.AgentBinding
		want  int // sessions for rev-a, rev-b, rev-c (rev-c on the other seat)
	}{
		{"same binding", "seat-a", func(b config.AgentBinding) config.AgentBinding { return b }, 1},
		{"same settings, different binding", "seat-b", func(b config.AgentBinding) config.AgentBinding { return b }, 2},
		{"different model", "seat-b", func(b config.AgentBinding) config.AgentBinding { b.Model = "haiku"; return b }, 2},
		{"different effort", "seat-b", func(b config.AgentBinding) config.AgentBinding { b.Effort = "low"; return b }, 2},
		{"different tool grant", "seat-b", func(b config.AgentBinding) config.AgentBinding { b.Tools = "Read,Grep"; return b }, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wf := spineWF("", "",
				"[[gate]]\nskill = \"rev-c\"\nagent = \""+tc.agent+"\"\non = [\"in_progress\"]\n",
				"in_progress|executor||rev-a,rev-b|seat-a||bundle = true", "done")
			docs := fakeDocs{workflow: wf, extraSkills: bundleSkillDocs("rev-a", "rev-b", "rev-c")}
			r := &bundleRunner{}
			g := New(r, docs, "/repo", "")
			g.SetNamedAgents(func(name string) (config.AgentBinding, bool) {
				switch name {
				case "seat-a":
					return base, true
				case "seat-b":
					return tc.other(base), true
				}
				return config.AgentBinding{}, false
			})
			g.newRunner = func(string, string) (agentcli.Runner, error) { return r, nil }
			dec, err := bundleGate(t, g)
			if err != nil {
				t.Fatal(err)
			}
			// rev-a and rev-b always share seat-a; rev-c joins only on an identical seat.
			if r.calls() != tc.want {
				t.Errorf("sessions = %d, want %d", r.calls(), tc.want)
			}
			if len(dec.Reviewers) != 3 {
				t.Fatalf("reviewers = %+v", dec.Reviewers)
			}
			joined := dec.Reviewers[2].BundleID != ""
			if joined != (tc.want == 1) {
				t.Errorf("rev-c bundled = %v with %d sessions", joined, tc.want)
			}
		})
	}
}

func TestParseBundleDecisionsCopiesReviewed(t *testing.T) {
	const quote = "BUNDLE-REVIEWED-QUOTATION-not-in-notes"
	decs, err := parseBundleDecisions([]byte(`{"verdicts":[{"skill":"rev-a","decision":"reject","notes":"the plan's AC3","reasoning":"because","reviewed":"`+quote+`"}]}`), []string{"rev-a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(decs) != 1 {
		t.Fatalf("decisions = %+v", decs)
	}
	if decs[0].Accept || decs[0].Notes != "the plan's AC3" || decs[0].Reviewed != quote || decs[0].ReviewedTruncated {
		t.Fatalf("bundle entry = %+v", decs[0])
	}
	if strings.Contains(decs[0].Notes, quote) {
		t.Fatal("the quotation must be its own field, not a substring of notes")
	}
	if !strings.Contains(bundlePreamble, `"reviewed": "..."`) {
		t.Fatalf("bundle preamble must show reviewed beside notes and reasoning:\n%s", bundlePreamble)
	}
}

func TestBundle_PartitionKeepsUnrelatedSeatsApart(t *testing.T) {
	wf := spineWF("", "", "", "in_progress|executor||rev-a,rev-b,rev-c|reviewer||bundle = true", "done")
	docs := fakeDocs{workflow: wf, extraSkills: bundleSkillDocs("rev-a", "rev-b", "rev-c")}
	g := New(&bundleRunner{}, docs, "/repo", "")
	units := g.partitionBundles(context.Background(), workitem.Item{ID: "sty_p", Category: "feature"}, "in_progress",
		[]reviewerRef{{skill: "rev-a"}, {skill: "rev-b"}, {skill: ""}, {skill: "rev-missing"}, {skill: "rev-c"}})
	var shape []int
	for _, u := range units {
		shape = append(shape, len(u.idx))
	}
	// rev-a, rev-b, rev-c share [reviewer]; the absent rubric is its own unit; the
	// empty skill is not a gate at all.
	if len(units) != 2 || shape[0] != 3 || shape[1] != 1 {
		t.Errorf("unit sizes = %v, want [3 1]", shape)
	}
}
