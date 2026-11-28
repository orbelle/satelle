package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/hook"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/workflow"
)

const partInfraWorkflow = `[states.intake]
start = true
[states.build]
performer = { agent = "agent-x9", skill = "work" }
[states.shipped]
terminal = true
[[transition]]
from = ["intake"]
to = "build"
[[transition]]
from = ["build"]
to = "shipped"
`

const foreignPartWorkflow = `[states.intake]
start = true
[states.build]
performer = { agent = "infra-reviewer", skill = "infra-tests-check" }
[states.shipped]
terminal = true
[[transition]]
from = ["intake"]
to = "build"
[[transition]]
from = ["build"]
to = "shipped"
`

func gatedFixture(t *testing.T) string {
	t.Helper()
	dir := isolate(t)
	must(t, "init")
	writeGatedWorkflow(t, dir)
	sat := filepath.Join(dir, ".satelle")
	toml, err := os.ReadFile(filepath.Join(sat, "satelle.toml"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sat, "satelle.toml"), string(toml)+"\n[[workflows.rule]]\nworkflow = \"gated\"\ncategory = \"c\"\n")
	return dir
}

func partFixture(t *testing.T) string {
	t.Helper()
	dir := gatedFixture(t)
	writeRepos(t, dir, "x", "y")
	sat := filepath.Join(dir, ".satelle")
	writeFile(t, filepath.Join(sat, "repos", "x", "satelle.toml"), "[[workflows.rule]]\nworkflow = \"infra\"\ncategory = \"c\"\n")
	writeFile(t, filepath.Join(sat, "repos", "x", "workflows", "infra.toml"), partInfraWorkflow)
	must(t, "validate")
	return dir
}

func writeForeignPart(t *testing.T, dir string) {
	t.Helper()
	sat := filepath.Join(dir, ".satelle", "repos", "infra")
	writeFile(t, filepath.Join(sat, "satelle.toml"), "[[workflows.rule]]\nworkflow = \"delivery\"\ncategory = \"c\"\n")
	writeFile(t, filepath.Join(sat, "workflows", "delivery.toml"), foreignPartWorkflow)
}

func workflowOfItem(t *testing.T, id string) string {
	t.Helper()
	w, _ := itemJSON(t, "story", id)["workflow"].(string)
	return w
}

func plantSeatAt(t *testing.T, id, session, tree string) {
	t.Helper()
	k, _, err := openKernel()
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	machine, host := thisMachine(t)
	if _, err := k.Ledger.Append(id, item.KindSeatBound, "test", item.Seat{Session: session, Location: kernel.LocalLocation(tree), Machine: machine, Host: host}); err != nil {
		t.Fatal(err)
	}
	if err := k.Cache.SyncItem(k.Ledger, id); err != nil {
		t.Fatal(err)
	}
}

func retargetItem(t *testing.T, id, repo string) {
	t.Helper()
	k, _, err := openKernel()
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	if _, err := k.Ledger.Append(id, item.KindUpdated, "remote", item.Fields{Repo: &repo}); err != nil {
		t.Fatal(err)
	}
	if err := k.Cache.SyncItem(k.Ledger, id); err != nil {
		t.Fatal(err)
	}
}

func openTestKernel(t *testing.T) *kernel.Kernel {
	t.Helper()
	k, _, err := openKernel()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	return k
}

func partGate(k *kernel.Kernel, session, tree string) hook.Gate {
	return hook.Gate{Root: tree, TempRoots: []string{}, Exempt: k.Settings.Gate.EditExemptPaths, Locked: k.Settings.Gate.LockedPaths,
		Grant: func(tr string) hook.Grant { return grantAt(k, session, tr) }}
}

func refusesTree(t *testing.T, g hook.Gate, tree, want string) {
	t.Helper()
	for _, rel := range []string{"main.go", "notes/a.md"} {
		v := g.Edit(filepath.Join(tree, rel))
		if v.Allow || !strings.Contains(v.Reason, "cannot be read") || !strings.Contains(v.Reason, want) {
			t.Errorf("edit of %s: allow %v, %q", rel, v.Allow, v.Reason)
		}
	}
}

