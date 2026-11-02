package agentstep

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/reviewscore"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// CaseJudgeOptions configure a CaseJudge.
type CaseJudgeOptions struct {
	// Root is the repo root; its .satelle/ supplies agents.toml and the
	// constitution the reviewer is shown.
	Root string
	// Binding is the agents.toml section that judges.
	Binding string
	// Docs serves skills and principles (workflows are synthesised per case).
	Docs DocGetter
	// Resolve and NewRunner are seams for tests; the defaults read agents.toml
	// and start the binding's real agent CLI.
	Resolve   func(name string) (config.AgentBinding, bool)
	NewRunner func(iface, command string) (agentcli.Runner, error)
	// Offload keeps an oversized diff's overflow out of line, exactly as a real
	// dispatch's does; nil keeps none.
	Offload func(ctx context.Context, itemID string, content []byte) (string, error)
	// NoConstitution keeps the repo's constitution.md out of the reviewer's
	// prompt: a judge that reads the constitution itself, from a pointer, is
	// handed none inline.
	NoConstitution bool
}

// CaseJudge judges one known case with a named reviewer binding through
// Engine.Gate — the isolated reviewer path a real gate uses — over a one-edge
// workflow synthesised for the case's skill. It writes no ledger rows.
type CaseJudge struct {
	opts    CaseJudgeOptions
	adapter string
}

// NewCaseJudge resolves the binding up front, so an unknown binding or a missing
// agent CLI is one clear error rather than one per case.
func NewCaseJudge(opts CaseJudgeOptions) (*CaseJudge, error) {
	if opts.Binding == "" {
		return nil, fmt.Errorf("a reviewer binding name is required")
	}
	if opts.Resolve == nil {
		agents, err := config.LoadEffectiveAgents(filepath.Join(opts.Root, config.DefaultDataDir), nil)
		if err != nil {
			return nil, fmt.Errorf("load agents.toml: %w", err)
		}
		opts.Resolve = func(name string) (config.AgentBinding, bool) {
			b, ok := agents.Agents.RawBinding(name)
			if !ok {
				return config.AgentBinding{}, false
			}
			return agents.Agents.EffectiveBinding(b, config.UseOneShot), true
		}
	}
	if opts.NewRunner == nil {
		opts.NewRunner = LookupRunner
	}
	b, ok := opts.Resolve(opts.Binding)
	if !ok {
		return nil, fmt.Errorf("no [%s] binding in agents.toml", opts.Binding)
	}
	r, err := opts.NewRunner(b.ResolvedInterface(), b.CommandTemplate())
	if err != nil {
		return nil, fmt.Errorf("runner for [%s]: %w", opts.Binding, err)
	}
	return &CaseJudge{opts: opts, adapter: r.Name()}, nil
}

// Adapter names the agent CLI adapter behind the binding.
func (j *CaseJudge) Adapter() string { return j.adapter }

// Judge implements reviewscore.Judge: the case's own skill, alone.
func (j *CaseJudge) Judge(ctx context.Context, c reviewscore.Case) (reviewscore.JudgeResult, error) {
	return j.run(ctx, c, []string{c.Skill}, false)
}

// JudgeBundled judges the case as one section of a bundled session carrying every
// rubric in skills (sty_23e10d92), and returns the verdict for the case's own.
func (j *CaseJudge) JudgeBundled(ctx context.Context, c reviewscore.Case, skills []string) (reviewscore.JudgeResult, error) {
	return j.run(ctx, c, skills, true)
}

func (j *CaseJudge) run(ctx context.Context, c reviewscore.Case, skills []string, bundle bool) (reviewscore.JudgeResult, error) {
	b, _ := j.opts.Resolve(j.opts.Binding)
	runner, err := j.opts.NewRunner(b.ResolvedInterface(), b.CommandTemplate())
	if err != nil {
		return reviewscore.JudgeResult{}, fmt.Errorf("runner for [%s]: %w", j.opts.Binding, err)
	}
	from, to := c.From, c.To
	if from == "" {
		from = "backlog"
	}
	if to == "" {
		to = "in_progress"
	}
	g := New(nil, caseDocs{wf: caseWorkflow(skills, j.opts.Binding, from, to, bundle), real: j.opts.Docs}, j.opts.Root, "")
	g.SetRunner(runner)
	g.SetReviewerBinding(b)
	g.SetNamedAgents(func(name string) (config.AgentBinding, bool) { return j.opts.Resolve(name) })
	if j.opts.Offload != nil {
		g.SetDiffOffloader(j.opts.Offload)
	}
	if body, err := os.ReadFile(filepath.Join(j.opts.Root, config.DefaultDataDir, "constitution.md")); err == nil && !j.opts.NoConstitution {
		g.SetConstitution(string(body))
	}
	item := workitem.Item{ID: c.StoryID, Kind: workitem.KindStory, Status: from, Category: "feature", Title: c.ID, Body: c.Summary}
	if p := c.Payload; p != nil {
		item.Title, item.Body, item.AcceptanceCriteria, item.Tags = p.Title, p.Body, p.AcceptanceCriteria, p.Tags
		if p.Category != "" {
			item.Category = p.Category
		}
		docs := make([]DocState, 0, len(p.Docs))
		for _, d := range p.Docs {
			docs = append(docs, DocState{Name: d.Name, Type: d.Type, Body: d.Body})
		}
		g.SetDocsResolver(func(context.Context, string) []DocState { return docs })
		if len(p.PriorVerdicts) > 0 {
			g.SetPriorVerdictsResolver(func(context.Context, string, string, string, verb.StaleExemption) []PriorVerdict {
				out := make([]PriorVerdict, 0, len(p.PriorVerdicts))
				for _, v := range p.PriorVerdicts {
					out = append(out, PriorVerdictFrom(v))
				}
				return out
			})
		}
		if len(p.DefinitionEdits) > 0 {
			g.SetDefinitionEditsResolver(func(context.Context, string) []DefinitionEdit {
				out := make([]DefinitionEdit, 0, len(p.DefinitionEdits))
				for _, e := range p.DefinitionEdits {
					out = append(out, DefinitionEdit{Field: e.Field, Old: e.Old, New: e.New, Actor: e.Actor, At: e.At})
				}
				return out
			})
		}
		if patch := p.Patch; patch != "" {
			g.SetDiffResolver(func(context.Context, string) *DiffState {
				return &DiffState{Patch: patch, Source: "replay " + c.ID}
			})
		}
	} else {
		diff := c.Diff
		g.SetDiffResolver(func(context.Context, string) *DiffState {
			return &DiffState{Patch: diff, Source: "reviewcorpus " + c.ID}
		})
	}
	start := time.Now()
	dec, err := g.Gate(ctx, item, to)
	wall := time.Since(start)
	if err != nil {
		return reviewscore.JudgeResult{Wall: wall}, fmt.Errorf("gate: %w", err)
	}
	for _, rv := range dec.Reviewers {
		if rv.Skill == c.Skill {
			return judgeResult(rv, wall), nil
		}
	}
	return reviewscore.JudgeResult{Wall: wall}, nil
}

