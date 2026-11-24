//go:build integration && unix

package tests

import (
	"encoding/json"
	"fmt"
	"maps"
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
	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/gittest"
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

const approveFromReviewSkill = "satelle-pr-approve-from-review"

var prGateNames = []string{"pr-pending", "pr-ready", "pr-review", "pr-approval"}

func repoWorkflow(t *testing.T, name string) *workflow.Workflow {
	t.Helper()
	w, err := workflow.Parse("../.satelle/workflows/" + name + ".toml")
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func repoPRGateSet(t *testing.T) map[string]workflow.Gate {
	t.Helper()
	names := append([]string{"code-ac"}, prGateNames...)
	var got [2][]workflow.Gate
	for i, wf := range []string{"delivery", "chore"} {
		w := repoWorkflow(t, wf)
		for _, name := range names {
			g, ok := w.Gates[name]
			if !ok {
				t.Fatalf("%s.toml defines no gate %s", wf, name)
			}
			got[i] = append(got[i], g)
		}
	}
	out := map[string]workflow.Gate{}
	for j, name := range names {
		out[name] = got[0][j]
		a, b := got[0][j], got[1][j]
		if a.Skill != b.Skill || a.Agent != b.Agent || !slices.Equal(a.After, b.After) || a.Carry != b.Carry || a.Trigger != b.Trigger ||
			!slices.Equal(a.Panel, b.Panel) || !slices.Equal(a.Paths, b.Paths) || a.Combine != b.Combine {
			t.Fatalf("delivery and chore define the gate %s differently: %+v vs %+v", name, a, b)
		}
	}
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
}

type prNames struct {
	pending, ready, approve, approval           string
	queue, base, waits                          string
	codeACOnRoute, readyOnRoute, approveOnRoute bool
	route                                       func(gates string) string
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
	return prNames{pending: "pr-pending", ready: "pr-ready", approve: "pr-review", approval: "pr-approval", queue: "release", base: repoQueueBase(t), waits: review.Waits}
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
	codeAC, approve, approval, pending, ready := gs["code-ac"], gs["pr-review"], gs["pr-approval"], gs["pr-pending"], gs["pr-ready"]
	if codeAC.Skill != "satelle-code-ac-review" || codeAC.Agent != "" || len(codeAC.Panel) == 0 || codeAC.Combine != "all" || !codeAC.Carry ||
		!slices.Equal(codeAC.After, []string{"satelle-verdict-notes-check", "satelle-verdict-reference-check"}) {
		t.Fatalf("code-ac is %+v", codeAC)
	}
	reviewer := codeAC.Panel[0]
	codeAC.Panel = []string{reviewer}
	if approve.Skill != approveFromReviewSkill || approve.Agent != "" || !slices.Equal(approve.After, []string{statusSkill}) {
		t.Fatalf("pr-review is %+v", approve)
	}
	if approval.Skill != "satelle-pr-approval-check" || approval.Agent != "" || !slices.Equal(approval.After, []string{statusSkill}) {
		t.Fatalf("pr-approval is %+v", approval)
	}
	if pending.Skill != statusSkill || pending.Agent != "" || len(pending.After) != 0 {
		t.Fatalf("pr-pending is %+v", pending)
	}
	if ready.Skill != "satelle-pr-ready-check" || ready.Agent != "" || !slices.Equal(ready.After, []string{statusSkill}) {
		t.Fatalf("pr-ready is %+v", ready)
	}
	skills, err := workflow.LoadSkills("../.satelle/skills")
	if err != nil || len(skills) == 0 {
		t.Fatalf("no repo skills: %v", err)
	}
	for _, sk := range skills {
		copyRepoSkill(t, s, sk)
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
	var toIntegration []string
	if n.codeACOnRoute {
		toIntegration = []string{"code-ac"}
	}
	toReview := []string{n.pending}
	if n.readyOnRoute {
		toReview = append(toReview, n.ready)
	}
	if n.approveOnRoute {
		toReview = append(toReview, n.approve)
	}
	gates := gateTOML(t, "code-ac", codeAC) + gateTOML(t, n.pending, pending) + gateTOML(t, n.ready, ready) + gateTOML(t, n.approve, approve) + gateTOML(t, n.approval, approval)
	var enqueue []string
	if n.queue != "" {
		enqueue = []string{n.queue}
	}
	if n.route != nil {
		s.write(".satelle/workflows/delivery.toml", n.route(gates+gateTOML(t, "reference-review", codeAC)))
	} else {
		s.write(".satelle/workflows/delivery.toml", prSandboxWorkflowWith(t, gates, toIntegration, toReview, []string{n.approval}, enqueue, n.waits))
	}
	if n.queue != "" {
		s.write(".satelle/queues/"+n.queue+".toml", queueTOML(n.base, "delivery"))
	}
	fake := realDir(t, t.TempDir())
	writeFile(t, filepath.Join(fake, "claude"), `#!/bin/sh
printf 'call\n' >> "$FAKE_DIR/claude.calls"
model=none
prev=
for a in "$@"; do [ "$prev" = "--model" ] && model=$a; prev=$a; done
cat > "$FAKE_DIR/prompt.$model"
v=accept
[ -f "$FAKE_DIR/verdict" ] && v=$(cat "$FAKE_DIR/verdict")
n="every criterion is met: AC1 holds on the head of the pull request for $v by $model"
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
	s.env["SATELLE_SESSION"] = "pr-author"
	p := &prSandbox{sandbox: s, srv: srv, fake: fake, reviewer: reviewer}
	p.old = strings.TrimSpace(s.git("rev-parse", "HEAD"))
	s.write("src/change.go", "package change\n")
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
	for _, r := range def.Workflows.Rule {
		if r.Workflow == "pr-review" {
			t.Fatalf("satelle.toml still has a rule selecting pr-review: %+v", r)
		}
	}
	return "[workflows]\ndefault = \"delivery\"\n"
}

func sequential(gates []string) string {
	if len(gates) == 0 {
		return ""
	}
	return `groups = [{ mode = "sequential", gates = [` + tomlGates(gates) + `] }]
`
}

func prSandboxWorkflow(t *testing.T, gates string, toIntegration, toReview, toApproved []string) string {
	t.Helper()
	_, review := repoRouteStates(t)
	return prSandboxWorkflowWith(t, gates, toIntegration, toReview, toApproved, []string{"release"}, review.Waits)
}

func prSandboxWorkflowWith(t *testing.T, gates string, toIntegration, toReview, toApproved, enqueue []string, waits string) string {
	t.Helper()
	integration, review := repoRouteStates(t)
	approved := "[states.approved]\n"
	if len(enqueue) > 0 {
		approved += "enqueue = [" + tomlGates(enqueue) + "]\n"
	}
	waitsLine := ""
	if waits != "" {
		waitsLine = "waits = " + strconv.Quote(waits) + "\n"
	}
	return `name = "delivery"
[states.backlog]
start = true
[states.in_progress]
performer = { agent = "executor", skill = "coder" }
[states.integration]
` + holdTOML(integration.Hold) + `[states.in_review]
` + waitsLine + holdTOML(review.Hold) + approved + `[states.blocked]
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
` + sequential(toIntegration) + `[[transition]]
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

func (p *prSandbox) claudeCalls() int {
	p.t.Helper()
	b, err := os.ReadFile(filepath.Join(p.fake, "claude.calls"))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		p.t.Fatal(err)
	}
	n := 0
	for _, ln := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if ln != "" {
			n++
		}
	}
	return n
}

func lastRun(runs []gateRun, gate string) gateRun {
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].Gate == gate {
			return runs[i]
		}
	}
	return gateRun{}
}

func (p *prSandbox) setPR(n int, head, title string) {
	p.t.Helper()
	entry := map[string]any{"number": n, "headRefOid": head, "title": title, "isDraft": false}
	replaced := false
	for i, pr := range p.prs {
		if pr["number"] == n {
			p.prs[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		p.prs = append(p.prs, entry)
	}
	b, _ := json.Marshal(p.prs)
	writeFile(p.t, filepath.Join(p.fake, "prs.json"), string(b), 0o644)
}

func (p *prSandbox) item(n int, titled bool) string {
	p.t.Helper()
	id := p.create("story", "--title", "change", "--criteria", "it is reviewed").ID
	p.must("story", "seat", id, "--base", p.old)
	p.must("story", "set", id, "--status", "in_progress")
	p.must("story", "set", id, "--status", "integration")
	title := "change (" + id + ")"
	if !titled {
		title = "change for " + id
	}
	p.setPR(n, p.head, title)
	return id
}

func (p *prSandbox) reviewing(n int, titled bool) string {
	p.t.Helper()
	id := p.item(n, titled)
	p.must("story", "set", id, "--status", "in_review")
	return id
}

func (p *prSandbox) diffJudgedHash(id string) string {
	p.t.Helper()
	var rep struct {
		Judged []struct {
			Gate   string `json:"gate"`
			Result string `json:"result"`
			Judged string `json:"judged"`
		} `json:"judged"`
	}
	p.mustJSON(&rep, "story", "diff", id)
	for i := len(rep.Judged) - 1; i >= 0; i-- {
		j := rep.Judged[i]
		if j.Gate == "code-ac" && (j.Result == "accept" || j.Result == "carried") {
			return j.Judged
		}
	}
	p.t.Fatalf("no accepted code-ac judged entry on %s: %+v", id, rep.Judged)
	return ""
}

func (p *prSandbox) backToReview(id string) {
	p.t.Helper()
	p.must("story", "set", id, "--status", "integration")
	p.must("story", "set", id, "--status", "in_review")
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

	check := shBlock0(t, repoSkillRel(t, "satelle-pr-approval-check"))
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

func TestPRApproveFromReviewPublishesOnTheStory(t *testing.T) {
	n := repoPRNames(t)
	n.codeACOnRoute = true
	n.approveOnRoute = true
	p := newPRSandboxNamed(t, n)
	short := p.head[:12]

	untitled := p.item(20, false)
	calls := p.claudeCalls()
	if r := p.run("story", "set", untitled, "--status", "in_review"); r.code == 0 || !strings.Contains(r.out+r.err, "no open pull request") {
		t.Fatalf("untitled: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if p.published() != 0 || p.status(untitled) != "integration" || p.claudeCalls() != calls {
		t.Fatalf("untitled: published %d, status %s, judge calls %d (want %d)", p.published(), p.status(untitled), p.claudeCalls(), calls)
	}

	p.judge("accept", "every criterion is met: AC1 to AC4 hold on src/change.go")
	approved := p.item(21, true)
	calls = p.claudeCalls()
	n0 := p.statusCount()
	if r := p.run("story", "set", approved, "--status", "in_review"); r.code != 0 {
		t.Fatalf("integration -> in_review: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if got := p.status(approved); got != "in_review" {
		t.Fatalf("status %s, want in_review", got)
	}
	if got := p.claudeCalls(); got != calls {
		t.Fatalf("presenting into in_review ran %d extra judge(s), want 0", got-calls)
	}
	if pend := lastRun(p.gateRuns(approved), "pr-pending"); pend.Result != "accept" {
		t.Fatalf("pr-pending %+v", pend)
	} else {
		for _, c := range []string{"satelle/pr-ready", "satelle/pr-review", "satelle/pr-approval"} {
			if !strings.Contains(pend.Notes, c+" pending on "+short) {
				t.Errorf("pr-pending notes lack %s: %q", c, pend.Notes)
			}
		}
	}
	if run := lastRun(p.gateRuns(approved), "pr-review"); run.Result != "accept" ||
		!strings.Contains(run.Notes, "published APPROVE on "+approved+" head "+short) ||
		!strings.Contains(run.Notes, "after "+statusSkill+": satelle/pr-review success on "+short+", posted by the satelle app (account "+appAccount+")") {
		t.Fatalf("pr-review %+v", run)
	}
	if got := p.statusesSince(n0); !slices.Equal(got, []string{"satelle/pr-approval pending", "satelle/pr-ready pending", "satelle/pr-review pending", "satelle/pr-review success"}) {
		t.Fatalf("integration -> in_review statuses %v", got)
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
	if want := "satelle · project acme · gate code-ac (" + p.reviewer + ") on " + approved + " head " + short + ": approve"; first != want {
		t.Errorf("body's first line %q, want %q", first, want)
	}
	if !strings.Contains(rest, "every criterion is met: AC1 to AC4 hold on src/change.go") {
		t.Errorf("body lacks the code-ac notes:\n%s", rv.GetBody())
	}
	gr := p.githubReviews(approved)
	if len(gr) != 1 || gr[0]["event"] != "APPROVE" || gr[0]["commit_sha"] != p.head || gr[0]["pull_number"] != float64(21) ||
		gr[0]["repository"] != "orbelle/acme" || gr[0]["review_id"] == nil || !strings.HasPrefix(gr[0]["html_url"].(string), "https://github.com/orbelle/acme/pull/21") ||
		len(gr[0]["body_sha256"].(string)) != 64 {
		t.Fatalf("github.review %v", gr)
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
	if r := p.exec("bash", "", "-c", block); r.code != 0 || !strings.Contains(r.out, approved+": sent back: the satelle app requested changes on head "+p.head) {
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

	p.must("story", "set", approved, "--status", "integration")
	if r := p.run("story", "set", approved, "--status", "in_review"); r.code != 0 {
		t.Fatalf("idempotent integration -> in_review: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if p.published() != 1 {
		t.Fatalf("idempotent re-present published %d reviews, want 1", p.published())
	}
	if run := lastRun(p.gateRuns(approved), "pr-review"); !strings.Contains(run.Notes, "already published on head "+short+" of "+approved) {
		t.Fatalf("idempotent pr-review %+v", run)
	}

	p.judge("accept", "carried check: AC1 holds on src/change.go with full coverage")
	carried := p.item(22, true)
	p.must("story", "set", carried, "--status", "in_progress")
	p.must("story", "set", carried, "--status", "integration")
	if run := lastRun(p.gateRuns(carried), "code-ac"); run.Result != "carried" {
		t.Fatalf("carried code-ac %+v", run)
	}
	if r := p.run("story", "set", carried, "--status", "in_review"); r.code != 0 {
		t.Fatalf("carried integration -> in_review: exit %d\n%s%s", r.code, r.out, r.err)
	}
	revs = p.srv.PublishedReviews("acme")
	if len(revs) != 2 {
		t.Fatalf("carried published %d reviews, want 2", len(revs))
	}
	first, rest, _ = strings.Cut(revs[1].GetBody(), "\n")
	if want := "satelle · project acme · gate code-ac (" + p.reviewer + ") on " + carried + " head " + short + ": approve"; first != want {
		t.Errorf("carried body's first line %q, want %q", first, want)
	}
	if !strings.Contains(rest, "carried check: AC1 holds on src/change.go with full coverage") {
		t.Errorf("carried body lacks the original code-ac notes:\n%s", revs[1].GetBody())
	}

	reqChanges := p.item(23, true)
	p.must("ledger", "append", reqChanges, "--kind", "github.review", "--payload",
		fmt.Sprintf(`{"event":"REQUEST_CHANGES","pull_number":23,"commit_sha":%q,"review_id":999}`, p.head))
	pubBefore := p.published()
	if r := p.run("story", "set", reqChanges, "--status", "in_review"); r.code == 0 ||
		!strings.Contains(r.out+r.err, "not publishing an approval: changes were requested on head "+short+" of "+reqChanges) {
		t.Fatalf("over requested changes: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if p.published() != pubBefore || p.status(reqChanges) != "integration" {
		t.Fatalf("over requested changes: published %d (want %d), status %s", p.published(), pubBefore, p.status(reqChanges))
	}

	mismatch := p.item(24, true)
	p.setPR(24, p.old, "change ("+mismatch+")")
	judged := p.diffJudgedHash(mismatch)
	pubBefore = p.published()
	if r := p.run("story", "set", mismatch, "--status", "in_review"); r.code == 0 ||
		!strings.Contains(r.out+r.err, p.old[:12]) || !strings.Contains(r.out+r.err, judged[:12]) {
		t.Fatalf("mismatched PR head: exit %d (want head %s and judged %s)\n%s%s", r.code, p.old[:12], judged[:12], r.out, r.err)
	}
	if p.published() != pubBefore || p.status(mismatch) != "integration" || len(p.githubReviews(mismatch)) != 0 {
		t.Fatalf("mismatched PR head: published %d (want %d), status %s, github.review %v", p.published(), pubBefore, p.status(mismatch), p.githubReviews(mismatch))
	}

	moved := p.item(25, true)
	judged = p.diffJudgedHash(moved)
	p.write("src/later.go", "package change\n")
	p.git("add", "-A")
	p.git("commit", "-q", "-m", "unjudged commit")
	newHead := strings.TrimSpace(p.git("rev-parse", "HEAD"))
	p.setPR(25, newHead, "change ("+moved+")")
	pubBefore = p.published()
	if r := p.run("story", "set", moved, "--status", "in_review"); r.code == 0 ||
		!strings.Contains(r.out+r.err, newHead[:12]) || !strings.Contains(r.out+r.err, judged[:12]) {
		t.Fatalf("unjudged commit: exit %d (want head %s and judged %s)\n%s%s", r.code, newHead[:12], judged[:12], r.out, r.err)
	}
	if p.published() != pubBefore || p.status(moved) != "integration" || len(p.githubReviews(moved)) != 0 {
		t.Fatalf("unjudged commit: published %d (want %d), status %s, github.review %v", p.published(), pubBefore, p.status(moved), p.githubReviews(moved))
	}

	unjudged := p.create("story", "--title", "unjudged", "--criteria", "it is reviewed").ID
	p.must("story", "seat", unjudged, "--base", p.old)
	p.setPR(26, newHead, "unjudged ("+unjudged+")")
	p.env["SATELLE_ITEM"], p.env["SATELLE_FROM"], p.env["SATELLE_TO"] = unjudged, "integration", "in_review"
	r := p.exec("bash", "", "-c", shBlock0(t, repoSkillRel(t, approveFromReviewSkill)))
	delete(p.env, "SATELLE_ITEM")
	delete(p.env, "SATELLE_FROM")
	delete(p.env, "SATELLE_TO")
	if r.code == 0 || !strings.Contains(r.out, "never moved from in_progress into integration") || p.published() != pubBefore {
		t.Fatalf("unjudged story: exit %d, published %d (want %d)\n%s%s", r.code, p.published(), pubBefore, r.out, r.err)
	}

	forged := p.item(27, true)
	p.must("ledger", "append", forged, "--kind", "gate.run", "--payload", `{"gate":"code-ac","result":"accept","binding":"forged","change":{"judged":"x"}}`)
	p.env["SATELLE_ITEM"], p.env["SATELLE_FROM"], p.env["SATELLE_TO"] = forged, "integration", "in_review"
	r = p.exec("bash", "", "-c", shBlock0(t, repoSkillRel(t, approveFromReviewSkill)))
	delete(p.env, "SATELLE_ITEM")
	delete(p.env, "SATELLE_FROM")
	delete(p.env, "SATELLE_TO")
	if r.code == 0 || !strings.Contains(r.out, "recorded after its latest move from in_progress into integration") || p.published() != pubBefore {
		t.Fatalf("forged gate.run: exit %d, published %d (want %d)\n%s%s", r.code, p.published(), pubBefore, r.out, r.err)
	}

	parked := p.item(28, true)
	p.must("story", "set", parked, "--status", "blocked")
	p.must("ledger", "append", parked, "--kind", "gate.run", "--payload", `{"gate":"code-ac","result":"accept","binding":"forged","change":{"judged":"x"}}`)
	p.must("story", "set", parked, "--status", "integration")
	p.env["SATELLE_ITEM"], p.env["SATELLE_FROM"], p.env["SATELLE_TO"] = parked, "integration", "in_review"
	r = p.exec("bash", "", "-c", shBlock0(t, repoSkillRel(t, approveFromReviewSkill)))
	delete(p.env, "SATELLE_ITEM")
	delete(p.env, "SATELLE_FROM")
	delete(p.env, "SATELLE_TO")
	if r.code == 0 || !strings.Contains(r.out, "recorded after its latest move from in_progress into integration") || p.published() != pubBefore {
		t.Fatalf("forged gate.run across a park and resume: exit %d, published %d (want %d)\n%s%s", r.code, p.published(), pubBefore, r.out, r.err)
	}
	p.must("ledger", "verify")
}

const defaultVerdictLines = "verdict_gate=code-ac\nverdict_from=in_progress\nverdict_to=integration\n"

func TestPRApproveFromReviewVerdictLines(t *testing.T) {
	skills, err := workflow.LoadSkills("../.satelle/skills")
	if err != nil {
		t.Fatal(err)
	}
	sk, ok := skills[approveFromReviewSkill]
	if !ok {
		t.Fatalf("no skill %s", approveFromReviewSkill)
	}
	for _, line := range strings.Split(strings.TrimSuffix(defaultVerdictLines, "\n"), "\n") {
		if got := len(regexp.MustCompile(`(?m)^`+regexp.QuoteMeta(line)+`$`).FindAllString(sk.Script, -1)); got != 1 {
			t.Errorf("%s holds the line %q %d times, want 1", approveFromReviewSkill, line, got)
		}
	}
	lines := strings.Split(sk.Script, "\n")
	if len(lines) < 2 || lines[0] != "#!/usr/bin/env bash" || lines[1] != "set -u" {
		t.Fatalf("%s's shell does not open with the shebang and set -u: %q", approveFromReviewSkill, lines[:min(2, len(lines))])
	}
	assign := regexp.MustCompile(`^[a-z_]+=\S*$`)
	var top []string
	for _, ln := range lines[2:] {
		if !assign.MatchString(ln) {
			break
		}
		top = append(top, ln)
	}
	if got := strings.Join(top, "\n") + "\n"; got != defaultVerdictLines {
		t.Errorf("the value lines at the top of %s's shell are %q, want %q", approveFromReviewSkill, got, defaultVerdictLines)
	}
	rest := strings.Replace(sk.Script, defaultVerdictLines, "", 1)
	for _, w := range []string{"code-ac", "in_progress", "integration"} {
		if m := regexp.MustCompile(`\b` + w + `\b`).FindString(rest); m != "" {
			t.Errorf("%s's shell names %s outside its value lines", approveFromReviewSkill, w)
		}
	}
}

func substrateRoute(name, gates string) string {
	return `name = ` + strconv.Quote(name) + `
[states.backlog]
start = true
[states.in_progress]
performer = { agent = "executor", skill = "coder" }
[states.approval]
[states.blocked]
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
to = "approval"
` + sequential([]string{"reference-review"}) + `[[transition]]
from = ["approval"]
to = "in_progress"
[[transition]]
from = ["in_progress", "approval"]
to = "blocked"
[[transition]]
from = ["blocked"]
to = "@prior"
[[transition]]
from = ["approval"]
to = "done"
`
}

func TestPRApproveFromReviewReadsTheNamedVerdict(t *testing.T) {
	n := repoPRNames(t)
	n.queue = ""
	n.route = func(gates string) string { return substrateRoute("delivery", gates) }
	p := newPRSandboxNamed(t, n)
	origin := realDir(t, t.TempDir())
	p.exec("git", "", "init", "-q", "--bare", origin)
	p.git("remote", "add", "origin", origin)
	p.git("push", "-q", "origin", p.old+":refs/heads/trunk")
	p.exec("git", "", "-C", origin, "symbolic-ref", "HEAD", "refs/heads/trunk")
	p.git("fetch", "-q", "origin")
	short := p.head[:12]

	onRoute := func(pr int) string {
		t.Helper()
		id := p.create("story", "--title", "change", "--criteria", "it is reviewed").ID
		p.must("story", "seat", id, "--base", p.old)
		p.must("story", "set", id, "--status", "in_progress")
		p.must("story", "set", id, "--status", "approval")
		p.setPR(pr, p.head, "change ("+id+")")
		return id
	}
	runShell := func(id, block string) result {
		t.Helper()
		p.env["SATELLE_ITEM"], p.env["SATELLE_FROM"], p.env["SATELLE_TO"] = id, "approval", "done"
		defer func() { delete(p.env, "SATELLE_ITEM"); delete(p.env, "SATELLE_FROM"); delete(p.env, "SATELLE_TO") }()
		return p.exec("bash", "", "-c", block)
	}
	shell := shBlock0(t, repoSkillRel(t, approveFromReviewSkill))
	named := strings.Replace(shell, defaultVerdictLines, "verdict_gate=reference-review\nverdict_from=in_progress\nverdict_to=approval\n", 1)
	if named == shell {
		t.Fatalf("%s's shell holds no default value lines", approveFromReviewSkill)
	}

	p.judge("accept", "every criterion is met: AC1 to AC4 hold on src/change.go")
	id := onRoute(31)
	if run := lastRun(p.gateRuns(id), "reference-review"); run.Result != "accept" || run.Transition != "in_progress->approval" {
		t.Fatalf("reference-review %+v", run)
	}
	if run := lastRun(p.gateRuns(id), "code-ac"); run.Gate != "" {
		t.Fatalf("code-ac ran on the substrate route: %+v", run)
	}
	r := runShell(id, named)
	if r.code != 0 || !strings.Contains(r.out, "published APPROVE on "+id+" head "+short) {
		t.Fatalf("named verdict: exit %d\n%s%s", r.code, r.out, r.err)
	}
	revs := p.srv.PublishedReviews("acme")
	if len(revs) != 1 {
		t.Fatalf("published %d reviews, want 1", len(revs))
	}
	rv := revs[0]
	if rv.GetEvent() != "APPROVE" || rv.GetCommitSha() != p.head || rv.GetPullNumber() != 31 {
		t.Fatalf("published %v", rv)
	}
	first, rest, _ := strings.Cut(rv.GetBody(), "\n")
	if want := "satelle · project acme · gate reference-review (" + p.reviewer + ") on " + id + " head " + short + ": approve"; first != want {
		t.Errorf("body's first line %q, want %q", first, want)
	}
	if !strings.Contains(rest, "every criterion is met: AC1 to AC4 hold on src/change.go") {
		t.Errorf("body lacks the reference-review notes:\n%s", rv.GetBody())
	}
	gr := p.githubReviews(id)
	if len(gr) != 1 || gr[0]["event"] != "APPROVE" || gr[0]["commit_sha"] != p.head || gr[0]["pull_number"] != float64(31) {
		t.Fatalf("github.review %v", gr)
	}

	other := onRoute(32)
	r = runShell(other, shell)
	if r.code == 0 || !strings.Contains(r.out, "never moved from in_progress into integration") || p.published() != 1 || len(p.githubReviews(other)) != 0 {
		t.Fatalf("default lines on the substrate route: exit %d, published %d (want 1)\n%s%s", r.code, p.published(), r.out, r.err)
	}
	p.must("ledger", "verify")
}

const prGateRoutes = ".satelle/pr-gates.routes"

func TestPRApproveFromReviewTakesTheRouteValues(t *testing.T) {
	n := repoPRNames(t)
	n.queue = ""
	n.codeACOnRoute = true
	n.approveOnRoute = true
	p := newPRSandboxNamed(t, n)
	origin := realDir(t, t.TempDir())
	p.exec("git", "", "init", "-q", "--bare", origin)
	p.git("remote", "add", "origin", origin)
	p.git("push", "-q", "origin", p.old+":refs/heads/trunk")
	p.exec("git", "", "-C", origin, "symbolic-ref", "HEAD", "refs/heads/trunk")

	refGate := repoPRGateSet(t)["code-ac"]
	refGate.Panel = []string{p.reviewer}
	refReview := gateTOML(t, "reference-review", refGate)
	p.write(".satelle/workflows/substrate.toml", substrateRoute("substrate", refReview))
	p.write(".satelle/satelle.toml", prSandboxConfig(t)+"\n[[workflows.rule]]\nworkflow = \"substrate\"\ncategory = \"substrate\"\n")
	p.git("add", "-A")
	p.git("commit", "-q", "-m", "the substrate route")
	p.mustConfigValid()
	p.head = strings.TrimSpace(p.git("rev-parse", "HEAD"))
	short := p.head[:12]

	pushRoutes := func(body string) {
		t.Helper()
		blob := strings.TrimSpace(p.exec("git", body, "hash-object", "-w", "--stdin").out)
		p.env["GIT_INDEX_FILE"] = filepath.Join(t.TempDir(), "index")
		p.git("read-tree", p.old)
		p.git("update-index", "--add", "--cacheinfo", "100644,"+blob+","+prGateRoutes)
		tree := strings.TrimSpace(p.git("write-tree"))
		delete(p.env, "GIT_INDEX_FILE")
		c := strings.TrimSpace(p.git("commit-tree", tree, "-p", p.old, "-m", "routes"))
		p.git("push", "-q", "-f", "origin", c+":refs/heads/trunk")
	}
	runShell := func(id string) result {
		t.Helper()
		p.env["SATELLE_ITEM"], p.env["SATELLE_FROM"], p.env["SATELLE_TO"] = id, "", ""
		defer func() { delete(p.env, "SATELLE_ITEM"); delete(p.env, "SATELLE_FROM"); delete(p.env, "SATELLE_TO") }()
		return p.exec("bash", "", "-c", shBlock0(t, repoSkillRel(t, approveFromReviewSkill)))
	}
	onSubstrate := func(pr int) string {
		t.Helper()
		id := p.create("story", "--title", "change", "--criteria", "it is reviewed", "--category", "substrate").ID
		p.must("story", "seat", id, "--base", p.old)
		p.must("story", "set", id, "--status", "in_progress")
		p.must("story", "set", id, "--status", "approval")
		if run := lastRun(p.gateRuns(id), "reference-review"); run.Result != "accept" || run.Transition != "in_progress->approval" {
			t.Fatalf("reference-review on %s %+v", id, run)
		}
		p.setPR(pr, p.head, "change ("+id+")")
		return id
	}
	published := func(id string, pr int, gate, notes string) {
		t.Helper()
		r := runShell(id)
		if r.code != 0 || !strings.Contains(r.out, "published APPROVE on "+id+" head "+short) {
			t.Fatalf("%s: exit %d\n%s%s", id, r.code, r.out, r.err)
		}
		revs := p.srv.PublishedReviews("acme")
		rv := revs[len(revs)-1]
		if rv.GetEvent() != "APPROVE" || rv.GetCommitSha() != p.head || rv.GetPullNumber() != int64(pr) {
			t.Fatalf("%s: published %v", id, rv)
		}
		first, rest, _ := strings.Cut(rv.GetBody(), "\n")
		if want := "satelle · project acme · gate " + gate + " (" + p.reviewer + ") on " + id + " head " + short + ": approve"; first != want {
			t.Errorf("%s: body's first line %q, want %q", id, first, want)
		}
		if !strings.Contains(rest, notes) {
			t.Errorf("%s: body lacks the %s notes:\n%s", id, gate, rv.GetBody())
		}
		if gr := p.githubReviews(id); len(gr) != 1 || gr[0]["event"] != "APPROVE" || gr[0]["pull_number"] != float64(pr) {
			t.Fatalf("%s: github.review %v", id, gr)
		}
	}
	refused := func(id, name string, has ...string) {
		t.Helper()
		before := p.published()
		r := runShell(id)
		for _, h := range has {
			if r.code == 0 || !strings.Contains(r.out, h) {
				t.Errorf("%s: exit %d, want a refusal naming %q\n%s%s", name, r.code, h, r.out, r.err)
			}
		}
		if p.published() != before || len(p.githubReviews(id)) != 0 {
			t.Errorf("%s: published %d (want %d), github.review %v", name, p.published(), before, p.githubReviews(id))
		}
	}

	pushRoutes("# verdicts per route\n\n  substrate   reference-review in_progress approval\n")

	p.judge("accept", "code-ac: every criterion is met, AC1 holds on src/change.go")
	a := p.item(31, true)
	published(a, 31, "code-ac", "code-ac: every criterion is met, AC1 holds on src/change.go")

	p.judge("accept", "reference-review: every criterion is met, AC1 holds as src/change.go says")
	b := onSubstrate(32)
	if run := lastRun(p.gateRuns(b), "code-ac"); run.Gate != "" {
		t.Fatalf("code-ac ran on the substrate route: %+v", run)
	}
	published(b, 32, "reference-review", "reference-review: every criterion is met, AC1 holds as src/change.go says")

	installed, err := os.ReadFile(filepath.Join(p.dir, repoSkillRel(t, approveFromReviewSkill)))
	if err != nil {
		t.Fatal(err)
	}
	if string(installed) != repoText(t, repoSkillRel(t, approveFromReviewSkill)) {
		t.Fatalf("the installed %s differs from the pack's", approveFromReviewSkill)
	}

	c := onSubstrate(33)
	p.write(prGateRoutes, "substrate code-ac in_progress integration\n")
	published(c, 33, "reference-review", "reference-review: every criterion is met, AC1 holds as src/change.go says")
	if err := os.Remove(filepath.Join(p.dir, prGateRoutes)); err != nil {
		t.Fatal(err)
	}

	pushRoutes("other reference-review in_progress approval\n")
	d := onSubstrate(34)
	refused(d, "no line for the route", "never moved from in_progress into integration")

	pushRoutes("substrate reference-review in_progress approval\nsubstrate code-ac in_progress integration\n")
	refused(d, "two lines", prGateRoutes, "substrate", "several lines")

	pushRoutes("# one route\nsubstrate reference-review approval\n")
	refused(d, "three fields", prGateRoutes, "line 2", "<workflow> <verdict_gate> <verdict_from> <verdict_to>")

	pushRoutes("substrate reference-review in_progress approval\n")
	p.write(".satelle/workflows/substrate.toml", strings.Replace(substrateRoute("substrate", refReview), "[states.approval]\n", "[states.approval]\nenqueue = [\"landing\"]\n", 1))
	p.write(".satelle/queues/landing.toml", queueTOML("origin/trunk", "substrate"))
	p.git("add", "-A")
	p.git("commit", "-q", "-m", "the substrate route lands through a queue")
	p.git("remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing"))
	refused(d, "fetch failure", "could not fetch origin trunk to read "+prGateRoutes)
	p.must("ledger", "verify")
}

func TestPRReviewWaitsAtReviewWithoutAReview(t *testing.T) {
	p := newPRSandbox(t)
	id := p.reviewing(50, true)
	p.serveReviews("none")
	const rounds = 5
	for i := range rounds {
		r := p.present(id)
		if r.code == 0 || !strings.Contains(r.err, "no approving review") || strings.Contains(r.err, "pr-review.md") {
			t.Fatalf("presentation %d: exit %d\n%s%s", i+1, r.code, r.out, r.err)
		}
		if got := p.status(id); got != "in_review" {
			t.Fatalf("presentation %d left the story at %s, want in_review", i+1, got)
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
		t.Fatalf("missing approval blocked the story %d times", b)
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
	toReview := []string{"pr-pending", "integration-check", "pr-ready", "pr-review"}
	routed := 0
	for _, f := range files {
		w, err := workflow.Parse(f)
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(f)
		if name == "pr-review.toml" {
			t.Errorf("pr-review.toml is still present")
		}
		route := name == "delivery.toml" || name == "chore.toml"
		if g, ok := w.Gates["pr-review"]; ok && (!route || g.Skill != approveFromReviewSkill || len(g.Judges()) != 0) {
			t.Errorf("%s: gate pr-review is %+v, want the check %s on delivery and chore only", name, g, approveFromReviewSkill)
		}
		for _, tr := range w.Transitions {
			approval := route && slices.Equal(tr.From, []string{"in_review"}) && tr.To == "approved"
			toReviewMove := route && slices.Equal(tr.From, []string{"integration"}) && tr.To == "in_review"
			for _, g := range tr.Groups {
				if !approval && slices.Contains(g.Gates, "pr-approval") || !toReviewMove && slices.Contains(g.Gates, "pr-review") {
					t.Errorf("%s: %v -> %s runs %v", name, tr.From, tr.To, g.Gates)
				}
			}
		}
		if !route {
			continue
		}
		routed++
		st, ok := w.States["in_review"]
		if !ok || st.Waits != "" || st.Hold != nil || st.Terminal || st.Park || st.Performer != nil {
			t.Errorf("%s: in_review is %+v, want a state with no waits, no hold and no performer", name, st)
		}
		if _, err := w.Find("integration", "approved", ""); err == nil {
			t.Errorf("%s: integration -> approved skips in_review", name)
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
			t.Errorf("%s: in_review cannot be blocked: %v", name, err)
		}
	}
	if routed != 2 {
		t.Fatalf("checked %d of delivery.toml and chore.toml", routed)
	}
	if strings.Contains(shBlock0(t, ".satelle/skills/satelle-pr-ready-check/SKILL.md"), `echo "approved:`) {
		t.Error("pr-ready still approves")
	}
	for _, retired := range []string{
		"../.satelle/workflows/pr-review.toml",
		"../.satelle/tasks/pr-review.md",
		"../.satelle/skills/satelle-pr-review-publish.md",
		"../.satelle/skills/satelle-pr-review-run-publish.md",
	} {
		if _, err := os.Stat(retired); !os.IsNotExist(err) {
			t.Errorf("%s is still present: %v", retired, err)
		}
	}
	retiredTerms := regexp.MustCompile(`(?i)pr-review\.toml|tasks/pr-review\.md|satelle-pr-review-run-publish|pull-request review (workflow|run|task)|cold review(er)?`)
	for _, dir := range []string{"../.satelle/workflows", "../.satelle/tasks", "../.satelle/skills"} {
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && d.Name() == "testdata" {
				return filepath.SkipDir
			}
			if d.IsDir() {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if m := retiredTerms.FindString(string(b)); m != "" {
				t.Errorf("%s still names retired PR review term %q", path, m)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	skills, err := workflow.LoadSkills("../.satelle/skills")
	if err != nil {
		t.Fatal(err)
	}
	for _, sk := range skills {
		b, err := os.ReadFile(sk.Path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "satelle github") && sk.Name != approveFromReviewSkill && sk.Name != statusSkill {
			t.Errorf("%s calls satelle github", sk.Path)
		}
	}
	block := shBlock0(t, repoSkillRel(t, statusSkill))
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
	if m := regexp.MustCompile(`(?m)^app_account=(\d+)$`).FindStringSubmatch(block); m == nil || m[1] != appAccount {
		t.Errorf("the status skill's own app_account= line is %q, want %s", m, appAccount)
	}
	if m := regexp.MustCompile(`(sed|cat)[^\n]*\.satelle/skills/`).FindString(block); m != "" {
		t.Errorf("the status skill reads a skill file: %s", m)
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
		for _, w := range []string{"satelle/pr-", statusSkill, "pr-pending", "satelle-pr-review-publish", "satelle-pr-review-run-publish", approveFromReviewSkill} {
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

func repoSkillRel(t *testing.T, name string) string {
	t.Helper()
	dirRel := ".satelle/skills/" + name + "/" + workflow.SkillFile
	if _, err := os.Stat(filepath.Join("..", dirRel)); err == nil {
		return dirRel
	}
	return ".satelle/skills/" + name + ".md"
}

func runSkillCheck(t *testing.T, s *sandbox, skill string) result {
	t.Helper()
	rel := repoSkillRel(t, skill)
	block := shBlock0(t, rel)
	if strings.HasSuffix(rel, "/"+workflow.SkillFile) {
		dir, err := filepath.Abs(filepath.Dir(filepath.Join("..", rel)))
		if err != nil {
			t.Fatal(err)
		}
		s.env["SATELLE_SKILL_DIR"] = dir
		defer delete(s.env, "SATELLE_SKILL_DIR")
		block = gate.ResolveSkillScript(block)
	}
	return s.exec("bash", "", "-c", block)
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
[gates.pr-review]
skill = "satelle-code-ac-review"
agent = "`+p.reviewer+`"
after = ["`+statusSkill+`"]
`, nil, []string{"pr-ready"}, []string{"pr-review"}))
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
		{"approve accepts", "", "accept", "every criterion is met: the stub is ready and the judge accepts", false, "pr-review", "accept", "success", "every criterion is met"},
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
			edge = "in_review->approved"
			p.must("story", "set", id, "--status", "in_review")
			n0 = p.statusCount()
			p.present(id)
			run = lastRun(p.gateRuns(id), c.gate)
			want := "approved"
			if c.result != "accept" {
				want = "in_review"
			}
			if got := p.status(id); got != want {
				t.Errorf("%s: story at %s, want %s", c.name, got, want)
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

	block := shBlock0(t, repoSkillRel(t, statusSkill))
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
	n := repoPRNames(t)
	n.codeACOnRoute = true
	n.approveOnRoute = true
	p := newPRSandboxNamed(t, n)
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
	pend := lastRun(p.gateRuns(id), "pr-pending")
	if pend.Result != "accept" {
		t.Fatalf("pr-pending %+v", pend)
	}
	for _, c := range []string{"satelle/pr-ready", "satelle/pr-review", "satelle/pr-approval"} {
		if !strings.Contains(pend.Notes, c+" not published on "+short+": ") || !strings.Contains(pend.Notes, refusal) {
			t.Errorf("pr-pending notes for %s: %q", c, pend.Notes)
		}
	}
	run := lastRun(p.gateRuns(id), "pr-review")
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

	rej := p.item(32, true)
	p.must("story", "set", rej, "--status", "in_review")
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
	r := p.run("gate", "run", "delivery/pr-pending", "--item", dry)
	if !strings.Contains(r.out+r.err, "a dry run publishes no status") || p.statusCount() != 0 {
		t.Fatalf("dry run: exit %d, %d statuses\n%s%s", r.code, p.statusCount(), r.out, r.err)
	}
}

var prGateSkills = []string{statusSkill, "satelle-pr-ready-check", approveFromReviewSkill, "satelle-pr-approval-check"}

func baseBranchLines(t *testing.T, text string) string {
	t.Helper()
	_, rest, ok := strings.Cut(text, "resolve_base_branch() {\n")
	if !ok {
		return ""
	}
	block, _, ok := strings.Cut(rest, "\n}\nresolve_base_branch\n")
	if !ok {
		t.Fatal("a base branch block has no end line")
	}
	return block
}

func TestPRGateSkillsHoldNoRepoValue(t *testing.T) {
	strip := strings.NewReplacer("--name pr-review", "", `.name == "pr-review"`, "", "`pr-review`", "", prGateRoutes, "")
	forbidden := map[string]*regexp.Regexp{
		"the word main":               regexp.MustCompile(`\bmain\b`),
		"a gates= assignment":         regexp.MustCompile(`(?m)^\s*gates=`),
		"a satelle/pr- context":       regexp.MustCompile(`satelle/pr-`),
		"a gate name":                 regexp.MustCompile(`(^|[^A-Za-z0-9_-])pr-(ready|approve|approval|pending|review)($|[^A-Za-z0-9_-])`),
		"a path under .satelle/tasks": regexp.MustCompile(`\.satelle/tasks/`),
		"a story id":                  regexp.MustCompile(`sto_[0-9a-f]{8}`),
	}
	entryForbidden := map[string]*regexp.Regexp{
		"the queue name release": regexp.MustCompile(`\brelease\b`),
	}
	rels := make([]string, 0, len(prGateSkills)+2)
	for _, name := range prGateSkills {
		rels = append(rels, repoSkillRel(t, name))
	}
	entryFiles := []string{prGateEntryDir + "/" + workflow.SkillFile, prGateEntryDir + "/" + workflow.PackFile}
	rels = append(rels, entryFiles...)
	var blocks []string
	for _, rel := range rels {
		b, err := os.ReadFile(filepath.Join("..", rel))
		if err != nil {
			t.Fatal(err)
		}
		text := string(b)
		if blk := baseBranchLines(t, text); blk != "" {
			blocks = append(blocks, blk)
		}
		for _, own := range append([]string{prGateEntry}, prGateSkills...) {
			text = strings.ReplaceAll(text, own, "")
		}
		text = strip.Replace(text)
		for what, re := range forbidden {
			for _, m := range re.FindAllString(text, -1) {
				t.Errorf("%s holds %s: %q", rel, what, m)
			}
		}
		if slices.Contains(entryFiles, rel) {
			for what, re := range entryForbidden {
				for _, m := range re.FindAllString(text, -1) {
					t.Errorf("%s holds %s: %q", rel, what, m)
				}
			}
		}
	}
	if len(blocks) != 3 {
		t.Fatalf("%d skills carry the base branch lines, want 3 (pr-ready, pr-approve-from-review, pr-approval)", len(blocks))
	}
	for i, b := range blocks[1:] {
		if b != blocks[0] {
			t.Errorf("the base branch lines differ between skills (block %d):\n%s\nvs\n%s", i+1, b, blocks[0])
		}
	}
}

const prGateEntry = "satelle-pr-gates"

func checkPackScriptNeeds(t *testing.T, pk workflow.Pack, skills map[string]workflow.Skill) {
	t.Helper()
	skillsDir := filepath.Join("..", ".satelle", "skills")
	declared := map[string]bool{}
	for _, s := range pk.Needs.Scripts {
		declared[s] = true
		if fi, err := os.Stat(filepath.Join(skillsDir, filepath.FromSlash(s))); err != nil || !fi.Mode().IsRegular() {
			t.Errorf("pack %s declares script %s, which is no file under .satelle/skills: %v", pk.Name, s, err)
		}
	}
	scriptRef := regexp.MustCompile(`scripts/[A-Za-z0-9._-]+`)
	for _, member := range pk.Skills {
		run := map[string]bool{}
		ents, err := os.ReadDir(filepath.Join(skillsDir, member, "scripts"))
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		for _, e := range ents {
			if e.Type().IsRegular() {
				run[member+"/scripts/"+e.Name()] = true
			}
		}
		for _, ref := range scriptRef.FindAllString(skills[member].Script, -1) {
			run[member+"/"+ref] = true
		}
		for _, s := range slices.Sorted(maps.Keys(run)) {
			if !declared[s] {
				t.Errorf("pack %s member %s runs %s, which its [needs] scripts does not declare: %v", pk.Name, member, s, pk.Needs.Scripts)
			}
		}
	}
}

const prGateEntryDir = ".satelle/skills/" + prGateEntry

func TestPRGatePack(t *testing.T) {
	packs, _, err := workflow.LoadPacks("../.satelle/skills")
	if err != nil {
		t.Fatal(err)
	}
	pk, ok := packs[prGateEntry]
	if !ok {
		t.Fatalf("no pack has entry %s: %v", prGateEntry, packs)
	}
	if pk.Version != "1.0.5" {
		t.Errorf("pack %s version %q, want 1.0.5", prGateEntry, pk.Version)
	}
	var packFiles []string
	for _, name := range append([]string{prGateEntry}, prGateSkills...) {
		err := filepath.WalkDir(filepath.Join("..", ".satelle", "skills", name), func(path string, d os.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() {
				return err
			}
			rel, err := filepath.Rel("..", path)
			packFiles = append(packFiles, rel)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	check := exec.Command("sh", append([]string{"scripts/comments.sh", "check"}, packFiles...)...)
	check.Dir = ".."
	if out, err := check.CombinedOutput(); err != nil {
		t.Errorf("the pack's files hold comments (%v):\n%s", err, out)
	}
	members := slices.Sorted(slices.Values(pk.Skills))
	if want := slices.Sorted(slices.Values(prGateSkills)); !slices.Equal(members, want) {
		t.Errorf("pack %s members %v, want %v", prGateEntry, members, want)
	}
	skills, err := workflow.LoadSkills("../.satelle/skills")
	if err != nil {
		t.Fatal(err)
	}
	if errs := pk.Validate(func(name string) (workflow.Skill, bool) { sk, ok := skills[name]; return sk, ok }); len(errs) != 0 {
		t.Errorf("pack %s: %v", prGateEntry, errs)
	}

	otherSkill := regexp.MustCompile(`\.satelle/skills/|skills/[A-Za-z0-9_-]+/`)
	for _, name := range prGateSkills {
		if _, err := os.Stat(filepath.Join("..", ".satelle", "skills", name+".md")); !os.IsNotExist(err) {
			t.Errorf("the single-file skill %s.md is still present: %v", name, err)
		}
		sk, ok := skills[name]
		if !ok || !sk.IsDir() {
			t.Errorf("%s: ok=%v, not a skill directory", name, ok)
			continue
		}
		if probs := sk.Problems(); len(probs) != 0 {
			t.Errorf("%s problems: %v", name, probs)
		}
		if m := otherSkill.FindString(sk.Script); m != "" {
			t.Errorf("%s's shell reads another skill's path: %q", name, m)
		}
	}
	for _, rel := range []string{
		"../.satelle/skills/satelle-pr-ready-check/SKILL.md",
		"../.satelle/skills/satelle-pr-ready-check/scripts/changelog-lines.sh",
	} {
		if _, err := os.Stat(rel); err != nil {
			t.Errorf("%s: %v", rel, err)
		}
	}
	if sk := skills["satelle-pr-ready-check"]; !strings.Contains(sk.Script, "scripts/changelog-lines.sh") || strings.Contains(sk.Script, "changelog-items.sh") {
		t.Errorf("satelle-pr-ready-check script does not run its own changelog-lines.sh:\n%s", sk.Script)
	}
	checkPackScriptNeeds(t, pk, skills)
	if !slices.Equal(pk.Needs.Network, []string{"api.github.com"}) {
		t.Errorf("pack %s [needs] network %v, want [api.github.com]", prGateEntry, pk.Needs.Network)
	}

	out, err := gittest.Command(t, "..", "ls-files", "-z").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel == "" || strings.HasPrefix(rel, "docs/releases/") {
			continue
		}
		b, err := os.ReadFile(filepath.Join("..", rel))
		if err != nil {
			continue
		}
		for _, name := range prGateSkills {
			if old := "skills/" + name + ".md"; strings.Contains(string(b), old) {
				t.Errorf("%s still names %s", rel, old)
			}
		}
	}

	accountLine := regexp.MustCompile(`(?m)^app_account=(\d+)$`)
	for _, name := range []string{statusSkill, "satelle-pr-approval-check"} {
		if m := accountLine.FindStringSubmatch(skills[name].Script); m == nil || m[1] != appAccount {
			t.Errorf("%s's app_account= line is %q, want %s", name, m, appAccount)
		}
	}

	guide := repoText(t, prGateEntryDir+"/"+workflow.SkillFile)
	for _, name := range prGateSkills {
		if w := "skills/" + name + "/" + workflow.SkillFile; !strings.Contains(guide, w) {
			t.Errorf("the pack guide does not name %s", w)
		}
	}
	for _, w := range []string{"ruleset", "satelle/<gate>", "after", "checkout.defaultRemote",
		".satelle/skills/satelle-pack.md", "substrate story", "satelle init",
		"code-ac", "in_progress", "integration", "cannot use", "unwired", "own skill",
		"verdict_gate=", "verdict_from=", "verdict_to=", prGateRoutes, "unedited", "base branch"} {
		if !strings.Contains(guide, w) {
			t.Errorf("the pack guide does not say %q", w)
		}
	}
	if strings.Contains(guide, "edits those three lines") {
		t.Error("the pack guide still tells a route to edit its installed copy of the member")
	}
	if member := repoText(t, repoSkillRel(t, approveFromReviewSkill)); !strings.Contains(member, prGateRoutes) || !strings.Contains(member, "unedited") {
		t.Errorf("%s does not say a route names its values in %s with the member unedited", approveFromReviewSkill, prGateRoutes)
	}
	if strings.Contains(strings.ReplaceAll(guide, ".satelle/skills/satelle-pack.md", ""), "skills/satelle-pack.md") {
		t.Error("the pack guide names skills/satelle-pack.md without its .satelle/ path")
	}
	if strings.Contains(guide, "satelle github") {
		t.Error("the pack guide names satelle github")
	}

	s := newSandbox(t)
	copyCheckout(t, "..", s.dir)
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "this repo's tree")
	r := s.exec(satelleBin, "", "validate", "--json")
	var v struct {
		Valid         bool              `json:"valid"`
		Problems      []string          `json:"problems"`
		SkillWarnings []json.RawMessage `json:"skill_warnings"`
		UnknownKeys   []struct {
			File string `json:"file"`
		} `json:"unknown_keys"`
	}
	if err := json.Unmarshal([]byte(r.out), &v); err != nil {
		t.Fatalf("validate --json: exit %d: %v\n%s%s", r.code, err, r.out, r.err)
	}
	if !v.Valid || r.code != 0 || len(v.Problems) != 0 || len(v.SkillWarnings) != 0 {
		t.Errorf("validate on this repo: valid %v, exit %d, problems %v, skill_warnings %s", v.Valid, r.code, v.Problems, v.SkillWarnings)
	}
	for _, u := range v.UnknownKeys {
		if strings.HasSuffix(u.File, workflow.PackFile) {
			t.Errorf("validate reports an unknown key in %s", u.File)
		}
	}
}

func TestPRGateSkillsTakeTheRepoValues(t *testing.T) {
	n := prNames{pending: "pending-x", ready: "ready-x", approve: "publish-x", approval: "approve-x", queue: "landing", base: "origin/trunk",
		waits: "the external approval", codeACOnRoute: true, readyOnRoute: true, approveOnRoute: true}
	p := newPRSandboxNamed(t, n)
	origin := realDir(t, t.TempDir())
	p.exec("git", "", "init", "-q", "--bare", origin)
	p.git("remote", "add", "origin", origin)
	p.git("push", "-q", "origin", p.old+":refs/heads/trunk")
	p.git("push", "-q", "origin", "HEAD:refs/heads/change")
	p.git("fetch", "-q", "origin")
	p.serveReviews("none")
	p.serveStatuses("by-app")
	served, err := os.ReadFile(filepath.Join(p.fake, "statuses.json"))
	if err != nil {
		t.Fatal(err)
	}
	renamed := strings.NewReplacer("satelle/pr-approval", "satelle/"+n.approval, "satelle/pr-review", "satelle/"+n.approve, "satelle/pr-ready", "satelle/"+n.ready)
	writeFile(t, filepath.Join(p.fake, "statuses.json"), renamed.Replace(string(served)), 0o644)
	short := p.head[:12]

	id := p.item(61, true)
	if r := p.exec(satelleBin, "### Changed\n- The gates read the base branch\n", "story", "attach", id, "--name", "changelog", "--type", "text/markdown", "--file", "-"); r.code != 0 {
		t.Fatalf("attach changelog: %s%s", r.out, r.err)
	}
	calls := p.claudeCalls()
	if r := p.run("story", "set", id, "--status", "in_review"); r.code != 0 || p.status(id) != "in_review" {
		t.Fatalf("integration -> in_review: exit %d, at %s\n%s%s", r.code, p.status(id), r.out, r.err)
	}
	if p.claudeCalls() != calls {
		t.Fatalf("integration -> in_review ran %d judge(s), want 0", p.claudeCalls()-calls)
	}
	ready := lastRun(p.gateRuns(id), n.ready)
	if ready.Result != "accept" || !strings.Contains(ready.Notes, "open against trunk, contains origin/trunk "+p.old[:12]) ||
		!strings.Contains(ready.Notes, id+" waits at in_review for "+n.waits) {
		t.Fatalf("%s %+v", n.ready, ready)
	}
	gr := p.githubReviews(id)
	if len(gr) != 1 || gr[0]["event"] != "APPROVE" {
		t.Fatalf("github.review %v", gr)
	}
	p.serveReviewsAs("approved-on-head", int64(gr[0]["review_id"].(float64)))
	if r := p.present(id); r.code != 0 || p.status(id) != "approved" {
		t.Fatalf("in_review -> approved: exit %d\n%s%s", r.code, r.out, r.err)
	}

	want := []string{"satelle/approve-x pending", "satelle/publish-x pending", "satelle/ready-x pending",
		"satelle/ready-x success", "satelle/publish-x success", "satelle/approve-x success"}
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
		t.Errorf("gh pr list ran %d times, want 3 (ready, approve-from-review, approval)\n%s", lists, log)
	}
	repoValue := regexp.MustCompile(`\bmain\b|\bpr-(ready|approve|approval|review)\b`)
	var seen []string
	seen = append(seen, string(log))
	for _, s := range p.srv.PublishedStatuses("acme") {
		seen = append(seen, s.GetContext()+" "+s.GetDescription())
	}
	for _, run := range p.gateRuns(id) {
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
		return runSkillCheck(t, p.sandbox, skill)
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
	p.exec("git", "", "-C", origin, "symbolic-ref", "HEAD", "refs/heads/absent")
	for _, c := range []struct {
		name    string
		enqueue []string
		queues  map[string]string
		has     []string
	}{
		{"no queue", nil, nil, []string{"the workflow delivery of " + id + " enqueues into no queue with a trigger base", "names no default branch", "[trigger]"}},
		{"two bases", []string{"landing", "other"}, map[string]string{"other": "origin/elsewhere"}, []string{"name 2 trigger bases", "landing base origin/trunk", "other base origin/elsewhere"}},
		{"no remote", []string{"landing"}, map[string]string{"landing": "trunk"}, []string{"the queue landing has trigger base trunk, which is not <remote>/<branch>"}},
	} {
		files := map[string]string{".satelle/workflows/delivery.toml": prSandboxWorkflowWith(t, gates(true), nil, []string{"pending-x"}, []string{"approve-x"}, c.enqueue, n.waits)}
		for q, base := range c.queues {
			files[".satelle/queues/"+q+".toml"] = queueTOML(base, "delivery")
		}
		commit(files)
		for _, skill := range []string{"satelle-pr-ready-check", approveFromReviewSkill, "satelle-pr-approval-check"} {
			r := runSkill(skill, "in_review", "approved")
			for _, h := range c.has {
				if r.code != 1 || !strings.Contains(r.out, h) {
					t.Errorf("%s, %s: exit %d, want 1 naming %q\n%s%s", c.name, skill, r.code, h, r.out, r.err)
				}
			}
		}
		if c.queues != nil {
			commit(map[string]string{".satelle/queues/landing.toml": queueTOML("origin/trunk", "delivery")})
		}
	}

	commit(map[string]string{
		".satelle/workflows/delivery.toml": prSandboxWorkflowWith(t, gates(false), nil, []string{"pending-x"}, []string{"approve-x"}, []string{"landing"}, n.waits),
	})
	n0 := p.statusCount()
	r := runSkill(statusSkill, "integration", "in_review")
	if r.code != 0 || p.statusCount() != n0 || strings.TrimSpace(r.out) != "no gate's after names "+statusSkill+", so no pending status is posted" {
		t.Errorf("pending mode with no gate naming the skill: exit %d, %d statuses\n%s%s", r.code, p.statusCount()-n0, r.out, r.err)
	}
}

func TestPRGateSkillsFallBackToTheRemoteDefaultBranch(t *testing.T) {
	n := prNames{pending: "pending-y", ready: "ready-y", approve: "publish-y", approval: "approve-y", queue: "",
		waits: "the external approval", codeACOnRoute: true, readyOnRoute: true, approveOnRoute: true}
	p := newPRSandboxNamed(t, n)
	origin := realDir(t, t.TempDir())
	p.exec("git", "", "init", "-q", "--bare", origin)
	p.git("remote", "add", "origin", origin)
	p.git("push", "-q", "origin", p.old+":refs/heads/stem")
	p.git("push", "-q", "origin", "HEAD:refs/heads/change")
	p.exec("git", "", "-C", origin, "symbolic-ref", "HEAD", "refs/heads/stem")
	p.git("fetch", "-q", "origin")
	p.serveReviews("none")
	p.serveStatuses("by-app")
	served, err := os.ReadFile(filepath.Join(p.fake, "statuses.json"))
	if err != nil {
		t.Fatal(err)
	}
	renamed := strings.NewReplacer("satelle/pr-approval", "satelle/"+n.approval, "satelle/pr-review", "satelle/"+n.approve, "satelle/pr-ready", "satelle/"+n.ready)
	writeFile(t, filepath.Join(p.fake, "statuses.json"), renamed.Replace(string(served)), 0o644)

	id := p.item(62, true)
	if r := p.exec(satelleBin, "### Changed\n- The gates read the remote's default branch\n", "story", "attach", id, "--name", "changelog", "--type", "text/markdown", "--file", "-"); r.code != 0 {
		t.Fatalf("attach changelog: %s%s", r.out, r.err)
	}
	if r := p.run("story", "set", id, "--status", "in_review"); r.code != 0 || p.status(id) != "in_review" {
		t.Fatalf("integration -> in_review: exit %d, at %s\n%s%s", r.code, p.status(id), r.out, r.err)
	}
	if ready := lastRun(p.gateRuns(id), n.ready); ready.Result != "accept" || !strings.Contains(ready.Notes, "open against stem, contains origin/stem "+p.old[:12]) {
		t.Fatalf("%s %+v", n.ready, ready)
	}
	gr := p.githubReviews(id)
	if len(gr) != 1 || gr[0]["event"] != "APPROVE" {
		t.Fatalf("github.review %v", gr)
	}
	p.serveReviewsAs("approved-on-head", int64(gr[0]["review_id"].(float64)))
	if r := p.present(id); r.code != 0 || p.status(id) != "approved" {
		t.Fatalf("in_review -> approved: exit %d\n%s%s", r.code, r.out, r.err)
	}
	prLists := func() []string {
		t.Helper()
		log, err := os.ReadFile(filepath.Join(p.fake, "gh.log"))
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, ln := range strings.Split(strings.TrimSpace(string(log)), "\n") {
			if strings.HasPrefix(ln, "pr list") {
				out = append(out, ln)
			}
		}
		return out
	}
	lists := prLists()
	if len(lists) != 3 {
		t.Fatalf("gh pr list ran %d times, want 3 (ready, approve-from-review, approval): %v", len(lists), lists)
	}
	for _, ln := range lists {
		if !strings.Contains(ln, "--base stem ") {
			t.Errorf("gh %s: not --base stem", ln)
		}
	}

	runSkill := func(skill string) result {
		t.Helper()
		p.env["SATELLE_ITEM"], p.env["SATELLE_FROM"], p.env["SATELLE_TO"] = id, "in_review", "approved"
		defer func() { delete(p.env, "SATELLE_ITEM"); delete(p.env, "SATELLE_FROM"); delete(p.env, "SATELLE_TO") }()
		return runSkillCheck(t, p.sandbox, skill)
	}
	checks := []string{"satelle-pr-ready-check", "satelle-pr-approval-check"}
	allBaseChecks := []string{"satelle-pr-ready-check", approveFromReviewSkill, "satelle-pr-approval-check"}
	gates := "[gates.pending-y]\nskill = \"" + statusSkill + "\"\n[gates.approve-y]\nskill = \"satelle-pr-approval-check\"\n"
	commit := func(enqueue []string, files map[string]string) {
		t.Helper()
		p.write(".satelle/workflows/delivery.toml", prSandboxWorkflowWith(t, gates, nil, []string{"pending-y"}, []string{"approve-y"}, enqueue, n.waits))
		for f, body := range files {
			p.write(f, body)
		}
		p.git("add", "-A")
		p.git("commit", "-q", "--allow-empty", "-m", "reconfigure")
		p.git("push", "-q", "-f", "origin", "HEAD:refs/heads/change")
	}
	refused := func(name string, skills []string, has ...string) {
		t.Helper()
		for _, skill := range skills {
			r := runSkill(skill)
			for _, h := range has {
				if r.code != 1 || !strings.Contains(r.out, h) {
					t.Errorf("%s, %s: exit %d, want 1 naming %q\n%s%s", name, skill, r.code, h, r.out, r.err)
				}
			}
		}
	}

	p.git("push", "-q", "origin", p.old+":refs/heads/trunk")
	commit([]string{"landing"}, map[string]string{".satelle/queues/landing.toml": queueTOML("origin/trunk", "delivery")})
	before := len(prLists())
	refused("queue base over the remote default", checks, "no open pull request against trunk has head")
	if lists := prLists()[before:]; len(lists) != 2 || !strings.Contains(lists[0], "--base trunk ") || !strings.Contains(lists[1], "--base trunk ") {
		t.Errorf("with a queue base, gh pr list %v, want two --base trunk", lists)
	}

	commit(nil, nil)
	opening := "the workflow delivery of " + id + " enqueues into no queue with a trigger base"
	p.exec("git", "", "-C", origin, "symbolic-ref", "HEAD", "refs/heads/absent")
	refused("remote HEAD names no branch", allBaseChecks, opening, "names no default branch", "[trigger]")
	p.exec("git", "", "-C", origin, "symbolic-ref", "HEAD", "refs/heads/stem")

	mirror := realDir(t, t.TempDir())
	p.exec("git", "", "init", "-q", "--bare", mirror)
	p.git("remote", "add", "mirror", mirror)
	refused("several remotes", allBaseChecks, opening, "origin", "mirror", "git config checkout.defaultRemote", "[trigger]")
	p.git("config", "checkout.defaultRemote", "origin")
	for _, skill := range checks {
		r := runSkill(skill)
		if r.code != 1 || !strings.Contains(r.out, "no open pull request against stem has head") || strings.Contains(r.out, "base branch") || strings.Contains(r.out, "enqueues into no queue") {
			t.Errorf("checkout.defaultRemote origin, %s: exit %d, want the lookup against stem\n%s%s", skill, r.code, r.out, r.err)
		}
	}
	p.git("config", "checkout.defaultRemote", "elsewhere")
	refused("checkout.defaultRemote names no remote", allBaseChecks, opening, "checkout.defaultRemote names elsewhere", "[trigger]")

	p.git("config", "--unset", "checkout.defaultRemote")
	p.git("remote", "remove", "mirror")
	p.git("remote", "remove", "origin")
	refused("no remote", allBaseChecks, opening, "has no git remote", "git remote add", "[trigger]")
}