func allowsTree(t *testing.T, g hook.Gate, tree string) {
	t.Helper()
	for _, rel := range []string{"main.go", "notes/a.md"} {
		if v := g.Edit(filepath.Join(tree, rel)); !v.Allow {
			t.Errorf("control: edit of %s refused: %s", rel, v.Reason)
		}
	}
}

func hasIssue(issues []doctorIssue, want string) bool {
	for _, is := range issues {
		if strings.Contains(is.Problem, want) {
			return true
		}
	}
	return false
}

func TestRoutePrintsThePartWorkflow(t *testing.T) {
	partFixture(t)
	id := createID(t, "story", "create", "--title", "a", "--repo", "x", "--category", "c")
	if w := workflowOfItem(t, id); w != "x/infra" {
		t.Fatalf("workflow %q, want x/infra", w)
	}
	out := must(t, "story", "route", id)
	for _, want := range []string{"workflow x/infra", "intake", "build", "shipped"} {
		if !strings.Contains(out, want) {
			t.Errorf("route lacks %q:\n%s", want, out)
		}
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--category", "c"}, "gated"},
		{[]string{"--category", "c", "--repo", "self"}, "gated"},
		{[]string{"--category", "c", "--repo", "y"}, "gated"},
		{[]string{"--category", "d", "--repo", "x"}, "default"},
	} {
		id := createID(t, append([]string{"story", "create", "--title", "b"}, c.args...)...)
		if w := workflowOfItem(t, id); w != c.want {
			t.Errorf("%v: workflow %q, want %q", c.args, w, c.want)
		}
	}
	out = must(t, "validate")
	if strings.Contains(out, "repository parts") {
		t.Errorf("validate shows part sections for a part that resolves:\n%s", out)
	}
}

func TestPartWorkflowRepoChangeIsRefused(t *testing.T) {
	partFixture(t)
	id := createID(t, "story", "create", "--title", "a", "--repo", "x", "--category", "c")
	for _, to := range []string{"y", "self"} {
		if out, err := run(t, "", "story", "set", id, "--repo", to); err == nil || !strings.Contains(out+err.Error(), "repository x") {
			t.Errorf("set --repo %s: %v\n%s", to, err, out)
		}
	}
	if m := itemJSON(t, "story", id); m["repo"] != "x" || m["workflow"] != "x/infra" {
		t.Errorf("the item changed: %v %v", m["repo"], m["workflow"])
	}
	if out, err := run(t, "", "story", "create", "--title", "b", "--workflow", "x/infra"); err == nil || !strings.Contains(out+err.Error(), "serves only") {
		t.Errorf("a self item was created on a part workflow: %v\n%s", err, out)
	}
}

func partMismatchItem(t *testing.T, dir string) string {
	t.Helper()
	id := createID(t, "story", "create", "--title", "a", "--repo", "x", "--category", "c")
	must(t, "story", "set", id, "--status", "build")
	plantRepo(t, id, "self")
	if out, err := run(t, "", "story", "seat", id, "--session", "s-1"); err == nil || !strings.Contains(out+err.Error(), "serves only") {
		t.Fatalf("a mismatched item was seated: %v\n%s", err, out)
	}
	machine, host := thisMachine(t)
	staleSeat(t, id, "s-1", machine, host)
	return id
}

func mismatchedSeat(t *testing.T, session, dir string) string {
	t.Helper()
	id := createID(t, "story", "create", "--title", "a", "--repo", "x", "--category", "c")
	must(t, "story", "set", id, "--status", "build")
	retargetItem(t, id, "self")
	plantSeatAt(t, id, session, dir)
	return id
}

