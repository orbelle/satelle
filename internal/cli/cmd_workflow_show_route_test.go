package cli

import (
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/wfgovern"
)

// `satelle workflow show <category>` renders the DERIVED route (sty_a989764d).
// These drive renderWorkflowRoute directly — it takes the route SOURCE rather
// than the app, so no store is needed.

func routeSourceFixture() wfgovern.RouteSource {
	done := `["*"]
obligations = ["raised", "coded", "closed"]
park = { state = "blocked", gate = "gate-blocked", advisor = "triage", advisor_skill = "triage-skill" }
cancel = { state = "cancelled", gate = "gate-cancel" }
recover = { step = "in_progress", from = ["release"] }

[container]
obligations = ["raised", "children-resolved"]
cancel = { state = "cancelled", gate = "gate-cancel" }
`
	step := `[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
agent = "executor"
skills = ["code"]
reviewers = ["gate-plan", "gate-arch"]
reviewer_agent = "reviewer"
requires = ["raised"]

[released]
status = "release"
agent = "executor"
requires = ["coded"]

[closed]
status = "done"
reviewers = ["gate-close"]
reviewer_agent = "reviewer"
terminal = true
requires = ["coded"]

[children-resolved]
status = "done"
agent = "reviewer"
reviewers = ["gate-children"]
terminal = true
requires = ["raised"]

[[gate]]
skill = "gate-summary"
agent = "reviewer-summary"
mandatory = true
for = ["*"]

[[gate]]
skill = "gate-ui"
on = ["in_progress"]
applies_to = ["surface:ui"]
for = ["*"]
`
	return wfgovern.RouteSource{Done: done, Step: step}
}

func renderRoute(t *testing.T, category string, tags []string) string {
	t.Helper()
	var b strings.Builder
	if err := renderWorkflowRoute(&b, routeSourceFixture(), category, tags); err != nil {
		t.Fatalf("render %q: %v", category, err)
	}
	return b.String()
}

// sty_4cf2c585: the derived route view reports each step's declared reject_budget,
// and only the steps that declare one.
func TestWorkflowShowRouteReportsDeclaredRejectBudget(t *testing.T) {
	rs := routeSourceFixture()
	rs.Step = strings.Replace(rs.Step, "[closed]\nstatus = \"done\"\n", "[closed]\nstatus = \"done\"\nreject_budget = 2\n", 1)
	var b strings.Builder
	if err := renderWorkflowRoute(&b, rs, "feature", nil); err != nil {
		t.Fatalf("render: %v", err)
	}
	var budgets []string
	for _, ln := range strings.Split(b.String(), "\n") {
		if strings.Contains(ln, "reject budget:") {
			budgets = append(budgets, strings.TrimSpace(ln))
		}
	}
	if len(budgets) != 1 || !strings.HasPrefix(budgets[0], "reject budget: 2 ") {
		t.Fatalf("want exactly one `reject budget: 2` line (the terminal step's), got %q in:\n%s", budgets, b.String())
	}
	if out := renderRoute(t, "feature", nil); strings.Contains(out, "reject budget") {
		t.Errorf("a route declaring no budget must render none:\n%s", out)
	}
}

// AC1: obligations in order with the step discharging each, the entry gates and
// their bindings, and the synthesised topology marked as synthesised.
func TestShowRouteRendersTheWildcardSpine(t *testing.T) {
	out := renderRoute(t, "feature", nil)

	for _, want := range []string{
		"ROUTE feature",
		// The wildcard governed; saying so matters because it silently changes
		// which route the reader is looking at.
		`[*] (the wildcard`,
		"discharges: raised",
		"discharges: coded",
		"discharges: closed",
		"performer:  executor under @skill:code",
		"entry gates: gate-plan [reviewer], gate-arch [reviewer]",
		"Synthesised topology",
		"blocked:",
		"cancelled:",
		"recover: back to in_progress from release",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("route view missing %q:\n%s", want, out)
		}
	}
	// Order is derived; raised must precede coded must precede closed.
	ri, ci, di := strings.Index(out, "discharges: raised"),
		strings.Index(out, "discharges: coded"),
		strings.Index(out, "discharges: closed")
	if !(ri < ci && ci < di) {
		t.Errorf("obligations are out of route order (raised=%d coded=%d closed=%d):\n%s", ri, ci, di, out)
	}
}

