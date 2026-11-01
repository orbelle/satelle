// doctor_context.go — `satelle doctor` checks the context every agent actually
// receives (the part of the context audit that is not a model call).
//
// Two kinds of context reach an agent: the SessionStart injection of the harness
// driving the session, and the system prompt satelle assembles for each
// dispatched seat (planner, coder, reviewers, summariser, consult). This file
// assembles both from the substrate — the same renderers the hook and the
// dispatch use, so the thing judged is the thing delivered — and reports:
//
//   - whether the SessionStart context fits each bound harness's limit,
//   - whether the per-prompt reminder fits its harness's declared clip,
//   - passages copied word-for-word between two documents,
//   - references that name nothing live.
//
// None of it decides what a context SHOULD contain; each finding reports a
// measurable fact against configuration. The semantic half (paraphrase) is the
// opt-in contextReview below. Doctor injects these as Opts.ContextChecks and
// Opts.ContextReview because they need renderers that live here and doctor does
// not import cli.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/agentstep"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/ctxaudit"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/doctor"
	"github.com/bobmcallan/satelle/internal/health"
	"github.com/bobmcallan/satelle/internal/structure"
	"github.com/bobmcallan/satelle/internal/wfhook"
)

// contextAudit is everything the context checks share: the contexts an agent
// receives, the corpus they are compared against, and the facts resolved once.
type contextAudit struct {
	repoRoot, dataDir string
	cfg               config.Config
	docs              fsDocs
	env               injectedEnv
	constitution      ctxaudit.Doc // Body "" when the repo has none
	fits              []sessionFit
	gaps              []harnessGap
	contexts          []ctxaudit.Context
	corpus            []ctxaudit.Doc
	skills            map[string]bool
	vars              map[string]string // the layered ${NAME} KV (machine catalog under the repo's)
}

func relTo(repoRoot, path string) string {
	if strings.HasPrefix(path, "embedded:") {
		return path
	}
	if rel, err := filepath.Rel(repoRoot, path); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(path)
}

func (a *contextAudit) toDoc(d docindex.Doc) ctxaudit.Doc {
	out := ctxaudit.Doc{Kind: d.Kind, Name: d.Name, Where: relTo(a.repoRoot, d.Path), Body: d.Body}
	if d.Kind == "principles" {
		out.Residency = "ondemand"
		if docHasTag(d.Body, sessionTag) {
			out.Residency = "session"
		} else if docHasTag(d.Body, config.SeatResidencyTag) {
			out.Residency = "seat"
		}
	}
	return out
}

// assembleContexts resolves the contexts and the corpus for one repo.
func assembleContexts(ctx context.Context, repoRoot, dataDir string) *contextAudit {
	a := &contextAudit{repoRoot: repoRoot, dataDir: dataDir, docs: fsDocs{dataDir}, skills: map[string]bool{}}
	if cfg, _, err := config.Load(filepath.Join(dataDir, config.ConfigName)); err == nil {
		a.cfg = cfg
	}
	constPath := filepath.Join(dataDir, config.DefaultConstitutionName)
	constitution := readConstitution(constPath)
	if constitution != "" {
		a.constitution = ctxaudit.Doc{Kind: "constitution", Name: "constitution", Where: relTo(repoRoot, constPath), Body: constitution}
	}
	workflows := doctor.GoverningWorkflows(dataDir)
	rules, _ := condenseRules(dataDir)
	a.env = injectedEnv{Cfg: a.cfg, Docs: a.docs, Workflows: workflows, Constitution: constitution, ConstPath: constPath, Rules: rules}
	eff, err := config.LoadEffectiveAgents(dataDir, a.cfg.Vars)
	a.env.Agents, a.env.AgentsErr = eff.Agents, err
	a.vars = eff.Vars

	principles, _ := a.docs.List(ctx, "principles")
	skills, _ := a.docs.List(ctx, "skills")
	if a.constitution.Body != "" {
		a.corpus = append(a.corpus, a.constitution)
	}
	for _, d := range principles {
		a.corpus = append(a.corpus, a.toDoc(d))
	}
	for _, d := range skills {
		a.corpus = append(a.corpus, a.toDoc(d))
		a.skills[d.Name] = true
	}

	a.fits, a.gaps = sessionFits(dataDir, repoRoot)
	resident := selectAlwaysDocs(principles)
	for _, f := range a.fits {
		c := ctxaudit.Context{Label: "session[" + f.Harness.Name + "]"}
		if a.constitution.Body != "" {
			c.Parts = append(c.Parts, a.constitution)
		}
		for _, d := range resident {
			c.Parts = append(c.Parts, a.toDoc(d))
		}
		a.contexts = append(a.contexts, c)
	}
	a.contexts = append(a.contexts, a.seatContexts(ctx)...)
	return a
}

