package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func partFixture(t *testing.T) string {
	t.Helper()
	dir := isolate(t)
	must(t, "init")
	writeGatedWorkflow(t, dir)
	writeRepos(t, dir, "x", "y")
	sat := filepath.Join(dir, ".satelle")
	toml, err := os.ReadFile(filepath.Join(sat, "satelle.toml"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sat, "satelle.toml"), string(toml)+"\n[[workflows.rule]]\nworkflow = \"gated\"\ncategory = \"c\"\n")
	writeFile(t, filepath.Join(sat, "repos", "x", "satelle.toml"), "[[workflows.rule]]\nworkflow = \"infra\"\ncategory = \"c\"\n")
	writeFile(t, filepath.Join(sat, "repos", "x", "workflows", "infra.toml"), partInfraWorkflow)
	must(t, "validate")
	return dir
}

func workflowOfItem(t *testing.T, id string) string {
	t.Helper()
	w, _ := itemJSON(t, "story", id)["workflow"].(string)
	return w
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
	must(t, "validate")
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

func TestPartMismatchEditGate(t *testing.T) {
	dir := partFixture(t)
	self := createID(t, "story", "create", "--title", "s", "--category", "c")
	must(t, "story", "seat", self, "--session", "s-0")
	must(t, "story", "set", self, "--status", "st-two")
	if deny, why := hookGate(t, dir, "s-0", "Edit", "main.go", ""); deny {
		t.Fatalf("control: a seated edit on a shared workflow was denied: %s", why)
	}
	id := partMismatchItem(t, dir)
	if deny, _ := hookGate(t, dir, "s-1", "Edit", "main.go", ""); !deny {
		t.Error("an item on another repository's workflow was granted the tree")
	}
	k, _, err := openKernel()
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	it, err := k.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	var mismatch *kernel.TargetMismatch
	if scope, err := editScope(k.Workflows, it); scope != "" || !errors.As(err, &mismatch) {
		t.Errorf("editScope = %q, %v; want a TargetMismatch", scope, err)
	}
	if g := grantAt(k, "s-1", dir); g.Tree || g.AttachOnly {
		t.Errorf("grantAt = %+v, want no grant", g)
	}
	seats, err := seatsOf(k, "s-1")
	if err != nil || len(seats) != 1 || seats[0].Performing {
		t.Errorf("seatsOf = %+v, %v", seats, err)
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
	writeFile(t, filepath.Join(dir, ".satelle", "repos", "x", "satelle.toml"), "[[workflows.rule]]\nworkflow = \"nope\"\ncategory = \"c\"\n")
	if _, err := run(t, "", "validate"); err == nil {
		t.Fatal("a part rule naming an undefined workflow validated")
	}
	if deny, _ := hookGate(t, dir, "s-1", "Edit", "main.go", ""); !deny {
		t.Error("with an invalid configuration a part item was granted the tree")
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
				plantRepo(t, id, "y")
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
	ws := map[string]*workflow.Workflow{"x/infra": {Name: "x/infra", Part: "x", States: map[string]workflow.State{"shipped": {Terminal: true}}}}
	if !itemTerminal(item.Item{Workflow: "x/infra", Status: "shipped", Repo: "x"}, ws) {
		t.Error("an x item at shipped is not terminal")
	}
	if itemTerminal(item.Item{Workflow: "x/infra", Status: "shipped", Repo: "y"}, ws) {
		t.Error("a y item on x's workflow counted as terminal")
	}
}
