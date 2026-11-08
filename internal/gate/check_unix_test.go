//go:build !windows

package gate

import (
	"context"
	"errors"
	"os"
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
	r.Skills["irrelevant"] = checkSkill("echo nothing here; exit 1")
	r.Dir = t.TempDir()
	rs, pass := r.Execute(context.Background())
	if pass || results(rs) != "quiet=skipped pass=accept fail=reject" || len(d.calls) != 0 {
		t.Fatalf("%v %s", pass, results(rs))
	}
	if rs[1].Notes != "item=i1 from=s0 to=s1" || rs[2].Notes != "nope" {
		t.Fatalf("notes %q %q", rs[1].Notes, rs[2].Notes)
	}
	if rs[0].Result != Skipped || rs[0].Notes != "trigger irrelevant found nothing relevant\nnothing here" {
		t.Fatalf("skip %q %q", rs[0].Result, rs[0].Notes)
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
	var te *TimeoutError
	if !errors.As(err, &te) || te.Bound != 200*time.Millisecond || err.Error() != "check timed out after 200ms" {
		t.Fatalf("timeout error %T %v", err, err)
	}
}

func TestRunCheckShebangReadsPayload(t *testing.T) {
	script := "#!/bin/sh\ncat\n"
	ok, notes, err := RunCheck(context.Background(), script, t.TempDir(), CheckEnv{Item: "itm_1", From: "a", To: "b"}, 0)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v notes=%q", ok, err, notes)
	}
	if !strings.Contains(notes, `"id":"itm_1"`) || !strings.Contains(notes, `"to":"b"`) {
		t.Fatalf("payload not on stdin: %q", notes)
	}
}

