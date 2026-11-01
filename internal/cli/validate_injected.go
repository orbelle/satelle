package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/agentstep"
	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/condense"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/structure"
	"github.com/bobmcallan/satelle/internal/wfdot"
	"github.com/bobmcallan/satelle/internal/wfgovern"
)

// The injected-context report of `satelle validate` (sty_012394c0).
//
// Instruction text is paid for on every call and shapes what an agent writes, so
// its size is worth seeing per kind (what a kind of document costs) and per seat
// (what each agent actually receives). It reports measured bytes only: satelle
// judges no seat against a budget, because managing a seat's context is its
// CLI's job (sty_58a7bdfe), and nothing here can fail validate.

// injectedRow is one line of the report. Unavailable, when set, replaces the
// figure with an adapter-named reason: a number that cannot be computed is never
// printed as zero.
type injectedRow struct {
	Group       string // "kind" or "seat"
	Label       string // principles, driver[claude], reviewer[claude/stream]
	Bytes       int
	Note        string
	Unavailable string
}

// injectedEnv is everything the report reads, resolved by the caller so the core
// runs without a store.
type injectedEnv struct {
	Cfg          config.Config
	Docs         agentstep.DocGetter // principles and skills, embedded fallback included
	Workflows    []docindex.Doc
	Constitution string
	ConstPath    string
	Agents       config.AgentsConfig
	AgentsErr    error
	// Rules is how the SessionStart context is condensed under its harness's
	// byte cap (zero: the embedded defaults); the driver rows are measured after
	// that fit. A dispatched seat is never condensed.
	Rules condense.Rules
}

// seatUse is what the route asks one agents.toml section to run: the skills it
// may be handed and the charter kind it opens with.
type seatUse struct {
	section string
	kind    agentstep.SeatKind
	step    string
	skills  []string
	judges  bool // the section serves a gate step, so its dispatch judges and is handed the verdict rules
}

// gate marks the seat as serving a gate step and returns it.
func (s *seatUse) gate() *seatUse {
	s.judges = true
	return s
}

func (s *seatUse) add(skill string) {
	if skill == "" {
		return
	}
	for _, have := range s.skills {
		if have == skill {
			return
		}
	}
	s.skills = append(s.skills, skill)
}

// routeSeats collects the dispatched seats every category's derived route
// allocates: a named performer per step, the reviewer sections gates run under,
// and the consult bindings of rework loops. In-loop work (agent=executor) is the
// driving session, reported separately as driver[<harness>].
func routeSeats(workflows []docindex.Doc) []*seatUse {
	rs := wfgovern.RouteSourceOf(workflows)
	lists, err := wfdot.ParseDone(rs.Done)
	if err != nil {
		return nil
	}
	seats := map[string]*seatUse{}
	get := func(section string, kind agentstep.SeatKind, step string) *seatUse {
		if section == "" {
			section = config.RoleReviewer
		}
		s, ok := seats[section]
		if !ok {
			s = &seatUse{section: section, kind: kind, step: step}
			seats[section] = s
		}
		return s
	}
	for _, l := range lists {
		d, err := wfgovern.RouteSpecFor(rs, l.Category, nil)
		if err != nil {
			continue
		}
		for _, st := range d.Spec.States {
			switch {
			case len(st.On) > 0:
				// An always-on gate node; an executor-side augmentation is in-loop.
				switch st.Agent {
				case "executor", "planner", "coder":
					continue
				}
				get(st.Agent, agentstep.SeatReviewer, st.On[0]).gate().add(st.Skill)
			case st.Agent != "" && st.Agent != "executor" && st.Agent != config.RoleReviewer:
				get(st.Agent, agentstep.SeatPerformer, st.Name).add(st.Skill)
			}
		}
		for _, tr := range d.Spec.Transitions {
			skills := tr.Skills
			if len(skills) == 0 && tr.Skill != "" {
				skills = []string{tr.Skill}
			}
			for _, sk := range skills {
				get(tr.Agent, agentstep.SeatReviewer, tr.To).gate().add(sk)
			}
		}
		for _, r := range d.Reworks {
			get(r.Consult, agentstep.SeatConsult, r.Step)
		}
	}
	out := make([]*seatUse, 0, len(seats))
	for _, s := range seats {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].section < out[j].section })
	return out
}

