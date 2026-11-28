//go:build !windows

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const plainFlow = "[states.a]\nstart = true\n[states.z]\nterminal = true\n[[transition]]\nfrom = [\"a\"]\nto = \"z\"\n"

func TestWorkflowShowJSONPart(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	root := filepath.Join(dir, ".satelle")
	files := map[string]string{
		"repos.toml":                   "[repos.x]\norigin = \"https://github.com/o/x\"\n",
		"repos/x/satelle.toml":         "[[workflows.rule]]\nworkflow = \"infra\"\ncategory = \"c\"\n",
		"repos/x/workflows/infra.toml": plainFlow,
		"repos/x/agents.toml":          "[agents.coder]\ncommand = \"claude\"\nmodel = \"x-coder\"\n",
		"workflows/lookalike.toml":     "name = \"x/flow\"\n" + plainFlow,
	}
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must(t, "validate")
	var st struct {
		Workflows []string `json:"workflows"`
	}
	if err := json.Unmarshal([]byte(must(t, "status", "--json")), &st); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, w := range st.Workflows {
		var show map[string]any
		if err := json.Unmarshal([]byte(must(t, "workflow", "show", w, "--json")), &show); err != nil {
			t.Fatalf("workflow show %s: %v", w, err)
		}
		part, ok := show["part"].(string)
		if !ok {
			t.Fatalf("workflow show %s --json has no string part: %v", w, show["part"])
		}
		want := ""
		if w == "x/infra" {
			want = "x"
		}
		if part != want {
			t.Errorf("workflow %s: part %q, want %q", w, part, want)
		}
		seen[w] = true
	}
	if !seen["x/infra"] || !seen["x/flow"] {
		t.Errorf("status listed %v", st.Workflows)
	}
}
