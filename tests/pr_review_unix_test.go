//go:build integration && unix

package tests

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
	"github.com/bobmcallan/satelle/internal/workflow"
)

const appAccount = "344745317"

const orbelleReviewerAccount = 350948835

const (
	repointedReviewID = 5815187973
	repointedGiven    = "6109ffe2f71d13665ce69edb01104e3599067a8d"
	repointedReported = "f93eef33d5814570f648a88df4f207b23e9f9996"
)

func tomlGates(gates []string) string {
	return `"` + strings.Join(gates, `", "`) + `"`
}

const statusSkill = "satelle-gate-status-publish"

const runPublishSkill = "satelle-pr-review-run-publish"

const reviewSession = "s-review"

var prGateNames = []string{"pr-pending", "pr-ready", "pr-approval"}

func repoWorkflow(t *testing.T, name string) *workflow.Workflow {
	t.Helper()
	w, err := workflow.Parse("../.satelle/workflows/" + name + ".toml")
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func repoPRGates(t *testing.T) (review, approval workflow.Gate) {
	t.Helper()
	g := repoPRGateSet(t)
	return g["pr-review"], g["pr-approval"]
}

func repoPRGateSet(t *testing.T) map[string]workflow.Gate {
	t.Helper()
	var got [2][]workflow.Gate
	for i, wf := range []string{"delivery", "chore"} {
		w := repoWorkflow(t, wf)
		for _, name := range prGateNames {
			g, ok := w.Gates[name]
			if !ok {
				t.Fatalf("%s.toml defines no gate %s", wf, name)
			}
			got[i] = append(got[i], g)
		}
	}
	out := map[string]workflow.Gate{}
	for j, name := range prGateNames {
		out[name] = got[0][j]
		a, b := got[0][j], got[1][j]
		if a.Skill != b.Skill || a.Agent != b.Agent || !slices.Equal(a.After, b.After) || a.Carry != b.Carry || a.Trigger != b.Trigger ||
			!slices.Equal(a.Panel, b.Panel) || !slices.Equal(a.Paths, b.Paths) || a.Combine != b.Combine {
			t.Fatalf("delivery and chore define the gate %s differently: %+v vs %+v", name, a, b)
		}
	}
	g, ok := repoWorkflow(t, "pr-review").Gates["pr-review"]
	if !ok {
		t.Fatal("pr-review.toml defines no gate pr-review")
	}
	out["pr-review"] = g
	return out
}

func repoRouteStates(t *testing.T) (integration, review workflow.State) {
	t.Helper()
	var got [2][2]workflow.State
	for i, wf := range []string{"delivery", "chore"} {
		w := repoWorkflow(t, wf)
		for j, name := range []string{"integration", "in_review"} {
			st, ok := w.States[name]
			if !ok {
				t.Fatalf("%s.toml has no state %s", wf, name)
			}
			got[i][j] = st
		}
	}
	for j, name := range []string{"integration", "in_review"} {
		a, b := got[0][j], got[1][j]
		if a.Waits != b.Waits || !reflect.DeepEqual(a.Hold, b.Hold) {
			t.Fatalf("delivery and chore define the state %s differently: %+v vs %+v", name, a, b)
		}
	}
	return got[0][0], got[0][1]
}

func holdTOML(h *workflow.Hold) string {
	if h == nil {
		return ""
	}
	return fmt.Sprintf("hold = { rounds = %d, to = %q }\n", h.Rounds, h.To)
}

func gateTOML(t *testing.T, name string, g workflow.Gate) string {
	t.Helper()
	if len(g.Paths) > 0 || g.Trigger != "" {
		t.Fatalf("gate %s has fields the sandbox does not copy: %+v", name, g)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[gates.%s]\nskill = %q\n", name, g.Skill)
	if g.Agent != "" {
		fmt.Fprintf(&b, "agent = %q\n", g.Agent)
	}
	if len(g.Panel) > 0 {
		q := make([]string, len(g.Panel))
		for i, a := range g.Panel {
			q[i] = strconv.Quote(a)
		}
		fmt.Fprintf(&b, "panel = [%s]\ncombine = %q\n", strings.Join(q, ", "), g.Combine)
	}
	if g.Carry {
		b.WriteString("carry = true\n")
	}
	if len(g.After) > 0 {
		q := make([]string, len(g.After))
		for i, a := range g.After {
			q[i] = strconv.Quote(a)
		}
		fmt.Fprintf(&b, "after = [%s]\n", strings.Join(q, ", "))
	}
	return b.String()
}

type prSandbox struct {
	*sandbox
	srv      *fakeserver.Server
	fake     string
	head     string
	old      string
	prs      []map[string]any
	reviewer string
	task     string
}

type prNames struct {
	pending, ready, approval, review string
	queue, base, waits               string
	readyOnRoute                     bool
}

func repoQueueBase(t *testing.T) string {
	t.Helper()
	q, err := workflow.ParseQueue("../.satelle/queues/release.toml")
	if err != nil {
		t.Fatal(err)
	}
	if q.Trigger == nil || q.Trigger.Base == "" {
		t.Fatal(".satelle/queues/release.toml names no trigger base")
	}
	return q.Trigger.Base
}

func queueTOML(base, wf string) string {
	return "title = \"Landing of approved stories\"\n\n[trigger]\nworkflow = " + strconv.Quote(wf) + "\nbase = " + strconv.Quote(base) + "\nmin_size = 0\n"
}

func repoPRNames(t *testing.T) prNames {
	t.Helper()
	_, review := repoRouteStates(t)
	return prNames{pending: "pr-pending", ready: "pr-ready", approval: "pr-approval", review: "pr-review", queue: "release", base: repoQueueBase(t), waits: review.Waits}
}

func newPRSandbox(t *testing.T) *prSandbox {
	t.Helper()
	return newPRSandboxNamed(t, repoPRNames(t))
}

func newPRSandboxNamed(t *testing.T, n prNames) *prSandbox {
	t.Helper()
	for _, tool := range []string{"bash", "jq", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s is required by the PR review skills and is not on PATH", tool)
		}
	}
	if _, err := exec.LookPath("sha256sum"); err != nil {
		if _, err := exec.LookPath("shasum"); err != nil {
			t.Fatal("sha256sum or shasum is required by the PR review skills and neither is on PATH")
		}
	}
	srv := fakeserver.Start(t, devToken)
	s := signedIn(t)
	s.must("project", "create", "acme", "--bind", "--server", srv.URL)
	srv.BindGitHub("acme", "orbelle/acme")

	gs := repoPRGateSet(t)
	review, approval, pending, ready := gs["pr-review"], gs["pr-approval"], gs["pr-pending"], gs["pr-ready"]
	codeAC := repoGateAgents(t, ".satelle/workflows/delivery.toml")["code-ac"]
	if review.Skill != "satelle-code-ac-review" || review.Agent != "" || len(review.Panel) != 1 || review.Combine != "all" || !slices.Contains(codeAC, review.Panel[0]) ||
		!slices.Equal(review.After, []string{"satelle-verdict-notes-check", "satelle-verdict-reference-check", runPublishSkill, statusSkill}) {
		t.Fatalf("pr-review is %+v, want the code-ac judge (one of %v) as a one-member panel with the reasons rules, the publish after, then the status", review, codeAC)
	}
	reviewer := review.Panel[0]
	if approval.Skill != "satelle-pr-approval-check" || approval.Agent != "" || !slices.Equal(approval.After, []string{statusSkill}) {
		t.Fatalf("pr-approval is %+v", approval)
	}
	if pending.Skill != statusSkill || pending.Agent != "" || len(pending.After) != 0 {
		t.Fatalf("pr-pending is %+v", pending)
	}
	if ready.Skill != "satelle-pr-ready-check" || ready.Agent != "" || !slices.Equal(ready.After, []string{statusSkill}) {
		t.Fatalf("pr-ready is %+v", ready)
	}
	skills, err := filepath.Glob("../.satelle/skills/*.md")
	if err != nil || len(skills) == 0 {
		t.Fatalf("no repo skills: %v", err)
	}
	for _, f := range skills {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		s.write(".satelle/skills/"+filepath.Base(f), string(b))
	}
	s.write(".satelle/satelle.toml", prSandboxConfig(t))
	s.write(".satelle/agents.toml", fmt.Sprintf("[agents.%q]\n", reviewer)+`command = "claude"
model = "ra-1"
tools = ["Read", "Grep", "Glob"]
[agents.executor]
command = "claude"
model = "ex-1"
tools = ["Read", "Edit"]
`)
	toReview := []string{n.pending}
	if n.readyOnRoute {
		toReview = append(toReview, n.ready)
	}
	gates := gateTOML(t, n.pending, pending) + gateTOML(t, n.ready, ready) + gateTOML(t, n.approval, approval)
	s.write(".satelle/workflows/delivery.toml", prSandboxWorkflowWith(t, gates, toReview, []string{n.approval}, []string{n.queue}, n.waits))
	s.write(".satelle/workflows/pr-review.toml", prReviewWorkflowNamed(n.review, gateTOML(t, n.review, review)))
	s.write(".satelle/queues/"+n.queue+".toml", queueTOML(n.base, "pr-review"))
	fake := realDir(t, t.TempDir())
	writeFile(t, filepath.Join(fake, "claude"), `#!/bin/sh
model=none
prev=
for a in "$@"; do [ "$prev" = "--model" ] && model=$a; prev=$a; done
cat > "$FAKE_DIR/prompt.$model"
v=accept
[ -f "$FAKE_DIR/verdict" ] && v=$(cat "$FAKE_DIR/verdict")
n="$v by $model"
[ -f "$FAKE_DIR/notes" ] && n=$(cat "$FAKE_DIR/notes")
[ -f "$FAKE_DIR/dead" ] && { echo "the judge died" >&2; exit 3; }
printf '{"type":"result","subtype":"success","is_error":false,"result":"{\\"verdict\\":\\"%s\\",\\"notes\\":\\"%s\\"}","usage":{"input_tokens":11,"output_tokens":7}}\n' "$v" "$n"
`, 0o755)
	writeFile(t, filepath.Join(fake, "gh"), `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_DIR/gh.log"
case "$1" in
  pr) cat "$FAKE_DIR/prs.json" ;;
  api) case "$2" in
       */statuses*) printf '%s\n' "$*" >> "$FAKE_DIR/gh-statuses.log"; cat "$FAKE_DIR/statuses.json" ;;
       *) printf '%s\n' "$*" >> "$FAKE_DIR/gh-api.log"; cat "$FAKE_DIR/reviews.json" ;;
     esac ;;
  *) echo "fake gh: unexpected $*" >&2; exit 1 ;;
esac
`, 0o755)
	s.path = append(s.path, fake)
	s.env["FAKE_DIR"] = fake

	s.git("add", "-A")
	s.git("commit", "-q", "-m", "config")
	s.mustConfigValid()
	p := &prSandbox{sandbox: s, srv: srv, fake: fake, reviewer: reviewer}
	p.old = strings.TrimSpace(s.git("rev-parse", "HEAD"))
	s.write("src/main.go", "package main\n")
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "the change")
	p.head = strings.TrimSpace(s.git("rev-parse", "HEAD"))
	p.serveReviews("none")
	p.serveStatuses("by-app")
	return p
}

