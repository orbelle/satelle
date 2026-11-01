// `satelle substrate` — effective process view and virtual-defaults tooling
// (sty_29e5a9a5 / epic:substrate-planes). Defaults resolve from the binary;
// on-disk files are overrides. list --effective shows provenance; edit
// materializes a default for day-one editing; prune removes unedited seeds.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/reviewscore"
	"github.com/bobmcallan/satelle/internal/substrate"
	"github.com/bobmcallan/satelle/internal/substratereview"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/wfgovern"
	"github.com/bobmcallan/satelle/internal/wfroute"
	"github.com/bobmcallan/satelle/internal/workitem"
)

func init() {
	root := &cobra.Command{
		Use:   "substrate",
		Short: "Effective process view and virtual-defaults tooling",
		Long: `Substrate is the authored process (workflows, skills, principles) layered
over the binary's sparse embedded defaults.

  satelle substrate list --effective   provenance default|edited|authored
  satelle substrate edit <kind> <name> materialize a default for editing
  satelle substrate prune              remove unedited seed copies (dry-run default)
  satelle substrate condense           the rules a resident document is condensed by
  satelle substrate review             judge every item against its history and the constitution
  satelle substrate review --apply <item>...  raise a substrate story for each cut you choose

See decision-substrate-planes-local-first.`,
	}

	var asJSON bool
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List effective substrate with provenance",
		Long: `List the substrate this repo actually runs, each item tagged with where it came
from: default (embedded), edited (a materialised copy you changed), or authored
(yours alone).

The provenance is the reason to run it. Overriding is per FILE, so an embedded
default stays live until a repo-authored file of the same name exists — this is
what shows you which half of the tree is really yours.`,
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := appFrom(cmd)
			if err != nil {
				return err
			}
			// Always effective (the only meaningful list under virtual defaults).
			return runSubstrateList(cmd.OutOrStdout(), a.Store.DocIndex, asJSON)
		},
	}
	listCmd.Flags().Bool("effective", true, "show effective process (default; always on)")
	listCmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON")

	editCmd := &cobra.Command{
		Use:   "edit [kind] [name]",
		Short: "Materialize an embedded default onto disk for editing",
		Long: `Copy an embedded default onto disk under .satelle/ so you can edit it.

The copy takes over from that moment: the file OVERRIDES the embedded default by
name, and it no longer tracks improvements shipped in later binaries. Materialise
what you actually intend to diverge on — satelle substrate prune removes copies
that were never edited.`,
		Args:        cobra.ExactArgs(2),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := appFrom(cmd)
			if err != nil {
				return err
			}
			return runSubstrateEdit(cmd.OutOrStdout(), a.DataDir, a.Store.DocIndex, args[0], args[1])
		},
	}

	var yes bool
	pruneCmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove unedited seed copies of embedded defaults (dry-run unless --yes)",
		Long: `Remove on-disk copies of embedded defaults that were never edited, so the repo
tracks the shipped version again.

Dry-run unless --yes: it reports what it would remove first. Only byte-identical
copies go — anything you changed is authored substrate and is left alone, which
is why this cannot quietly discard your work.`,
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := appFrom(cmd)
			if err != nil {
				return err
			}
			opts := ResolveBackupOpts(a.Config, a.RepoRoot)
			opts.BackupsDir = a.RuntimeDir
			// Resolve the seat off the app handle this command already opened —
			// currentSeat() would app.Open() a second handle on the same DB.
			storyID, state := pruneSeat(cmd.Context(), a)
			return runSubstratePrune(cmd.Context(), cmd.OutOrStdout(), cmd.InOrStdin(),
				a.DataDir, opts, yes, storyID, state)
		},
	}
	pruneCmd.Flags().BoolVar(&yes, "yes", false, "actually remove files (default is dry-run)")

	condenseCmd := &cobra.Command{
		Use:   "condense",
		Short: "Print the rules a resident document is condensed by (embedded defaults + repo override)",
		Long: `Print the effective condense rules as TOML: the binary's embedded defaults with
<data dir>/config/condense.toml laid over them, key by key.

These rules decide how a resident document is fitted when its context has a size
budget: the ladder, the filler dropped, the tokens never edited, where a clause
or a sentence ends. Read-only; the authored documents are never changed.`,
		Args:        cobra.NoArgs,
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := appFrom(cmd)
			if err != nil {
				return err
			}
			rules, rerr := condenseRules(a.DataDir)
			if rerr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "satelle: %s does not parse, showing the embedded rules: %v\n", config.CondenseOverrideRel, rerr)
			}
			return toml.NewEncoder(cmd.OutOrStdout()).Encode(rules)
		},
	}

	root.AddCommand(listCmd, editCmd, pruneCmd, condenseCmd, newSubstrateReviewCmd())
	register(root)
}

