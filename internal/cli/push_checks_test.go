package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/pflag"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/agentvalidate"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/doctor"
	"github.com/bobmcallan/satelle/internal/health"
	"github.com/bobmcallan/satelle/internal/hosted"
)

// What a push checks and what it sends (sty_89b9df20): the three push commands
// refuse a shared agents.toml that carries machine-specific content and a
// configuration that does not validate, before any network call, and what they
// send is the file's own bytes.

// newCountingServer is a hosted server that must never be reached: every request
// is counted and answered with a 500.
func newCountingServer(t *testing.T) (*httptest.Server, func() int) {
	t.Helper()
	var n atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	t.Cleanup(ts.Close)
	return ts, func() int { return int(n.Load()) }
}

const (
	configPushToml = "[sync]\n" + boundProjectToml
	teamBoundToml  = "[hosted]\nworkspace = \"Acme\"\n"
)

// pushRunner is one push command, run as `satelle` would: it returns the output
// and the error.
type pushRunner struct {
	name string
	toml string // the satelle.toml the command needs to get as far as the server
	run  func(t *testing.T, url string, dryRun bool) (string, error)
}

func pushRunners() []pushRunner {
	return []pushRunner{
		{"sync config push", configPushToml, func(t *testing.T, url string, dry bool) (string, error) {
			cmd, buf := testCmd()
			err := runSyncConfigPush(cmd, url, "", dry)
			return buf.String(), err
		}},
		{"sync config push --snapshot-only", configPushToml, func(t *testing.T, url string, dry bool) (string, error) {
			args := []string{"sync", "config", "push", "--snapshot-only", "--server", url}
			if dry {
				args = append(args, "--dry-run")
			}
			return runRoot(t, args...)
		}},
		{"publish push", teamBoundToml, func(t *testing.T, url string, dry bool) (string, error) {
			cmd, buf := testCmd()
			err := runPublishPush(cmd, url, "", "", "", dry, []string{"workflows/agents.toml"})
			return buf.String(), err
		}},
		{"sync bindings push", teamBoundToml, func(t *testing.T, url string, dry bool) (string, error) {
			cmd, buf := testCmd()
			err := runSyncBindingsPush(cmd, url, "", dry)
			return buf.String(), err
		}},
	}
}

// agentsPushRepo is a repo whose shared agents layer is body, pointed at.
func agentsPushRepo(t *testing.T, satelleToml, body string) string {
	t.Helper()
	repo := syncConfigRepo(t, satelleToml)
	_ = os.Remove(filepath.Join(repo, ".satelle", "agents.toml")) // the legacy seed
	writeRepoFile(t, repo, ".satelle/workflows/agents.toml", body)
	pointAt(t, repo)
	return repo
}

// AC1/AC7: every push command — snapshot-only and dry runs included — refuses a
// shared agents.toml carrying machine-specific content, naming file, section and
// field and pointing at agents.local.toml, and contacts nothing.
func TestPushCommandsRefuseMachineSpecificAgents(t *testing.T) {
	for _, r := range pushRunners() {
		for _, dry := range []bool{false, true} {
			name := r.name
			if dry {
				name += " --dry-run"
			}
			t.Run(name, func(t *testing.T) {
				ts, requests := newCountingServer(t)
				seedCred(t, ts.URL)
				agentsPushRepo(t, r.toml, secretAgentsToml)

				out, err := r.run(t, ts.URL, dry)
				if err == nil {
					t.Fatalf("a machine-specific agents.toml was not refused:\n%s", out)
				}
				for _, want := range []string{
					"agents.toml [reviewer] command", "agents.toml [reviewer] env.ANTHROPIC_BASE_URL", "agents.toml [reviewer] settings.env.CLAUDE_TOKEN",
					".satelle/workflows/agents.local.toml",
				} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("refusal lacks %q:\n%v", want, err)
					}
				}
				if n := requests(); n != 0 {
					t.Fatalf("a refused push contacted the server %d time(s)", n)
				}
			})
		}
	}
}