// A gate's after check runs on every verdict (a judged accept, a judged
// reject, a carry) with the result in its environment and the notes on stdin;
// a failing after turns an accept into a reject carrying its output; it never
// runs on a skip or on a reject that is a failure rather than a verdict.
func TestAfterCheckRunsOnVerdicts(t *testing.T) {
	log := t.TempDir() + "/after.log"
	after := checkSkill(`p=$(cat); echo "gate=$SATELLE_GATE result=$SATELLE_GATE_RESULT binding=$SATELLE_GATE_BINDING item=$SATELLE_ITEM $p" >> ` + log + `; echo "published $SATELLE_GATE_RESULT"`)
	run := func(gates map[string]workflow.Gate, prior []Result, extra map[string]string) []Result {
		names := make([]string, 0, len(gates))
		for _, n := range []string{"ok", "bad", "carry", "skip", "gone", "dead", "garbled", "check", "panel"} {
			if _, ok := gates[n]; ok {
				names = append(names, n)
			}
		}
		r, d := setup([]workflow.Group{{Mode: workflow.Parallel, Gates: names}}, gates)
		for k, v := range extra {
			d.answers[k] = v
		}
		for _, n := range []string{"dead", "garbled"} {
			b := r.Agents["y1"]
			b.Name = n
			r.Agents[n] = b
		}
		r.Skills["after"] = after
		r.Skills["pass"] = checkSkill("echo checked")
		r.Prior, r.Dir = prior, t.TempDir()
		rs, _ := r.Execute(context.Background())
		return rs
	}
	gates := map[string]workflow.Gate{
		"ok":      {Skill: "judge", Agent: "y1", After: []string{"after"}},
		"bad":     {Skill: "judge", Agent: "n1", After: []string{"after"}},
		"carry":   {Skill: "judge", Agent: "y1", Carry: true, After: []string{"after"}},
		"skip":    {Skill: "judge", Agent: "y1", Paths: []string{"docs/**"}, After: []string{"after"}},
		"gone":    {Skill: "nope", Agent: "y1", After: []string{"after"}},
		"dead":    {Skill: "judge", Agent: "dead", After: []string{"after"}},
		"garbled": {Skill: "judge", Agent: "garbled", After: []string{"after"}},
		"check":   {Skill: "pass", After: []string{"after"}},
		"panel":   {Skill: "judge", Panel: []string{"y1", "garbled"}, Combine: workflow.CombineAny, After: []string{"after"}},
	}
	prior := []Result{{Transition: "s0->s1", Gate: "carry", Result: Accept, Coverage: "cov:"}}
	rs := run(gates, prior, map[string]string{"garbled": "no verdict here"})
	got := map[string]Result{}
	for _, x := range rs {
		got[x.Gate] = x
	}
	if got["ok"].Result != Accept || !strings.Contains(got["ok"].Notes, "fine\nafter after: published accept") {
		t.Errorf("judged accept: %+v", got["ok"])
	}
	if got["bad"].Result != Reject || !strings.Contains(got["bad"].Notes, "after after: published reject") {
		t.Errorf("judged reject: %+v", got["bad"])
	}
	if got["carry"].Result != Carried || !strings.Contains(got["carry"].Notes, "published carried") {
		t.Errorf("carry: %+v", got["carry"])
	}
	if got["check"].Result != Accept || !strings.Contains(got["check"].Notes, "published accept") {
		t.Errorf("check: %+v", got["check"])
	}
	for _, n := range []string{"skip", "gone", "dead", "garbled", "panel"} {
		if strings.Contains(got[n].Notes, "after after") {
			t.Errorf("%s ran after: %+v", n, got[n])
		}
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	logged := string(b)
	for _, want := range []string{
		`gate=ok result=accept binding=y1 item=i1`, `"notes":"fine"`, `"gate":"ok"`, `"result":"accept"`, `"binding":"y1"`,
		`"members":[{"binding":"y1","result":"accept","notes":"fine"}]`,
		`gate=bad result=reject binding=n1`, `"notes":"bad"`,
		`gate=carry result=carried`,
		`gate=check result=accept binding= `,
	} {
		if !strings.Contains(logged, want) {
			t.Errorf("after log lacks %q:\n%s", want, logged)
		}
	}
	if n := strings.Count(logged, "\n"); n != 4 {
		t.Errorf("after ran %d times, want 4:\n%s", n, logged)
	}
	for _, ln := range strings.Split(logged, "\n") {
		if (strings.HasPrefix(ln, "gate=carry") || strings.HasPrefix(ln, "gate=check")) && strings.Contains(ln, `"members"`) {
			t.Errorf("a carry or a check gate sees members: %s", ln)
		}
	}

	// A failing after turns an accept (and a carry) into a reject with its output.
	gates = map[string]workflow.Gate{
		"ok":    {Skill: "judge", Agent: "y1", After: []string{"fails"}},
		"carry": {Skill: "judge", Agent: "y1", Carry: true, After: []string{"fails"}},
	}
	r, _ := setup([]workflow.Group{{Mode: workflow.Parallel, Gates: []string{"ok", "carry"}}}, gates)
	r.Skills["fails"] = checkSkill("echo could not publish; exit 1")
	r.Prior, r.Dir = prior, t.TempDir()
	rs, pass := r.Execute(context.Background())
	if pass || rs[0].Result != Reject || !strings.Contains(rs[0].Notes, "after fails: could not publish") || rs[1].Result != Reject {
		t.Fatalf("failing after: %v %+v", pass, rs)
	}
	if rs[0].Coverage == "" {
		t.Errorf("coverage dropped: %+v", rs[0])
	}
}

// Checks other than an after see no gate fields.
func TestRunCheckWithoutGateHasNoGateFields(t *testing.T) {
	ok, notes, err := RunCheck(context.Background(), `cat; echo; echo "g=${SATELLE_GATE-unset}"`, t.TempDir(), CheckEnv{Item: "i"}, 0)
	if !ok || err != nil || strings.Contains(notes, `"gate"`) || !strings.Contains(notes, "g=unset") {
		t.Fatalf("%v %v %q", ok, err, notes)
	}
}

// A gate's timeout bounds that gate's check scripts (check skill, trigger and
// after, each separately) in place of the run's bound; a gate without one keeps
// the run's bound. A timed-out gate rejects naming the bound and its source.
func TestGateTimeoutBoundsThatGate(t *testing.T) {
	gates := map[string]workflow.Gate{
		"long":  {Skill: "slow", Timeout: "5s"},
		"short": {Skill: "slow"},
		"tight": {Skill: "hang", Trigger: "yes", Timeout: "200ms"},
		"trig":  {Skill: "pass", Trigger: "hang", Timeout: "200ms"},
		"after": {Skill: "pass", After: []string{"hang"}, Timeout: "200ms"},
	}
	r, _ := setup([]workflow.Group{{Mode: workflow.Parallel, Gates: []string{"long", "short", "tight", "trig", "after"}}}, gates)
	r.Skills["slow"] = checkSkill("sleep 1; echo ok")
	r.Skills["hang"] = checkSkill("sleep 30")
	r.Skills["pass"] = checkSkill("echo checked")
	r.Skills["yes"] = checkSkill("exit 0")
	r.Dir, r.CheckTimeout = t.TempDir(), 200*time.Millisecond
	start := time.Now()
	rs, pass := r.Execute(context.Background())
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("gates took %v", took)
	}
	got := map[string]Result{}
	for _, x := range rs {
		got[x.Gate] = x
	}
	if pass || got["long"].Result != Accept || got["long"].Notes != "ok" {
		t.Fatalf("long: %v %+v", pass, got["long"])
	}
	for n, want := range map[string]string{
		"short": "timed out after 200ms (default; set timeout on [gates.short] to raise it)",
		"tight": "timed out after 200ms (gates.tight timeout)",
		"trig":  "trigger hang: timed out after 200ms (gates.trig timeout)",
		"after": "after hang: timed out after 200ms (gates.after timeout)",
	} {
		if got[n].Result != Reject || !strings.Contains(got[n].Notes, want) {
			t.Errorf("%s: want reject with %q, got %+v", n, want, got[n])
		}
	}
	if !strings.HasPrefix(got["tight"].Notes, "timed out after 200ms (gates.tight timeout)") {
		t.Errorf("tight notes %q", got["tight"].Notes)
	}
}