func runSubstrateList(out io.Writer, idx *docindex.Store, asJSON bool) error {
	rows, err := substrate.List(context.Background(), idx)
	if err != nil {
		return err
	}
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}
	fmt.Fprintf(out, "%-12s %-40s %-10s %s\n", "KIND", "NAME", "PROV", "SOURCE")
	for _, r := range rows {
		fmt.Fprintf(out, "%-12s %-40s %-10s %s\n", r.Kind, r.Name, r.Provenance, r.Source)
	}
	fmt.Fprintf(out, "\n%d effective item(s)\n", len(rows))
	return nil
}

// isUneditedSeed delegates to substrate (shared with prune and provenance).
func isUneditedSeed(diskBody, embeddedBody string) bool {
	return substrate.IsUneditedSeed(diskBody, embeddedBody)
}

func runSubstrateEdit(out io.Writer, dataDir string, idx *docindex.Store, kind, name string) error {
	kind = strings.TrimSpace(kind)
	name = strings.TrimSpace(name)
	if kind == "" || name == "" {
		return fmt.Errorf("substrate edit: kind and name required")
	}
	rel := kind + "/" + name + config.EmbeddedExt(kind, name)
	path := filepath.Join(dataDir, filepath.FromSlash(rel))
	if fileExists(path) {
		fmt.Fprintln(out, path)
		return nil
	}
	// Resolve embedded body.
	var body string
	for _, d := range config.EmbeddedDefaults() {
		if d.Kind == kind && d.Name == name {
			body = d.Body
			break
		}
	}
	if body == "" {
		// Try docindex Get (may be already virtual).
		if doc, err := idx.Get(context.Background(), kind, name); err == nil {
			body = doc.Body
		}
	}
	if body == "" {
		return fmt.Errorf("substrate edit: no embedded default %s/%s", kind, name)
	}
	stamped := applyEmbeddedStamp(body, substrate.SHA(body))
	if err := writeEmbedded(path, stamped); err != nil {
		return err
	}
	fmt.Fprintln(out, path)
	return nil
}

// recordPruneChange is the seam prune records removals through (injectable so
// the CLI test can observe the call without a live ledger).
var recordPruneChange = verb.RecordPathMutation

// pruneSeat reports the engaged story and the status it is engaged at, using the
// already-open app handle. Fail-open: no seat, or an unreadable one, means prune
// records nothing — pruning outside a story is legitimate.
func pruneSeat(ctx context.Context, a *app.App) (storyID, state string) {
	if a.Store == nil || a.Store.Leases == nil {
		return "", ""
	}
	wfs, err := a.Store.DocIndex.List(ctx, "workflows")
	if err != nil {
		return "", ""
	}
	items, err := a.Store.Stories.List(ctx, workitem.ListFilter{})
	if err != nil {
		return "", ""
	}
	leases, err := a.Store.Leases.List(ctx)
	if err != nil {
		return "", ""
	}
	live, _, err := evaluateSeat(leases, items, wfs, time.Now().UTC())
	if err != nil || len(live) == 0 {
		return "", ""
	}
	seat, _ := pickSessionSeat(live, nil, config.ResolveSession())
	return seat.ItemID, seat.StoryStatus
}