// injectedRows computes every row of the report.
func injectedRows(ctx context.Context, env injectedEnv) []injectedRow {
	var rows []injectedRow
	docs, _ := env.Docs.List(ctx, "principles")
	always := selectAlwaysDocs(docs)

	// --- per kind -----------------------------------------------------------
	principles := 0
	for _, d := range always {
		principles += len(strings.TrimSpace(stripFrontmatter(d.Body)))
	}
	rows = append(rows,
		injectedRow{Group: "kind", Label: "principles", Bytes: principles,
			Note: fmt.Sprintf("%d session-resident", len(always))},
		injectedRow{Group: "kind", Label: "constitution", Bytes: len(env.Constitution)})
	rows = append(rows, skillKindRows(ctx, env)...)

	// --- per seat -----------------------------------------------------------
	rows = append(rows, driverRows(env, always)...)
	rows = append(rows, dispatchedRows(ctx, env)...)
	return rows
}

// skillKindRows reports the size a skill of each kind can add to a prompt: the
// largest one (what a seat pays), with the total for context. Functional checks
// run as scripts and never enter a prompt, so they are named and not measured.
func skillKindRows(ctx context.Context, env injectedEnv) []injectedRow {
	skills, _ := env.Docs.List(ctx, "skills")
	type agg struct {
		largest, total, n int
		name              string
	}
	groups := map[string]*agg{}
	checks := 0
	for _, d := range skills {
		kind := "performer"
		switch {
		case structure.CheckCommand(d.Body) != "":
			checks++
			continue
		case docHasTag(d.Body, "type:reviewer"):
			kind = "reviewer"
		}
		g := groups[kind]
		if g == nil {
			g = &agg{}
			groups[kind] = g
		}
		g.n++
		g.total += len(d.Body)
		if len(d.Body) > g.largest {
			g.largest, g.name = len(d.Body), d.Name
		}
	}
	var rows []injectedRow
	for _, kind := range []string{"performer", "reviewer"} {
		g := groups[kind]
		if g == nil {
			continue
		}
		rows = append(rows, injectedRow{Group: "kind", Label: "skills[" + kind + "]", Bytes: g.largest,
			Note: fmt.Sprintf("largest %s; %d skills, %d bytes in total", g.name, g.n, g.total)})
	}
	if checks > 0 {
		rows = append(rows, injectedRow{Group: "kind", Label: "skills[functional-check]",
			Unavailable: fmt.Sprintf("%d scripts, never injected", checks)})
	}
	return rows
}

// driverRows is one row per configured in-loop harness: the deterministic
// SessionStart assembly under that harness's own limit, the same function the
// hook calls, minus the volatile lines (web probe, drift advisories, seat).
func driverRows(env injectedEnv, always []docindex.Doc) []injectedRow {
	var rows []injectedRow
	for _, h := range env.Cfg.Harnesses() {
		limit := env.Cfg.ContextLimit(h)
		content, omitted := sessionAssembly(env.Constitution, always, env.ConstPath, h, limit, 0, env.Rules)
		row := injectedRow{Group: "seat", Label: "driver[" + h + "]", Bytes: len(content)}
		if len(omitted) > 0 {
			full, _ := sessionAssembly(env.Constitution, always, env.ConstPath, h, 1<<30, 0, env.Rules)
			row.Note = fmt.Sprintf("limit %d bytes; %d bytes omitted, indexed instead: %s",
				limit, max(len(full)-len(content), 0), strings.Join(omitted, ", "))
		} else {
			row.Note = fmt.Sprintf("limit %d bytes", limit)
		}
		rows = append(rows, row)
	}
	return rows
}

