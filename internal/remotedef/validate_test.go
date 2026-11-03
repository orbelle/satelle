package remotedef

import (
	"bufio"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The cases below are ported from satelle-server's
// internal/remotedef/validate_test.go at 02e27f8, minus the checks that need
// server state (runner catalog, remote_id lookup, NotRun, schema, build params),
// plus the client's own rule (keep_warm never above max_nodes).

// minimalV1 is the least a v1 definition carries.
const minimalV1 = `version = 1
provider = "fly-sprite"
satelle_version = "0.0.709"

[drive]
kind = "story"
`

// taskV1 is the smallest valid v1 task definition, ending in its [drive] table.
const taskV1 = `version = 1
provider = "fly-sprite"
satelle_version = "0.0.709"

[drive]
kind = "task"
task = "sweep"
`

// oldShape is a definition in the earlier shape: every key it carries is refused.
const oldShape = `satelle_version = "0.0.702"
remote_id = "rmt_abc123"
wake_key = "REMOTE_WAKE_KEY"
lifecycle = "shutdown"
spend_cap_usd = 25.5

[[secret]]
key = "CLAUDE_CODE_OAUTH_TOKEN"
scope = "team"
from = "env"

[[secret]]
key = "GH_TOKEN"
scope = "team"
from = "github-app"
permissions = { contents = "read" }

[[repo]]
url = "https://github.com/acme/widgets.git"
ref = "main"

[identity]
key = "REMOTE_PRIVATE_KEY"

[runner]
type = "sprite"

[runner.params]
org = "team-org"

[host]
story_command = "satelle story drive {story}"
bootstrap = [["host-cli", "setup"]]

[drive]
kind = "story"

[keepalive]
instances = 1
window_seconds = 600

[[segment]]
entry = "plan"
exit = "done"

[[trigger]]
name = "manual"
kind = "manual"

[preflight]
env_probe = ["host-cli", "env"]

[[step]]
name = "plan"

[[mcp]]
name = "docs"
url = "https://mcp.example.com/sse"
`

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func hasKey(fs []Finding, key string) bool {
	return slices.ContainsFunc(fs, func(f Finding) bool { return f.Key == key })
}

func findingAt(fs []Finding, key string) (Finding, bool) {
	i := slices.IndexFunc(fs, func(f Finding) bool { return f.Key == key })
	if i < 0 {
		return Finding{}, false
	}
	return fs[i], true
}

// AC1: the full v1 example (the server's own testdata) validates with no
// findings and decodes into every table it carries.
func TestFullV1ValidatesClean(t *testing.T) {
	body := readTestdata(t, "full.v1.remote.toml")
	if fs := Validate(body); len(fs) != 0 {
		t.Fatalf("full v1 example: findings=%+v", fs)
	}
	def, err := Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	if def.Version != 1 || def.Provider != ProviderFlySprite || def.Machine.Swap != "8G" || def.Machine.Image == "" ||
		def.Machine.Region != "syd" || def.Machine.Size == "" ||
		len(def.Setup.OnCreate) != 1 || len(def.Setup.PostStart) != 1 || def.Identity.Principal == "" ||
		def.Lifecycle.MaxNodes == nil || *def.Lifecycle.MaxNodes != 2 || def.Lifecycle.Scope != ScopeRun ||
		def.Trigger == nil || def.Trigger.On != "story.status:ready_for_review" || def.Drive.Kind != DriveTask {
		t.Errorf("decoded = %+v", def)
	}
	want := []SecretEntry{
		{Key: "CLAUDE_CODE_OAUTH_TOKEN", Source: SourceBackend, Name: "CLAUDE_CODE_OAUTH_TOKEN"},
		{Key: "GH_TOKEN", Source: SourceRunToken, Name: "GH_TOKEN", Permissions: map[string]string{"contents": "read", "pull_requests": "write"}},
		{Key: "REVIEW_NOTES", Source: SourceSatelle, Name: "REVIEW_NOTES"},
	}
	if got := def.SecretEntries(); !reflect.DeepEqual(got, want) {
		t.Errorf("secret entries = %+v, want %+v", got, want)
	}
}

// Every v1 fixture this package ships validates clean.
func TestEveryTestdataDefinitionValidates(t *testing.T) {
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		if fs := Validate(readTestdata(t, e.Name())); len(fs) != 0 {
			t.Errorf("testdata/%s: findings=%+v", e.Name(), fs)
		}
	}
}

