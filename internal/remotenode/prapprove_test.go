package remotenode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/forge"
	"github.com/bobmcallan/satelle/internal/forge/github"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/remotedef"
	"github.com/bobmcallan/satelle/internal/remotenode/devtest"
	"github.com/bobmcallan/satelle/internal/runstatus"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// embeddedPRApprove is the pr-approve bundle template the binary ships.
func embeddedPRApprove(t *testing.T) map[string]string {
	t.Helper()
	files, ok := config.EmbeddedRemote("pr-approve")
	if !ok {
		t.Fatal("the binary ships no pr-approve remote bundle")
	}
	return files
}

// The shipped pr-approve bundle installs; its v1 definition runs task
// tsk_pr-approve, fetches exactly one repo, allows only the GitHub API and
// declares one github-app override with the permissions its token needs to read
// the repo, approve and post a status, beside the agent CLI's team credential,
// and that allowlist is what the node reports.
func TestEmbeddedPRApproveBundleInstallsAndDeclaresItsSandbox(t *testing.T) {
	dataDir := t.TempDir()
	def, err := InstallDefinition(dataDir, bundleOf(t, embeddedPRApprove(t)))
	if err != nil {
		t.Fatal(err)
	}
	if !def.RunsTask() || def.TaskID() != "tsk_pr-approve" {
		t.Errorf("drive = %+v, want kind task on tsk_pr-approve", def.Drive)
	}
	if len(def.Repos) != 1 {
		t.Errorf("repos = %+v, want exactly one", def.Repos)
	}
	if want := []string{"api.github.com"}; !reflect.DeepEqual(def.Allow(), want) {
		t.Errorf("allow = %v, want %v", def.Allow(), want)
	}
	key, ok := def.GitHubAppKey("github.com")
	if !ok || key != "GH_TOKEN" {
		t.Errorf("github-app key = %q, %v, want GH_TOKEN", key, ok)
	}
	for _, e := range def.SecretEntries() {
		if e.Source == remotedef.SourceRunToken {
			want := map[string]string{"contents": "read", "pull_requests": "write", "statuses": "write"}
			if !reflect.DeepEqual(e.Permissions, want) {
				t.Errorf("github-app permissions = %v, want %v", e.Permissions, want)
			}
		}
	}
	if n := len(def.SecretEntries()); n != 2 {
		t.Errorf("secrets = %+v, want GH_TOKEN and CLAUDE_CODE_OAUTH_TOKEN", def.SecretEntries())
	}
	if _, err := os.Stat(filepath.Join(dataDir, "principles", forge.RepoConstitution+".md")); err != nil {
		t.Errorf("the bundle's constitution placeholder was not installed: %v", err)
	}
	rep := Report{Allow: def.Allow()}
	raw, _ := json.Marshal(rep)
	var back Report
	if err := json.Unmarshal(raw, &back); err != nil || !reflect.DeepEqual(back.Allow, []string{"api.github.com"}) {
		t.Errorf("reported allow = %v (%v), want [api.github.com]", back.Allow, err)
	}
}