func TestPartMismatchEditGate(t *testing.T) {
	dir := partFixture(t)
	control := createID(t, "story", "create", "--title", "c", "--repo", "x", "--category", "c")
	must(t, "story", "set", control, "--status", "build")
	plantSeatAt(t, control, "s-0", dir)
	id := mismatchedSeat(t, "s-1", dir)
	for _, rel := range []string{"main.go", "notes/a.md"} {
		if deny, why := hookGate(t, dir, "s-1", "Edit", rel, ""); !deny || !strings.Contains(why, "serves only") {
			t.Errorf("edit of %s from a mismatched seat: deny %v, %q", rel, deny, why)
		}
	}
	k := openTestKernel(t)
	if g := grantAt(k, "s-0", dir); g.AttachOnly || g.Unread != "" {
		t.Errorf("control: grantAt = %+v", g)
	}
	it, err := k.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	var mismatch *kernel.TargetMismatch
	if scope, err := editScope(k.Resolver(), it); scope != "" || !errors.As(err, &mismatch) || !kernel.Refused(err) {
		t.Errorf("editScope = %q, %v; want a TargetMismatch", scope, err)
	}
	g := grantAt(k, "s-1", dir)
	if g.Tree || !g.AttachOnly || g.Item != id || g.State != "build" || !strings.Contains(g.Unread, "serves only") {
		t.Errorf("grantAt = %+v", g)
	}
	refusesTree(t, partGate(k, "s-1", dir), dir, "serves only")
	seats, err := seatsOf(k, "s-1")
	if err != nil || len(seats) != 1 || seats[0].Performing {
		t.Errorf("seatsOf = %+v, %v", seats, err)
	}
}

func TestPartRefusalConcurrentSelfSeat(t *testing.T) {
	for _, selfFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "self first", false: "part first"}[selfFirst], func(t *testing.T) {
			dir := partFixture(t)
			self := createID(t, "story", "create", "--title", "s", "--category", "c")
			must(t, "story", "set", self, "--status", "st-two")
			control := createID(t, "story", "create", "--title", "c", "--repo", "x", "--category", "c")
			must(t, "story", "set", control, "--status", "build")
			plantSeatAt(t, self, "s-0", dir)
			plantSeatAt(t, control, "s-0", dir)
			var id string
			if selfFirst {
				plantSeatAt(t, self, "s-1", dir)
				id = mismatchedSeat(t, "s-1", dir)
			} else {
				id = mismatchedSeat(t, "s-1", dir)
				plantSeatAt(t, self, "s-1", dir)
			}
			k := openTestKernel(t)
			if g := grantAt(k, "s-0", dir); !g.Tree || g.AttachOnly {
				t.Errorf("control: grantAt = %+v", g)
			}
			allowsTree(t, partGate(k, "s-0", dir), dir)
			g := grantAt(k, "s-1", dir)
			if g.Tree || !g.AttachOnly || g.Item != id || !strings.Contains(g.Unread, "serves only") {
				t.Errorf("grantAt = %+v", g)
			}
			refusesTree(t, partGate(k, "s-1", dir), dir, "serves only")
		})
	}
}

func TestPartRefusalPackGate(t *testing.T) {
	for _, mismatched := range []bool{false, true} {
		t.Run(map[bool]string{false: "control", true: "mismatched"}[mismatched], func(t *testing.T) {
			dir := partFixture(t)
			self := createID(t, "story", "create", "--title", "s", "--category", "c")
			must(t, "story", "set", self, "--status", "st-two")
			plantSeatAt(t, self, "s-1", dir)
			if mismatched {
				mismatchedSeat(t, "s-1", dir)
			}
			t.Setenv("SATELLE_SESSION", "s-1")
			_, check, closeFn, err := treeEditCheck("install", false)
			if err != nil {
				t.Fatal(err)
			}
			defer closeFn()
			for _, rel := range []string{"main.go", "notes/a.md"} {
				err := check(filepath.Join(dir, rel))
				switch {
				case mismatched && (err == nil || !strings.Contains(err.Error(), "serves only")):
					t.Errorf("pack check of %s: %v", rel, err)
				case !mismatched && err != nil:
					t.Errorf("control: pack check of %s: %v", rel, err)
				}
			}
		})
	}
}

