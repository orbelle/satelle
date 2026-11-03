package doctor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/health"
	"github.com/bobmcallan/satelle/internal/syncstate"
	"github.com/bobmcallan/satelle/internal/testutil"
)

// healthyAgentsTOML is the baseline every case deltas from: an in-loop executor
// and an isolated read-only reviewer whose binary (sh) is always on PATH, so a
// healthy fixture is genuinely healthy rather than passing by omission.
const healthyAgentsTOML = `[executor]
role    = "agent"
command = "in-loop"

[reviewer]
role    = "reviewer"
command = "sh -c --disallowedTools Write,Edit {system} {tools} {model}"
tools   = "Read,Grep,Glob"
`

// A lifecycle is a DERIVED ROUTE — done.toml + step.toml (sty_d953c5d8). The
// doctor matrix installs both halves; workflowAdd still patches the done half's
// frontmatter, which is where a lifecycle hook is declared. Frontmatter is the
// `[meta]` table now, so an addition is TOML (sty_81bb0dde).
const healthyWorkflow = `[meta]
name = "done"
scope = "project"
type = "workflow"
description = "Fixture declaration of done governing every category for the doctor test matrix."

["*"]
obligations = ["raised", "coded", "closed"]
`

const healthySteps = `[meta]
name = "step"
scope = "project"
type = "workflow"
description = "Fixture step catalogue for the doctor test matrix."

[raised]
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
`

// fixtureOpts are the deltas a case applies to the healthy baseline.
type fixtureOpts struct {
	agents      string // replaces agents.toml when non-empty
	extraFiles  map[string]string
	catalog     string // machine-wide profile catalog when non-empty
	omitAgents  bool
	workflowAdd string // extra [meta] lines for the fixture workflow (TOML)
}