// dispatchedRows sizes each dispatched seat as constitution + principles (when
// its binding injects them) + charter + call-to-action + its largest skill,
// assembled by agentstep.SeatSystemPrompt — the composition a dispatch uses.
func dispatchedRows(ctx context.Context, env injectedEnv) []injectedRow {
	var rows []injectedRow
	for _, seat := range routeSeats(env.Workflows) {
		row := injectedRow{Group: "seat", Label: seat.section}
		if env.AgentsErr != nil {
			row.Unavailable = seat.section + ": agents.toml does not load: " + env.AgentsErr.Error()
			rows = append(rows, row)
			continue
		}
		binding, ok := bindingFor(env.Agents, seat.section)
		if !ok {
			row.Unavailable = seat.section + ": no binding declared in agents.toml"
			rows = append(rows, row)
			continue
		}
		cmd := binding.CommandTemplate()
		if config.IsInLoopCommand(cmd) {
			row.Unavailable = seat.section + ": in-loop binding, nothing is dispatched"
			rows = append(rows, row)
			continue
		}
		adapter := agentcli.AdapterName(cmd)
		row.Label = fmt.Sprintf("%s[%s/%s]", seat.section, adapter, binding.ResolvedInterface())
		if adapter == agentcli.HarnessUnknown {
			row.Unavailable = fmt.Sprintf("%s: unrecognised adapter, size not estimated", config.ExecutableToken(cmd))
			rows = append(rows, row)
			continue
		}
		in := agentstep.SeatPromptInput{
			Kind: seat.kind, Section: seat.section, Step: seat.step, Workflow: wfgovern.DerivedRouteName,
			Shell:   agentcli.GrantAdmitsShell(binding.Tools),
			Judging: seat.judges,
		}
		if seat.judges {
			in.Verdict = agentstep.ReviewerVerdictDoc(ctx, env.Docs)
		}
		if binding.InjectsPrinciples() {
			in.Constitution = strings.TrimSpace(env.Constitution)
			in.Principles = agentstep.ResidentPrincipleDocs(ctx, env.Docs, binding.ResolvedPrinciples())
		}
		name, body := largestSkill(ctx, env.Docs, seat.skills)
		in.Rubric = body
		row.Bytes = len(agentstep.SeatPayloadFor(in))
		switch {
		case name != "":
			row.Note = "largest skill " + name
		case seat.kind == agentstep.SeatConsult:
			row.Note = "no skill: a consult opens with its charter only"
		default:
			row.Note = "no LLM skill on this seat's route"
		}
		// The plan citations ride the payload, not the system prompt: they add to
		// what the seat is sent but not to the prompt above.
		if seat.judges && seatDeclaresCitations(ctx, env.Docs, seat.skills) {
			row.Bytes += agentstep.CitationsPayloadCeiling
			row.Note += fmt.Sprintf("; includes up to %d bytes of plan citations", agentstep.CitationsPayloadCeiling)
		}
		rows = append(rows, row)
	}
	return rows
}

// seatDeclaresCitations reports whether any of the seat's skills opts into plan
// citation excerpts (payload_citations).
func seatDeclaresCitations(ctx context.Context, docs agentstep.DocGetter, names []string) bool {
	for _, n := range names {
		if d, err := docs.Get(ctx, "skills", n); err == nil && agentstep.DeclaresPayloadCitations(d.Body) {
			return true
		}
	}
	return false
}

// bindingFor resolves an agents.toml section the way dispatch does: the
// [reviewer] default, else a named agent.
func bindingFor(a config.AgentsConfig, section string) (config.AgentBinding, bool) {
	if section == config.RoleReviewer {
		return a.ReviewerBinding(), true
	}
	return a.NamedBinding(section)
}

// largestSkill returns the biggest skill body among names that an LLM would be
// handed — a functional check runs as a script, so it never counts.
func largestSkill(ctx context.Context, docs agentstep.DocGetter, names []string) (string, string) {
	var best, bestBody string
	for _, n := range names {
		d, err := docs.Get(ctx, "skills", n)
		if err != nil || structure.CheckCommand(d.Body) != "" {
			continue
		}
		if len(d.Body) > len(bestBody) {
			best, bestBody = n, d.Body
		}
	}
	return best, bestBody
}

// printInjectedReport writes the rows as measured bytes. It is a report, so it
// can never change validate's exit code.
func printInjectedReport(out io.Writer, rows []injectedRow) {
	fmt.Fprintln(out, "# injected context (measured bytes)")
	group := ""
	for _, r := range rows {
		if r.Group != group {
			group = r.Group
			fmt.Fprintf(out, "%s:\n", map[string]string{"kind": "per kind", "seat": "per seat"}[group])
		}
		if r.Unavailable != "" {
			fmt.Fprintf(out, "INFO  %-34s unavailable (%s)\n", r.Label, r.Unavailable)
			continue
		}
		line := fmt.Sprintf("INFO  %-34s %7d bytes", r.Label, r.Bytes)
		if r.Note != "" {
			line += "  — " + r.Note
		}
		fmt.Fprintln(out, line)
	}
}

// reportInjected resolves the environment from the app and prints the report.
// Nothing here can fail validate; an environment piece that will not load turns
// the affected rows into named unavailable ones.
func reportInjected(ctx context.Context, out io.Writer, a *app.App) {
	applySessionContextFacts(a.Config)
	workflows, _ := a.Store.DocIndex.List(ctx, "workflows")
	constPath := a.PlaneConstitution()
	rules, _ := condenseRules(a.PlaneDir())
	env := injectedEnv{
		Cfg: a.Config, Docs: a.Store.DocIndex, Workflows: workflows,
		Constitution: readConstitution(constPath), ConstPath: constPath, Rules: rules,
	}
	eff, err := config.LoadEffectiveAgents(a.PlaneDir(), a.PlaneConfig().Vars)
	env.Agents, env.AgentsErr = eff.Agents, err
	printInjectedReport(out, injectedRows(ctx, env))
}
