// The init deployment validation (sty_d0d6bb67): `satelle init` ends by PROVING
// the deployed system green, not just intending it — the agents layer must load
// (and its reviewer harness resolve), every deployed substrate artifact must pass
// its deterministic structure check, and the workflow set must be consistent. A
// repo that fails here would refuse to run, so init surfaces it immediately and
// exits non-zero. Deterministic and store-free: it reads the files init just
// wrote (or preserved), matching what `satelle <noun> validate` reports once the
// substrate is indexed.

package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/doctor"
	"github.com/bobmcallan/satelle/internal/health"
)

// validateDeployment validates the deployed system under dataDir and returns an
// error (init exits non-zero) when it does not validate green.
//
// It is a PRINTER over doctor.Check (sty_e9da28e2), not a second check list:
// init, `satelle doctor`, and the engage precondition must never disagree about
// whether a repo is ready, and the only way to guarantee that is for them to run
// the same code. The one thing init adds is the substrate analysis below, which
// judges what init itself just wrote.
func validateDeployment(out io.Writer, dataDir string) error {
	fmt.Fprintln(out, "\nValidating the deployed system:")
	failed := 0

	repoRoot := filepath.Dir(dataDir)

	// The retired actors.toml deserves its own instruction, and only init is in a
	// position to give it (doctor reports the file as simply missing).
	agentsPath, _ := config.AgentsPath(dataDir)
	if _, statErr := os.Stat(agentsPath); os.IsNotExist(statErr) {
		if _, lerr := os.Stat(filepath.Join(dataDir, config.ActorsConfigName)); lerr == nil {
			fmt.Fprintf(out, "FAIL  %s/%s — missing; the retired %s is present — rename it to %s\n",
				config.DefaultDataDir, config.AgentsRel, config.ActorsConfigName, config.AgentsRel)
		}
	}

	systemFailed := validateSystem(out, dataDir, false)
	failed += systemFailed
	if systemFailed == 0 {
		fmt.Fprintln(out, "PASS  agents layer, workflows, skills, principles, tasks")
	}

	// Substrate analysis: placement/residency/tag-axis (fatal), unedited
	// constitution + config drift (advisory). Print advisory WARN lines even when
	// fatals will fail the init, so they stay visible. This is the one check init
	// owns — it judges what init itself just wrote.
	defects := analyzeSubstrate(dataDir, repoRoot)
	failed += reportSubstrateAnalysis(out, defects)

	if failed > 0 {
		return fmt.Errorf("init: the deployed system failed validation (%d problem(s)) — fix the reported files and re-run `satelle init`", failed)
	}
	fmt.Fprintln(out, "PASS  deployed system validates green")
	return nil
}

// validateSystem runs doctor.Check over the deployed system under dataDir, prints
// its findings and returns how many are errors. `sync.*` findings are warnings
// here, never failures: sync backup state is runtime health, not a scaffold defect
// — `satelle doctor` still fails them (sty_30696eeb), but init/migrate must not
// refuse a repo that has not yet pushed, nor a push refuse for not having pushed.
//
// forPush judges the configuration as another machine would load it (this
// machine's agents.local.toml and the synced workspace layer are left out) and
// prints only the failures: a push names what stops it, not every advisory.
func validateSystem(out io.Writer, dataDir string, forPush bool) (failed int) {
	rep := doctor.Check(context.Background(), doctor.Opts{
		RepoRoot:                  filepath.Dir(dataDir),
		DataDir:                   dataDir,
		ScaffoldDrift:             scaffoldFindings,
		SubstrateTracking:         substrateTrackingFindings,
		ExcludeMachineAgentLayers: forPush,
	})
	if !forPush {
		for _, f := range rep.Findings.WithSeverity(health.SeverityWarn) {
			fmt.Fprintf(out, "WARN  [%s] %s\n", f.ID, f.Detail)
		}
	}
	for _, f := range rep.Findings.WithSeverity(health.SeverityError) {
		if strings.HasPrefix(f.ID, "sync.") {
			if !forPush {
				fmt.Fprintf(out, "WARN  [%s] %s\n", f.ID, f.Detail)
			}
			continue
		}
		failed++
		fmt.Fprintf(out, "FAIL  [%s] %s\n", f.ID, f.Detail)
		if f.Remediation != "" {
			fmt.Fprintf(out, "      fix: %s\n", f.Remediation)
		}
	}
	return failed
}

// pushValidator is the validation preflightPush runs: validateSystem judged for a
// push. It is a variable only so the tests of push MECHANICS — which push
// throwaway fixtures that are not valid substrate — can stand it down (TestMain);
// the validation tests put the real one back (realPushValidator).
var pushValidator = func(out io.Writer, dataDir string) int { return validateSystem(out, dataDir, true) }

// preflightPush is the check every push of configuration runs before it contacts
// the server (sty_89b9df20): the same deployed-system validation `satelle init`
// runs, judged as another machine would load the configuration. A configuration
// that fails it is refused, so a push never publishes what would not load on
// the machine that receives it.
func preflightPush(out io.Writer, dataDir string) error {
	if failed := pushValidator(out, dataDir); failed > 0 {
		return fmt.Errorf("push refused: the configuration fails validation (%d problem(s)) — fix the findings above", failed)
	}
	return nil
}

// The three helpers below are thin delegations to internal/doctor, which owns
// these readers. They remain here only so init's tests keep their existing call
// sites; there is exactly one implementation (sty_e9da28e2).

// skillResolves reports whether a skill name resolves on disk or as an embedded
// default.
func skillResolves(dataDir string) func(skill string) bool { return doctor.SkillResolver(dataDir) }

// deployedVars loads the [vars] KV from the deployed satelle.toml (overlaid by
// the gitignored satelle.local.toml).
func deployedVars(dataDir string) map[string]string { return doctor.RepoVars(dataDir) }

// deployedWorkflowDocs reads the deployed workflow files under dataDir as docs.
func deployedWorkflowDocs(dataDir string) []docindex.Doc { return doctor.WorkflowDocs(dataDir) }
