package agentstep

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/structure"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/wfdot"
	"github.com/bobmcallan/satelle/internal/wfgovern"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// reviewerPrep is one gate's pre-flight: its rubric, its structure and contract
// checks passed, and the transition payload it judges. It is the ONE place that
// pre-flight lives, shared by the single-gate path (runReviewerWith) and the
// bundled path (runReviewerBundle), so a bundled rubric is refused, degraded and
// decorated exactly as it is when it runs alone (sty_23e10d92).
type reviewerPrep struct {
	skill string
	body  string
	tp    transitionPayload
	// payload is tp marshalled — the stdin of a functional check. It stays the
	// capped inline JSON. A judging reviewer does not receive this value; Invoke
	// writes material to disk and substitutes paths.
	payload []byte
	// material is the pre-cap bytes the fills already computed. The check path
	// never reads it. A judging Invoke writes it under the dispatch scratch.
	material reviewerMaterial
	// criteria is what this gate knows about the criteria it judges and which of
	// them its latest verdict settled (sty_99d3162b).
	criteria criteriaPrep
	// unresolved marks a DECLARED gate whose rubric is not installed: advisory,
	// nothing judges it, and the caller records it as an ungated advance.
	unresolved bool
}

// reviewerMaterial is the pre-cap copy of what a reviewer is handed today.
// Docs, the diff, and the history slices are the resolver results before the
// count and excerpt cuts. The capped copies stay on transitionPayload.
type reviewerMaterial struct {
	Docs            []DocState
	Diff            *DiffState
	Messages        []MessageState
	PriorVerdicts   []PriorVerdict
	DefinitionEdits []DefinitionEdit
	Deferred        []DeferredItem
	Waivers         []WaiverItem
	Citations       *CitationsState
	// LedgerJSON is the store listing when List succeeded, including an empty
	// array. LedgerErr is the unavailable reason ("ledger: …") when it did not.
	LedgerJSON []byte
	LedgerErr  string
	// LedgerWindow says how many older rows the listing left out (Omitted > 0).
	LedgerWindow ledger.Window
}

