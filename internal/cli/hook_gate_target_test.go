//go:build unix

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gittest"
)

type targetFix struct{ ctl, a, b string }

func noTempRoots(t *testing.T) {
	t.Helper()
	tempRootsOverride = []string{}
	t.Cleanup(func() { tempRootsOverride = nil })
}

func seatedTargetFix(t *testing.T) targetFix {
	t.Helper()
	f := targetFix{ctl: isolate(t)}
	noTempRoots(t)
	must(t, "init")
	writePlanWorkflow(t, f.ctl)
	f.a = checkoutWith(t, "https://github.com/o/a")
	f.b = checkoutWith(t, "https://github.com/o/b")
	writeRepos(t, f.ctl, "a", "b")
	writeFile(t, filepath.Join(f.ctl, ".satelle", "repos.local.toml"), "[repos.a]\npath = \""+f.a+"\"\n[repos.b]\npath = \""+f.b+"\"\n")
	must(t, "validate")
	return f
}

func seatOnTarget(t *testing.T, repo, status string) string {
	t.Helper()
	args := []string{"story", "create", "--title", "t", "--workflow", "planned"}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	id := createID(t, args...)
	must(t, "story", "set", id, "--status", "st-plan")
	if status == "st-build" {
		must(t, "story", "set", id, "--status", "st-build")
	}
	must(t, "story", "seat", id, "--session", "s-1")
	return id
}

func gateAt(t *testing.T, cwd, tool, path, command string) (deny bool, reason string) {
	t.Helper()
	out, err := run(t, claudePayload("s-1", cwd, tool, path, command), "hook", "gate", "--harness", "claude")
	if err != nil {
		t.Fatalf("hook gate: %v\n%s", err, out)
	}
	if strings.TrimSpace(out) == "" {
		return false, ""
	}
	var doc struct {
		HookSpecificOutput struct {
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("hook output %q: %v", out, err)
	}
	return doc.HookSpecificOutput.PermissionDecision == "deny", doc.HookSpecificOutput.PermissionDecisionReason
}

func wantWrite(t *testing.T, cwd, path string, allowed bool, want ...string) {
	t.Helper()
	wantGate(t, cwd, "Write", path, "", allowed, want...)
}

func wantGate(t *testing.T, cwd, tool, path, command string, allowed bool, want ...string) {
	t.Helper()
	deny, reason := gateAt(t, cwd, tool, path, command)
	if deny == allowed {
		t.Fatalf("%s %s%s: deny = %v, want %v: %s", tool, path, command, deny, !allowed, reason)
	}
	for _, w := range want {
		if !strings.Contains(reason, w) {
			t.Errorf("%s %s%s: reason lacks %q: %s", tool, path, command, w, reason)
		}
	}
}

func TestHookGateSeatedTarget(t *testing.T) {
	f := seatedTargetFix(t)
	c := checkoutWith(t, "https://github.com/o/c")
	id := seatOnTarget(t, "a", "st-build")

	wantWrite(t, f.ctl, filepath.Join(f.a, "x.go"), true)
	wantWrite(t, f.ctl, filepath.Join(f.b, "x.go"), false, "repo a at "+f.a, id)
	wantWrite(t, f.ctl, filepath.Join(c, "x.go"), false, "repo a at "+f.a)
	wantWrite(t, f.ctl, filepath.Join(f.ctl, "main.go"), false, "targets repo a")

	wantGate(t, f.ctl, "Bash", "", "git -C "+f.b+" commit -m x", false, "repo a at "+f.a)
	wantGate(t, f.ctl, "Bash", "", "git -C "+f.a+" commit -m x", true)

	local := filepath.Join(f.ctl, ".satelle", "repos.local.toml")
	mapping := readFileString(t, local)
	writeFile(t, local, "[repos.b]\npath = \""+f.b+"\"\n")
	wantWrite(t, f.ctl, filepath.Join(f.ctl, "main.go"), false, "targets repo a", "no local checkout mapped")
	writeFile(t, local, mapping)
	gittest.Git(t, f.a, "remote", "set-url", "origin", "https://github.com/o/other")
	wantWrite(t, f.ctl, filepath.Join(f.a, "x.go"), false, "has origin", "registers")
	gittest.Git(t, f.a, "remote", "set-url", "origin", "https://github.com/o/a")
	wantWrite(t, f.ctl, filepath.Join(f.a, "x.go"), true)

	must(t, "story", "release", id, "--session", "s-1")
	seatOnTarget(t, "a", "st-plan")
	for _, p := range []string{filepath.Join(f.a, "x.go"), filepath.Join(f.ctl, "main.go"), filepath.Join(f.ctl, "notes", "n.md")} {
		wantWrite(t, f.ctl, p, false, "st-plan")
	}
}

func TestHookGateSelfSeatWithRegistry(t *testing.T) {
	f := seatedTargetFix(t)
	for _, repo := range []string{"self", ""} {
		id := seatOnTarget(t, repo, "st-build")
		wantWrite(t, f.ctl, filepath.Join(f.ctl, "main.go"), true)
		wantWrite(t, f.ctl, filepath.Join(f.a, "x.go"), false, "another repository's working tree")
		must(t, "story", "release", id, "--session", "s-1")
	}
}

func twoNamesFix(t *testing.T) (f targetFix, s1, s2 string) {
	t.Helper()
	f = seatedTargetFix(t)
	link := filepath.Join(canonical(t.TempDir()), "a-link")
	if err := os.Symlink(f.a, link); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(f.ctl, ".satelle", "repos.toml"), "[repos.a]\norigin = \"https://github.com/o/a\"\n[repos.a2]\norigin = \"https://github.com/o/a\"\n")
	writeFile(t, filepath.Join(f.ctl, ".satelle", "repos.local.toml"), "[repos.a]\npath = \""+f.a+"\"\n[repos.a2]\npath = \""+link+"\"\n")
	must(t, "validate")
	return f, seatOnTarget(t, "a", "st-build"), seatOnTarget(t, "a2", "st-plan")
}

