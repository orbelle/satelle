package hook

import (
	"strings"
	"testing"
)

func refuseBranch(target string) func(string) Verdict {
	return func(branch string) Verdict {
		if branch == target {
			return Verdict{Reason: "push to " + branch + " refused"}
		}
		return allow
	}
}

func TestGateJudgesPushes(t *testing.T) {
	root := t.TempDir()
	g := Gate{Root: root, TempRoots: []string{}, Grant: func(string) Grant { return Grant{Tree: true} }, Push: refuseBranch("sto_b")}
	for _, c := range []string{
		"git push origin HEAD:sto_b",
		"git push origin x:refs/heads/sto_b",
		"git push origin --delete sto_b",
		"git push origin sto_b",
		"git push origin HEAD:sto_a HEAD:sto_b",
		"A+=1 git push origin HEAD:sto_b",
		`A="1" "git" push origin HEAD:sto_b`,
		`A='1' 'git' push origin HEAD:sto_b`,
		`A+="1" "git" push origin HEAD:sto_b`,
		"A=1; git push origin HEAD:sto_b",
	} {
		if v := g.Bash(c, root); v.Allow || !strings.Contains(v.Reason, "sto_b") {
			t.Errorf("%q: %+v, want a refusal naming sto_b", c, v)
		}
	}
	for _, c := range []string{"git push origin HEAD:sto_a", "git status", "go test ./..."} {
		if v := g.Bash(c, root); !v.Allow {
			t.Errorf("%q: refused: %s", c, v.Reason)
		}
	}
	called := false
	probe := g
	probe.Push = func(string) Verdict { called = true; return allow }
	for _, c := range []string{"git status", "go test ./...", "ls -la", "git log --oneline | grep push"} {
		probe.Bash(c, root)
	}
	if called {
		t.Error("Push was called for a command that holds no push")
	}
	nilPush := g
	nilPush.Push = nil
	if v := nilPush.Bash("git push origin HEAD:sto_b", root); !v.Allow {
		t.Errorf("with Push nil a push was judged: %s", v.Reason)
	}
}
