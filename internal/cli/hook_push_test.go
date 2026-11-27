package cli

import (
	"strings"
	"testing"
)

func pushFixture(t *testing.T) (dir, a, b string) {
	t.Helper()
	dir = isolate(t)
	must(t, "init")
	writeGatedWorkflow(t, dir)
	a = createID(t, "story", "create", "--title", "item a", "--workflow", "gated")
	b = createID(t, "story", "create", "--title", "item b", "--workflow", "gated")
	must(t, "story", "seat", a, "--session", "s-1")
	must(t, "story", "set", a, "--status", "st-two")
	return dir, a, b
}

func TestEditGateRefusesPushToAnotherItemsBranch(t *testing.T) {
	dir, _, b := pushFixture(t)
	for _, c := range []string{
		"git push origin HEAD:" + b,
		"git push origin HEAD:refs/heads/" + b,
		"git push origin --delete " + b,
		"git push -u origin " + b,
		"git status && git push origin HEAD:" + b,
		"A+=1 git push origin HEAD:" + b,
		`A="1" "git" push origin HEAD:` + b,
		"A=1; git push origin HEAD:" + b,
	} {
		if deny, why := hookGate(t, dir, "s-1", "Bash", "", c); !deny || !strings.Contains(why, b) {
			t.Errorf("%q: deny %v, reason %q; want a refusal naming %s", c, deny, why, b)
		}
	}
}