func TestMinimalV1ValidatesClean(t *testing.T) {
	if fs := Validate([]byte(minimalV1)); len(fs) != 0 {
		t.Fatalf("minimal v1: findings=%+v", fs)
	}
}

// AC1, AC5, AC6: each v1 key in its accepted forms. A table is appended after
// [drive]; a top-level key is put first.
func TestV1AcceptedForms(t *testing.T) {
	for name, body := range map[string]string{
		"swap in K":                 minimalV1 + "[machine]\nswap = \"512K\"\n",
		"swap in M":                 minimalV1 + "[machine]\nswap = \"2048M\"\n",
		"swap in G":                 minimalV1 + "[machine]\nswap = \"8G\"\n",
		"machine strings":           minimalV1 + "[machine]\nregion = \"syd\"\nsize = \"shared-cpu-2x\"\nimage = \"img:base\"\n",
		"secrets provider default":  minimalV1 + "[secrets]\nnames = [\"A_KEY\"]\n",
		"job scope":                 minimalV1 + "[lifecycle]\nscope = \"job\"\nidle_destroy = \"2h\"\n",
		"max_nodes one":             minimalV1 + "[lifecycle]\nmax_nodes = 1\n",
		"keep_warm on a task":       taskV1 + "[lifecycle]\nkeep_warm = 2\nidle_destroy = \"10m\"\nmax_nodes = 3\n",
		"keep_warm equal max_nodes": taskV1 + "[lifecycle]\nkeep_warm = 3\nmax_nodes = 3\n",
		"keep_warm zero on a task":  taskV1 + "[lifecycle]\nkeep_warm = 0\n",
		"satelle override":          minimalV1 + "[secrets.override]\nNOTES = \"satelle:TEAM_NOTES\"\n",
		"status trigger":            minimalV1 + "[trigger]\non = \"story.status:review\"\n",
		"no trigger":                minimalV1,
		"setup lists":               minimalV1 + "[setup]\non_create = [\"make\"]\npost_start = []\n",
		"identity principal":        minimalV1 + "[identity]\nprincipal = \"bot@acme\"\n",
		"name":                      "name = \"builder-2\"\n" + minimalV1,
		"repo and github-app token": minimalV1 + "[[repo]]\nurl = \"https://github.com/acme/w.git\"\nref = \"v1.2.0\"\n[secrets.override]\nGH = { provider = \"github-app\", permissions = { statuses = \"write\" } }\n",
	} {
		if fs := Validate([]byte(body)); len(fs) != 0 {
			t.Errorf("%s: findings=%+v", name, fs)
		}
	}
}

// [secrets] names resolve from the satelle vault: an omitted provider or
// "satelle" validates, the retired "infisical" is refused at secrets.provider
// naming the replacement, and any other value names the one backend.
func TestSecretsProvider(t *testing.T) {
	for _, c := range []struct {
		name, provider string
		want           []string // substrings of the finding at secrets.provider; nil = no finding
	}{
		{"absent", "", nil},
		{"satelle", "satelle", nil},
		{"infisical", "infisical", []string{"retired", `"satelle"`}},
		{"unknown", "vault-x", []string{`"satelle"`}},
	} {
		t.Run(c.name, func(t *testing.T) {
			body := minimalV1 + "[secrets]\nnames = [\"A_KEY\"]\n"
			if c.provider != "" {
				body += "provider = \"" + c.provider + "\"\n"
			}
			fs := Validate([]byte(body))
			f, ok := findingAt(fs, "secrets.provider")
			if c.want == nil {
				if len(fs) != 0 {
					t.Fatalf("want no finding; findings=%+v", fs)
				}
				return
			}
			if !ok || f.Severity != SeverityError || len(Errors(fs)) != 1 {
				t.Fatalf("want one error at secrets.provider; findings=%+v", fs)
			}
			for _, w := range c.want {
				if !strings.Contains(f.Message, w) {
					t.Errorf("message %q does not contain %q", f.Message, w)
				}
			}
		})
	}
}

