package config

import (
	"testing"
)

// embeddedBody returns the shipped body of one embedded default.
func embeddedBody(t *testing.T, kind, name string) string {
	t.Helper()
	for _, d := range EmbeddedDefaults() {
		if d.Kind == kind && d.Name == name {
			return d.Body
		}
	}
	t.Fatalf("no embedded default %s/%s", kind, name)
	return ""
}

// stampedCopy is body as init writes it: stamped against its own bytes.
func stampedCopy(body string) string { return ApplyStamp(body, SHA(body)) }

// uneditedTree writes the tree every sty_097833e8 test shares: unedited copies in
// every form (current stamped, older stamped, stampless identical, a TOML route,
// a task), an edited seed in both forms, and an authored file.
func uneditedTree(t *testing.T, repo string) {
	t.Helper()
	step := embeddedBody(t, "skills", "satelle-step-summary")
	writeFile(t, repo, ".satelle/skills/satelle-step-summary.md", stampedCopy(step))
	writeFile(t, repo, ".satelle/skills/satelle-estimate-actual-review.md", embeddedBody(t, "skills", "satelle-estimate-actual-review"))
	goals := embeddedBody(t, "principles", "satelle-agent-goals")
	writeFile(t, repo, ".satelle/principles/satelle-agent-goals.md", stampedCopy(goals+"\nan older default\n"))
	writeFile(t, repo, ".satelle/workflows/done.toml", embeddedBody(t, "workflows", "done"))
	writeFile(t, repo, ".satelle/tasks/tsk_substrate-audit.md", stampedCopy(embeddedBody(t, "tasks", "tsk_substrate-audit")))

	// Edited: the stamp no longer matches the body (stamped), or the body differs
	// from the default (stampless).
	adv := embeddedBody(t, "skills", "satelle-workflow-advisor")
	writeFile(t, repo, ".satelle/skills/satelle-workflow-advisor.md", ApplyStamp(adv+"\noperator edit\n", SHA(adv)))
	writeFile(t, repo, ".satelle/skills/satelle-docs-only-check.md", embeddedBody(t, "skills", "satelle-docs-only-check")+"\noperator edit\n")

	writeFile(t, repo, ".satelle/skills/my-own-skill.md", "---\nname: my-own-skill\ntype: skill\n---\nauthored\n")
	writeFile(t, repo, ".satelle/skills/secret.local.md", "SECRET\n")
}

var uneditedPaths = []string{
	"principles/satelle-agent-goals.md",
	"skills/satelle-estimate-actual-review.md",
	"skills/satelle-step-summary.md",
	"tasks/tsk_substrate-audit.md",
	"workflows/done.toml",
}

var keptPaths = []string{
	"skills/my-own-skill.md",
	"skills/satelle-docs-only-check.md",
	"skills/satelle-workflow-advisor.md",
}

func bundlePaths(files []ConfigFile) []string {
	var out []string
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}
