// `satelle workflow show <name>` renders ONE workflow's effective governance:
// its identity, the shape of its status graph, and — the reason this command
// exists (sty_ede16f51) — its LIFECYCLE HOOKS with the binding each one will
// actually run.
//
// Before hooks were declarable, create review resolved a skill from frontmatter
// and ran it against an empty agent selector that silently became `[reviewer]`.
// Nothing displayed that choice. This command is where an operator reads it:
// operation, skill, logical agent, whether that agent resolves through a
// machine-wide profile or a local binding, its interface, model, effort and
// permission ceiling, and the files those values came from.
//
// Read-only, and deliberately tolerant: an unresolvable binding renders an
// explicit unresolved marker rather than failing the command, because the whole
// point is to diagnose a misconfiguration (`satelle agent validate` is the
// surface that refuses one).
package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/agentvalidate"
	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/wfdot"
	"github.com/bobmcallan/satelle/internal/wfgovern"
	"github.com/bobmcallan/satelle/internal/wfhook"
)

func workflowShowCmd() *cobra.Command {
	var asCategory bool
	var tags []string
	c := &cobra.Command{
		Use:   "show <name-or-category>",
		Short: "Show one workflow's identity, graph shape, and lifecycle-hook allocations",
		Long: `show renders a single workflow's effective governance.

Beyond the status graph it prints the workflow's LIFECYCLE HOOKS — operations
that fire outside the graph (story creation today) — with the binding each will
run: skill, logical agent, the profile or local binding it resolves through,
interface, model, effort, permission ceiling, and source files.

Read-only. An unresolvable allocation is marked UNRESOLVED rather than failing
the command; satelle agent validate is the surface that refuses one.`,
		Args:        cobra.ExactArgs(1),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := appFrom(cmd)
			if err != nil {
				return err
			}
			// A document name wins, so `show done` / `show step` / `show <workflow>`
			// stay exactly what they were. --category is the explicit override for a
			// category that collides with a workflow file name.
			if !asCategory {
				doc, derr := a.Store.DocIndex.Get(context.Background(), "workflows", args[0])
				if derr == nil {
					return renderWorkflowShow(cmd.OutOrStdout(), a, doc, skillResolver(a))
				}
			}
			// Fall through to the DERIVED route for this category. The docindex miss
			// is deliberately discarded: "not found in the doc index" answers a
			// question the operator did not ask, and used to be the only thing
			// `workflow show <category>` ever said (sty_a989764d).
			return showDerivedRoute(cmd.OutOrStdout(), a, args[0], tags)
		},
	}
	c.Flags().BoolVar(&asCategory, "category", false,
		"treat the argument as a story CATEGORY and render its derived route, even if a workflow document shares the name")
	c.Flags().StringSliceVar(&tags, "tags", nil,
		"story tags to derive the route with: tag-appended obligations and tag-scoped gates (comma-separated)")
	return c
}

// showDerivedRoute resolves the repo's route source and renders one category's
// route. A category no route claims is reported as exactly that, never as a
// document-index miss.
func showDerivedRoute(out io.Writer, a *app.App, category string, tags []string) error {
	docs, err := a.Store.DocIndex.List(context.Background(), "workflows")
	if err != nil {
		return fmt.Errorf("workflow show %q: %w", category, err)
	}
	rs, ok, rerr := wfgovern.RouteGovernsErr(docs, category)
	if rerr != nil {
		return fmt.Errorf("workflow show %q: %w", category, rerr)
	}
	if !ok {
		return fmt.Errorf("workflow show %q: no workflow document of that name, and no derived route governs that category — see `satelle workflow list`", category)
	}
	grants, _, _ := hookGrantIndex(a, a.PlaneDir())
	placements := make(map[string]string, len(grants))
	for name, g := range grants {
		placements[name] = g.Placement
	}
	return renderWorkflowRouteWith(out, rs, category, tags, placements)
}

