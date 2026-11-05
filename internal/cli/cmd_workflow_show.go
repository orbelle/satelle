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
	"strings"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/wfdot"
	"github.com/bobmcallan/satelle/internal/wfgovern"
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
		return fmt.Errorf("workflow show %q: no workflow document of that name, and no derived route governs that category", category)
	}
	return renderWorkflowRouteWith(out, rs, category, tags, nil)
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
	return nil
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