// newFixtureRepo builds a repo under an isolated SATELLE_HOME and returns its
// root. Every case is a delta on one healthy baseline, so a failure points at
// the delta rather than at fixture divergence.
func newFixtureRepo(t *testing.T, o fixtureOpts) string {
	t.Helper()
	testutil.IsolateHome(t)
	root := t.TempDir()
	dataDir := filepath.Join(root, ".satelle")
	for _, d := range []string{"workflows", "skills", "principles", "tasks"} {
		if err := os.MkdirAll(filepath.Join(dataDir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dataDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("satelle.toml", "")
	if !o.omitAgents {
		body := healthyAgentsTOML
		if o.agents != "" {
			body = o.agents
		}
		write("agents.toml", body)
	}
	wf := healthyWorkflow
	if o.workflowAdd != "" {
		// Appended AFTER the [meta] scalars and BEFORE the first category table:
		// a TOML key placed after a `["*"]` header would belong to that table, not
		// to meta.
		wf = strings.Replace(wf, "\n[\"*\"]\n", "\n"+o.workflowAdd+"\n[\"*\"]\n", 1)
	}
	write("workflows/done.toml", wf)
	write("workflows/step.toml", healthySteps)
	for rel, body := range o.extraFiles {
		write(rel, body)
	}
	if o.catalog != "" {
		if err := os.MkdirAll(globalDirForTest(t), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(globalDirForTest(t), "agents.toml"), []byte(o.catalog), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func globalDirForTest(t *testing.T) string {
	t.Helper()
	return os.Getenv("SATELLE_HOME")
}

// check runs the deterministic pass over a fixture.
func check(t *testing.T, root string) Report {
	t.Helper()
	return Check(context.Background(), Opts{RepoRoot: root, DataDir: filepath.Join(root, ".satelle")})
}

// ids returns the set of finding ids in a report.
func ids(r Report) map[string]bool {
	out := map[string]bool{}
	for _, f := range r.Findings {
		out[f.ID] = true
	}
	return out
}

// TestHealthyRepo is the matrix baseline: nothing is wrong, so nothing is
// reported as an error and the exit code is zero.
func TestHealthyRepo(t *testing.T) {
	r := check(t, newFixtureRepo(t, fixtureOpts{}))
	if !r.OK {
		t.Fatalf("healthy fixture must have no error findings: %v", r.Findings.Details(health.SeverityError))
	}
	if ExitCode([]Report{r}) != ExitHealthy {
		t.Error("a healthy repo must exit 0")
	}
	// The composition ran: grants and provenance are present.
	if len(r.Grants) < 2 || len(r.Sources) < 2 {
		t.Errorf("composition did not produce grants/provenance: %+v", r)
	}
}

// TestCheckFailsUnbackedWorkstate is the doctor-surface proof of sty_30696eeb
// AC5: Check (not just checkSyncHealth) refuses PASS when a recorded push is
// older than [sync] stale_after.
func TestCheckFailsUnbackedWorkstate(t *testing.T) {
	root := newFixtureRepo(t, fixtureOpts{
		extraFiles: map[string]string{
			"satelle.toml": "[sync]\nproject = \"p\"\nstale_after = \"1h\"\n",
		},
	})
	if err := syncstate.RecordResolvedScope(os.Getenv("SATELLE_HOME"), root, config.PersonalScope, "p", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := syncstate.RecordPush(os.Getenv("SATELLE_HOME"), root, true, "", "", time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	r := check(t, root)
	if r.OK {
		t.Fatalf("unbacked repo must not PASS: %v", r.Findings)
	}
	if !ids(r)[health.IDSyncUnbacked] {
		t.Fatalf("missing %s: %v", health.IDSyncUnbacked, r.Findings)
	}
}

func TestCheckFailsStandingPushFailure(t *testing.T) {
	root := newFixtureRepo(t, fixtureOpts{
		extraFiles: map[string]string{
			"satelle.toml": "[sync]\nproject = \"p\"\nstale_after = \"24h\"\n",
		},
	})
	if err := syncstate.RecordResolvedScope(os.Getenv("SATELLE_HOME"), root, config.PersonalScope, "p", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := syncstate.RecordPush(os.Getenv("SATELLE_HOME"), root, false, "hosted down", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	r := check(t, root)
	if r.OK {
		t.Fatalf("standing failure must not PASS: %v", r.Findings)
	}
	if !ids(r)[health.IDSyncFailing] {
		t.Fatalf("missing %s: %v", health.IDSyncFailing, r.Findings)
	}
	var detail string
	for _, f := range r.Findings {
		if f.ID == health.IDSyncFailing {
			detail = f.Detail
			break
		}
	}
	if detail != "hosted down" {
		t.Fatalf("sync.failing detail = %q, want exact recorded reason", detail)
	}
}

// TestDefectMatrix is AC7: one case per defect class, each asserting the stable
// finding id — so a check that silently stops firing fails here by name.
func TestDefectMatrix(t *testing.T) {
	cases := []struct {
		name string
		opts fixtureOpts
		want string
		// advisory marks a class that is reported but must NOT make a repo
		// unhealthy — a condition the environment can fix later.
		advisory bool
	}{
		{
			name: "missing profile reference",
			opts: fixtureOpts{
				agents:  healthyAgentsTOML + "\n[judge]\nprofile = \"absent-profile\"\n",
				catalog: "[profiles.present]\nrole = \"reviewer\"\ncommand = \"sh -c {system}\"\n",
			},
			want: health.IDAgentsProfileBroken,
		},
		{
			// A repo may legitimately be initialised before its agent CLI exists,
			// so this is reported and never blocking.
			name: "missing executable",
			opts: fixtureOpts{agents: healthyAgentsTOML +
				"\n[judge]\nrole = \"reviewer\"\ncommand = \"definitely-not-installed-cli --disallowedTools Write {system}\"\ntools = \"Read\"\n"},
			want:     health.IDBinaryMissing,
			advisory: true,
		},
		{
			name: "malformed command",
			opts: fixtureOpts{agents: healthyAgentsTOML +
				"\n[judge]\nrole = \"reviewer\"\ncommand = \"{system} --disallowedTools Write\"\ntools = \"Read\"\n"},
			want: health.IDBinaryMalformed,
		},
		{
			name: "unsafe reviewer",
			opts: fixtureOpts{agents: healthyAgentsTOML +
				"\n[judge]\nrole = \"reviewer\"\ninterface = \"acp\"\ncommand = \"grok agent stdio\"\n"},
			want: health.IDReviewerUnsafe,
		},
		{
			name: "broken create hook",
			opts: fixtureOpts{workflowAdd: "[[meta.hooks]]\noperation = \"create_review\"\nskill = \"fixture-review\"\nagent = \"nobody\"\n"},
			want: health.IDHookAlloc,
		},
		{
			name: "unresolved hook skill",
			opts: fixtureOpts{workflowAdd: "create_review = \"no-such-skill\"\n"},
			want: health.IDWorkflowConsistency,
		},
		{
			name: "broken workflow structure",
			opts: fixtureOpts{extraFiles: map[string]string{
				"workflows/broken.md": "---\nname: broken\n---\n# broken\n",
			}},
			want: health.IDWorkflowStructure,
		},
		{
			name: "broken done.toml route source",
			opts: fixtureOpts{extraFiles: map[string]string{
				"workflows/done.toml": `[meta]
name = "done"
scope = "project"
type = "workflow"
description = "Broken park.agent key."

["*"]
obligations = ["raised", "coded", "closed"]
park = { state = "blocked", agent = "reviewer", gate = "satelle-story-blocked-review" }
`,
			}},
			want: health.IDWorkflowStructure,
		},
		{
			name: "unresolved env var",
			opts: fixtureOpts{agents: healthyAgentsTOML +
				"\n[judge]\nrole = \"reviewer\"\ncommand = \"sh -c --disallowedTools Write {system}\"\ntools = \"Read\"\nenv = { TOKEN = \"${NOWHERE_DEFINED}\" }\n"},
			want: health.IDEnvUnresolved,
		},
		{
			name: "workflow node allocates a missing binding",
			// The allocation lives on a STEP of the shipped route: a step whose
			// agent names no binding (sty_d953c5d8).
			opts: fixtureOpts{extraFiles: map[string]string{
				"workflows/done.toml": strings.Replace(healthyWorkflow,
					`obligations = ["raised", "coded", "closed"]`,
					`obligations = ["raised", "planned", "coded", "closed"]`, 1),
				"workflows/step.toml": healthySteps +
					`[planned]
status = "plan"
agent = "ghost"
requires = ["raised"]
`,
			}},
			want: health.IDNodeAlloc,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := check(t, newFixtureRepo(t, c.opts))
			if !ids(r)[c.want] {
				t.Fatalf("want a %s finding, got %v", c.want, r.Findings)
			}
			if c.advisory {
				if !r.OK || ExitCode([]Report{r}) != ExitHealthy {
					t.Errorf("%s is advisory and must not make the repo unhealthy: %v",
						c.name, r.Findings.Details(health.SeverityError))
				}
				return
			}
			if r.OK {
				t.Errorf("%s must make the repo unhealthy", c.name)
			}
			if ExitCode([]Report{r}) != ExitUnhealthy {
				t.Error("an unhealthy repo must exit 1")
			}
			// Every finding must carry a registered id and a usable message.
			for _, f := range r.Findings {
				if f.Detail == "" || f.Title == "" {
					t.Errorf("finding is unusable: %+v", f)
				}
			}
		})
	}
}

// TestAbsentAgentsLayerIsTheBaseline: no repo agents file is the baseline seats,
// not a defect — doctor must not report the layer missing or the seats unbound.
func TestAbsentAgentsLayerIsTheBaseline(t *testing.T) {
	r := check(t, newFixtureRepo(t, fixtureOpts{omitAgents: true}))
	if ids(r)[health.IDAgentsLoad] {
		t.Fatalf("absent agents.toml must not be an agents.load finding: %v", r.Findings)
	}
	for _, d := range r.Findings.Details(health.SeverityError) {
		t.Errorf("baseline-only repo must be free of errors: %s", d)
	}
}

func TestStatusDriftFindingsAreInjected(t *testing.T) {
	root := newFixtureRepo(t, fixtureOpts{})
	base := check(t, root)
	if ids(base)[health.IDStatusDrift] {
		t.Fatal("no status-drift authority was injected, so no drift finding may appear")
	}
	withDrift := Check(context.Background(), Opts{
		RepoRoot: root, DataDir: filepath.Join(root, ".satelle"),
		StatusDrift: func(string) health.Findings {
			return health.Findings{health.Error(health.IDStatusDrift, "Story status disagrees with ledger",
				"sty_x row=backlog ledger=done")}
		},
	})
	if !ids(withDrift)[health.IDStatusDrift] || withDrift.OK {
		t.Errorf("injected status drift must FAIL the report: %+v", withDrift.Findings)
	}
	clean := Check(context.Background(), Opts{
		RepoRoot: root, DataDir: filepath.Join(root, ".satelle"),
		StatusDrift: func(string) health.Findings { return nil },
	})
	if ids(clean)[health.IDStatusDrift] {
		t.Errorf("empty injection must not invent drift: %+v", clean.Findings)
	}
}

// TestBreakingDriftFindingsAreInjected (sty_6e143870 AC3): doctor surfaces an
// unacknowledged Breaking release through the injected authority. A manual
// migration is a warning (commands still run); one init heals is an error.
func TestBreakingDriftFindingsAreInjected(t *testing.T) {
	root := newFixtureRepo(t, fixtureOpts{})
	dataDir := filepath.Join(root, ".satelle")
	if ids(check(t, root))[health.IDBreakingUnacknowledged] {
		t.Fatal("no breaking-drift authority was injected, so no finding may appear")
	}
	warn := Check(context.Background(), Opts{
		RepoRoot: root, DataDir: dataDir,
		BreakingDrift: func(string) health.Findings {
			return health.Findings{health.Warn(health.IDBreakingUnacknowledged, "Breaking release needs manual migration",
				"binary 0.0.9 is ahead of deployed stamp 0.0.1 across BREAKING release 0.0.5")}
		},
	})
	if !ids(warn)[health.IDBreakingUnacknowledged] {
		t.Fatalf("injected finding must reach the report: %+v", warn.Findings)
	}
	if !warn.OK {
		t.Errorf("a manual-migration warning must not fail the report: %+v", warn.Findings)
	}
	fail := Check(context.Background(), Opts{
		RepoRoot: root, DataDir: dataDir,
		BreakingDrift: func(string) health.Findings {
			return health.Findings{health.Error(health.IDBreakingUnacknowledged, "Breaking release not applied",
				"binary 0.0.9 is ahead of deployed stamp 0.0.1 across BREAKING release 0.0.5")}
		},
	})
	if fail.OK {
		t.Errorf("an init-heals release still unapplied must FAIL the report: %+v", fail.Findings)
	}
	none := Check(context.Background(), Opts{
		RepoRoot: root, DataDir: dataDir,
		BreakingDrift: func(string) health.Findings { return nil },
	})
	if ids(none)[health.IDBreakingUnacknowledged] {
		t.Errorf("empty injection must not invent a finding: %+v", none.Findings)
	}
}

// TestSubstrateTrackingFindingsAreInjected (sty_45bf867b AC3): doctor surfaces
// an untracked process substrate through the injected authority, as a warning
// that does not fail the report; no injection means no finding.
func TestSubstrateTrackingFindingsAreInjected(t *testing.T) {
	root := newFixtureRepo(t, fixtureOpts{})
	dataDir := filepath.Join(root, ".satelle")
	if ids(check(t, root))[health.IDSubstrateUntracked] {
		t.Fatal("no substrate-tracking authority was injected, so no finding may appear")
	}
	warn := Check(context.Background(), Opts{
		RepoRoot: root, DataDir: dataDir,
		SubstrateTracking: func(string) health.Findings {
			return health.Findings{health.Warn(health.IDSubstrateUntracked, "Process substrate not tracked in git",
				".satelle/ is present but no file under it is tracked in git")}
		},
	})
	if !ids(warn)[health.IDSubstrateUntracked] {
		t.Fatalf("injected finding must reach the report: %+v", warn.Findings)
	}
	if !warn.OK {
		t.Errorf("an untracked-substrate warning must not fail the report: %+v", warn.Findings)
	}
}

// TestScaffoldFindingsAreInjectedNotImported pins the dependency direction:
// doctor never imports the harness-scaffold writer, it consumes the detector as
// an injected authority. A nil injection skips the check rather than guessing.
func TestScaffoldFindingsAreInjectedNotImported(t *testing.T) {
	root := newFixtureRepo(t, fixtureOpts{})
	base := check(t, root)
	if ids(base)[health.IDScaffoldStale] {
		t.Fatal("no scaffold authority was injected, so no scaffold finding may appear")
	}
	withDrift := Check(context.Background(), Opts{
		RepoRoot: root, DataDir: filepath.Join(root, ".satelle"),
		ScaffoldDrift: func(string) health.Findings {
			return health.Findings{health.Error(health.IDScaffoldStale, "Stale", ".claude/settings.json drifted")}
		},
	})
	if !ids(withDrift)[health.IDScaffoldStale] || withDrift.OK {
		t.Errorf("the injected authority's findings must be reported: %+v", withDrift.Findings)
	}
}

// TestEnvValuesAreNeverCarried pins AC6's secret rule at the data layer: the
// report names environment KEYS and whether they resolved, never their values —
// and the JSON payload cannot leak one either.
func TestEnvValuesAreNeverCarried(t *testing.T) {
	const secret = "sk-doctor-must-not-carry-this"
	root := newFixtureRepo(t, fixtureOpts{
		agents: healthyAgentsTOML +
			"\n[judge]\nrole = \"reviewer\"\ncommand = \"sh -c --disallowedTools Write {system}\"\ntools = \"Read\"\nenv = { TOKEN = \"${DOCTOR_SECRET}\", PLAIN = \"" + secret + "\" }\n",
	})
	// Define the var so it resolves — the value must STILL never appear.
	if err := os.WriteFile(filepath.Join(root, ".satelle", "satelle.toml"),
		[]byte("[vars]\nDOCTOR_SECRET = \""+secret+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := check(t, root)

	keys := r.Env["judge"]
	if len(keys) != 2 {
		t.Fatalf("both env keys must be named: %+v", r.Env)
	}
	for _, k := range keys {
		if !k.Resolved {
			t.Errorf("key %s should resolve", k.Key)
		}
	}
	var buf strings.Builder
	if err := RenderJSON(&buf, []Report{r}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), secret) {
		t.Error("the JSON payload must never carry an env value")
	}
	var text strings.Builder
	RenderText(&text, []Report{r}, true, true)
	if strings.Contains(text.String(), secret) {
		t.Error("the text report must never carry an env value")
	}
	if !strings.Contains(text.String(), "TOKEN") {
		t.Errorf("the key NAME must be shown:\n%s", text.String())
	}
}

// TestUnresolvedEnvKeyIsNamedNotValued pins the other half: an unresolved key is
// reported by name with its state, and the finding names the variable only.
func TestUnresolvedEnvKeyIsNamedNotValued(t *testing.T) {
	root := newFixtureRepo(t, fixtureOpts{
		agents: healthyAgentsTOML +
			"\n[judge]\nrole = \"reviewer\"\ncommand = \"sh -c --disallowedTools Write {system}\"\ntools = \"Read\"\nenv = { TOKEN = \"${NOWHERE}\" }\n",
	})
	r := check(t, root)
	if r.Env["judge"][0].Resolved {
		t.Error("an undefined var must report as unresolved")
	}
	var found bool
	for _, f := range r.Findings {
		if f.ID == health.IDEnvUnresolved && strings.Contains(f.Detail, "NOWHERE") {
			found = true
		}
	}
	if !found {
		t.Errorf("want an %s finding naming the variable: %v", health.IDEnvUnresolved, r.Findings)
	}
}

// TestProvenanceReachesTheReport pins AC6's other half: the effective value and
// its SOURCE tier are both present, for a repo value, a profile-supplied value,
// and an embedded fallback.
func TestProvenanceReachesTheReport(t *testing.T) {
	root := newFixtureRepo(t, fixtureOpts{
		agents: "[executor]\nrole = \"agent\"\ncommand = \"in-loop\"\n\n[reviewer]\nprofile = \"catalog-judge\"\nmodel = \"repo-pinned\"\n",
		catalog: "[profiles.catalog-judge]\nrole = \"reviewer\"\n" +
			"command = \"sh -c --disallowedTools Write,Edit {system} {tools} {model}\"\ntools = \"Read,Grep,Glob\"\nmodel = \"profile-model\"\n",
	})
	r := check(t, root)
	if !r.OK {
		t.Fatalf("fixture must be healthy: %v", r.Findings.Details(health.SeverityError))
	}
	src := r.Sources["reviewer"]
	if src["model"] != "repo" {
		t.Errorf("repo override should be sourced to repo, got %q", src["model"])
	}
	if src["command"] != "profile:catalog-judge" {
		t.Errorf("profile-supplied command should name the profile, got %q", src["command"])
	}
	var buf strings.Builder
	RenderText(&buf, []Report{r}, true, false)
	for _, want := range []string{
		`model = "repo-pinned" ← repo (.satelle/workflows/agents.toml)`,
		"← profile:catalog-judge (~/.satelle/agents.toml)",
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("render missing %q:\n%s", want, buf.String())
		}
	}
}
