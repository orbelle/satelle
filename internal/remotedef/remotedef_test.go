package remotedef

import (
	"os"
	"testing"
)

func TestParseReadsTheItemFixture(t *testing.T) {
	b, err := os.ReadFile("testdata/remote.toml")
	if err != nil {
		t.Fatal(err)
	}
	def, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if def.Provider != ProviderFlySprite || def.Drive.Kind != DriveItem || def.RunsTask() || len(def.Allow()) != 2 {
		t.Fatalf("def = %+v", def)
	}
}

func TestParseReadsTheTaskARemoteDrives(t *testing.T) {
	b, err := os.ReadFile("testdata/task.remote.toml")
	if err != nil {
		t.Fatal(err)
	}
	def, err := Parse(b)
	if err != nil || !def.RunsTask() || def.Drive.Task != "nightly-sweep" {
		t.Fatalf("def = %+v, %v", def, err)
	}
}

func TestSecretEntries(t *testing.T) {
	def, err := Parse([]byte(minimalV1 + `[[repo]]
url = "https://github.com/a/b"
ref = "main"
[secrets]
names = ["B_KEY", "A_KEY"]
[secrets.override]
GH = { provider = "github-app", permissions = { contents = "read" } }
NOTE = "satelle:NOTES"
`))
	if err != nil {
		t.Fatal(err)
	}
	got := def.SecretEntries()
	want := []struct {
		key  string
		src  SecretSource
		name string
	}{{"B_KEY", SourceBackend, "B_KEY"}, {"A_KEY", SourceBackend, "A_KEY"}, {"GH", SourceRunToken, "GH"}, {"NOTE", SourceSatelle, "NOTES"}}
	if len(got) != len(want) {
		t.Fatalf("entries = %+v", got)
	}
	for i, w := range want {
		if got[i].Key != w.key || got[i].Source != w.src || got[i].Name != w.name {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], w)
		}
	}
}

func TestRepoName(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/acme/widgets.git": "widgets",
		"https://github.com/acme/widgets/":    "widgets",
		"https://example.com/x":               "x",
	} {
		if got := RepoName(in); got != want {
			t.Errorf("RepoName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNetworkPermits(t *testing.T) {
	n := Network{Allow: []string{"proxy.golang.org", "*.example.org", "10.0.0.7", "Sum.Golang.Org."}}
	for host, want := range map[string]bool{
		"proxy.golang.org":      true,
		"PROXY.golang.org":      true,
		"proxy.golang.org.":     true,
		"golang.org":            false,
		"evil.proxy.golang.org": false,
		"a.example.org":         true,
		"b.a.example.org":       true,
		"example.org":           false,
		"badexample.org":        false,
		"sum.golang.org":        true,
		"10.0.0.7":              true,
		"10.0.0.8":              false,
		"":                      false,
		".":                     false,
	} {
		if got := n.Permits(host); got != want {
			t.Errorf("Permits(%q) = %v, want %v", host, got, want)
		}
	}
	if (Network{Allow: []string{"*.0.0.7"}}).Permits("10.0.0.7") {
		t.Error("a wildcard admitted an IP literal")
	}
	if (Network{}).Permits("proxy.golang.org") {
		t.Error("an empty allow-list admitted a host")
	}
}
