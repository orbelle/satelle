// Package gate runs a transition's gate groups in order: sequential groups
// stop at the first reject, parallel groups run every gate. Each gate is
// first judged for relevance (paths, trigger), then for carry (an earlier
// accept with the same coverage), and only then run: a check skill as a
// script, any other skill by its judge or panel. Every gate yields a Result
// for the caller to record.
package gate

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/workflow"
)

// KindRun is the ledger kind a gate result is recorded under.
const KindRun = "gate.run"

// Results.
const (
	Accept  = "accept"
	Reject  = "reject"
	Skipped = "skipped"
	Carried = "carried"
)

// Result is one gate's outcome on one transition: the gate.run payload.
type Result struct {
	Transition string `json:"transition"`
	Gate       string `json:"gate"`
	Binding    string `json:"binding,omitempty"`
	Result     string `json:"result"`
	Notes      string `json:"notes,omitempty"`
	TokensIn   int    `json:"tokens_in"`
	TokensOut  int    `json:"tokens_out"`
	DurationMS int64  `json:"duration_ms"`
	Coverage   string `json:"coverage,omitempty"`
	// Attempt names the presentation this result belongs to: every gate of
	// one Transition call shares it.
	Attempt string `json:"attempt,omitempty"`
	// Changed is the diff a judged gate (accept or reject over a coverage)
	// saw: the reference a later skip compares with.
	Changed []string `json:"changed,omitempty"`
	// Warning says a relevance skip happened on an empty diff, or one that has
	// shrunk since the item's last judged diff: a skip that may mean the base
	// is wrong, not that nothing relevant changed.
	Warning string `json:"warning,omitempty"`

	// verdict says the result is a verdict: a check that ran, or a judge
	// (every panel member) that answered with one. A reject that is a failure
	// (missing skill, dispatch error, unparseable answer) is not.
	verdict bool
}

// TransitionKey names a move for carry and the ledger.
func TransitionKey(from, to string) string { return from + "->" + to }

// FromOf is the from-state of a TransitionKey.
func FromOf(key string) string {
	from, _, _ := strings.Cut(key, "->")
	return from
}

// Run is one presentation of a transition.
type Run struct {
	Item       item.Item
	From, To   string
	Transition *workflow.Transition
	Workflow   *workflow.Workflow
	Skills     map[string]workflow.Skill
	Agents     agent.Catalog
	Dispatcher agent.Dispatcher

	Dir     string   // where checks and judges run
	Base    string   // the commit the work started from
	Changed []string // paths changed since Base
	// Coverage hashes what a gate with these paths judges; nil disables carry.
	Coverage func(paths []string) (string, error)
	// Diff returns the change a gate with these paths judges, for the prompt.
	Diff func(paths []string) (string, error)
	// Prior is every earlier gate.run result on the item, oldest first.
	Prior []Result
	// ReviewerDiffers refuses a judge on the from-state performer's model.
	ReviewerDiffers bool
	CheckTimeout    time.Duration
	// Record is the item's record section (attachments and children) for the
	// judge prompt; "" adds nothing.
	Record string

	skipWarning string // diffWarning, computed once per Execute
}

// Execute runs the groups and returns the results in run order and whether
// the transition may proceed (no gate rejected).
func (r Run) Execute(ctx context.Context) ([]Result, bool) {
	r.skipWarning = r.diffWarning()
	var all []Result
	for _, g := range r.Transition.Groups {
		var res []Result
		if g.Mode == workflow.Parallel {
			res = make([]Result, len(g.Gates))
			var wg sync.WaitGroup
			for i, name := range g.Gates {
				wg.Add(1)
				go func() {
					defer wg.Done()
					res[i] = r.one(ctx, name)
				}()
			}
			wg.Wait()
		} else {
			for _, name := range g.Gates {
				out := r.one(ctx, name)
				res = append(res, out)
				if out.Result == Reject {
					break
				}
			}
		}
		all = append(all, res...)
		for _, x := range res {
			if x.Result == Reject {
				return all, false
			}
		}
	}
	return all, true
}

func (r Run) key() string { return TransitionKey(r.From, r.To) }

func (r Run) env() CheckEnv { return CheckEnv{Item: r.Item.ID, From: r.From, To: r.To, Base: r.Base} }

// one runs a single gate through relevance, carry and judgement.
func (r Run) one(ctx context.Context, name string) Result {
	start := time.Now()
	res := r.decide(ctx, name)
	res.Transition, res.Gate = r.key(), name
	if g, ok := r.Workflow.Gates[name]; ok && g.After != "" && (res.verdict || res.Result == Carried) {
		res = r.after(ctx, name, g.After, res)
	}
	if (res.Result == Accept || res.Result == Reject) && res.Coverage != "" {
		res.Changed = r.Changed
	}
	res.DurationMS = time.Since(start).Milliseconds()
	return res
}

