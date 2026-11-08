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
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
	"github.com/bobmcallan/satelle/internal/workflow"
)

// The satelle GitHub app's account (satelle[bot], app 5379260): the user.id
// its reviews carry (tests/testdata/github/reviews/README.md).
const appAccount = "344745317"

// orbelleReviewerAccount is orbelle-reviewer[bot]'s user.id (app 5563015):
// internal only, so its reviews never count.
const orbelleReviewerAccount = 350948835

// The review GitHub re-pointed (tests/testdata/github/reviews/README.md):
// satelle[bot] approved orbelle/satelle#41 on repointedGiven, which
// sto_e809d7d6's github.review ledger event 4215b0fe records, and GitHub's
// list-reviews endpoint now reports it on repointedReported, a later head.
const (
	repointedReviewID = 5815187973
	repointedGiven    = "6109ffe2f71d13665ce69edb01104e3599067a8d"
	repointedReported = "f93eef33d5814570f648a88df4f207b23e9f9996"
)

// prRouteTail is the gates integration -> approved runs after pr-ready, in
// order: the sandbox route and this repo's delivery and chore routes share it.
var prRouteTail = []string{"pr-review", "pr-approval"}

// tomlGates renders gate names as a TOML array's elements.
func tomlGates(gates []string) string {
	return `"` + strings.Join(gates, `", "`) + `"`
}

// repoPRGates returns this repo's pr-review and pr-approval gates as
// .satelle/workflows/delivery.toml defines them, after checking chore.toml
// defines them identically.
func repoPRGates(t *testing.T) (review, approval workflow.Gate) {
	t.Helper()
	var got [2][2]workflow.Gate
	for i, wf := range []string{"delivery", "chore"} {
		w, err := workflow.Parse("../.satelle/workflows/" + wf + ".toml")
		if err != nil {
			t.Fatal(err)
		}
		for j, name := range []string{"pr-review", "pr-approval"} {
			g, ok := w.Gates[name]
			if !ok {
				t.Fatalf("%s.toml defines no gate %s", wf, name)
			}
			got[i][j] = g
		}
	}
	for j := range 2 {
		a, b := got[0][j], got[1][j]
		if a.Skill != b.Skill || a.Agent != b.Agent || a.After != b.After || a.Carry != b.Carry || a.Trigger != b.Trigger ||
			!slices.Equal(a.Panel, b.Panel) || !slices.Equal(a.Paths, b.Paths) || a.Combine != b.Combine {
			t.Fatalf("delivery and chore define the gate differently: %+v vs %+v", a, b)
		}
	}
	return got[0][0], got[0][1]
}

// gateTOML is a gate as a TOML table, refusing fields the sandbox would drop.
func gateTOML(t *testing.T, name string, g workflow.Gate) string {
	t.Helper()
	if len(g.Panel) > 0 || len(g.Paths) > 0 || g.Trigger != "" || g.Combine != "" {
		t.Fatalf("gate %s has fields the sandbox does not copy: %+v", name, g)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[gates.%s]\nskill = %q\n", name, g.Skill)
	if g.Agent != "" {
		fmt.Fprintf(&b, "agent = %q\n", g.Agent)
	}
	if g.Carry {
		b.WriteString("carry = true\n")
	}
	if g.After != "" {
		fmt.Fprintf(&b, "after = %q\n", g.After)
	}
	return b.String()
}

// prSandbox is a repo bound to a fake satelle.dev project ("acme", bound to
// orbelle/acme), carrying this repo's skills and its pr-review and
// pr-approval gates wired, in the sandbox only, into integration -> approved.
// A fake claude judges (its verdict and notes come from files) and performs
// (saving its prompt); a fake gh serves the pull requests and reviews.
type prSandbox struct {
	*sandbox
	srv  *fakeserver.Server
	fake string
	head string
	old  string
	prs  []map[string]any
}

func newPRSandbox(t *testing.T) *prSandbox {
	t.Helper()
	for _, tool := range []string{"bash", "jq", "git", "sha256sum"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s is required by the PR review skills and is not on PATH", tool)
		}
	}
	srv := fakeserver.Start(t, syncToken)
	s := signedIn(t)
	s.must("project", "create", "acme", "--bind", "--server", srv.URL)
	srv.BindGitHub("acme", "orbelle/acme")

	review, approval := repoPRGates(t)
	if review.Skill != "satelle-code-ac-review" || review.Agent != "reviewer-alt" || review.After != "satelle-pr-review-publish" {
		t.Fatalf("pr-review is %+v, want the code-ac judge on reviewer-alt with the publish after", review)
	}
	if approval.Skill != "satelle-pr-approval-check" || approval.Agent != "" || approval.After != "" {
		t.Fatalf("pr-approval is %+v", approval)
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
	s.write(".satelle/agents.toml", `[agents.reviewer-alt]
command = "claude"
model = "ra-1"
tools = ["Read", "Grep", "Glob"]
[agents.executor]
command = "claude"
model = "ex-1"
tools = ["Read", "Edit"]
`)
	s.write(".satelle/workflows/delivery.toml", `name = "delivery"
[states.backlog]
start = true
[states.in_progress]
performer = { agent = "executor", skill = "coder" }
[states.integration]
[states.approved]
[states.done]
terminal = true

`+gateTOML(t, "pr-review", review)+gateTOML(t, "pr-approval", approval)+`
[[transition]]
from = ["backlog"]
to = "in_progress"
[[transition]]
from = ["in_progress"]
to = "integration"
[[transition]]
from = ["integration"]
to = "approved"
groups = [{ mode = "sequential", gates = [`+tomlGates(prRouteTail)+`] }]
[[transition]]
from = ["integration", "approved"]
to = "in_progress"
[[transition]]
from = ["approved"]
to = "done"
`)
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
printf '{"type":"result","subtype":"success","is_error":false,"result":"{\\"verdict\\":\\"%s\\",\\"notes\\":\\"%s\\"}","usage":{"input_tokens":11,"output_tokens":7}}\n' "$v" "$n"
`, 0o755)
	writeFile(t, filepath.Join(fake, "gh"), `#!/bin/sh
case "$1" in
  pr) cat "$FAKE_DIR/prs.json" ;;
  api) printf '%s\n' "$*" >> "$FAKE_DIR/gh-api.log"; cat "$FAKE_DIR/reviews.json" ;;
  *) echo "fake gh: unexpected $*" >&2; exit 1 ;;
