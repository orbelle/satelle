//go:build !windows

package kernel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/item"
)

const reviewTable = `
[review]
tier_tag = "risk"
[review.panels.review]
low = ["r1"]
high = ["r1", "r2"]
`

func reviewRoot(t *testing.T, rules string) string {
	t.Helper()
	root := overrideRoot(t, rules, "")
	f, err := os.OpenFile(filepath.Join(root, "agents.toml"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("[agents.r2]\ncommand = \"cursor-agent\"\nmodel = \"m2\"\ntools = [\"Read\", \"Grep\", \"Glob\"]\n"); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestReviewTierRecordedOnGateRun(t *testing.T) {
	root := reviewRoot(t, reviewTable)
	k, err := Open(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	d := &fakeDispatcher{answers: map[string]string{"doer": "did it", "r1": accept, "r2": accept}}
	k.Dispatcher = d
	ctx := context.Background()
	it, err := k.Create(item.Fields{Kind: ptr("thing"), Title: ptr("t"), Tags: &[]string{"risk:high"}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Transition(ctx, it.ID, "work", "me"); err != nil {
		t.Fatal(err)
	}
	dry, err := k.GateDry(ctx, GateDryRequest{Workflow: "flow", Gate: "review", Item: it.ID, From: "work", To: "closed", ShowInput: true})
	if err != nil || dry.Input == nil || dry.Input.Tier != "high" || strings.Join(dry.Input.Judges, ",") != "r1,r2" {
		t.Fatalf("dry input: %v %+v", err, dry.Input)
	}
	if _, err := k.Perform(ctx, it.ID, "me"); err != nil {
		t.Fatal(err)
	}
	if res, err := k.Transition(ctx, it.ID, "closed", "me"); err != nil || !res.Moved {
		t.Fatalf("close: %v %+v", err, res)
	}
	ev, err := k.Ledger.Read(it.ID)
	if err != nil {
		t.Fatal(err)
	}
	var res gate.Result
	for _, e := range ev {
		if e.Kind == gate.KindRun {
			_ = e.Decode(&res)
		}
	}
	if res.Tier != "high" || res.Binding != "r1,r2" || len(res.Members) != 2 {
		t.Errorf("gate.run %+v, want tier high judged by r1 and r2", res)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	judges := map[string]bool{}
	for _, r := range d.reqs {
		if r.Binding.Name != "doer" {
			judges[r.Binding.Name] = true
		}
	}
	if len(judges) != 2 || !judges["r1"] || !judges["r2"] {
		t.Errorf("dispatched judges %v, want r1 and r2", judges)
	}
}

func TestReviewProblems(t *testing.T) {
	problems := func(rules string) string {
		var msgs []string
		for _, e := range Validate(reviewRoot(t, rules)) {
			msgs = append(msgs, e.Error())
		}
		return strings.Join(msgs, "\n")
	}
	if p := problems(reviewTable); p != "" {
		t.Errorf("a valid [review]: %q", p)
	}
	bad := "\n[review]\ntier_tag = \"risk\"\n[review.panels.review]\nlow = [\"ghost\"]\n[review.panels.nowhere]\nlow = [\"r1\"]\n"
	p := problems(bad)
	for _, w := range []string{`agent "ghost" is not in the agents catalog`, `no workflow has a gate "nowhere"`} {
		if !strings.Contains(p, w) {
			t.Errorf("validate %q lacks %q", p, w)
		}
	}
	if _, err := Open(Options{Root: reviewRoot(t, bad)}); err == nil {
		t.Error("the kernel opened with a bad [review]")
	}
	same := "[rules]\nreviewer_differs_from_performer = true\n[review]\ntier_tag = \"risk\"\n[review.panels.review]\nlow = [\"r3\"]\n"
	root := reviewRoot(t, same)
	f, _ := os.OpenFile(filepath.Join(root, "agents.toml"), os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString("[agents.r3]\ncommand = \"cursor-agent\"\nmodel = \"big\"\ntools = [\"Read\"]\n")
	f.Close()
	var msgs []string
	for _, e := range Validate(root) {
		msgs = append(msgs, e.Error())
	}
	if all := strings.Join(msgs, "\n"); !strings.Contains(all, `judge "r3" runs model "big", the same as the performer`) {
		t.Errorf("a review-panel judge with the performer's model: %q", all)
	}
}