// seatContexts assembles what each dispatched seat is handed: the constitution
// and the principles its binding injects, then the skills its route gives it.
// A seat that cannot be dispatched yields no context here; dispatchedRows
// reports why.
func (a *contextAudit) seatContexts(ctx context.Context) []ctxaudit.Context {
	if a.env.AgentsErr != nil {
		return nil
	}
	var out []ctxaudit.Context
	for _, seat := range routeSeats(a.env.Workflows) {
		binding, ok := bindingFor(a.env.Agents, seat.section)
		if !ok || config.IsInLoopCommand(binding.CommandTemplate()) ||
			agentcli.AdapterName(binding.CommandTemplate()) == agentcli.HarnessUnknown {
			continue
		}
		c := ctxaudit.Context{Label: "seat[" + seat.section + "]"}
		if binding.InjectsPrinciples() {
			if a.constitution.Body != "" {
				c.Parts = append(c.Parts, a.constitution)
			}
			for _, d := range agentstep.ResidentPrincipleDocs(ctx, a.docs, binding.ResolvedPrinciples()) {
				if seat.judges && d.Name == config.ReviewerVerdictPrinciple {
					continue // delivered once below, as the dispatch composes it
				}
				c.Parts = append(c.Parts, a.toDoc(d))
			}
		}
		// A judging seat is handed the shared verdict rules once, as its dispatch
		// composes them, so the audit sees them where the skills no longer restate them.
		if seat.judges && agentstep.ReviewerVerdictDoc(ctx, a.docs) != "" {
			if d, err := a.docs.Get(ctx, "principles", config.ReviewerVerdictPrinciple); err == nil {
				c.Parts = append(c.Parts, a.toDoc(d))
			}
		}
		for _, name := range seat.skills {
			d, err := a.docs.Get(ctx, "skills", name)
			if err != nil || structure.CheckCommand(d.Body) != "" {
				continue // unresolved, or a script that never enters a prompt
			}
			c.Parts = append(c.Parts, a.toDoc(d))
		}
		out = append(out, c)
	}
	return out
}

// contextChecks is Opts.ContextChecks: every deterministic context finding.
func contextChecks(ctx context.Context, repoRoot, dataDir string) health.Findings {
	a := assembleContexts(ctx, repoRoot, dataDir)
	var out health.Findings
	out = append(out, a.sessionFindings()...)
	out = append(out, a.reminderFindings()...)
	out = append(out, a.duplicateFindings()...)
	out = append(out, a.baselineOverrideFindings(ctx)...)
	out = append(out, a.referenceFindings(ctx)...)
	return out
}

// sessionFindings — the SessionStart context against each bound harness's limit.
func (a *contextAudit) sessionFindings() health.Findings {
	var out health.Findings
	for _, f := range a.fits {
		if len(f.Omitted) == 0 {
			continue
		}
		out = append(out, health.Warn(health.IDContextSessionOverflow, "SessionStart context overflows a harness limit",
			fmt.Sprintf("harness %s: the SessionStart context renders %d %s against its limit of %d %s, so these are omitted to read pointers: %s",
				f.Harness.Name, f.Rendered, f.Harness.Unit, f.Harness.Limit, f.Harness.Unit, strings.Join(f.Omitted, ", "))).
			About(f.Harness.Name).
			WithRemediation("trim the constitution or a principles:session principle, or raise [harness."+f.Harness.Name+"] "+limitKey(f.Harness)+" if that harness really delivers more"))
	}
	for _, g := range a.gaps {
		out = append(out, health.Info(health.IDContextSessionUnavailable, "SessionStart context cannot be judged",
			fmt.Sprintf("unavailable: %s: %s", g.Subject, g.Reason)).About(g.Subject))
	}
	return out
}

