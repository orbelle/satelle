package remotedef

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// minimalV1 is the least a v1 definition carries.
const minimalV1 = `version = 1
provider = "docker-local"
satelle_version = "0.0.709"

[drive]
kind = "item"
`

// taskV1 is the smallest valid v1 task definition, ending in its [drive] table.
const taskV1 = `version = 1
provider = "fly-sprite"
satelle_version = "0.0.709"

[drive]
kind = "task"
task = "sweep"
`

func hasKey(fs []Finding, key string) bool {
	return slices.ContainsFunc(fs, func(f Finding) bool { return f.Key == key })
}

func TestFullV1ValidatesClean(t *testing.T) {
	b, err := os.ReadFile("testdata/full.v1.remote.toml")
	if err != nil {
		t.Fatal(err)
	}
	if fs := Validate(b); len(fs) != 0 {
		t.Fatalf("findings = %+v", fs)
	}
	def, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if def.Machine.Swap != "8G" || def.Setup.OnCreate[0] != "apt-get install -y jq" || len(def.Repos) != 1 ||
		def.Network.Allow[0] != "api.anthropic.com" || *def.Lifecycle.MaxNodes != 2 || def.Drive.Task != "pr-approve" {
		t.Fatalf("decoded = %+v", def)
	}
}

func TestEveryTestdataAndRepoDefinitionValidates(t *testing.T) {
	paths, _ := filepath.Glob("testdata/*.toml")
	paths = append(paths, "../../.satelle/remotes/satelle-cicd/remote.toml")
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if fs := Errors(Validate(b)); len(fs) != 0 {
			t.Errorf("%s: %s", p, FormatFindings(fs))
		}
	}
}

func TestMinimalDefinitionsValidateClean(t *testing.T) {
	for _, body := range []string{minimalV1, taskV1} {
		if fs := Validate([]byte(body)); len(fs) != 0 {
			t.Errorf("findings = %+v for\n%s", fs, body)
		}
	}
}

