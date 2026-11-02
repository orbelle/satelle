package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/forge"
	"github.com/bobmcallan/satelle/internal/remotedef"
	"github.com/bobmcallan/satelle/internal/remotenode/devtest"
)

const (
	prRepo       = "orbelle/satelle"
	prToken      = "ghs_pr-approve-token"
	prHead       = "1111111111111111111111111111111111111111"
	prNumber     = 42
	prDiffMarker = "DIFF-SENTINEL-c0ffee"
	prCritMarker = "CRITERIA-SENTINEL-5a5a"
	prConstMark  = "CONSTITUTION-SENTINEL-77aa"
)

// prNode is a node-like repo: no story in its store, the node definition in its
// environment naming orbelle/satelle and the github-app secret GH_TOKEN, the
// token the fake GitHub minted for that repo, and the forge pointed at the fake.
type prNode struct {
	repo string
	gh   *devtest.GitHub
	exe  string
}

// newPRNode makes the node repo and its execution for story. withConstitution installs principle
// repo-constitution, as a bundle carrying it would.
func newPRNode(t *testing.T, story string, withConstitution bool) *prNode {
	t.Helper()
	repo := tempRepo(t)
	t.Chdir(repo)
	if withConstitution {
		writeRepoFile(t, repo, ".satelle/principles/repo-constitution.md",
			"---\nname: repo-constitution\ntype: principle\ndescription: the repo's constitution\n---\n\n# Constitution\n\n"+prConstMark+"\n")
	}
	writeRepoFile(t, repo, ".satelle/constitution.md", "# constitution\n\n"+prConstMark+"\n")
	if out, err := runRoot(t, "reindex"); err != nil {
		t.Fatalf("reindex: %v\n%s", err, out)
	}
	gh := devtest.NewGitHub(t)
	gh.Mint(prToken, prRepo)
	prevURL, prevHTTP := forgeAPIURL, forgeHTTP
	forgeAPIURL, forgeHTTP = gh.URL, gh.Client()
	t.Cleanup(func() { forgeAPIURL, forgeHTTP = prevURL, prevHTTP })

	def := remotedef.Definition{
		Repos:  []remotedef.Repo{{URL: "https://github.com/" + prRepo + ".git", Ref: "main"}},
		Secret: []remotedef.SecretDecl{{Key: "GH_TOKEN", Scope: remotedef.ScopeTeam, From: remotedef.FromGitHubApp}},
	}
	env, err := def.EnvValue()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(remotedef.EnvDefinition, env)
	t.Setenv("GH_TOKEN", prToken)

	out, err := runRoot(t, "execution", "create", "--title", "pr approval run", "--parent", "tsk_pr-approve", "--input", "story_id="+story)
	if err != nil {
		t.Fatalf("execution create: %v\n%s", err, out)
	}
	return &prNode{repo: repo, gh: gh, exe: idOf(t, out)}
}

// addPR serves the open PR on epic/<story> with body.
func (n *prNode) addPR(story, body string) {
	n.gh.AddPR(devtest.PR{Repo: prRepo, Number: prNumber, HeadRef: forge.EpicHead(story), HeadSHA: prHead, Body: body,
		Diff: "diff --git a/x.go b/x.go\n+// " + prDiffMarker + "\n"})
}

// approve runs `pr approve` for story on the node's execution.
func (n *prNode) approve(t *testing.T, story string) string {
	t.Helper()
	out, err := runRoot(t, "pr", "approve", "--story", story, "--execution", n.exe)
	if err != nil {
		t.Fatalf("pr approve: %v\n%s", err, out)
	}
	return out
}

// runOutput is the execution's run-output doc.
func (n *prNode) runOutput(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(n.repo, ".satelle", "tasks", "tsk_pr-approve", "output-"+n.exe+".md"))
	if err != nil {
		t.Fatalf("run-output doc: %v", err)
	}
	return string(b)
}

// stubReview replaces the cold review with verdict, counting dispatches; during,
// when set, runs inside the review (between the PR read and the approval).
func stubReview(t *testing.T, v forge.Verdict, during func()) *int {
	t.Helper()
	calls := new(int)
	prev := prReview
	prReview = func(_ context.Context, _ *cobra.Command, _ string, req forge.ReviewRequest) (forge.Verdict, error) {
		*calls++
		if during != nil {
			during()
		}
		return v, nil
	}
	t.Cleanup(func() { prReview = prev })
	return calls
}