func prSandboxConfig(t *testing.T) string {
	t.Helper()
	var def struct {
		Workflows struct {
			Rule []struct {
				Workflow string `toml:"workflow"`
				Kind     string `toml:"kind"`
				Tag      string `toml:"tag"`
				Category string `toml:"category"`
			} `toml:"rule"`
		} `toml:"workflows"`
	}
	if _, err := toml.DecodeFile("../.satelle/satelle.toml", &def); err != nil {
		t.Fatal(err)
	}
	ruled := false
	for _, r := range def.Workflows.Rule {
		if r.Workflow == "pr-review" {
			if r.Kind != "execution" || r.Tag != "pr-review" || r.Category != "" || ruled {
				t.Fatalf("satelle.toml selects pr-review by %+v, want one rule: kind execution, tag pr-review", r)
			}
			ruled = true
		}
	}
	if !ruled {
		t.Fatal("satelle.toml has no rule selecting the pr-review workflow")
	}
	return "[workflows]\ndefault = \"delivery\"\n\n[[workflows.rule]]\nworkflow = \"pr-review\"\nkind = \"execution\"\ntag = \"pr-review\"\n"
}

func sequential(gates []string) string {
	if len(gates) == 0 {
		return ""
	}
	return `groups = [{ mode = "sequential", gates = [` + tomlGates(gates) + `] }]
`
}

func prSandboxWorkflow(t *testing.T, gates string, toReview, toApproved []string) string {
	t.Helper()
	_, review := repoRouteStates(t)
	return prSandboxWorkflowWith(t, gates, toReview, toApproved, []string{"release"}, review.Waits)
}

func prSandboxWorkflowWith(t *testing.T, gates string, toReview, toApproved, enqueue []string, waits string) string {
	t.Helper()
	integration, review := repoRouteStates(t)
	approved := "[states.approved]\n"
	if len(enqueue) > 0 {
		approved += "enqueue = [" + tomlGates(enqueue) + "]\n"
	}
	return `name = "delivery"
[states.backlog]
start = true
[states.in_progress]
performer = { agent = "executor", skill = "coder" }
[states.integration]
` + holdTOML(integration.Hold) + `[states.in_review]
waits = ` + strconv.Quote(waits) + `
` + holdTOML(review.Hold) + approved + `[states.blocked]
park = true
resumes = true
[states.done]
terminal = true

` + gates + `
[[transition]]
from = ["backlog"]
to = "in_progress"
[[transition]]
from = ["in_progress"]
to = "integration"
[[transition]]
from = ["integration"]
to = "in_review"
` + sequential(toReview) + `[[transition]]
from = ["in_review"]
to = "approved"
` + sequential(toApproved) + `[[transition]]
from = ["integration", "in_review", "approved"]
to = "in_progress"
[[transition]]
from = ["in_progress", "integration", "in_review"]
to = "blocked"
[[transition]]
from = ["blocked"]
to = "@prior"
[[transition]]
from = ["approved"]
to = "done"
`
}

func prReviewWorkflow(gate string) string { return prReviewWorkflowNamed("pr-review", gate) }

func prReviewWorkflowNamed(name, gate string) string {
	return `name = "pr-review"
[states.backlog]
start = true
[states.done]
terminal = true
[states.cancelled]
terminal = true

` + gate + `
[[transition]]
from = ["backlog"]
to = "done"
groups = [{ mode = "sequential", gates = [` + strconv.Quote(name) + `] }]
[[transition]]
from = ["*"]
to = "cancelled"
`
}

func (p *prSandbox) serveStatuses(fixture string) {
	p.t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "github", "statuses", fixture+".json"))
	if err != nil {
		p.t.Fatal(err)
	}
	writeFile(p.t, filepath.Join(p.fake, "statuses.json"), strings.ReplaceAll(string(b), "@HEAD@", p.head), 0o644)
}

func (p *prSandbox) statusesSince(n int) []string {
	p.t.Helper()
	var out []string
	for _, s := range p.srv.PublishedStatuses("acme")[n:] {
		if s.GetCommitSha() != p.head {
			p.t.Errorf("status on %s, not the head %s", s.GetCommitSha(), p.head)
		}
		if len([]rune(s.GetDescription())) > 140 {
			p.t.Errorf("description over 140 characters: %q", s.GetDescription())
		}
		out = append(out, s.GetContext()+" "+s.GetState())
	}
	return out
}

func (p *prSandbox) statusCount() int { return len(p.srv.PublishedStatuses("acme")) }

var pendingStatuses = []string{"satelle/pr-approval pending", "satelle/pr-ready pending", "satelle/pr-review pending"}

func lastRun(runs []gateRun, gate string) gateRun {
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].Gate == gate {
			return runs[i]
		}
	}
	return gateRun{}
}

func (p *prSandbox) item(n int, titled bool) string {
	p.t.Helper()
	id := p.create("story", "--title", "change", "--criteria", "it is reviewed").ID
	p.must("story", "set", id, "--status", "in_progress")
	p.must("story", "set", id, "--status", "integration")
	title := "change (" + id + ")"
	if !titled {
		title = "change for " + id
	}
	p.prs = append(p.prs, map[string]any{"number": n, "headRefOid": p.head, "title": title, "isDraft": false})
	b, _ := json.Marshal(p.prs)
	writeFile(p.t, filepath.Join(p.fake, "prs.json"), string(b), 0o644)
	return id
}

func (p *prSandbox) reviewing(n int, titled bool) string {
	p.t.Helper()
	id := p.item(n, titled)
	p.must("story", "set", id, "--status", "in_review")
	return id
}

func (p *prSandbox) reviewRun(story string, tagged bool) (string, result) {
	p.t.Helper()
	if p.task == "" {
		p.task = p.create("task", "--title", "Review a story's pull request head", "--input-kind", "story", "--session", reviewSession).ID
	}
	var exe struct {
		ID string `json:"id"`
	}
	p.mustJSON(&exe, "task", "run", p.task, "--session", reviewSession)
	tags := []string{"execution", "set", exe.ID, "--add-tag", "pr-review"}
	if tagged {
		tags = append(tags, "--add-tag", "reviews:"+story, "--add-tag", "head:"+p.head)
	}
	p.must(append(tags, "--session", reviewSession)...)
	p.must("execution", "set", exe.ID, "--reselect", "pr-review", "--session", reviewSession)
	if wf := p.runItem(exe.ID).Workflow; wf != "pr-review" {
		p.t.Fatalf("the review run %s follows %q after the reselect, want pr-review", exe.ID, wf)
	}
	it := p.get(story)
	args := []string{"execution", "set", exe.ID, "--title", "Review of " + story + " pull request, head " + p.head[:12], "--body", "-"}
	for _, c := range it.Criteria {
		args = append(args, "--criteria", c)
	}
	if r := p.exec(satelleBin, it.Body, append(args, "--replace", "--session", reviewSession)...); r.code != 0 {
		p.t.Fatalf("copying %s's record to %s: exit %d\n%s%s", story, exe.ID, r.code, r.out, r.err)
	}
	if got := p.runItem(exe.ID); !slices.Equal(got.Criteria, it.Criteria) {
		p.t.Fatalf("the review run's criteria %v, the story's %v", got.Criteria, it.Criteria)
	}
	p.must("execution", "seat", exe.ID, "--base", p.old, "--session", reviewSession)
	r := p.run("execution", "set", exe.ID, "--status", "done", "--session", reviewSession)
	p.must("execution", "release", exe.ID, "--session", reviewSession)
	return exe.ID, r
}

func (p *prSandbox) runItem(exe string) item.Item {
	p.t.Helper()
	var it item.Item
	p.mustJSON(&it, "execution", "get", exe)
	return it
}

func (p *prSandbox) runStatus(exe string) string { p.t.Helper(); return p.runItem(exe).Status }

