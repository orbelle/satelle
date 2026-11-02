package config

import (
	"strings"
	"testing"
)

// The PR-approval task and its review skill ship as embedded defaults; the
// remote bundle template beside them does not, so its placeholder constitution
// never becomes a principle of every repo (sty_d57d078b).
func TestEmbeddedPRApproveDefaultsAndTemplate(t *testing.T) {
	have := map[string]bool{}
	for _, d := range EmbeddedDefaults() {
		have[d.Kind+"/"+d.Name] = true
		if d.Kind == remotesDir || d.Name == "repo-constitution" {
			t.Errorf("the remote template's %s is an embedded default", d.RelPath())
		}
	}
	for _, want := range []string{"tasks/tsk_pr-approve", "skills/satelle-pr-review"} {
		if !have[want] {
			t.Errorf("%s is not an embedded default", want)
		}
	}
	files, ok := EmbeddedRemote("pr-approve")
	if !ok || !strings.Contains(files["remote.toml"], `task = "pr-approve"`) || files["principles/repo-constitution.md"] == "" {
		t.Errorf("EmbeddedRemote(pr-approve) = %v, %v; want remote.toml and the constitution placeholder", keys(files), ok)
	}
	if _, ok := EmbeddedRemote("no-such-remote"); ok {
		t.Error("EmbeddedRemote of an unknown name reported a template")
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
