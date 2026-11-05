package wfgovern

import (
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/wfdot"
)

func init() {
	EmbeddedRoute = func(name string) string {
		for _, d := range config.EmbeddedDefaults() {
			if d.Kind == "workflows" && d.Name == name {
				return d.Body
			}
		}
		return ""
	}
}

// authoredDocs is the list the doc index yields: the repo's own half where it
// authored one, and the shipped default for a half it did not.
func authoredDocs(done, step string) []docindex.Doc {
	ds := embeddedDocs()
	if done != "" {
		ds[0] = docindex.Doc{Kind: "workflows", Name: RouteSourceDone, Body: done}
	}
	if step != "" {
		ds[1] = docindex.Doc{Kind: "workflows", Name: RouteSourceStep, Body: step}
	}
	return ds
}

func embeddedDocs() []docindex.Doc {
	return []docindex.Doc{
		{Kind: "workflows", Name: RouteSourceDone, Body: embeddedRouteBody(RouteSourceDone), Embedded: true},
		{Kind: "workflows", Name: RouteSourceStep, Body: embeddedRouteBody(RouteSourceStep), Embedded: true},
	}
}

func stepNames(c wfdot.Catalogue) []string {
	var out []string
	for _, s := range c.Steps {
		out = append(out, s.Name)
	}
	return out
}

func mustList(t *testing.T, rs RouteSource, cat string) wfdot.List {
	t.Helper()
	d, err := RouteSpecFor(rs, cat, nil)
	if err != nil {
		t.Fatal(err)
	}
	return d.List
}

func TestRouteMerge_GateBySkill(t *testing.T) {
	base := "[[gate]]\nskill = \"a\"\nmandatory = true\n\n[[gate]]\nskill = \"b\"\n"
	over := "[[gate]]\nskill = \"b\"\nagent = \"x\"\n\n[[gate]]\nskill = \"c\"\n"
	out := mergeRouteHalf(base, over)
	cat, err := wfdot.ParseSteps(out + "\n[raised]\nstatus = \"backlog\"\nstart = true\n")
	if err != nil {
		t.Fatal(err)
	}
	var skills []string
	for _, g := range cat.Gates {
		skills = append(skills, g.Skill)
		if g.Skill == "b" && g.Agent != "x" {
			t.Errorf("b not replaced: %+v", g)
		}
	}
	if strings.Join(skills, ",") != "b,c" {
		t.Errorf("gates = %v, want b,c (a repo gate list replaces the baseline list)", skills)
	}
}

func TestRouteMerge_EmptyRepoKeepsBaselineBytes(t *testing.T) {
	const base = "[raised]\nstatus = \"backlog\"\n"
	if got := mergeRouteHalf(base, "  \n"); got != base {
		t.Errorf("got %q, want the baseline unchanged", got)
	}
}

func TestRouteMerge_BrokenRepoBodyReturnedAsWritten(t *testing.T) {
	const bad = "[coded\n"
	if got := mergeRouteHalf("[raised]\nstatus = \"backlog\"\n", bad); got != bad {
		t.Errorf("a broken repo body must reach the parser as written, got %q", got)
	}
}
