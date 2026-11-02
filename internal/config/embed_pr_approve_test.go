package config

import (
	"reflect"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/remotedef"
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

// AC3: the shipped pr-approve template is schema v1 and validates with no
// finding: a task drive of pr-approve, started by a story entering pr_open, whose
// GH_TOKEN is a github-app override with exactly the permissions the approval
// needs and whose agent CLI credential is a team value satelle holds.
func TestEmbeddedPRApproveTemplateIsV1(t *testing.T) {
	files, ok := EmbeddedRemote("pr-approve")
	if !ok {
		t.Fatal("the binary ships no pr-approve remote bundle")
	}
	body := []byte(files["remote.toml"])
	if fs := remotedef.Validate(body); len(fs) != 0 {
		t.Fatalf("the shipped template has findings: %+v", fs)
	}
	def, err := remotedef.Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	if def.Version != 1 || def.Provider != remotedef.ProviderFlySprite ||
		def.Drive != (remotedef.Drive{Kind: remotedef.DriveTask, Task: "pr-approve"}) {
		t.Errorf("definition = %+v, want v1 on fly-sprite driving task pr-approve", def)
	}
	if def.Trigger == nil || def.Trigger.On != "story.status:pr_open" {
		t.Errorf("trigger = %+v, want on story.status:pr_open", def.Trigger)
	}
	want := []remotedef.SecretEntry{
		{Key: "CLAUDE_CODE_OAUTH_TOKEN", Source: remotedef.SourceSatelle, Name: "CLAUDE_CODE_OAUTH_TOKEN"},
		{Key: "GH_TOKEN", Source: remotedef.SourceRunToken, Name: "GH_TOKEN",
			Permissions: map[string]string{"contents": "read", "pull_requests": "write", "statuses": "write"}},
	}
	if got := def.SecretEntries(); !reflect.DeepEqual(got, want) {
		t.Errorf("secrets = %+v, want %+v", got, want)
	}
	if len(def.Repos) != 1 || !reflect.DeepEqual(def.Allow(), []string{"api.github.com"}) {
		t.Errorf("repos = %+v allow = %v, want one repo and only the GitHub API", def.Repos, def.Allow())
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
