package cli

// sty_d6e209aa: a missing, unreadable or divergent authored process is reported,
// never silently replaced by the embedded default. AC1 (unreadable refuses on
// every surface), AC3 (absent is reported, never refused).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
)

// isolateProcessEnv keeps a process-of-record test off the real ~/.satelle and
// off any SATELLE_CONFIG override, with no hosted endpoint.
func isolateProcessEnv(t *testing.T) {
	t.Helper()
	t.Setenv("SATELLE_HOME", t.TempDir())
	t.Setenv(config.SessionEnv, "")
	_ = os.Unsetenv(config.SessionEnv)
	t.Setenv("SATELLE_CONFIG", "")
	_ = os.Unsetenv("SATELLE_CONFIG")
	t.Setenv("SATELLE_SERVER_ENDPOINT", "none")
}

// gitMainTree makes an empty git repo with one commit and returns its root.
func gitMainTree(t *testing.T, base string) string {
	t.Helper()
	main := filepath.Join(base, "proj")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, main, "README.md", "x\n")
	gitIn(t, main, "init", "-q")
	gitIn(t, main, "config", "user.email", "t@example.com")
	gitIn(t, main, "config", "user.name", "t")
	gitIn(t, main, "add", "-A")
	gitIn(t, main, "commit", "-q", "-m", "init")
	return main
}

// alwaysPassSkill is a functional-check gate skill that always accepts, so a
// transition under the embedded route runs with no agent CLI.
func alwaysPassSkill(name string) string {
	return "---\nname: " + name + "\ntype: skill\ndescription: always-pass functional check for a fixture\n---\n\n```check\ntrue\n```\n"
}

// overrideEmbeddedGates replaces the embedded route's judged gates with
// functional checks (a same-named authored skill overrides its default).
func overrideEmbeddedGates(t *testing.T, root, dataDir string) {
	t.Helper()
	for _, name := range []string{
		"satelle-story-intent-review", "satelle-estimate-actual-review", "satelle-step-summary",
	} {
		writeFile(t, root, dataDir+"/skills/"+name+".md", alwaysPassSkill(name))
	}
}

func idOf(t *testing.T, stdout string) string {
	t.Helper()
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &created); err != nil || created.ID == "" {
		t.Fatalf("parse story JSON: %v\n%s", err, stdout)
	}
	return created.ID
}

// wantUnreadableRefusal asserts the refusal text of AC1: the unreadable path, the
// embedded default route that would otherwise have governed (as binary-shipped),
// and that the story's gates would not be the repository's.
func wantUnreadableRefusal(t *testing.T, surface, wfPath, text string) {
	t.Helper()
	for _, want := range []string{
		wfPath, "embedded default route", "binary-shipped", `lane "default"`,
		"gates would not be the repository's gates",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("%s: refusal must carry %q:\n%s", surface, want, text)
		}
	}
}
