//go:build clislow

package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/verb"
)

func TestSlowStorySetDeliversInLoopSkills(t *testing.T) {
	id := inLoopSkillsRepo(t)
	for _, tc := range []struct{ status, skill, kind string }{
		{"in_progress", "exec-step", string(verb.PerformerExecutor)},
		{"operating", "operator-step", string(verb.PerformerInLoop)},
	} {
		stderr, err := setStatus(t, id, tc.status)
		if err != nil {
			t.Fatalf("%s: %v\n%s", tc.status, err, stderr)
		}
		if n := strings.Count(stderr, "Follow "+tc.skill+" to the letter."); n != 1 {
			t.Errorf("%s: the rubric was shown %d times, want once:\n%s", tc.status, n, stderr)
		}
		body := strings.ReplaceAll(inLoopSkillBody, "%s", tc.skill)
		sum := sha256.Sum256([]byte(body))
		var found bool
		if n := len(ledgerRows(t, ledger.KindAgentInvocation)); n != 0 {
			t.Errorf("%s: an in-loop delivery is not an invocation, but %d agent_invocation rows were written (they would inflate the cost views)", tc.status, n)
		}
		for _, e := range ledgerRows(t, ledger.KindSkillDelivery) {
			var p struct {
				Agent  string               `json:"kind"`
				To     string               `json:"to"`
				Skills []verb.SkillDelivery `json:"skills"`
			}
			if json.Unmarshal(e.Payload, &p) != nil || p.To != tc.status {
				continue
			}
			found = true
			want := verb.SkillDelivery{Name: tc.skill, Hash: hex.EncodeToString(sum[:]), Delivered: true}
			if e.Actor != "in-loop" || p.Agent != tc.kind || len(p.Skills) != 1 || p.Skills[0] != want {
				t.Errorf("%s: row actor=%q agent=%q skills=%+v, want in-loop / %s / [%+v]", tc.status, e.Actor, p.Agent, p.Skills, tc.kind, want)
			}
		}
		if !found {
			t.Errorf("%s: no skill_delivery row records the delivery", tc.status)
		}
	}
}
