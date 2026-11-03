package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/snapsync"
	"github.com/bobmcallan/satelle/internal/syncstate"
	"github.com/bobmcallan/satelle/internal/testutil"
)

// Substrate continuity advisories (sty_7562055c): the line engagement and
// SessionStart print when substrate is not in step.

const skillsOnlyToml = "[review]\ngate_create = false\n\n[sync]\n" + boundProjectToml

// skillsMachine is a checkout that syncs only skills, so a deploy never reaches
// the route files an engaging machine needs.
func skillsMachine(t *testing.T, url string) *snapMachine {
	t.Helper()
	return &snapMachine{t: t, url: url, repo: syncConfigRepo(t, skillsOnlyToml), state: filepath.Join(t.TempDir(), "document-sync-state.json")}
}

// engagingMachine adds a route whose in_progress step is performed by the
// in-loop executor, so `story set --status in_progress` engages without
// dispatching anything. The machine's team set is skills only, so the project
// tier its deploy writes leaves the repo route and agents governing.
func engagingMachine(t *testing.T, url string) *snapMachine {
	t.Helper()
	m := skillsMachine(t, url)
	m.write("satelle.local.toml", "[team]\npaths = [\"skills/**\"]\n")
	writeRoute(t, filepath.Join(m.repo, ".satelle", "workflows"),
		`["*"]
obligations = ["raised", "coded", "closed"]
`,
		`[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
agent = "executor"
requires = ["raised"]

[closed]
status = "done"
terminal = true
requires = ["coded"]
`)
	m.use()
	t.Chdir(m.repo)
	return m
}

// createStory makes a story on the machine and returns its id.
func (m *snapMachine) createStory() string {
	m.t.Helper()
	m.use()
	m.t.Chdir(m.repo)
	out, err := runRoot(m.t, "story", "create", "--title", "t", "--body", "b", "--acceptance", "1. ok", "--category", "chore")
	if err != nil {
		m.t.Fatalf("create: %v\n%s", err, out)
	}
	var created map[string]any
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		m.t.Fatalf("parse create: %v\n%s", err, out)
	}
	return created["id"].(string)
}

// engage runs `story set --status in_progress` and returns the combined output.
func (m *snapMachine) engage(id string) string {
	m.t.Helper()
	m.use()
	m.t.Chdir(m.repo)
	out, err := runRoot(m.t, "story", "set", id, "--status", "in_progress")
	if err != nil {
		m.t.Fatalf("engagement must never be refused for substrate drift: %v\n%s", err, out)
	}
	return out
}

func advisoryLines(out string) []string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "substrate not in step") {
			lines = append(lines, l)
		}
	}
	return lines
}

func TestEngagementAdvisoryUnboundMachineIsUnverified(t *testing.T) {
	testutil.IsolateHome(t)
	ts, _ := newSnapServer(t) // no credential seeded: unbound
	if err := config.SaveGlobalHostedServer(ts.URL); err != nil {
		t.Fatal(err)
	}
	b := engagingMachine(t, ts.URL)
	// An earlier sync resolved personal; the machine has since signed out.
	b.resolvedScope(config.PersonalScope)
	b.write("skills/x.md", "local only\n")
	id := b.createStory()
	lines := advisoryLines(b.engage(id))
	if len(lines) != 1 || !strings.Contains(lines[0], "unverified (offline/unbound)") {
		t.Fatalf("an unbound machine with local-only skills must say so, unverified: %v", lines)
	}
}

func TestEngagementAdvisoryAllLocalIsSilent(t *testing.T) {
	testutil.IsolateHome(t)
	ts, _ := newSnapServer(t)
	m := engagingMachine(t, ts.URL)
	if err := os.WriteFile(filepath.Join(m.repo, ".satelle", "satelle.toml"), []byte("[review]\ngate_create = false\n"+boundProjectToml), 0o644); err != nil {
		t.Fatal(err)
	}
	m.write("skills/x.md", "never synced, but skills is local\n")
	if lines := advisoryLines(m.engage(m.createStory())); len(lines) != 0 {
		t.Fatalf("an all-local repo must stay silent: %v", lines)
	}
}