// prepareReviewer resolves skill's rubric, refuses a broken one, and builds the
// payload the gate judges. An absent rubric is not an error — it returns
// unresolved (sty_d59ec6a9).
func (g *Engine) prepareReviewer(ctx context.Context, item workitem.Item, toStatus, skill, gateAgent string, decorate func(*transitionPayload)) (reviewerPrep, error) {
	body, err := g.skillBody(ctx, skill)
	if err != nil {
		if errors.Is(err, docindex.ErrNotFound) {
			// Advisory degradation: the edge DECLARED this gate but its rubric is
			// not installed, so nothing judges the transition and it advances.
			// Fail-open is deliberate (a fresh repo must work before every gate is
			// authored) — but it must not be SILENT, so name the skill that was
			// skipped (sty_d59ec6a9).
			return reviewerPrep{skill: skill, unresolved: true}, nil
		}
		return reviewerPrep{}, err
	}
	// Broken substrate refuses to run (sty_d0d6bb67): a PRESENT reviewer skill
	// that fails its deterministic structure check must not judge the edge. An
	// ABSENT rubric stays advisory by design (fresh repos keep working); an
	// invalid one is a broken definition and refuses, naming the problems.
	if problems := structure.Doc("skills", skill, body, nil); len(problems) > 0 {
		return reviewerPrep{}, fmt.Errorf(
			"gate refused: reviewer skill %q fails structure validation: %s — fix the substrate (`satelle skill validate %s`)",
			skill, strings.Join(problems, "; "), skill)
	}
	// Reviewer skill contract check (design §6.3): the body must specify the
	// verdict contract (at least decision + notes). reasoning is recommended.
	if problems := structure.ReviewerSkillContract(body); len(problems) > 0 {
		return reviewerPrep{}, fmt.Errorf(
			"gate refused: reviewer skill %q does not specify the verdict contract: %s — the skill must document returning JSON {decision, notes} (reasoning recommended)",
			skill, strings.Join(problems, "; "))
	}
	tp := transitionPayload{Story: item, From: item.Status, To: toStatus, ReviewSkill: skill}
	mat := &reviewerMaterial{}
	if g.children != nil {
		tp.Children = g.children(ctx, item)
	}
	g.fillPayloadDocs(ctx, item.ID, &tp, mat)
	// The full prior-verdict history rides ONLY the gate payload (sty_0f5e600c):
	// it is re-review context, so the executor and retrospective payloads go
	// without it. The performer of a re-presented edge gets the latest verdict of
	// each seat instead, as findings_to_answer (sty_f948d345), so it answers the
	// findings and keeps the story, not the last rejection, as its authority.
	// The edge's carry policy and the step's performer are resolved once: the
	// performer decides both what keeps a prior verdict fresh and where the
	// criteria under judgment live (sty_99d3162b).
	carry := g.edgeCarry(ctx, item, item.Status, toStatus) == wfdot.CarryUntilAccept
	perf, perr := g.StepPerformer(ctx, item, toStatus)
	if perr != nil {
		perf = verb.PerformerResolution{}
	}
	g.fillPriorVerdicts(ctx, item.ID, item.Status, toStatus, exemptionOf(carry, perf, perr), &tp, mat)
	crit := criteriaPrep{carry: carry, requirePer: requiresPerCriterion(body)}
	crit.judged, crit.source = judgedCriteria(item, mat.Docs, perf)
	tp.CriteriaSource = crit.source
	tp.CriteriaState = crit.setState(item, mat.Docs, mat.PriorVerdicts, skill)
	g.fillDefinitionEdits(ctx, item.ID, &tp, mat)
	g.fillDeferred(ctx, item.ID, skill, &tp, mat)
	g.fillWaivers(ctx, item.ID, skill, &tp, mat)
	g.fillFindingAnswers(ctx, item, toStatus, &tp)
	// Engagement diff rides the GATE payload only (sty_a125b440): reviewers
	// without a shell need the slice; executors have one. fillDiff never
	// errors — a missing baseline is a marker, not a refused transition.
	g.fillDiff(ctx, item.ID, &tp, mat)
	// A rubric that opts in (payload_citations) gets the plan's file:line
	// citations resolved to the lines they name; every other gate goes without.
	g.fillCitations(ctx, item.ID, body, &tp, mat)
	gateAddrs := []string{"reviewer"}
	if strings.TrimSpace(gateAgent) != "" && gateAgent != "reviewer" {
		gateAddrs = append(gateAddrs, gateAgent)
	}
	g.fillMessages(ctx, item.ID, gateAddrs, &tp, mat)
	g.fillLedger(ctx, item.ID, mat)
	if decorate != nil {
		decorate(&tp)
	}
	payload, err := json.Marshal(tp)
	if err != nil {
		return reviewerPrep{}, err
	}
	return reviewerPrep{skill: skill, body: body, tp: tp, payload: payload, material: *mat, criteria: crit}, nil
}

// fillLedger lists the story's ledger once, the same filter as
// `satelle ledger list --story`. Failure is an unavailable reason. It never
// shells out and never re-resolves.
func (g *Engine) fillLedger(ctx context.Context, itemID string, mat *reviewerMaterial) {
	if mat == nil {
		return
	}
	if strings.TrimSpace(itemID) == "" {
		mat.LedgerErr = "ledger: story id required"
		return
	}
	b, win, err := verb.ListStoryLedger(ctx, itemID)
	if err != nil {
		mat.LedgerErr = "ledger: " + err.Error()
		return
	}
	mat.LedgerJSON = b
	mat.LedgerWindow = win
}

// reviewerSeat is the resolved harness an LLM gate runs under: the binding with
// its engine-wide fallbacks filled, the agents.toml section it came from, the
// model and why it was chosen, the runner, and the idle bound. Two gates that
// resolve to the same seat can share one session (sty_23e10d92).
type reviewerSeat struct {
	binding     config.AgentBinding
	section     string
	modelSource string
	runner      agentcli.Runner
	idle        time.Duration
}