// renderWorkflowShow writes the whole view. Split from the cobra wiring — and
// taking the skill resolver as a parameter rather than reaching for the store —
// so a test can drive it against a doc + agents layer with no database.
func renderWorkflowShow(out io.Writer, a *app.App, doc docindex.Doc, resolves func(string) bool) error {
	dataDir := a.PlaneDir()

	fmt.Fprintf(out, "WORKFLOW %s\n", doc.Name)
	fmt.Fprintf(out, "  scope:      %s\n", scopeLabel(doc))
	if applies := frontmatterLine(doc.Body, "applies_to"); applies != "" {
		fmt.Fprintf(out, "  applies_to: %s\n", applies)
	}
	fmt.Fprintf(out, "  source:     %s\n", workflowSourceFile(dataDir, doc))

	if wfgovern.IsRouteSource(doc.Name) {
		// A route SOURCE carries no lifecycle of its own — its half of the grammar
		// is what there is to show, and the route itself is `satelle story route`
		// (sty_9835070d).
		fmt.Fprintln(out, "  kind:       route source (half of a derived route; not a workflow)")
		switch doc.Name {
		case wfgovern.RouteSourceDone:
			if lists, err := wfdot.ParseDone(doc.Body); err == nil {
				var cats []string
				for _, l := range lists {
					cats = append(cats, l.Category)
				}
				fmt.Fprintf(out, "  declares:   done for %s\n", strings.Join(cats, ", "))
			} else {
				fmt.Fprintf(out, "  declares:   (does not parse: %v)\n", err)
			}
		case wfgovern.RouteSourceStep:
			if cat, err := wfdot.ParseSteps(doc.Body); err == nil {
				fmt.Fprintf(out, "  declares:   %d step(s), %d always-on gate(s)\n", len(cat.Steps), len(cat.Gates))
			} else {
				fmt.Fprintf(out, "  declares:   (does not parse: %v)\n", err)
			}
		}
	} else {
		// A lifecycle is done.toml + step.toml; nothing else under the workflows kind
		// carries one, so there is no graph to summarise (sty_d953c5d8).
		fmt.Fprintln(out, "  kind:       not a route source — a lifecycle is done.toml + step.toml")
	}

	// The hook section — the reason this command exists.
	hooks, problems := wfhook.Parse(doc.Body)
	fmt.Fprintln(out, "Lifecycle hooks (fire outside the status graph):")
	if len(hooks) == 0 {
		fmt.Fprintln(out, "  (none declared — creation stays deterministic-only)")
	}
	grants, agentsFile, catalogFile := hookGrantIndex(a, dataDir)
	for _, h := range hooks {
		renderHook(out, h, grants, agentsFile, catalogFile, resolves)
	}
	for _, p := range problems {
		fmt.Fprintf(out, "  PROBLEM %s\n", p)
	}
	return nil
}

// renderHook prints one hook's full allocation. Every field the operator needs
// to answer "what will actually run, and where was it authored?" is on this
// block; nothing is left to be inferred from another command's output.
func renderHook(out io.Writer, h wfhook.Hook, grants map[string]agentvalidate.Grant, agentsFile, catalogFile string, resolves func(string) bool) {
	kind := "advisory"
	if h.Verdict {
		kind = "verdict"
	}
	fmt.Fprintf(out, "  HOOK %s (%s)\n", h.Operation, kind)
	if !wfhook.Known(h.Operation) {
		fmt.Fprintf(out, "    operation:  UNKNOWN — this binary knows: %s\n", strings.Join(wfhook.Operations(), ", "))
	}
	skillNote := ""
	if resolves != nil && !resolves(h.Skill) {
		skillNote = "  UNRESOLVED in the substrate"
	}
	fmt.Fprintf(out, "    skill:      %s%s\n", h.Skill, skillNote)
	fmt.Fprintf(out, "    agent:      %s\n", h.Describe())

	g, ok := grants[h.Agent]
	if !ok {
		fmt.Fprintf(out, "    binding:    UNRESOLVED — agents.toml declares no [%s]\n", h.Agent)
		fmt.Fprintf(out, "    sources:    %s\n", agentsFile)
		return
	}
	fmt.Fprintf(out, "    binding:    %s\n", bindingOrigin(g, h.Agent))
	fmt.Fprintf(out, "    interface:  %s (backend %s)\n", g.Interface, g.Backend)
	fmt.Fprintf(out, "    placement:  %s\n", g.Placement)
	fmt.Fprintf(out, "    model:      %s\n", orNone(g.Model))
	fmt.Fprintf(out, "    effort:     %s\n", orNone(g.Effort))
	fmt.Fprintf(out, "    ceiling:    %s\n", ceilingLabel(g))
	fmt.Fprintf(out, "    sources:    %s\n", strings.Join(hookSourceFiles(g, agentsFile, catalogFile), ", "))
}