// AC1 + the reported bug: this is the invocation that failed with a docindex
// miss before this story.
func TestShowRouteRendersANonWildcardSection(t *testing.T) {
	out := renderRoute(t, "container", nil)

	if !strings.Contains(out, "section:      [container]") {
		t.Errorf("an exact section must be reported as itself, not the wildcard:\n%s", out)
	}
	if !strings.Contains(out, "discharges: children-resolved") {
		t.Errorf("the container route must show its own obligation:\n%s", out)
	}
	// Its route is two steps; the spine's coded/closed steps belong to another
	// section and must not appear.
	for _, unwanted := range []string{"discharges: coded", "discharges: closed"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("the container route leaked %q from another section:\n%s", unwanted, out)
		}
	}
	// AC1: a gate excluded by for: is NAMED with its reason. Without this a reader
	// cannot tell a gate that does not apply from a gate nobody declared.
	if !strings.Contains(out, `gate-summary — EXCLUDED: for: [*] does not name section "container"`) {
		t.Errorf("an excluded gate must be named with its exclusion reason:\n%s", out)
	}
}

// AC2: tags change the route, and the view says which tags produced it.
func TestShowRouteAppliesTags(t *testing.T) {
	without := renderRoute(t, "feature", nil)
	with := renderRoute(t, "feature", []string{"surface:ui"})

	if !strings.Contains(with, "tags:         surface:ui") {
		t.Errorf("the view must echo the tags it derived with:\n%s", with)
	}
	if !strings.Contains(with, "gate-ui") {
		t.Errorf("a tag-scoped gate must appear when the tag is carried:\n%s", with)
	}
	// Without the tag it is reported as NOT RUN rather than silently absent —
	// "no gate" and "gate not for you" are different facts.
	if !strings.Contains(without, "not run:    gate-ui") {
		t.Errorf("a tag-scoped gate that does not apply must still be disclosed:\n%s", without)
	}
}

// AC3: the view reads the same derivation the engine uses. If it re-implemented
// selection, a Spec built directly would disagree with what is rendered.
func TestShowRouteMatchesTheEngineDerivation(t *testing.T) {
	rs := routeSourceFixture()
	d, err := wfgovern.RouteSpecFor(rs, "container", nil)
	if err != nil {
		t.Fatal(err)
	}
	out := renderRoute(t, "container", nil)
	for _, st := range d.Spec.States {
		if st.Obligation == "" {
			continue
		}
		if !strings.Contains(out, "discharges: "+st.Obligation) {
			t.Errorf("the Spec discharges %q but the view does not show it:\n%s", st.Obligation, out)
		}
	}
}

// AC4: a category no section claims and no wildcard covers is an error naming
// the governing-section problem — never a document-index miss.
func TestShowRouteUnknownCategoryReportsTheSectionProblem(t *testing.T) {
	rs := wfgovern.RouteSource{
		Done: `[container]
obligations = ["raised"]
`,
		Step: `[raised]
status = "backlog"
start = true
`,
	}
	var b strings.Builder
	err := renderWorkflowRoute(&b, rs, "feature", nil)
	if err == nil {
		t.Fatal("a category with no section and no wildcard must be an error")
	}
	for _, want := range []string{"feature", "declaration of done"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must name the governing-section problem, got: %v", err)
		}
	}
	if strings.Contains(err.Error(), "docindex") {
		t.Errorf("a docindex miss must never surface as the answer: %v", err)
	}
}