func (p *prSandbox) cancelRun(exe string) {
	p.t.Helper()
	p.must("execution", "set", exe, "--status", "cancelled", "--session", reviewSession)
}

func (p *prSandbox) backToReview(id string) {
	p.t.Helper()
	p.must("story", "set", id, "--status", "integration")
	p.must("story", "set", id, "--status", "in_review")
}

func (p *prSandbox) kindsSince(id string, n int) []string {
	p.t.Helper()
	evs := p.ledger(id, "")
	if len(evs) < n {
		p.t.Fatalf("%s's chain lost events", id)
	}
	var kinds []string
	for _, e := range evs[n:] {
		if e.Item != id {
			p.t.Fatalf("event of %s on %s's chain", e.Item, id)
		}
		kinds = append(kinds, e.Kind)
	}
	return kinds
}

func (p *prSandbox) serveReviews(fixture string) {
	p.t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "github", "reviews", fixture+".json"))
	if err != nil {
		p.t.Fatal(err)
	}
	body := strings.NewReplacer("@HEAD@", p.head, "@OLD@", p.old).Replace(string(b))
	writeFile(p.t, filepath.Join(p.fake, "reviews.json"), body, 0o644)
}

func (p *prSandbox) serveReviewsAs(fixture string, reviewID int64) {
	p.t.Helper()
	p.serveReviews(fixture)
	path := filepath.Join(p.fake, "reviews.json")
	b, err := os.ReadFile(path)
	if err != nil {
		p.t.Fatal(err)
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	var rs []map[string]any
	if err := dec.Decode(&rs); err != nil || len(rs) == 0 {
		p.t.Fatalf("%s: %v", fixture, err)
	}
	rs[0]["id"] = json.Number(fmt.Sprint(reviewID))
	out, err := json.Marshal(rs)
	if err != nil {
		p.t.Fatal(err)
	}
	writeFile(p.t, path, string(out), 0o644)
}

func fixtureBody(t *testing.T, fixture string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "github", "reviews", fixture+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var rs []struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(b, &rs); err != nil || len(rs) == 0 {
		t.Fatalf("%s: %v", fixture, err)
	}
	return rs[len(rs)-1].Body
}

func (p *prSandbox) judge(verdict, notes string) {
	p.t.Helper()
	writeFile(p.t, filepath.Join(p.fake, "verdict"), verdict, 0o644)
	writeFile(p.t, filepath.Join(p.fake, "notes"), notes, 0o644)
}

func (p *prSandbox) present(id string) result {
	p.t.Helper()
	return p.run("story", "set", id, "--status", "approved")
}

func (p *prSandbox) status(id string) string { p.t.Helper(); return p.get(id).Status }

func (p *prSandbox) latestAttachment(id, name string) string {
	p.t.Helper()
	r := p.run("story", "attachment", id, name)
	if r.code != 0 {
		p.t.Fatalf("story attachment %s %s: exit %d\n%s%s", id, name, r.code, r.out, r.err)
	}
	return r.out
}

func (p *prSandbox) published() int { return len(p.srv.PublishedReviews("acme")) }

func (p *prSandbox) githubReviews(id string) []map[string]any {
	p.t.Helper()
	var out []map[string]any
	for _, e := range p.ledger(id, "github.review") {
		var m map[string]any
		if err := json.Unmarshal(e.Payload, &m); err != nil {
			p.t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func (p *prSandbox) transitions(id, from, to string) int {
	p.t.Helper()
	n := 0
	for _, e := range p.ledger(id, item.KindTransitioned) {
		var tr struct{ From, To string }
		if json.Unmarshal(e.Payload, &tr) == nil && tr.From == from && tr.To == to {
			n++
		}
	}
	return n
}

func TestPRReviewApprovalCheckAgainstRecordedReviews(t *testing.T) {
	p := newPRSandbox(t)
	id := p.reviewing(12, true)
	untitled := p.reviewing(13, false)

	check := shBlock0(t, ".satelle/skills/satelle-pr-approval-check.md")
	if m := regexp.MustCompile(`(?m)^app_account=(\d+)$`).FindStringSubmatch(check); m == nil || m[1] != appAccount {
		t.Fatalf("the approval check's app_account is %v, want %s", m, appAccount)
	}
	if !strings.Contains(check, "app=5379260\n") {
		t.Fatal("the approval check does not name app 5379260")
	}
	b, err := os.ReadFile(filepath.Join("testdata", "github", "reviews", "approved-other-account.json"))
	if err != nil {
		t.Fatal(err)
	}
	var other []struct {
		User struct {
			ID    int64  `json:"id"`
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := json.Unmarshal(b, &other); err != nil || len(other) == 0 {
		t.Fatalf("approved-other-account: %v", err)
	}
	if u := other[0].User; u.ID != orbelleReviewerAccount || u.Login != "orbelle-reviewer[bot]" {
		t.Fatalf("approved-other-account is by %s (%d), want orbelle-reviewer[bot] (%d)", u.Login, u.ID, orbelleReviewerAccount)
	}
	run := func(item string) result {
		t.Helper()
		p.env["SATELLE_ITEM"], p.env["SATELLE_FROM"], p.env["SATELLE_TO"] = item, "in_review", "approved"
		defer func() { delete(p.env, "SATELLE_ITEM"); delete(p.env, "SATELLE_FROM"); delete(p.env, "SATELLE_TO") }()
		return p.exec("bash", "", "-c", check)
	}
	for _, rec := range []struct {
		sha string
		rid int64
	}{{p.head, 5746161424}, {p.old, repointedReviewID}} {
		p.must("ledger", "append", id, "--kind", "github.review", "--payload",
			fmt.Sprintf(`{"event":"APPROVE","pull_number":12,"commit_sha":%q,"review_id":%d}`, rec.sha, rec.rid))
	}
	const noApproval = "no approving review by the satelle app"
	for _, c := range []struct {
		fixture string
		pass    bool
		reopen  bool
		out     string
	}{
		{"approved-on-head", true, false, ""},
		{"approved-then-commented", true, false, ""},
		{"approved-older-head", false, false, noApproval},
		{"approved-other-account", false, false, noApproval},
		{"approved-then-dismissed", false, false, noApproval},
		{"none", false, false, noApproval},
		{"approved-repointed", false, false, "GitHub re-pointed an older approval"},
		{"approved-then-changes-requested", false, true, ""},
		{"changes-requested", false, true, ""},
	} {
		p.serveReviews(c.fixture)
		r := run(id)
		if (r.code == 0) != c.pass {
			t.Errorf("%s: exit %d, want pass=%v\n%s%s", c.fixture, r.code, c.pass, r.out, r.err)
		}
		want := "in_review"
		if c.reopen {
			want = "in_progress"
			if !strings.Contains(r.out, "requested changes on head "+p.head[:12]) || !strings.Contains(r.out, "reopened at in_progress") {
				t.Errorf("%s: output %q", c.fixture, r.out)
			}
			if got := p.latestAttachment(id, "pr-review"); got != fixtureBody(t, c.fixture) {
				t.Errorf("%s: pr-review = %q", c.fixture, got)
			}
		}
		if got := p.status(id); got != want {
			t.Errorf("%s: status %s, want %s", c.fixture, got, want)
		}
		if c.pass && !strings.Contains(r.out, "approved by the satelle app (account "+appAccount+", app 5379260)") {
			t.Errorf("%s: output %q", c.fixture, r.out)
		}
		if c.out != "" && !strings.Contains(r.out, c.out) {
			t.Errorf("%s: output %q, want %q", c.fixture, r.out, c.out)
		}
		if c.fixture == "approved-repointed" && (!strings.Contains(r.out, p.head[:12]) || !strings.Contains(r.out, p.old[:12])) {
			t.Errorf("%s: output %q does not name both heads %s and %s", c.fixture, r.out, p.head[:12], p.old[:12])
		}
		if c.reopen {
			p.backToReview(id)
		}
	}
	if n := p.transitions(id, "in_review", "in_progress"); n != 2 {
		t.Errorf("reopened %d times, want 2", n)
	}

	unrecorded := p.reviewing(14, true)
	p.serveReviews("approved-on-head")
	if r := run(unrecorded); r.code == 0 || !strings.Contains(r.out, "is not on the item's ledger") {
		t.Errorf("unrecorded approval: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if got := p.status(unrecorded); got != "in_review" {
		t.Errorf("unrecorded item moved to %s", got)
	}

	p.serveReviews("changes-requested")
	os.Remove(filepath.Join(p.fake, "gh-api.log"))
	if r := run(untitled); r.code == 0 || !strings.Contains(r.out, "no open pull request") {
		t.Errorf("untitled PR: exit %d\n%s", r.code, r.out)
	}
	if _, err := os.Stat(filepath.Join(p.fake, "gh-api.log")); err == nil {
		t.Error("the reviews of a PR mapped to no item were read")
	}
	if got := p.status(untitled); got != "in_review" {
		t.Errorf("untitled item moved to %s", got)
	}
}

func TestPRReviewRepointedReviewIsRecorded(t *testing.T) {
	read := func(name string) []map[string]any {
		t.Helper()
		b, err := os.ReadFile(filepath.Join("testdata", "github", "reviews", name))
		if err != nil {
			t.Fatal(err)
		}
		dec := json.NewDecoder(strings.NewReader(string(b)))
		dec.UseNumber()
		var rs []map[string]any
		if err := dec.Decode(&rs); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return rs
	}
	var rec map[string]any
	for _, r := range read("repointed-recorded.json") {
		if r["id"] == json.Number(fmt.Sprint(repointedReviewID)) {
			rec = r
		}
	}
	if rec == nil {
		t.Fatalf("repointed-recorded.json has no review %d", repointedReviewID)
	}
	user, _ := rec["user"].(map[string]any)
	if rec["state"] != "APPROVED" || user["id"] != json.Number(appAccount) || rec["submitted_at"] != "2026-11-07T21:58:23Z" {
		t.Fatalf("review %d is %v by %v at %v", repointedReviewID, rec["state"], user["id"], rec["submitted_at"])
	}
	if rec["commit_id"] != repointedReported || rec["commit_id"] == repointedGiven {
		t.Fatalf("review %d reports commit_id %v, want the re-pointed %s (given on %s)", repointedReviewID, rec["commit_id"], repointedReported, repointedGiven)
	}

	cs := read("approved-repointed.json")
	if len(cs) != 1 {
		t.Fatalf("approved-repointed.json holds %d reviews, want 1", len(cs))
	}
	if cs[0]["commit_id"] != "@HEAD@" {
		t.Fatalf("approved-repointed.json commit_id %v, want @HEAD@", cs[0]["commit_id"])
	}
	if len(cs[0]) != len(rec) {
		t.Fatalf("approved-repointed.json has %d fields, the recorded review %d", len(cs[0]), len(rec))
	}
	for k, v := range rec {
		if k == "commit_id" {
			continue
		}
		if got, ok := cs[0][k]; !ok || !reflect.DeepEqual(got, v) {
			t.Errorf("approved-repointed.json %s = %v, recorded %v", k, got, v)
		}
	}
}

func TestPRReviewRunPublishesOnTheStory(t *testing.T) {
	p := newPRSandbox(t)
	short := p.head[:12]

	untitled := p.reviewing(20, false)
	p.judge("accept", "fine: AC1 holds, the publish check runs last in its after list")
	exe, r := p.reviewRun(untitled, true)
	if r.code == 0 || !strings.Contains(r.out+r.err, "no open pull request") {
		t.Fatalf("untitled: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if n := p.published(); n != 0 {
		t.Fatalf("published %d reviews for a PR mapped to no story", n)
	}
	if got, run := p.status(untitled), p.runStatus(exe); got != "in_review" || run != "backlog" {
		t.Fatalf("untitled: story at %s, run at %s", got, run)
	}
	p.cancelRun(exe)

	untagged := p.reviewing(18, true)
	exe, r = p.reviewRun(untagged, false)
	if r.code == 0 || !strings.Contains(r.out+r.err, "needs exactly one reviews:<story> tag and one head:<sha> tag") {
		t.Fatalf("untagged run: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if n := p.published(); n != 0 || p.runStatus(exe) != "backlog" || len(p.githubReviews(untagged)) != 0 {
		t.Fatalf("untagged run: %d published, run at %s, github.review %v", n, p.runStatus(exe), p.githubReviews(untagged))
	}
	p.cancelRun(exe)

	silent := p.reviewing(19, true)
	p.judge("accept", "")
	n0 := p.statusCount()
	exe, r = p.reviewRun(silent, true)
	if r.code == 0 || !strings.Contains(r.err, "the judge gave no reasons") {
		t.Fatalf("silent accept: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if n := p.published(); n != 0 {
		t.Fatalf("a refused accept was published (%d reviews)", n)
	}
	if got, run := p.status(silent), p.runStatus(exe); got != "in_review" || run != "backlog" {
		t.Fatalf("silent accept: story at %s, run at %s", got, run)
	}
	if runs := p.gateRuns(exe); len(runs) != 1 || runs[0].Gate != "pr-review" || runs[0].Result != "reject" ||
		!strings.Contains(runs[0].Notes, "after "+runPublishSkill+": not run: after satelle-verdict-notes-check refused the verdict") ||
		!strings.Contains(runs[0].Notes, "after "+statusSkill+": satelle/pr-review failure on "+short+", posted by the satelle app (account "+appAccount+")") ||
		!strings.Contains(runs[0].Answer, `"verdict":"accept","notes":""`) {
		t.Fatalf("silent accept gate runs %+v", runs)
	}
	if got := p.statusesSince(n0); !slices.Equal(got, []string{"satelle/pr-review failure"}) {
		t.Fatalf("silent accept statuses %v", got)
	}
	p.cancelRun(exe)

	approved := p.reviewing(21, true)
	if runs := p.gateRuns(approved); len(runs) != 1 || runs[0].Gate != "pr-pending" || runs[0].Result != "accept" {
		t.Fatalf("integration -> in_review gate runs %+v", runs)
	} else {
		for _, c := range []string{"satelle/pr-ready", "satelle/pr-review", "satelle/pr-approval"} {
			if !strings.Contains(runs[0].Notes, c+" pending on "+short) {
				t.Errorf("pr-pending notes lack %s: %q", c, runs[0].Notes)
			}
		}
	}
	p.judge("accept", "every criterion is met: AC1 to AC4 hold on the head of the pull request")
	n0 = p.statusCount()
	before := len(p.ledger(approved, ""))
	exe, r = p.reviewRun(approved, true)
	if r.code != 0 {
		t.Fatalf("accept: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if got := p.runStatus(exe); got != "done" {
		t.Fatalf("the review run is at %s, want done", got)
	}
	if got := p.status(approved); got != "in_review" {
		t.Fatalf("the review run moved the story to %s", got)
	}
	revs := p.srv.PublishedReviews("acme")
	if len(revs) != 1 {
		t.Fatalf("published %d reviews", len(revs))
	}
	rv := revs[0]
	if rv.GetEvent() != "APPROVE" || rv.GetCommitSha() != p.head || rv.GetPullNumber() != 21 {
		t.Fatalf("published %v", rv)
	}
	first, rest, _ := strings.Cut(rv.GetBody(), "\n")
	if want := "satelle · project acme · gate pr-review (" + p.reviewer + ") on " + approved + " head " + short + ", review run " + exe + ": approve"; first != want {
		t.Errorf("body's first line %q, want %q", first, want)
	}
	if !strings.Contains(rest, "every criterion is met") {
		t.Errorf("body lacks the judge's notes:\n%s", rv.GetBody())
	}
	gr := p.githubReviews(approved)
	if len(gr) != 1 || gr[0]["event"] != "APPROVE" || gr[0]["commit_sha"] != p.head || gr[0]["pull_number"] != float64(21) ||
		gr[0]["repository"] != "orbelle/acme" || gr[0]["review_id"] == nil || !strings.HasPrefix(gr[0]["html_url"].(string), "https://github.com/orbelle/acme/pull/21") ||
		len(gr[0]["body_sha256"].(string)) != 64 {
		t.Fatalf("github.review %v", gr)
	}
	if got := p.githubReviews(exe); len(got) != 0 {
		t.Fatalf("github.review on the run: %v", got)
	}
	if got := p.kindsSince(approved, before); !slices.Equal(got, []string{"github.review"}) {
		t.Fatalf("the review run appended %v to the story, want only github.review", got)
	}
	runs := p.gateRuns(exe)
	if len(runs) != 1 || runs[0].Gate != "pr-review" || runs[0].Result != "accept" ||
		!strings.Contains(runs[0].Notes, "after "+runPublishSkill+": published APPROVE on "+approved+" head "+short) ||
		!strings.Contains(runs[0].Notes, "after "+statusSkill+": satelle/pr-review success on "+short+", posted by the satelle app (account "+appAccount+")") {
		t.Fatalf("run gate runs %+v", runs)
	}
	if got := p.statusesSince(n0); !slices.Equal(got, []string{"satelle/pr-review success"}) {
		t.Fatalf("review run statuses %v", got)
	}
	if d := p.srv.PublishedStatuses("acme")[n0].GetDescription(); d != "accept (backlog->done): every criterion is met: AC1 to AC4 hold on the head of the pull request" {
		t.Errorf("pr-review description %q", d)
	}

	p.serveReviewsAs("approved-on-head", int64(gr[0]["review_id"].(float64)))
	n0 = p.statusCount()
	if r := p.present(approved); r.code != 0 {
		t.Fatalf("in_review -> approved after an approval: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if got := p.status(approved); got != "approved" {
		t.Fatalf("status %s, want approved", got)
	}
	if run := lastRun(p.gateRuns(approved), "pr-approval"); run.Result != "accept" ||
		!strings.Contains(run.Notes, "after "+statusSkill+": satelle/pr-approval success on "+short) {
		t.Fatalf("pr-approval %+v", run)
	}
	if got := p.statusesSince(n0); !slices.Equal(got, []string{"satelle/pr-approval success"}) {
		t.Fatalf("approval statuses %v", got)
	}
	if d := p.srv.PublishedStatuses("acme")[n0].GetDescription(); !strings.HasPrefix(d, "accept (in_review->approved): ") {
		t.Errorf("pr-approval description %q", d)
	}

	p.serveReviews("changes-requested")
	block := idBlock(t, ".satelle/skills/release.md", "### Reviews on the heads", approved)
	if strings.Contains(block, "satelle github") {
		t.Fatal("the release review read calls satelle.dev")
	}
	r = p.exec("bash", "", "-c", block)
	if r.code != 0 || !strings.Contains(r.out, approved+": sent back: the satelle app requested changes on head "+p.head) {
		t.Fatalf("release review read: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if got := p.status(approved); got != "in_progress" {
		t.Fatalf("release review read left %s at %s", approved, got)
	}
	if got := p.latestAttachment(approved, "pr-review"); got != fixtureBody(t, "changes-requested") {
		t.Fatalf("pr-review = %q", got)
	}
	if p.transitions(approved, "approved", "in_progress") != 1 {
		t.Fatal("no approved -> in_progress on the chain")
	}

	id := p.reviewing(22, true)
	created := len(p.ledger(id, ""))
	p.serveReviews("none")
	p.judge("reject", "AC2 is not met: a reject never reopens the item at in_progress")
	n0 = p.statusCount()
	exe, r = p.reviewRun(id, true)
	if r.code == 0 {
		t.Fatalf("reject: exit 0\n%s%s", r.out, r.err)
	}
	if got := p.runStatus(exe); got != "backlog" {
		t.Fatalf("a rejected review run is at %s, want backlog", got)
	}
	revs = p.srv.PublishedReviews("acme")
	if len(revs) != 2 || revs[1].GetEvent() != "REQUEST_CHANGES" || revs[1].GetCommitSha() != p.head || revs[1].GetPullNumber() != 22 {
		t.Fatalf("published %v", revs)
	}
	body := revs[1].GetBody()
	if first, _, _ := strings.Cut(body, "\n"); first != "satelle · project acme · gate pr-review ("+p.reviewer+") on "+id+" head "+short+", review run "+exe+": request changes" || !strings.Contains(body, "AC2 is not met") {
		t.Fatalf("body %q", body)
	}
	if got := p.status(id); got != "in_review" {
		t.Fatalf("the review run moved the story to %s", got)
	}
	if got := p.kindsSince(id, created); !slices.Equal(got, []string{"github.review"}) {
		t.Fatalf("the rejecting run appended %v to the story, want only github.review", got)
	}
	if r := p.run("story", "attachment", id, "pr-review"); r.code == 0 {
		t.Fatalf("the review run attached pr-review to the story:\n%s", r.out)
	}
	if gr := p.githubReviews(id); len(gr) != 1 || gr[0]["event"] != "REQUEST_CHANGES" || gr[0]["commit_sha"] != p.head {
		t.Fatalf("github.review %v", gr)
	}
	if run := lastRun(p.gateRuns(exe), "pr-review"); run.Result != "reject" || !strings.Contains(run.Notes, "after "+runPublishSkill+": published REQUEST_CHANGES on "+id+" head "+short) {
		t.Fatalf("reject gate.run %+v", run)
	}
	if got := p.statusesSince(n0); !slices.Equal(got, []string{"satelle/pr-review failure"}) {
		t.Fatalf("reject statuses %v", got)
	}
	if d := p.srv.PublishedStatuses("acme")[n0].GetDescription(); !strings.HasPrefix(d, "reject (backlog->done): AC2 is not met") {
		t.Errorf("reject description %q", d)
	}
	p.cancelRun(exe)

	p.serveReviews("changes-requested")
	r = p.present(id)
	if r.code == 0 || !strings.Contains(r.err, "reopened at in_progress") {
		t.Fatalf("in_review -> approved over requested changes: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if got := p.status(id); got != "in_progress" {
		t.Fatalf("status %s, want in_progress", got)
	}
	if got := p.latestAttachment(id, "pr-review"); got != fixtureBody(t, "changes-requested") {
		t.Fatalf("pr-review = %q", got)
	}
	if p.transitions(id, "in_review", "in_progress") != 1 {
		t.Fatal("no in_review -> in_progress on the chain")
	}
	p.must("ledger", "verify")

	if r := p.run("story", "perform", id); r.code != 0 {
		t.Fatalf("perform: exit %d\n%s%s", r.code, r.out, r.err)
	}
	prompt, err := os.ReadFile(filepath.Join(p.fake, "prompt.ex-1"))
	if err != nil {
		t.Fatal(err)
	}
	header, _, _ := strings.Cut(fixtureBody(t, "changes-requested"), "\n")
	if !strings.Contains(string(prompt), "Attachment pr-review") || !strings.Contains(string(prompt), header) {
		t.Fatalf("the coder's prompt lacks the review:\n%s", prompt)
	}

	p.backToReview(id)
	p.serveReviews("none")
	p.judge("accept", "fine now: AC2 holds, the item reopens with the review attached")
	exe, r = p.reviewRun(id, true)
	if r.code != 0 || p.runStatus(exe) != "done" {
		t.Fatalf("accept over requested changes: exit %d, run at %s\n%s%s", r.code, p.runStatus(exe), r.out, r.err)
	}
	if n := p.published(); n != 2 {
		t.Fatalf("an approval was published over requested changes on the same head (%d reviews)", n)
	}
	if gr := p.githubReviews(id); len(gr) != 1 || gr[0]["event"] != "REQUEST_CHANGES" {
		t.Fatalf("github.review %v", gr)
	}
	if run := lastRun(p.gateRuns(exe), "pr-review"); !strings.Contains(run.Notes, "not publishing an approval: changes were requested on head "+short+" of "+id) {
		t.Fatalf("accept over requested changes notes %q", run.Notes)
	}
	if got := p.status(id); got != "in_review" {
		t.Fatalf("status %s, want review", got)
	}
	p.serveReviews("changes-requested")
	if r := p.present(id); r.code == 0 || p.status(id) != "in_progress" || p.transitions(id, "in_review", "in_progress") != 2 {
		t.Fatalf("re-present over requested changes: exit %d, status %s\n%s%s", r.code, p.status(id), r.out, r.err)
	}

	p.backToReview(id)
	p.serveReviews("none")
	p.judge("reject", "AC3 is not met either: the review body is never attached as pr-review")
	exe, r = p.reviewRun(id, true)
	if r.code == 0 {
		t.Fatalf("new findings: exit 0\n%s%s", r.out, r.err)
	}
	revs = p.srv.PublishedReviews("acme")
	if len(revs) != 3 || revs[2].GetEvent() != "REQUEST_CHANGES" || !strings.Contains(revs[2].GetBody(), "AC3 is not met either") {
		t.Fatalf("published %v", revs)
	}
	if gr := p.githubReviews(id); len(gr) != 2 || gr[0]["event"] != "REQUEST_CHANGES" || gr[1]["event"] != "REQUEST_CHANGES" {
		t.Fatalf("github.review %v", gr)
	}
	if got := p.status(id); got != "in_review" {
		t.Fatalf("status %s", got)
	}
	p.cancelRun(exe)
	p.must("ledger", "verify")
}

func TestPRReviewWaitsAtReviewWithoutAReview(t *testing.T) {
	p := newPRSandbox(t)
	id := p.reviewing(50, true)
	p.serveReviews("none")
	const rounds = 5
	for i := range rounds {
		r := p.present(id)
		if r.code == 0 || !strings.Contains(r.err, "no approving review") || !strings.Contains(r.err, ".satelle/tasks/pr-review.md") {
			t.Fatalf("presentation %d: exit %d\n%s%s", i+1, r.code, r.out, r.err)
		}
		if got := p.status(id); got != "in_review" {
			t.Fatalf("presentation %d left the story at %s, want review", i+1, got)
		}
	}
	n := 0
	for _, run := range p.gateRuns(id) {
		if run.Gate == "pr-approval" {
			n++
			if run.Result != "reject" {
				t.Errorf("pr-approval %s", run.Result)
			}
		}
	}
	if n != rounds {
		t.Errorf("pr-approval ran %d times, want %d", n, rounds)
	}
	if b := p.transitions(id, "in_review", "blocked"); b != 0 {
		t.Fatalf("waiting for a review blocked the story %d times", b)
	}
	if n := p.published(); n != 0 {
		t.Fatalf("published %d reviews", n)
	}
}

func TestPRReviewGatesAreOnTheRoutes(t *testing.T) {
	files, err := filepath.Glob("../.satelle/workflows/*.toml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no workflows: %v", err)
	}
	toReview := []string{"pr-pending", "integration-check", "pr-ready"}
	routed, reviewed := 0, 0
	for _, f := range files {
		w, err := workflow.Parse(f)
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(f)
		route := name == "delivery.toml" || name == "chore.toml"
		if _, ok := w.Gates["pr-review"]; ok && name != "pr-review.toml" {
			t.Errorf("%s defines a pr-review gate: the review run's workflow owns it", name)
		}
		for _, tr := range w.Transitions {
			approval := route && slices.Equal(tr.From, []string{"in_review"}) && tr.To == "approved"
			review := name == "pr-review.toml" && slices.Equal(tr.From, []string{"backlog"}) && tr.To == "done"
			for _, g := range tr.Groups {
				if !approval && slices.Contains(g.Gates, "pr-approval") || !review && slices.Contains(g.Gates, "pr-review") {
					t.Errorf("%s: %v -> %s runs %v", name, tr.From, tr.To, g.Gates)
				}
			}
		}
		if name == "pr-review.toml" {
			reviewed++
			tr, err := w.Find("backlog", "done", "")
			if err != nil {
				t.Fatal(err)
			}
			if len(tr.Groups) != 1 || tr.Groups[0].Mode != workflow.Sequential || !slices.Equal(tr.Groups[0].Gates, []string{"pr-review"}) {
				t.Errorf("pr-review.toml: backlog -> done runs %+v, want one sequential group [pr-review]", tr.Groups)
			}
			if !w.States["backlog"].Start || !w.States["done"].Terminal {
				t.Errorf("pr-review.toml states %+v", w.States)
			}
		}
		if !route {
			continue
		}
		routed++
		st, ok := w.States["in_review"]
		if !ok || strings.TrimSpace(st.Waits) == "" || st.Hold != nil || st.Terminal || st.Park || st.Performer != nil {
			t.Errorf("%s: review is %+v, want a state that waits, with no hold and no performer", name, st)
		}
		if _, err := w.Find("integration", "approved", ""); err == nil {
			t.Errorf("%s: integration -> approved skips review", name)
		}
		tr, err := w.Find("integration", "in_review", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(tr.Groups) != 1 || tr.Groups[0].Mode != workflow.Sequential || !slices.Equal(tr.Groups[0].Gates, toReview) {
			t.Errorf("%s: integration -> in_review runs %+v, want one sequential group %v", name, tr.Groups, toReview)
		}
		tr, err = w.Find("in_review", "approved", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(tr.Groups) != 1 || tr.Groups[0].Mode != workflow.Sequential || !slices.Equal(tr.Groups[0].Gates, []string{"pr-approval"}) {
			t.Errorf("%s: in_review -> approved runs %+v, want one sequential group [pr-approval]", name, tr.Groups)
		}
		for _, from := range []string{"integration", "in_review", "approved"} {
			tr, err := w.Find(from, "in_progress", "")
			if err != nil {
				t.Errorf("%s: no rework edge from %s: %v", name, from, err)
			} else if len(tr.Groups) != 0 {
				t.Errorf("%s: the rework edge from %s runs %+v", name, from, tr.Groups)
			}
		}
		if _, err := w.Find("in_review", "blocked", ""); err != nil {
			t.Errorf("%s: review cannot be blocked: %v", name, err)
		}
	}
	if routed != 2 || reviewed != 1 {
		t.Fatalf("checked %d of delivery.toml and chore.toml, %d pr-review.toml", routed, reviewed)
	}
	if strings.Contains(shBlock0(t, ".satelle/skills/satelle-pr-ready-check.md"), `echo "approved:`) {
		t.Error("pr-ready still approves")
	}
	if _, err := os.Stat("../.satelle/skills/satelle-pr-review-publish.md"); !os.IsNotExist(err) {
		t.Errorf("the in-session publish skill satelle-pr-review-publish.md is still there: %v", err)
	}
	skills, _ := filepath.Glob("../.satelle/skills/*.md")
	for _, f := range skills {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if base := filepath.Base(f); strings.Contains(string(b), "satelle github") && base != runPublishSkill+".md" && base != statusSkill+".md" {
			t.Errorf("%s calls satelle github", base)
		}
	}
	review, _ := repoPRGates(t)
	if n := len(review.After); n < 2 || review.After[n-2] != runPublishSkill || review.After[n-1] != statusSkill {
		t.Errorf("pr-review's after is %v: want the run publish check immediately before the status entry, last", review.After)
	}
	block := shBlock0(t, ".satelle/skills/"+statusSkill+".md")
	var posting []string
	for _, f := range files {
		w, err := workflow.Parse(f)
		if err != nil {
			t.Fatal(err)
		}
		for name, g := range w.Gates {
			if slices.Contains(g.After, statusSkill) {
				posting = append(posting, name)
				if g.After[len(g.After)-1] != statusSkill {
					t.Errorf("%s: gate %s's after %v names the status entry but not last", filepath.Base(f), name, g.After)
				}
			}
		}
	}
	slices.Sort(posting)
	posting = slices.Compact(posting)
	if !slices.Equal(posting, []string{"pr-approval", "pr-ready", "pr-review"}) {
		t.Errorf("the gates whose after names the status skill are %v, want pr-approval, pr-ready, pr-review", posting)
	}
	base := repoQueueBase(t)
	for _, wf := range []string{"delivery", "chore"} {
		if q := repoWorkflow(t, wf).States["approved"].Enqueue; !slices.Equal(q, []string{"release"}) || base != "origin/main" {
			t.Errorf("%s.toml: approved enqueues into %v, whose base is %q; want one queue, release, with base origin/main", wf, q, base)
		}
	}
	for _, ln := range strings.Split(block, "\n") {
		if strings.Contains(ln, "gh ") && !strings.Contains(ln, "command -v gh") && !strings.Contains(ln, "gh api \"repos/{owner}/{repo}/commits/$head/statuses") && !strings.Contains(ln, "gh is not installed") {
			t.Errorf("the status skill calls gh other than to read the statuses: %s", ln)
		}
	}
	if strings.Contains(block, appAccount) {
		t.Error("the status skill holds its own copy of the app's account id")
	}
	err = filepath.WalkDir("..", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel("..", path)
		if d.IsDir() {
			if rel != "." && rel != "internal" && rel != "cmd" && !strings.HasPrefix(rel, "internal"+string(filepath.Separator)) && !strings.HasPrefix(rel, "cmd"+string(filepath.Separator)) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasPrefix(rel, "internal"+string(filepath.Separator)) && !strings.HasPrefix(rel, "cmd"+string(filepath.Separator)) {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") || !(strings.HasSuffix(path, ".go") || strings.HasSuffix(path, ".md") || strings.HasSuffix(path, ".toml")) {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, w := range []string{"satelle/pr-", statusSkill, "pr-pending", "satelle-pr-review-publish", runPublishSkill} {
			if strings.Contains(string(b), w) {
				t.Errorf("%s names %q: what is posted, and by which gate, is the repo's configuration", rel, w)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if block := idBlock(t, ".satelle/skills/release.md", "### Reviews on the heads", "sto_x"); strings.Contains(block, "satelle github") {
		t.Error("the release review read calls satelle github")
	}
}

func shBlock0(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", rel))
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(b), "```sh\n")
	if !ok {
		t.Fatalf("%s has no sh block", rel)
	}
	block, _, ok := strings.Cut(rest, "```")
	if !ok {
		t.Fatalf("%s: unclosed sh block", rel)
	}
	return bindSession(block)
}

func idBlock(t *testing.T, rel, heading, id string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", rel))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	i := strings.Index(doc, heading)
	if i < 0 {
		t.Fatalf("%s has no %q", rel, heading)
	}
	_, rest, ok := strings.Cut(doc[i:], "```sh\n")
	if !ok {
		t.Fatalf("%s has no sh block after %q", rel, heading)
	}
	block, _, ok := strings.Cut(rest, "```")
	if !ok {
		t.Fatalf("%s: unclosed sh block", rel)
	}
	re := regexp.MustCompile(`(?m)^id=\S+$`)
	if len(re.FindAllString(block, -1)) != 1 {
		t.Fatalf("%s: the block has no single id= line:\n%s", rel, block)
	}
	return bindSession(re.ReplaceAllString(block, "id="+id))
}

func TestGateStatusEveryResultThroughTheGates(t *testing.T) {
	p := newPRSandbox(t)
	p.write(".satelle/skills/stub-ready.md", "---\ntype: check\n---\n```sh\nmode=$(cat \"$FAKE_DIR/ready\" 2>/dev/null)\ncase \"$mode\" in\n  fail) echo not ready; exit 1 ;;\n  hang) sleep 30 ;;\nesac\necho ready\n```\n")
	p.write(".satelle/workflows/delivery.toml", prSandboxWorkflow(t, `[gates.pr-ready]
skill = "stub-ready"
timeout = "3s"
after = ["`+statusSkill+`"]
`, []string{"pr-ready"}, nil))
	p.write(".satelle/workflows/pr-review.toml", prReviewWorkflow(`[gates.pr-review]
skill = "satelle-code-ac-review"
agent = "`+p.reviewer+`"
after = ["`+statusSkill+`"]
`))
	p.git("add", "-A")
	p.git("commit", "-q", "-m", "stub gates")
	p.head = strings.TrimSpace(p.git("rev-parse", "HEAD"))
	p.serveStatuses("by-app")

	ready := filepath.Join(p.fake, "ready")
	dead := filepath.Join(p.fake, "dead")
	long := strings.Repeat("n", 400)
	for i, c := range []struct {
		name, ready, verdict, notes   string
		dead                          bool
		gate, result, state, notesHas string
	}{
		{"ready passes", "", "accept", "", false, "pr-ready", "accept", "success", "ready"},
		{"ready fails", "fail", "", "", false, "pr-ready", "reject", "failure", "not ready"},
		{"ready times out", "hang", "", "", false, "pr-ready", "reject", "failure", "timed out"},
		{"review accepts", "", "accept", "every criterion is met: the stub is ready and the judge accepts", false, "pr-review", "accept", "success", "every criterion is met"},
		{"judge dies", "", "accept", "", true, "pr-review", "error", "error", ""},
		{"no verdict", "", "maybe", "the answer holds no verdict the gate can read at all", false, "pr-review", "reject", "failure", ""},
		{"long note", "", "accept", long, false, "pr-review", "accept", "success", ""},
	} {
		writeFile(t, ready, c.ready, 0o644)
		os.Remove(dead)
		if c.dead {
			writeFile(t, dead, "", 0o644)
		}
		p.judge(c.verdict, c.notes)
		id := p.item(40+i, true)
		var run gateRun
		var edge string
		n0 := p.statusCount()
		if c.gate == "pr-ready" {
			edge = "integration->in_review"
			p.run("story", "set", id, "--status", "in_review")
			run = lastRun(p.gateRuns(id), c.gate)
			want := "in_review"
			if c.result != "accept" {
				want = "integration"
			}
			if got := p.status(id); got != want {
				t.Errorf("%s: story at %s, want %s", c.name, got, want)
			}
		} else {
			edge = "backlog->done"
			p.must("story", "set", id, "--status", "in_review")
			n0 = p.statusCount()
			exe, _ := p.reviewRun(id, true)
			run = lastRun(p.gateRuns(exe), c.gate)
			want := "done"
			if c.result != "accept" {
				want = "backlog"
			}
			if got := p.runStatus(exe); got != want {
				t.Errorf("%s: review run at %s, want %s", c.name, got, want)
			}
			if got := p.status(id); got != "in_review" {
				t.Errorf("%s: the review run moved the story to %s", c.name, got)
			}
		}
		if run.Result != c.result || !strings.Contains(run.Notes, c.notesHas) {
			t.Errorf("%s: gate.run %s %q, want %s with %q", c.name, run.Result, run.Notes, c.result, c.notesHas)
		}
		got := p.srv.PublishedStatuses("acme")[n0:]
		var last string
		for _, s := range got {
			if s.GetContext() == "satelle/"+c.gate {
				last = s.GetState()
			}
		}
		if last != c.state {
			t.Errorf("%s: satelle/%s %q, want %q (%v)", c.name, c.gate, last, c.state, got)
		}
		if !strings.Contains(run.Notes, "after "+statusSkill+": satelle/"+c.gate+" "+c.state+" on "+p.head[:12]) {
			t.Errorf("%s: notes lack the status line: %q", c.name, run.Notes)
		}
		if len(got) > 0 && !strings.HasPrefix(got[len(got)-1].GetDescription(), c.result+" ("+edge+"): ") {
			t.Errorf("%s: description %q, want it to name %s on %s", c.name, got[len(got)-1].GetDescription(), c.result, edge)
		}
		if c.name == "long note" {
			d := got[len(got)-1].GetDescription()
			if want := ("accept (" + edge + "): " + long)[:140]; d != want {
				t.Errorf("long note: description %q (%d), want the first 140 characters", d, len(d))
			}
		}
	}
	os.Remove(dead)
	writeFile(t, ready, "", 0o644)

	block := shBlock0(t, ".satelle/skills/"+statusSkill+".md")
	p.env["SATELLE_ITEM"], p.env["SATELLE_FROM"], p.env["SATELLE_TO"] = "sto_x", "in_review", "approved"
	defer func() {
		for _, k := range []string{"SATELLE_ITEM", "SATELLE_FROM", "SATELLE_TO", "SATELLE_GATE"} {
			delete(p.env, k)
		}
	}()
	for _, c := range []struct{ gate, result, state string }{
		{"pr-review", "carried", "success"}, {"pr-ready", "skipped", "success"}, {"pr-approval", "error", "error"},
	} {
		p.env["SATELLE_GATE"] = c.gate
		n0 := p.statusCount()
		in, _ := json.Marshal(map[string]string{"result": c.result, "notes": "first\nsecond"})
		r := p.exec("bash", string(in), "-c", block)
		got := p.statusesSince(n0)
		if r.code != 0 || !slices.Equal(got, []string{"satelle/" + c.gate + " " + c.state}) {
			t.Errorf("%s: exit %d %v\n%s%s", c.result, r.code, got, r.out, r.err)
		}
	}
	delete(p.env, "SATELLE_GATE")
	n0 := p.statusCount()
	if r := p.exec("bash", "", "-c", block); r.code != 0 || !slices.Equal(p.statusesSince(n0), []string{"satelle/pr-ready pending", "satelle/pr-review pending"}) {
		t.Errorf("pending mode: exit %d, statuses %v, want the gates whose after names the skill\n%s%s", r.code, p.statusesSince(n0), r.out, r.err)
	}
	p.env["SATELLE_GATE"] = "pr-review"
	p.serveStatuses("by-other")
	r := p.exec("bash", `{"result":"accept","notes":"fine"}`, "-c", block)
	if r.code != 0 || !strings.Contains(r.out, "but GitHub shows it from account 350948835, not the satelle app's account "+appAccount) {
		t.Errorf("another account's status: exit %d\n%s%s", r.code, r.out, r.err)
	}
	r = p.exec("bash", `{"result":"accept"}`, "-c", "PATH=/nonexistent\n"+block)
	if r.code != 0 || !strings.Contains(r.out, "jq is not installed, so no status is posted") {
		t.Errorf("without jq: exit %d\n%s%s", r.code, r.out, r.err)
	}
}

func TestGateStatusRefusedKeepsTheVerdict(t *testing.T) {
	p := newPRSandbox(t)
	const refusal = "the app's installation lacks the statuses permission"
	p.srv.RefuseStatuses("acme", refusal)
	short := p.head[:12]
	notPublished := func(gate, notes string) {
		t.Helper()
		if want := "after " + statusSkill + ": satelle/" + gate + " not published on " + short + ": "; !strings.Contains(notes, want) || !strings.Contains(notes, refusal) {
			t.Errorf("%s notes lack %q and the refusal: %q", gate, want, notes)
		}
	}

	id := p.item(31, true)
	if r := p.run("story", "set", id, "--status", "in_review"); r.code != 0 {
		t.Fatalf("integration -> in_review under a refusal: exit %d\n%s%s", r.code, r.out, r.err)
	}
	runs := p.gateRuns(id)
	if len(runs) != 1 || runs[0].Gate != "pr-pending" || runs[0].Result != "accept" {
		t.Fatalf("integration -> in_review gate runs %+v", runs)
	}
	for _, c := range []string{"satelle/pr-ready", "satelle/pr-review", "satelle/pr-approval"} {
		if !strings.Contains(runs[0].Notes, c+" not published on "+short+": ") || !strings.Contains(runs[0].Notes, refusal) {
			t.Errorf("pr-pending notes for %s: %q", c, runs[0].Notes)
		}
	}

	p.judge("accept", "every criterion is met: AC1 to AC4 hold on the head of the pull request")
	exe, r := p.reviewRun(id, true)
	if r.code != 0 || p.runStatus(exe) != "done" {
		t.Fatalf("review run accept under a refusal: exit %d, run at %s\n%s%s", r.code, p.runStatus(exe), r.out, r.err)
	}
	run := lastRun(p.gateRuns(exe), "pr-review")
	if run.Result != "accept" {
		t.Fatalf("pr-review %+v", run)
	}
	notPublished("pr-review", run.Notes)
	gr := p.githubReviews(id)
	if len(gr) != 1 || gr[0]["event"] != "APPROVE" {
		t.Fatalf("github.review %v", gr)
	}

	p.serveReviewsAs("approved-on-head", int64(gr[0]["review_id"].(float64)))
	if r := p.present(id); r.code != 0 {
		t.Fatalf("in_review -> approved under a refusal: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if got := p.status(id); got != "approved" {
		t.Fatalf("status %s", got)
	}
	run = lastRun(p.gateRuns(id), "pr-approval")
	if run.Result != "accept" {
		t.Fatalf("pr-approval %+v", run)
	}
	notPublished("pr-approval", run.Notes)
	if n := p.statusCount(); n != 0 {
		t.Fatalf("published %d statuses under a refusal", n)
	}

	rej := p.reviewing(32, true)
	p.serveReviews("none")
	p.judge("reject", "AC2 is not met: the statuses are never posted on the head")
	exe, r = p.reviewRun(rej, true)
	if r.code == 0 || p.runStatus(exe) != "backlog" {
		t.Fatalf("review run reject under a refusal: exit %d, run at %s\n%s%s", r.code, p.runStatus(exe), r.out, r.err)
	}
	run = lastRun(p.gateRuns(exe), "pr-review")
	if run.Result != "reject" {
		t.Fatalf("reject gate.run %+v", run)
	}
	notPublished("pr-review", run.Notes)
	if got := p.status(rej); got != "in_review" {
		t.Fatalf("status %s", got)
	}
	p.cancelRun(exe)
	p.serveReviews("changes-requested")
	if r := p.present(rej); r.code == 0 || !strings.Contains(r.err, "reopened at in_progress") {
		t.Fatalf("in_review -> approved over requested changes under a refusal: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if got := p.status(rej); got != "in_progress" {
		t.Fatalf("status %s", got)
	}
	if n := p.statusCount(); n != 0 {
		t.Fatalf("published %d statuses under a refusal", n)
	}

	p.srv.RefuseStatuses("acme", "")
	dry := p.item(33, true)
	r = p.run("gate", "run", "delivery/pr-pending", "--item", dry)
	if !strings.Contains(r.out+r.err, "a dry run publishes no status") || p.statusCount() != 0 {
		t.Fatalf("dry run: exit %d, %d statuses\n%s%s", r.code, p.statusCount(), r.out, r.err)
	}
}

var prGateSkills = []string{statusSkill, "satelle-pr-ready-check", "satelle-pr-approval-check", runPublishSkill}

func baseBranchLines(t *testing.T, text string) string {
	t.Helper()
	_, rest, ok := strings.Cut(text, "# base branch: begin\n")
	if !ok {
		return ""
	}
	block, _, ok := strings.Cut(rest, "# base branch: end\n")
	if !ok {
		t.Fatal("a base branch block has no end line")
	}
	return block
}

func TestPRGateSkillsHoldNoRepoValue(t *testing.T) {
	strip := strings.NewReplacer("--name pr-review", "", `.name == "pr-review"`, "", "`pr-review`", "")
	forbidden := map[string]*regexp.Regexp{
		"the word main":               regexp.MustCompile(`\bmain\b`),
		"a gates= assignment":         regexp.MustCompile(`(?m)^\s*gates=`),
		"a satelle/pr- context":       regexp.MustCompile(`satelle/pr-`),
		"a gate name":                 regexp.MustCompile(`(^|[^A-Za-z0-9_-])pr-(ready|approval|pending|review)($|[^A-Za-z0-9_-])`),
		"a path under .satelle/tasks": regexp.MustCompile(`\.satelle/tasks/`),
		"a story id":                  regexp.MustCompile(`sto_[0-9a-f]{8}`),
	}
	var blocks []string
	for _, name := range prGateSkills {
		b, err := os.ReadFile("../.satelle/skills/" + name + ".md")
		if err != nil {
			t.Fatal(err)
		}
		text := string(b)
		if blk := baseBranchLines(t, text); blk != "" {
			blocks = append(blocks, blk)
		}
		for _, own := range prGateSkills {
			text = strings.ReplaceAll(text, own, "")
		}
		text = strip.Replace(text)
		for what, re := range forbidden {
			for _, m := range re.FindAllString(text, -1) {
				t.Errorf("%s.md holds %s: %q", name, what, m)
			}
		}
	}
	if len(blocks) != 3 {
		t.Fatalf("%d skills carry the base branch lines, want 3 (pr-ready, pr-approval, run-publish)", len(blocks))
	}
	for i, b := range blocks[1:] {
		if b != blocks[0] {
			t.Errorf("the base branch lines differ between skills (block %d):\n%s\nvs\n%s", i+1, b, blocks[0])
		}
	}
}

func TestPRGateSkillsTakeTheRepoValues(t *testing.T) {
	n := prNames{pending: "pending-x", ready: "ready-x", approval: "approve-x", review: "review-x", queue: "landing", base: "origin/trunk",
		waits: "the review run of another session", readyOnRoute: true}
	p := newPRSandboxNamed(t, n)
	origin := realDir(t, t.TempDir())
	p.exec("git", "", "init", "-q", "--bare", origin)
	p.git("remote", "add", "origin", origin)
	script, err := os.ReadFile("../scripts/changelog-items.sh")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(p.dir, "scripts", "changelog-items.sh"), string(script), 0o755)
	p.git("add", "-A")
	p.git("commit", "-q", "-m", "changelog script")
	p.head = strings.TrimSpace(p.git("rev-parse", "HEAD"))
	p.git("push", "-q", "origin", p.old+":refs/heads/trunk")
	p.git("push", "-q", "origin", "HEAD:refs/heads/change")
	p.git("fetch", "-q", "origin")
	p.serveReviews("none")
	p.serveStatuses("by-app")
	served, err := os.ReadFile(filepath.Join(p.fake, "statuses.json"))
	if err != nil {
		t.Fatal(err)
	}
	renamed := strings.NewReplacer("satelle/pr-approval", "satelle/"+n.approval, "satelle/pr-review", "satelle/"+n.review, "satelle/pr-ready", "satelle/"+n.ready)
	writeFile(t, filepath.Join(p.fake, "statuses.json"), renamed.Replace(string(served)), 0o644)
	short := p.head[:12]

	id := p.item(61, true)
	if r := p.exec(satelleBin, "### Changed\n- The gates read the base branch\n", "story", "attach", id, "--name", "changelog", "--type", "text/markdown", "--file", "-"); r.code != 0 {
		t.Fatalf("attach changelog: %s%s", r.out, r.err)
	}
	if r := p.run("story", "set", id, "--status", "in_review"); r.code != 0 || p.status(id) != "in_review" {
		t.Fatalf("integration -> in_review: exit %d, at %s\n%s%s", r.code, p.status(id), r.out, r.err)
	}
	ready := lastRun(p.gateRuns(id), n.ready)
	if ready.Result != "accept" || !strings.Contains(ready.Notes, "open against trunk, contains origin/trunk "+p.old[:12]) ||
		!strings.Contains(ready.Notes, id+" waits at in_review for "+n.waits) {
		t.Fatalf("%s %+v", n.ready, ready)
	}
	p.judge("accept", "every criterion is met: AC1 to AC4 hold on the head of the pull request")
	exe, r := p.reviewRun(id, true)
	if r.code != 0 || p.runStatus(exe) != "done" {
		t.Fatalf("review run: exit %d, at %s\n%s%s", r.code, p.runStatus(exe), r.out, r.err)
	}
	gr := p.githubReviews(id)
	if len(gr) != 1 || gr[0]["event"] != "APPROVE" {
		t.Fatalf("github.review %v", gr)
	}
	p.serveReviewsAs("approved-on-head", int64(gr[0]["review_id"].(float64)))
	if r := p.present(id); r.code != 0 || p.status(id) != "approved" {
		t.Fatalf("in_review -> approved: exit %d\n%s%s", r.code, r.out, r.err)
	}

	want := []string{"satelle/approve-x pending", "satelle/ready-x pending", "satelle/review-x pending",
		"satelle/ready-x success", "satelle/review-x success", "satelle/approve-x success"}
	if got := p.statusesSince(0); !slices.Equal(got, want) {
		t.Errorf("statuses %v, want %v", got, want)
	}
	log, err := os.ReadFile(filepath.Join(p.fake, "gh.log"))
	if err != nil {
		t.Fatal(err)
	}
	lists := 0
	for _, ln := range strings.Split(strings.TrimSpace(string(log)), "\n") {
		if strings.HasPrefix(ln, "pr list") {
			lists++
			if !strings.Contains(ln, "--base trunk ") {
				t.Errorf("gh %s: not --base trunk", ln)
			}
		}
	}
	if lists != 3 {
		t.Errorf("gh pr list ran %d times, want 3 (ready, publish, approval)\n%s", lists, log)
	}
	repoValue := regexp.MustCompile(`\bmain\b|\bpr-(ready|review|approval)\b`)
	var seen []string
	seen = append(seen, string(log))
	for _, s := range p.srv.PublishedStatuses("acme") {
		seen = append(seen, s.GetContext()+" "+s.GetDescription())
	}
	for _, run := range append(p.gateRuns(id), p.gateRuns(exe)...) {
		seen = append(seen, run.Gate+" "+run.Notes)
	}
	for _, s := range seen {
		for _, own := range prGateSkills {
			s = strings.ReplaceAll(s, own, "")
		}
		if m := repoValue.FindString(s); m != "" {
			t.Errorf("%q holds this repo's value %q", s, m)
		}
	}
	if run := lastRun(p.gateRuns(id), n.approval); !strings.Contains(run.Notes, "satelle/approve-x success on "+short+", posted by the satelle app (account "+appAccount+")") {
		t.Errorf("%s %+v", n.approval, run)
	}

	runSkill := func(skill, from, to string) result {
		t.Helper()
		p.env["SATELLE_ITEM"], p.env["SATELLE_FROM"], p.env["SATELLE_TO"] = id, from, to
		defer func() { delete(p.env, "SATELLE_ITEM"); delete(p.env, "SATELLE_FROM"); delete(p.env, "SATELLE_TO") }()
		return p.exec("bash", "", "-c", shBlock0(t, ".satelle/skills/"+skill+".md"))
	}
	commit := func(files map[string]string) {
		t.Helper()
		for f, body := range files {
			p.write(f, body)
		}
		p.git("add", "-A")
		p.git("commit", "-q", "--allow-empty", "-m", "reconfigure")
	}
	gates := func(after bool) string {
		a := ""
		if after {
			a = "after = [\"" + statusSkill + "\"]\n"
		}
		return "[gates.pending-x]\nskill = \"" + statusSkill + "\"\n[gates.approve-x]\nskill = \"satelle-pr-approval-check\"\n" + a
	}
	for _, c := range []struct {
		name    string
		enqueue []string
		queues  map[string]string
		has     []string
	}{
		{"no queue", nil, nil, []string{"the workflow delivery of " + id + " enqueues into no queue with a trigger base", "[trigger]"}},
		{"two bases", []string{"landing", "other"}, map[string]string{"other": "origin/elsewhere"}, []string{"name 2 trigger bases", "landing base origin/trunk", "other base origin/elsewhere"}},
		{"no remote", []string{"landing"}, map[string]string{"landing": "trunk"}, []string{"the queue landing has trigger base trunk, which is not <remote>/<branch>"}},
	} {
		files := map[string]string{".satelle/workflows/delivery.toml": prSandboxWorkflowWith(t, gates(true), []string{"pending-x"}, []string{"approve-x"}, c.enqueue, n.waits)}
		for q, base := range c.queues {
			files[".satelle/queues/"+q+".toml"] = queueTOML(base, "pr-review")
		}
		commit(files)
		for _, skill := range []string{"satelle-pr-ready-check", "satelle-pr-approval-check"} {
			r := runSkill(skill, "in_review", "approved")
			for _, h := range c.has {
				if r.code != 1 || !strings.Contains(r.out, h) {
					t.Errorf("%s, %s: exit %d, want 1 naming %q\n%s%s", c.name, skill, r.code, h, r.out, r.err)
				}
			}
		}
		if c.queues != nil {
			commit(map[string]string{".satelle/queues/landing.toml": queueTOML("origin/trunk", "pr-review")})
		}
	}

	commit(map[string]string{
		".satelle/workflows/delivery.toml":  prSandboxWorkflowWith(t, gates(false), []string{"pending-x"}, []string{"approve-x"}, []string{"landing"}, n.waits),
		".satelle/workflows/pr-review.toml": prReviewWorkflowNamed("review-x", "[gates.review-x]\nskill = \"satelle-code-ac-review\"\nagent = \""+p.reviewer+"\"\n"),
	})
	n0 := p.statusCount()
	r = runSkill(statusSkill, "integration", "in_review")
	if r.code != 0 || p.statusCount() != n0 || strings.TrimSpace(r.out) != "no gate's after names "+statusSkill+", so no pending status is posted" {
		t.Errorf("pending mode with no gate naming the skill: exit %d, %d statuses\n%s%s", r.code, p.statusCount()-n0, r.out, r.err)
	}
}
