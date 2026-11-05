package doctor

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/bobmcallan/satelle/internal/agentvalidate"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/health"
)

// Exit codes — part of the command's contract, so a script can branch on them.
const (
	// ExitHealthy: no error findings. Warnings are allowed and still printed.
	ExitHealthy = 0
	// ExitUnhealthy: at least one error finding in at least one repository.
	ExitUnhealthy = 1
	// ExitUsage: doctor itself could not run (bad flag, unreadable global config).
	ExitUsage = 2
)

// ExitCode maps a set of reports to the process exit code.
func ExitCode(reports []Report) int {
	for _, r := range reports {
		if !r.OK {
			return ExitUnhealthy
		}
	}
	return ExitHealthy
}

// Payload is the --json shape. Stable ids, no secret values.
type Payload struct {
	Repos    []Report `json:"repos"`
	Summary  Summary  `json:"summary"`
	ExitCode int      `json:"exit_code"`
}

// Summary is the healthy/unhealthy tally across the checked repositories.
type Summary struct {
	Healthy   int `json:"healthy"`
	Unhealthy int `json:"unhealthy"`
}

// RenderJSON writes the machine-readable payload.
func RenderJSON(w io.Writer, reports []Report) error {
	healthy, unhealthy := Summarise(reports)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(Payload{
		Repos:    reports,
		Summary:  Summary{Healthy: healthy, Unhealthy: unhealthy},
		ExitCode: ExitCode(reports),
	})
}

// RenderText writes the human report at one of three levels:
//
//	detail=false           findings only — the shape a multi-repo sweep needs
//	detail=true            + each binding's effective fields with their source
//	detail,verbose=true    + every workflow node/edge allocation
//
// The middle level is the single-repo default: an operator asking "is this repo
// ready?" wants the effective configuration and its provenance, not a line per
// gated edge.
func RenderText(w io.Writer, reports []Report, detail, verbose bool) {
	for i, r := range reports {
		if i > 0 {
			fmt.Fprintln(w)
		}
		renderReport(w, r, detail, verbose)
	}
	if len(reports) > 1 {
		healthy, unhealthy := Summarise(reports)
		fmt.Fprintf(w, "\n%d healthy, %d unhealthy\n", healthy, unhealthy)
	}
}

func renderReport(w io.Writer, r Report, detail, verbose bool) {
	status := "HEALTHY"
	if !r.OK {
		status = "UNHEALTHY"
	}
	fmt.Fprintf(w, "%s %s\n", status, orDash(r.Repo))

	if detail {
		renderGrants(w, r)
		renderGates(w, r, verbose)
		renderEnv(w, r)
	}

	for _, sev := range []health.Severity{health.SeverityError, health.SeverityWarn, health.SeverityInfo} {
		label := map[health.Severity]string{
			health.SeverityError: "FAIL",
			health.SeverityWarn:  "WARN",
			health.SeverityInfo:  "INFO",
		}[sev]
		for _, f := range r.Findings.WithSeverity(sev) {
			fmt.Fprintf(w, "  %s  [%s] %s\n", label, f.ID, f.Detail)
			if f.Remediation != "" {
				fmt.Fprintf(w, "        fix: %s\n", f.Remediation)
			}
		}
	}
	if r.OK && len(r.Findings.WithSeverity(health.SeverityWarn)) == 0 {
		fmt.Fprintln(w, "  PASS  no problems found")
	}
}

// renderGrants prints each binding's effective fields WITH the tier that
// supplied them — the "effective value and source" view. It is the same shape
// `satelle agent validate` prints, so the two surfaces read identically.
func renderGrants(w io.Writer, r Report) {
	if len(r.Grants) == 0 {
		return
	}
	fmt.Fprintln(w, "  Agent grants (effective value → source):")
	for _, g := range r.Grants {
		ro := "read-write"
		if g.ReadOnly {
			ro = "read-only"
		}
		fmt.Fprintf(w, "    GRANT [%s] role=%s interface=%s backend=%s %s\n", g.Name, g.Role, g.Interface, g.Backend, ro)
		RenderGrantSources(w, "      ", g)
	}
}

