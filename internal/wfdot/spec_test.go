package wfdot

import (
	"strings"
	"testing"
)

// The Spec METHODS used to be exercised through DOT fixtures, because DOT text
// was how a Spec came into existence. With the DOT front end retired
// (sty_d953c5d8) the only constructor is BuildRoute, so these cases are stated
// as Spec literals: the same shapes the retired fixtures built, asserted against
// the same methods. route_test.go covers the construction side; this file covers
// what the constructed thing answers.

func TestValidateNamesTheDefects(t *testing.T) {
	cases := []struct {
		name string
		spec Spec
		want string
	}{
		{"no states", Spec{}, "no states"},
		{
			"edge from an unknown state",
			Spec{States: []State{{Name: "backlog"}}, Transitions: []Transition{{From: "nope", To: "backlog"}}},
			`from unknown state "nope"`,
		},
		{
			"edge to an unknown state",
			Spec{States: []State{{Name: "backlog"}}, Transitions: []Transition{{From: "backlog", To: "nope"}}},
			`to unknown state "nope"`,
		},
		{
			"done is not terminal",
			Spec{
				States:      []State{{Name: "backlog"}, {Name: "done"}},
				Transitions: []Transition{{From: "backlog", To: "done"}, {From: "done", To: "backlog"}},
			},
			`"done" must be terminal`,
		},
		{
			"applies_to on a performing spine node",
			Spec{
				States: []State{{Name: "backlog"}, {Name: "in_progress", Agent: "executor",
					Skill: "code", AppliesTo: []string{"surface:ui"}}, {Name: "done"}},
				Transitions: []Transition{{From: "backlog", To: "in_progress"}, {From: "in_progress", To: "done"}},
			},
			"is not supported without on=",
		},
		{
			"a performing on= node that is also on an edge",
			Spec{
				States: []State{{Name: "backlog"}, {Name: "aug", Agent: "executor",
					Skill: "x", On: []string{"in_progress"}}, {Name: "done"}},
				Transitions: []Transition{{From: "backlog", To: "aug"}, {From: "aug", To: "done"}},
			},
			"must be edge-less",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(Validate(tc.spec), " | ")
			if !strings.Contains(got, tc.want) {
				t.Errorf("Validate = %q, want a problem mentioning %q", got, tc.want)
			}
		})
	}
}

func TestTagsMatchAppliesTo(t *testing.T) {
	cases := []struct {
		appliesTo, tags []string
		want            bool
	}{
		{nil, []string{"surface:ui"}, true},                      // unscoped matches anything
		{[]string{"surface:ui"}, []string{"surface:ui"}, true},   // exact
		{[]string{"surface:ui"}, []string{"surface:cli"}, false}, // different value
		{[]string{"surface:ui"}, nil, false},                     // scoped, untagged story
		{[]string{"a", "b"}, []string{"b"}, true},                // any-match
	}
	for _, tc := range cases {
		if got := tagsMatchAppliesTo(tc.appliesTo, tc.tags); got != tc.want {
			t.Errorf("tagsMatchAppliesTo(%v, %v) = %v, want %v", tc.appliesTo, tc.tags, got, tc.want)
		}
	}
}