// reviewerSeatFor resolves the seat gateAgent's LLM gate runs under, refusing a
// binding that cannot produce an isolated verdict. The error is the refusal a
// gate surfaces; the caller supplies the decision that carries it. skill only
// names the gate in the no-runner refusal.
func (g *Engine) reviewerSeatFor(ctx context.Context, item workitem.Item, toStatus, skill, gateAgent string) (reviewerSeat, error) {
	binding, section, berr := g.gateBinding(gateAgent)
	if berr != nil {
		return reviewerSeat{}, berr
	}
	// Engine-wide caches fill only the default [reviewer] binding. Named
	// bindings must be self-contained in agents.toml (sty_a476a2f8).
	if section == "reviewer" {
		if binding.Tools == "" {
			binding.Tools = g.tools
		}
		if binding.Model == "" {
			binding.Model = g.model
		}
		if len(binding.Env) == 0 {
			binding.Env = g.reviewerEnv
		}
		if binding.Principles == "" && binding.InjectPrinciples == nil {
			if g.injectPrinciples {
				binding.Principles = config.PrinciplesSession
			} else {
				binding.Principles = config.PrinciplesNone
			}
		}
	}
	// Model selection (sty_7069bced): binding.Model already carries the
	// explicit configured model or the engine-wide g.model fallback filled
	// above, so this only descends the ladder (inherited/creator/cli-default)
	// when BOTH are empty — an explicit model behaves exactly as before (AC6).
	// A gate has no step/agent override tier of its own (edges superseded
	// their DOT model= at sty_a476a2f8; this does not reverse that).
	modelResolved, modelSource := g.selectModel(ctx, binding, item.ID, "", "")
	binding.Model = modelResolved
	// Mechanism: a gate needs an isolated verdict. command=in-loop cannot produce
	// one — fail loud at gate time (design §6.4), not by policing tools/model.
	if config.IsInLoopCommand(binding.CommandTemplate()) {
		return reviewerSeat{}, fmt.Errorf(
			"gate refused: reviewer binding %q is command=in-loop and cannot produce an isolated verdict — set [%s] command to an isolated agent CLI (claude|grok or a full template)", section, section)
	}
	// Role must resolve to reviewer for the gate binding (design §4.4 / §8 / sty_a476a2f8).
	if config.ResolvedRole(section, binding) != config.RoleReviewer {
		return reviewerSeat{}, fmt.Errorf(
			"gate refused: binding [%s] has role=%q (want role=reviewer) — a named performer never advances status; allocate a role=\"reviewer\" binding on gated edges",
			section, config.ResolvedRole(section, binding))
	}
	// Default [reviewer] uses the bootstrap runner (g.runner). A named
	// role=reviewer binding must run its OWN harness — leave Runner nil so
	// Invoke builds from the binding (sty_68dafd5f; runner must follow agent=).
	var gateRunner agentcli.Runner
	if section == "reviewer" {
		if g.runner == nil {
			return reviewerSeat{}, fmt.Errorf(
				"reviewer: transition %s→%s is gated by %q but no agent runner is configured", item.Status, toStatus, skill)
		}
		gateRunner = g.runner
	} else if kind, _ := binding.ResolvedPlacement(); kind == config.PlacementRemote {
		// A gate judged by a remote performer (sty_f5cae2e5): the binding's own
		// same-named node binding runs the harness, so the seat carries the
		// channel runner. It opens lazily — building a seat only to key it starts
		// no node — and the seat's caller closes it after the verdict.
		rr, rerr := g.remoteRunnerFor(section, binding, item.ID)
		if rerr != nil {
			return reviewerSeat{}, fmt.Errorf("gate refused: reviewer binding [%s]: %w", section, rerr)
		}
		gateRunner = rr
	}
	idle, ierr := g.idleTimeoutFor(section, binding)
	if ierr != nil {
		return reviewerSeat{}, fmt.Errorf("reviewer: invalid idle_timeout in .satelle/workflows/agents.toml [%s]: %w", section, ierr)
	}
	return reviewerSeat{binding: binding, section: section, modelSource: modelSource, runner: gateRunner, idle: idle}, nil
}

// closeSeat ends what the seat's runner holds open once its gate has been judged:
// a remote seat's performer node ends with the verdict. A local runner holds nothing.
func closeSeat(seat reviewerSeat) {
	if rr, ok := seat.runner.(*remoteRunner); ok {
		_ = rr.Close()
	}
}

// stampInvocation copies what one Invoke run measured about itself onto the
// decision it produced — the same fields for a lone gate and a bundled one — and
// the auth mode its seat's binding declares.
func stampInvocation(d *verb.GateDecision, res InvokeResult, seat reviewerSeat) {
	d.ModelSource = seat.modelSource
	d.AuthMode = seat.binding.ResolvedAuth()
	d.SystemPromptBytes = res.SystemPromptBytes
	d.PayloadBytes = res.PayloadBytes
	d.ToolIsolation = res.ToolIsolation
}

