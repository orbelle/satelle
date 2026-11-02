package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/agentstep"
	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/reviewscore"
	"github.com/bobmcallan/satelle/internal/substratereview"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// newReviewJudge builds the judge `review score` runs a binding through, and the
// adapter name it reports. It is a seam so tests score with a stub binding and
// never start a model; the default is agentstep.CaseJudge, which goes through
// Engine.Gate — the isolated reviewer path a real gate uses.
var newReviewJudge = func(cmd *cobra.Command, binding string) (reviewscore.Judge, string, error) {
	a, err := appFrom(cmd)
	if err != nil {
		return nil, "", err
	}
	j, err := newCaseJudge(a, binding, nil)
	if err != nil {
		return nil, "", err
	}
	return j, j.Adapter(), nil
}

// caseJudgeRunner is the runner seam of every case judge: nil starts the
// binding's real agent CLI; a test captures the assembled request instead.
var caseJudgeRunner func(iface, command string) (agentcli.Runner, error)

// newCaseJudge builds the isolated reviewer path a real gate uses for one
// agents.toml binding. `review score` and
// `substrate review` both judge through it. resolve overrides how a binding
// name resolves (substrate review's default judge re-points the reviewer seat);
// nil reads agents.toml.
func newCaseJudge(a *app.App, binding string, resolve func(string) (config.AgentBinding, bool)) (*agentstep.CaseJudge, error) {
	opts, err := caseJudgeOptions(a, binding, resolve)
	if err != nil {
		return nil, err
	}
	return agentstep.NewCaseJudge(opts)
}

// caseJudgeOptions are newCaseJudge's options, for a caller that sets more of
// them before building the judge.
func caseJudgeOptions(a *app.App, binding string, resolve func(string) (config.AgentBinding, bool)) (agentstep.CaseJudgeOptions, error) {
	if resolve == nil {
		eff, err := requireAgents(a)
		if err != nil {
			return agentstep.CaseJudgeOptions{}, err
		}
		resolve = func(name string) (config.AgentBinding, bool) {
			b, ok := eff.Agents.RawBinding(name)
			if !ok {
				return config.AgentBinding{}, false
			}
			return eff.Agents.EffectiveBinding(b, config.UseOneShot), true
		}
	}
	return agentstep.CaseJudgeOptions{
		Root:      a.RepoRoot,
		Binding:   binding,
		Docs:      a.Store.DocIndex,
		Offload:   diffOffloader(a),
		Resolve:   resolve,
		NewRunner: caseJudgeRunner,
	}, nil
}