// The agents layer is checked by PATH, not by the label the caller typed.
func TestPublishPushChecksAgentsPathWhateverKindWasTyped(t *testing.T) {
	ts, requests := newCountingServer(t)
	seedCred(t, ts.URL)
	agentsPushRepo(t, teamBoundToml, secretAgentsToml)
	cmd, buf := testCmd()
	err := runPublishPush(cmd, ts.URL, "", "workflow", "", false, []string{"workflows/agents.toml"})
	if err == nil || !strings.Contains(err.Error(), "agents.local.toml") {
		t.Fatalf("--kind workflow bypassed the agents check: err=%v\n%s", err, buf.String())
	}
	if n := requests(); n != 0 {
		t.Fatalf("a refused push contacted the server %d time(s)", n)
	}
}

// pushableAgents is a shared agents layer that carries nothing machine-specific.
const pushableAgents = `# authored by hand — a push must send these bytes, comment and all
[executor]
role    = "agent"
command = "in-loop"

[reviewer]
role    = "reviewer"
command = "` + agentcli.DefaultClaudeCommand + `"
tools   = "Read,Grep,Glob"
model   = "opus"
env     = { ANTHROPIC_AUTH_TOKEN = "${GLM_API_KEY}", BLANK = "" }
settings = { model = "opus", env = { CLAUDE_TOKEN = "${CLAUDE_TOKEN}" } }
`