// criteriaBody renders a story's criteria block in a local repo holding the
// story, the way the PR opener writes it, and returns it with the story id.
// The node repo made after it holds no story.
func criteriaBody(t *testing.T) (story, body string) {
	t.Helper()
	repo := tempRepo(t)
	t.Chdir(repo)
	out, err := runRoot(t, "story", "create", "--title", "Approve PRs", "--body", "goal",
		"--acceptance", "1. "+prCritMarker+" holds.\n2. go test ./... passes.", "--category", "feature")
	if err != nil {
		t.Fatalf("story create: %v\n%s", err, out)
	}
	story = idOf(t, out)
	body, err = runRoot(t, "pr", "body", story)
	if err != nil {
		t.Fatalf("pr body: %v\n%s", err, body)
	}
	return story, body
}

// AC1: on accept, one APPROVE review lands on the PR's exact head, posted with
// the minted token, and one commit status under the configured context.
func TestPRApproveAcceptPostsAnApprovalOnTheExactHeadAndOneStatus(t *testing.T) {
	story, body := criteriaBody(t)
	n := newPRNode(t, story, true)
	writeRepoFile(t, n.repo, ".satelle/satelle.toml", "[review]\ngate_create = false\n\n[forge.github]\nstatus_context = \"orbelle/approval\"\n")
	n.addPR(story, "Opens the slice.\n\n"+body)
	stubReview(t, forge.Verdict{Accept: true, Findings: "every criterion met"}, nil)

	n.approve(t, story)

	reviews := n.gh.Reviews()
	if len(reviews) != 1 {
		t.Fatalf("reviews posted = %+v, want one", reviews)
	}
	if r := reviews[0]; r.Event != "APPROVE" || r.CommitID != prHead || r.Token != prToken || r.Number != prNumber {
		t.Errorf("review = %+v, want APPROVE on %s with the minted token", r, prHead)
	}
	statuses := n.gh.Statuses()
	if len(statuses) != 1 || statuses[0].SHA != prHead || statuses[0].Context != "orbelle/approval" ||
		statuses[0].State != forge.StatusSuccess || statuses[0].Token != prToken {
		t.Errorf("statuses = %+v, want one success under orbelle/approval on %s", statuses, prHead)
	}
	doc := n.runOutput(t)
	for _, want := range []string{"outcome: accept", "every criterion met", "Review: 1000 by satelle-reviewer[bot] on " + prHead} {
		if !strings.Contains(doc, want) {
			t.Errorf("run-output doc lacks %q:\n%s", want, doc)
		}
	}
}

// promptRunner is the reviewer agent CLI: it records the assembled request and
// answers with a verdict.
type promptRunner struct {
	mu   sync.Mutex
	sent []agentcli.Request
}

func (r *promptRunner) Name() string    { return "fake" }
func (r *promptRunner) Command() string { return "fake" }
func (r *promptRunner) Run(_ context.Context, req agentcli.Request) ([]byte, error) {
	r.mu.Lock()
	r.sent = append(r.sent, req)
	r.mu.Unlock()
	return []byte(`{"decision": "accept", "notes": "meets the criteria", "reviewed": "the diff"}`), nil
}

// AC2: the satelle-pr-review dispatch carries the story id, the PR, the head and the
// three retrieval commands, and none of the PR's diff, the story's criteria or
// the repo's constitution.
func TestPRReviewDispatchCarriesPointersOnly(t *testing.T) {
	story, body := criteriaBody(t)
	n := newPRNode(t, story, true)
	n.addPR(story, body)
	r := &promptRunner{}
	prev := caseJudgeRunner
	caseJudgeRunner = func(string, string) (agentcli.Runner, error) { return r, nil }
	t.Cleanup(func() { caseJudgeRunner = prev })

	n.approve(t, story)

	if len(r.sent) != 1 {
		t.Fatalf("review dispatches = %d, want 1\nrun output:\n%s", len(r.sent), n.runOutput(t))
	}
	prompt := r.sent[0].SystemPrompt + "\n" + r.sent[0].Payload
	ref := prRepo + "#42"
	for _, want := range []string{story, ref, prHead, "satelle pr view " + ref + " --criteria",
		"satelle doc get principles repo-constitution", "satelle pr diff " + ref} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the dispatch lacks the pointer %q", want)
		}
	}
	for _, leak := range []string{prDiffMarker, prCritMarker, prConstMark} {
		if strings.Contains(prompt, leak) {
			t.Errorf("the dispatch carries %s inline", leak)
		}
	}
	if len(n.gh.Reviews()) != 1 {
		t.Errorf("an accepted review posted %d reviews, want 1", len(n.gh.Reviews()))
	}
}