// limitKey names the [harness.<name>] key that sets h's limit.
func limitKey(h harnessBinding) string {
	if h.Unit == "characters" {
		return "tool_context_limit_chars"
	}
	return "context_limit_bytes"
}

// reminderFindings — the text the prompt hook adds to every turn, against the
// clip each bound harness declares for it ([harness.<name>]
// tool_context_limit_chars). That clip is the harness's channel limit, like the
// SessionStart cap — not a satelle budget — so only an overrun is reported.
func (a *contextAudit) reminderFindings() health.Findings {
	var out health.Findings
	chars := utf8.RuneCountInString(hookPromptReminder)
	for _, f := range a.fits {
		h := f.Harness.Name
		label := "reminder[" + h + "]"
		if !agentcli.FactsFor(h).PromptContext.Available {
			continue
		}
		if limit := a.cfg.ToolContextLimitChars(h); limit > 0 && chars > limit {
			out = append(out, health.Warn(health.IDContextReminderOverClip, "Per-prompt reminder is over the harness clip",
				fmt.Sprintf("%s is %d characters, over [harness.%s] tool_context_limit_chars = %d", label, chars, h, limit)).About(label))
		}
	}
	return out
}

// duplicateFindings — verbatim duplicated guidance, in every assembled context,
// compared against the whole substrate corpus (including documents the context
// never receives). Deterministic: no model, no network.
func (a *contextAudit) duplicateFindings() health.Findings {
	min := a.cfg.ResolveDuplicateMinWords()
	if min <= 0 {
		return health.Findings{health.Info(health.IDContextDuplicateVerbatim, "Duplicate check not run",
			"no duplicate_min_words resolves from satelle.toml [doctor] or the embedded doctor.toml, so verbatim duplication was not checked")}
	}
	var out health.Findings
	for _, g := range ctxaudit.GroupDuplicates(ctxaudit.Duplicates(a.contexts, a.corpus, min), min) {
		wheres := make([]string, 0, len(g.Docs))
		for _, d := range g.Docs {
			wheres = append(wheres, d.Where)
		}
		who := wheres[0] + " and " + wheres[1]
		if len(wheres) > 2 {
			who = fmt.Sprintf("%d documents (%s)", len(wheres), strings.Join(wheres, ", "))
		}
		out = append(out, health.Warn(health.IDContextDuplicateVerbatim, "Duplicated guidance",
			fmt.Sprintf("%s share %d words verbatim (seen in %s): %q", who, g.Words, g.Context, g.Quote)).
			About(wheres[0]).
			WithRemediation("keep the passage in one document and refer to it by [[name]] in the others (a paraphrase is found by `satelle doctor --context-review`)"))
	}
	return out
}

// baselinePrinciples are the embedded principles every repo's authored
// principles and skills are checked against (sty_bcd344fd): the story shape and
// start-small baselines a workflow is grounded in.
var baselinePrinciples = []string{"satelle-story-shape", "satelle-start-small"}