// AC2/AC3: sync config push sends the agents layer and satelle.toml as they are
// on disk, less the project binding it names; with no binding there is nothing
// to name.
func TestSyncConfigPushSendsFileBytesAndNamesWithheldKeys(t *testing.T) {
	ts := newFakeConfigServer(t)
	seedCred(t, ts.URL)
	const toml = "[sync]\n[hosted]\nproject = \"probe\"\n# stays\n"
	agentsPushRepo(t, toml, pushableAgents)

	cmd, buf := testCmd()
	if err := runSyncConfigPush(cmd, ts.URL, "", false); err != nil {
		t.Fatalf("push: %v\n%s", err, buf.String())
	}
	client := hosted.NewClient(ts.URL, hosted.FileStore{}, nil)
	agents, _, err := client.ConfigFileContent(context.Background(), "probe", config.AgentsRel, 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(agents) != pushableAgents {
		t.Errorf("hosted agents.toml is not the file's bytes:\n%s", agents)
	}
	settings, _, err := client.ConfigFileContent(context.Background(), "probe", config.ConfigName, 0)
	if err != nil {
		t.Fatal(err)
	}
	if want := "[sync]\n[hosted]\n# stays\n"; string(settings) != want {
		t.Errorf("hosted satelle.toml = %q, want the file less the project binding %q", settings, want)
	}
	if !strings.Contains(buf.String(), "settings: withheld [hosted] project from satelle.toml") {
		t.Errorf("the withheld key is not named:\n%s", buf.String())
	}

	// Both keys are named; a dry run names them too.
	both := "[sync]\nproject = \"from-sync\"\n[hosted]\nproject = \"probe\"\n"
	agentsPushRepo(t, both, pushableAgents)
	cmdD, bufD := testCmd()
	if err := runSyncConfigPush(cmdD, ts.URL, "", true); err != nil {
		t.Fatalf("dry-run: %v\n%s", err, bufD.String())
	}
	for _, want := range []string{"settings: withheld [sync] project from satelle.toml", "settings: withheld [hosted] project from satelle.toml"} {
		if !strings.Contains(bufD.String(), want) {
			t.Errorf("dry-run does not name %q:\n%s", want, bufD.String())
		}
	}

	// No binding in satelle.toml: nothing withheld, nothing said.
	agentsPushRepo(t, "[sync]\n", pushableAgents)
	cmdN, bufN := testCmd()
	if err := runSyncConfigPush(cmdN, ts.URL, "", true); err != nil {
		t.Fatalf("dry-run: %v\n%s", err, bufN.String())
	}
	if strings.Contains(bufN.String(), "settings: withheld") {
		t.Errorf("no project key is present, yet the push says it withheld one:\n%s", bufN.String())
	}
}

// AC2: publish push and sync bindings push send the agents layer's own bytes.
func TestPublishAndBindingsPushSendFileBytes(t *testing.T) {
	t.Run("publish push", func(t *testing.T) {
		ts := newFakePublishServer(t)
		seedCred(t, ts.URL)
		agentsPushRepo(t, teamBoundToml, pushableAgents)
		cmd, buf := testCmd()
		if err := runPublishPush(cmd, ts.URL, "", "", "", false, []string{"workflows/agents.toml"}); err != nil {
			t.Fatalf("publish push: %v\n%s", err, buf.String())
		}
		content, _, err := publishTestClient(ts.URL).PublishedContent(context.Background(), "ws-team", "workflows/agents.toml", 0)
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != pushableAgents {
			t.Errorf("published body is not the file's bytes:\n%s", content)
		}
	})
	t.Run("sync bindings push", func(t *testing.T) {
		ts, f := newFakeBindingsServer(t)
		seedCred(t, ts.URL)
		agentsPushRepo(t, teamBoundToml, pushableAgents)
		cmd, buf := testCmd()
		if err := runSyncBindingsPush(cmd, ts.URL, "", false); err != nil {
			t.Fatalf("bindings push: %v\n%s", err, buf.String())
		}
		if string(f.putBody) != pushableAgents {
			t.Errorf("published body is not the file's bytes:\n%s", f.putBody)
		}
	})
}

// pushUnchecked pushes this machine's config the way a push before these checks
// did: every file as it is on disk, no refusal. It stands in for the hosted copy
// an older push left, which carries the same bytes the repo has.
func (m *snapMachine) pushUnchecked() {
	m.t.Helper()
	m.use()
	cfg, repoRoot, dataDir, err := loadRepoConfig()
	if err != nil {
		m.t.Fatal(err)
	}
	bundle, err := config.ConfigFiles(cfg, config.PersonalScope, repoRoot, config.Ignore{})
	if err != nil {
		m.t.Fatal(err)
	}
	project, err := resolveBoundProject(cfg, repoRoot)
	if err != nil {
		m.t.Fatal(err)
	}
	cmd, _ := testCmd()
	client := newHostedClient(cmd.Context(), m.url, repoRoot)
	manifest, err := client.ConfigManifest(cmd.Context(), project)
	if err != nil {
		m.t.Fatal(err)
	}
	if _, handled, err := pushConfigSnapshots(cmd, cfg, repoRoot, dataDir, config.Ignore{}, client, m.url, project, manifest, bundle.Files); err != nil || !handled {
		m.t.Fatalf("unchecked push: handled=%v err=%v", handled, err)
	}
}

// AC4: status and reconcile read the local agents.toml as it is, so a file
// carrying machine-specific content is in step with a hosted copy of the same
// bytes, neither command fails on it, and reconcile no longer claims its local
// side is redacted.
func TestSyncStatusAndReconcileReadMachineSpecificAgentsAsIs(t *testing.T) {
	ts, _ := newSnapServer(t)
	seedCred(t, ts.URL)
	m := &snapMachine{t: t, url: ts.URL, repo: syncConfigRepo(t, "[sync]\n"+boundProjectToml), state: filepath.Join(t.TempDir(), "document-sync-state.json")}
	_ = os.Remove(filepath.Join(m.repo, ".satelle", "agents.toml"))
	m.write("workflows/agents.toml", secretAgentsToml)

	if out, err := m.tryPushConfig(); err == nil {
		t.Fatalf("the premise: a push must refuse this file:\n%s", out)
	}
	m.pushUnchecked()

	out, err := m.status()
	if err != nil {
		t.Fatalf("status failed on a repo carrying machine-specific content: %v\n%s", err, out)
	}
	wantState(t, out, "agents", "in step")
	rec, err := m.reconcile()
	if err != nil {
		t.Fatalf("reconcile failed on a repo carrying machine-specific content: %v\n%s", err, rec)
	}
	for _, text := range []string{out, rec} {
		if strings.Contains(text, "secrets redacted") || strings.Contains(strings.ToLower(text), "redacted") {
			t.Errorf("output still claims redaction:\n%s", text)
		}
	}
}

// routedPushRepo is a repo whose route allocates its planned step to agent
// "ghost", with the shared agents layer pushing only the agents area.
func routedPushRepo(t *testing.T, satelleToml, shared string) string {
	t.Helper()
	repo := agentsPushRepo(t, satelleToml, shared)
	writeRoute(t, filepath.Join(repo, ".satelle", "workflows"),
		"[\"*\"]\nobligations = [\"raised\", \"planned\", \"coded\", \"closed\"]\n",
		"[raised]\nstatus = \"backlog\"\nstart = true\n\n"+
			"[planned]\nstatus = \"plan\"\nagent = \"ghost\"\nrequires = [\"raised\"]\n\n"+
			"[coded]\nstatus = \"in_progress\"\nagent = \"executor\"\nrequires = [\"raised\"]\n\n"+
			"[closed]\nstatus = \"done\"\nterminal = true\nrequires = [\"coded\"]\n")
	return repo
}

const (
	executorOnlyAgents = "[executor]\nrole = \"agent\"\ncommand = \"in-loop\"\n"
	ghostBinding       = "\n[ghost]\nrole = \"agent\"\ncommand = \"claude -p --allowedTools {tools} --append-system-prompt {system}\"\ntools = \"Read,Edit,Bash(satelle:*)\"\n"
)

// AC6: the validation judges the configuration as another machine would load it,
// so an allocation that resolves only through this machine's agents.local.toml, or
// only through the synced agents.workspace.toml, passes init here and is refused
// by the push with the node.alloc finding.
func TestPushValidationExcludesMachineAgentLayers(t *testing.T) {
	useRealPushValidator(t)
	for _, layer := range []string{config.LocalAgentsName, config.WorkspaceAgentsName} {
		t.Run(layer, func(t *testing.T) {
			ts, requests := newCountingServer(t)
			seedCred(t, ts.URL)
			repo := routedPushRepo(t, configPushToml, executorOnlyAgents)
			writeRepoFile(t, repo, ".satelle/workflows/"+layer, ghostBinding)
			dataDir := filepath.Join(repo, ".satelle")

			var initOut strings.Builder
			if failed := validateSystem(&initOut, dataDir, false); failed != 0 {
				t.Fatalf("the premise: init must pass with the seat declared in %s (%d failed):\n%s", layer, failed, initOut.String())
			}
			cmd, buf := testCmd()
			err := runSyncConfigPush(cmd, ts.URL, "", false)
			if err == nil {
				t.Fatalf("a push that resolves only through %s was not refused:\n%s", layer, buf.String())
			}
			if !strings.Contains(buf.String(), "node.alloc") {
				t.Errorf("the refusal must print the node.alloc finding:\n%s", buf.String())
			}
			if n := requests(); n != 0 {
				t.Fatalf("a refused push contacted the server %d time(s)", n)
			}
		})
	}
}

// coderPushRepo is a repo whose route allocates its coded step to the performer
// seat "coder", with shared as the shared agents layer.
func coderPushRepo(t *testing.T, shared string) (repo, dataDir string) {
	t.Helper()
	repo = agentsPushRepo(t, configPushToml, shared)
	writeRoute(t, filepath.Join(repo, ".satelle", "workflows"),
		"[\"*\"]\nobligations = [\"raised\", \"coded\", \"closed\"]\n",
		"[raised]\nstatus = \"backlog\"\nstart = true\n\n"+
			"[coded]\nstatus = \"in_progress\"\nagent = \"coder\"\nrequires = [\"raised\"]\n\n"+
			"[closed]\nstatus = \"done\"\nterminal = true\nrequires = [\"coded\"]\n")
	return repo, filepath.Join(repo, ".satelle")
}

const (
	piPerformerCommand = "pi -p {system} {payload}"
	// roleOnlyCoder is a shared [coder] that writes no command and no tools: its
	// grant comes from each machine's agents.local.toml or profile.
	roleOnlyCoder = executorOnlyAgents + "\n[coder]\nrole = \"agent\"\n"
)

// nodeAllocFindings returns the node.alloc findings of one severity.
func nodeAllocFindings(rep doctor.Report, sev health.Severity) []health.Finding {
	var out []health.Finding
	for _, f := range rep.Findings.WithSeverity(sev) {
		if f.ID == health.IDNodeAlloc {
			out = append(out, f)
		}
	}
	return out
}

// AC1/AC2 (sty_50dae450): a shared performer seat that writes no tools and no
// command is completed per machine, so a push is not refused for its missing
// context channel and prints no adapter's read tool; judged as another machine
// loads it, doctor reports it as a warning naming agents.local.toml.
func TestPushDefersRoleOnlyPerformerGrantToTheMachine(t *testing.T) {
	useRealPushValidator(t)
	ts, requests := newCountingServer(t)
	seedCred(t, ts.URL)
	repo, dataDir := coderPushRepo(t, roleOnlyCoder)

	cmd, buf := testCmd()
	if err := runSyncConfigPush(cmd, ts.URL, "", true); err != nil {
		t.Fatalf("a role-only performer seat was refused: %v\n%s", err, buf.String())
	}
	if n := requests(); n != 0 {
		t.Fatalf("a dry run contacted the server %d time(s)", n)
	}

	var out strings.Builder
	if failed := validateSystem(&out, dataDir, true); failed != 0 {
		t.Fatalf("push validation failed %d time(s):\n%s", failed, out.String())
	}
	for _, banned := range []string{"FAIL  [node.alloc]", "`Read`", "`read`"} {
		if strings.Contains(out.String(), banned) {
			t.Errorf("push output carries %q:\n%s", banned, out.String())
		}
	}

	rep := doctor.Check(context.Background(), doctor.Opts{RepoRoot: repo, DataDir: dataDir, ExcludeMachineAgentLayers: true})
	if errs := nodeAllocFindings(rep, health.SeverityError); len(errs) != 0 {
		t.Fatalf("want no error-severity node.alloc, got %v", errs)
	}
	warns := nodeAllocFindings(rep, health.SeverityWarn)
	if len(warns) != 1 {
		t.Fatalf("want one warn-severity node.alloc finding, got %v", rep.Findings)
	}
	d := warns[0].Detail
	if !strings.Contains(d, "agent=coder") || !strings.Contains(d, "agents.local.toml") || !strings.Contains(d, "profile") {
		t.Errorf("the warning must name the seat and say the grant comes from agents.local.toml or a profile: %q", d)
	}
	if strings.Contains(d, "`Read`") || strings.Contains(d, "`read`") {
		t.Errorf("the warning must name no single adapter's read tool: %q", d)
	}
}

// AC3 (sty_50dae450): the same shared file passes the push, and on a machine
// whose agents.local.toml names a profile, the coder's grant is the profile's.
func TestRoleOnlyCoderPushesAndRunsTheProfileGrant(t *testing.T) {
	useRealPushValidator(t)
	repo, dataDir := coderPushRepo(t, roleOnlyCoder)
	const profileTools = "read,write,edit,bash"
	writeRepoFile(t, repo, ".satelle/workflows/"+config.LocalAgentsName, "[coder]\nprofile = \"p\"\n")
	if err := os.MkdirAll(config.GlobalDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	catalog := "[profiles.p]\nrole = \"agent\"\ncommand = \"" + piPerformerCommand + "\"\ntools = \"" + profileTools + "\"\n"
	if err := os.WriteFile(config.GlobalAgentsPath(), []byte(catalog), 0o644); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if failed := validateSystem(&out, dataDir, true); failed != 0 {
		t.Fatalf("the push refused a coder with no shared tools key (%d failed):\n%s", failed, out.String())
	}

	layers, err := config.LoadAgentLayers(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	global, err := config.LoadGlobalAgents()
	if err != nil {
		t.Fatal(err)
	}
	r := agentvalidate.ValidateEffectiveLayered(layers, global, nil, doctor.GoverningWorkflows(dataDir), doctor.SkillBodyResolver(dataDir))
	var coder *agentvalidate.Grant
	for i := range r.Grants {
		if r.Grants[i].Name == "coder" {
			coder = &r.Grants[i]
		}
	}
	if coder == nil {
		t.Fatalf("no coder grant: %+v", r.Grants)
	}
	if coder.Tools != profileTools {
		t.Errorf("coder tools = %q, want the profile's %q", coder.Tools, profileTools)
	}
	if got := coder.Sources["tools"]; got != config.SourceProfile("p") {
		t.Errorf("coder tools sourced %q, want %q", got, config.SourceProfile("p"))
	}
	for _, p := range r.Problems {
		if strings.Contains(p, "no context channel") {
			t.Errorf("the profile grant carries a channel, yet: %s", p)
		}
	}

	agentOut, _ := runRoot(t, "agent", "validate")
	if !strings.Contains(agentOut, "GRANT [coder]") || !strings.Contains(agentOut, `tools="`+profileTools+`"`) {
		t.Errorf("satelle agent validate must report the profile's coder grant:\n%s", agentOut)
	}
}

// AC5 (sty_50dae450): a shared [coder] that writes its own command or tools is
// judged strictly, so a push is still refused when that grant has no channel.
func TestPushRefusesSharedPerformerGrantWithoutChannel(t *testing.T) {
	useRealPushValidator(t)
	for name, coder := range map[string]string{
		"shared command, no tools": "[coder]\nrole = \"agent\"\ncommand = \"" + piPerformerCommand + "\"\n",
		"shared tools, no channel": "[coder]\nrole = \"agent\"\ntools = \"Write\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			ts, requests := newCountingServer(t)
			seedCred(t, ts.URL)
			coderPushRepo(t, executorOnlyAgents+"\n"+coder)
			cmd, buf := testCmd()
			err := runSyncConfigPush(cmd, ts.URL, "", true)
			if err == nil {
				t.Fatalf("a shared grant with no context channel was pushed:\n%s", buf.String())
			}
			if !strings.Contains(buf.String(), "FAIL  [node.alloc]") || !strings.Contains(buf.String(), "context channel") {
				t.Errorf("the refusal must print the node.alloc context-channel finding:\nerr: %v\nout: %s", err, buf.String())
			}
			if n := requests(); n != 0 {
				t.Fatalf("a refused push contacted the server %d time(s)", n)
			}
		})
	}
}

// AC10: no help text says a push or pull redacts agent configuration.
func TestSyncHelpTextDoesNotDescribeRedaction(t *testing.T) {
	root := NewRootCmd()
	for _, path := range [][]string{{"sync", "bindings", "push"}, {"sync", "bindings", "pull"}, {"sync", "bindings"}, {"sync", "config", "push"}, {"publish", "push"}} {
		c, _, err := root.Find(path)
		if err != nil || c == nil || c.Name() != path[len(path)-1] {
			t.Fatalf("command %v not found: %v", path, err)
		}
		text := c.Short + "\n" + c.Long
		c.Flags().VisitAll(func(f *pflag.Flag) { text += "\n" + f.Usage })
		if strings.Contains(strings.ToLower(text), "redact") {
			t.Errorf("satelle %s help still describes redaction:\n%s", strings.Join(path, " "), text)
		}
	}
}