func runSubstratePrune(ctx context.Context, out io.Writer, in io.Reader, dataDir string, opts BackupOpts, yes bool, storyID, storyState string) error {
	plan := config.UneditedSeedPaths(dataDir) // rel paths to remove, tasks included
	if len(plan) == 0 {
		fmt.Fprintln(out, "prune: no unedited seed copies found")
		return nil
	}
	fmt.Fprintf(out, "prune would remove %d unedited seed(s):\n", len(plan))
	for _, rel := range plan {
		fmt.Fprintf(out, "  - %s\n", rel)
	}
	if !yes {
		fmt.Fprintln(out, "dry-run only — re-run with --yes to remove (edited/authored files are never touched)")
		return nil
	}
	var removed []string
	for _, rel := range plan {
		path := filepath.Join(dataDir, filepath.FromSlash(rel))
		body, _ := os.ReadFile(path)
		if _, err := backupExistingFile(dataDir, BackupKindPreMutation, rel, body, opts); err != nil {
			return fmt.Errorf("prune: backup %s: %w", rel, err)
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("prune: remove %s: %w", rel, err)
		}
		removed = append(removed, path)
		fmt.Fprintf(out, "  removed %s\n", rel)
	}
	// These deletions are invisible to every re-derived evidence channel — git
	// cannot see an ignored path and the mtime walk cannot see a file that is
	// gone — so record them here or the engaged story has no evidence of the
	// slice (sty_30d3bd99). Best-effort: never lose the operator their prune.
	if storyID != "" && len(removed) > 0 {
		if err := recordPruneChange(ctx, storyID, removed, storyState, storyState, time.Now().UTC()); err != nil {
			fmt.Fprintf(out, "prune: warning: could not record the removals against %s: %v\n", storyID, err)
		}
	}
	fmt.Fprintf(out, "prune: removed %d seed(s); effective process is unchanged (virtual defaults)\n", len(plan))
	return nil
}

// substrateReviewSkill is the embedded rubric the review's judge runs under.
const substrateReviewSkill = "satelle-substrate-review"

// substrateManifestDoc names the document each judge dispatch carries.
const substrateManifestDoc = "substrate-review-manifest"

// substrateJudge judges one batch: its judgement, and the seat's raw answer,
// kept beside the report when the answer does not parse.
type substrateJudge func(ctx context.Context, in substratereview.JudgeInput) (substratereview.Judgement, string, error)

// newSubstrateJudge builds the judge `substrate review` runs each batch
// through. It is a seam so tests judge with a stub and never start a model; the
// default goes through newCaseJudge — the isolated reviewer path a real gate
// uses — under the satelle-substrate-review rubric, with the batch's manifest
// (pointers, never the items' text or history) as its one document. With no
// --binding the judge is the repo's in-loop model (substrateJudgeSeat).
var newSubstrateJudge = func(cmd *cobra.Command, binding string, hist substratereview.History) (substrateJudge, error) {
	a, err := appFrom(cmd)
	if err != nil {
		return nil, err
	}
	name, resolve, err := substrateJudgeSeat(a, binding, hist)
	if err != nil {
		return nil, err
	}
	j, err := newCaseJudge(a, name, resolve)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, in substratereview.JudgeInput) (substratereview.Judgement, string, error) {
		b, err := json.MarshalIndent(substratereview.ManifestOf(in), "", "  ")
		if err != nil {
			return substratereview.Judgement{}, "", err
		}
		res, err := j.Judge(ctx, reviewscore.Case{
			ID: "substrate-review-" + in.Area, Skill: substrateReviewSkill, StoryID: "substrate-review",
			From: "backlog", To: "plan",
			Payload: &reviewscore.ReplayPayload{
				Title: "Review this repo's substrate: " + in.Area,
				Body: fmt.Sprintf("Judge every item, and every constitution rule it names, in the %s document under the satelle-substrate-review rubric. Area: %s. "+
					"The document carries pointers only: every path in it is an absolute file (under %s, or in this repo) — read each with your read tools.",
					substrateManifestDoc, in.Area, in.Scratch),
				Category: substratereview.ApplyCategory, NoPatch: true,
				Docs: []reviewscore.ReplayDoc{{Name: substrateManifestDoc, Type: "document", Body: string(b)}},
			},
		})
		if err != nil {
			return substratereview.Judgement{}, res.Notes, err
		}
		if res.Verdict != reviewscore.VerdictAccept {
			return substratereview.Judgement{}, res.Notes, fmt.Errorf("the judge did not accept the batch: %s", judgeNoteLine(res.Notes))
		}
		jd, err := substratereview.ParseJudgement(res.Notes)
		return jd, res.Notes, err
	}, nil
}