func TestPartRefusalLinkedWorktree(t *testing.T) {
	dir := gatedFixture(t)
	writeRepos(t, dir, "infra", "y")
	infra := createID(t, "story", "create", "--title", "i", "--repo", "infra", "--category", "c")
	self := createID(t, "story", "create", "--title", "s", "--category", "c")
	must(t, "story", "set", self, "--status", "st-two")
	linked := filepath.Join(t.TempDir(), "linked")
	gittest.Git(t, dir, "worktree", "add", "-q", linked, "-b", "linked")
	plantSeatAt(t, infra, "s-1", linked)
	plantSeatAt(t, self, "s-1", linked)
	k := openTestKernel(t)
	if g := grantAt(k, "s-1", linked); !g.Tree || g.AttachOnly {
		t.Errorf("control: grantAt = %+v", g)
	}
	allowsTree(t, partGate(k, "s-1", linked), linked)
	k.Close()
	writeForeignPart(t, dir)
	k = openTestKernel(t)
	g := grantAt(k, "s-1", linked)
	if g.Tree || !g.AttachOnly || g.Item != infra || !strings.Contains(g.Unread, "does not resolve") {
		t.Errorf("grantAt = %+v", g)
	}
	refusesTree(t, partGate(k, "s-1", linked), linked, "does not resolve")
	it, err := k.Get(infra)
	if err != nil {
		t.Fatal(err)
	}
	if itemTerminal(it, k.Resolver()) {
		t.Error("an unresolved part's story counted as terminal")
	}
	verdict := pushVerdict("s-1", infra, k.Cache.Item,
		func() ([]item.Seat, error) { return k.Cache.Seats("s-1", "") },
		func(it item.Item) (string, error) { return editScope(k.Resolver(), it) })
	if verdict.Allow || !strings.Contains(verdict.Reason, "does not resolve") {
		t.Errorf("push of an unresolved part's story: %+v", verdict)
	}
}