func init() {
	review := &cobra.Command{Use: "review", Short: "Score a reviewer binding on known cases",
		Long: `Measure review quality, not rejection counts.

Run a named agents.toml reviewer binding over cases whose right verdict is known
- the frozen corpus and replays captured from recorded gate rows - and compare
two bindings' reports. Scoring writes no ledger rows and changes no story.`}

	var sBinding, sCorpus, sReplay, sOut string
	var sRuns, sWorkers int
	score := &cobra.Command{
		Use:   "score",
		Short: "Run a reviewer binding over a corpus and/or replay cases and report its quality",
		Long: `Judge every case with --binding through the isolated reviewer path a real gate
uses, then print per rubric and in total: recall (defects rejected), false
blockers (valid cases rejected), escapes (defects accepted), finding match (the
notes cite the known defect, or unscored when the case declares no
defect_markers), wall time, and tokens and cost or the adapter-named reason they
are unavailable. The JSON report goes to --out.

--corpus is a directory of rubric/case/{case.json,change.diff}; --replay a
directory written by review capture. Real judgements spend real money: runs
and workers default to 1.`,
		Annotations: needsStore(),
		Args:        cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if sBinding == "" {
				return fmt.Errorf("--binding is required: the agents.toml section that judges")
			}
			if sCorpus == "" && sReplay == "" {
				return fmt.Errorf("give --corpus <dir> and/or --replay <dir>: there is nothing to score")
			}
			var cases []reviewscore.Case
			if sCorpus != "" {
				cs, err := reviewscore.Load(sCorpus)
				if err != nil {
					return err
				}
				cases = append(cases, cs...)
			}
			if sReplay != "" {
				cs, err := reviewscore.LoadReplay(sReplay)
				if err != nil {
					return err
				}
				cases = append(cases, cs...)
			}
			if len(cases) == 0 {
				return fmt.Errorf("no cases found under the given directories")
			}
			judge, adapter, err := newReviewJudge(cmd, sBinding)
			if err != nil {
				return err
			}
			rep := reviewscore.Score(cmd.Context(), cases, judge, reviewscore.Options{
				Binding: sBinding, Adapter: adapter, Runs: sRuns, Workers: sWorkers,
				Dispositions: substratereview.Dispositions,
			})
			out := sOut
			if out == "" {
				out = filepath.Join(os.TempDir(), fmt.Sprintf("satelle-review-score-%s-%s.json", sBinding, time.Now().UTC().Format("20060102T150405Z")))
			}
			if err := rep.WriteJSON(out); err != nil {
				return err
			}
			rep.Render(cmd.OutOrStdout())
			fmt.Fprintf(cmd.OutOrStdout(), "report: %s\n", out)
			return nil
		},
	}
	score.Flags().StringVar(&sBinding, "binding", "", "agents.toml section that judges (required)")
	score.Flags().StringVar(&sCorpus, "corpus", "", "directory of corpus cases (rubric/case/{case.json,change.diff})")
	score.Flags().StringVar(&sReplay, "replay", "", "directory of replay cases written by review capture")
	score.Flags().StringVar(&sOut, "out", "", "JSON report path (default: a file under the OS temp dir)")
	score.Flags().IntVar(&sRuns, "runs", 1, "times each case is judged")
	score.Flags().IntVar(&sWorkers, "workers", 1, "concurrent judgements")

	var cLedger, cExpect, cOut, cRubric, cPatch string
	var cNoPatch bool
	var cMarkers, cDocs []string
	capture := &cobra.Command{
		Use:   "capture <story-id>",
		Short: "Turn one recorded review row into a replay case",
		Long: `Write a replay case from a review row of <story-id>: the edge, skill, original
notes, the definition as it stood then, the documents, the verdict YOU judge it
should have had under the skill as it stands today (--expect) and optional
--defect-marker phrases.

The ledger keeps only the payload size, so the case is marked reconstructed and
the reviewed material is supplied: --patch <file> (or --no-patch when the edge
judged no code), and --doc name=<file> for a document re-attached since. Any gap
leaves the case unfaithful: written, but review score does not judge it.`,
		Annotations: needsStore(),
		Args:        cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			in, err := gatherCapture(cmd, args[0])
			if err != nil {
				return err
			}
			in.LedgerID, in.Expect, in.OutDir, in.Rubric = cLedger, reviewscore.Verdict(cExpect), cOut, cRubric
			in.Markers, in.NoPatch = cMarkers, cNoPatch
			if cPatch != "" {
				b, err := os.ReadFile(cPatch)
				if err != nil {
					return fmt.Errorf("read --patch: %w", err)
				}
				if strings.TrimSpace(string(b)) == "" {
					return fmt.Errorf("--patch %s is empty: use --no-patch when the edge judged no code", cPatch)
				}
				in.Patch = string(b)
			}
			for _, spec := range cDocs {
				name, file, ok := strings.Cut(spec, "=")
				if !ok || name == "" || file == "" {
					return fmt.Errorf("--doc %q: want name=<file>", spec)
				}
				b, err := os.ReadFile(file)
				if err != nil {
					return fmt.Errorf("read --doc %s: %w", name, err)
				}
				doc := reviewscore.ReplayDoc{Name: name, Type: "document", Body: string(b)}
				for _, d := range in.Docs {
					if d.Name == name {
						doc.Type = d.Type
					}
				}
				in.ReviewedDocs = append(in.ReviewedDocs, doc)
			}
			dir, err := reviewscore.Capture(in)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "replay case written: %s\n", dir)
			if gaps := reviewscore.CaseGaps(dir); len(gaps) > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "UNFAITHFUL (review score will not judge it): %s\n", strings.Join(gaps, "; "))
			}
			return nil
		},
	}
	capture.Flags().StringVar(&cLedger, "ledger", "", "id of the review_accept/review_reject ledger row (required)")
	capture.Flags().StringVar(&cExpect, "expect", "", "the human-judged verdict: accept or reject (required)")
	capture.Flags().StringVar(&cOut, "out", "", "replay directory to write into (required)")
	capture.Flags().StringVar(&cRubric, "rubric", "", "rubric the case is reported under (default: the reviewer skill)")
	capture.Flags().StringArrayVar(&cMarkers, "defect-marker", nil, "phrase a reviewer's notes cite when it names the known defect (repeatable)")
	capture.Flags().StringVar(&cPatch, "patch", "", "file holding the change the reviewer was shown (e.g. a git diff of the reviewed range)")
	capture.Flags().BoolVar(&cNoPatch, "no-patch", false, "the edge judged no code, so there is no change to replay")
	capture.Flags().StringArrayVar(&cDocs, "doc", nil, "name=<file>: an attached document as it stood at the review (repeatable)")
	_ = capture.MarkFlagRequired("ledger")
	_ = capture.MarkFlagRequired("expect")
	_ = capture.MarkFlagRequired("out")

	compare := &cobra.Command{
		Use:   "compare <report-a.json> <report-b.json>",
		Short: "Print two score reports side by side",
		Long: `Print two review score reports per rubric and in total, one column per binding
with the delta. It refuses reports that were not scored over the same case set
(or the same number of runs), because every figure is then about different work.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := reviewscore.ReadReport(args[0])
			if err != nil {
				return err
			}
			b, err := reviewscore.ReadReport(args[1])
			if err != nil {
				return err
			}
			cmp, err := reviewscore.Compare(a, b)
			if err != nil {
				return err
			}
			cmp.Render(cmd.OutOrStdout())
			return nil
		},
	}

	review.AddCommand(score, capture, compare)
	register(review)
}

// ledgerPage is the most rows the store returns in one list.
const ledgerPage = 2000

// gatherCapture reads a story's definition, ledger rows and documents through
// the verbs, so capture sees exactly what the CLI reads elsewhere.
func gatherCapture(cmd *cobra.Command, storyID string) (reviewscore.CaptureInput, error) {
	ctx := cmd.Context()
	call := func(name string, req, into any) error {
		b, err := json.Marshal(req)
		if err != nil {
			return err
		}
		out, err := verb.Dispatch(ctx, name, b)
		if err != nil {
			return err
		}
		return json.Unmarshal(out, into)
	}
	var it workitem.Item
	if err := call("story-get", map[string]any{"id": storyID}, &it); err != nil {
		return reviewscore.CaptureInput{}, err
	}
	// The store returns at most 2000 rows oldest first, so a long story is paged:
	// the rows after the review are the ones that show what changed since.
	var entries []ledger.Entry
	for after := ""; ; {
		var page []ledger.Entry
		req := map[string]any{"story_id": storyID, "limit": ledgerPage, "oldest": true}
		if after != "" {
			req["after_id"] = after
		}
		if err := call("ledger-list", req, &page); err != nil {
			return reviewscore.CaptureInput{}, err
		}
		entries = append(entries, page...)
		if len(page) < ledgerPage {
			break
		}
		after = page[len(page)-1].ID
	}
	var refs []struct {
		Name   string `json:"name"`
		Type   string `json:"type"`
		Binary bool   `json:"binary"`
	}
	if err := call("story-doc-list", map[string]any{"story_id": storyID}, &refs); err != nil {
		return reviewscore.CaptureInput{}, err
	}
	var docs []reviewscore.ReplayDoc
	for _, r := range refs {
		// The same attachments a gate payload inlines: a binary or a recorded
		// change patch never rides in it.
		if r.Binary || strings.EqualFold(r.Type, "change") {
			continue
		}
		var d struct {
			Body string `json:"body"`
		}
		if err := call("story-doc-get", map[string]any{"story_id": storyID, "name": r.Name}, &d); err != nil {
			return reviewscore.CaptureInput{}, err
		}
		docs = append(docs, reviewscore.ReplayDoc{Name: r.Name, Type: r.Type, Body: d.Body})
	}
	return reviewscore.CaptureInput{
		Story: reviewscore.StoryDefinition{
			ID: it.ID, Title: it.Title, Body: it.Body, AcceptanceCriteria: it.AcceptanceCriteria,
			Category: it.Category, Tags: it.Tags,
		},
		Entries: entries,
		Docs:    docs,
	}, nil
}