// AC1: a definition in the earlier shape is refused, every finding at the path
// of its old key carrying the message satelle-server's oldKeys has for it, which
// names the v1 key that replaced it or says v1 removed it. The key → substring
// table is the server's TestOldShapeRefusedAtEachOldKey.
func TestOldShapeRefusedAtEachOldKey(t *testing.T) {
	fs := Validate([]byte(oldShape))
	if len(Errors(fs)) == 0 {
		t.Fatal("the earlier shape validated")
	}
	for key, names := range map[string]string{
		"runner":             "provider",
		"host.bootstrap":     "[setup]",
		"host.story_command": "removed",
		"secret[0]":          "[secrets]",
		"secret[1]":          "[secrets.override]",
		"lifecycle":          "[lifecycle] table",
		"trigger":            "[trigger] table",
		"remote_id":          "removed",
		"wake_key":           "removed",
		"spend_cap_usd":      "removed",
		"identity.key":       "identity.principal",
		"keepalive":          "removed",
		"segment[0]":         "removed",
		"preflight":          "removed",
		"step[0]":            "removed",
		"mcp[0]":             "removed",
		"version":            "version = 1",
		"provider":           "fly-sprite",
	} {
		f, ok := findingAt(fs, key)
		if !ok {
			t.Errorf("no finding at %q; findings=%+v", key, fs)
			continue
		}
		if f.Severity != SeverityError || !strings.Contains(f.Message, names) {
			t.Errorf("finding at %q = %+v, want an error naming %q", key, f, names)
		}
	}
	// Each old key's finding is the server's message for it, exactly.
	for key, base := range map[string]string{
		"secret[0]": "secret", "secret[1]": "secret", "host.bootstrap": "host.bootstrap", "runner": "runner",
		"lifecycle": "lifecycle", "wake_key": "wake_key", "remote_id": "remote_id", "spend_cap_usd": "spend_cap_usd",
		"keepalive": "keepalive", "segment[0]": "segment", "step[0]": "step", "mcp[0]": "mcp",
		"preflight": "preflight", "identity.key": "identity.key",
	} {
		if f, _ := findingAt(fs, key); f.Message != oldKeys[base] {
			t.Errorf("finding at %q = %q, want the oldKeys message %q", key, f.Message, oldKeys[base])
		}
	}
}