// after runs a gate's after check on its verdict. Exit 0 keeps the result;
// a non-zero exit or an error turns it into a reject. Either way the check's
// output joins the notes.
func (r Run) after(ctx context.Context, name, skillName string, res Result) Result {
	skill, ok := r.Skills[skillName]
	if !ok {
		res.Result = Reject
		res.Notes = joinNotes(res.Notes, fmt.Sprintf("after skill %q is missing", skillName))
		return res
	}
	env := r.env()
	env.Gate, env.Result, env.Binding, env.Notes = name, res.Result, res.Binding, res.Notes
	passed, out, err := RunCheck(ctx, skill.Script, r.Dir, env, r.CheckTimeout)
	note := fmt.Sprintf("after %s: %s", skillName, out)
	if err != nil {
		note = fmt.Sprintf("after %s: %v\n%s", skillName, err, out)
	}
	res.Notes = joinNotes(res.Notes, note)
	if err != nil || !passed {
		res.Result = Reject
	}
	return res
}

func joinNotes(a, b string) string {
	if a == "" {
		return b
	}
	return a + "\n" + b
}

func (r Run) decide(ctx context.Context, name string) Result {
	g, ok := r.Workflow.Gates[name]
	if !ok {
		return Result{Result: Reject, Notes: fmt.Sprintf("gate %q is not defined", name)}
	}
	if len(g.Paths) > 0 && !MatchAny(g.Paths, r.Changed) {
		return Result{Result: Skipped, Notes: fmt.Sprintf("no changed path matches %v", g.Paths), Warning: r.skipWarning}
	}
	if g.Trigger != "" {
		trig, ok := r.Skills[g.Trigger]
		if !ok {
			return Result{Result: Reject, Notes: fmt.Sprintf("trigger skill %q is missing", g.Trigger)}
		}
		relevant, out, err := RunCheck(ctx, trig.Script, r.Dir, r.env(), r.CheckTimeout)
		if err != nil {
			return Result{Result: Reject, Notes: fmt.Sprintf("trigger %s: %v\n%s", g.Trigger, err, out)}
		}
		if !relevant {
			return Result{Result: Skipped, Notes: fmt.Sprintf("trigger %s found nothing relevant\n%s", g.Trigger, out), Warning: r.skipWarning}
		}
	}
	coverage := ""
	if r.Coverage != nil {
		c, err := r.Coverage(g.Paths)
		if err != nil {
			return Result{Result: Reject, Notes: fmt.Sprintf("coverage: %v", err)}
		}
		coverage = c
		if g.Carry && r.carried(name, c) {
			return Result{Result: Carried, Coverage: c, Notes: "an earlier accept covers the same change"}
		}
	}
	skill, ok := r.Skills[g.Skill]
	if !ok {
		return Result{Result: Reject, Notes: fmt.Sprintf("skill %q is missing", g.Skill), Coverage: coverage}
	}
	var res Result
	if skill.IsCheck() {
		passed, out, err := RunCheck(ctx, skill.Script, r.Dir, r.env(), r.CheckTimeout)
		res = Result{Result: Reject, Notes: out}
		if err != nil {
			res.Notes = fmt.Sprintf("%v\n%s", err, out)
		} else {
			res.verdict = true
			if passed {
				res.Result = Accept
			}
		}
	} else {
		res = r.judge(ctx, g, skill)
	}
	res.Coverage = coverage
	return res
}

// diffWarning is what a relevance skip on this run reports: the diff is
// empty, or paths of the item's last judged diff are no longer changed (by set
// containment, so a diff that swapped its paths for others is caught too).
// "" when the diff holds everything last judged.
func (r Run) diffWarning() string {
	check := fmt.Sprintf("check the item's base (satelle %s diff %s)", r.Item.Kind, r.Item.ID)
	if len(r.Changed) == 0 {
		b := r.Base
		if len(b) > 12 {
			b = b[:12]
		}
		return fmt.Sprintf("the item's diff is empty (base %s): no changed path reached this gate; %s", b, check)
	}
	for i := len(r.Prior) - 1; i >= 0; i-- {
		p := r.Prior[i]
		if (p.Result != Accept && p.Result != Reject) || p.Coverage == "" || len(p.Changed) == 0 {
			continue
		}
		now := map[string]bool{}
		for _, c := range r.Changed {
			now[c] = true
		}
		var gone []string
		for _, c := range p.Changed {
			if !now[c] {
				gone = append(gone, c)
			}
		}
		if len(gone) == 0 {
			return ""
		}
		shown := gone
		more := ""
		if len(shown) > 5 {
			shown, more = shown[:5], ", …"
		}
		return fmt.Sprintf("the item's diff has shrunk since its last judged diff (%s on %s): %d path(s) no longer changed: %s%s; %s",
			p.Gate, p.Transition, len(gone), strings.Join(shown, ", "), more, check)
	}
	return ""
}