// bindingOrigin says whether the effective binding came from a machine-wide
// profile or is purely local, naming the profile when there is one. It reads the
// provenance the resolver already recorded rather than re-resolving anything.
func bindingOrigin(g agentvalidate.Grant, section string) string {
	profiles := map[string]bool{}
	for _, src := range g.Sources {
		for _, part := range strings.Split(src, "+") {
			if name, ok := config.CatalogProfile(part); ok {
				profiles[name] = true
			}
		}
	}
	if len(profiles) == 0 {
		return fmt.Sprintf("local binding [%s]", section)
	}
	names := make([]string, 0, len(profiles))
	for n := range profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return fmt.Sprintf("local binding [%s] over profile %s", section, strings.Join(names, ", "))
}

// hookSourceFiles lists the files that contributed to the effective binding —
// always the repo agents layer, plus the machine-wide catalog when a profile
// supplied any field.
func hookSourceFiles(g agentvalidate.Grant, agentsFile, catalogFile string) []string {
	files := []string{agentsFile}
	for _, src := range g.Sources {
		if config.IsCatalogSource(src) {
			return append(files, catalogFile)
		}
	}
	return files
}

// hookGrantIndex resolves the effective agents layer and indexes each binding's
// grant by section. A broken agents layer yields an empty index — every hook then
// renders UNRESOLVED, which is the diagnosis this command is for.
func hookGrantIndex(a *app.App, dataDir string) (map[string]agentvalidate.Grant, string, string) {
	agentsFile, _ := config.AgentsPath(dataDir)
	catalogFile := config.GlobalAgentsPath()
	index := map[string]agentvalidate.Grant{}

	layers, _ := config.LoadAgentLayers(dataDir)
	if layers.RepoErr != nil {
		return index, agentsFile, catalogFile
	}
	global, err := config.LoadGlobalAgents()
	if err != nil {
		global = config.GlobalAgentsConfig{}
	}
	// Workflows are passed as nil: this is a DISPLAY of binding facts, not a
	// re-run of the allocation checks `agent validate` owns.
	// Layered, so the baseline seats and the workspace and local layers that
	// actually run are indexed too — a hook on a baseline seat is not UNRESOLVED.
	// A layer that failed to load is the zero layer (LoadAgentLayers).
	for _, g := range agentvalidate.ValidateEffectiveLayered(layers, global, a.PlaneConfig().Vars, nil, nil).Grants {
		index[g.Name] = g
	}
	return index, agentsFile, catalogFile
}

// ceilingLabel renders the permission ceiling in the terms the agent model uses.
func ceilingLabel(g agentvalidate.Grant) string {
	base := "read-write (no ceiling expressed)"
	if g.ReadOnly {
		base = "read-only"
	}
	if g.Backend == "in-loop" {
		base = "in-loop (full session grant — cannot produce an isolated verdict)"
	}
	if g.Tools != "" {
		base += fmt.Sprintf(", tools=%q", g.Tools)
	}
	return base
}

// workflowSourceFile names where the workflow body came from: the repo file when
// one exists on disk, else the embedded default.
func workflowSourceFile(dataDir string, doc docindex.Doc) string {
	path := filepath.Join(dataDir, "workflows", doc.Name+config.EmbeddedExt("workflows", doc.Name))
	if fileExists(path) {
		return path
	}
	return "embedded default (satelle binary)"
}

// scopeLabel reports the workflow's declared scope, defaulting to project.
func scopeLabel(doc docindex.Doc) string {
	if s := frontmatterLine(doc.Body, "scope"); s != "" {
		return s
	}
	return "project"
}

// frontmatterLine returns a top-level frontmatter key's raw value, or "".
func frontmatterLine(body, key string) string {
	lines := strings.Split(body, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return ""
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			return ""
		}
		if rest, ok := strings.CutPrefix(lines[i], key+":"); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(none — the CLI's default)"
	}
	return s
}
