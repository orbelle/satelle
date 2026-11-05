// Per-noun validation: `satelle workflow|skill|principle|task validate [name]`.
// "validate" is a SEMANTIC, kind-specific check — a valid workflow PROCESS, a
// valid skill contract — run DETERMINISTICALLY (code, not an LLM rubric) over the
// on-disk files, reporting pass/fail and exiting non-zero on any failure. It is
// distinct from OKF conformance, which `satelle reindex` owns and self-heals.
// There is no generic top-level `satelle validate`: each noun validates its own.
package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/agentstep"
	"github.com/bobmcallan/satelle/internal/agentvalidate"
	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/doctor"
	"github.com/bobmcallan/satelle/internal/structure"
	"github.com/bobmcallan/satelle/internal/wfdot"
	"github.com/bobmcallan/satelle/internal/wfgovern"
)

// authoredValidateCmd builds the per-noun `<noun> validate [name]` subcommand for
// an authored kind (workflows/skills/principles/tasks).
func authoredValidateCmd(kind string) *cobra.Command {
	return &cobra.Command{
		Use:   "validate [name]",
		Short: "Validate authored " + kind + " against the deterministic " + strings.TrimSuffix(kind, "s") + " check",
		Long: `Check authored ` + kind + ` against the deterministic structure check — frontmatter,
required fields, the contract that kind must satisfy — for one name, or all of
them when no name is given.

Deterministic and read-only: it reports problems and changes nothing, and no
agent is dispatched. Run it after editing a file; the same check runs where the
binary loads that substrate, so a green result here is what loading expects.`,
		Args:        cobra.MaximumNArgs(1),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) > 0 {
				name = args[0]
			}
			a, err := appFrom(cmd)
			if err != nil {
				return err
			}
			if err := validateKind(cmd, a, kind, name); err != nil {
				return err
			}
			return nil
		},
	}
}

