package remotedef

import (
	"reflect"
	"strings"
	"testing"
)

// Parse reads the v1 story fixture the node tests serve: its secrets, repos,
// allowlist, setup and drive.
func TestParseReadsTheStoryFixture(t *testing.T) {
	d, err := Parse(readTestdata(t, "remote.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if want := []SecretEntry{{Key: "CLAUDE_CODE_OAUTH_TOKEN", Source: SourceBackend, Name: "CLAUDE_CODE_OAUTH_TOKEN"}}; !reflect.DeepEqual(d.SecretEntries(), want) {
		t.Errorf("secrets = %+v, want %+v", d.SecretEntries(), want)
	}
	if want := []Repo{{URL: "https://github.com/acme/widgets.git", Ref: "main"}}; !reflect.DeepEqual(d.Repos, want) {
		t.Errorf("repos = %+v, want %+v", d.Repos, want)
	}
	if want := []string{"api.example.com", "*.example.org"}; !reflect.DeepEqual(d.Allow(), want) {
		t.Errorf("allow = %v, want %v", d.Allow(), want)
	}
	if want := []string{"host-cli setup"}; !reflect.DeepEqual(d.Setup.OnCreate, want) {
		t.Errorf("setup on_create = %v, want %v", d.Setup.OnCreate, want)
	}
	if d.RunsTask() || d.Drive.Kind != DriveStory || d.Trigger != nil {
		t.Errorf("drive = %+v trigger = %+v, want a request-only story drive", d.Drive, d.Trigger)
	}
}

// A [drive] kind = "task" definition names the task a wake runs, as its bare
// name; the node finds it under the tsk_ id SyncTasks ingests it as.
func TestParseReadsTheTaskARemoteDrives(t *testing.T) {
	d, err := Parse(readTestdata(t, "task.remote.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !d.RunsTask() || d.Drive.Kind != DriveTask || d.Drive.Task != "nightly-sweep" {
		t.Errorf("drive = %+v, want a task remote for nightly-sweep", d.Drive)
	}
	if got, want := d.TaskID(), "tsk_nightly-sweep"; got != want {
		t.Errorf("TaskID() = %q, want %q", got, want)
	}
}

// AC1: Parse is the single refusal seam. A definition with an error finding is
// refused with every finding's key path, an old-shape one with the server's
// message naming the v1 key that replaced it.
func TestParseRefusesWhatValidateRefuses(t *testing.T) {
	for name, c := range map[string]struct{ body, want string }{
		"malformed TOML":    {"[[secret\nkey = 1", "parse: not valid TOML"},
		"wrong-typed allow": {minimalV1 + "[network]\nallow = \"x\"\n", "network.allow: must be a list"},
		"wrong-typed repo":  {minimalV1 + "[[repo]]\nurl = 3\nref = \"main\"\n", "repo[0].url: must be a string"},
		"old secret":        {minimalV1 + "[[secret]]\nkey = \"A\"\n", "secret[0]: [[secret]] was replaced in v1 by [secrets]"},
		"old host":          {minimalV1 + "[host]\nbootstrap = [[\"x\"]]\n", "host.bootstrap: was replaced in v1 by [setup]"},
		"nothing":           {"", "version: required"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(c.body))
			if err == nil || !strings.Contains(err.Error(), "remote.toml") || !strings.Contains(err.Error(), c.want) {
				t.Errorf("Parse = %v, want an error naming remote.toml and %q", err, c.want)
			}
		})
	}
}

// SecretEntries lists the names in order, then the overrides in key order, and
// leaves out a name an override also carries.
func TestSecretEntries(t *testing.T) {
	d := Definition{Secrets: Secrets{
		Names: []string{"B_KEY", "A_KEY", "GH_TOKEN"},
		Override: map[string]OverrideValue{
			"GH_TOKEN": map[string]any{"provider": "github-app", "permissions": map[string]any{"contents": "read"}},
			"NOTES":    "satelle:TEAM_NOTES",
			"BAD":      "satelle:",
		},
	}}
	want := []SecretEntry{
		{Key: "B_KEY", Source: SourceBackend, Name: "B_KEY"},
		{Key: "A_KEY", Source: SourceBackend, Name: "A_KEY"},
		{Key: "GH_TOKEN", Source: SourceRunToken, Name: "GH_TOKEN", Permissions: map[string]string{"contents": "read"}},
		{Key: "NOTES", Source: SourceSatelle, Name: "TEAM_NOTES"},
	}
	if got := d.SecretEntries(); !reflect.DeepEqual(got, want) {
		t.Errorf("SecretEntries = %+v, want %+v", got, want)
	}
}

// GitHubAppKey names the github-app override's key on github.com, and none on
// another host or when the definition declares none.
func TestGitHubAppKey(t *testing.T) {
	d := Definition{Secrets: Secrets{Override: map[string]OverrideValue{
		"NOTES":    "satelle:NOTES",
		"GH_TOKEN": map[string]any{"provider": "github-app"},
	}}}
	for host, want := range map[string]string{"": "GH_TOKEN", "github.com": "GH_TOKEN", "GitHub.com": "GH_TOKEN", "ghe.example.com": ""} {
		if got, ok := d.GitHubAppKey(host); got != want || ok != (want != "") {
			t.Errorf("GitHubAppKey(%q) = %q, %v, want %q", host, got, ok, want)
		}
	}
	if _, ok := (Definition{}).GitHubAppKey(""); ok {
		t.Error("a definition with no github-app override names a key")
	}
}

// The definition the node hands a run in its environment reads back the same,
// overrides included, and carries no value.
func TestEnvValueRoundTrips(t *testing.T) {
	d, err := Parse(readTestdata(t, "full.v1.remote.toml"))
	if err != nil {
		t.Fatal(err)
	}
	v, err := d.EnvValue()
	if err != nil {
		t.Fatal(err)
	}
	back, err := FromEnvValue(v)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back.SecretEntries(), d.SecretEntries()) || !reflect.DeepEqual(back.Repos, d.Repos) || back.Machine != d.Machine {
		t.Errorf("round trip = %+v, want %+v", back, d)
	}
	if key, ok := back.GitHubAppKey("github.com"); !ok || key != "GH_TOKEN" {
		t.Errorf("GitHubAppKey after the round trip = %q, %v", key, ok)
	}
}

func TestRepoName(t *testing.T) {
	for url, want := range map[string]string{
		"https://github.com/acme/widgets.git": "widgets",
		"https://github.com/acme/widgets/":    "widgets",
		"https://git.example.com/team/tool":   "tool",
	} {
		if got := RepoName(url); got != want {
			t.Errorf("RepoName(%q) = %q, want %q", url, got, want)
		}
	}
}
