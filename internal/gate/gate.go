// Package gate runs a transition's gate groups in order: sequential groups
// stop at the first reject or error, parallel groups run every gate. Each gate is
// first judged for relevance (paths, trigger), then for carry (an earlier
// accept with the same coverage), and only then run: a check skill as a
// script, any other skill by its judge or panel. Every gate yields a Result
// for the caller to record.
package gate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/logsread"
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
	// Error is a judge that could not be asked (its dispatch failed): not a
	// verdict, yet the transition is refused as on a reject.
	Error = "error"
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
	// Class is the judge's label for its blocking finding, as it gave it; the
	// gate does not interpret it.
	Class string `json:"class,omitempty"`
	// Members is a panel's verdict per member, in panel order.
	Members []Member `json:"members,omitempty"`
	// Location is where a remote-placed check ran: remote:<node>. "" is here.
	Location string `json:"location,omitempty"`
	// Change is the change a model gate (a skill that is not a check) judged
	// or carried: its span and the hash of the diff alone. Checks and skips
	// carry none.
	Change *Change `json:"change,omitempty"`
	// Answer is a single judge's raw answer, kept only when the gate's after
	// checks refused its verdict. A panel keeps it on each member instead.
	Answer string `json:"answer,omitempty"`

	// judges is each judge's own answer (one, or one per panel member in
	// panel order), for the after checks; never recorded.
	judges []judgeAnswer

	// verdict says the result is a verdict: a check that ran, or a judge
	// (every panel member) that answered with one. A reject that is a failure
	// (missing skill, dispatch error, unparseable answer) is not.
	verdict bool
}

// Change is what a model gate's verdict covers: the span it was shown (base,
// head commit, and the branch it was on, "" when detached) and the sha256 of
// exactly the diff text of its paths over that span. Unlike Coverage it does
// not move when the item's body or attachments do.
type Change struct {
	Base   string `json:"base"`
	Head   string `json:"head,omitempty"`
	Branch string `json:"branch,omitempty"`
	Hash   string `json:"hash"`
}

// TransitionKey names a move for carry and the ledger.
func TransitionKey(from, to string) string { return from + "->" + to }

// FromOf is the from-state of a TransitionKey.
func FromOf(key string) string {
	from, _, _ := strings.Cut(key, "->")
	return from
}

// ToOf is the to-state of a TransitionKey.
func ToOf(key string) string {
	_, to, _ := strings.Cut(key, "->")
	return to
}

// Member is one panel member's verdict within a panel's combined Result.
type Member struct {
	Binding string `json:"binding"`
	Result  string `json:"result"`
	Class   string `json:"class,omitempty"`
	// Answer is the member's raw answer, kept only when the gate's after
	// checks refused the panel's verdict.
	Answer string `json:"answer,omitempty"`
}

// judgeAnswer is one judge's own result, notes and raw answer text.
type judgeAnswer struct {
	binding, result, notes, answer string
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
	// Coverage hashes what a gate with these paths judges (coverage), and the
	// diff alone (change); nil disables carry.
	Coverage func(paths []string) (coverage, change string, err error)
	// Span is the base, head and branch of what this presentation judges; a
	// model gate's result records it with the change hash.
	Span Change
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
	// Remote runs a check gate placed remote:<id>; nil refuses it.
	Remote RemoteChecker
	// Head is the pushed head a remote check judges; HeadErr says why there
	// is none. The caller sets them only when a gate is placed on a remote.
	Head    Head
	HeadErr error
	// Logs is the runtime logs dir a judge's dispatch log is written under;
	// "" writes none.
	Logs string

	skipWarning string // diffWarning, computed once per Execute
}

// Refuses reports whether a result refuses the transition: a reject, or an
// error (a judge that could not be asked).
func Refuses(result string) bool { return result == Reject || result == Error }