// Without a gate timeout or a run bound, a gate's check runs under the default.
func TestGateBoundDefault(t *testing.T) {
	r := Run{}
	d, src, err := r.bound("g", workflow.Gate{})
	if err != nil || DefaultCheckTimeout != 10*time.Minute || d != DefaultCheckTimeout || src != "default; set timeout on [gates.g] to raise it" {
		t.Fatalf("%v %q %v", d, src, err)
	}
	r.CheckTimeout = time.Minute
	if d, _, _ := r.bound("g", workflow.Gate{}); d != time.Minute {
		t.Fatalf("run bound %v", d)
	}
	if d, src, _ := r.bound("g", workflow.Gate{Timeout: "30m"}); d != 30*time.Minute || src != "gates.g timeout" {
		t.Fatalf("gate bound %v %q", d, src)
	}
	if _, _, err := r.bound("g", workflow.Gate{Timeout: "soon"}); err == nil {
		t.Fatal("unparsable timeout accepted")
	}
}

// A timeout that does not parse (a config that skipped validation) fails the
// gate closed before any script runs.
func TestGateBadTimeoutFailsClosed(t *testing.T) {
	r, _ := setup([]workflow.Group{{Mode: workflow.Sequential, Gates: []string{"g"}}}, map[string]workflow.Gate{
		"g": {Skill: "pass", Timeout: "soon"},
	})
	marker := t.TempDir() + "/ran"
	r.Skills["pass"] = checkSkill("touch " + marker)
	r.Dir = t.TempDir()
	rs, pass := r.Execute(context.Background())
	if pass || rs[0].Result != Reject || !strings.Contains(rs[0].Notes, `timeout "soon" on gates.g`) {
		t.Fatalf("%v %+v", pass, rs)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("check ran under an unparsable timeout")
	}
}

// RunCheckCode reports the script's exit: its code, -1 for a signal, and a
// timeout as an error; RunCheck passes only on 0.
func TestRunCheckCodeReportsTheExit(t *testing.T) {
	for script, want := range map[string]int{"exit 0": 0, "exit 1": 1, "exit 3": 3, "kill -9 $$": -1} {
		code, _, err := RunCheckCode(context.Background(), script, t.TempDir(), CheckEnv{}, 0)
		if err != nil || code != want {
			t.Fatalf("%s: code %d err %v, want %d", script, code, err, want)
		}
		ok, _, err := RunCheck(context.Background(), script, t.TempDir(), CheckEnv{}, 0)
		if err != nil || ok != (want == 0) {
			t.Fatalf("RunCheck %s: %v %v", script, ok, err)
		}
	}
	code, _, err := RunCheckCode(context.Background(), "sleep 30", t.TempDir(), CheckEnv{}, 200*time.Millisecond)
	var te *TimeoutError
	if code != -1 || !errors.As(err, &te) {
		t.Fatalf("timeout: %d %v", code, err)
	}
}

