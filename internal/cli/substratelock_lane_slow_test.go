//go:build clislow

package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/ledger"
)

// AC4 + AC5: every condition that is not the lane is still refused while a
// non-substrate seat performs, and the deny names the holder (id, status,
// category), the condition and its fix — never "re-engage" for a lease that is
// not this session's, and no file-a-substrate-story advice when the session
// already holds a substrate seat.
func TestSlowSubstrateLockRefusalNamesTheCondition(t *testing.T) {
	const advice = "make the change under a substrate-lane story"
	cases := []struct {
		name  string
		sid   string
		specs []laneSpec
		// target: the key whose worktree is edited ("" = the main tree)
		target string
		// holders are the spec keys the deny must name; absent the ones it must not
		holders []string
		want    func(ids map[string]string) []string
		not     []string
	}{
		{
			name: "no seat attributed to this session",
			sid:  "sess", specs: []laneSpec{{key: "F1", category: "feature", session: "other"}},
			holders: []string{"F1"},
			want: func(map[string]string) []string {
				return []string{"No performing seat is attributed to this session", "Engage the substrate story in this session", advice}
			},
			not: []string{"re-engage", "seat release"},
		},
		{
			name: "own seat stale",
			sid:  "sess",
			specs: []laneSpec{
				{key: "F0", category: "feature", session: "sess", stale: true},
				{key: "F1", category: "feature", session: "other"},
			},
			holders: []string{"F1"},
			want: func(ids map[string]string) []string {
				return []string{"own seat " + ids["F0"] + " (session sess) is stale", "re-engage it (`satelle story set " + ids["F0"] + " --status in_progress`)", advice}
			},
			not: []string{"seat release"},
		},
		{
			name: "stale lease of another session",
			sid:  "sess",
			specs: []laneSpec{
				{key: "F0", category: "feature", session: "other2", stale: true},
				{key: "F1", category: "feature", session: "other"},
			},
			holders: []string{"F1"},
			want: func(ids map[string]string) []string {
				return []string{"No performing seat is attributed to this session", "Stale lease " + ids["F0"] + " belongs to another session", "`satelle story seat release " + ids["F0"] + "`", advice}
			},
			not: []string{"re-engage", "is stale, so it was not counted"},
		},
		{
			name: "stale lease stamped by no session",
			sid:  "sess",
			specs: []laneSpec{
				{key: "F0", category: "feature", session: "", stale: true},
				{key: "F1", category: "feature", session: "other"},
			},
			holders: []string{"F1"},
			want: func(ids map[string]string) []string {
				return []string{"Stale lease " + ids["F0"] + " belongs to no session", "`satelle story seat release " + ids["F0"] + "`"}
			},
			not: []string{"re-engage", "is stale, so it was not counted"},
		},
		{
			name: "ambiguous, none substrate",
			sid:  "sess",
			specs: []laneSpec{
				{key: "F1", category: "feature", session: "sess"},
				{key: "F2", category: "fix", session: "sess"},
			},
			holders: []string{"F1", "F2"},
			want: func(ids map[string]string) []string {
				return []string{"This session holds seats " + ids["F1"] + ", " + ids["F2"], "and none of them is in the substrate lane", "Make it from the substrate story's worktree", advice}
			},
			not: []string{"re-engage"},
		},
		{
			name: "ambiguous, several substrate",
			sid:  "sess",
			specs: []laneSpec{
				{key: "S1", category: "substrate", session: "sess"},
				{key: "S2", category: "substrate", session: "sess"},
				{key: "F1", category: "feature", session: "sess"},
			},
			holders: []string{"F1"},
			want: func(ids map[string]string) []string {
				return []string{"and more than one of them is in the substrate lane", "release the seats that are not substrate"}
			},
			// the session already holds a substrate seat: no file-a-substrate-story advice
			not: []string{advice, "file one"},
		},
		{
			name: "attributed, session also holds a substrate seat",
			sid:  "sess",
			specs: []laneSpec{
				{key: "S", category: "substrate", session: "sess"},
				{key: "F", category: "fix", session: "sess"},
			},
			target:  "F",
			holders: []string{"F"},
			want: func(ids map[string]string) []string {
				return []string{"attributed to your story " + ids["F"], "worktree of your substrate-lane story"}
			},
			not: []string{advice},
		},
		{
			name: "attributed, no substrate seat",
			sid:  "sess",
			specs: []laneSpec{
				{key: "F", category: "fix", session: "sess"},
			},
			holders: []string{"F"},
			want: func(ids map[string]string) []string {
				return []string{"attributed to your story " + ids["F"], advice, "finish or park " + ids["F"]}
			},
			not: []string{"worktree of your substrate-lane story"},
		},
	}
	for _, via := range []string{"edit", "bash"} {
		for _, tc := range cases {
			t.Run(via+"/"+tc.name, func(t *testing.T) {
				e := newLaneEnv(t, tc.specs)
				dir := e.repo
				if tc.target != "" {
					dir = e.trees[tc.target]
				}
				target := filepath.Join(dir, laneRel)
				s := e.seats(tc.sid, target)
				err, out := e.gate(t, via, target, s)
				if err == nil {
					t.Fatalf("the lock must still refuse while a non-substrate seat performs:\n%s", out)
				}
				reason := denyReasonOf(t, out)
				for _, k := range tc.holders {
					cat := ""
					for _, sp := range tc.specs {
						if sp.key == k {
							cat = sp.category
						}
					}
					if want := e.ids[k] + ` (status "in_progress", category "` + cat + `")`; !strings.Contains(reason, want) {
						t.Errorf("deny must name the holder as %s: %s", want, reason)
					}
				}
				for _, w := range tc.want(e.ids) {
					if !strings.Contains(reason, w) {
						t.Errorf("deny missing %q: %s", w, reason)
					}
				}
				for _, w := range tc.not {
					if strings.Contains(reason, w) {
						t.Errorf("deny must not contain %q: %s", w, reason)
					}
				}
				if n, _ := rowsByStory(t, ledger.KindSubstrateChangeNote); len(n) != 0 {
					t.Errorf("a refusal notes nobody: %v", n)
				}
			})
		}
	}
}