// baselineOverrideFindings — a repo document under principles or skills that
// shadows a baseline principle by name with a different body, or restates a
// passage of one word for word (the duplicate finder, at the configured
// threshold). Whether an override CONTRADICTS a baseline is a judgement, not a
// fact, so it is not decided here.
func (a *contextAudit) baselineOverrideFindings(ctx context.Context) health.Findings {
	baseline := map[string]ctxaudit.Doc{}
	for _, d := range config.EmbeddedDefaults() {
		if d.Kind == "principles" && slices.Contains(baselinePrinciples, d.Name) {
			baseline[d.Name] = ctxaudit.Doc{Kind: d.Kind, Name: d.Name, Where: "embedded:" + d.RelPath(), Body: d.Body}
		}
	}
	if len(baseline) == 0 {
		return nil
	}
	norm := func(body string) string { return strings.TrimSpace(ctxaudit.StripFrontmatter(body)) }
	var out health.Findings
	overrides := ctxaudit.Context{Label: "repo overrides"}
	for _, kind := range []string{"principles", "skills"} {
		listed, _ := a.docs.List(ctx, kind)
		for _, d := range listed {
			if d.Embedded {
				continue
			}
			doc := a.toDoc(d)
			if b, ok := baseline[d.Name]; ok {
				if norm(d.Body) != norm(b.Body) {
					out = append(out, health.Warn(health.IDContextBaselineOverride, "Baseline principle overridden",
						fmt.Sprintf("%s shadows %s with a different body", doc.Where, d.Name)).
						About(doc.Where).
						WithRemediation("delete the override so the embedded baseline applies, or keep only what differs in a document of another name that refers to [["+d.Name+"]]"))
				}
				continue // a shadow is reported as one; it is not also a restatement of itself
			}
			overrides.Parts = append(overrides.Parts, doc)
		}
	}
	min := a.cfg.ResolveDuplicateMinWords()
	if min <= 0 || len(overrides.Parts) == 0 {
		return out
	}
	corpus := make([]ctxaudit.Doc, 0, len(baseline))
	for _, name := range baselinePrinciples {
		if b, ok := baseline[name]; ok {
			corpus = append(corpus, b)
		}
	}
	for _, dup := range ctxaudit.Duplicates([]ctxaudit.Context{overrides}, corpus, min) {
		repo, base := dup.A, dup.B
		if _, ok := baseline[repo.Name]; ok && repo.Kind == "principles" {
			repo, base = base, repo
		}
		if _, ok := baseline[base.Name]; !ok || base.Kind != "principles" {
			continue // two repo documents sharing a passage: duplicateFindings reports that
		}
		out = append(out, health.Warn(health.IDContextBaselineOverride, "Baseline principle restated",
			fmt.Sprintf("%s restates %s: %q", repo.Where, base.Name, dup.Quote)).
			About(repo.Where).
			WithRemediation("refer to [["+base.Name+"]] instead of restating it, so the baseline is written once"))
	}
	return out
}

// taskSkillRe finds the skill a task names as its rubric (`@skill:<name>`).
var taskSkillRe = regexp.MustCompile(`@skill:([A-Za-z0-9][A-Za-z0-9_-]*)`)

// wiredSkills is every skill something references: a route step's skills,
// reviewers and gates, a done.toml create_review / park / cancel gate, a step
// summary, an advisor, a lifecycle hook, or a task's `@skill:`. The route
// enumeration is agentstep's, so doctor and the workflow consistency check agree
// about what a route names.
func wiredSkills(ctx context.Context, dataDir string, workflows []docindex.Doc) map[string]bool {
	wired := map[string]bool{}
	for _, w := range docindex.WithoutUnreadable(workflows) {
		for _, s := range agentstep.ReferencedWorkflowSkills(w.Body) {
			wired[s] = true
		}
		hooks, _ := wfhook.Parse(w.Body)
		for _, h := range hooks {
			wired[h.Skill] = true
		}
	}
	tasks, _ := fsDocs{dataDir}.List(ctx, "tasks")
	for _, t := range tasks {
		for _, m := range taskSkillRe.FindAllStringSubmatch(t.Body, -1) {
			wired[m[1]] = true
		}
	}
	return wired
}