// Execute runs the groups and returns the results in run order and whether
// the transition may proceed (no gate rejected or errored).
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
				if Refuses(out.Result) {
					break
				}
			}
		}
		all = append(all, res...)
		for _, x := range res {
			if Refuses(x.Result) {
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
	if g, ok := r.Workflow.Gates[name]; ok && len(g.After) > 0 && (res.verdict || res.Result == Carried) {
		res = r.after(ctx, name, g, res)
	}
	if (res.Result == Accept || res.Result == Reject) && res.Coverage != "" {
		res.Changed = r.Changed
	}
	res.DurationMS = time.Since(start).Milliseconds()
	return res
}

// after runs a gate's after checks on its verdict, in order. Each sees the
// result as it stands and every judge's own answer; exit 0 keeps the result
// and its output (if any) joins the notes. A non-zero exit or an error turns
// the result into a reject with its output, and the checks after it do not
// run (each is noted as not run). A refused judged verdict keeps the raw
// answer: on the result for one judge, on each member for a panel.
func (r Run) after(ctx context.Context, name string, g workflow.Gate, res Result) Result {
	refusedBy := ""
	for _, skillName := range g.After {
		if refusedBy != "" {
			res.Notes = joinNotes(res.Notes, fmt.Sprintf("after %s: not run: after %s refused the verdict", skillName, refusedBy))
			continue
		}
		if !r.afterOne(ctx, name, g, skillName, &res) {
			res.Result = Reject
			refusedBy = skillName
		}
	}
	if refusedBy != "" {
		keepAnswer(&res)
	}
	return res
}

// afterOne runs one after check on res, adding its notes; false when it
// refused the verdict (missing, failed to run, or a non-zero exit).
func (r Run) afterOne(ctx context.Context, name string, g workflow.Gate, skillName string, res *Result) bool {
	skill, ok := r.Skills[skillName]
	if !ok {
		res.Notes = joinNotes(res.Notes, fmt.Sprintf("after skill %q is missing", skillName))
		return false
	}
	env := r.env()
	env.Gate, env.Result, env.Binding, env.Notes = name, res.Result, res.Binding, res.Notes
	for _, j := range res.judges {
		env.Members = append(env.Members, CheckMember{Binding: j.binding, Result: j.result, Notes: j.notes})
	}
	limit, source, berr := r.bound(name, g)
	if berr != nil {
		res.Notes = joinNotes(res.Notes, fmt.Sprintf("after %s: %v", skillName, berr))
		return false
	}
	passed, out, err := RunCheck(ctx, skill.Script, r.Dir, env, limit)
	switch {
	case err != nil:
		res.Notes = joinNotes(res.Notes, fmt.Sprintf("after %s: %s", skillName, checkFailure(err, source, out)))
	case passed && out == "":
	default:
		res.Notes = joinNotes(res.Notes, fmt.Sprintf("after %s: %s", skillName, out))
	}
	return err == nil && passed
}

// keepAnswer records the raw answer of each judge on a refused verdict.
func keepAnswer(res *Result) {
	if len(res.Members) == 0 {
		if len(res.judges) == 1 {
			res.Answer = res.judges[0].answer
		}
		return
	}
	if len(res.judges) != len(res.Members) {
		return
	}
	for i := range res.Members {
		res.Members[i].Answer = res.judges[i].answer
	}
}

// bound is what limits each of the gate's check scripts, and where that
// limit comes from: the gate's own timeout, else the run's, else the default.
// A timeout that does not parse is an error: the gate fails closed.
func (r Run) bound(name string, g workflow.Gate) (time.Duration, string, error) {
	d, err := g.Bound()
	if err != nil {
		return 0, "", fmt.Errorf("timeout %q on gates.%s: %w", g.Timeout, name, err)
	}
	if d > 0 {
		return d, "gates." + name + " timeout", nil
	}
	def := fmt.Sprintf("default; set timeout on [gates.%s] to raise it", name)
	if r.CheckTimeout > 0 {
		return r.CheckTimeout, def, nil
	}
	return DefaultCheckTimeout, def, nil
}

// checkFailure is the notes of a check that failed to run: a timeout names
// its bound and where the bound came from; any other error is given as is.
func checkFailure(err error, source, out string) string {
	var te *TimeoutError
	if errors.As(err, &te) {
		return fmt.Sprintf("timed out after %v (%s)\n%s", te.Bound, source, out)
	}
	return fmt.Sprintf("%v\n%s", err, out)
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
	limit, source, err := r.bound(name, g)
	if err != nil {
		return Result{Result: Reject, Notes: err.Error()}
	}
	if len(g.Paths) > 0 && !MatchAny(g.Paths, r.Changed) {
		return Result{Result: Skipped, Notes: fmt.Sprintf("no changed path matches %v", g.Paths), Warning: r.skipWarning}
	}
	if g.Trigger != "" {
		trig, ok := r.Skills[g.Trigger]
		if !ok {
			return Result{Result: Reject, Notes: fmt.Sprintf("trigger skill %q is missing", g.Trigger)}
		}
		code, out, err := RunCheckCode(ctx, trig.Script, r.Dir, r.env(), limit)
		switch {
		case err != nil:
			return Result{Result: Reject, Notes: fmt.Sprintf("trigger %s: %s", g.Trigger, checkFailure(err, source, out))}
		case code == 0:
		case code == 1:
			return Result{Result: Skipped, Notes: fmt.Sprintf("trigger %s found nothing relevant\n%s", g.Trigger, out), Warning: r.skipWarning}
		default:
			// Any other exit is a trigger that failed: the gate runs anyway
			// (fails closed) and its notes say so first.
			how := fmt.Sprintf("exit %d", code)
			if code < 0 {
				how = "killed by a signal"
			}
			res := r.judged(ctx, name, g, limit, source)
			res.Notes = joinNotes(fmt.Sprintf("trigger %s failed (%s); the gate ran\n%s", g.Trigger, how, out), res.Notes)
			return res
		}
	}
	return r.judged(ctx, name, g, limit, source)
}

// judged is a relevant gate's outcome: carried, or run by its check or judge.
func (r Run) judged(ctx context.Context, name string, g workflow.Gate, limit time.Duration, source string) Result {
	skill, ok := r.Skills[g.Skill]
	coverage := ""
	var change *Change
	if r.Coverage != nil {
		c, ch, err := r.Coverage(g.Paths)
		if err != nil {
			return Result{Result: Reject, Notes: fmt.Sprintf("coverage: %v", err)}
		}
		coverage = c
		if !skill.IsCheck() {
			change = &Change{Base: r.Span.Base, Head: r.Span.Head, Branch: r.Span.Branch, Hash: ch}
		}
		if g.Carry && r.carried(name, c) {
			return Result{Result: Carried, Coverage: c, Change: change, Notes: "an earlier accept covers the same change"}
		}
	}
	if !ok {
		return Result{Result: Reject, Notes: fmt.Sprintf("skill %q is missing", g.Skill), Coverage: coverage}
	}
	var res Result
	switch {
	case skill.IsCheck() && strings.HasPrefix(g.Placement, agent.PlaceRemote):
		res = r.remote(ctx, name, g, skill, limit, source)
	case skill.IsCheck():
		passed, out, err := RunCheck(ctx, skill.Script, r.Dir, r.env(), limit)
		res = Result{Result: Reject, Notes: out}
		if err != nil {
			res.Notes = checkFailure(err, source, out)
		} else {
			res.verdict = true
			if passed {
				res.Result = Accept
			}
		}
	default:
		res = r.judge(ctx, g, skill)
	}
	res.Coverage = coverage
	if res.Result == Accept || res.Result == Reject {
		res.Change = change
	}
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
	accepts, rejects := 0, 0
	var notes []string
	for _, m := range members {
		switch m.Result {
		case Accept:
			accepts++
		case Reject:
			rejects++
		}
		j := judgeAnswer{binding: m.Binding, result: m.Result, notes: m.Notes}
		if len(m.judges) == 1 {
			j = m.judges[0]
		}
		out.judges = append(out.judges, j)
		out.verdict = out.verdict && m.verdict
		out.TokensIn += m.TokensIn
		out.TokensOut += m.TokensOut
		out.Members = append(out.Members, Member{Binding: m.Binding, Result: m.Result, Class: m.Class})
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
	// A panel that does not pass rejects when any member rejected; when none
	// did, its members could not be asked and the panel is an error.
	switch {
	case pass:
		out.Result = Accept
	case rejects > 0:
		out.Result = Reject
	default:
		out.Result = Error
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
	req := agent.Request{Binding: b, System: skill.Body, Prompt: prompt, Dir: r.Dir}
	if r.Logs != "" {
		req.Log = logsread.DispatchPath(r.Logs, name, r.Item.ID, time.Now().UnixNano())
	}
	out, err := r.Dispatcher.Dispatch(ctx, agent.Judge, req)
	res.TokensIn, res.TokensOut = out.TokensIn, out.TokensOut
	if err != nil {
		// The judge was never asked: an error, not its verdict.
		res.Result, res.Notes = Error, "dispatch failed: "+err.Error()
		return res
	}
	v, parsed := parseVerdict(out.Text)
	res.Result, res.Notes, res.Class, res.verdict = v.Verdict, v.Notes, v.Class, parsed
	res.judges = []judgeAnswer{{binding: name, result: v.Verdict, notes: v.Notes, answer: out.Text}}
	return res
}

func (r Run) performerModel() string {
	st, ok := r.Workflow.States[r.From]
	if !ok || st.Agent() == nil {
		return ""
	}
	return r.Agents[st.Agent().Agent].Model
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
