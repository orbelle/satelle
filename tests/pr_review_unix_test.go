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

var prRouteTail = []string{"pr-review", "pr-approval"}

func tomlGates(gates []string) string {
	return `"` + strings.Join(gates, `", "`) + `"`
}

const statusSkill = "satelle-gate-status-publish"

var prGateNames = []string{"pr-pending", "pr-ready", "pr-review", "pr-approval"}

func repoPRGates(t *testing.T) (review, approval workflow.Gate) {
	t.Helper()
	g := repoPRGateSet(t)
	return g["pr-review"], g["pr-approval"]
}

// repoPRGateSet reads the pull-request gates of delivery.toml and checks
// chore.toml defines each the same way.
func repoPRGateSet(t *testing.T) map[string]workflow.Gate {
	t.Helper()
	var got [2][]workflow.Gate
	for i, wf := range []string{"delivery", "chore"} {
		w, err := workflow.Parse("../.satelle/workflows/" + wf + ".toml")
		if err != nil {
			t.Fatal(err)
		}
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
	return out
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

func newPRSandbox(t *testing.T) *prSandbox {
	t.Helper()
	for _, tool := range []string{"bash", "jq", "git", "sha256sum"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s is required by the PR review skills and is not on PATH", tool)
		}
	}
	srv := fakeserver.Start(t, devToken)
	s := signedIn(t)
	s.must("project", "create", "acme", "--bind", "--server", srv.URL)
	srv.BindGitHub("acme", "orbelle/acme")

	gs := repoPRGateSet(t)
	review, approval, pending := gs["pr-review"], gs["pr-approval"], gs["pr-pending"]
	codeAC := repoGateAgents(t, ".satelle/workflows/delivery.toml")["code-ac"]
	if review.Skill != "satelle-code-ac-review" || len(review.Judges()) != 1 || !slices.Contains(codeAC, review.Judges()[0]) ||
		!slices.Equal(review.After, []string{"satelle-verdict-notes-check", "satelle-verdict-reference-check", "satelle-pr-review-publish", statusSkill}) {
		t.Fatalf("pr-review is %+v, want the code-ac judge (one of %v) with the reasons rules, the publish after, then the status", review, codeAC)
	}
	reviewer := review.Judges()[0]
	if approval.Skill != "satelle-pr-approval-check" || approval.Agent != "" || !slices.Equal(approval.After, []string{statusSkill}) {
		t.Fatalf("pr-approval is %+v", approval)
	}
	if pending.Skill != statusSkill || pending.Agent != "" || len(pending.After) != 0 {
		t.Fatalf("pr-pending is %+v", pending)
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
	s.write(".satelle/satelle.toml", "[workflows]\ndefault = \"delivery\"\n")
	s.write(".satelle/agents.toml", fmt.Sprintf("[agents.%q]\n", reviewer)+`command = "claude"
model = "ra-1"
tools = ["Read", "Grep", "Glob"]
[agents.executor]
command = "claude"
model = "ex-1"
tools = ["Read", "Edit"]
`)
	s.write(".satelle/workflows/delivery.toml", prSandboxWorkflow(gateTOML(t, "pr-pending", pending)+gateTOML(t, "pr-review", review)+gateTOML(t, "pr-approval", approval),
		append([]string{"pr-pending"}, prRouteTail...)))
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

func prSandboxWorkflow(gates string, route []string) string {
	return `name = "delivery"
[states.backlog]
start = true
[states.in_progress]
performer = { agent = "executor", skill = "coder" }
[states.integration]
[states.approved]
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
to = "approved"
groups = [{ mode = "sequential", gates = [` + tomlGates(route) + `] }]
[[transition]]
from = ["integration", "approved"]
to = "in_progress"
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

// statusesSince returns the commit statuses published after the first n.
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

var pendingStatuses = []string{"satelle/pr-ready pending", "satelle/pr-review pending", "satelle/pr-approval pending"}

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
	id := p.item(12, true)
	untitled := p.item(13, false)

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
		p.env["SATELLE_ITEM"], p.env["SATELLE_FROM"], p.env["SATELLE_TO"] = item, "integration", "approved"
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
		want := "integration"
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
			p.must("story", "set", id, "--status", "integration")
		}
	}
	if n := p.transitions(id, "integration", "in_progress"); n != 2 {
		t.Errorf("reopened %d times, want 2", n)
	}

	unrecorded := p.item(14, true)
	p.serveReviews("approved-on-head")
	if r := run(unrecorded); r.code == 0 || !strings.Contains(r.out, "is not on the item's ledger") {
		t.Errorf("unrecorded approval: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if got := p.status(unrecorded); got != "integration" {
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
	if got := p.status(untitled); got != "integration" {
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

func TestPRReviewGatePublishesAndReopens(t *testing.T) {
	p := newPRSandbox(t)

	untitled := p.item(20, false)
	p.judge("accept", "fine: AC1 holds, the publish check runs last in its after list")
	if r := p.present(untitled); r.code != 2 || !strings.Contains(r.err, "no open pull request") {
		t.Fatalf("untitled: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if n := p.published(); n != 0 {
		t.Fatalf("published %d reviews for a PR mapped to no item", n)
	}
	if got := p.status(untitled); got != "integration" {
		t.Fatalf("untitled item moved to %s", got)
	}

	silent := p.item(19, true)
	p.judge("accept", "")
	n0 := p.statusCount()
	if r := p.present(silent); r.code != 2 || !strings.Contains(r.err, "the judge gave no reasons") {
		t.Fatalf("silent accept: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if n := p.published(); n != 0 {
		t.Fatalf("a refused accept was published (%d reviews)", n)
	}
	if got := p.status(silent); got != "integration" {
		t.Fatalf("silent accept left the item at %s", got)
	}
	if runs := p.gateRuns(silent); len(runs) != 2 || runs[0].Gate != "pr-pending" || runs[1].Result != "reject" ||
		!strings.Contains(runs[1].Notes, "after satelle-pr-review-publish: not run: after satelle-verdict-notes-check refused the verdict") ||
		!strings.Contains(runs[1].Notes, "after "+statusSkill+": satelle/pr-review failure on "+p.head[:12]+", posted by the satelle app (account "+appAccount+")") ||
		!strings.Contains(runs[1].Answer, `"verdict":"accept","notes":""`) {
		t.Fatalf("silent accept gate runs %+v", runs)
	}
	if got := p.statusesSince(n0); !slices.Equal(got, append(slices.Clone(pendingStatuses), "satelle/pr-review failure")) {
		t.Fatalf("silent accept statuses %v", got)
	}

	approved := p.item(21, true)
	p.judge("accept", "every criterion is met: AC1 to AC4 hold on the head of the pull request")
	p.serveReviewsAs("approved-on-head", 1)
	n0 = p.statusCount()
	if r := p.present(approved); r.code != 0 {
		t.Fatalf("accept: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if got := p.status(approved); got != "approved" {
		t.Fatalf("status %s, want approved", got)
	}
	revs := p.srv.PublishedReviews("acme")
	if len(revs) != 1 {
		t.Fatalf("published %d reviews", len(revs))
	}
	rv := revs[0]
	if rv.GetEvent() != "APPROVE" || rv.GetCommitSha() != p.head || rv.GetPullNumber() != 21 {
		t.Fatalf("published %v", rv)
	}
	for _, want := range []string{"project acme", "gate pr-review (" + p.reviewer + ") on " + approved, "head " + p.head[:12] + ": approve", "every criterion is met"} {
		if !strings.Contains(rv.GetBody(), want) {
			t.Errorf("body lacks %q:\n%s", want, rv.GetBody())
		}
	}
	gr := p.githubReviews(approved)
	if len(gr) != 1 || gr[0]["event"] != "APPROVE" || gr[0]["commit_sha"] != p.head || gr[0]["pull_number"] != float64(21) ||
		gr[0]["repository"] != "orbelle/acme" || gr[0]["review_id"] != float64(1) || !strings.HasPrefix(gr[0]["html_url"].(string), "https://github.com/orbelle/acme/pull/21") ||
		len(gr[0]["body_sha256"].(string)) != 64 {
		t.Fatalf("github.review %v", gr)
	}
	runs := p.gateRuns(approved)
	if len(runs) != 3 || runs[0].Gate != "pr-pending" || runs[0].Result != "accept" ||
		runs[1].Gate != "pr-review" || runs[1].Result != "accept" || !strings.Contains(runs[1].Notes, "after satelle-pr-review-publish: published approval") ||
		runs[2].Gate != "pr-approval" || runs[2].Result != "accept" {
		t.Fatalf("gate runs %+v", runs)
	}
	for _, c := range []string{"satelle/pr-ready", "satelle/pr-review", "satelle/pr-approval"} {
		if !strings.Contains(runs[0].Notes, c+" pending on "+p.head[:12]) {
			t.Errorf("pr-pending notes lack %s: %q", c, runs[0].Notes)
		}
	}
	for _, x := range runs[1:] {
		if want := "after " + statusSkill + ": satelle/" + x.Gate + " success on " + p.head[:12] + ", posted by the satelle app (account " + appAccount + ")"; !strings.Contains(x.Notes, want) {
			t.Errorf("%s notes lack %q: %q", x.Gate, want, x.Notes)
		}
	}
	if got := p.statusesSince(n0); !slices.Equal(got, append(slices.Clone(pendingStatuses), "satelle/pr-review success", "satelle/pr-approval success")) {
		t.Fatalf("approved statuses %v", got)
	}
	for _, s := range p.srv.PublishedStatuses("acme")[n0+3:] {
		if !strings.HasPrefix(s.GetDescription(), "accept (integration->approved): ") {
			t.Errorf("description %q", s.GetDescription())
		}
	}
	if d := p.srv.PublishedStatuses("acme")[n0+3].GetDescription(); d != "accept (integration->approved): every criterion is met: AC1 to AC4 hold on the head of the pull request" {
		t.Errorf("pr-review description %q", d)
	}

	p.serveReviews("changes-requested")
	block := idBlock(t, ".satelle/skills/release.md", "### Reviews on the heads", approved)
	if strings.Contains(block, "satelle github") {
		t.Fatal("the release review read calls satelle.dev")
	}
	r := p.exec("bash", "", "-c", block)
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

	id := p.item(22, true)
	created := len(p.ledger(id, ""))
	p.serveReviews("none")
	p.judge("reject", "AC2 is not met: a reject never reopens the item at in_progress")
	n0 = p.statusCount()
	r = p.present(id)
	if r.code != 2 || !strings.Contains(r.err, "reopened at in_progress") {
		t.Fatalf("reject: exit %d\n%s%s", r.code, r.out, r.err)
	}
	revs = p.srv.PublishedReviews("acme")
	if len(revs) != 2 || revs[1].GetEvent() != "REQUEST_CHANGES" || revs[1].GetCommitSha() != p.head || revs[1].GetPullNumber() != 22 {
		t.Fatalf("published %v", revs)
	}
	body := revs[1].GetBody()
	if !strings.Contains(body, "request changes") || !strings.Contains(body, "AC2 is not met") {
		t.Fatalf("body %q", body)
	}
	if got := p.status(id); got != "in_progress" {
		t.Fatalf("status %s, want in_progress", got)
	}
	if got := p.latestAttachment(id, "pr-review"); got != body {
		t.Fatalf("pr-review = %q, want the published body %q", got, body)
	}
	evs := p.ledger(id, "")
	if len(evs) < created {
		t.Fatal("the chain lost events")
	}
	var kinds []string
	for _, e := range evs[created:] {
		if e.Item != id {
			t.Fatalf("event of %s on %s's chain", e.Item, id)
		}
		kinds = append(kinds, e.Kind)
	}
	if got := p.statusesSince(n0); !slices.Equal(got, append(slices.Clone(pendingStatuses), "satelle/pr-review failure")) {
		t.Fatalf("reject statuses %v", got)
	}
	if d := p.srv.PublishedStatuses("acme")[n0+3].GetDescription(); !strings.HasPrefix(d, "reject (integration->approved): AC2 is not met") {
		t.Errorf("reject description %q", d)
	}
	if got := strings.Join(kinds, " "); got != "gate.start gate.run gate.start github.review attachment.added item.transitioned gate.run" {
		t.Fatalf("chain after the reject: %s", got)
	}
	if p.transitions(id, "integration", "in_progress") != 1 {
		t.Fatal("no integration -> in_progress on the chain")
	}
	p.must("ledger", "verify")

	if r := p.run("story", "perform", id); r.code != 0 {
		t.Fatalf("perform: exit %d\n%s%s", r.code, r.out, r.err)
	}
	prompt, err := os.ReadFile(filepath.Join(p.fake, "prompt.ex-1"))
	if err != nil {
		t.Fatal(err)
	}
	header, _, _ := strings.Cut(body, "\n")
	if !strings.Contains(string(prompt), "Attachment pr-review") || !strings.Contains(string(prompt), header) {
		t.Fatalf("the coder's prompt lacks the review:\n%s", prompt)
	}

	p.must("story", "set", id, "--status", "integration")
	if r := p.present(id); r.code != 2 {
		t.Fatalf("repeat reject: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if n := p.published(); n != 2 {
		t.Fatalf("a repeat of the same findings published again (%d)", n)
	}
	if got := p.status(id); got != "in_progress" || p.transitions(id, "integration", "in_progress") != 2 {
		t.Fatalf("repeat reject: status %s, %d reopenings", got, p.transitions(id, "integration", "in_progress"))
	}
	p.must("story", "set", id, "--status", "integration")
	p.judge("reject", "AC3 is not met either: the review body is never attached as pr-review")
	if r := p.present(id); r.code != 2 {
		t.Fatalf("new findings: exit %d\n%s%s", r.code, r.out, r.err)
	}
	revs = p.srv.PublishedReviews("acme")
	if len(revs) != 3 || revs[2].GetEvent() != "REQUEST_CHANGES" || !strings.Contains(revs[2].GetBody(), "AC3 is not met either") {
		t.Fatalf("published %v", revs)
	}
	if gr := p.githubReviews(id); len(gr) != 2 || gr[0]["event"] != "REQUEST_CHANGES" || gr[1]["event"] != "REQUEST_CHANGES" {
		t.Fatalf("github.review %v", gr)
	}
	if got := p.latestAttachment(id, "pr-review"); got != revs[2].GetBody() {
		t.Fatalf("pr-review = %q", got)
	}
	if got := p.status(id); got != "in_progress" {
		t.Fatalf("status %s", got)
	}

	p.must("story", "set", id, "--status", "integration")
	p.judge("accept", "fine now: AC2 and AC3 hold, the item reopens with the review attached")
	p.serveReviews("changes-requested")
	n0 = p.statusCount()
	r = p.present(id)
	if r.code != 2 || !strings.Contains(r.err, "gate pr-approval") {
		t.Fatalf("re-present: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if n := p.published(); n != 3 {
		t.Fatalf("an approval was published over requested changes (%d reviews)", n)
	}
	if got := p.statusesSince(n0); !slices.Equal(got, append(slices.Clone(pendingStatuses), "satelle/pr-review success", "satelle/pr-approval failure")) {
		t.Fatalf("re-present statuses %v: none may stay success from an earlier presentation", got)
	}
	if got := p.status(id); got != "in_progress" {
		t.Fatalf("status %s, want in_progress", got)
	}
	if got := p.latestAttachment(id, "pr-review"); got != fixtureBody(t, "changes-requested") {
		t.Fatalf("pr-review = %q", got)
	}
	p.must("story", "set", id, "--status", "integration")
	p.serveReviews("none")
	if r := p.present(id); r.code != 2 || !strings.Contains(r.err, "no approving review") {
		t.Fatalf("no review: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if got := p.status(id); got != "integration" {
		t.Fatalf("status %s, want integration", got)
	}
	if n := p.published(); n != 3 {
		t.Fatalf("published %d", n)
	}
	p.must("ledger", "verify")
}

func TestPRReviewGatesAreOnTheRoutes(t *testing.T) {
	files, err := filepath.Glob("../.satelle/workflows/*.toml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no workflows: %v", err)
	}
	want := append([]string{"pr-pending", "integration-check", "pr-ready"}, prRouteTail...)
	routed := 0
	for _, f := range files {
		w, err := workflow.Parse(f)
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(f)
		route := name == "delivery.toml" || name == "chore.toml"
		for _, tr := range w.Transitions {
			onRoute := route && slices.Equal(tr.From, []string{"integration"}) && tr.To == "approved"
			for _, g := range tr.Groups {
				if !onRoute && (slices.Contains(g.Gates, "pr-review") || slices.Contains(g.Gates, "pr-approval")) {
					t.Errorf("%s: %v -> %s runs %v", name, tr.From, tr.To, g.Gates)
				}
			}
		}
		if !route {
			continue
		}
		routed++
		tr, err := w.Find("integration", "approved", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(tr.Groups) != 1 || tr.Groups[0].Mode != workflow.Sequential || !slices.Equal(tr.Groups[0].Gates, want) {
			t.Errorf("%s: integration -> approved runs %+v, want one sequential group %v (ending with the sandbox's prRouteTail)", name, tr.Groups, want)
		}
	}
	if routed != 2 {
		t.Fatalf("checked %d of delivery.toml and chore.toml", routed)
	}
	if strings.Contains(shBlock0(t, ".satelle/skills/satelle-pr-ready-check.md"), `echo "approved:`) {
		t.Error("pr-ready still approves")
	}
	skills, _ := filepath.Glob("../.satelle/skills/*.md")
	for _, f := range skills {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if base := filepath.Base(f); strings.Contains(string(b), "satelle github") && base != "satelle-pr-review-publish.md" && base != statusSkill+".md" {
			t.Errorf("%s calls satelle github", base)
		}
	}
	review, _ := repoPRGates(t)
	if n := len(review.After); n < 2 || review.After[n-2] != "satelle-pr-review-publish" || review.After[n-1] != statusSkill {
		t.Errorf("pr-review's after is %v: want the publish check immediately before the status entry, last", review.After)
	}
	block := shBlock0(t, ".satelle/skills/"+statusSkill+".md")
	m := regexp.MustCompile(`(?m)^gates="([^"]*)"$`).FindStringSubmatch(block)
	if m == nil {
		t.Fatal("the status skill has no gates= line")
	}
	var posting []string
	for name, g := range repoPRGateSet(t) {
		if n := len(g.After); n > 0 && g.After[n-1] == statusSkill {
			posting = append(posting, name)
		}
	}
	slices.Sort(posting)
	named := strings.Fields(m[1])
	slices.Sort(named)
	if !slices.Equal(posting, named) {
		t.Errorf("gates whose after ends with the status entry %v, the skill's gates= line %v", posting, named)
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
		for _, w := range []string{"satelle/pr-", statusSkill, "pr-pending", "satelle-pr-review-publish"} {
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
	gates := `[gates.pr-ready]
skill = "stub-ready"
timeout = "3s"
after = ["` + statusSkill + `"]
[gates.pr-review]
skill = "satelle-code-ac-review"
agent = "` + p.reviewer + `"
after = ["` + statusSkill + `"]
`
	p.write(".satelle/workflows/delivery.toml", prSandboxWorkflow(gates, []string{"pr-ready", "pr-review"}))
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
		{"ready passes", "", "accept", "every criterion is met: the stub is ready and the judge accepts", false, "pr-ready", "accept", "success", "ready"},
		{"ready fails", "fail", "", "", false, "pr-ready", "reject", "failure", "not ready"},
		{"ready times out", "hang", "", "", false, "pr-ready", "reject", "failure", "timed out"},
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
		n0 := p.statusCount()
		p.present(id)
		run := lastRun(p.gateRuns(id), c.gate)
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
		if c.name == "long note" {
			d := got[len(got)-1].GetDescription()
			if want := ("accept (integration->approved): " + long)[:140]; d != want {
				t.Errorf("long note: description %q (%d), want the first 140 characters", d, len(d))
			}
		}
	}
	os.Remove(dead)
	writeFile(t, ready, "", 0o644)

	block := shBlock0(t, ".satelle/skills/"+statusSkill+".md")
	p.env["SATELLE_ITEM"], p.env["SATELLE_FROM"], p.env["SATELLE_TO"] = "sto_x", "integration", "approved"
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
	p.env["SATELLE_GATE"] = "code-ac"
	n0 := p.statusCount()
	if r := p.exec("bash", `{"result":"accept"}`, "-c", block); r.code != 0 || p.statusCount() != n0 || !strings.Contains(r.out, "posts no status") {
		t.Errorf("a gate not on gates=: exit %d\n%s", r.code, r.out)
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

	id := p.item(31, true)
	p.judge("accept", "every criterion is met: AC1 to AC4 hold on the head of the pull request")
	p.serveReviewsAs("approved-on-head", 1)
	if r := p.present(id); r.code != 0 {
		t.Fatalf("accept under a refusal: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if got := p.status(id); got != "approved" {
		t.Fatalf("status %s", got)
	}
	runs := p.gateRuns(id)
	if len(runs) != 3 {
		t.Fatalf("gate runs %+v", runs)
	}
	for _, x := range runs {
		if x.Result != "accept" {
			t.Errorf("%s: %s", x.Gate, x.Result)
		}
	}
	for _, c := range []string{"satelle/pr-ready", "satelle/pr-review", "satelle/pr-approval"} {
		if !strings.Contains(runs[0].Notes, c+" not published on "+p.head[:12]+": ") || !strings.Contains(runs[0].Notes, refusal) {
			t.Errorf("pr-pending notes for %s: %q", c, runs[0].Notes)
		}
	}
	for _, x := range runs[1:] {
		if want := "after " + statusSkill + ": satelle/" + x.Gate + " not published on " + p.head[:12] + ": "; !strings.Contains(x.Notes, want) || !strings.Contains(x.Notes, refusal) {
			t.Errorf("%s notes lack %q and the refusal: %q", x.Gate, want, x.Notes)
		}
	}
	if n := p.statusCount(); n != 0 {
		t.Fatalf("published %d statuses under a refusal", n)
	}

	rej := p.item(32, true)
	p.serveReviews("none")
	p.judge("reject", "AC2 is not met: the statuses are never posted on the head")
	if r := p.present(rej); r.code != 2 || !strings.Contains(r.err, "reopened at in_progress") {
		t.Fatalf("reject under a refusal: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if run := lastRun(p.gateRuns(rej), "pr-review"); run.Result != "reject" || !strings.Contains(run.Notes, "satelle/pr-review not published") {
		t.Fatalf("reject gate.run %+v", run)
	}
	if got := p.status(rej); got != "in_progress" {
		t.Fatalf("status %s", got)
	}

	p.srv.RefuseStatuses("acme", "")
	dry := p.item(33, true)
	r := p.run("gate", "run", "delivery/pr-pending", "--item", dry)
	if !strings.Contains(r.out+r.err, "a dry run publishes no status") || p.statusCount() != 0 {
		t.Fatalf("dry run: exit %d, %d statuses\n%s%s", r.code, p.statusCount(), r.out, r.err)
	}
}
