package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
)

// writeRemote writes body as .satelle/remotes/<name>/remote.toml under repo.
func writeRemote(t *testing.T, repo, name, body string) string {
	t.Helper()
	p := filepath.Join(repo, config.DefaultDataDir, remotesDirName, name, "remote.toml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// AC7: satelle init reports each .satelle/remotes/*/remote.toml that fails v1
// validation, one line per finding with its key path, the old key's line naming
// its v1 replacement; a v1 definition produces no line, init rewrites neither
// file, and init still succeeds.
func TestInitReportsRemoteDefinitionsThatAreNotV1(t *testing.T) {
	repo := t.TempDir()
	const old = `satelle_version = "0.0.847"
remote_id = "rmt_pr-approve"
wake_key = "REMOTE_WAKE_KEY"
lifecycle = "idle"

[drive]
kind = "task"
task = "pr-approve"

[[repo]]
url = "https://github.com/OWNER/NAME.git"
ref = "main"

[[secret]]
key = "GH_TOKEN"
scope = "team"
from = "github-app"
permissions = { contents = "read" }
`
	oldPath := writeRemote(t, repo, "old", old)
	files, ok := config.EmbeddedRemote("pr-approve")
	if !ok {
		t.Fatal("the binary ships no pr-approve remote bundle")
	}
	newPath := writeRemote(t, repo, "fresh", files["remote.toml"])

	var out strings.Builder
	if err := runInitTest(t, &out, repo); err != nil {
		t.Fatalf("init: %v\n%s", err, out.String())
	}
	got := out.String()
	for _, want := range []string{
		"warning: remote old: definition is not v1: secret[0]: [[secret]] was replaced in v1 by [secrets]",
		"warning: remote old: definition is not v1: version: required",
		"warning: remote old: definition is not v1: provider: required",
		"warning: remote old: definition is not v1: wake_key: was removed in v1",
		"warning: remote old: definition is not v1: remote_id: was removed in v1",
		"warning: remote old: definition is not v1: lifecycle: the earlier lifecycle string is refused: v1 uses the [lifecycle] table",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("init output lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "remote fresh") {
		t.Errorf("init reported the v1 definition:\n%s", got)
	}
	if b, _ := os.ReadFile(oldPath); string(b) != old {
		t.Error("init rewrote the old definition")
	}
	if b, _ := os.ReadFile(newPath); string(b) != files["remote.toml"] {
		t.Error("init rewrote the v1 definition")
	}
}

// A repo with no remotes reports nothing about them.
func TestRemoteDefinitionReportWithNoRemotes(t *testing.T) {
	if got := remoteDefinitionReport(t.TempDir()); len(got) != 0 {
		t.Errorf("report = %v, want none", got)
	}
}