// carried reports whether the gate's latest result on this transition was an
// accept (or carry) over the same coverage.
func (r Run) carried(gate, coverage string) bool {
	for i := len(r.Prior) - 1; i >= 0; i-- {
		p := r.Prior[i]
		if p.Gate != gate || p.Transition != r.key() || p.Result == Skipped {
			continue
		}
		return (p.Result == Accept || p.Result == Carried) && p.Coverage == coverage
	}
	return false
}

// judge dispatches the gate's skill to its agent or every panel member and
// combines their verdicts.
func (r Run) judge(ctx context.Context, g workflow.Gate, skill workflow.Skill) Result {
	names := g.Judges()
	prompt, err := r.prompt(g)
	if err != nil {
		return Result{Result: Reject, Notes: err.Error(), Binding: strings.Join(names, ",")}
	}
	members := make([]Result, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			members[i] = r.judgeOne(ctx, name, skill, prompt)
		}()
	}
	wg.Wait()
	if len(members) == 1 {
		return members[0]
	}
	out := Result{Binding: strings.Join(names, ","), verdict: true}
	accepts := 0
	var notes []string
	for _, m := range members {
		if m.Result == Accept {
			accepts++
		}
		out.verdict = out.verdict && m.verdict
		out.TokensIn += m.TokensIn
		out.TokensOut += m.TokensOut
		notes = append(notes, fmt.Sprintf("%s: %s: %s", m.Binding, m.Result, m.Notes))
	}
	pass := false
	switch g.Combine {
	case workflow.CombineAny:
		pass = accepts > 0
	case workflow.CombineMost:
		pass = 2*accepts > len(members)
	default:
		pass = accepts == len(members)
	}
	out.Result = Reject
	if pass {
		out.Result = Accept
	}
	out.Notes = fmt.Sprintf("panel (%s): %d of %d accept\n%s", g.Combine, accepts, len(members), strings.Join(notes, "\n"))
	return out
}

func (r Run) judgeOne(ctx context.Context, name string, skill workflow.Skill, prompt string) Result {
	res := Result{Binding: name, Result: Reject}
	b, ok := r.Agents[name]
	if !ok {
		res.Notes = fmt.Sprintf("agent %q is not in the catalog", name)
		return res
	}
	if r.ReviewerDiffers {
		if model := r.performerModel(); model != "" && model == b.Model {
			res.Notes = fmt.Sprintf("refused: judge %q runs model %q, the same as the performer of %q", name, model, r.From)
			return res
		}
	}
	out, err := r.Dispatcher.Dispatch(ctx, agent.Judge, agent.Request{Binding: b, System: skill.Body, Prompt: prompt, Dir: r.Dir})
	res.TokensIn, res.TokensOut = out.TokensIn, out.TokensOut
	if err != nil {
		res.Notes = "dispatch failed: " + err.Error()
		return res
	}
	v, parsed := parseVerdict(out.Text)
	res.Result, res.Notes, res.verdict = v.Verdict, v.Notes, parsed
	return res
}

func (r Run) performerModel() string {
	st, ok := r.Workflow.States[r.From]
	if !ok || st.Performer == nil {
		return ""
	}
	return r.Agents[st.Performer.Agent].Model
}

// prompt is what a judge is asked: the item, the move, and the change.
func (r Run) prompt(g workflow.Gate) (string, error) {
	it := r.Item
	var b strings.Builder
	fmt.Fprintf(&b, "Judge item %s moving from %q to %q.\n\n", it.ID, r.From, r.To)
	fmt.Fprintf(&b, "Kind: %s\nTitle: %s\n", it.Kind, it.Title)
	if len(it.Tags) > 0 {
		fmt.Fprintf(&b, "Tags: %s\n", strings.Join(it.Tags, ", "))
	}
	if it.Body != "" {
		fmt.Fprintf(&b, "\nBody:\n%s\n", it.Body)
	}
	if len(it.Criteria) > 0 {
		b.WriteString("\nAcceptance criteria:\n")
		for i, c := range it.Criteria {
			fmt.Fprintf(&b, "%d. %s\n", i+1, c)
		}
	}
	if r.Base != "" {
		fmt.Fprintf(&b, "\nBase commit: %s\n", r.Base)
	}
	if len(r.Changed) > 0 {
		fmt.Fprintf(&b, "Changed paths:\n%s\n", strings.Join(r.Changed, "\n"))
	}
	if r.Diff != nil {
		d, err := r.Diff(g.Paths)
		if err != nil {
			return "", fmt.Errorf("diff: %w", err)
		}
		const max = 200_000
		if len(d) > max {
			d = d[:max] + "\n… diff truncated …\n"
		}
		if d != "" {
			fmt.Fprintf(&b, "\nDiff:\n%s\n", d)
		}
	}
	b.WriteString(r.Record)
	b.WriteString("\nAnswer with your judgement, ending with one JSON object: {\"verdict\":\"accept\"|\"reject\",\"notes\":\"...\"}\n")
	return b.String(), nil
}
