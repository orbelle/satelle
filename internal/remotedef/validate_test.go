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
permissions = { contents = "read" }

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
permissions = { contents = "read" }
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
permissions = { contents = "read" }
`,
			refusal: `secret GH_TOKEN: from = "github-app" needs a [[repo]] on github.com`,
		},
		{
			name: "named host but repos only on github.com",
			toml: `
[[secret]]
key = "GH_TOKEN"
from = "github-app"
permissions = { contents = "read" }
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

// A github-app [[secret]] declares the permissions its token is minted with: a
// non-empty table of GitHub App permission names matching [a-z_]+, each exactly
// "read" or "write". A missing table, an empty one, a bad name, a bad value, or a
// table on a secret of any other source is refused naming secret[i].permissions,
// i the secret's 0-based position: the rules satelle-server applies
// (sty_5b905ad8).
func TestValidateGitHubAppSecretDeclaresItsPermissions(t *testing.T) {
	const repo = "\n[[repo]]\nurl = \"https://github.com/acme/widgets.git\"\nref = \"main\"\n"
	app := func(perms string) string {
		return "[[secret]]\nkey = \"GH_TOKEN\"\nfrom = \"github-app\"\n" + perms + repo
	}
	cases := []struct {
		name, toml string
		refusal    string // "" accepts
	}{
		{name: "the pr-approve set", toml: app(`permissions = { contents = "read", pull_requests = "write", statuses = "write" }`)},
		{name: "missing", toml: app(""), refusal: `secret[0].permissions: required for from = "github-app"`},
		{name: "empty", toml: app(`permissions = {}`), refusal: `secret[0].permissions: must not be empty`},
		{name: "uppercase name", toml: app(`permissions = { Contents = "read" }`), refusal: `secret[0].permissions: permission name "Contents" must match [a-z_]+`},
		{name: "hyphenated name", toml: app(`permissions = { pull-requests = "write" }`), refusal: `secret[0].permissions: permission name "pull-requests" must match [a-z_]+`},
		{name: "admin value", toml: app(`permissions = { contents = "admin" }`), refusal: `secret[0].permissions: permission contents = "admin" must be "read" or "write"`},
		{name: "capitalised value", toml: app(`permissions = { contents = "Read" }`), refusal: `secret[0].permissions: permission contents = "Read" must be "read" or "write"`},
		{
			name:    "a table on an env secret",
			toml:    "[[secret]]\nkey = \"API_KEY\"\nfrom = \"env\"\npermissions = { contents = \"read\" }\n",
			refusal: `secret[0].permissions: only a from = "github-app" secret declares permissions`,
		},
		{
			name:    "the second secret is the bad one",
			toml:    "[[secret]]\nkey = \"API_KEY\"\nfrom = \"env\"\n\n" + app(""),
			refusal: `secret[1].permissions: required for from = "github-app"`,
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

// An absent permissions key decodes to a nil table and permissions = {} to an
// empty one, which is how Validate tells a missing table from an empty one.
func TestPermissionsMissingAndEmptyDecodeApart(t *testing.T) {
	missing, err := Parse([]byte("[[secret]]\nkey = \"GH_TOKEN\"\nfrom = \"github-app\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	empty, err := Parse([]byte("[[secret]]\nkey = \"GH_TOKEN\"\nfrom = \"github-app\"\npermissions = {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if missing.Secret[0].Permissions != nil {
		t.Errorf("absent permissions = %#v, want nil", missing.Secret[0].Permissions)
	}
	if p := empty.Secret[0].Permissions; p == nil || len(p) != 0 {
		t.Errorf("permissions = {} decoded to %#v, want a non-nil empty table", p)
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