// referencedPayload is the work item a judging reviewer receives. Inline fields
// match transitionPayload. The large fields are paths under the dispatch scratch.
type referencedPayload struct {
	Story           workitem.Item        `json:"story"`
	From            string               `json:"from"`
	To              string               `json:"to"`
	ReviewSkill     string               `json:"review_skill"`
	ReviewSkills    []string             `json:"review_skills,omitempty"`
	ReviewMode      string               `json:"review_mode,omitempty"`
	Children        []ChildState         `json:"children,omitempty"`
	Docs            []referencedDoc      `json:"docs,omitempty"`
	RouteDrift      *wfgovern.RouteDrift `json:"route_drift,omitempty"`
	PriorVerdicts   *pathRef             `json:"prior_verdicts,omitempty"`
	DefinitionEdits *pathRef             `json:"definition_edits,omitempty"`
	Deferred        *pathRef             `json:"deferred,omitempty"`
	Waivers         *pathRef             `json:"waivers,omitempty"`
	Citations       *pathRef             `json:"citations,omitempty"`
	FindingAnswers  *FindingAnswers      `json:"finding_answers,omitempty"`
	Amendment       *AmendmentState      `json:"amendment,omitempty"`
	Diff            *referencedDiff      `json:"diff,omitempty"`
	Messages        *pathRef             `json:"messages,omitempty"`
	CriteriaSource  string               `json:"criteria_source,omitempty"`
	CriteriaState   []criterionState     `json:"criteria_state,omitempty"`
	PeerFindings    []peerFinding        `json:"peer_findings,omitempty"`
	Ledger          ledgerRef            `json:"ledger"`
}

type referencedDoc struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Path string `json:"path"`
	// source is the persistent file the scratch copy was made from, "" when
	// none; the dispatch log names it instead of the copy's content.
	source string
}

type referencedDiff struct {
	Baseline   string `json:"baseline_sha,omitempty"`
	Dirty      bool   `json:"baseline_dirty,omitempty"`
	NoBaseline bool   `json:"no_baseline,omitempty"`
	Note       string `json:"note,omitempty"`
	Source     string `json:"source,omitempty"`
	PatchPath  string `json:"patch_path"`
}

type pathRef struct {
	Path string `json:"path"`
}

// ledgerRef points a gate at the story's ledger listing. Omitted, Total and
// Limit are set only when the listing is the newest Limit of Total rows and
// Omitted older ones are not in the file (sty_523edea7).
type ledgerRef struct {
	Path        string `json:"path,omitempty"`
	Unavailable string `json:"unavailable,omitempty"`
	Omitted     int    `json:"omitted,omitempty"`
	Total       int    `json:"total,omitempty"`
	Limit       int    `json:"limit,omitempty"`
}