func TestHookGateTwoNamesOneCheckout(t *testing.T) {
	f, _, s2 := twoNamesFix(t)
	wantWrite(t, f.ctl, filepath.Join(f.a, "x.go"), false, "st-plan", s2)
	must(t, "story", "release", s2, "--session", "s-1")
	wantWrite(t, f.ctl, filepath.Join(f.a, "x.go"), true)
}

func TestSeatedTargetsMergeByTree(t *testing.T) {
	f, _, s2 := twoNamesFix(t)
	r, err := findRepo(f.ctl)
	if err != nil {
		t.Fatal(err)
	}
	k, err := r.open()
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	got := seatedTargets(k, r, "s-1")
	if len(got) != 1 {
		t.Fatalf("targets %+v, want one", got)
	}
	if g := got[0]; !reflect.DeepEqual(g.Names, []string{"a", "a2"}) || g.Tree != f.a || !g.Grant.AttachOnly || g.Item != s2 {
		t.Fatalf("target %+v", g)
	}
}

func TestControlGrantSelfOnly(t *testing.T) {
	f := seatedTargetFix(t)
	r, err := findRepo(f.ctl)
	if err != nil {
		t.Fatal(err)
	}
	check := func(want bool) {
		t.Helper()
		k, err := r.open()
		if err != nil {
			t.Fatal(err)
		}
		defer k.Close()
		if got := grantAt(k, "s-1", f.ctl).Tree; got != want {
			t.Errorf("grantAt Tree = %v, want %v", got, want)
		}
		if got := seatGrantUnconfigured(r, "s-1", f.ctl).Tree; got != want {
			t.Errorf("seatGrantUnconfigured Tree = %v, want %v", got, want)
		}
	}
	id := seatOnTarget(t, "a", "st-build")
	check(false)
	must(t, "story", "release", id, "--session", "s-1")
	seatOnTarget(t, "", "st-build")
	check(true)
}

func TestPackTreeCheckExternalSeat(t *testing.T) {
	f := seatedTargetFix(t)
	t.Setenv("SATELLE_SESSION", "s-1")
	broken := filepath.Join(f.ctl, ".satelle", "workflows", "broken.toml")
	paths := []string{filepath.Join(f.ctl, ".satelle", "packs.toml"), filepath.Join(f.ctl, ".satelle", "skills", "x", "SKILL.md")}
	verify := func(wantErr bool) {
		t.Helper()
		for _, prospective := range []bool{false, true} {
			if prospective {
				writeFile(t, broken, "name = [\n")
			}
			_, check, closeFn, err := treeEditCheck("pack install", prospective)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range paths {
				if err := check(p); (err != nil) != wantErr {
					t.Errorf("prospective %v %s: err %v, want error %v", prospective, p, err, wantErr)
				}
			}
			closeFn()
			if prospective {
				if err := os.Remove(broken); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	id := seatOnTarget(t, "a", "st-build")
	verify(true)
	must(t, "story", "release", id, "--session", "s-1")
	seatOnTarget(t, "", "st-build")
	verify(false)
}

func TestHookGateRegistryRemoved(t *testing.T) {
	for _, mode := range []string{"deleted", "emptied"} {
		t.Run(mode, func(t *testing.T) {
			f := seatedTargetFix(t)
			plain := canonical(t.TempDir())
			seatOnTarget(t, "a", "st-build")
			tracked := filepath.Join(f.ctl, ".satelle", "repos.toml")
			if mode == "deleted" {
				for _, p := range []string{tracked, filepath.Join(f.ctl, ".satelle", "repos.local.toml")} {
					if err := os.Remove(p); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				writeFile(t, tracked, "")
				writeFile(t, filepath.Join(f.ctl, ".satelle", "repos.local.toml"), "")
			}
			for _, p := range []string{filepath.Join(f.ctl, "main.go"), filepath.Join(f.ctl, "notes", "n.md"), filepath.Join(f.a, "x.go"), filepath.Join(plain, "x")} {
				wantWrite(t, f.ctl, p, false, "targets repo a", "not registered in .satelle/repos.toml")
			}
		})
	}
}

func TestHookGateSingleRepoFenceUnchanged(t *testing.T) {
	ctl := isolate(t)
	noTempRoots(t)
	must(t, "init")
	writePlanWorkflow(t, ctl)
	foreign := checkoutWith(t, "")
	p := filepath.Join(foreign, "x.go")
	want := "satelle: " + p + " is in another repository's working tree (" + foreign + "). This session works in " + ctl + "; edit another repository from a session there."
	expect := func() {
		t.Helper()
		deny, reason := gateAt(t, ctl, "Write", p, "")
		if !deny || reason != want {
			t.Fatalf("deny %v reason %q\nwant %q", deny, reason, want)
		}
	}
	expect()
	id := seatOnTarget(t, "", "st-build")
	expect()
	must(t, "story", "release", id, "--session", "s-1")
	expect()
}