// referenceFindings — every satelle reference in the assembled contexts and in
// every skill reachable from the repo's workflows and tasks that names nothing
// live, plus a stale documents index.
func (a *contextAudit) referenceFindings(ctx context.Context) health.Findings {
	wired := wiredSkills(ctx, a.dataDir, a.env.Workflows)
	env := ctxaudit.RefEnv{
		Names:  ctxaudit.Catalog(a.dataDir, config.EmbeddedDefaults()),
		Skills: a.skills,
		Wired:  wired,
	}
	// The texts to read: every part of every context, and every wired skill —
	// each once, whichever context carries it.
	texts := map[string]ctxaudit.Doc{}
	for _, c := range a.contexts {
		for _, p := range c.Parts {
			texts[p.Source()] = p
		}
	}
	for _, d := range a.corpus {
		if d.Kind == "skills" && wired[d.Name] {
			texts[d.Source()] = d
		}
	}
	keys := make([]string, 0, len(texts))
	for k := range texts {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var out health.Findings
	for _, k := range keys {
		d := texts[k]
		for _, p := range ctxaudit.CheckRefs(d.Body, d.Where, d.Name, env) {
			switch p.Kind {
			case ctxaudit.RefUnresolved:
				out = append(out, health.Warn(health.IDContextRefUnresolved, "Unresolved satelle reference",
					fmt.Sprintf("%s refers to %s, which %s", p.Where, p.Ref, p.Detail)).
					About(p.Where).WithRemediation("repoint the reference at a live artifact, or delete it"))
			case ctxaudit.RefUnwired:
				out = append(out, health.Info(health.IDContextRefUnwired, "Named skill is not wired",
					fmt.Sprintf("%s refers to %s, which %s", p.Where, p.Ref, p.Detail)).
					About(p.Where).WithRemediation("wire the skill into a route, hook or task, or stop naming it"))
			}
		}
	}
	for _, p := range ctxaudit.StaleIndex(a.dataDir) {
		out = append(out, health.Warn(health.IDContextIndexStale, "Stale documents index", p).
			About("documents/index.md").WithRemediation("run `satelle reindex`"))
	}
	return out
}

// ---------------------------------------------------------------------------
// The opt-in semantic half (`satelle doctor --context-review`).
// ---------------------------------------------------------------------------

// contextAuditSkill is the rubric the semantic review runs under. It is resolved
// through doctor.SkillBodyResolver, so an authored
// .satelle/skills/satelle-context-audit.md overrides the embedded default —
// including the output contract, which lives in that file and not in Go.
const contextAuditSkill = "satelle-context-audit"

// contextReviewRunner builds the runner for a reviewer binding. nil is the real
// one (agentcli); a test sets it to supply a stub binding's runner.
var contextReviewRunner func(iface, command string) (agentcli.Runner, error)

// reviewPart / reviewDoc are the payload shapes the rubric reads.
type reviewDoc struct {
	Source    string `json:"source"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Residency string `json:"residency,omitempty"`
	Where     string `json:"where"`
	Body      string `json:"body"`
}

type reviewPayload struct {
	Context struct {
		Label string      `json:"label"`
		Parts []reviewDoc `json:"parts"`
	} `json:"context"`
	// Corpus is the index of every principle and skill — authored and embedded,
	// resident or on-demand, linked or not — plus the constitution.
	Corpus []reviewDoc `json:"corpus"`
}

func toReviewDoc(d ctxaudit.Doc) reviewDoc {
	return reviewDoc{Source: d.Source(), Name: d.Name, Kind: d.Kind, Residency: d.Residency, Where: d.Where, Body: d.Body}
}

// reviewFinding is the default output contract's one finding. The contract is
// documented in the embedded rubric; Go only parses this shape.
type reviewFinding struct {
	Part     string `json:"part"`
	Restates string `json:"restates"`
	Kind     string `json:"kind"`
	Quote    string `json:"quote"`
}

var jsonObjectRe = regexp.MustCompile(`(?s)\{.*\}`)

// parseReview reads the rubric's reply: one JSON object with a findings array,
// possibly wrapped in prose or a code fence.
func parseReview(text string) ([]reviewFinding, error) {
	raw := jsonObjectRe.FindString(text)
	if raw == "" {
		return nil, fmt.Errorf("no JSON object in the reply")
	}
	var v struct {
		Findings []reviewFinding `json:"findings"`
	}
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, err
	}
	return v.Findings, nil
}

// contextReview is Opts.ContextReview: for each distinct context doctor
// assembled it runs the repo's configured reviewer binding, read-only and
// one-shot, over that context and the substrate corpus index, and reports each
// restatement it returns. A reviewer that cannot run, or whose reply does not
// parse, is an "unavailable" finding naming the adapter.
func contextReview(ctx context.Context, repoRoot, dataDir string) health.Findings {
	a := assembleContexts(ctx, repoRoot, dataDir)
	unavailable := func(adapter, why string) health.Findings {
		return health.Findings{health.Info(health.IDContextReviewUnavailable, "Semantic context review unavailable",
			fmt.Sprintf("unavailable: %s adapter: %s", adapter, why))}
	}
	rubric, ok := doctor.SkillBodyResolver(dataDir)(contextAuditSkill)
	if !ok {
		return unavailable("none", "the "+contextAuditSkill+" rubric resolves neither on disk nor as an embedded default")
	}
	if a.env.AgentsErr != nil {
		return unavailable("none", "agents.toml does not load: "+a.env.AgentsErr.Error())
	}
	resolved, rerr := config.ResolveAgentEnvs(a.env.Agents, a.vars)
	if rerr != nil {
		return unavailable("none", "reviewer env does not resolve: "+rerr.Error())
	}
	binding := resolved.ReviewerBinding()
	adapter := agentcli.AdapterName(binding.CommandTemplate())

	eng := agentstep.New(nil, a.docs, repoRoot, "")
	eng.SetWarnWriter(os.Stderr)
	if contextReviewRunner != nil {
		eng.SetRunnerFactory(contextReviewRunner)
	}

	corpus := make([]reviewDoc, 0, len(a.corpus))
	for _, d := range a.corpus {
		corpus = append(corpus, toReviewDoc(d))
	}
	whereOf := map[string]string{}
	for _, d := range a.corpus {
		whereOf[d.Source()] = d.Where
	}

	// Distinct contexts only: harnesses that receive the same parts are one call.
	seen := map[string]bool{}
	var distinct []ctxaudit.Context
	for _, c := range a.contexts {
		var sig []string
		for _, p := range c.Parts {
			sig = append(sig, p.Source())
		}
		key := strings.Join(sig, "\x00")
		if len(c.Parts) == 0 || seen[key] {
			continue
		}
		seen[key] = true
		distinct = append(distinct, c)
	}

	type hit struct{ label, part, other, kind, quote string }
	hits := make([][]hit, len(distinct))
	problems := make([]health.Findings, len(distinct))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 3)
	for i, c := range distinct {
		wg.Add(1)
		go func(i int, c ctxaudit.Context) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			var p reviewPayload
			p.Context.Label = c.Label
			for _, part := range c.Parts {
				p.Context.Parts = append(p.Context.Parts, toReviewDoc(part))
			}
			p.Corpus = corpus
			res, err := eng.ReviewText(ctx, agentstep.ReviewTextRequest{
				Section: config.RoleReviewer, Binding: binding, Skill: contextAuditSkill, Rubric: rubric, Payload: p,
			})
			who := adapter
			if res.Adapter != "" {
				who = res.Adapter
			}
			if err != nil {
				problems[i] = unavailable(who, c.Label+": "+err.Error())
				return
			}
			found, perr := parseReview(res.Text)
			if perr != nil {
				problems[i] = unavailable(who, c.Label+": the reviewer's output did not parse: "+perr.Error())
				return
			}
			for _, f := range found {
				part, other := whereOf[f.Part], whereOf[f.Restates]
				if part == "" {
					part = f.Part
				}
				if other == "" {
					other = f.Restates
				}
				kind := strings.TrimSpace(f.Kind)
				if kind == "" {
					kind = "restatement"
				}
				hits[i] = append(hits[i], hit{c.Label, part, other, kind, strings.TrimSpace(f.Quote)})
			}
		}(i, c)
	}
	wg.Wait()

	// A restatement two contexts both carry is reported once, under the first.
	var out health.Findings
	reported := map[string]bool{}
	for i := range distinct {
		out = append(out, problems[i]...)
		for _, h := range hits[i] {
			key := h.part + "\x00" + h.other
			if reported[key] {
				continue
			}
			reported[key] = true
			out = append(out, health.Warn(health.IDContextDuplicateSemantic, "Restated guidance",
				fmt.Sprintf("%s restates %s (%s, seen in %s): %s", h.part, h.other, h.kind, h.label, h.quote)).
				About(h.part).
				WithRemediation("keep the guidance in "+h.other+" and refer to it by [[name]] from "+h.part))
		}
	}
	return out
}
