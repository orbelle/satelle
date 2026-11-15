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

const KindRun = "gate.run"

const (
	Accept  = "accept"
	Reject  = "reject"
	Skipped = "skipped"
	Carried = "carried"
	Error   = "error"
)

type Result struct {
	Transition string   `json:"transition"`
	Gate       string   `json:"gate"`
	Binding    string   `json:"binding,omitempty"`
	Result     string   `json:"result"`
	Notes      string   `json:"notes,omitempty"`
	TokensIn   int      `json:"tokens_in"`
	TokensOut  int      `json:"tokens_out"`
	DurationMS int64    `json:"duration_ms"`
	Coverage   string   `json:"coverage,omitempty"`
	Attempt    string   `json:"attempt,omitempty"`
	Changed    []string `json:"changed,omitempty"`
	Warning    string   `json:"warning,omitempty"`
	Class      string   `json:"class,omitempty"`
	Members    []Member `json:"members,omitempty"`
	Location   string   `json:"location,omitempty"`
	Change     *Change  `json:"change,omitempty"`
	Answer     string   `json:"answer,omitempty"`

	judges []judgeAnswer

	verdict bool
}

type Change struct {
	Base   string `json:"base"`
	Head   string `json:"head,omitempty"`
	Branch string `json:"branch,omitempty"`
	Hash   string `json:"hash"`
}

func TransitionKey(from, to string) string { return from + "->" + to }

func FromOf(key string) string {
	from, _, _ := strings.Cut(key, "->")
	return from
}

func ToOf(key string) string {
	_, to, _ := strings.Cut(key, "->")
	return to
}

type Member struct {
	Binding string `json:"binding"`
	Result  string `json:"result"`
	Class   string `json:"class,omitempty"`
	Answer  string `json:"answer,omitempty"`
}

type judgeAnswer struct {
	binding, result, notes, answer string
}

type Run struct {
	Item       item.Item
	From, To   string
	Transition *workflow.Transition
	Workflow   *workflow.Workflow
	Skills     map[string]workflow.Skill
	Agents     agent.Catalog
	Dispatcher agent.Dispatcher

	Dir             string
	Base            string
	Changed         []string
	Coverage        func(paths []string) (coverage, change string, err error)
	Span            Change
	Diff            func(paths []string) (string, error)
	Prior           []Result
	ReviewerDiffers bool
	CheckTimeout    time.Duration
	Record          string
	Remote          RemoteChecker
	Head            Head
	HeadErr         error
	Logs            string

	skipWarning string
}

func Refuses(result string) bool { return result == Reject || result == Error }

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