// judgeResult maps a reviewer verdict to the score's judgement. A figure the
// adapter did not report keeps the reason the engine recorded for it — never a
// zero.
func judgeResult(rv verb.ReviewerVerdict, wall time.Duration) reviewscore.JudgeResult {
	res := reviewscore.JudgeResult{Verdict: reviewscore.VerdictReject, Notes: rv.Notes, Model: rv.ModelResolved, Wall: wall}
	if rv.Accept {
		res.Verdict = reviewscore.VerdictAccept
	}
	if rv.Reasoning != "" {
		res.Notes = strings.TrimSpace(res.Notes + "\n" + rv.Reasoning)
	}
	if rv.UsageAvailable {
		res.Usage = reviewscore.Usage{Available: true, TokensIn: rv.TokensIn, TokensOut: rv.TokensOut, CostUSD: rv.CostUSD, CostReason: rv.CostUnavailableReason}
		return res
	}
	reason := rv.UsageUnavailableReason
	if reason == "" {
		reason = "usage not reported"
	}
	res.Usage = reviewscore.Usage{Reason: "unavailable (" + reason + ")"}
	return res
}

// caseWorkflow synthesises the one-edge route a case is judged under: from→to,
// the skills gating the entry to `to`, run by the named reviewer binding.
func caseWorkflow(skills []string, binding, from, to string, bundle bool) []docindex.Doc {
	obligations := []string{`"raised"`, `"ob-` + to + `"`}
	var step strings.Builder
	step.WriteString("[raised]\nstatus = \"" + from + "\"\nstart = true\n\n")
	step.WriteString("[ob-" + to + "]\nstatus = \"" + to + "\"\nagent = \"executor\"\n")
	step.WriteString("reviewers = [\"" + strings.Join(skills, "\", \"") + "\"]\nreviewer_agent = \"" + binding + "\"\n")
	if bundle {
		step.WriteString("bundle = true\n")
	}
	terminal := to == "done"
	if terminal {
		step.WriteString("terminal = true\n")
	}
	step.WriteString("requires = [\"raised\"]\n\n")
	if !terminal {
		obligations = append(obligations, `"ob-done"`)
		step.WriteString("[ob-done]\nstatus = \"done\"\nterminal = true\nrequires = [\"ob-" + to + "\"]\n")
	}
	// The shipped always-on gates stay out of a scoring route: the case is
	// judged by its own skill alone.
	for _, s := range []string{"satelle-step-summary", "satelle-estimate-actual-review"} {
		step.WriteString("\n[[gate]]\nskill = \"" + s + "\"\nfor = [\"unused\"]\n")
	}
	var done strings.Builder
	for _, cat := range []string{`"*"`, "docs", "epic-parent", "parent", "execution", "task"} {
		done.WriteString("[" + cat + "]\nobligations = [" + strings.Join(obligations, ", ") + "]\n\n")
	}
	head := func(name, what string) string {
		return "[meta]\nname = \"" + name + "\"\ntype = \"workflow\"\ndescription = \"" + what + "\"\nscope = \"system\"\n\n"
	}
	return []docindex.Doc{
		{Kind: "workflows", Name: "done", Body: head("done", "reviewer scoring route") + done.String()},
		{Kind: "workflows", Name: "step", Body: head("step", "reviewer scoring steps") + step.String()},
	}
}

// caseDocs serves the synthesised workflow and delegates skills and principles
// to the repo's real substrate.
type caseDocs struct {
	wf   []docindex.Doc
	real DocGetter
}

func (d caseDocs) Get(ctx context.Context, kind, name string) (docindex.Doc, error) {
	if kind == "workflows" {
		for _, w := range d.wf {
			if w.Name == name {
				return w, nil
			}
		}
		return docindex.Doc{}, docindex.ErrNotFound
	}
	return d.real.Get(ctx, kind, name)
}

func (d caseDocs) List(ctx context.Context, kind string) ([]docindex.Doc, error) {
	if kind == "workflows" {
		return d.wf, nil
	}
	return d.real.List(ctx, kind)
}
