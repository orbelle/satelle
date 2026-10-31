package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
)

// localLayerRepo scaffolds a repo whose shared agents.toml is sharedBody and
// whose machine-local override is localBody (empty = no file), with a catalog
// holding the profile "judge" under SATELLE_HOME.
func localLayerRepo(t *testing.T, sharedBody, localBody string) string {
	t.Helper()
	repo := tempRepo(t)
	wfDir := filepath.Join(repo, ".satelle", "workflows")
	writeRoute(t, wfDir, "[\"*\"]\nobligations = [\"raised\", \"closed\"]\n",
		"[raised]\nstatus = \"backlog\"\nstart = true\n\n[closed]\nstatus = \"done\"\nterminal = true\nrequires = [\"raised\"]\n")
	if err := os.WriteFile(filepath.Join(wfDir, config.AgentsConfigName), []byte(sharedBody), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(repo, ".satelle", config.AgentsConfigName))
	if localBody != "" {
		if err := os.WriteFile(filepath.Join(wfDir, config.LocalAgentsName), []byte(localBody), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	catalog := "[profiles.judge]\nrole = \"reviewer\"\ncommand = \"" + agentcli.DefaultClaudeCommand + "\"\ntools = \"Read,Grep,Glob\"\nmodel = \"catalog-model\"\n"
	if err := os.MkdirAll(config.GlobalDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.GlobalAgentsPath(), []byte(catalog), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo
}

const localLayerShared = "[executor]\nrole = \"agent\"\ncommand = \"in-loop\"\n\n[reviewer]\nrole = \"reviewer\"\ncommand = \"" +
	agentcli.DefaultClaudeCommand + "\"\ntools = \"Read,Grep,Glob\"\n"

// A profile= left in the SHARED agents.toml is warned about by both commands,
// naming the binding and the local file to move it into.
func TestSharedProfileWarnedByAgentValidateAndDoctor(t *testing.T) {
	localLayerRepo(t, localLayerShared+"profile = \"judge\"\n", "")
	for _, verb := range []string{"agent validate", "doctor"} {
		out, _ := runRoot(t, strings.Fields(verb)...)
		for _, want := range []string{`[reviewer] profile: profile="judge"`, ".satelle/workflows/agents.local.toml"} {
			if !strings.Contains(out, want) {
				t.Errorf("%s must warn on a shared profile=, missing %q:\n%s", verb, want, out)
			}
		}
	}
}

// The same profile= written only in agents.local.toml draws no warning, and a
// field the local file wins reads as local with its path on both commands.
func TestLocalLayerShownByAgentValidateAndDoctor(t *testing.T) {
	localLayerRepo(t, localLayerShared, "[reviewer]\nprofile = \"judge\"\nmodel = \"local-model\"\n")
	for _, verb := range []string{"agent validate", "doctor"} {
		out, err := runRoot(t, strings.Fields(verb)...)
		if verb == "agent validate" && err != nil {
			t.Fatalf("%s: %v\n%s", verb, err, out)
		}
		if strings.Contains(out, "[reviewer] profile: profile=") {
			t.Errorf("%s: a profile= only in agents.local.toml must not warn:\n%s", verb, out)
		}
		for _, want := range []string{
			`model = "local-model" ← local (.satelle/workflows/agents.local.toml)`,
			`← repo (.satelle/workflows/agents.toml)`,
		} {
			if !strings.Contains(out, want) {
				t.Errorf("%s must show each field's tier and file, missing %q:\n%s", verb, want, out)
			}
		}
	}
}

// `agent validate --help` names the local file, its precedence and that it
// never syncs.
func TestAgentValidateHelpNamesLocalLayer(t *testing.T) {
	out, err := runRoot(t, "agent", "validate", "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Precedence:\nagents.local.toml > agents.toml >", "it never\nsyncs"} {
		if !strings.Contains(out, want) {
			t.Errorf("agent validate --help missing %q:\n%s", want, out)
		}
	}
}

// When .satelle/ itself is not ignored (the operator negates part of it), the
// needed set carries the machine-local agents file.
func TestGitignoreNeededIgnoresLocalAgents(t *testing.T) {
	need := gitignoreNeeded("!.satelle/workflows/agents.toml\n", t.TempDir())
	var found bool
	for _, line := range need {
		found = found || line == ".satelle/workflows/agents.local.toml"
	}
	if !found {
		t.Errorf("needed set must ignore .satelle/workflows/agents.local.toml when .satelle/ is not ignored: %v", need)
	}
}

// sty_1f496a91 AC2/AC3: a repo with no agents.local.toml whose seats the catalog's
// [seats] map names resolves each seat to its profile's command, model, tools and
// timeouts, and `agent validate` shows them on the GRANT lines — while the repo's
// own agents.toml carries no profile= for a sync push to refuse.
func TestAgentValidateShowsCatalogSeatMapWithoutLocalFile(t *testing.T) {
	shared := "[executor]\nrole = \"agent\"\ncommand = \"in-loop\"\n\n[reviewer]\nrole = \"reviewer\"\n\n[coder]\nrole = \"agent\"\n"
	repo := localLayerRepo(t, shared, "")
	writeCatalog(t, `
[profiles.seat-judge]
role         = "reviewer"
command      = "`+agentcli.DefaultClaudeCommand+`"
tools        = "Read,Grep,Glob"
model        = "judge-seat-model"
timeout      = "25m"
idle_timeout = "6m"

[profiles.seat-coder]
role         = "agent"
interface    = "stream"
command      = "`+agentcli.DefaultClaudeStreamCommand+`"
tools        = "Read,Grep,Glob,Edit,Write"
model        = "coder-seat-model"
timeout      = "40m"
idle_timeout = "9m"

[seats]
reviewer = "seat-judge"
coder    = "seat-coder"
`)
	wfDir := filepath.Join(repo, ".satelle", "workflows")
	if _, err := os.Stat(filepath.Join(wfDir, config.LocalAgentsName)); !os.IsNotExist(err) {
		t.Fatalf("the fixture must carry no %s: %v", config.LocalAgentsName, err)
	}
	body, err := os.ReadFile(filepath.Join(wfDir, config.AgentsConfigName))
	if err != nil {
		t.Fatal(err)
	}
	if refusals, cerr := config.CheckAgentsTransport(body); cerr != nil || len(refusals) != 0 || strings.Contains(string(body), "profile") {
		t.Fatalf("the repo agents.toml must carry no profile= and push clean: %v %v", refusals, cerr)
	}

	out, err := runRoot(t, "agent", "validate")
	if err != nil {
		t.Fatalf("agent validate: %v\n%s", err, out)
	}
	grant := func(name string) string {
		for _, ln := range strings.Split(out, "\n") {
			if strings.Contains(ln, "GRANT ["+name+"]") {
				return ln
			}
		}
		return ""
	}
	for name, wants := range map[string][]string{
		"reviewer": {`command="` + agentcli.DefaultClaudeCommand + `"`, `model="judge-seat-model"`, `tools="Read,Grep,Glob"`, `timeout="25m"`, `idle_timeout="6m"`},
		"coder":    {`command="` + agentcli.DefaultClaudeStreamCommand + `"`, `model="coder-seat-model"`, `tools="Read,Grep,Glob,Edit,Write"`, `timeout="40m"`, `idle_timeout="9m"`},
	} {
		line := grant(name)
		for _, want := range wants {
			if !strings.Contains(line, want) {
				t.Errorf("GRANT [%s] line lacks %q: %q\n%s", name, want, line, out)
			}
		}
	}
	for _, want := range []string{
		`model = "coder-seat-model" ← global-seat:seat-coder (~/.satelle/agents.toml)`,
		`model = "judge-seat-model" ← global-seat:seat-judge (~/.satelle/agents.toml)`,
		"SEAT [coder] -> seat-coder",
		"Machine-local layer: " + config.LocalAgentsRel + " (absent",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("agent validate missing %q:\n%s", want, out)
		}
	}
}