// substrateJudgeSeat is the seat a review judges under. A named --binding is
// used as agents.toml has it. With none, the judge is the repo's in-loop
// (orchestrator) model: the reviewer seat — its grant, role and budget row —
// re-pointed at the preset one-shot command of the in-loop agent CLI and the
// model the latest in-loop session_model row names.
func substrateJudgeSeat(a *app.App, binding string, hist substratereview.History) (string, func(string) (config.AgentBinding, bool), error) {
	if binding != "" {
		return binding, nil, nil
	}
	eff, err := requireAgents(a)
	if err != nil {
		return "", nil, err
	}
	base, _ := eff.Agents.LiveRawBinding("reviewer")
	judge, err := orchestratorJudgeBinding(base, hist.OrchestratorAgent, hist.Orchestrator)
	if err != nil {
		return "", nil, err
	}
	return "reviewer", func(name string) (config.AgentBinding, bool) {
		if name == "reviewer" {
			return judge, true
		}
		b, ok := eff.Agents.RawBinding(name)
		if !ok {
			return config.AgentBinding{}, false
		}
		return eff.Agents.EffectiveBinding(b, config.UseOneShot), true
	}, nil
}

// orchestratorJudgeBinding is base re-pointed at agent's preset one-shot
// command (the same agent → command mapping config.StarterGlobalAgents uses)
// and model. It keeps base's grant, role, principles, env and timeouts, and
// nothing that would route the judge back to base's own agent (its command,
// interface, effort, settings, secondary, profile or placement). An agent with
// no preset, or no recorded in-loop model, has no one-shot judge.
func orchestratorJudgeBinding(base config.AgentBinding, agent, model string) (config.AgentBinding, error) {
	const stop = "substrate review: no in-loop model recorded for a one-shot judge"
	if strings.TrimSpace(agent) == "" || strings.TrimSpace(model) == "" {
		return config.AgentBinding{}, fmt.Errorf("%s — pass --binding", stop)
	}
	r, err := agentcli.NewRunner(agent)
	if err != nil {
		return config.AgentBinding{}, fmt.Errorf("%s (the in-loop agent %q has no one-shot runner) — pass --binding", stop, agent)
	}
	command := r.Command()
	return config.AgentBinding{
		Interface: config.InterfaceCommand, Command: command, Model: model,
		Tools: base.Tools, Role: base.Role, Principles: base.Principles, InjectPrinciples: base.InjectPrinciples,
		Env: base.Env, Timeout: base.Timeout, IdleTimeout: base.IdleTimeout, BusyTimeout: base.BusyTimeout,
	}, nil
}

// judgeNoteLine is the first line of a judge's notes, bounded, for an error;
// the whole answer is kept in the raw file beside the report.
func judgeNoteLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

