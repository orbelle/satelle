package cli

import (
	"os"
	"path/filepath"
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
	} {
		if deny, why := hookGate(t, dir, "s-1", "Bash", "", c); !deny || !strings.Contains(why, b) {
			t.Errorf("%q: deny %v, reason %q; want a refusal naming %s", c, deny, why, b)
		}
	}
	for _, c := range []string{
		"A+=1 git push origin HEAD:" + b,
		`A="1" "git" push origin HEAD:` + b,
		"A=1; git push origin HEAD:" + b,
	} {
		if deny, why := hookGate(t, dir, "s-1", "Bash", "", c); !deny || !(strings.Contains(why, b) || strings.Contains(why, "fail-closed rule")) {
			t.Errorf("%q: deny %v, reason %q; want a refusal naming %s or the fail-closed rule", c, deny, why, b)
		}
	}
}

func TestEditGateRefusesPushWithoutAnEditingSeat(t *testing.T) {
	dir, _, b := pushFixture(t)
	must(t, "story", "seat", b, "--session", "s-1")
	if deny, why := hookGate(t, dir, "s-1", "Bash", "", "git push origin HEAD:"+b); !deny || !strings.Contains(why, b) {
		t.Errorf("a seat on %s in a state with no tree performer let its branch be pushed: %q", b, why)
	}
	if deny, why := hookGate(t, dir, "", "Bash", "", "git push origin HEAD:"+b); !deny || !strings.Contains(why, "no session id") {
		t.Errorf("a push with no session: deny %v, %q", deny, why)
	}
	if err := os.WriteFile(filepath.Join(dir, ".satelle", "workflows", "gated.toml"), []byte("not = [valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	if deny, _ := hookGate(t, dir, "s-1", "Bash", "", "git push origin HEAD:"+b); !deny {
		t.Error("with an invalid configuration a push to another item's branch was allowed")
	}
}

func TestEditGateAllowsPushToOwnItemsBranch(t *testing.T) {
	dir, a, _ := pushFixture(t)
	for _, c := range []string{
		"git push -u origin " + a,
		"git push -u origin HEAD:refs/heads/" + a,
		"git push origin HEAD:" + a,
		"git push origin --delete " + a,
		"git status && git push origin HEAD:refs/heads/" + a,
		"cd " + dir + " && git push origin HEAD:" + a,
		"git -C " + dir + " push origin HEAD:refs/heads/" + a,
		"git --no-pager push origin HEAD:" + a,
		`"git" push origin HEAD:` + a,
	} {
		if deny, why := hookGate(t, dir, "s-1", "Bash", "", c); deny {
			t.Errorf("%q: refused: %s", c, why)
		}
	}
	for _, c := range []string{"go test ./...", "ls", "go test ./... 2>&1 | tail -5", "time go test ./..."} {
		if deny, why := hookGate(t, dir, "s-1", "Bash", "", c); deny {
			t.Errorf("%q: refused: %s", c, why)
		}
	}
}

func skillLine(t *testing.T, rel, line string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", ".satelle", "skills", rel))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), line) {
		t.Fatalf("%s no longer holds %q", rel, line)
	}
	return line
}

func TestSkillPushFormsAreRead(t *testing.T) {
	substrate := skillLine(t, "substrate.md", "git push -u origin <id>")
	prReady := skillLine(t, "satelle-pr-ready-check/SKILL.md", "git push -u origin <branch>")
	toMain := skillLine(t, "substrate.md", "git push origin <id>:main")
	release := skillLine(t, "release/SKILL.md", "git push origin HEAD:refs/heads/main")

	dir, a, b := pushFixture(t)
	for _, line := range []string{strings.ReplaceAll(substrate, "<id>", "X"), strings.ReplaceAll(prReady, "<branch>", "X")} {
		if deny, why := hookGate(t, dir, "s-1", "Bash", "", strings.ReplaceAll(line, "X", a)); deny {
			t.Errorf("%q for the session's own item: refused: %s", line, why)
		}
		if deny, _ := hookGate(t, dir, "s-1", "Bash", "", strings.ReplaceAll(line, "X", b)); !deny {
			t.Errorf("%q for another item: allowed", line)
		}
	}

	d := createID(t, "story", "create", "--title", "substrate item", "--workflow", "gated")
	must(t, "story", "seat", d, "--session", "s-d")
	doneItem(t, dir, d)
	if deny, why := hookGate(t, dir, "s-d", "Bash", "", strings.ReplaceAll(toMain, "<id>", d)); deny {
		t.Errorf("a done substrate item's push to main: refused: %s", why)
	}
	r := createID(t, "story", "create", "--title", "release run", "--workflow", "gated")
	must(t, "story", "seat", r, "--session", "s-r")
	if deny, why := hookGate(t, dir, "s-r", "Bash", "", release); deny {
		t.Errorf("the release push to main: refused: %s", why)
	}
}

func doneItem(t *testing.T, dir, id string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "ok.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	must(t, "story", "set", id, "--status", "st-two")
	must(t, "story", "set", id, "--status", "st-three")
}

func TestEditGateAllowsReleaseAndSubstratePushesToMain(t *testing.T) {
	dir, _, _ := pushFixture(t)
	r := createID(t, "story", "create", "--title", "release run", "--workflow", "gated")
	must(t, "story", "seat", r, "--session", "s-r")
	for _, c := range []string{
		"git push origin HEAD:refs/heads/main",
		"git push -q origin --delete release/" + r,
		"git push -q origin HEAD:refs/heads/release/x",
	} {
		if deny, why := hookGate(t, dir, "s-r", "Bash", "", c); deny {
			t.Errorf("%q: refused: %s", c, why)
		}
	}
	d := createID(t, "story", "create", "--title", "substrate item", "--workflow", "gated")
	must(t, "story", "seat", d, "--session", "s-d")
	doneItem(t, dir, d)
	if deny, why := hookGate(t, dir, "s-d", "Bash", "", "git push origin "+d+":main"); deny {
		t.Errorf("a done substrate item's push to main: refused: %s", why)
	}
	if deny, why := hookGate(t, dir, "s-r", "Bash", "", "git push origin main:"+d); !deny || !strings.Contains(why, d) {
		t.Errorf("a push to item %s's branch from the release session: deny %v, %q", d, deny, why)
	}
}

func TestEditGateRefusesUnreadPushLines(t *testing.T) {
	dir, a, _ := pushFixture(t)
	for _, c := range []string{`cd "$D" && git push origin HEAD:refs/heads/` + a, "time git push origin HEAD:" + a} {
		if deny, why := hookGate(t, dir, "s-1", "Bash", "", c); !deny || !strings.Contains(why, "fail-closed rule") {
			t.Errorf("%q: deny %v, %q; want the fail-closed refusal", c, deny, why)
		}
	}
	if deny, why := hookGate(t, dir, "s-1", "Bash", "", "git -C "+dir+" push origin HEAD:refs/heads/"+a); deny {
		t.Errorf("the named plain rewrite was refused: %s", why)
	}
}