// AC1: the client's oldKeys are satelle-server's, entry for entry: the golden
// copy in testdata is the server map at 02e27f8, copied verbatim.
func TestOldKeysMirrorTheServer(t *testing.T) {
	f, err := os.Open("testdata/server-oldkeys.02e27f8.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	server := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.Index(line, `":`)
		if i < 0 {
			t.Fatalf("golden line %q is not a map entry", line)
		}
		key, kerr := strconv.Unquote(line[:i+1])
		msg, merr := strconv.Unquote(strings.TrimSuffix(strings.TrimSpace(line[i+2:]), ","))
		if kerr != nil || merr != nil {
			t.Fatalf("golden line %q: %v %v", line, kerr, merr)
		}
		server[key] = msg
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(oldKeys, server) {
		t.Errorf("client oldKeys differ from the server's:\nclient %v\nserver %v", oldKeys, server)
	}
}

// AC1, AC5, AC6: each key outside v1, and each v1 key with a bad value, is a
// finding at its own path; no finding echoes the value it refused.
func TestV1FieldFindings(t *testing.T) {
	const sentinel = "SENTINEL_V1_5e2a"
	withRepo := minimalV1 + "[[repo]]\nurl = \"https://github.com/a/b\"\nref = \"main\"\n"
	cases := []struct {
		name, body, key string
	}{
		{"missing version", strings.Replace(minimalV1, "version = 1\n", "", 1), "version"},
		{"version 2", strings.Replace(minimalV1, "version = 1", "version = 2", 1), "version"},
		{"version as a string", strings.Replace(minimalV1, "version = 1", `version = "`+sentinel+`"`, 1), "version"},
		{"missing provider", strings.Replace(minimalV1, "provider = \"fly-sprite\"\n", "", 1), "provider"},
		{"unknown provider", strings.Replace(minimalV1, `provider = "fly-sprite"`, `provider = "`+sentinel+`"`, 1), "provider"},
		{"provider aws", strings.Replace(minimalV1, `provider = "fly-sprite"`, `provider = "aws"`, 1), "provider"},
		{"provider sprite", strings.Replace(minimalV1, `provider = "fly-sprite"`, `provider = "sprite"`, 1), "provider"},
		{"remote_id", "remote_id = \"x\"\n" + minimalV1, "remote_id"},
		{"bad name", "name = \"" + sentinel + "\"\n" + minimalV1, "name"},
		{"unknown top-level key", "colour = \"" + sentinel + "\"\n" + minimalV1, "colour"},
		{"swap without a unit", minimalV1 + "[machine]\nswap = \"8\"\n", "machine.swap"},
		{"swap 8GB", minimalV1 + "[machine]\nswap = \"8GB\"\n", "machine.swap"},
		{"swap as a bool", minimalV1 + "[machine]\nswap = true\n", "machine.swap"},
		{"swap with a bad unit", minimalV1 + "[machine]\nswap = \"8" + sentinel + "\"\n", "machine.swap"},
		{"empty region", minimalV1 + "[machine]\nregion = \"\"\n", "machine.region"},
		{"empty size", minimalV1 + "[machine]\nsize = \" \"\n", "machine.size"},
		{"image as a number", minimalV1 + "[machine]\nimage = 7\n", "machine.image"},
		{"unknown machine key", minimalV1 + "[machine]\ncpus = 4\n", "machine.cpus"},
		{"setup command as argv", minimalV1 + "[setup]\non_create = [[\"make\"]]\n", "setup.on_create[0]"},
		{"empty setup command", minimalV1 + "[setup]\npost_start = [\" \"]\n", "setup.post_start[0]"},
		{"identity without principal", minimalV1 + "[identity]\nprincipal = \"\"\n", "identity.principal"},
		{"secrets provider", minimalV1 + "[secrets]\nprovider = \"" + sentinel + "\"\n", "secrets.provider"},
		{"bad name key", minimalV1 + "[secrets]\nnames = [\"" + sentinel + "-x\"]\n", "secrets.names[0]"},
		{"duplicate name", minimalV1 + "[secrets]\nnames = [\"A\", \"A\"]\n", "secrets.names[1]"},
		{"override also a name", minimalV1 + "[secrets]\nnames = [\"A\"]\n[secrets.override]\nA = \"satelle:A\"\n", "secrets.override.A"},
		{"override in neither form", minimalV1 + "[secrets.override]\nA = \"" + sentinel + "\"\n", "secrets.override.A"},
		{"satelle ref with no name", minimalV1 + "[secrets.override]\nX = \"satelle:\"\n", "secrets.override.X"},
		{"override names a bad satelle key", minimalV1 + "[secrets.override]\nA = \"satelle:" + sentinel + "-x\"\n", "secrets.override.A"},
		{"override provider", minimalV1 + "[secrets.override]\nA = { provider = \"" + sentinel + "\" }\n", "secrets.override.A.provider"},
		{"github-app with no repo", minimalV1 + "[secrets.override]\nA = { provider = \"github-app\", permissions = { contents = \"read\" } }\n", "secrets.override.A"},
		{"github-app with no permissions", withRepo + "[secrets.override]\nGH_TOKEN = { provider = \"github-app\" }\n", "secrets.override.GH_TOKEN.permissions"},
		{"github-app with empty permissions", withRepo + "[secrets.override]\nGH_TOKEN = { provider = \"github-app\", permissions = {} }\n", "secrets.override.GH_TOKEN.permissions"},
		{"bad permission level", withRepo + "[secrets.override]\nGH_TOKEN = { provider = \"github-app\", permissions = { contents = \"" + sentinel + "\" } }\n", "secrets.override.GH_TOKEN.permissions.contents"},
		{"admin permission level", withRepo + "[secrets.override]\nGH_TOKEN = { provider = \"github-app\", permissions = { contents = \"admin\" } }\n", "secrets.override.GH_TOKEN.permissions.contents"},
		{"unknown override key", withRepo + "[secrets.override]\nGH_TOKEN = { provider = \"github-app\", permissions = { contents = \"read\" }, host = \"" + sentinel + "\" }\n", "secrets.override.GH_TOKEN.host"},
		{"bad scope", minimalV1 + "[lifecycle]\nscope = \"" + sentinel + "\"\n", "lifecycle.scope"},
		{"scope forever", minimalV1 + "[lifecycle]\nscope = \"forever\"\n", "lifecycle.scope"},
		{"bad idle_destroy", minimalV1 + "[lifecycle]\nidle_destroy = \"" + sentinel + "\"\n", "lifecycle.idle_destroy"},
		{"negative idle_destroy", minimalV1 + "[lifecycle]\nidle_destroy = \"-5m\"\n", "lifecycle.idle_destroy"},
		{"max_nodes zero", minimalV1 + "[lifecycle]\nmax_nodes = 0\n", "lifecycle.max_nodes"},
		{"max_nodes as a string", minimalV1 + "[lifecycle]\nmax_nodes = \"" + sentinel + "\"\n", "lifecycle.max_nodes"},
		{"keep_warm negative", taskV1 + "[lifecycle]\nkeep_warm = -1\n", "lifecycle.keep_warm"},
		{"keep_warm as a string", taskV1 + "[lifecycle]\nkeep_warm = \"" + sentinel + "\"\n", "lifecycle.keep_warm"},
		{"keep_warm as a float", taskV1 + "[lifecycle]\nkeep_warm = 1.5\n", "lifecycle.keep_warm"},
		{"keep_warm on a story drive", minimalV1 + "[lifecycle]\nkeep_warm = 1\n", "lifecycle.keep_warm"},
		{"keep_warm zero on a story drive", minimalV1 + "[lifecycle]\nkeep_warm = 0\n", "lifecycle.keep_warm"},
		{"keep_warm on a github-runner drive", strings.Replace(minimalV1, `kind = "story"`, `kind = "github-runner"`, 1) + "[lifecycle]\nkeep_warm = 1\n", "lifecycle.keep_warm"},
		// The client's own rule: a warm node is a live node.
		{"keep_warm above max_nodes", taskV1 + "[lifecycle]\nkeep_warm = 3\nmax_nodes = 2\n", "lifecycle.keep_warm"},
		{"trigger with no on", minimalV1 + "[trigger]\n", "trigger.on"},
		{"trigger on a bad event", minimalV1 + "[trigger]\non = \"" + sentinel + "\"\n", "trigger.on"},
		{"trigger on an empty status", minimalV1 + "[trigger]\non = \"story.status:\"\n", "trigger.on"},
		{"trigger on github.workflow_job", minimalV1 + "[trigger]\non = \"github.workflow_job\"\n", "trigger.on"},
		{"drive kind github-runner", strings.Replace(minimalV1, `kind = "story"`, `kind = "github-runner"`, 1), "drive.kind"},
		{"drive kind step", strings.Replace(minimalV1, `kind = "story"`, `kind = "step"`, 1), "drive.kind"},
		{"drive kind performer", strings.Replace(minimalV1, `kind = "story"`, `kind = "performer"`, 1), "drive.kind"},
		{"drive kind missing", strings.Replace(minimalV1, "kind = \"story\"\n", "", 1), "drive.kind"},
		{"task on a story", minimalV1 + "task = \"x\"\n", "drive.task"},
		{"task kind needs a task", strings.Replace(minimalV1, `kind = "story"`, `kind = "task"`, 1), "drive.task"},
		{"repo with a credential", minimalV1 + "[[repo]]\nurl = \"https://u:" + sentinel + "@github.com/a/b\"\nref = \"main\"\n", "repo[0].url"},
		{"network allow", minimalV1 + "[network]\nallow = [\"https://" + sentinel + "\"]\n", "network.allow[0]"},
		{"credential in setup", minimalV1 + "[setup]\napi_token = \"" + sentinel + "\"\n", "setup.api_token"},
		// AC6: each removed key at its own path.
		{"spend_cap_usd", "spend_cap_usd = 5\n" + minimalV1, "spend_cap_usd"},
		{"keepalive", minimalV1 + "[keepalive]\ninstances = 1\n", "keepalive"},
		{"segment", minimalV1 + "[[segment]]\nentry = \"plan\"\n", "segment[0]"},
		{"step", minimalV1 + "[[step]]\nname = \"plan\"\n", "step[0]"},
		{"wake_key", "wake_key = \"K\"\n" + minimalV1, "wake_key"},
		{"mcp", minimalV1 + "[[mcp]]\nname = \"docs\"\n", "mcp[0]"},
		{"preflight", minimalV1 + "[preflight]\nenv_probe = [\"x\"]\n", "preflight"},
		{"identity key", minimalV1 + "[identity]\nprincipal = \"bot\"\nkey = \"K\"\n", "identity.key"},
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

// AC6: each removed old-shape key is refused with the server's oldKeys message,
// not as a bare unknown key.
func TestRemovedKeysCarryTheOldKeysMessage(t *testing.T) {
	for key, body := range map[string]string{
		"spend_cap_usd": "spend_cap_usd = 5\n" + minimalV1,
		"keepalive":     minimalV1 + "[keepalive]\ninstances = 1\n",
		"segment[0]":    minimalV1 + "[[segment]]\nentry = \"plan\"\n",
		"step[0]":       minimalV1 + "[[step]]\nname = \"plan\"\n",
		"wake_key":      "wake_key = \"K\"\n" + minimalV1,
		"mcp[0]":        minimalV1 + "[[mcp]]\nname = \"docs\"\n",
		"preflight":     minimalV1 + "[preflight]\nenv_probe = [\"x\"]\n",
		"identity.key":  minimalV1 + "[identity]\nprincipal = \"bot\"\nkey = \"K\"\n",
	} {
		base, _, _ := strings.Cut(key, "[")
		f, ok := findingAt(Validate([]byte(body)), key)
		if !ok || f.Message != oldKeys[base] {
			t.Errorf("%s: finding = %+v (found %v), want %q", key, f, ok, oldKeys[base])
		}
	}
}

// github.workflow_job is refused at trigger.on like any value not of the form
// story.status:<status>, and github-runner at drive.kind like any unknown kind.
func TestGitHubRunnerFormsAreNotReserved(t *testing.T) {
	fs := Validate([]byte(minimalV1 + "[trigger]\non = \"github.workflow_job\"\n"))
	if f, ok := findingAt(fs, "trigger.on"); !ok || f.Message != "must be story.status:<status>" {
		t.Fatalf("finding at trigger.on = %+v (found %v); findings=%+v", f, ok, fs)
	}
	fs = Validate([]byte(strings.Replace(minimalV1, `kind = "story"`, `kind = "github-runner"`, 1)))
	if f, ok := findingAt(fs, "drive.kind"); !ok || f.Message != "is not one of "+enumList(DriveKinds) {
		t.Fatalf("finding at drive.kind = %+v (found %v); findings=%+v", f, ok, fs)
	}
}

// One defect never hides another: an old key, a type clash and a bad v1 value
// in one definition are each reported.
func TestSeveralDefectsAreAllReported(t *testing.T) {
	body := `lifecycle = "idle"
provider = "fly-sprite"
satelle_version = "0.0.709"
version = 1

[runner]
type = "sprite"

[machine]
swap = true

[drive]
kind = "nope"
`
	fs := Validate([]byte(body))
	for _, key := range []string{"lifecycle", "runner", "machine.swap", "drive.kind"} {
		if !hasKey(fs, key) {
			t.Errorf("missing finding at %q: %+v", key, fs)
		}
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
	if !hasKey(fs, "parse") {
		t.Fatalf("findings = %+v", fs)
	}
	if b, _ := json.Marshal(fs); strings.Contains(string(b), sentinel) {
		t.Fatalf("parse finding echoed the input: %s", b)
	}
	if !strings.Contains(fs[0].Message, "line 1") {
		t.Errorf("parse finding should name the line: %q", fs[0].Message)
	}
}

// No finding carries a submitted value, whatever check refused it, old shape or v1.
func TestNoFindingEchoesAValue(t *testing.T) {
	const (
		cred = "SENTINEL_CRED_7f3a"
		bad  = "SENTINEL_BAD_9c1e"
	)
	body := strings.NewReplacer(
		`lifecycle = "shutdown"`, `lifecycle = "`+bad+`"`,
		`satelle_version = "0.0.702"`, `satelle_version = "`+bad+`"`,
		`ref = "main"`, `ref = "-`+bad+`"`,
		`type = "sprite"`, `type = "`+bad+`"`,
		`org = "team-org"`, `org = "`+cred+`"`,
		`from = "env"`, `from = "`+cred+`"`,
		`url = "https://github.com/acme/widgets.git"`, `url = "https://u:`+cred+`@github.com/acme/widgets.git"`,
	).Replace(oldShape)
	fs := Validate([]byte(body))
	if len(Errors(fs)) == 0 {
		t.Fatal("expected findings")
	}
	b, _ := json.Marshal(fs)
	_, perr := Parse([]byte(body))
	for _, s := range []string{cred, bad} {
		if strings.Contains(string(b), s) {
			t.Errorf("findings echo %q: %s", s, b)
		}
		if perr == nil || strings.Contains(perr.Error(), s) {
			t.Errorf("Parse error %v echoes %q or is nil", perr, s)
		}
	}
}
