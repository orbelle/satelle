package wfdot

import (
	"strings"
	"testing"
)

// sty_58a7bdfe AC2: context_budget and turn_budget are removed from steps. A
// step.toml still carrying either fails to parse naming the key and that
// satelle init removes it — never as a generic unknown key.
func TestStepBudgetKeysRefusedNamingTheHeal(t *testing.T) {
	for _, key := range []string{"context_budget", "turn_budget"} {
		steps := strings.Replace(readinessSteps, "freeze = true\n", "freeze = true\n"+key+" = 9\n", 1)
		if steps == readinessSteps {
			t.Fatal("fixture: the coded step must carry freeze = true")
		}
		_, err := ParseSteps(steps)
		if err == nil {
			t.Fatalf("%s: a step budget key must fail to parse", key)
		}
		if msg := err.Error(); !strings.Contains(msg, key) || !strings.Contains(msg, "satelle init") || strings.Contains(msg, "unknown key") {
			t.Errorf("%s: refusal = %q, want it to name the key and satelle init", key, msg)
		}
	}
}