esac
`, 0o755)
	s.path = append(s.path, fake)
	s.env["FAKE_DIR"] = fake

	s.git("add", "-A")
	s.git("commit", "-q", "-m", "config")
	s.must("validate")
	p := &prSandbox{sandbox: s, srv: srv, fake: fake}
	p.old = strings.TrimSpace(s.git("rev-parse", "HEAD"))
	s.write("src/main.go", "package main\n")
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "the change")
	p.head = strings.TrimSpace(s.git("rev-parse", "HEAD"))
	p.serveReviews("none")
	return p
}

// item creates a story at integration whose PR (number n) is open on HEAD,
// titled with its id unless titled is false.
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

// serveReviews makes the fake gh answer every reviews read with a fixture,
// its @HEAD@ and @OLD@ bound to this sandbox's commits.
func (p *prSandbox) serveReviews(fixture string) {
	p.t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "github", "reviews", fixture+".json"))
	if err != nil {
		p.t.Fatal(err)
	}
	body := strings.NewReplacer("@HEAD@", p.head, "@OLD@", p.old).Replace(string(b))
	writeFile(p.t, filepath.Join(p.fake, "reviews.json"), body, 0o644)
}

// serveReviewsAs serves a fixture as serveReviews does, its first review's id
// set to reviewID: the id the fake satelle.dev gave the published review.
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

// fixtureBody is the body of a fixture's last review.
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

// judge sets the fake judge's next verdict and notes.
func (p *prSandbox) judge(verdict, notes string) {
	p.t.Helper()
	writeFile(p.t, filepath.Join(p.fake, "verdict"), verdict, 0o644)
	writeFile(p.t, filepath.Join(p.fake, "notes"), notes, 0o644)
}

// present presents integration -> approved once.
func (p *prSandbox) present(id string) result {
	p.t.Helper()
	return p.run("story", "set", id, "--status", "approved")
}

func (p *prSandbox) status(id string) string { p.t.Helper(); return p.get(id).Status }

// latestAttachment is the body of the newest attachment named name.
func (p *prSandbox) latestAttachment(id, name string) string {
	p.t.Helper()
	r := p.run("story", "attachment", id, name)
	if r.code != 0 {
		p.t.Fatalf("story attachment %s %s: exit %d\n%s%s", id, name, r.code, r.out, r.err)
	}
	return r.out
}

func (p *prSandbox) published() int { return len(p.srv.PublishedReviews("acme")) }

// githubReviews returns id's github.review payloads, oldest first.
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

// transitions counts id's item.transitioned events from -> to.
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

// The approval check passes only on the app account's latest approving review
// on HEAD, matched by its numeric id; requested changes on HEAD reopen the
// item; a PR whose title lacks the item id is never read.
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
	// The item's ledger records what the publish check published: the
	// approval the on-head fixtures carry, given on HEAD, and the re-pointed
	// fixture's approval, given on the older head.
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

	// An approval on HEAD whose review_id the item's ledger does not record
	// is refused and never moves the item.
	unrecorded := p.item(14, true)
	p.serveReviews("approved-on-head")
	if r := run(unrecorded); r.code == 0 || !strings.Contains(r.out, "is not on the item's ledger") {
		t.Errorf("unrecorded approval: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if got := p.status(unrecorded); got != "integration" {
		t.Errorf("unrecorded item moved to %s", got)
	}

	// A PR whose title lacks the item id maps to no item: rejected, never
	// read, never reopened.
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

// GitHub re-points an older approval's commit_id to the PR's new head: the
// recorded response reports review 5815187973 on f93eef33, not on 6109ffe2
// where the ledger records it was given. approved-repointed.json is that
// review with only commit_id bound to the sandbox's HEAD.
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

// AC1, AC3 and AC4 through the gates as this repo defines them, wired into the
// sandbox's integration -> approved: the judge's verdict is published on the
// PR head through the fake satelle.dev; a reject (the judge's own, or the
// app's seen on a re-present) reopens the item with the review attached as
// pr-review, which the coder's next run reads.
func TestPRReviewGatePublishesAndReopens(t *testing.T) {
	p := newPRSandbox(t)

	// AC4: a PR titled without the item id is never published to.
	untitled := p.item(20, false)
	p.judge("accept", "fine")
	if r := p.present(untitled); r.code != 2 || !strings.Contains(r.err, "no open pull request") {
		t.Fatalf("untitled: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if n := p.published(); n != 0 {
		t.Fatalf("published %d reviews for a PR mapped to no item", n)
	}
	if got := p.status(untitled); got != "integration" {
		t.Fatalf("untitled item moved to %s", got)
	}

	// AC1: a judge accept is published as APPROVE on HEAD; the approval check
	// then reads the app's approval and the item moves.
	approved := p.item(21, true)
	p.judge("accept", "every criterion is met")
	// GitHub reports the review the publish check published: the fake
	// satelle.dev's first review id, which github.review records below.
	p.serveReviewsAs("approved-on-head", 1)
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
	for _, want := range []string{"project acme", "gate pr-review (reviewer-alt) on " + approved, "head " + p.head[:12] + ": approve", "every criterion is met"} {
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
	if len(runs) != 2 || runs[0].Gate != "pr-review" || runs[0].Result != "accept" || !strings.Contains(runs[0].Notes, "after satelle-pr-review-publish: published approval") ||
		runs[1].Gate != "pr-approval" || runs[1].Result != "accept" {
		t.Fatalf("gate runs %+v", runs)
	}

	// AC3 (c): the release run's step-0 review read sends back an approved
	// item whose PR head carries the app's requested changes.
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

	// AC3 (a): the judge's own reject is published as REQUEST_CHANGES and the
	// one presenting command leaves the item at in_progress with the body
	// attached.
	id := p.item(22, true)
	created := len(p.ledger(id, ""))
	p.serveReviews("none")
	p.judge("reject", "AC2 is not met")
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
	if got := strings.Join(kinds, " "); got != "github.review attachment.added item.transitioned gate.run" {
		t.Fatalf("chain after the reject: %s", got)
	}
	if p.transitions(id, "integration", "in_progress") != 1 {
		t.Fatal("no integration -> in_progress on the chain")
	}
	p.must("ledger", "verify")

	// AC3 (d): the coder's next run reads the review from the record.
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

	// AC3 (e): a repeated reject of the same head. Same findings: nothing new
	// is published, and the item reopens again. Different findings: a second
	// REQUEST_CHANGES, recorded and attached.
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
	p.judge("reject", "AC3 is not met either")
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

	// AC3 (b): re-presented on the same head, the judge now accepts, but the
	// app's CHANGES_REQUESTED stands: no approval is published over it, and
	// the approval check reopens the item with the app's review.
	p.must("story", "set", id, "--status", "integration")
	p.judge("accept", "fine now")
	p.serveReviews("changes-requested")
	r = p.present(id)
	if r.code != 2 || !strings.Contains(r.err, "gate pr-approval") {
		t.Fatalf("re-present: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if n := p.published(); n != 3 {
		t.Fatalf("an approval was published over requested changes (%d reviews)", n)
	}
	if got := p.status(id); got != "in_progress" {
		t.Fatalf("status %s, want in_progress", got)
	}
	if got := p.latestAttachment(id, "pr-review"); got != fixtureBody(t, "changes-requested") {
		t.Fatalf("pr-review = %q", got)
	}
	// With no review on GitHub the check rejects and nothing reopens.
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

// AC1: the operator's confirmation sits between integration and approved on
// both routes: confirm is the operator's (it records a confirmation), entered
// only from integration, left by gateless moves to approved or back to
// in_progress; there is no integration -> approved; only approved enqueues the
// release queue, which still fires by hand.
func TestConfirmationIsOnTheRoutes(t *testing.T) {
	for _, wf := range []string{"delivery", "chore"} {
		w, err := workflow.Parse("../.satelle/workflows/" + wf + ".toml")
		if err != nil {
			t.Fatal(err)
		}
		if typ, ok := w.States["confirm"].Operated(); !ok || typ != "confirmation" {
			t.Errorf("%s: confirm is not the operator's confirmation (%q, %v)", wf, typ, ok)
		}
		if _, err := w.Find("integration", "approved", ""); err == nil {
			t.Errorf("%s: integration -> approved skips the confirmation", wf)
		}
		for _, to := range []string{"approved", "in_progress"} {
			tr, err := w.Find("confirm", to, "")
			if err != nil {
				t.Errorf("%s: confirm -> %s: %v", wf, to, err)
				continue
			}
			if len(tr.Groups) != 0 || !slices.Equal(tr.From, []string{"confirm"}) {
				t.Errorf("%s: confirm -> %s is %+v, want an explicit gateless move", wf, to, tr)
			}
		}
		for _, tr := range w.Transitions {
			if tr.To == "confirm" && !slices.Equal(tr.From, []string{"integration"}) {
				t.Errorf("%s: confirm is entered from %v", wf, tr.From)
			}
			if tr.To == "approved" && !slices.Equal(tr.From, []string{"confirm"}) {
				t.Errorf("%s: approved is entered from %v", wf, tr.From)
			}
		}
		for name, st := range w.States {
			want := name == "approved"
			if got := slices.Equal(st.Enqueue, []string{"release"}); got != want {
				t.Errorf("%s: state %s enqueues %v", wf, name, st.Enqueue)
			}
		}
	}
	var q struct {
		ID     string `toml:"id"`
		Title  string `toml:"title"`
		Filter struct {
			Kind   string `toml:"kind"`
			Status string `toml:"status"`
		} `toml:"filter"`
		Trigger struct {
			Task    string `toml:"task"`
			Base    string `toml:"base"`
			MinSize *int   `toml:"min_size"`
		} `toml:"trigger"`
	}
	if err := workflow.DecodeStrict("../.satelle/queues/release.toml", &q); err != nil {
		t.Fatal(err)
	}
	if q.Filter.Status != "approved" || q.Trigger.MinSize == nil || *q.Trigger.MinSize != 0 {
		t.Errorf("release queue %+v: want status approved, fired by hand (min_size 0)", q)
	}
}

// sto_efe81afd AC1: delivery and chore run integration -> confirm through
// pr-ready, then the review gate and the approval check, in one sequential
// group; no other transition runs them; pr-ready no longer approves; only the
// publish check calls satelle.dev.
func TestPRReviewGatesAreOnTheRoutes(t *testing.T) {
	files, err := filepath.Glob("../.satelle/workflows/*.toml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no workflows: %v", err)
	}
	want := append(append([]string{"integration-check", "pr-ready"}, prRouteTail...), "result-summary")
	routed := 0
	for _, f := range files {
		w, err := workflow.Parse(f)
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(f)
		route := name == "delivery.toml" || name == "chore.toml"
		for _, tr := range w.Transitions {
			onRoute := route && slices.Equal(tr.From, []string{"integration"}) && tr.To == "confirm"
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
		tr, err := w.Find("integration", "confirm", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(tr.Groups) != 1 || tr.Groups[0].Mode != workflow.Sequential || !slices.Equal(tr.Groups[0].Gates, want) {
			t.Errorf("%s: integration -> confirm runs %+v, want one sequential group %v (the sandbox's prRouteTail, then the result summary)", name, tr.Groups, want)
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
		if strings.Contains(string(b), "satelle github") && filepath.Base(f) != "satelle-pr-review-publish.md" {
			t.Errorf("%s calls satelle github", filepath.Base(f))
		}
	}
	review, _ := repoPRGates(t)
	if review.After != "satelle-pr-review-publish" {
		t.Errorf("the publish check is reached as %q, not pr-review's after", review.After)
	}
	if block := idBlock(t, ".satelle/skills/release.md", "### Reviews on the heads", "sto_x"); strings.Contains(block, "satelle github") {
		t.Error("the release review read calls satelle github")
	}
}

// shBlock0 is the first ```sh block of a repo file, unchanged.
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
	return block
}

// idBlock is shBlock's sibling for a block run once per item: the first ```sh
// block after heading, with its single id= line rebound to id.
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
	return re.ReplaceAllString(block, "id="+id)
}