// prApproveEnv is a task env whose node runs the embedded tsk_pr-approve: the
// task is ingested from the binary's defaults, the repo's constitution principle
// is installed, and the fake GitHub serves orbelle/satelle to the run's token.
func prApproveEnv(t *testing.T) (*taskEnv, *devtest.GitHub) {
	t.Helper()
	e := newTaskEnv(t, false)
	pdir := filepath.Join(t.TempDir(), "principles")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: repo-constitution\ntype: principle\ndescription: the repo's constitution\n---\n\n# Constitution\n"
	if err := os.WriteFile(filepath.Join(pdir, "repo-constitution.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.DocIndex.Sync(context.Background(), map[string]string{"principles": pdir}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Stories.Get(context.Background(), "tsk_pr-approve"); err != nil {
		t.Fatalf("the embedded tsk_pr-approve was not ingested: %v", err)
	}
	gh := devtest.NewGitHub(t)
	gh.Mint("ghs_run", "orbelle/satelle")
	return e, gh
}

// approveStep is the executor step of tsk_pr-approve doing what its action
// names, in-process: the approval for the execution's story_id input against the
// fake, with only the review's verdict stubbed.
func approveStep(gh *devtest.GitHub, verdict forge.Verdict) executorOnly {
	return executorOnly{step: func(item workitem.Item) error {
		f, err := github.New(forge.Config{APIURL: gh.URL, Token: "ghs_run", HTTP: gh.Client()})
		if err != nil {
			return err
		}
		story, _ := item.InputValues[InputStoryID].(string)
		_, err = forge.RunApproval(context.Background(), forge.Approval{
			Forge: f, Repo: "orbelle/satelle", StoryID: story, StatusContext: config.DefaultForgeStatusContext,
			Constitution: forge.ConstitutionInstalled,
			Review: func(context.Context, forge.ReviewRequest) (forge.Verdict, error) {
				return verdict, nil
			},
		}, item.ID)
		return err
	}}
}

func prApproveRun(e *taskEnv, perf executorOnly, log *statusLog) TaskRun {
	tr := taskRun(e, perf, log)
	tr.Def = remotedef.Definition{Drive: remotedef.Drive{Kind: remotedef.DriveTask, Task: "pr-approve"}}
	return tr
}

// AC4: a rejecting review posts nothing; the run concludes reject with the
// reviewer's findings on the heartbeat, and the subject carries one
// execution_link row naming the execution with outcome reject. The accepting
// control on the same setup posts one review and one status, so the zero is the
// code's. A wake whose story has no open PR concludes reject naming that.
func TestRunTaskPRApproveRejectPostsNothingAndLinksTheFindings(t *testing.T) {
	const findings = "AC2 unmet: internal/x.go:12 inlines the diff"
	pr := devtest.PR{Repo: "orbelle/satelle", Number: 9, HeadRef: forge.EpicHead("sty_subject"), HeadSHA: strings.Repeat("c", 40),
		Body: forge.RenderCriteria("sty_subject", "subject", "1. It approves.")}

	e, gh := prApproveEnv(t)
	gh.AddPR(pr)
	log := &statusLog{}
	res, err := RunTask(context.Background(), prApproveRun(e, approveStep(gh, forge.Verdict{Findings: findings}), log))
	if err != nil {
		t.Fatal(err)
	}
	if gh.Posts() != 0 {
		t.Errorf("forge posts = %d (reviews %+v, statuses %+v), want none", gh.Posts(), gh.Reviews(), gh.Statuses())
	}
	if res.Outcome != verb.OutcomeReject || !strings.Contains(res.Findings, findings) {
		t.Errorf("result = %+v, want reject carrying the findings", res)
	}
	sent := log.all()
	if last := sent[len(sent)-1]; last.RunState != runstatus.Concluded || last.Outcome != verb.OutcomeReject || !strings.Contains(last.Findings, findings) {
		t.Errorf("concluded heartbeat = %+v, want reject with the findings", last)
	}
	links := e.rows("sty_subject", ledger.KindExecutionLink)
	if len(links) != 1 {
		t.Fatalf("execution_link rows on the subject = %d, want 1", len(links))
	}
	var p ledger.ExecutionLinkPayload
	if err := json.Unmarshal(links[0].Payload, &p); err != nil || p.Execution != res.Execution || p.Outcome != verb.OutcomeReject || p.Task != "tsk_pr-approve" {
		t.Errorf("link payload = %+v (%v), want execution %s, task tsk_pr-approve, outcome reject", p, err, res.Execution)
	}

	// Control: the same setup with an accepting review posts one of each.
	eOK, ghOK := prApproveEnv(t)
	ghOK.AddPR(pr)
	ok, err := RunTask(context.Background(), prApproveRun(eOK, approveStep(ghOK, forge.Verdict{Accept: true, Findings: "met"}), &statusLog{}))
	if err != nil || ok.Outcome != verb.OutcomeAccept {
		t.Fatalf("accepting control = %+v, %v", ok, err)
	}
	if len(ghOK.Reviews()) != 1 || len(ghOK.Statuses()) != 1 {
		t.Errorf("accepting control posted reviews %+v, statuses %+v; want one of each", ghOK.Reviews(), ghOK.Statuses())
	}

	// No open PR for the subject's epic head.
	eNone, ghNone := prApproveEnv(t)
	none, err := RunTask(context.Background(), prApproveRun(eNone, approveStep(ghNone, forge.Verdict{Accept: true}), &statusLog{}))
	if err != nil || none.Outcome != verb.OutcomeReject || !strings.Contains(none.Findings, "no open PR") {
		t.Errorf("no PR = %+v, %v; want reject naming no open PR", none, err)
	}
	if ghNone.Posts() != 0 {
		t.Errorf("no PR posted %d, want none", ghNone.Posts())
	}
}