func newSubstrateReviewCmd() *cobra.Command {
	var asJSON bool
	var binding, out, from, server string
	var apply []string
	cmd := &cobra.Command{
		Use:   "review",
		Short: "Judge every substrate item against its default, the code and the BDD baseline; --apply raises the cuts you choose",
		Long: `Give every principle, skill, task, workflow step and gate, agent seat and
constitution rule a disposition (remove, reduce, keep, conflict) or "not
judged", one judge dispatch per area, each sent a manifest of file paths, never
the items' text or history.

The judge is the repo's in-loop model on the reviewer seat; --binding names an
agents.toml section instead. Changes no substrate file: the JSON report goes to
--out or the OS temp dir.

Each review runs as an execution of the task tsk_substrate-review, shown in
the local UI's Activity tab while it runs.

--apply <item> raises one category-substrate story per chosen item; --from
<report.json> applies a saved report. See satelle help substrate.`,
		Args:        cobra.NoArgs,
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := appFrom(cmd)
			if err != nil {
				return err
			}
			var rep substratereview.Report
			if from != "" {
				if len(apply) == 0 {
					return fmt.Errorf("--from applies a saved report: give --apply <item>")
				}
				if rep, err = substratereview.ReadReport(from); err != nil {
					return err
				}
			} else {
				ctx := cmd.Context()
				path, err := substrateReportPath(a.RepoRoot, out)
				if err != nil {
					return err
				}
				run, err := startSubstrateReviewRun(ctx, a, cmd.ErrOrStderr())
				if err != nil {
					return err
				}
				if rep, err = runSubstrateReview(cmd, a, binding, path, run); err != nil {
					return run.fail(ctx, err)
				}
				rep.ExecutionID = run.id
				if err := rep.WriteJSON(path); err != nil {
					return run.fail(ctx, err)
				}
				if err := run.stage(ctx, "report", map[string]any{"path": path, "items": len(rep.Items)}); err != nil {
					return run.fail(ctx, err)
				}
				if asJSON {
					enc := json.NewEncoder(cmd.OutOrStdout())
					enc.SetIndent("", "  ")
					if err := enc.Encode(rep); err != nil {
						return err
					}
				} else {
					rep.Render(cmd.OutOrStdout())
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "report: %s\n", path)
				for _, f := range rep.Failures {
					fmt.Fprintf(cmd.ErrOrStderr(), "substrate review: batch %s returned no judgement (%s); its items read %q — raw answer: %s\n",
						f.Label(), f.Err, substratereview.NotJudged, f.RawPath)
				}
				if rep.Batches > 0 && len(rep.Failures) == rep.Batches {
					return run.fail(ctx, fmt.Errorf("substrate review: every batch (%d) returned no judgement — the raw answers are named above", rep.Batches))
				}
				if err := run.finish(ctx, path); err != nil {
					return err
				}
			}
			if len(apply) == 0 {
				return nil
			}
			return applySubstrateCuts(cmd.Context(), cmd.OutOrStdout(), rep, apply, substrateHolding(cmd.Context(), a, resolveServer(server)))
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the report as JSON instead of markdown")
	cmd.Flags().StringVar(&binding, "binding", "", "agents.toml section that judges (default: the repo's in-loop model on the reviewer seat)")
	cmd.Flags().StringVar(&out, "out", "", "JSON report path, outside the repo tree (default: a file under the OS temp dir)")
	cmd.Flags().StringArrayVar(&apply, "apply", nil, "item key from the report to raise a substrate story for (repeatable)")
	cmd.Flags().StringVar(&from, "from", "", "apply from this saved JSON report instead of judging again")
	cmd.Flags().StringVar(&server, "server", "", "Hosted server URL (overrides the configured machine hosted server).")
	return cmd
}

// substrateReportPath is where the JSON report goes: --out, which must lie
// outside the repo tree, else a timestamped file under the OS temp dir.
func substrateReportPath(repoRoot, out string) (string, error) {
	if out == "" {
		return filepath.Join(os.TempDir(), fmt.Sprintf("satelle-substrate-review-%s.json", time.Now().UTC().Format("20060102T150405Z"))), nil
	}
	abs, err := filepath.Abs(out)
	if err != nil {
		return "", err
	}
	if root, err := filepath.Abs(repoRoot); err == nil {
		if rel, err := filepath.Rel(root, abs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("--out %s is inside the repo tree: substrate review changes no file in the tree — write the report outside it", out)
		}
	}
	return abs, nil
}

// runSubstrateReview gathers the inventory, its evidence and the rules, judges
// each area batch, and joins them into the report. A batch whose judge fails or
// whose answer does not parse keeps the raw answer at
// <reportPath>.raw-<area>[-<part>].txt and its items read not judged; the other
// batches are reported all the same.
func runSubstrateReview(cmd *cobra.Command, a *app.App, binding, reportPath string, run *substrateReviewRun) (substratereview.Report, error) {
	ctx := cmd.Context()
	in, bound, hist, err := gatherSubstrateReview(ctx, a)
	if err != nil {
		return substratereview.Report{}, err
	}
	if err := run.stage(ctx, "gather", map[string]any{"items": len(in.Items)}); err != nil {
		return substratereview.Report{}, err
	}
	judge, err := newSubstrateJudge(cmd, binding, hist)
	if err != nil {
		return substratereview.Report{}, err
	}
	label := binding
	if label == "" {
		label = fmt.Sprintf("orchestrator:%s/%s", hist.OrchestratorAgent, hist.Orchestrator)
	}
	batches := substratereview.Batches(in)
	var judged []substratereview.Judgement
	var failures []substratereview.BatchFailure
	for i, b := range batches {
		j, raw, err := judge(ctx, b)
		outcome := "judged"
		if err != nil {
			outcome = "no judgement"
		}
		if serr := run.stage(ctx, "judge", map[string]any{"batch": fmt.Sprintf("%d/%d", i+1, len(batches)), "area": b.Area, "outcome": outcome}); serr != nil {
			return substratereview.Report{}, serr
		}
		if err == nil {
			judged = append(judged, j)
			continue
		}
		f := substratereview.BatchFailure{Area: b.Area, Chunk: b.Chunk, Err: err.Error()}
		rawPath := fmt.Sprintf("%s.raw-%s.txt", reportPath, f.Label())
		if werr := os.WriteFile(rawPath, []byte(raw), 0o644); werr != nil {
			f.Err += "; the raw answer could not be kept: " + werr.Error()
		} else {
			f.RawPath = rawPath
		}
		failures = append(failures, f)
	}
	rep := substratereview.Build(in, substratereview.Merge(judged...))
	rep.GeneratedAt, rep.Binding, rep.SeatModels = time.Now().UTC(), label, bound
	rep.Failures, rep.Batches = failures, len(batches)
	return rep, nil
}

// gatherSubstrateReview resolves everything the judges point at from the repo:
// the effective documents and tasks and their origins, the derived routes, the
// agents layer, every story's ledger rows, the constitution and the session
// set. It writes each item's history (and the text of an item with no file,
// and the current default of an edited copy) under a scratch directory outside
// the tree, so the manifests carry paths, never content.
func gatherSubstrateReview(ctx context.Context, a *app.App) (substratereview.JudgeInput, map[string]string, substratereview.History, error) {
	var none substratereview.History
	idx := a.Store.DocIndex
	rows, err := substrate.List(ctx, idx)
	if err != nil {
		return substratereview.JudgeInput{}, nil, none, err
	}
	tasks, err := substrate.ListTasks(filepath.Join(a.ProcessDataDir, "tasks"))
	if err != nil {
		return substratereview.JudgeInput{}, nil, none, err
	}
	wfs, err := idx.List(ctx, "workflows")
	if err != nil {
		return substratereview.JudgeInput{}, nil, none, err
	}
	var routes []wfroute.Route
	if rs := wfgovern.RouteSourceOf(wfs); rs.Present() {
		for _, cat := range wfgovern.RouteCategories(rs.Done) {
			d, err := wfgovern.RouteSpecFor(rs, cat, nil)
			if err != nil {
				return substratereview.JudgeInput{}, nil, none, err
			}
			routes = append(routes, wfroute.Build(d.Spec, wfgovern.DerivedRouteName, nil, d.Advisors, d.Reworks))
		}
	}
	eff, err := requireAgents(a)
	if err != nil {
		return substratereview.JudgeInput{}, nil, none, err
	}
	names := []string{"executor", "reviewer"}
	for n := range eff.Agents.Agents {
		names = append(names, n)
	}
	sort.Strings(names[2:])
	bound := map[string]string{}
	var agents []substratereview.Agent
	for _, n := range names {
		b, _ := eff.Agents.RawBinding(n)
		var srcs []string
		for _, f := range eff.Provenance.Fields(n) {
			srcs = append(srcs, eff.Provenance.Source(n, f))
		}
		bound[n] = b.Model
		agents = append(agents, substratereview.Agent{Name: n, Model: b.Model, Source: strings.Join(srcs, "+")})
	}
	_, agentsTeam := config.ProjectTierAgentsPath(a.PlaneDir())
	items := substratereview.Inventory(substratereview.InventoryInput{
		Docs: rows, TierRoot: a.AuthoredDirs().Tier, Workflow: wfgovern.DerivedRouteName,
		Routes: routes, AgentsTeam: agentsTeam, Agents: agents, Tasks: tasks,
	})

	entries := map[string][]ledger.Entry{}
	ids, err := a.Store.Ledger.StoryIDs(ctx)
	if err != nil {
		return substratereview.JudgeInput{}, nil, none, err
	}
	for _, id := range ids {
		es, err := a.Store.Ledger.ListByStory(ctx, id, "")
		if err != nil {
			return substratereview.JudgeInput{}, nil, none, err
		}
		entries[id] = es
	}
	hist := substratereview.Fold(entries, substratereview.EvidenceOptions{ParkStates: substratereview.ParkStates(routes), Bound: bound})

	var session []string
	for _, r := range rows {
		if r.Kind != "principles" {
			continue
		}
		if d, err := idx.Get(ctx, "principles", r.Name); err == nil && docHasTag(d.Body, sessionTag) {
			session = append(session, r.Name)
		}
	}
	constPath := a.PlaneConstitution()
	constitution, _ := os.ReadFile(constPath)

	in := substratereview.JudgeInput{
		Rules:             substratereview.Rules(string(constitution), session),
		ConstitutionPath:  constPath,
		OrchestratorModel: hist.Orchestrator,
	}
	for _, it := range items {
		in.Items = append(in.Items, substratereview.JudgeItem{Item: it, Evidence: hist.For(it.Selector, it.Seats)})
	}
	scratch, err := os.MkdirTemp(substrateScratchRoot, "satelle-substrate-review-")
	if err != nil {
		return substratereview.JudgeInput{}, nil, none, err
	}
	if err := substratereview.Materialise(scratch, &in, substrateBodies(ctx, idx, eff.Agents)); err != nil {
		return substratereview.JudgeInput{}, nil, none, err
	}
	return in, bound, hist, nil
}

// substrateScratchRoot is where a review's scratch directory is made: "" is the
// OS temp dir. A seam so tests keep it inside their own temp dir.
var substrateScratchRoot = ""

// substrateBodies resolves the text Materialise writes: an item's effective
// text when it has no file (the docindex serves the embedded fallback; an agent
// seat is its effective binding as TOML), and the current embedded default of
// an edited or seeded copy.
func substrateBodies(ctx context.Context, idx *docindex.Store, agents config.AgentsConfig) substratereview.Bodies {
	return substratereview.Bodies{
		Text: func(it substratereview.Item) (string, string, bool) {
			if it.Kind == substratereview.KindAgent {
				b, ok := agents.RawBinding(it.Name)
				if !ok {
					return "", "", false
				}
				var buf strings.Builder
				if err := toml.NewEncoder(&buf).Encode(map[string]config.AgentBinding{it.Name: b}); err != nil {
					return "", "", false
				}
				return buf.String(), it.Name + ".toml", true
			}
			kind, name := substrateDocOf(it)
			if d, err := idx.Get(ctx, kind, name); err == nil && d.Body != "" {
				return d.Body, name + config.EmbeddedExt(kind, name), true
			}
			// Tasks are not in the doc index: an embedded one is its default.
			return embeddedDefaultOf(kind, name)
		},
		Default: func(it substratereview.Item) (string, string, bool) {
			return embeddedDefaultOf(substrateDocOf(it))
		},
	}
}

// embeddedDefaultOf is the binary's default document kind/name and the file
// name to write it under.
func embeddedDefaultOf(kind, name string) (string, string, bool) {
	for _, d := range config.EmbeddedDefaults() {
		if d.Kind == kind && d.Name == name {
			return d.Body, name + d.Ext, true
		}
	}
	return "", "", false
}

// substrateDocOf is the document kind and name an item's text lives in: a
// workflow step or gate is authored in the step catalogue.
func substrateDocOf(it substratereview.Item) (kind, name string) {
	switch it.Kind {
	case substratereview.KindStep, substratereview.KindGate:
		return "workflows", "step"
	}
	kind, _, _ = strings.Cut(it.Key, "/")
	return kind, it.Name
}

// applySubstrateCuts raises one substrate story per chosen item through the
// same create verb `satelle story create` uses, refusing (and raising nothing
// for) an item the report does not hold or one this repo cannot cut; team says
// whether this repo holds a team-owned item (substrateHolding). It edits no
// file. Any refusal makes the run fail after the rest are raised.
func applySubstrateCuts(ctx context.Context, out io.Writer, rep substratereview.Report, keys []string, team func(substratereview.ReportItem) substratereview.TeamHolding) error {
	var refused []string
	for _, key := range keys {
		it, ok := rep.Find(key)
		if !ok {
			refused = append(refused, fmt.Sprintf("%s: not an item of this report — `satelle substrate review` lists the item keys", key))
			continue
		}
		if why := substratereview.Refusal(it, team); why != "" {
			refused = append(refused, why)
			continue
		}
		title, body, criteria := substratereview.StoryFor(it)
		tags := []string{substratereview.ApplyTag}
		if rep.ExecutionID != "" {
			// The run the cut came from, as a relation tag (sty_c356ed4f).
			tags = append(tags, "execution:"+rep.ExecutionID)
			body = strings.TrimRight(body, "\n") + "\n\nFrom execution " + rep.ExecutionID + " of " + substrateReviewTask + ".\n"
		}
		req, _ := json.Marshal(map[string]any{
			"title": title, "body": body, "acceptance_criteria": criteria,
			"category": substratereview.ApplyCategory, "tags": tags,
		})
		resp, err := verb.Dispatch(ctx, "story-create", req)
		if err != nil {
			return fmt.Errorf("apply %s: %w", key, err)
		}
		var created workitem.Item
		_ = json.Unmarshal(resp, &created)
		fmt.Fprintf(out, "raised %s for %s (%s, origin %s)\n", created.ID, key, it.Disposition, it.Origin)
	}
	for _, r := range refused {
		fmt.Fprintf(out, "refused %s\n", r)
	}
	if len(refused) > 0 {
		return fmt.Errorf("substrate review --apply: %d item(s) refused", len(refused))
	}
	return nil
}