// writeReviewerMaterial writes the pre-cap bytes under scratch/material and
// returns the referenced work item. A write error returns before any swap.
func writeReviewerMaterial(scratch string, mat *reviewerMaterial, tp transitionPayload) (referencedPayload, error) {
	dir := filepath.Join(scratch, "material")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return referencedPayload{}, err
	}
	ref := referencedPayload{
		Story:          tp.Story,
		From:           tp.From,
		To:             tp.To,
		ReviewSkill:    tp.ReviewSkill,
		ReviewSkills:   tp.ReviewSkills,
		ReviewMode:     tp.ReviewMode,
		Children:       tp.Children,
		RouteDrift:     tp.RouteDrift,
		FindingAnswers: tp.FindingAnswers,
		Amendment:      tp.Amendment,
		CriteriaSource: tp.CriteriaSource,
		CriteriaState:  tp.CriteriaState,
		PeerFindings:   tp.PeerFindings,
	}
	docs, err := writeMaterialDocs(dir, mat.Docs)
	if err != nil {
		return referencedPayload{}, err
	}
	ref.Docs = docs
	if mat.Diff != nil {
		abs, err := writeMaterialJSON(dir, "diff.json", mat.Diff)
		if err != nil {
			return referencedPayload{}, err
		}
		ref.Diff = &referencedDiff{
			Baseline:   mat.Diff.Baseline,
			Dirty:      mat.Diff.Dirty,
			NoBaseline: mat.Diff.NoBaseline,
			Note:       mat.Diff.Note,
			Source:     mat.Diff.Source,
			PatchPath:  abs,
		}
	}
	if len(mat.Messages) > 0 {
		abs, err := writeMaterialJSON(dir, "messages.json", mat.Messages)
		if err != nil {
			return referencedPayload{}, err
		}
		ref.Messages = &pathRef{Path: abs}
	}
	if len(mat.PriorVerdicts) > 0 {
		abs, err := writeMaterialJSON(dir, "prior_verdicts.json", mat.PriorVerdicts)
		if err != nil {
			return referencedPayload{}, err
		}
		ref.PriorVerdicts = &pathRef{Path: abs}
	}
	if len(mat.DefinitionEdits) > 0 {
		abs, err := writeMaterialJSON(dir, "definition_edits.json", mat.DefinitionEdits)
		if err != nil {
			return referencedPayload{}, err
		}
		ref.DefinitionEdits = &pathRef{Path: abs}
	}
	if len(mat.Deferred) > 0 {
		abs, err := writeMaterialJSON(dir, "deferred.json", mat.Deferred)
		if err != nil {
			return referencedPayload{}, err
		}
		ref.Deferred = &pathRef{Path: abs}
	}
	if len(mat.Waivers) > 0 {
		abs, err := writeMaterialJSON(dir, "waivers.json", mat.Waivers)
		if err != nil {
			return referencedPayload{}, err
		}
		ref.Waivers = &pathRef{Path: abs}
	}
	if mat.Citations != nil {
		abs, err := writeMaterialJSON(dir, "citations.json", mat.Citations)
		if err != nil {
			return referencedPayload{}, err
		}
		ref.Citations = &pathRef{Path: abs}
	}
	if len(mat.LedgerJSON) > 0 && mat.LedgerErr == "" {
		path := filepath.Join(dir, "ledger.json")
		if err := os.WriteFile(path, mat.LedgerJSON, 0o600); err != nil {
			return referencedPayload{}, err
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return referencedPayload{}, err
		}
		ref.Ledger = ledgerRef{Path: abs}
		if w := mat.LedgerWindow; w.Omitted > 0 {
			ref.Ledger.Omitted, ref.Ledger.Total, ref.Ledger.Limit = w.Omitted, w.Total, w.Limit
		}
	} else {
		reason := mat.LedgerErr
		if reason == "" {
			reason = "ledger: not resolved"
		}
		ref.Ledger = ledgerRef{Unavailable: reason}
	}
	return ref, nil
}

// writeMaterialDocs writes each document's full body under dir/docs and returns
// the references that stand for them in a payload: name, type and path.
func writeMaterialDocs(dir string, docs []DocState) ([]referencedDoc, error) {
	if len(docs) == 0 {
		return nil, nil
	}
	docsDir := filepath.Join(dir, "docs")
	if err := os.MkdirAll(docsDir, 0o700); err != nil {
		return nil, err
	}
	used := map[string]int{}
	var out []referencedDoc
	for _, d := range docs {
		name, ok := materialDocFile(d.Name, used)
		if !ok {
			continue
		}
		path := filepath.Join(docsDir, name)
		if err := os.WriteFile(path, []byte(d.Body), 0o600); err != nil {
			return nil, err
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		out = append(out, referencedDoc{Name: d.Name, Type: d.Type, Path: abs, source: d.Path})
	}
	return out, nil
}

func writeMaterialJSON(dir, name string, v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return "", err
	}
	return filepath.Abs(path)
}

// materialDocFile returns a basename safe to write under material/docs.
// Empty, ".", and ".." are skipped. A repeated basename gains a numeric suffix.
func materialDocFile(name string, used map[string]int) (string, bool) {
	base := filepath.Base(strings.TrimSpace(name))
	if base == "" || base == "." || base == ".." || strings.ContainsAny(base, `/\`) {
		return "", false
	}
	n := used[base]
	used[base] = n + 1
	if n == 0 {
		return base, true
	}
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	return fmt.Sprintf("%s-%d%s", stem, n+1, ext), true
}
