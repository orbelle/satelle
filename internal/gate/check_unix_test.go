//go:build !windows

package gate

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/workflow"
)

func checkSkill(script string) workflow.Skill {
	return workflow.Skill{Meta: map[string]string{"type": workflow.TypeCheck}, Script: script}
}

func TestCheckGatesAndTriggers(t *testing.T) {
	r, d := setup([]workflow.Group{{Mode: workflow.Sequential, Gates: []string{"quiet", "pass", "fail", "never"}}}, map[string]workflow.Gate{
		"quiet": {Skill: "pass", Trigger: "irrelevant"},
		"pass":  {Skill: "pass", Trigger: "relevant"},
		"fail":  {Skill: "fail"},
		"never": {Skill: "judge", Agent: "y1"},
	})
	r.Skills["pass"] = checkSkill(`echo "item=$SATELLE_ITEM from=$SATELLE_FROM to=$SATELLE_TO"`)
	r.Skills["fail"] = checkSkill("echo nope >&2; exit 3")
	r.Skills["relevant"] = checkSkill("exit 0")
	r.Skills["irrelevant"] = checkSkill("exit 1")
	r.Dir = t.TempDir()
	rs, pass := r.Execute(context.Background())
	if pass || results(rs) != "quiet=skipped pass=accept fail=reject" || len(d.calls) != 0 {
		t.Fatalf("%v %s", pass, results(rs))
	}
	if rs[1].Notes != "item=i1 from=s0 to=s1" || rs[2].Notes != "nope" {
		t.Fatalf("notes %q %q", rs[1].Notes, rs[2].Notes)
	}
}

func TestRunCheckBoundsAndTimeout(t *testing.T) {
	ok, out, err := RunCheck(context.Background(), "i=0; while [ $i -lt 3000 ]; do echo line$i; i=$((i+1)); done", t.TempDir(), CheckEnv{}, 0)
	if !ok || err != nil || !strings.Contains(out, "bytes elided") || !strings.HasPrefix(out, "line0") || !strings.HasSuffix(out, "line2999") {
		t.Fatalf("%v %v %d", ok, err, len(out))
	}
	start := time.Now()
	ok, _, err = RunCheck(context.Background(), "sleep 30", t.TempDir(), CheckEnv{}, 200*time.Millisecond)
	if ok || err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("timeout: %v %v %v", ok, err, time.Since(start))
	}
}