// AC3: a head pushed between the review and the approval is not approved and
// gets no status; the run records reject naming the move.
func TestPRApproveRefusesAHeadThatMovedSinceTheReview(t *testing.T) {
	story, body := criteriaBody(t)
	n := newPRNode(t, story, true)
	n.addPR(story, body)
	const moved = "2222222222222222222222222222222222222222"
	stubReview(t, forge.Verdict{Accept: true, Findings: "ok"}, func() { n.gh.SetHead(prRepo, prNumber, moved) })

	n.approve(t, story)

	if p := n.gh.Posts(); p != 0 {
		t.Errorf("forge posts = %d (reviews %+v, statuses %+v), want none", p, n.gh.Reviews(), n.gh.Statuses())
	}
	doc := n.runOutput(t)
	for _, want := range []string{"outcome: reject", "head moved", prHead, moved} {
		if !strings.Contains(doc, want) {
			t.Errorf("run-output doc lacks %q:\n%s", want, doc)
		}
	}
}

// AC8: the criteria reach a node that holds no story on the PR body: `pr view
// --criteria` returns them, and a body with no block, a block for another story
// or a tampered block is rejected naming why, with no review and no post.
func TestPRCriteriaReachTheNodeOnThePRBody(t *testing.T) {
	story, body := criteriaBody(t)
	if !strings.Contains(body, story) || !strings.Contains(body, prCritMarker) || !strings.Contains(body, "sha256:") {
		t.Fatalf("pr body = %q, want the story id, its criteria and their sha256", body)
	}

	n := newPRNode(t, story, true)
	if out, err := runRoot(t, "story", "get", story); err == nil {
		t.Fatalf("the node's store holds %s:\n%s", story, out)
	}
	n.addPR(story, body)
	out, err := runRoot(t, "pr", "view", prRepo+"#42", "--criteria", "--story", story)
	if err != nil {
		t.Fatalf("pr view --criteria: %v\n%s", err, out)
	}
	if !strings.Contains(out, prCritMarker) || !strings.Contains(out, "go test ./... passes.") || !strings.Contains(out, forge.CriteriaHash(
		"1. "+prCritMarker+" holds.\n2. go test ./... passes.")) {
		t.Errorf("pr view --criteria = %q, want the story's criteria and their hash", out)
	}

	other := strings.Replace(body, story, "sty_00000000", 1)
	tampered := strings.Replace(body, "passes.", "passes, mostly.", 1)
	for _, tc := range []struct {
		name, body, want string
	}{
		{"no block", "A PR with no criteria.", "no satelle criteria block"},
		{"another story", other, "names sty_00000000, not " + story},
		{"tampered", tampered, "do not match their recorded hash"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n.gh.SetBody(prRepo, prNumber, tc.body)
			calls := stubReview(t, forge.Verdict{Accept: true}, nil)
			n.approve(t, story)
			doc := n.runOutput(t)
			if !strings.Contains(doc, "outcome: reject") || !strings.Contains(doc, tc.want) {
				t.Errorf("run-output doc = %s, want reject naming %q", doc, tc.want)
			}
			if *calls != 0 || n.gh.Posts() != 0 {
				t.Errorf("reviews dispatched = %d, forge posts = %d; want none", *calls, n.gh.Posts())
			}
		})
	}
}

// AC9: a node whose bundle carries no repo-constitution principle rejects before
// the review, naming the missing constitution, and posts nothing.
func TestPRApproveWithoutTheConstitutionRejectsBeforeTheReview(t *testing.T) {
	story, body := criteriaBody(t)
	n := newPRNode(t, story, false)
	n.addPR(story, body)
	calls := stubReview(t, forge.Verdict{Accept: true}, nil)

	n.approve(t, story)

	doc := n.runOutput(t)
	if !strings.Contains(doc, "outcome: reject") || !strings.Contains(doc, "constitution not installed") {
		t.Errorf("run-output doc = %s, want reject naming the missing constitution", doc)
	}
	if *calls != 0 || n.gh.Posts() != 0 {
		t.Errorf("reviews dispatched = %d, forge posts = %d; want none", *calls, n.gh.Posts())
	}
}

// AC4 (the command's half): a rejecting review posts nothing and records reject
// with the reviewer's findings.
func TestPRApproveRejectPostsNothingAndRecordsTheFindings(t *testing.T) {
	story, body := criteriaBody(t)
	n := newPRNode(t, story, true)
	n.addPR(story, body)
	stubReview(t, forge.Verdict{Accept: false, Findings: "AC2 unmet: x.go:3 inlines the diff"}, nil)

	n.approve(t, story)

	if p := n.gh.Posts(); p != 0 {
		t.Errorf("forge posts = %d, want none", p)
	}
	doc := n.runOutput(t)
	if !strings.Contains(doc, "outcome: reject") || !strings.Contains(doc, "AC2 unmet: x.go:3 inlines the diff") {
		t.Errorf("run-output doc = %s, want reject with the findings", doc)
	}
}