// RenderGrantSources writes one grant's per-field provenance. Exported so the
// `satelle agent validate` printer uses this exact function — one renderer, so
// the two displays cannot drift.
//
// env and settings VALUES are never printed: those lines name the field and its
// source only.
func RenderGrantSources(w io.Writer, indent string, g agentvalidate.Grant) {
	if len(g.Sources) == 0 {
		return
	}
	vals := map[string]string{
		"interface":    g.Interface,
		"command":      g.Command,
		"tools":        g.Tools,
		"model":        g.Model,
		"effort":       g.Effort,
		"timeout":      g.Timeout,
		"idle_timeout": g.IdleTimeout,
		"role":         g.Role,
		"principles":   g.Principles,
		"secondary":    g.Secondary,
	}
	fields := make([]string, 0, len(g.Sources))
	for f := range g.Sources {
		fields = append(fields, f)
	}
	sort.Strings(fields)
	for _, f := range fields {
		// Each tier is followed by the file (or origin) that supplied it, so the
		// operator can tell which file to edit (sty_5c56143b).
		tier := g.Sources[f]
		if file := config.SourceFile(tier); file != tier {
			tier = fmt.Sprintf("%s (%s)", tier, file)
		}
		if f == "env" || f == "settings" {
			fmt.Fprintf(w, "%ssource: %s ← %s — values withheld\n", indent, f, tier)
			continue
		}
		fmt.Fprintf(w, "%ssource: %s = %q ← %s\n", indent, f, vals[f], tier)
	}
}

// renderGates prints workflow node/edge allocations and — separately labelled —
// LIFECYCLE HOOK allocations, which fire outside the status graph.
func renderGates(w io.Writer, r Report, verbose bool) {
	var hooks, nodes []agentvalidate.GateAllocation
	for _, ga := range r.Gates {
		if ga.Operation != "" {
			hooks = append(hooks, ga)
			continue
		}
		nodes = append(nodes, ga)
	}
	if len(hooks) > 0 {
		fmt.Fprintln(w, "  Lifecycle hooks (outside the status graph):")
		for _, ga := range hooks {
			fmt.Fprintf(w, "    HOOK [%s] %s skill=%s agent=%s model=%q declared=%s\n",
				ga.Workflow, ga.Operation, ga.Skill, ga.Agent, ga.EffectiveModel, ga.Source)
		}
	}
	if len(nodes) > 0 && !verbose {
		fmt.Fprintf(w, "  Workflow allocations: %d node/edge gates (--verbose to list)\n", len(nodes))
	}
	if len(nodes) > 0 && verbose {
		fmt.Fprintln(w, "  Workflow allocations:")
		for _, ga := range nodes {
			fmt.Fprintf(w, "    NODE [%s] %s gate=%s agent=%s model=%q\n",
				ga.Workflow, ga.Node, ga.Skill, ga.Agent, ga.EffectiveModel)
		}
	}
}

// renderEnv names each binding's environment KEYS and whether they resolved.
// A value is never printed, in any mode.
func renderEnv(w io.Writer, r Report) {
	if len(r.Env) == 0 {
		return
	}
	sections := make([]string, 0, len(r.Env))
	for s := range r.Env {
		sections = append(sections, s)
	}
	sort.Strings(sections)
	fmt.Fprintln(w, "  Environment keys (names only — values are never printed):")
	for _, s := range sections {
		var parts []string
		for _, k := range r.Env[s] {
			state := "resolved"
			if !k.Resolved {
				state = "UNRESOLVED"
			}
			parts = append(parts, k.Key+" ("+state+")")
		}
		fmt.Fprintf(w, "    [%s] %s\n", s, strings.Join(parts, ", "))
	}
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(current repository)"
	}
	return s
}

// sortStrings is the local sort helper (doctor keeps its own so the package has
// no incidental dependency direction).
func sortStrings(s []string) { sort.Strings(s) }
