// `satelle workflow show` renders a workflow. Read-only.
package cli

import (
	"strings"

	"github.com/spf13/cobra"
)

func init() {
	wf := &cobra.Command{Use: "workflow", Short: "Inspect workflows (read-only)",
		Long: `Inspect the lifecycle this repo runs: which workflow governs a category, what a
route derives to, and whether the authored halves are valid.

Read-only — a lifecycle is authored markdown and TOML under .satelle/workflows,
so this never edits it. For one story's actual route and its verdicts, use
satelle story route.`}

	wf.AddCommand(workflowShowCmd())
	register(wf)
}

// workflowChoice is one entry in the ordered list satelle offers the agent.
type workflowChoice struct {
	Name      string   `json:"name"`
	Headline  string   `json:"headline,omitempty"`
	Scope     string   `json:"scope,omitempty"`
	AppliesTo []string `json:"applies_to,omitempty"`
	Embedded  bool     `json:"embedded"`
	Active    bool     `json:"active"` // the head — the workflow the engine enforces
}

// wfMeta parses a workflow's scope (scalar) and applies_to (list) from its
// frontmatter, reusing the package's frontmatter helper.
func wfMeta(body string) (scope string, appliesTo []string) {
	return frontmatterScope(body), frontmatterListValue(body, "applies_to")
}

// frontmatterScope returns the frontmatter `scope:` scalar, or "".
func frontmatterScope(body string) string {
	for _, ln := range strings.Split(frontmatter(body), "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "scope:") {
			return strings.Trim(strings.TrimSpace(strings.TrimPrefix(t, "scope:")), `"'`)
		}
	}
	return ""
}

// frontmatterListValue returns a list-valued frontmatter key (inline `[a, b]` or
// a block `- a` list), reusing frontmatterTags' parsing for the tags case.
func frontmatterListValue(body, key string) []string {
	if key == "tags" {
		return frontmatterTags(body)
	}
	lines := strings.Split(frontmatter(body), "\n")
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if !strings.HasPrefix(t, key+":") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(t, key+":"))
		if strings.HasPrefix(rest, "[") {
			return splitTrimTags(strings.TrimSuffix(strings.TrimPrefix(rest, "["), "]"))
		}
		var out []string
		for j := i + 1; j < len(lines); j++ {
			l2 := strings.TrimSpace(lines[j])
			if l2 == "" {
				continue
			}
			if strings.HasPrefix(l2, "- ") {
				out = append(out, strings.Trim(strings.TrimSpace(l2[2:]), `"'`))
				continue
			}
			break
		}
		return out
	}
	return nil
}