// A trigger that exits other than 0 or 1 has failed: the gate runs anyway
// (it fails closed) and its notes start with the trigger's failure.
func TestBrokenTriggerRunsTheGate(t *testing.T) {
	r, d := setup([]workflow.Group{{Mode: workflow.Parallel, Gates: []string{"two", "missingcmd", "signal", "judged", "failing"}}}, map[string]workflow.Gate{
		"two":        {Skill: "pass", Trigger: "broken"},
		"missingcmd": {Skill: "pass", Trigger: "nocmd"},
		"signal":     {Skill: "pass", Trigger: "killed"},
		"judged":     {Skill: "judge", Agent: "y1", Trigger: "broken"},
		"failing":    {Skill: "fail", Trigger: "broken"},
	})
	r.Skills["pass"] = checkSkill("echo checked")
	r.Skills["fail"] = checkSkill("echo nope; exit 3")
	r.Skills["broken"] = checkSkill("echo cannot read the change; exit 2")
	r.Skills["nocmd"] = checkSkill("nosuchcommand-xyz")
	r.Skills["killed"] = checkSkill("kill -9 $$")
	r.Dir = t.TempDir()
	rs, pass := r.Execute(context.Background())
	if pass || results(rs) != "two=accept missingcmd=accept signal=accept judged=accept failing=reject" || len(d.calls) != 1 {
		t.Fatalf("%v %s %d", pass, results(rs), len(d.calls))
	}
	line := "trigger broken failed (exit 2); the gate ran\ncannot read the change"
	if rs[0].Notes != line+"\nchecked" {
		t.Fatalf("two notes %q", rs[0].Notes)
	}
	if !strings.HasPrefix(rs[1].Notes, "trigger nocmd failed (exit 127); the gate ran") || !strings.HasSuffix(rs[1].Notes, "checked") {
		t.Fatalf("missingcmd notes %q", rs[1].Notes)
	}
	if !strings.HasPrefix(rs[2].Notes, "trigger killed failed (killed by a signal); the gate ran") || !strings.HasSuffix(rs[2].Notes, "checked") {
		t.Fatalf("signal notes %q", rs[2].Notes)
	}
	if !strings.HasPrefix(rs[3].Notes, line) || !strings.Contains(rs[3].Notes, "fine") {
		t.Fatalf("judged notes %q", rs[3].Notes)
	}
	if !strings.HasPrefix(rs[4].Notes, line) || !strings.HasSuffix(rs[4].Notes, "nope") {
		t.Fatalf("failing notes %q", rs[4].Notes)
	}
}

// The after list runs in order and stops at the first refusal: a notes rule
// (a check, here a fixture) that refuses an empty-notes accept turns it into
// a reject carrying the check's output and the judge's raw answer, and the
// publish check after it never runs. A gate with no after records the
// verdict as given.
func TestAfterListRefusesAndKeepsAnswer(t *testing.T) {
	const empty = `thinking done {"verdict":"accept","notes":""}`
	publishLog := t.TempDir() + "/publish.log"
	notesRule := checkSkill(`#!/bin/sh
p=$(cat)
case "$p" in
  *'"notes":""'*) echo "the judge gave no reasons"; exit 1;;
esac
exit 0
`)
	publish := checkSkill(`echo "$SATELLE_GATE $SATELLE_GATE_RESULT" >> ` + publishLog + `; echo published`)
	gates := map[string]workflow.Gate{
		"single": {Skill: "judge", Agent: "empty", After: []string{"notes", "publish"}},
		"panel":  {Skill: "judge", Panel: []string{"y1", "empty"}, Combine: workflow.CombineAll, After: []string{"notes", "publish"}},
		"good":   {Skill: "judge", Agent: "y1", After: []string{"notes", "publish"}},
		"bare":   {Skill: "judge", Agent: "empty"},
	}
	r, d := setup([]workflow.Group{{Mode: workflow.Parallel, Gates: []string{"single", "panel", "good", "bare"}}}, gates)
	b := r.Agents["y2"]
	b.Name = "empty"
	r.Agents["empty"] = b
	d.answers["empty"] = empty
	r.Skills["notes"], r.Skills["publish"] = notesRule, publish
	r.Dir = t.TempDir()
	rs, pass := r.Execute(context.Background())
	got := map[string]Result{}
	for _, x := range rs {
		got[x.Gate] = x
	}
	if pass {
		t.Fatal("refused verdicts passed")
	}
	s := got["single"]
	if s.Result != Reject || !strings.Contains(s.Notes, "after notes: the judge gave no reasons") ||
		!strings.Contains(s.Notes, "after publish: not run: after notes refused the verdict") || s.Answer != empty {
		t.Errorf("single: %+v", s)
	}
	p := got["panel"]
	if p.Result != Reject || p.Answer != "" || len(p.Members) != 2 || p.Members[1].Answer != empty || p.Members[0].Answer != yes ||
		p.Members[0].Result != Accept || p.Members[1].Result != Accept {
		t.Errorf("panel: %+v", p)
	}
	g := got["good"]
	if g.Result != Accept || g.Answer != "" || strings.Contains(g.Notes, "after notes") || !strings.Contains(g.Notes, "after publish: published") {
		t.Errorf("good: %+v", g)
	}
	if bare := got["bare"]; bare.Result != Accept || bare.Notes != "" || bare.Answer != "" {
		t.Errorf("a gate with no after changed the verdict: %+v", bare)
	}
	logged, err := os.ReadFile(publishLog)
	if err != nil || string(logged) != "good accept\n" {
		t.Fatalf("publish ran on %q (%v), want only good", logged, err)
	}
}