func TestPartUnregisteredValidates(t *testing.T) {
	dir := gatedFixture(t)
	writeForeignPart(t, dir)
	out := must(t, "validate")
	for _, want := range []string{"configuration valid", "repository parts not loaded in", "repos/infra: not loaded: repos.toml registers no valid repository of that name"} {
		if !strings.Contains(out, want) {
			t.Errorf("validate lacks %q:\n%s", want, out)
		}
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(must(t, "validate", "--json")), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["valid"] != true || doc["part_warnings"] == nil || doc["part_problems"] != nil {
		t.Errorf("validate --json: %v", doc)
	}
	in := mustDoctorInput(t, dir)
	issues, _ := checkRepos(context.Background(), in)
	if !hasIssue(issues, "repos/infra: not loaded") {
		t.Errorf("doctor repos issues: %+v", issues)
	}
	id := createID(t, "story", "create", "--title", "s", "--category", "c")
	if w := workflowOfItem(t, id); w != "gated" {
		t.Errorf("self story on %q", w)
	}
}

func TestPartUnresolvedValidates(t *testing.T) {
	dir := gatedFixture(t)
	writeRepos(t, dir, "infra", "y")
	writeForeignPart(t, dir)
	out := must(t, "validate")
	for _, want := range []string{"configuration valid", "repository parts that do not resolve in", "repos/infra: its stories are refused until it resolves", "infra-reviewer", "infra-tests-check"} {
		if !strings.Contains(out, want) {
			t.Errorf("validate lacks %q:\n%s", want, out)
		}
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(must(t, "validate", "--json")), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["valid"] != true || doc["part_problems"] == nil || doc["part_warnings"] != nil {
		t.Errorf("validate --json: %v", doc)
	}
	if out, err := run(t, "", "story", "create", "--title", "a", "--repo", "infra", "--category", "c"); err == nil || !strings.Contains(out+err.Error(), "does not resolve") {
		t.Errorf("create --repo infra: %v\n%s", err, out)
	}
	for _, repo := range []string{"self", "y"} {
		id := createID(t, "story", "create", "--title", "b", "--repo", repo, "--category", "c")
		if w := workflowOfItem(t, id); w != "gated" {
			t.Errorf("repo %s story on %q", repo, w)
		}
	}
	in := mustDoctorInput(t, dir)
	issues, _ := checkRepos(context.Background(), in)
	if !hasIssue(issues, "infra-reviewer") || !hasIssue(issues, "its stories are refused until it resolves") {
		t.Errorf("doctor repos issues: %+v", issues)
	}
}

func TestPartMismatchPushGate(t *testing.T) {
	dir := partFixture(t)
	id := partMismatchItem(t, dir)
	deny, why := hookGate(t, dir, "s-1", "Bash", "", "git push origin HEAD:"+id)
	if !deny || !strings.Contains(why, "cannot be read") || !strings.Contains(why, "serves only") {
		t.Errorf("push of a mismatched item's branch: deny %v, %q", deny, why)
	}
}

func TestPartMismatchRouteIsRefused(t *testing.T) {
	dir := partFixture(t)
	id := partMismatchItem(t, dir)
	if out, err := run(t, "", "story", "route", id); err == nil || !strings.Contains(out+err.Error(), "serves only") {
		t.Errorf("route of a mismatched item: %v\n%s", err, out)
	}
}

func TestPartMismatchUnconfiguredGate(t *testing.T) {
	dir := partFixture(t)
	partMismatchItem(t, dir)
	sat := filepath.Join(dir, ".satelle")
	toml, err := os.ReadFile(filepath.Join(sat, "satelle.toml"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sat, "satelle.toml"), string(toml)+"\n[[workflows.rule]]\nworkflow = \"nope\"\ncategory = \"z\"\n")
	if _, err := run(t, "", "validate"); err == nil {
		t.Fatal("a shared rule naming an undefined workflow validated")
	}
	for _, rel := range []string{"main.go", "notes/a.md"} {
		if deny, _ := hookGate(t, dir, "s-1", "Edit", rel, ""); !deny {
			t.Errorf("with an invalid configuration a part item's seat left %s editable", rel)
		}
	}
}

func TestPartMismatchDoctorKeepsSeat(t *testing.T) {
	for _, retarget := range []bool{false, true} {
		t.Run(map[bool]string{false: "control", true: "retargeted"}[retarget], func(t *testing.T) {
			dir := partFixture(t)
			id := createID(t, "story", "create", "--title", "a", "--repo", "x", "--category", "c")
			must(t, "story", "set", id, "--status", "build")
			must(t, "story", "set", id, "--status", "shipped")
			if retarget {
				retargetItem(t, id, "y")
			}
			staleSeat(t, id, "s-e", "m-other", "other-host")
			in := mustDoctorInput(t, dir)
			issues, _ := checkSeats(context.Background(), in)
			released := false
			for _, is := range issues {
				if is.item == id && is.Resolve == resolveReleaseSeat {
					released = true
				}
			}
			if released == retarget {
				t.Errorf("release seat issue %v with retarget %v: %+v", released, retarget, issues)
			}
		})
	}
}

func TestItemTerminalGuardsThePart(t *testing.T) {
	res := kernel.Resolver{Workflows: map[string]*workflow.Workflow{"x/infra": {Name: "x/infra", Part: "x", States: map[string]workflow.State{"shipped": {Terminal: true}}}}}
	if !itemTerminal(item.Item{Workflow: "x/infra", Status: "shipped", Repo: "x"}, res) {
		t.Error("an x item at shipped is not terminal")
	}
	if itemTerminal(item.Item{Workflow: "x/infra", Status: "shipped", Repo: "y"}, res) {
		t.Error("a y item on x's workflow counted as terminal")
	}
	res.Unresolved = map[string][]error{"x": {errors.New("p")}}
	if itemTerminal(item.Item{Workflow: "x/infra", Status: "shipped", Repo: "x"}, res) {
		t.Error("an unresolved part's item counted as terminal")
	}
}
