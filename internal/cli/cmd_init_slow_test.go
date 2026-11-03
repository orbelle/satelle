//go:build clislow

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSlowRunInitAgentGuidance asserts init ends its report with the agent-facing
// note when the repo carries an agent instruction file (sty_4c406061): the
// reading agent is told to add/update a satelle section preferring `satelle
// help` — and the file itself is never modified.
func TestSlowRunInitAgentGuidance(t *testing.T) {
	cases := []struct {
		name  string
		files []string
	}{
		{"claude.md present", []string{"CLAUDE.md"}},
		{"agents.md present", []string{"AGENTS.md"}},
		{"both present", []string{"CLAUDE.md", "AGENTS.md"}},
		{"case-insensitive", []string{"claude.md"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := t.TempDir()
			owned := "# My instructions\n\nkeep me\n"
			for _, f := range c.files {
				if err := os.WriteFile(filepath.Join(repo, f), []byte(owned), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var out strings.Builder
			if err := runInitTest(t, &out, repo); err != nil {
				t.Fatalf("runInit: %v", err)
			}
			for _, f := range c.files {
				if !strings.Contains(out.String(), f) {
					t.Errorf("agent note does not name %s:\n%s", f, out.String())
				}
			}
			for _, want := range []string{"Agent note:", `"## satelle" section`, "satelle help", "prefer that over duplicating"} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("agent note missing %q:\n%s", want, out.String())
				}
			}
			// AC1 (sty_fd4b1cd4): the note points custom/process agents at
			// .satelle/agents.toml, explicitly NOT a harness-specific agent dir.
			for _, want := range []string{"agents.toml", ".claude/agents"} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("agent note missing the agents-layer pointer %q:\n%s", want, out.String())
				}
			}
			// AC4: the seeded agents.toml scaffold names the anti-pattern.
			toml, _ := os.ReadFile(filepath.Join(repo, ".satelle", "workflows", "agents.toml"))
			for _, want := range []string{".claude/agents", "invisible", "one CLI vendor"} {
				if !strings.Contains(string(toml), want) {
					t.Errorf("seeded agents.toml missing anti-pattern comment %q:\n%s", want, toml)
				}
			}
			// init never edits the agent-owned file.
			for _, f := range c.files {
				if got, _ := os.ReadFile(filepath.Join(repo, f)); string(got) != owned {
					t.Errorf("init modified %s:\n%s", f, got)
				}
			}
		})
	}

	t.Run("neither present", func(t *testing.T) {
		repo := t.TempDir()
		var out strings.Builder
		if err := runInitTest(t, &out, repo); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "Agent note:") {
			t.Errorf("agent note emitted with no instruction file present:\n%s", out.String())
		}
	})
}
