package remotedef

import (
	"strings"
	"testing"
)

// A [[secret]] with from = "github-app" is accepted when the definition declares a
// [[repo]] on its host (github.com when it names none) and refused when it does
// not, naming the key and the host: the rule satelle-server applies
// (sty_c7f12b60). A secret of any other source needs no repo.
func TestValidateGitHubAppSecretNeedsARepoOnItsHost(t *testing.T) {
	cases := []struct {
		name, toml string
		refusal    string // "" accepts
	}{
		{
			name: "default host with a github.com repo",
			toml: `
[[secret]]
key = "GH_TOKEN"
from = "github-app"

[[repo]]
url = "https://github.com/acme/widgets.git"
ref = "main"
`,
		},
		{
			name: "named host with a repo on it",
			toml: `
[[secret]]
key = "GH_TOKEN"
from = "github-app"
host = "ghe.example.com"

[[repo]]
url = "https://GHE.example.com/acme/widgets.git"
ref = "main"
`,
		},
		{
			name: "no repos",
			toml: `
[[secret]]
key = "GH_TOKEN"
from = "github-app"
`,
			refusal: `secret GH_TOKEN: from = "github-app" needs a [[repo]] on github.com`,
		},
		{
			name: "named host but repos only on github.com",
			toml: `
[[secret]]
key = "GH_TOKEN"
from = "github-app"
host = "ghe.example.com"

[[repo]]
url = "https://github.com/acme/widgets.git"
ref = "main"
`,
			refusal: `secret GH_TOKEN: from = "github-app" needs a [[repo]] on ghe.example.com`,
		},
		{
			name: "env secret with no repos",
			toml: `
[[secret]]
key = "CLAUDE_CODE_OAUTH_TOKEN"
from = "env"
`,
		},
		{
			name: "argv secret with no repos",
			toml: `
[[secret]]
key = "API_KEY"
from = ["pass", "show", "api"]
`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, err := Parse([]byte(tc.toml))
			if err != nil {
				t.Fatal(err)
			}
			err = Validate(d)
			switch {
			case tc.refusal == "" && err != nil:
				t.Errorf("Validate = %v, want accepted", err)
			case tc.refusal != "" && (err == nil || !strings.Contains(err.Error(), tc.refusal)):
				t.Errorf("Validate = %v, want refusal %q", err, tc.refusal)
			}
		})
	}
}

// The host a github-app secret is minted for is its own, or github.com.
func TestSecretDeclGitHubHost(t *testing.T) {
	if got := (SecretDecl{From: FromGitHubApp}).GitHubHost(); got != DefaultGitHubHost {
		t.Errorf("GitHubHost() = %q, want %q", got, DefaultGitHubHost)
	}
	if got := (SecretDecl{From: FromGitHubApp, Host: "ghe.example.com"}).GitHubHost(); got != "ghe.example.com" {
		t.Errorf("GitHubHost() = %q, want ghe.example.com", got)
	}
	if (SecretDecl{From: []any{"github-app"}}).IsGitHubApp() {
		t.Error("an argv from is not github-app")
	}
}