// Each key outside v1, and each v1 key with a bad value, is a finding at its
// own path; no finding echoes the value it refused.
func TestV1FieldFindings(t *testing.T) {
	const sentinel = "SENTINEL_V1_5e2a"
	withRepo := minimalV1 + "[[repo]]\nurl = \"https://github.com/a/b\"\nref = \"main\"\n"
	cases := []struct{ name, body, key string }{
		{"missing version", strings.Replace(minimalV1, "version = 1\n", "", 1), "version"},
		{"version 2", strings.Replace(minimalV1, "version = 1", "version = 2", 1), "version"},
		{"missing provider", strings.Replace(minimalV1, "provider = \"docker-local\"\n", "", 1), "provider"},
		{"unknown provider", strings.Replace(minimalV1, `provider = "docker-local"`, `provider = "`+sentinel+`"`, 1), "provider"},
		{"bad satelle_version", strings.Replace(minimalV1, `"0.0.709"`, `"`+sentinel+`"`, 1), "satelle_version"},
		{"bad name", "name = \"" + sentinel + "\"\n" + minimalV1, "name"},
		{"unknown top-level key", "colour = \"" + sentinel + "\"\n" + minimalV1, "colour"},
		{"identity is not v1", minimalV1 + "[identity]\nprincipal = \"bot\"\n", "identity"},
		{"trigger is not v1", minimalV1 + "[trigger]\non = \"x\"\n", "trigger"},
		{"environment is not v1", "environment = \"prd\"\n" + minimalV1, "environment"},
		{"swap without a unit", minimalV1 + "[machine]\nswap = \"8\"\n", "machine.swap"},
		{"empty region", minimalV1 + "[machine]\nregion = \"\"\n", "machine.region"},
		{"image as a number", minimalV1 + "[machine]\nimage = 7\n", "machine.image"},
		{"unknown machine key", minimalV1 + "[machine]\ncpus = 4\n", "machine.cpus"},
		{"setup command as argv", minimalV1 + "[setup]\non_create = [[\"make\"]]\n", "setup.on_create[0]"},
		{"empty setup command", minimalV1 + "[setup]\npost_start = [\" \"]\n", "setup.post_start[0]"},
		{"secrets provider", minimalV1 + "[secrets]\nprovider = \"" + sentinel + "\"\n", "secrets.provider"},
		{"bad name key", minimalV1 + "[secrets]\nnames = [\"" + sentinel + "-x\"]\n", "secrets.names[0]"},
		{"duplicate name", minimalV1 + "[secrets]\nnames = [\"A\", \"A\"]\n", "secrets.names[1]"},
		{"override also a name", minimalV1 + "[secrets]\nnames = [\"A\"]\n[secrets.override]\nA = \"satelle:A\"\n", "secrets.override.A"},
		{"override in neither form", minimalV1 + "[secrets.override]\nA = \"" + sentinel + "\"\n", "secrets.override.A"},
		{"github-app with no repo", minimalV1 + "[secrets.override]\nA = { provider = \"github-app\", permissions = { contents = \"read\" } }\n", "secrets.override.A"},
		{"github-app with no permissions", withRepo + "[secrets.override]\nGH_TOKEN = { provider = \"github-app\" }\n", "secrets.override.GH_TOKEN.permissions"},
		{"bad permission level", withRepo + "[secrets.override]\nGH_TOKEN = { provider = \"github-app\", permissions = { contents = \"" + sentinel + "\" } }\n", "secrets.override.GH_TOKEN.permissions.contents"},
		{"bad scope", minimalV1 + "[lifecycle]\nscope = \"" + sentinel + "\"\n", "lifecycle.scope"},
		{"bad idle_destroy", minimalV1 + "[lifecycle]\nidle_destroy = \"" + sentinel + "\"\n", "lifecycle.idle_destroy"},
		{"max_nodes zero", minimalV1 + "[lifecycle]\nmax_nodes = 0\n", "lifecycle.max_nodes"},
		{"keep_warm on an item drive", minimalV1 + "[lifecycle]\nkeep_warm = 1\n", "lifecycle.keep_warm"},
		{"keep_warm above max_nodes", taskV1 + "[lifecycle]\nkeep_warm = 3\nmax_nodes = 2\n", "lifecycle.keep_warm"},
		{"drive kind unknown", strings.Replace(minimalV1, `kind = "item"`, `kind = "story"`, 1), "drive.kind"},
		{"drive kind missing", strings.Replace(minimalV1, "kind = \"item\"\n", "", 1), "drive.kind"},
		{"task on an item drive", minimalV1 + "task = \"x\"\n", "drive.task"},
		{"task kind needs a task", strings.Replace(minimalV1, `kind = "item"`, `kind = "task"`, 1), "drive.task"},
		{"repo with a credential", minimalV1 + "[[repo]]\nurl = \"https://u:" + sentinel + "@github.com/a/b\"\nref = \"main\"\n", "repo[0].url"},
		{"repo over ssh", minimalV1 + "[[repo]]\nurl = \"ssh://github.com/a/b\"\nref = \"main\"\n", "repo[0].url"},
		{"ref as an option", minimalV1 + "[[repo]]\nurl = \"https://github.com/a/b\"\nref = \"-" + sentinel + "\"\n", "repo[0].ref"},
		{"network allow", minimalV1 + "[network]\nallow = [\"https://" + sentinel + "\"]\n", "network.allow[0]"},
		{"credential in setup", minimalV1 + "[setup]\napi_token = \"" + sentinel + "\"\n", "setup.api_token"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fs := Validate([]byte(c.body))
			if len(Errors(fs)) == 0 || !hasKey(fs, c.key) {
				t.Fatalf("want an error at %q; findings=%+v", c.key, fs)
			}
			if b, _ := json.Marshal(fs); strings.Contains(string(b), sentinel) {
				t.Errorf("a finding echoes the submitted value: %s", b)
			}
			if _, err := Parse([]byte(c.body)); err == nil || !strings.Contains(err.Error(), c.key+": ") {
				t.Errorf("Parse = %v, want a refusal naming %q", err, c.key)
			}
		})
	}
}

func TestEmptyBodyReportsWhatIsRequired(t *testing.T) {
	fs := Validate(nil)
	for _, key := range []string{"version", "provider", "satelle_version", "drive.kind"} {
		if !hasKey(fs, key) {
			t.Errorf("missing finding for %q: %+v", key, fs)
		}
	}
}

func TestMalformedTOMLNamesPositionOnly(t *testing.T) {
	const sentinel = "SENTINEL_PARSE_5d2b"
	fs := Validate([]byte("satelle_version = \"" + sentinel + "\nname = ["))
	if !hasKey(fs, "parse") || !strings.Contains(fs[0].Message, "line 1") {
		t.Fatalf("findings = %+v", fs)
	}
	if b, _ := json.Marshal(fs); strings.Contains(string(b), sentinel) {
		t.Fatalf("parse finding echoed the input: %s", b)
	}
}