// substrateHookRepo is a governed repo opted in to sync skills, signed in, with
// a skill and the base it was last synced from.
func substrateHookRepo(t *testing.T, skill, synced string) string {
	t.Helper()
	repo := sessionContextRepo(t)
	hosted.DocumentSyncStatePathOverride = filepath.Join(t.TempDir(), "doc-state.json")
	t.Cleanup(func() { hosted.DocumentSyncStatePathOverride = "" })
	writeHarnessTOML(t, repo, "[sync]\nproject = \"p\"\nstale_after = \"24h\"\n")
	seedCred(t, config.DefaultHostedServer)
	data := filepath.Join(repo, config.DefaultDataDir)
	if err := os.MkdirAll(filepath.Join(data, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "skills", "x.md"), []byte(skill), 0o644); err != nil {
		t.Fatal(err)
	}
	base := hosted.AreaBase{Version: 1, Files: map[string]string{"skills/x.md": snapsync.SHA([]byte(synced))}}
	if err := hosted.SaveAreaBase(config.DefaultHostedServer, "p", repo, "skills", base); err != nil {
		t.Fatal(err)
	}
	if err := syncstate.RecordResolvedScope(config.GlobalDir(), repo, config.PersonalScope, "p", time.Now()); err != nil {
		t.Fatal(err)
	}
	othersInStepOffline(t, repo, "p", "skills")
	if err := syncstate.RecordSubstrate(config.GlobalDir(), repo, map[string]syncstate.AreaCheck{"skills": {State: snapsync.StateInStep}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	return repo
}

func TestHookContextSubstrateAdvisoryDriftedAndInStep(t *testing.T) {
	channel := config.Config{}.SessionContextEvent("grok")

	substrateHookRepo(t, "edited\n", "x\n") // local differs from the last synced base
	_, content, _, emitted := runContext(t, "grok", grokDualPayload(channel, "post_tool_use", "sess-drift"))
	if !emitted {
		t.Fatal("SessionStart context was not emitted")
	}
	lines := advisoryLines(content)
	// The record is fresh, so the line is not marked unverified, and the one
	// drifted area names the command that brings it in step.
	if len(lines) != 1 || !strings.Contains(lines[0], "skills unpushed") || !strings.Contains(lines[0], "run `satelle sync push`") || strings.Contains(lines[0], "unverified") {
		t.Fatalf("drifted SessionStart advisory = %v\n%s", lines, content)
	}
	if !strings.Contains(content, "CONSTITUTION-MARKER-sty_507d3d9c") {
		t.Errorf("the advisory must ride beside the session context, not replace it:\n%s", content)
	}

	substrateHookRepo(t, "x\n", "x\n") // in step
	_, content, _, emitted = runContext(t, "grok", grokDualPayload(channel, "post_tool_use", "sess-instep"))
	if !emitted || len(advisoryLines(content)) != 0 {
		t.Fatalf("an in-step SessionStart must print no advisory: emitted=%v\n%s", emitted, content)
	}
}

// The session advisory judges a drifted skill from the scope the last resolve
// recorded for the explicit [sync] project binding: an unbound repo (a scope
// recorded under its directory name does not count), a bound repo recorded
// local, and a record for another binding stay silent; a recorded team scope for
// the current binding names the drift.
func TestHookContextSubstrateAdvisoryJudgesFromRecordedScope(t *testing.T) {
	channel := config.Config{}.SessionContextEvent("grok")
	cases := []struct {
		name      string
		unbind    bool
		scope     config.Scope
		binding   string // "" is the repo directory name
		wantDrift bool
	}{
		{"unbound, scope recorded under the directory name", true, config.TeamScope, "", false},
		{"bound, recorded local", false, config.LocalScope, "p", false},
		{"bound, recorded for another binding", false, config.TeamScope, "other", false},
		{"bound, recorded team", false, config.TeamScope, "p", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := substrateHookRepo(t, "edited\n", "x\n")
			if c.unbind {
				writeHarnessTOML(t, repo, "[sync]\nstale_after = \"24h\"\n")
			}
			binding := c.binding
			if binding == "" {
				binding = filepath.Base(repo)
			}
			if err := syncstate.RecordResolvedScope(config.GlobalDir(), repo, c.scope, binding, time.Now()); err != nil {
				t.Fatal(err)
			}
			_, content, _, emitted := runContext(t, "grok", grokDualPayload(channel, "post_tool_use", "sess-scope"))
			if !emitted {
				t.Fatal("SessionStart context was not emitted")
			}
			lines := advisoryLines(content)
			if !c.wantDrift {
				if len(lines) != 0 {
					t.Fatalf("a local scope must print no advisory: %v", lines)
				}
				return
			}
			if len(lines) != 1 || !strings.Contains(lines[0], "skills unpushed") {
				t.Fatalf("a recorded team scope must name the drifted skill: %v\n%s", lines, content)
			}
		})
	}
}

// othersInStepOffline records every substrate area but except as synced from
// the repo's current files — its base and its last check — so an offline judge
// of a personal scope, which reads every area, sees only the areas a test names.
func othersInStepOffline(t *testing.T, repo, project string, except ...string) {
	t.Helper()
	data := filepath.Join(repo, config.DefaultDataDir)
	cfg, _, err := config.Load(filepath.Join(data, config.ConfigName))
	if err != nil {
		t.Fatal(err)
	}
	ign, err := config.LoadIgnore(data)
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]syncstate.AreaCheck{}
	for _, area := range config.SubstrateAreas(config.PersonalScope) {
		if slices.Contains(except, area) {
			continue
		}
		local, err := snapsync.LocalShas(cfg, repo, area, ign)
		if err != nil {
			t.Fatal(err)
		}
		if err := hosted.SaveAreaBase(config.DefaultHostedServer, project, repo, area, hosted.AreaBase{Version: 1, Files: local}); err != nil {
			t.Fatal(err)
		}
		states[area] = syncstate.AreaCheck{State: snapsync.StateInStep}
	}
	if err := syncstate.RecordSubstrate(config.GlobalDir(), repo, states, time.Now()); err != nil {
		t.Fatal(err)
	}
}