// validateKind runs the deterministic check for one kind over its on-disk files
// (nameFilter narrows to one), printing pass/fail and returning an error if any
// fail. Workflows also get the cross-workflow consistency check.
func validateKind(cmd *cobra.Command, a *app.App, kind, nameFilter string) error {
	out := cmd.OutOrStdout()

	dataDir := a.PlaneDir()
	// Disk-backed, through the same helper `satelle doctor` uses, so the two can
	// never contradict each other on an unchanged tree (sty_540cfcd3). The doc
	// index lags an authored edit, and validating right after an edit is exactly
	// what an author does.
	resolve := doctor.SkillResolver(dataDir)
	roots := a.AuthoredDirs()
	dir := roots.Dirs[kind]
	if kind == "tasks" {
		// Tasks are authored substrate under DataDir — never RuntimeDir (sty_4660bbe1).
		dir = filepath.Join(dataDir, "tasks")
	}
	// With a project tier, the files the team owns are validated where they
	// govern — the tier — and the repo's copies of them are not.
	owned := func(fn string) bool { return roots.Owns(kind + "/" + fn) }
	validated, failed, exempt := validateAuthoredFiles(out, kind, dir, nameFilter, resolve, owned)
	if tierDir := roots.Index().TierDir(kind); tierDir != "" && kind != "tasks" {
		v, f, e := validateAuthoredFiles(out, kind, tierDir, nameFilter, resolve, func(fn string) bool { return !owned(fn) })
		validated, failed, exempt = validated+v, failed+f, exempt+e
	}

	// A default task is virtual — init writes no header for it — so a named check
	// with no such file on disk validates the copy the binary ships. That is how the
	// begin-run gate tells a default task from a header that went missing.
	if kind == "tasks" && nameFilter != "" && validated+failed+exempt == 0 {
		v, f := validateEmbeddedTask(out, nameFilter)
		validated, failed = validated+v, failed+f
	}

	// Cross-workflow consistency (ambiguous applies_to, unresolved referenced
	// skills) — a whole-set check, so only for the workflows kind without a name.
	if kind == "workflows" && nameFilter == "" {
		// CONSISTENCY judges the AUTHORED set; ALLOCATION judges the GOVERNING set
		// (authored ∪ unshadowed embedded). doctor has always drawn that line —
		// an on-disk wildcard workflow legitimately shadows the embedded wildcard
		// baseline, so feeding both to the ambiguity check would report every repo
		// as broken, while allocation must still see embedded defaults that really
		// do govern a repo which has not overridden them.
		for _, p := range agentstep.WorkflowConsistency(doctor.WorkflowDocs(dataDir), resolve) {
			failed++
			fmt.Fprintf(out, "FAIL  workflows (consistency) — %s\n", p)
		}
		// Per-gate effective model surface (sty_19456622) — same allocation view as
		// `satelle agent validate`, informational (not a hard fail).
		//
		// An agents layer that does not load is a failure, not a silent skip
		// (sty_7028d996): a binding declaring an unknown placement is refused at
		// load, and this is where an author sees it.
		eff, aerr := config.LoadEffectiveAgents(dataDir, a.PlaneConfig().Vars)
		if aerr != nil {
			failed++
			fmt.Fprintf(out, "FAIL  agents — %v\n", aerr)
		} else {
			report := agentvalidate.Validate(eff.Agents, eff.Vars, doctor.GoverningWorkflows(dataDir))
			if len(report.Gates) > 0 {
				fmt.Fprintln(out, "Gate/node effective models:")
				for _, ga := range report.Gates {
					fmt.Fprintf(out, "  NODE [%s] %s gate=%s agent=%s effective_model=%q\n",
						ga.Workflow, ga.Node, ga.Skill, ga.Agent, ga.EffectiveModel)
				}
			}
			if len(report.Grants) > 0 {
				fmt.Fprintln(out, "Bindings:")
				for _, g := range report.Grants {
					fmt.Fprintf(out, "  BINDING [%s] placement=%s\n", g.Name, g.Placement)
				}
			}
		}
	}

	// Named form: the whole-set block above is skipped, which used to mean a
	// workflow naming a gate skill that does not resolve validated clean on the
	// very path an author uses while authoring it — and then advanced ungated at
	// run time (sty_d59ec6a9). Report the per-workflow half here.
	//
	// WARN, not FAIL: a repo mid-authoring writes the workflow before it writes
	// the gate skills, and the embedded satelle-workflow-advisor skill drives
	// exactly this named form. Blocking would make the ordinary authoring
	// sequence impossible. The whole-set path keeps FAIL — it answers a
	// different question (is my substrate coherent as shipped).
	//
	// The ambiguity check is NOT run here: it compares repo workflows against
	// each other, so it is whole-set by nature (AC3).
	if kind == "workflows" && nameFilter != "" {
		for _, w := range doctor.WorkflowDocs(dataDir) {
			if w.Name != nameFilter {
				continue
			}
			problems := agentstep.WorkflowSkillProblems(w, resolve)
			for _, p := range problems {
				fmt.Fprintf(out, "WARN  workflows/%s — %s\n", w.Name, p)
			}
			if len(problems) > 0 {
				// Name the CONSEQUENCE, not just the missing file. A warning that
				// only says "skill not found" is the same silence one level up:
				// the reader has to already know that an unresolved gate degrades
				// to advisory and lets the transition through.
				fmt.Fprintln(out, "      an edge whose gate skill does not resolve is ADVISORY — it advances UNGATED, with no reviewer and no verdict, until the skill exists")
			}
			break
		}
	}

	// A step skill that declares no evidence gets only the delivery check
	// (sty_23ceeca2). WARN, never FAIL: evidence is opt-in per skill, and a skill
	// mid-authoring declares it last.
	if kind == "skills" {
		for _, name := range stepSkillsWithoutEvidence(dataDir, nameFilter) {
			fmt.Fprintf(out, "WARN  skills/%s — step skill declares no evidence (evidence_sections, evidence_keys, evidence_commands or evidence_tools); only its delivery is checked\n", name)
		}
	}

	// Cross-half route join (sty_5d712bc5): every obligation in every category
	// table must resolve to a step table key. Per-file checks cannot see the
	// sibling half, so this lives here — both whole-set and named forms, because
	// an author validating one half must still see the break. If a half does
	// not parse, emit nothing: structure.Doc already named the parse failure.
	if kind == "workflows" {
		rs := wfgovern.RouteSourceOf(doctor.GoverningWorkflows(dataDir))
		if rs.Present() {
			lists, derr := wfdot.ParseDone(rs.Done)
			cat, serr := wfdot.ParseSteps(rs.Step)
			if serr == nil {
				// The deprecated `schedule` key still works. WARN, never FAIL: the
				// repo's route is valid, it only names a spelling that is going.
				for _, st := range wfdot.LegacyScheduleSteps(cat) {
					fmt.Fprintf(out, "WARN  workflows/step.toml — step %q: schedule = %q is deprecated — omit it (parallel is the default) or set sequential = true\n", st.Provides, st.Schedule)
				}
			}
			if derr == nil && serr == nil {
				unresolved := wfdot.UnresolvedObligations(lists, cat)
				for _, p := range unresolved {
					failed++
					fmt.Fprintf(out, "FAIL  workflows (route) — %s\n", p)
				}
				if len(unresolved) > 0 {
					fmt.Fprintln(out, "      an obligation names a step's TABLE KEY, never the status it declares — statuses repeat across steps by design")
				} else {
					// A route that cannot be assembled (a remote_wait outcome mapped
					// to an edge the route does not declare, say) is a structural
					// failure the first story to walk it would otherwise find.
					for _, p := range wfdot.RouteErrors(lists, cat) {
						failed++
						fmt.Fprintf(out, "FAIL  workflows (route) — %s\n", p)
					}
				}
			}
		}
	}

	fmt.Fprintf(out, "\nvalidated %d, failed %d, exempt %d\n", validated, failed, exempt)
	if failed > 0 {
		return fmt.Errorf("%d %s failed validation", failed, kind)
	}
	return nil
}

