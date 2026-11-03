package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/substratereview"
)

// judgeDispatch is one request the fake judge runner received, with the
// manifest it read through the payload's document path.
type judgeDispatch struct {
	req      agentcli.Request
	manifest substratereview.Manifest
}

// fakeJudgeRunner stands in for the judging agent CLI on the real
// newSubstrateJudge → CaseJudge → Engine path: it records every assembled
// request and the (iface, command) each runner was built from, reads the
// manifest the way a judge with a read grant would, and answers with answer.
type fakeJudgeRunner struct {
	mu       sync.Mutex
	built    [][2]string
	sent     []judgeDispatch
	readErrs []string
	answer   func(m substratereview.Manifest) string
}

func (r *fakeJudgeRunner) Name() string    { return "fake" }
func (r *fakeJudgeRunner) Command() string { return "fake" }

func (r *fakeJudgeRunner) Run(_ context.Context, req agentcli.Request) ([]byte, error) {
	var top struct {
		Docs []struct {
			Name string `json:"name"`
			Path string `json:"path"`
		} `json:"docs"`
	}
	var m substratereview.Manifest
	_ = json.Unmarshal([]byte(req.Payload), &top)
	for _, d := range top.Docs {
		if d.Name != substrateManifestDoc {
			continue
		}
		b, err := os.ReadFile(d.Path)
		if err == nil {
			err = json.Unmarshal(b, &m)
		}
		if err != nil {
			r.mu.Lock()
			r.readErrs = append(r.readErrs, err.Error())
			r.mu.Unlock()
		}
	}
	r.mu.Lock()
	r.sent = append(r.sent, judgeDispatch{req: req, manifest: m})
	r.mu.Unlock()
	return []byte(verdictJSON(r.answer(m))), nil
}

// isolateSubstrateScratch keeps the review's scratch files in the test's temp
// dir.
func isolateSubstrateScratch(t *testing.T) {
	t.Helper()
	prev := substrateScratchRoot
	substrateScratchRoot = t.TempDir()
	t.Cleanup(func() { substrateScratchRoot = prev })
}

// verdictJSON is the accept verdict a judge returns with notes as its text.
func verdictJSON(notes string) string {
	b, _ := json.Marshal(map[string]string{"decision": "accept", "notes": notes})
	return string(b)
}

// installFakeJudge routes every case judge's runner to a fake answering with
// answer.
func installFakeJudge(t *testing.T, answer func(m substratereview.Manifest) string) *fakeJudgeRunner {
	t.Helper()
	r := &fakeJudgeRunner{answer: answer}
	prev := caseJudgeRunner
	t.Cleanup(func() { caseJudgeRunner = prev })
	caseJudgeRunner = func(iface, command string) (agentcli.Runner, error) {
		r.mu.Lock()
		r.built = append(r.built, [2]string{iface, command})
		r.mu.Unlock()
		return r, nil
	}
	return r
}

// keepAll judges every manifest item keep, with a reason and evidence, and
// every rule "none".
func keepAll(m substratereview.Manifest) string {
	return judgementFor(m, nil)
}

// judgementFor judges every item of m: the disposition in want, else keep;
// every rule none.
func judgementFor(m substratereview.Manifest, want map[string]substratereview.ItemJudgement) string {
	var j substratereview.Judgement
	for _, it := range m.Items {
		ij, ok := want[it.Item]
		if !ok {
			ij = substratereview.ItemJudgement{Disposition: "keep", Rule: "repo-specific", Reason: "no default carries it", Evidence: it.Path + ":1"}
		}
		ij.Item = it.Item
		j.Items = append(j.Items, ij)
	}
	for _, r := range m.Rules {
		j.Constitution = append(j.Constitution, substratereview.RuleFinding{Rule: r.Name, None: true})
	}
	b, _ := json.Marshal(j)
	return string(b)
}

// appendLedgerRow writes one ledger row on story.
func appendLedgerRow(t *testing.T, story, kind string, payload map[string]any) {
	t.Helper()
	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	b, _ := json.Marshal(payload)
	if _, err := db.Ledger.Append(context.Background(), ledger.AppendInput{StoryID: story, Kind: kind, Actor: "test", Body: kind, Payload: b}, time.Now()); err != nil {
		t.Fatal(err)
	}
}

// recordInLoop records an in-loop session_model row naming agent and model.
func recordInLoop(t *testing.T, story, agent, model string) {
	t.Helper()
	appendLedgerRow(t, story, ledger.KindSessionModel, map[string]any{"role": "in-loop", "agent": agent, "model": model})
}

// setConfig rewrites the repo's satelle.toml.
func setConfig(t *testing.T, repo, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, ".satelle", "satelle.toml"), []byte("[review]\ngate_create = false\n\n"+body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const (
	bodySentinel      = "BODY-SENTINEL-7f3a"
	rejectionSentinel = "REJECTION-SENTINEL-91c2"
)

// manifestOnlyFixture is a repo holding a 200 KB principle, a repo skill with a
// rejection whose notes carry a sentinel, and an in-loop session_model row.
func manifestOnlyFixture(t *testing.T) (repo, story string) {
	t.Helper()
	repo = tempRepo(t)
	isolateSubstrateScratch(t)
	runValidatorsDropped(t, repo)
	big := "---\nname: huge\ntype: principle\ndescription: huge\n---\n# huge\n\n" + bodySentinel + "\n" + strings.Repeat("filler line of the principle body.\n", 200*1024/35)
	writeRepoFile(t, repo, ".satelle/principles/huge.md", big)
	writeRepoFile(t, repo, ".satelle/skills/repo-cut.md", "---\ntype: skill\nname: repo-cut\ndescription: a repo skill\n---\n# repo cut\n")
	writeRepoFile(t, repo, ".satelle/constitution.md", "# constitution\n\n- [[huge]]\n")
	if out, err := runRoot(t, "reindex"); err != nil {
		t.Fatalf("reindex: %v\n%s", err, out)
	}
	story = createStory(t, "History")
	appendLedgerRow(t, story, ledger.KindReviewReject, map[string]any{"from": "backlog", "to": "plan", "skill": "repo-cut", "attempt": "att_x", "notes": rejectionSentinel, "model_resolved": "M1"})
	recordInLoop(t, story, "claude", "claude-opus-5-5")
	return repo, story
}

// walkStrings calls f on every string value in v.
func walkStrings(v any, f func(string)) {
	switch x := v.(type) {
	case string:
		f(x)
	case []any:
		for _, e := range x {
			walkStrings(e, f)
		}
	case map[string]any:
		for _, e := range x {
			walkStrings(e, f)
		}
	}
}