// validateAuthoredDir runs the deterministic per-file check for one kind over a
// directory's authored files (nameFilter narrows to one), printing PASS/FAIL
// lines to out and returning the counts. It is file-based and store-free, so it
// serves both the per-noun `satelle <noun> validate` (validateKind) and the init
// deployment validation (sty_d0d6bb67), which runs before anything is indexed.
//
// Which files count is docindex.Indexable's answer, not a private `.md` test.
// This walk carried its own and it silently stopped seeing the route source the
// moment it became TOML: `workflow validate` reported "validated 0" on a
// perfectly good route, which reads as a clean pass (sty_81bb0dde).
func validateAuthoredDir(out io.Writer, kind, dir, nameFilter string, resolve func(skill string) bool) (validated, failed, exempt int) {
	return validateAuthoredFiles(out, kind, dir, nameFilter, resolve, nil)
}

// validateAuthoredFiles is validateAuthoredDir skipping every file name skip
// reports; nil skips none.
func validateAuthoredFiles(out io.Writer, kind, dir, nameFilter string, resolve func(skill string) bool, skip func(fn string) bool) (validated, failed, exempt int) {
	// A dir that exists and cannot be read is a FAILURE, not an empty kind
	// (sty_d6e209aa). For workflows it is the named message every surface prints:
	// the embedded route would otherwise govern, and its gates are not the repo's.
	if state, perr := docindex.ProbeDir(dir); state == docindex.DirUnreadable {
		if kind == "workflows" {
			fmt.Fprintf(out, "FAIL  workflows — %s\n", wfgovern.UnreadableMessage(dir, perr.Error()))
		} else {
			fmt.Fprintf(out, "FAIL  %s — %s cannot be read: %v\n", kind, dir, perr)
		}
		return 0, 1, 0
	}
	entries, derr := os.ReadDir(dir)
	if derr != nil {
		return 0, 0, 0
	}
	for _, e := range entries {
		fn := e.Name()
		if e.IsDir() || !docindex.Indexable(fn) || (skip != nil && skip(fn)) {
			continue
		}
		if kind == "tasks" && !strings.HasPrefix(fn, "tsk_") {
			continue
		}
		name := strings.TrimSuffix(fn, filepath.Ext(fn))
		if nameFilter != "" && nameFilter != name {
			continue
		}
		if reservedKeepFile(fn) {
			exempt++
			fmt.Fprintf(out, "EXEMPT %s/%s (reserved keep-file)\n", kind, name)
			continue
		}
		body, rerr := os.ReadFile(filepath.Join(dir, fn))
		if rerr != nil {
			failed++
			fmt.Fprintf(out, "FAIL  %s/%s — read: %v\n", kind, name, rerr)
			continue
		}
		validated++
		var problems []string
		switch kind {
		case "documents":
			if err := docindex.OKFConformance(name, string(body)); err != nil {
				problems = []string{err.Error()}
			}
		case "tasks":
			problems = structure.CheckTask(string(body))
		default:
			problems = structure.Doc(kind, name, string(body), resolve)
		}
		if len(problems) > 0 {
			for _, p := range problems {
				failed++
				fmt.Fprintf(out, "FAIL  %s/%s — %s\n", kind, name, p)
			}
		} else {
			fmt.Fprintf(out, "PASS  %s/%s\n", kind, name)
		}
	}
	return validated, failed, exempt
}

// stepSkillsWithoutEvidence names the skills the governing step catalogue
// delivers to a step's performer (not reviewer gates) whose body declares no
// evidence, in catalogue order and de-duplicated; nameFilter narrows to one. A
// skill that does not resolve is the route check's finding, not this one's.
func stepSkillsWithoutEvidence(dataDir, nameFilter string) []string {
	rs := wfgovern.RouteSourceOf(doctor.GoverningWorkflows(dataDir))
	if !rs.Present() {
		return nil
	}
	cat, err := wfdot.ParseSteps(rs.Step)
	if err != nil {
		return nil
	}
	body := doctor.SkillBodyResolver(dataDir)
	seen := map[string]bool{}
	var out []string
	for _, st := range cat.Steps {
		for _, sk := range st.Skills {
			if sk == "" || seen[sk] || (nameFilter != "" && nameFilter != sk) {
				continue
			}
			seen[sk] = true
			if b, ok := body(sk); ok && !structure.SkillDeclaresEvidence(b) {
				out = append(out, sk)
			}
		}
	}
	return out
}

// validateEmbeddedTask runs the task check over the copy of a default task the
// binary ships, for a name with no file on disk. It reports nothing for a name
// that is not a default.
func validateEmbeddedTask(out io.Writer, name string) (validated, failed int) {
	body, ok := embeddedDefault("tasks", name)
	if !ok {
		return 0, 0
	}
	if problems := structure.CheckTask(body); len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintf(out, "FAIL  tasks/%s — %s\n", name, p)
		}
		return 1, len(problems)
	}
	fmt.Fprintf(out, "PASS  tasks/%s (embedded default)\n", name)
	return 1, 0
}

// reservedKeepFile reports whether fn is a reserved, non-authored keep-file that
// validation exempts: the per-dir README, and the OKF layer's reserved
// index.md/log.md. Everything else under an authored dir must comply.
func reservedKeepFile(fn string) bool {
	switch fn {
	case "README.md", "index.md", "log.md":
		return true
	default:
		return false
	}
}
