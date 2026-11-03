package cli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/costview"
	"github.com/bobmcallan/satelle/internal/epicset"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/storyfold"
	"github.com/bobmcallan/satelle/internal/verb"
)

// Story and task are the same primitive; their command groups are built by one
// factory so the surface stays identical. Verb names follow the architecture's
// standard: list/get (read) + create/set (write), all kebab-case.
func init() {
	register(workItemGroup("story", "stories", "Manage stories (units of work / goals)"))
	register(workItemGroup("task", "tasks", "Manage tasks (project-level to-dos)"))
	// An execution is an isolated RUN of a task (create with --parent <tsk_id>);
	// it carries the run lifecycle while the task header stays a stable definition
	// (sty_ef08ce2a).
	register(workItemGroup("execution", "executions", "Manage task executions (isolated runs of a task)"))
}

// workItemGroup builds a `satelle <group>` command with create/get/list/set
// subcommands dispatching to the <group>-* verbs. plural is used only in help
// text (e.g. "List stories").
func workItemGroup(group, plural, short string) *cobra.Command {
	parent := &cobra.Command{
		Use: group, Short: short, Long: groupParentLong(group),
		// A group with no subcommand shows its help; an unknown subcommand (a
		// removed one such as `story chat`) is refused rather than silently
		// printing help and exiting 0 (sty_6f9ba7ca).
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			return fmt.Errorf("unknown command %q for %q", args[0], cmd.CommandPath())
		},
	}

	// create
	var cTitle, cBody, cStatus, cPriority, cCategory, cParent, cAccept string
	var cTags, cInputs []string
	create := &cobra.Command{
		Use:         "create",
		Short:       "Create a " + group,
		Long:        groupCreateLong(group),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := map[string]any{"title": cTitle}
			putIf(req, "body", cBody)
			putIf(req, "status", cStatus)
			putIf(req, "priority", cPriority)
			putIf(req, "category", cCategory)
			putIf(req, "parent_id", cParent)
			putIf(req, "acceptance_criteria", cAccept)
			if len(cTags) > 0 {
				req["tags"] = cTags
			}
			if len(cInputs) > 0 {
				inputs := make(map[string]string, len(cInputs))
				for _, kv := range cInputs {
					name, val, ok := strings.Cut(kv, "=")
					if !ok {
						return fmt.Errorf("--input %q must be name=value", kv)
					}
					inputs[name] = val
				}
				req["inputs"] = inputs
			}
			if err := dispatch(cmd, group+"-create", req); err != nil {
				return err
			}
			// Story-only: regenerate the disposable backlog view after a successful
			// create so CLI freshness does not depend on serve (sty_d0950127).
			if group == "story" {
				refreshStoryBacklog(cmd)
			}
			// Surface the pre-seed ungated state (sty_d4d0ee59): when gate_create
			// is neither on nor explicitly opted out, tell the operator on the
			// create path itself. Never fail a successful create over the advisory.
			// Category warn-mode advisory (sty_b2315e17) rides the same channel.
			if a, aerr := appFrom(cmd); aerr == nil {
				if notice := createGateNotice(a.ProcessConfig.Review.GateCreate, a.ProcessDataDir); notice != "" {
					fmt.Fprint(cmd.ErrOrStderr(), notice)
				}
				if notice := categoryNotice(a.Config, cCategory); notice != "" {
					fmt.Fprint(cmd.ErrOrStderr(), notice)
				}
			}
			return nil
		},
	}
	create.Flags().StringVar(&cTitle, "title", "", "title (required)")
	create.Flags().StringVar(&cBody, "body", "", "body / description")
	create.Flags().StringVar(&cStatus, "status", "", "status (default backlog)")
	create.Flags().StringVar(&cPriority, "priority", "", "priority")
	create.Flags().StringVar(&cCategory, "category", "", "category")
	create.Flags().StringVar(&cParent, "parent", "", "parent item id")
	create.Flags().StringVar(&cAccept, "acceptance", "", "acceptance criteria")
	create.Flags().StringSliceVar(&cTags, "tags", nil, "comma-separated tags")
	if group == "execution" {
		create.Flags().StringArrayVar(&cInputs, "input", nil, "name=value for an input the task declares (repeatable)")
	}
	_ = create.MarkFlagRequired("title")

	// get
	get := &cobra.Command{
		Use:         "get <id>",
		Short:       "Get a " + group + " by id",
		Long:        groupGetLong(group),
		Args:        cobra.ExactArgs(1),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := map[string]any{"id": args[0]}
			if group == "story" {
				fetchBeforeRead(cmd, args[0])
				return dispatchWithWorking(cmd, group+"-get", req)
			}
			return dispatch(cmd, group+"-get", req)
		},
	}

	// list
	var lStatus, lParent, lTag string
	var lLimit int
	var lArchived bool
	list := &cobra.Command{
		Use:         "list",
		Short:       "List " + plural,
		Long:        groupListLong(group, plural),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := map[string]any{}
			putIf(req, "status", lStatus)
			putIf(req, "parent_id", lParent)
			putIf(req, "tag", lTag)
			if lLimit > 0 {
				req["limit"] = lLimit
			}
			if lArchived {
				req["archived"] = true
			}
			if group == "story" {
				return dispatchWithWorking(cmd, group+"-list", req)
			}
			return dispatch(cmd, group+"-list", req)
		},
	}
	list.Flags().StringVar(&lStatus, "status", "", "filter by status")
	list.Flags().StringVar(&lParent, "parent", "", "filter by parent id")
	list.Flags().StringVar(&lTag, "tag", "", "filter by exact tag (ANY-match in multi-value namespaces)")
	list.Flags().IntVar(&lLimit, "limit", 0, "max rows (default 500)")
	list.Flags().BoolVar(&lArchived, "archived", false, "list only archived "+plural+" (the default list hides them)")

	// set (partial update — only flags the user changed are sent)
	set := &cobra.Command{
		Use:         "set <id>",
		Short:       "Update a " + group + " (only the flags you pass change)",
		Long:        groupSetLong(group),
		Args:        cobra.ExactArgs(1),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := map[string]any{"id": args[0]}
			f := cmd.Flags()
			// Step-edge self-report nudge (sty_56aae77a AC3): capture previous
			// status before the transition so the advisory names the step just left.
			var prevStatus string
			if group == "story" && f.Changed("status") {
				if a, aerr := appFrom(cmd); aerr == nil && a != nil {
					if it, gerr := a.Store.Stories.Get(cmd.Context(), args[0]); gerr == nil {
						prevStatus = it.Status
					}
				}
			}
			putChanged(req, f, "title", "title")
			putChanged(req, f, "body", "body")
			putChanged(req, f, "status", "status")
			putChanged(req, f, "priority", "priority")
			putChanged(req, f, "category", "category")
			putChanged(req, f, "parent", "parent_id")
			putChanged(req, f, "acceptance", "acceptance_criteria")
			if f.Changed("tags") {
				tags, _ := f.GetStringSlice("tags")
				req["tags"] = tags
			}
			// Additive tag mutation (sty_033d4611): never clobbers the rest.
			// Combined with --tags (full replace) is rejected by the verb.
			if f.Changed("add-tags") {
				add, _ := f.GetStringSlice("add-tags")
				req["add_tags"] = add
			}
			if f.Changed("remove-tags") {
				rm, _ := f.GetStringSlice("remove-tags")
				req["remove_tags"] = rm
			}
			resp, err := dispatchResponse(cmd, group+"-set", req)
			if err != nil {
				return err
			}
			// Story-only: keep the backlog view current after set (sty_d0950127).
			if group == "story" {
				refreshStoryBacklog(cmd)
				// On stderr, like the other transition-time notices, so the record
				// on stdout stays parseable.
				emitAdvice(cmd, cmd.ErrOrStderr(), setAdvice(resp))
			}
			// Entering a performing state is the point a developer is about to
			// work: say so when their substrate is not in step with the hosted
			// copy (sty_7562055c). Advisory only.
			if group == "story" && f.Changed("status") {
				substrateEngagementAdvisory(cmd, args[0])
			}
			// Category warn-mode advisory on an explicit --category change only
			// (sty_b2315e17): a status-only set on a legacy category never trips it.
			if f.Changed("category") {
				if a, aerr := appFrom(cmd); aerr == nil {
					cat, _ := f.GetString("category")
					if notice := categoryNotice(a.Config, cat); notice != "" {
						fmt.Fprint(cmd.ErrOrStderr(), notice)
					}
				}
			}
			// Step-edge self-report nudge after a real status change (sty_56aae77a).
			// No hardcoded terminal-status list — suppress only when status did not
			// change or a self-report for the left step already exists.
			if group == "story" && f.Changed("status") && prevStatus != "" {
				newStatus, _ := f.GetString("status")
				if newStatus != "" && newStatus != prevStatus {
					already := verb.HasStepSelfReport(cmd.Context(), args[0], prevStatus)
					if note := stepSelfReportNudge(args[0], prevStatus, already); note != "" {
						fmt.Fprint(cmd.ErrOrStderr(), note)
					}
				}
			}
			return nil
		},
	}
	set.Flags().String("title", "", "new title")
	set.Flags().String("body", "", "new body")
	set.Flags().String("status", "", "new status")
	set.Flags().String("priority", "", "new priority")
	set.Flags().String("category", "", "new category")
	set.Flags().String("parent", "", "new parent id")
	set.Flags().String("acceptance", "", "new acceptance criteria")
	set.Flags().StringSlice("tags", nil, "replace entire tag set (comma-separated); exclusive of --add-tags/--remove-tags")
	set.Flags().StringSlice("add-tags", nil, "add tags without dropping existing ones (comma-separated)")
	set.Flags().StringSlice("remove-tags", nil, "remove tags: exact match, or namespace group (sprint: / sprint:*)")

	parent.AddCommand(create, get, list, set)
	if group == "story" {
		parent.AddCommand(storyDocCommands()...)
		parent.AddCommand(storyCostCommands()...)
		parent.AddCommand(storyDiffCommand())
		parent.AddCommand(storyWaveCommand())
		parent.AddCommand(storyReadyCommand())
		parent.AddCommand(storyProofCommand())
		parent.AddCommand(storyDefinitionEditsCommand())
		parent.AddCommand(storySyncCommand())
		parent.AddCommand(storyReconcileCommand())
		parent.AddCommand(storyRestampCommand())
		parent.AddCommand(storyAmendCommand())
		parent.AddCommand(storyStopRequestCommand())
		parent.AddCommand(storyQueueCommand())
		parent.AddCommand(storyWorktreeCommand())
		parent.AddCommand(storyRecoverCommand())
		parent.AddCommand(storySeatCommands()...)
		parent.AddCommand(storyTidyCommands()...)
		parent.AddCommand(storyMessageCommand())
		parent.AddCommand(storyMessagesCommand())
		parent.AddCommand(storyMessageMarkCommand())
		parent.AddCommand(storySessionCommand())
		parent.AddCommand(storyReworkCommand())
		parent.AddCommand(storyWaiveCommand())
		parent.AddCommand(storyReleaseRecordCommand())
		parent.AddCommand(storyDriveCommand())
		parent.AddCommand(storyArchiveCommand(), storyUnarchiveCommand())
	}
	if group == "task" {
		// tasks are authored substrate → `satelle task validate` runs the
		// deterministic task structure check (ACTION+VERIFICATION contract).
		parent.AddCommand(authoredValidateCmd("tasks"))
		parent.AddCommand(taskArchiveCommand())
		parent.AddCommand(taskTriggerCommand())
	}
	if group == "execution" {
		parent.AddCommand(executionRecordCommand())
	}
	return parent
}

// refreshStoryBacklog best-effort regenerates the disposable story-backlog
// view after a story create/set. Synchronous — no goroutine or ticker — so the
// CLI stays self-sufficient without serve (sty_d0950127). A failure never
// fails the mutation command (the DB write already committed); warn on stderr.
func refreshStoryBacklog(cmd *cobra.Command) {
	a, err := appFrom(cmd)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "story: backlog refresh: %v\n", err)
		return
	}
	if _, _, err := verb.SyncStoryBacklog(cmd.Context(), a.Store.Stories, time.Now()); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "story: backlog refresh: %v\n", err)
	}
}

// executionRecordCommand builds `satelle execution record <exe_id>` — the in-loop
// path for collecting a run's OUTPUT as an OKF doc under the parent task's folder
// (sty_890b86cb). The orchestrator calls it as the run's final act (the task
// workflow's exit-gate rubric instructs it to). Output comes from --output or,
// when absent, stdin — so a run's captured log can be piped in.
func executionRecordCommand() *cobra.Command {
	var output, outcome string
	cmd := &cobra.Command{
		Use:   "record <exe_id>",
		Short: "Record a task execution's run output as an OKF doc under its task folder",
		Long: `Record what a run produced, as an authored document under the parent task's
folder — the evidence half of an execution.

--output takes the text; with the flag absent it reads STDIN, so a captured log
pipes straight in. Reach for it before closing the run: the after-validator
judges what the run did, and an unrecorded run leaves it nothing to read.

--outcome says how the run ended (accept, reject, performed or no-op) and is
written into the same document; a run that found nothing to do records no-op. A
record without --outcome keeps the outcome already on the document.`,
		Args:        cobra.ExactArgs(1),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := output
			if !cmd.Flags().Changed("output") {
				if data, err := io.ReadAll(cmd.InOrStdin()); err == nil {
					out = string(data)
				}
			}
			req := map[string]any{"id": args[0], "output": out}
			putIf(req, "outcome", outcome)
			return dispatch(cmd, "execution-record", req)
		},
	}
	cmd.Flags().StringVar(&output, "output", "", "run output text (default: read from stdin)")
	cmd.Flags().StringVar(&outcome, "outcome", "", "how the run ended: accept, reject, performed or no-op")
	return cmd
}

// taskArchiveCommand builds `satelle task archive <id>` — a task's disposal path
// (sty_cd209b8a): it marks the store record archived (excluded from the default
// task list, still readable via task get) and MOVES the header + executions to a
// mandatory timestamped backup. Archive is record disposition, distinct from the
// workflow status a task never runs through.
func taskArchiveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "archive <id>",
		Short: "Archive a task: move its files to backups and mark the record archived (excluded from list)",
		Long: `Retire a superseded task: the record is marked archived (dropped from the
default task list, still readable via task get) and its header plus executions
MOVE to a timestamped backup directory.

Archive is record DISPOSITION, not a workflow status — a task runs no lifecycle
to close. The files move rather than vanish, so a mistaken archive is
recoverable from the backup tree.`,
		Args:        cobra.ExactArgs(1),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			// The archive event builds on the task's hosted head (sty_0810c2a3).
			fetchChainBeforeWrite(cmd, args[0])
			return dispatch(cmd, "task-archive", map[string]any{"id": args[0]})
		},
	}
}

// storyArchiveCommand builds `satelle story archive <id>...` (sty_28665836): the
// way a story leaves the backlog without running its workflow's cancel route.
func storyArchiveCommand() *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "archive <id>...",
		Short: "Archive stories at their workflow's start state (hidden from the default list, no workflow run)",
		Long: `Archive stories: the record drops out of the default story list, the web
story board and the epic wave; story get still returns it.

Archive is record DISPOSITION, not a status. The story keeps its status and
chain; one story_updated event flips archived, with --reason and the actor, so
the flag travels with the chain through sync. No gate or reviewer runs.

Only a story at its workflow's start state may be archived, or a container
waiting on children while none of its members performs. Any other is refused
by id and status, and one refusal archives nothing. Naming an epic or such a
container also archives its start-state members; the rest are listed as skipped.
story unarchive reverses it; story list --archived shows what is archived.`,
		Args:        cobra.MinimumNArgs(1),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := map[string]any{"ids": args}
			putIf(req, "reason", reason)
			return dispatch(cmd, "story-archive", req)
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "why the stories are archived (recorded on the event)")
	return cmd
}

// storyUnarchiveCommand builds `satelle story unarchive <id>...`.
func storyUnarchiveCommand() *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "unarchive <id>...",
		Short: "Unarchive stories: they return to the default story list",
		Long: `Reverse story archive: a story_updated event changes archived from true to
false, with --reason and the actor, and the story reappears in the default list.
Status and chain are untouched. A story that is not archived is left as it is.`,
		Args:        cobra.MinimumNArgs(1),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := map[string]any{"ids": args}
			putIf(req, "reason", reason)
			return dispatch(cmd, "story-unarchive", req)
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "why the stories are unarchived (recorded on the event)")
	return cmd
}

// attachBody resolves the document body for `story attach`: --file reads it
// from a file (sty_97c53d72 — a multi-KB summary should not shell-quote through
// a flag), otherwise --body is used verbatim. The flags are declared mutually
// exclusive; a read failure is surfaced with the path context.
//
// `--file -` reads STDIN (sty_ef8a896b): piping evidence is what an agent
// reaches for, and the bare os.ReadFile failed on the conventional sentinel with
// a confusing "open -: no such file". in is the command's own reader
// (cmd.InOrStdin()), the same idiom `execution record` uses, so a test can
// inject one.
func attachBody(in io.Reader, body, file string) (string, error) {
	if file == "" {
		return body, nil
	}
	if file == "-" {
		data, err := io.ReadAll(in)
		if err != nil {
			return "", fmt.Errorf(`attach: read --file - (stdin): %w`, err)
		}
		return string(data), nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("attach: read --file: %w", err)
	}
	return string(data), nil
}

// attachBinary reads a local binary file, base64-encodes it, and dispatches
// story-doc-attach-binary. Cap, allowlist, and sniff live in the verb
// (sty_40e5a305); the CLI only defaults content-type from the extension when
// --content-type is omitted.
func attachBinary(cmd *cobra.Command, storyID, name, typ, path, contentType string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("attach: read --binary-file: %w", err)
	}
	if contentType == "" {
		// Convenience default only — verb still sniffs and cross-checks.
		if nameExt := filepath.Ext(name); nameExt != "" {
			contentType = mime.TypeByExtension(nameExt)
		}
		if contentType == "" {
			contentType = mime.TypeByExtension(filepath.Ext(path))
		}
	}
	req := map[string]any{
		"story_id":    storyID,
		"name":        name,
		"data_base64": base64.StdEncoding.EncodeToString(data),
	}
	putIf(req, "type", typ)
	putIf(req, "content_type", contentType)
	return dispatch(cmd, "story-doc-attach-binary", req)
}

// docWriteOut fetches a document and writes its body to path. Binary attachments
// are decoded from base64; markdown is written as the attachment body string.
// Never streams raw binary to stdout (sty_40e5a305 AC6).
func docWriteOut(cmd *cobra.Command, storyID, name, outPath string, force bool) error {
	if !force {
		if _, err := os.Stat(outPath); err == nil {
			return fmt.Errorf("doc: %s already exists (pass --force to overwrite)", outPath)
		} else if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("doc: stat --out: %w", err)
		}
	}
	// Probe list metadata via get-binary first when name looks binary; else markdown.
	// Prefer binary get when the name has a non-.md extension.
	ext := strings.ToLower(filepath.Ext(name))
	if ext != "" && ext != ".md" {
		return docWriteOutBinary(cmd, storyID, name, outPath)
	}
	// Markdown (or name without extension — try markdown get).
	var body json.RawMessage
	b, err := json.Marshal(map[string]any{"story_id": storyID, "name": name})
	if err != nil {
		return err
	}
	body, err = verb.Dispatch(cmd.Context(), "story-doc-get", b)
	if err != nil {
		// If markdown get failed because it is binary, fall through to binary path.
		if strings.Contains(err.Error(), "binary attachment") {
			return docWriteOutBinary(cmd, storyID, name, outPath)
		}
		return err
	}
	var ref struct {
		Body string `json:"body"`
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal(body, &ref); err != nil {
		return fmt.Errorf("doc: decode: %w", err)
	}
	if err := os.WriteFile(outPath, []byte(ref.Body), 0o644); err != nil {
		return fmt.Errorf("doc: write --out: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "wrote %s (%d bytes, type=%s)\n", outPath, len(ref.Body), ref.Type)
	return nil
}

func docWriteOutBinary(cmd *cobra.Command, storyID, name, outPath string) error {
	b, err := json.Marshal(map[string]any{"story_id": storyID, "name": name})
	if err != nil {
		return err
	}
	raw, err := verb.Dispatch(cmd.Context(), "story-doc-get-binary", b)
	if err != nil {
		return err
	}
	var ref struct {
		Name        string `json:"name"`
		ContentType string `json:"content_type"`
		Size        int64  `json:"size"`
		SHA256      string `json:"sha256"`
		DataB64     string `json:"data_base64"`
	}
	if err := json.Unmarshal(raw, &ref); err != nil {
		return fmt.Errorf("doc: decode binary: %w", err)
	}
	data, err := base64.StdEncoding.DecodeString(ref.DataB64)
	if err != nil {
		return fmt.Errorf("doc: decode base64: %w", err)
	}
	if err := os.WriteFile(outPath, data, 0o644); err != nil {
		return fmt.Errorf("doc: write --out: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "wrote %s (%d bytes, %s, sha256:%s)\n", outPath, len(data), ref.ContentType, ref.SHA256)
	return nil
}

// storySyncCommand builds `satelle story sync` (sty_8f7b2157): the dedicated,
// inspectable reconciliation of .satelle/stories — backlog-only views + an
// artifact review that REPORTS orphans/misfiles (never deletes evidence).
func storySyncCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "sync",
		Short: "Reconcile .satelle/stories: backlog-only views; review artifact dirs against the DB",
		Long: `Reconcile the on-disk .satelle/stories tree against the database: rematerialise
the backlog views and review the per-story artifact directories.

Reach for it when the views look stale or an artifact directory is unaccounted
for. It REPORTS orphans and problems and never deletes evidence — an ORPHANED
directory is authored material with no story row, and removing it is your call,
not this command's.`,
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := appFrom(cmd)
			if err != nil {
				return err
			}
			rep, err := verb.SyncStories(cmd.Context(), a.Store.Stories, time.Now())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "stories: %d backlog view(s) materialized; %d pruned; %d artifact dir(s)\n", rep.Materialized, rep.Pruned, rep.ArtifactDirs)
			for _, id := range rep.Orphaned {
				fmt.Fprintf(out, "  ORPHANED %s/ — no story in the database (authored evidence; remove manually if unwanted)\n", id)
			}
			for _, p := range rep.Problems {
				fmt.Fprintf(out, "  PROBLEM  %s\n", p)
			}
			if len(rep.Orphaned) == 0 && len(rep.Problems) == 0 {
				fmt.Fprintln(out, "artifacts: clean")
			}
			return nil
		},
	}
}

// storyReconcileCommand builds `satelle story reconcile [--repair] [--json]`.
// Default is a dry-run report of row/chain drift; --repair applies.
func storyReconcileCommand() *cobra.Command {
	var repair, asJSON bool
	cmd := &cobra.Command{
		Use:   "reconcile",
		Short: "Detect (and optionally repair) story rows that disagree with their ledger chain",
		Long: `Compare each story row's fields to the fold of its ledger chain. The chain is
the source of truth: a field on which they disagree is drift, and a drifted
story is not published by 'satelle sync workstate push' until repaired.

Default is a dry-run report that exits non-zero on drift. --repair rebuilds
each drifted row from the chain and records a status_reconcile event.

A transition whose from is not the status the chain had reached is listed as
stale: skipped when the story had already ended (done or cancelled), applied out
of order otherwise. It never fails reconcile; 'satelle ledger verify' reports
it as a stale_from break.`,
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := appFrom(cmd)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			stale, serr := verb.DetectStaleTransitions(ctx, a.Store.Stories, a.Store.Ledger)
			if serr != nil {
				return serr
			}
			if repair {
				repaired, rerr := verb.RepairDrift(ctx, a.Store.Stories, a.Store.Ledger, time.Now())
				if rerr != nil {
					return rerr
				}
				if asJSON {
					enc := json.NewEncoder(out)
					enc.SetIndent("", "  ")
					return enc.Encode(map[string]any{"repaired": repaired, "count": len(repaired), "stale_transitions": stale})
				}
				if len(repaired) == 0 {
					fmt.Fprintln(out, "status: clean — no row disagrees with its ledger chain")
				} else {
					fmt.Fprintf(out, "repaired %d field(s) from the ledger chain:\n", len(repaired))
					for _, d := range repaired {
						fmt.Fprintf(out, "  %s  %s: %q → %q\n", d.ID, d.Field, d.Row, d.Chain)
					}
				}
				printStaleTransitions(out, stale)
				return nil
			}
			drifts, derr := verb.DetectDrift(ctx, a.Store.Stories, a.Store.Ledger)
			if derr != nil {
				return derr
			}
			if asJSON {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				if err := enc.Encode(map[string]any{"drift": drifts, "count": len(drifts), "stale_transitions": stale}); err != nil {
					return err
				}
			} else if len(drifts) == 0 {
				fmt.Fprintln(out, "status: clean — no row disagrees with its ledger chain")
			} else {
				fmt.Fprintf(out, "%d field(s) disagree with the ledger chain:\n", len(drifts))
				for _, d := range drifts {
					fmt.Fprintf(out, "  %s  %s: row=%q chain=%q\n", d.ID, d.Field, d.Row, d.Chain)
				}
				fmt.Fprintln(out, "re-run with --repair to rebuild each row from the chain")
			}
			if !asJSON {
				printStaleTransitions(out, stale)
			}
			if len(drifts) > 0 {
				return fmt.Errorf("story reconcile: %d drifted", len(drifts))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&repair, "repair", false, "rebuild drifted rows from their ledger chain")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit machine-readable JSON")
	return cmd
}

// printStaleTransitions names each transition whose from was not the story's
// status at its place in the chain, and says whether the fold skipped it or
// applied it out of order. They are append-only facts of the chain that no repair
// removes, so they never fail reconcile: `satelle ledger verify` is the check
// that does.
func printStaleTransitions(out io.Writer, stale []storyfold.ReplayBreak) {
	if len(stale) == 0 {
		return
	}
	fmt.Fprintf(out, "%d stale transition(s) in the chain:\n", len(stale))
	for _, b := range stale {
		how := "applied out of order"
		if b.Skipped {
			how = "skipped"
		}
		fmt.Fprintf(out, "  %s  stale transition %s %s → %s %s (%s)\n", b.StoryID, b.EventID, b.From, b.To, how, b.Detail)
	}
}

// storyDiffCommand builds `satelle story diff [id]`: enumerate files changed
// since the engagement baseline (sty_da169e03). Enumeration only — no verdict.
// Id may be omitted when stdin is a transition payload `{story:{id},…}` so gate
// functional checks can invoke without shell id plumbing.
func storyDiffCommand() *cobra.Command {
	var patch, recorded, includeSubstrate, full bool
	cmd := &cobra.Command{
		Use:   "diff [id]",
		Short: "List files changed since engagement baseline (enumeration only)",
		Long: `Enumerate the files a story changed since its engagement baseline (the git
HEAD recorded on first entry into a performing state), including uncommitted
and untracked ones. Reach for it when a gate — or you — must know the slice.


Three enumerations, and picking the wrong one is the usual mistake:
  default              live git re-derive, baseline → worktree (--patch for the diff)
  --recorded           the change_record rows satelle wrote at each transition —
                       what a gate needs, and it sees git-ignored substrate too
  --include-substrate  also unions mtime-changed authored substrate, opt-in

It decides nothing; gates consume the output and judge. The diff is anchored to
the tree the story was engaged from and refuses elsewhere.`,
		Args:        cobra.MaximumNArgs(1),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				req := map[string]any{"id": args[0]}
				if patch {
					req["patch"] = true
				}
				if recorded {
					req["recorded"] = true
				}
				if includeSubstrate {
					req["include_substrate"] = true
				}
				return dispatch(cmd, "story-diff", req)
			}
			in, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return err
			}
			var body map[string]any
			if len(bytes.TrimSpace(in)) > 0 {
				if err := json.Unmarshal(in, &body); err != nil {
					return fmt.Errorf("story diff: stdin JSON: %w", err)
				}
			} else {
				body = map[string]any{}
			}
			if patch {
				body["patch"] = true
			}
			if recorded {
				body["recorded"] = true
			}
			if includeSubstrate {
				body["include_substrate"] = true
			}
			return dispatch(cmd, "story-diff", body)
		},
	}
	cmd.Flags().BoolVar(&patch, "patch", false, "include full unified patch since baseline (tracked)")
	cmd.Flags().BoolVar(&recorded, "recorded", false, "union change_record file lists instead of live git re-derive (sty_948ad5df)")
	cmd.Flags().BoolVar(&includeSubstrate, "include-substrate", false, "opt-in: union substrate mtime leg (authored dirs + data dir); default live path stays git-only")
	cmd.Flags().BoolVar(&full, "full", false, "skip compact rendering (noise-strip and ranking) and print the raw patch (sty_918e2086)")
	return cmd
}

// storyReleaseRecordCommand builds `satelle story release-record <id>...`:
// record one release against stories by id, from any session.
func storyReleaseRecordCommand() *cobra.Command {
	var commit, release string
	var fields []string
	cmd := &cobra.Command{
		Use:   "release-record <id>... --commit <sha> --release <id> [--field k=v ...]",
		Short: "Record a release against stories by id, done ones included; changes no status",
		Long: `Append one release_record ledger row to each named story: the commit and the
release identifier that shipped it, plus any --field metadata. It takes no seat
and changes no status, so it runs from any session and on a story already done.

The keys --field may carry are the repo's [release] fields in satelle.toml; an
undeclared key is refused when that list is set. Every id and key is checked
before anything is written, so a refusal records nothing.`,
		Args:        cobra.MinimumNArgs(1),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			kv := map[string]string{}
			for _, f := range fields {
				k, v, ok := strings.Cut(f, "=")
				if !ok {
					return fmt.Errorf("story release-record: --field %q is not k=v", f)
				}
				kv[strings.TrimSpace(k)] = v
			}
			return dispatch(cmd, "story-release-record", map[string]any{
				"ids": args, "commit": commit, "release": release, "fields": kv,
			})
		},
	}
	cmd.Flags().StringVar(&commit, "commit", "", "the commit the release shipped (required)")
	cmd.Flags().StringVar(&release, "release", "", "the release identifier, e.g. a version (required)")
	cmd.Flags().StringArrayVar(&fields, "field", nil, "extra release metadata as k=v (repeatable; keys from [release] fields)")
	return cmd
}

// storyProofCommand builds `satelle story proof [id]`: enumerate tests added or
// changed since the engagement baseline (sty_76796b8e). Enumeration only — it
// never runs tests and never exits non-zero for an enumerable state (including
// no-baseline and no new tests). Stdin-tolerant like story diff.
func storyProofCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "proof [id]",
		Short: "List tests changed since engagement baseline (enumeration only)",
		Long: `Enumerate the tests a story added or changed since its engagement baseline.
JSON only. Report-only — it does not run tests and does not decide pass/fail.

Reach for it from a functional-check gate (pipe the transition payload, no argv
id) or as an operator inspecting the slice. Default isolated reviewers have no
shell; this command is not injected into their payload.

Enumerable states always exit 0: no baseline (state=no_baseline), no new tests
(tests=[]), unparseable languages (listed under skipped with a reason), foreign
tree, git unavailable. Non-zero only for unknown id or a non-story item.

Go _test.go files include parsed func Test* names. Other languages are listed
by path with skipped reason. Changed non-test paths appear in non_test_files
so an empty tests list is not confused with a classifier miss.`,
		Args:        cobra.MaximumNArgs(1),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				return dispatch(cmd, "story-proof", map[string]any{"id": args[0]})
			}
			in, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return err
			}
			var body map[string]any
			if len(bytes.TrimSpace(in)) > 0 {
				if err := json.Unmarshal(in, &body); err != nil {
					return fmt.Errorf("story proof: stdin JSON: %w", err)
				}
			} else {
				body = map[string]any{}
			}
			return dispatch(cmd, "story-proof", body)
		},
	}
	return cmd
}

// storyDefinitionEditsCommand builds `satelle story definition-edits <id>`:
// enumerate the edits made to a story's definition while the route left it
// editable (sty_5262592e). Report-only — a functional-check gate reads the JSON
// and decides what a non-empty list means; the command itself always exits 0 for
// a readable story.
func storyDefinitionEditsCommand() *cobra.Command {
	var sinceEdge string
	cmd := &cobra.Command{
		Use:   "definition-edits <id>",
		Short: "List a story's definition edits (enumeration only)",
		Long: `List the title/body/acceptance_criteria/category edits recorded on a story's
ledger while its definition was still editable — field, before, after, actor.
JSON only. Report-only: it decides nothing.

--since-edge <from>:<to> keeps only the edits made after the story's latest
accepted <from>→<to> transition (for example backlog:plan). A functional-check
gate uses it to notice a definition edited after readiness was accepted, without
compiling that rule into the binary. With no accepted edge there is nothing to
have gone stale and the list is empty.`,
		Args:        cobra.ExactArgs(1),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := map[string]any{"id": args[0]}
			if sinceEdge != "" {
				req["since_edge"] = sinceEdge
			}
			return dispatch(cmd, "story-definition-edits", req)
		},
	}
	cmd.Flags().StringVar(&sinceEdge, "since-edge", "", "only edits after the latest accepted <from>:<to> transition (e.g. backlog:plan)")
	return cmd
}

// storyCostCommands builds `satelle story estimate` and `satelle story actual`:
// the driving session records a plan estimate at begin-work; the actual is
// COMPUTED, never entered (sty_8eae81ac AC1). Each dispatches to the
// story-estimate / story-actual verb.
func storyCostCommands() []*cobra.Command {
	var eTime, eBasis string
	var eTokens, eFreshInput, eOutput int
	estimate := &cobra.Command{
		Use:   "estimate <id>",
		Short: "Record a story's plan estimate (fresh-input/output, or legacy tokens/time)",
		Long: `Record the plan's estimate for a story: --fresh-input and --output for tokens
— the CURRENT units. --tokens and --time are LEGACY units, kept readable and
writable for stories already written in them, but never converted into fresh
input. A dollar figure is not an estimate.

The driving session records it — not the planner — and a workflow that gates on
the estimate greps those TAGS, not a plan section, so a figure written only into
the plan artifact leaves the gate unsatisfied. Record it before requesting the
transition the gate fires on.

Run ` + "`satelle story comparables <id>`" + ` first and estimate within its spread; the
satelle-estimate-actual-review gate rejects an estimate outside it.

--time takes a duration (30m, 2h, 1h30m) or a bare number of MINUTES (38).`,
		Args:        cobra.ExactArgs(1),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := map[string]any{"id": args[0]}
			putIf(req, "time", eTime)
			putIf(req, "basis", eBasis)
			if eTokens > 0 {
				req["tokens"] = eTokens
			}
			if eFreshInput > 0 {
				req["fresh_input"] = eFreshInput
			}
			if eOutput > 0 {
				req["output"] = eOutput
			}
			return dispatch(cmd, "story-estimate", req)
		},
	}
	estimate.Flags().IntVar(&eFreshInput, "fresh-input", 0, "estimated fresh input tokens")
	estimate.Flags().IntVar(&eOutput, "output", 0, "estimated output tokens")
	estimate.Flags().StringVar(&eTime, "time", "", "legacy unit: estimated duration (30m, 2h, or a bare number of minutes)")
	estimate.Flags().IntVar(&eTokens, "tokens", 0, "legacy unit: a bare estimated token count (never usd or fresh input)")
	estimate.Flags().StringVar(&eBasis, "basis", "", "optional note on the estimate basis")

	var cmpLimit int
	var cmpIgnore []string
	comparables := &cobra.Command{
		Use:   "comparables <id>",
		Short: "List finished stories whose measured token actuals an estimate can be compared against",
		Long: `Enumerate the finished stories comparable to <id>: terminal by their own
workflow, not a container (no children), and carrying a measured token actual
(actual-fresh-input / actual-output / actual-cache-read / actual-cache-write).
Ranked same category first, then by count of shared tags, then most recently
updated. A figure a story never measured is omitted, never shown as zero.

Run it BEFORE ` + "`satelle story estimate`" + ` and estimate within the spread it shows:
the satelle-estimate-actual-review gate reads the same list and rejects an
estimate no comparable supports. It decides nothing itself.

--ignore-prefix names tag prefixes left out of the shared-tag count (repeatable);
there is no default, the gate passes the ones it wants ignored. --limit caps the
list (0 = every comparable). Pass the global --json for the plain form.`,
		Args:        cobra.ExactArgs(1),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := map[string]any{"id": args[0]}
			if cmpLimit > 0 {
				req["limit"] = cmpLimit
			}
			if len(cmpIgnore) > 0 {
				req["ignore_prefixes"] = cmpIgnore
			}
			return dispatch(cmd, "story-comparables", req)
		},
	}
	comparables.Flags().IntVar(&cmpLimit, "limit", 0, "cap the list (0 = every comparable)")
	comparables.Flags().StringArrayVar(&cmpIgnore, "ignore-prefix", nil, "tag prefix to leave out of the shared-tag count; repeatable")

	actual := &cobra.Command{
		Use:   "actual <id>",
		Short: "Compute and record a story's actual cost from its ledger",
		Long: `Compute the story's actual from its ledger (dispatch + driver-usage rows) and
record it as actual-fresh-input / actual-output / actual-cache-read /
actual-cache-write / actual-minutes / actual-tokens tags, plus actual-cost
(low, medium, or high) when tokens were measured, and the full actual_recorded
ledger payload. A stored actual-usd tag is left as stored.

The actual is COMPUTED, never entered: this command takes no figures — pass
none. A hand-typed token, cache, or time actual is overwritten by the computed
one. This is also run automatically when the story reaches its workflow's
terminal state, so running it by hand is for an early look, not a requirement.`,
		Args:        cobra.ExactArgs(1),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return dispatch(cmd, "story-actual", map[string]any{"id": args[0]})
		},
	}

	backfill := &cobra.Command{
		Use:   "backfill-driver <id>",
		Short: "Attribute a closed story's driver spend from the session record it outlived",
		Long: `Recover the driving session's spend for a closed story whose driver_usage rows
are unavailable because its harness had no driver-usage reader at the time. The
session record's per-message timestamps are attributed to the story's own
engage..close window and appended as a backfilled row, marked as derived from
timestamps — never presented as a live-measured delta.

A window shared with another story driven in the same session is reported
ambiguous and is not split. A session record that is gone, an empty window, or
a model the harness reports no price for stays unavailable with the named
reason. Re-running appends nothing it already wrote. Run ` + "`satelle story actual <id>`" + `
afterwards to refresh the story's actual-* tags.`,
		Args:        cobra.ExactArgs(1),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return dispatch(cmd, "story-driver-backfill", map[string]any{"id": args[0]})
		},
	}

	// log — the generic typed telemetry/quality event write primitive (AC1,
	// sty_b73c3236), retiring `story step-cost`: any typed event — an in-loop
	// step's self-reported actual tokens/duration and per-step estimate (`--kind
	// step-self-report`), or a future prompted quality signal — is recorded the
	// same way. --data is repeatable; a value that parses as a number is recorded
	// numerically, else as a string. Refused when a key/value looks like a secret.
	var logKind string
	var logData []string
	log := &cobra.Command{
		Use:   "log <id> --kind <kind> [--data key=val ...]",
		Short: "Record a typed telemetry/quality event against a story",
		Long: `Append a typed event to a story's ledger — a step self-report, a quality
signal, whatever the kind names.

--data takes key=value pairs and types them: a value that parses as a number is
stored numerically, everything else as a string, so --data tokens_total=42000
lands as a number a later reader can sum. Append-only: nothing here rewrites a
prior entry.`,
		Args:        cobra.ExactArgs(1),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, derr := parseLogData(logData)
			if derr != nil {
				return derr
			}
			req := map[string]any{"id": args[0], "kind": logKind}
			if len(data) > 0 {
				req["data"] = data
			}
			return dispatch(cmd, "story-log", req)
		},
	}
	log.Flags().StringVar(&logKind, "kind", "", "the event kind (e.g. step-self-report, agent-retry) — required")
	log.Flags().StringArrayVar(&logData, "data", nil, "a key=value telemetry field; repeatable")

	// cost — the observability VIEW (sty_a699ad14): the per-gate token + wall-time
	// cost recorded on the story's agent_invocation ledger entries, so an operator
	// sees which reviewer/dispatch spent what. Distinct from estimate/actual (the
	// plan's own time/token figures) — this is measured runtime cost.
	var bySkill, allStories bool
	var skillStoryID, sessionID string
	var gateValue, rounds bool
	var gateEpicID, gateSince, gateUntil string
	var gateJSON bool
	cost := &cobra.Command{
		Use:   "cost [id]",
		Short: "Show the measured per-gate token + wall-time cost recorded for a story",
		Long: `Show what a story actually cost to run: derived cost, band and fresh-token
headline, per-seat breakdown, elapsed and agent time, the driving session's own
line, an epic-parent's family roll-up, then per-gate agent-invocation detail.

This is MEASURED transport cost, distinct from the estimate/actual tags. A row
printed as "—" is unmeasured, never free. "cost" is priced at read time from
the [pricing] you configure (satelle help cost-pricing); "reported" is the
provider's own dollars.

--by-skill rolls up agent_invocation rows by skill (--all or --story <id>).
--session <id> lists every story that session drove, plus its remainder.
--gate-value reports invocations, dollars, accepts and rejects per skill and
seat (--story, --epic, --since/--until).
--rounds <id> reports each edge's presentations: seat and performer cost,
findings answered, round converged; an epic adds a family total.`,
		Args:        cobra.MaximumNArgs(1),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := appFrom(cmd); err != nil {
				return err
			}
			if gateValue {
				return runGateValue(cmd, gateValueArgs{
					storyID: skillStoryID, epicID: gateEpicID,
					since: gateSince, until: gateUntil, asJSON: gateJSON,
				})
			}
			if sessionID != "" {
				recon, err := verb.ComputeSessionReconciliation(cmd.Context(), sessionID)
				if err != nil {
					return err
				}
				printSessionReconciliation(cmd, recon)
				return nil
			}
			if bySkill {
				scope := skillStoryID
				if !allStories && scope == "" {
					if len(args) == 1 {
						scope = args[0]
					} else {
						return fmt.Errorf("story cost --by-skill: pass --all, --story <id>, or a story id")
					}
				} else if allStories && (scope != "" || len(args) == 1) {
					return fmt.Errorf("story cost --by-skill: --all and --story/<id> are mutually exclusive")
				}
				rollup, err := verb.ComputeSkillRollup(cmd.Context(), scope)
				if err != nil {
					return err
				}
				rw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
				fmt.Fprintln(rw, "SKILL\tINVOCATIONS\tFRESH\tCACHE-WRITE\tCACHE-READ\tUNSPLIT\tTOTAL IN\tTOTAL OUT\tAVG SYS BYTES\tAVG PAYLOAD BYTES\tREPORTED $")
				for _, r := range rollup.Rows {
					fmt.Fprintf(rw, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%s\n",
						r.Skill, r.Invocations, r.FreshTokens, r.CacheWriteTokens, r.CacheReadTokens,
						r.UnsplitTokens, r.TotalInputTokens, r.TotalOutputTokens,
						r.AvgSystemPromptBytes, r.AvgPayloadBytes,
						costTotalLabel(r.TotalCostUSD, r.Invocations-r.UncostedRows, r.UncostedRows))
				}
				if err := rw.Flush(); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(),
					"note: UNSPLIT is measured input from rows recorded before the cache split existed — add it to FRESH+CACHE-WRITE+CACHE-READ to reconcile TOTAL IN.")
				return nil
			}
			if len(args) != 1 {
				return fmt.Errorf("story cost: a story id is required (or pass --by-skill)")
			}
			if rounds {
				rr, err := verb.ComputeReviewRounds(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				if gateJSON {
					enc := json.NewEncoder(cmd.OutOrStdout())
					enc.SetIndent("", "  ")
					return enc.Encode(rr)
				}
				return printReviewRounds(cmd, rr)
			}
			// A gate wait the harness has since flushed is closed here, by the read
			// that wants its count (sty_c4b92c9e).
			verb.SettleGateWaits(cmd.Context(), args[0], time.Now().UTC())
			sc, err := verb.ComputeStoryCost(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			printCostSummary(cmd, sc)
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			// Dispatched/reviewed invocations — the precise sub-process cost.
			// Unreported usage renders as — (sty_56aae77a), never a confident 0.
			fmt.Fprintln(w, "TRANSITION\tSTEP\tMODEL\tTOKENS in/out\tTOTAL\tDURATION")
			for _, r := range sc.Rows {
				step := r.Agent
				if r.Skill != "" {
					step = r.Skill
				}
				fmt.Fprintf(w, "%s→%s\t%s\t%s\t%s\t%s\t%s\n",
					r.From, r.To, step, ledger.ModelLabel(r.Model, r.ModelResolved),
					rowTokensIO(r.TokensIn, r.TokensOut, r.UsageAvailable),
					rowTokensTotal(r.TokensTotal, r.UsageAvailable),
					fmtDurationMs(r.DurationMs))
			}
			fmt.Fprintf(w, "TOTAL\t\t\t\t%s\t%s\n",
				measuredTotalLabel(sc.TotalTokens, sc.MeasuredRows, sc.UnmeasuredRows),
				fmtDurationMs(sc.TotalDurationMs))
			if err := w.Flush(); err != nil {
				return err
			}
			// Per-step report — every step's wall-time (derived from transition
			// timestamps, so IN-LOOP steps are covered too) with any self-reported
			// actual tokens and per-step estimate (satelle story log --kind step-self-report).
			if len(sc.Steps) > 0 {
				sw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
				fmt.Fprintln(sw, "\nSTEP\tWALL-TIME\tACTUAL TOKENS\tEST TOKENS\tEST TIME")
				for _, s := range sc.Steps {
					fmt.Fprintf(sw, "%s\t%s\t%s\t%s\t%s\n",
						s.Step, fmtDurationMs(s.WallTimeMs),
						stepTokens(s.TokensTotal, s.HasTokens),
						dashIfZero(s.EstTokens), fmtDurationMs(s.EstDurationMs))
				}
				fmt.Fprintf(sw, "TOTAL\t%s\t\t\t\n", fmtDurationMs(sc.TotalWallMs))
				if err := sw.Flush(); err != nil {
					return err
				}
			}
			// Driver section (sty_81caa41b): the driving session's own recorded
			// usage, kept separate from the dispatched/reviewer rows above.
			if len(sc.DriverRows) > 0 {
				if err := printDriverSection(cmd, sc); err != nil {
					return err
				}
			}
			fmt.Fprintln(cmd.OutOrStdout(),
				"note: '—' means unmeasured, never free. Dispatched rows without provider usage are unknown; in-loop ACTUAL TOKENS need satelle story log --kind step-self-report. actual-* tags are computed from the ledger by `satelle story actual` (never hand-entered); step-self-report figures remain session self-report, not measured transport cost. TOKENS in includes cache-creation and cache-read tokens when the provider reports them (the full prompt, not the uncached remainder); rows recorded before that accounting omit cache and understate input.")
			return nil
		},
	}
	cost.Flags().BoolVar(&bySkill, "by-skill", false, "roll up agent_invocation rows by skill instead of by story (needs --all or --story)")
	cost.Flags().BoolVar(&allStories, "all", false, "with --by-skill, roll up across every story")
	cost.Flags().StringVar(&skillStoryID, "story", "", "with --by-skill or --gate-value, scope to one story only")
	cost.Flags().StringVar(&sessionID, "session", "", "list every story this driving session drove, plus its unattributed remainder")
	cost.Flags().BoolVar(&gateValue, "gate-value", false, "report invocations/dollars/fresh tokens/accepts/rejects/$-per-reject per skill and seat")
	cost.Flags().StringVar(&gateEpicID, "epic", "", "with --gate-value, scope to an epic-parent and every descendant found by parent_id")
	cost.Flags().StringVar(&gateSince, "since", "", "with --gate-value, only entries at/after this date (YYYY-MM-DD)")
	cost.Flags().StringVar(&gateUntil, "until", "", "with --gate-value, only entries at/before this date (YYYY-MM-DD)")
	cost.Flags().BoolVar(&rounds, "rounds", false, "report each gated edge's presentations, each seat attempt's turns, tool calls, outcome and duration, each round's performer cost and answered findings, and the round the edge converged or stopped at (an epic id adds each child and a family total)")
	cost.Flags().BoolVar(&gateJSON, "json", false, "with --gate-value or --rounds, emit the report as JSON")

	// resummarise — re-run the step summariser for one edge to close a missing-
	// summary gap (sty_a1151fb0). The remediation `satelle story cost`/the done-time
	// warning names when a transient kill holed the pull-context chain.
	var rsFrom, rsTo string
	resummarise := &cobra.Command{
		Use:   "resummarise <id> --from <state> --to <state>",
		Short: "Re-run the step summariser for one edge to close a missing-summary gap",
		Long: `Re-run the step summariser for ONE edge whose summary is missing — the hole a
transient kill leaves in the trail.

Both --from and --to are required: this repairs a named edge, not the story.
Reach for it when the route or a done-time warning says a summary is absent;
it fills the gap rather than re-judging anything.`,
		Args:        cobra.ExactArgs(1),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return dispatch(cmd, "story-resummarise", map[string]any{"id": args[0], "from": rsFrom, "to": rsTo})
		},
	}
	resummarise.Flags().StringVar(&rsFrom, "from", "", "the edge's FROM state — required")
	resummarise.Flags().StringVar(&rsTo, "to", "", "the edge's TO state — required")

	// retrospect — dispatch the retrospective agent over a finished story to file
	// improvement proposals (sty_b53730e2). Opt-in per story, so its cost (visible
	// via `story cost`) is measured before it is ever made auto-on-done.
	retrospect := &cobra.Command{
		Use:   "retrospect <id>",
		Short: "Run the retrospective agent over a finished story to file improvement proposals",
		Long: `Dispatch the retrospective agent over a FINISHED story: it reads what happened
and files improvement proposals.

Opt-in per story, deliberately — it costs a full agent turn, visible afterwards
in satelle story cost. Reach for it on a story worth learning from, not as a
routine close step.`,
		Args:        cobra.ExactArgs(1),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			model, _ := cmd.Flags().GetString("model")
			return dispatch(cmd, "story-retrospect", map[string]any{"id": args[0], "model": model})
		},
	}
	retrospect.Flags().String("model", "", "model for this dispatch, overriding agents.toml (recorded with source=agent)")

	return []*cobra.Command{estimate, comparables, actual, backfill, log, cost, resummarise, retrospect}
}

// parseLogData turns a list of "key=value" flags into a typed data map: a
// value that parses as an int64 or a float64 is recorded numerically, else as
// a string — so a caller passes `--data tokens_total=42000` and gets a JSON
// number, not a quoted string, in the telemetry payload.
func parseLogData(kv []string) (map[string]any, error) {
	if len(kv) == 0 {
		return nil, nil
	}
	data := make(map[string]any, len(kv))
	for _, pair := range kv {
		key, val, ok := strings.Cut(pair, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid --data %q: want key=value", pair)
		}
		if n, err := strconv.ParseInt(val, 10, 64); err == nil {
			data[key] = n
			continue
		}
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			data[key] = f
			continue
		}
		data[key] = val
	}
	return data, nil
}

// stepTokens renders an in-loop step's self-reported actual tokens. A step with no
// recorded tokens shows '—' (unmeasured — the CLI can't see the driving session's
// tokens), deliberately distinct from a recorded 0.
func stepTokens(total int, has bool) string {
	if !has {
		return "—"
	}
	return strconv.Itoa(total)
}

// rowTokensIO renders TOKENS in/out for a dispatched row. Unreported usage is
// '—/—' — never a confident 0/0 (sty_56aae77a).
func rowTokensIO(in, out int, available bool) string {
	if !available {
		return "—/—"
	}
	return fmt.Sprintf("%d/%d", in, out)
}

// rowTokensTotal renders TOTAL for a dispatched row. Unreported → '—'.
func rowTokensTotal(total int, available bool) string {
	if !available {
		return "—"
	}
	return strconv.Itoa(total)
}

// measuredTotalLabel renders the TOTAL column for the cost table. When some
// invocations are unreported, the count is inline so the number is never
// mistaken for a full-story total (sty_56aae77a). When NOTHING was measured
// (measured == 0), totalTokens is the sum of an empty set, not a real zero —
// printing it bare would misrepresent "nothing measured" as "measured zero"
// (sty_b8542a3a AC1 rework), so this renders "unavailable" instead, same as
// the rest of the cost view's never-implied-zero convention.
func measuredTotalLabel(totalTokens, measured, unmeasured int) string {
	if measured == 0 {
		if unmeasured > 0 {
			return fmt.Sprintf("unavailable (%d invocations unreported)", unmeasured)
		}
		return "unavailable"
	}
	if unmeasured <= 0 {
		return strconv.Itoa(totalTokens)
	}
	return fmt.Sprintf("%d (measured; %d of %d invocations unreported)",
		totalTokens, unmeasured, measured+unmeasured)
}

// rowCostUSD renders a row's $ column. Nil means the provider reported no
// cost — "unknown", never a confident $0 (sty_c4df7376).
func rowCostUSD(cost *float64) string {
	if cost == nil {
		return "unknown"
	}
	return fmt.Sprintf("$%.4f", *cost)
}

// costTotalLabel renders a TOTAL/rollup $ figure. costed==0 means nothing in
// the set priced at all — unknown, never $0. A nonzero uncosted count prints
// inline so the total is never mistaken for the full set's cost.
func costTotalLabel(total float64, costed, uncosted int) string {
	if costed == 0 {
		return "unknown"
	}
	if uncosted == 0 {
		return fmt.Sprintf("$%.4f", total)
	}
	return fmt.Sprintf("$%.4f (%d rows unknown)", total, uncosted)
}

// driverRowsToCostview converts verb's richer DriverUsagePayload rows (the
// driver_usage ledger payload, which also carries fields printDriverSection
// has no use for — WindowKey, Cumulative, Late/Pending — owned by the
// recorder in driver_usage.go) down to costview.DriverRow: the figures-only
// shape costview.FormatDriverRows renders, so the CLI table and the web
// page's Driver sessions table are built from the same rows through the
// same formatting pass and can never disagree (sty_b8542a3a AC7 rework).
func driverRowsToCostview(rows []verb.DriverUsagePayload) []costview.DriverRow {
	out := make([]costview.DriverRow, len(rows))
	for i, d := range rows {
		out[i] = costview.DriverRow{
			SessionID: d.SessionID, Executable: d.Executable, Model: d.Model,
			FreshInput: d.FreshInput, CacheRead: d.CacheRead, CacheWrite: d.CacheWrite, Output: d.Output,
			CostUSD: d.CostUSD, CostUnavailableReason: d.CostUnavailableReason,
			Available: d.Available, UnavailableReason: d.UnavailableReason, WallSeconds: d.WallSeconds, Trigger: d.Trigger,
			From: d.From, To: d.To,
			ModelCalls: d.ModelCalls, ModelCallsUnavailableReason: d.ModelCallsUnavailableReason,
			Turns: d.Turns, Unflushed: d.Unflushed, BaselineFresh: d.BaselineFresh, Backfilled: d.Backfilled,
			Overlaps: d.Overlaps, AttributionReason: d.AttributionReason,
		}
		out[i].Cumulative.ModelCalls = d.Cumulative.ModelCalls
	}
	return costview.MarkSuperseded(out)
}

// printDriverSection renders the DRIVER SESSION table — the driving (in-loop)
// session's own recorded usage, kept separate from the dispatched/reviewer
// Rows above (sty_81caa41b, sty_b8542a3a AC1/AC7 rework). costview.
// FormatDriverRows is the SAME formatting pass the web page's Driver sessions
// table calls: fresh input, output, cache read and cache write are separate
// columns, never folded into one cache-inclusive figure, and a TOTAL column
// with nothing measured at all renders "unavailable" rather than a literal 0.
func printDriverSection(cmd *cobra.Command, sc verb.StoryCost) error {
	rowViews, totalView := costview.FormatDriverRows(driverRowsToCostview(sc.DriverRows))
	dw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(dw, "\nDRIVER SESSION\tEXECUTABLE\tTRIGGER\tFRESH IN\tOUT\tCALLS\tCACHE READ\tCACHE WRITE\tREPORTED $\tAGENT TIME")
	for _, d := range rowViews {
		fmt.Fprintf(dw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			d.SessionID, d.Executable, d.Trigger, d.FreshIn, d.Out, d.Calls, d.CacheRead, d.CacheWrite, d.USD, d.AgentTime)
	}
	if totalView != nil {
		fmt.Fprintf(dw, "%s\t\t\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			totalView.SessionID, totalView.FreshIn, totalView.Out, totalView.Calls, totalView.CacheRead, totalView.CacheWrite,
			totalView.USD, totalView.AgentTime)
	}
	if err := dw.Flush(); err != nil {
		return err
	}
	for _, d := range rowViews {
		if d.Note != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "  note %s %s: %s\n", d.SessionID, d.Trigger, d.Note)
		}
	}
	printGateWaits(cmd, sc.GateWaits)
	// GRAND TOTAL reads off sc.Figures — costview.Own's single accumulation
	// over BOTH agent_invocation and driver_usage rows (figures.go addDispatchRow/
	// addDriverRow feed the same accumulator) — rather than the legacy
	// GrandTotalTokens/GrandTotalCostUSD fields (a second, cache-inclusive sum
	// kept only for JSON back-compat). Fresh/out/cache stay separate columns
	// and never blend into one cache-inclusive figure, and "unavailable" never
	// reads as a literal 0 (sty_b8542a3a AC1 rework).
	f := sc.Figures
	splitRows := f.UsageRows - f.UnsplitRows
	fmt.Fprintf(cmd.OutOrStdout(), "\nGRAND TOTAL (agent_invocation + driver): fresh in %s | out %s | cache read %s | cache write %s | cost %s | agent time %s\n",
		costview.FormatSplitTokens(f.FreshInput, splitRows, f.UnsplitRows, f.UsageUnavailableRows),
		costview.FormatTokensMeasured(f.Output, f.UsageRows, f.UsageUnavailableRows),
		costview.FormatSplitTokens(f.CacheRead, splitRows, f.UnsplitRows, f.UsageUnavailableRows),
		costview.FormatSplitTokens(f.CacheWrite, splitRows, f.UnsplitRows, f.UsageUnavailableRows),
		costview.FormatDerived(f.Derived),
		costview.FormatDuration(f.AgentMs))
	for _, line := range costview.FormatAdapterCoverage(f) {
		fmt.Fprintln(cmd.OutOrStdout(), line)
	}
	for _, recon := range sc.Sessions {
		printSessionReconciliation(cmd, recon)
	}
	return nil
}

// printGateWaits renders the driver's model calls across each handed-off gate
// wait (sty_c4b92c9e): the count read from the gate rows, or why there is none
// yet — open while the harness has not flushed the turn, "—" where it cannot say.
func printGateWaits(cmd *cobra.Command, waits []costview.GateWait) {
	if len(waits) == 0 {
		return
	}
	gw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(gw, "\nGATE WAIT\tEXECUTABLE\tDRIVER MODEL CALLS\tNOTE")
	for _, w := range waits {
		fmt.Fprintf(gw, "%s\t%s\t%s\t%s\n", w.Handle, w.Executable, costview.FormatModelCalls(w), w.Reason)
	}
	_ = gw.Flush()
}

// printSessionReconciliation renders one driving session's reconciliation
// (sty_81caa41b AC9/AC11): every story it drove plus the unattributed
// remainder — session time the ledger cannot pin to any story (e.g. between
// one story's close and the next story's engage).
func printSessionReconciliation(cmd *cobra.Command, recon verb.SessionReconciliation) {
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintf(cmd.OutOrStdout(), "\nSession %s (%s) reconciliation:\n", recon.SessionID, recon.Executable)
	fmt.Fprintln(w, "STORY\tTOKENS\tREPORTED $")
	for _, s := range recon.Stories {
		tokens, cost := "—", "unknown"
		if s.Available {
			tokens = strconv.Itoa(s.FreshInput + s.CacheRead + s.CacheWrite + s.Output)
			if s.Backfilled {
				tokens += " (backfilled: " + costview.BackfilledLabel + ")"
			}
			cost = rowCostUSD(s.CostUSD)
		} else {
			tokens = fmt.Sprintf("unavailable (%s)", s.UnavailableReason)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", s.StoryID, tokens, cost)
	}
	total := "unavailable"
	if recon.TotalAvailable {
		total = strconv.Itoa(recon.Total.FreshInput + recon.Total.CacheRead + recon.Total.CacheWrite + recon.Total.Output)
	} else if recon.TotalUnavailableReason != "" {
		total = fmt.Sprintf("unavailable (%s)", recon.TotalUnavailableReason)
	}
	unattributed := "partial (an attributed story is unavailable)"
	if recon.UnattributedAvailable {
		unattributed = strconv.Itoa(recon.Unattributed.FreshInput + recon.Unattributed.CacheRead + recon.Unattributed.CacheWrite + recon.Unattributed.Output)
	}
	fmt.Fprintf(w, "SESSION TOTAL\t%s\t\nUNATTRIBUTED\t%s\t\n", total, unattributed)
	_ = w.Flush()
}

// printCostSummary renders the costview headline: the cost band and fresh
// tokens lead, cache read/write are secondary columns, elapsed wall time and
// agent time are both shown (never substituted for each other), any legacy
// estimate prints in its own unit beside the figure it matches, and — when the
// story has children — the family roll-up. The band is costview.FormatCostBand
// on the SAME Figures the actual-cost tag and the web page read, so all three
// agree. An empty band renders "unavailable". A dollar estimate is not shown.
func printCostSummary(cmd *cobra.Command, sc verb.StoryCost) {
	out := cmd.OutOrStdout()
	f := sc.Figures
	fmt.Fprintln(out, "COST SUMMARY")
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	splitRows := f.UsageRows - f.UnsplitRows
	// cost is the one primary dollar figure: the measured tokens priced now under
	// the operator's [pricing]. The provider's own dollars are the labelled
	// "reported" line; the band is the secondary size indicator after them.
	fmt.Fprintf(w, "cost\t%s\n", costview.FormatDerived(f.Derived))
	fmt.Fprintf(w, "reported\t%s\n", costview.FormatUSD(f.CostUSD, f.CostRows, f.CostUnavailableRows))
	fmt.Fprintf(w, "BAND\t%s\n", costview.FormatCostBand(f))
	fmt.Fprintf(w, "FRESH IN\t%s\t%s\n", costview.FormatSplitTokens(f.FreshInput, splitRows, f.UnsplitRows, f.UsageUnavailableRows), costview.FormatEstimate(sc.Estimates, "fresh-input"))
	fmt.Fprintf(w, "OUT\t%s\t%s\n", costview.FormatTokensMeasured(f.Output, f.UsageRows, f.UsageUnavailableRows), costview.FormatEstimate(sc.Estimates, "output"))
	fmt.Fprintf(w, "CACHE READ\t%s\t\n", costview.FormatSplitTokens(f.CacheRead, splitRows, f.UnsplitRows, f.UsageUnavailableRows))
	fmt.Fprintf(w, "CACHE WRITE\t%s\t\n", costview.FormatSplitTokens(f.CacheWrite, splitRows, f.UnsplitRows, f.UsageUnavailableRows))
	if f.UnsplitTokens > 0 {
		fmt.Fprintf(w, "UNSPLIT (legacy)\t%s\t\n", costview.FormatTokens(f.UnsplitTokens))
	}
	fmt.Fprintf(w, "elapsed (wall)\t%s\t%s\n", costview.FormatDuration(f.ElapsedMs), costview.FormatEstimate(sc.Estimates, "minutes"))
	fmt.Fprintf(w, "agent time (dispatch+driver)\t%s\t\n", costview.FormatDuration(f.AgentMs))
	_ = w.Flush()
	printSeatLines(out, "BY SEAT", sc.Seats)
	fmt.Fprintln(out, costview.FormatDriverCoverage(f))
	if legacy := costview.FormatLegacyTokenEstimate(sc.Estimates); legacy != "" {
		fmt.Fprintln(out, legacy)
	}
	if sc.Family != nil {
		fmt.Fprintln(out, "\nFAMILY")
		fw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(fw, "CHILD\tcost\tBAND\tFRESH IN\tOUT\tCACHE READ\tCACHE WRITE\tELAPSED")
		for _, c := range sc.Family.Children {
			cf := c.Figures
			cfSplitRows := cf.UsageRows - cf.UnsplitRows
			fmt.Fprintf(fw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", c.ID,
				costview.FormatDerived(cf.Derived),
				costview.FormatCostBand(cf),
				costview.FormatSplitTokens(cf.FreshInput, cfSplitRows, cf.UnsplitRows, cf.UsageUnavailableRows), costview.FormatTokensMeasured(cf.Output, cf.UsageRows, cf.UsageUnavailableRows),
				costview.FormatSplitTokens(cf.CacheRead, cfSplitRows, cf.UnsplitRows, cf.UsageUnavailableRows), costview.FormatSplitTokens(cf.CacheWrite, cfSplitRows, cf.UnsplitRows, cf.UsageUnavailableRows),
				costview.FormatDuration(cf.ElapsedMs))
		}
		ft := sc.Family.Total
		ftSplitRows := ft.UsageRows - ft.UnsplitRows
		fmt.Fprintf(fw, "FAMILY TOTAL\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			costview.FormatDerived(ft.Derived),
			costview.FormatCostBand(ft),
			costview.FormatSplitTokens(ft.FreshInput, ftSplitRows, ft.UnsplitRows, ft.UsageUnavailableRows), costview.FormatTokensMeasured(ft.Output, ft.UsageRows, ft.UsageUnavailableRows),
			costview.FormatSplitTokens(ft.CacheRead, ftSplitRows, ft.UnsplitRows, ft.UsageUnavailableRows), costview.FormatSplitTokens(ft.CacheWrite, ftSplitRows, ft.UnsplitRows, ft.UsageUnavailableRows),
			costview.FormatDuration(ft.ElapsedMs))
		_ = fw.Flush()
		printSeatLines(out, "FAMILY BY SEAT", sc.Family.Seats)
		for _, line := range costview.FormatAdapterCoverage(ft) {
			fmt.Fprintln(out, "  "+line)
		}
		fmt.Fprintln(out, "  "+costview.FormatDriverCoverage(ft))
	}
	fmt.Fprintln(out)
}

// printSeatLines renders the per-seat token breakdown with each seat's derived
// cost — the same costview.FormatSeatLines pass the web page's seat table reads.
func printSeatLines(out io.Writer, title string, lines []costview.SeatLine) {
	if len(lines) == 0 {
		return
	}
	fmt.Fprintf(out, "\n%s\n", title)
	sw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(sw, "SEAT\tAUTH\tFRESH IN\tCACHE READ\tCACHE WRITE\tOUT\tcost")
	for _, v := range costview.FormatSeatLines(lines) {
		fmt.Fprintf(sw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", v.Seat, v.Auth, v.FreshIn, v.CacheRead, v.CacheWrite, v.Out, v.Cost)
	}
	_ = sw.Flush()
	fmt.Fprintln(out)
}

// gateValueArgs is the parsed input to runGateValue — one of storyID or
// epicID scopes the story set (storyID wins if both are set); since/until
// (either may be empty) bound the date range.
type gateValueArgs struct {
	storyID, epicID string
	since, until    string
	asJSON          bool
}

// runGateValue renders `satelle story cost --gate-value` (sty_b8542a3a AC6):
// invocations, dollars, fresh tokens, accepts, rejects and dollars-per-reject
// per skill/seat. It is a read-only query over stored evidence — it
// recommends nothing and changes no configuration.
func runGateValue(cmd *cobra.Command, args gateValueArgs) error {
	opts := verb.GateValueOptions{StoryID: args.storyID, EpicID: args.epicID}
	if args.since != "" {
		t, err := time.Parse("2006-01-02", args.since)
		if err != nil {
			return fmt.Errorf("story cost --gate-value: --since: %w", err)
		}
		opts.Since = t
	}
	if args.until != "" {
		t, err := time.Parse("2006-01-02", args.until)
		if err != nil {
			return fmt.Errorf("story cost --gate-value: --until: %w", err)
		}
		opts.Until = t.Add(24*time.Hour - time.Nanosecond) // inclusive of the whole day
	}
	report, err := verb.ComputeGateValue(cmd.Context(), opts)
	if err != nil {
		return err
	}
	if args.asJSON {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	// A bundled reviewer session is one measured call judging several rubrics;
	// each rubric's row carries an ALLOCATED share of it, and the NOTE column
	// says so (sty_23e10d92). The column only appears when a row carries one.
	allocated := false
	for _, r := range report.Rows {
		allocated = allocated || r.AllocationNote != ""
	}
	header := "SKILL\tSEAT\tINVOCATIONS\tREPORTED $\tFRESH TOKENS\tUNSPLIT (legacy)\tACCEPTS\tREJECTS\tREPORTED $/REJECT"
	if allocated {
		header += "\tNOTE"
	}
	fmt.Fprintln(w, header)
	for _, r := range report.Rows {
		splitRows := r.UsageRows - r.UnsplitRows
		line := fmt.Sprintf("%s\t%s\t%d\t%s\t%s\t%s\t%d\t%d\t%s",
			r.Skill, r.Seat, r.Invocations,
			costview.FormatUSD(r.CostUSD, r.Costed, r.Uncosted), costview.FormatSplitTokens(r.FreshTokens, splitRows, r.UnsplitRows, r.UsageUnavailableRows),
			dashIfZero(r.UnsplitTokens),
			r.Accepts, r.Rejects, costview.FormatCostPerReject(r.CostUSD, r.Costed, r.Uncosted, r.Rejects))
		if allocated {
			line += "\t" + r.AllocationNote
			if r.AllocationUnavailableReason != "" {
				line += " (unavailable: " + r.AllocationUnavailableReason + ")"
			}
		}
		fmt.Fprintln(w, line)
	}
	return w.Flush()
}

// stepSelfReportNudge is the step-edge advisory printed after a real status
// change (sty_56aae77a AC3). Empty when suppressed (already reported). Pure so
// the CLI test can pin the wording without a full transition.
func stepSelfReportNudge(storyID, prevStatus string, alreadyReported bool) string {
	if storyID == "" || prevStatus == "" || alreadyReported {
		return ""
	}
	return fmt.Sprintf(
		"note: record the step you just finished —\n  satelle story log %s --kind step-self-report --data step=%s --data tokens_total=<n>\n",
		storyID, prevStatus)
}

// dashIfZero renders a count, or '—' when zero (no estimate recorded).
func dashIfZero(n int) string {
	if n <= 0 {
		return "—"
	}
	return strconv.Itoa(n)
}

// fmtDurationMs renders a millisecond duration compactly (e.g. 3.1s, 2m4s). Zero
// (an uninstrumented/plain-text invocation) renders as a dash.
func fmtDurationMs(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	return (time.Duration(ms) * time.Millisecond).Round(100 * time.Millisecond).String()
}

// storyWaveCommand builds `satelle story wave <epic-parent-id>`: the read-only
// assessment of which children of an epic may start now (sty_a0563b3e). It
// engages, dispatches and writes nothing.
func storyWaveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "wave <epic-parent-id>",
		Short: "Print the children of an epic that may start now (read-only)",
		Long: `Print the runnable child ids of an epic-parent, one per line. Read-only: it
writes no status, lease or tag, and engages or dispatches nothing.

A dependency is satisfied only when that story's own route says done; an
unsatisfied child is listed (# omitted) with the dependency named. The schedule
is parallel by default; a container's route opts out with sequential = true, and
then more than one runnable child exits non-zero and prints no id.

Then one '# delivery' line per open child: its order, lane, delivering step,
where that step runs (in-loop, dispatched, executor, team:<path>), its agent and
the orchestrator's recorded reason.

See 'satelle help epic-wave' for the full contract. --json prints one object:
runnable ids and omitted entries (id, cause, waiting_on, reason), both arrays.`,
		Args:        cobra.ExactArgs(1),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := json.Marshal(map[string]any{"id": args[0]})
			if err != nil {
				return err
			}
			resp, err := verb.Dispatch(cmd.Context(), "story-wave", body)
			if err != nil {
				return err
			}
			if v, _ := cmd.Flags().GetBool("json"); v {
				return printJSON(cmd, resp)
			}
			var res verb.WaveResult
			if err := json.Unmarshal(resp, &res); err != nil {
				return fmt.Errorf("story wave: decode response: %w", err)
			}
			out := cmd.OutOrStdout()
			for _, id := range res.Runnable {
				fmt.Fprintln(out, id)
			}
			for _, o := range res.Omitted {
				fmt.Fprintf(out, "# omitted %s: %s\n", o.ID, o.Reason)
			}
			for _, d := range res.Delivery {
				fmt.Fprintln(out, formatWaveDelivery(d))
			}
			return nil
		},
	}
}

// formatWaveDelivery is one "# delivery" line of `satelle story wave`: how a
// child runs, as the verb resolved it. It only lays out what the verb returns.
func formatWaveDelivery(d verb.WaveDelivery) string {
	parts := []string{d.Order}
	if d.Lane != "" {
		parts = append(parts, "lane="+d.Lane)
	}
	if d.Step != "" {
		parts = append(parts, "step="+d.Step)
	}
	switch {
	case d.Error != "":
		parts = append(parts, "error: "+d.Error)
	case d.Placement != "":
		parts = append(parts, d.Placement)
	}
	if d.Agent != "" {
		parts = append(parts, "agent="+d.Agent)
	}
	line := fmt.Sprintf("# delivery %s: %s", d.ID, strings.Join(parts, ", "))
	if d.Note != "" {
		line += " (" + d.Note + ")"
	}
	if d.Reason != "" {
		line += " — " + d.Reason
	}
	return line
}

// storyReadyCommand builds `satelle story ready <id> [<id>...]`: ready backlog
// stories and epics without engaging them (sty_a32bd6a9). It runs each story's
// readiness step — the one its route declares — records the verdicts on the
// story, and marks it only when the repo's own readiness gates accept.
func storyReadyCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "ready <id> [<id>...]",
		Short: "Ready backlog stories or epics without engaging them",
		Long: `Run each story's readiness step — performer, then reviewers — and
record its verdicts. A round never changes status: an accepted story gets only the
step's mark tag and stays in backlog; 'satelle story set <id> --status plan' takes
backlog → plan. An epic round judges the epic; with ready_children unset or true it
also runs each backlog child's round (each stays in backlog), and a child with no
readiness step is reported no-readiness-step. With ready_children = false each
child is planned when 'satelle story wave' lets it start.

A story is refused (the others still run) when not at the route's start status,
its budget is spent, or it has no dispatched performer.
One line per story: <id> marked|not-accepted|refused|no-readiness-step[: why]; non-zero exit unless all marked. See 'satelle help workflow-convert'.`,
		Args:        cobra.MinimumNArgs(1),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			// The round runs in process and records on every story it readies — an
			// epic's backlog children too — so each is fetched first (sty_00212637).
			fetchBeforeWrite(cmd, readyWriteSet(cmd, args)...)
			body, err := json.Marshal(map[string]any{"ids": args})
			if err != nil {
				return err
			}
			// A readiness round runs a performer and reviewers, so an agent caller
			// hands it to a detached run exactly as it does a gated transition.
			if verb.DispatchesReviewer("story-ready", body) {
				if handled, herr := handOffGate(cmd, "story-ready", args[0]); handled {
					return herr
				}
			}
			resp, err := verb.Dispatch(cmd.Context(), "story-ready", body)
			if err != nil {
				return err
			}
			var results []verb.ReadyResult
			if err := json.Unmarshal(resp, &results); err != nil {
				return fmt.Errorf("story ready: decode response: %w", err)
			}
			if v, _ := cmd.Flags().GetBool("json"); v {
				if err := printJSON(cmd, resp); err != nil {
					return err
				}
			} else {
				out := cmd.OutOrStdout()
				for _, r := range results {
					fmt.Fprintln(out, r.Line())
					for _, c := range r.Children {
						fmt.Fprintln(out, "  "+c.Line())
					}
				}
			}
			emitAdvice(cmd, cmd.OutOrStdout(), readyAdvice(results))
			unmarked := 0
			for _, r := range results {
				if !r.Marked() {
					unmarked++
				}
			}
			if unmarked > 0 {
				return fmt.Errorf("story ready: %d of %d not marked", unmarked, len(results))
			}
			return nil
		},
	}
}

// readyWriteSet is every story a readiness round over ids may write to: each id
// and, transitively, the members epicset.Members gives a container — the source
// the epic round iterates. It is not filtered by what the round will skip; a
// skipped member costs only the fetch's mark lookup.
func readyWriteSet(cmd *cobra.Command, ids []string) []string {
	a, err := appFrom(cmd)
	if err != nil {
		return ids
	}
	seen := map[string]bool{}
	var out []string
	var walk func(id string)
	walk = func(id string) {
		if seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
		it, err := a.Store.Stories.Get(cmd.Context(), id)
		if err != nil {
			return
		}
		members, err := epicset.Members(cmd.Context(), a.Store.Stories, it)
		if err != nil {
			return
		}
		for _, m := range members {
			walk(m.ID)
		}
	}
	for _, id := range ids {
		walk(id)
	}
	return out
}

// storyRestampCommand builds `satelle story restamp <id> [--workflow <name>]`:
// the first-class re-stamp of a story's governing workflow (sty_ed3386cf) — the
// sanctioned replacement for hand-editing the tag list when the right workflow
// changes mid-flight (a re-categorised story, or a more specific category
// workflow authored after create). Dispatches to the story-restamp verb, which
// validates the target (it must resolve, and the story's current status must be
// one of its states), upserts the workflow: tag preserving every other tag, and
// records a workflow_stamped ledger row plus an operation-log line.
func storyRestampCommand() *cobra.Command {
	var wfName string
	restamp := &cobra.Command{
		Use:   "restamp <id>",
		Short: "Re-stamp the story's governing workflow (re-resolve by category, or --workflow)",
		Long: `restamp re-stamps the workflow that governs a story. Without --workflow it
re-resolves from the story's CURRENT category — the same resolution create uses —
so a re-categorised story picks up its category-specific workflow. With
--workflow <name> it stamps that workflow explicitly.

The target is validated before anything changes: the workflow must resolve in
the substrate, and the story's current status must be a state the workflow
declares (else the story would be stranded mid-lifecycle). Every other tag —
estimate/actual, category, ad-hoc — survives untouched, and the change is
recorded as a workflow_stamped ledger row and an operation-log line. Stories
only: tasks and executions are unstamped by design.`,
		Args:        cobra.ExactArgs(1),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := map[string]any{"id": args[0]}
			putIf(req, "workflow", wfName)
			return dispatch(cmd, "story-restamp", req)
		},
	}
	restamp.Flags().StringVar(&wfName, "workflow", "", "explicit workflow to stamp (default: re-resolve from the story's category)")
	return restamp
}

// storyAmendCommand builds `satelle story amend <id>` — the gated correction of
// a story's FROZEN definition fields (sty_81aa4d8f). `story set` refuses those
// fields once a story leaves its entry state, and before this the only exit from
// a wrong definition was cancel-and-re-raise: a new id and a full route re-walk.
// Amend keeps the id and the history, and pays for that with a gate: the repo's
// amend_review reviewer judges the correction, the reason is mandatory, and the
// ledger records every field's before and after.
func storyAmendCommand() *cobra.Command {
	var mTitle, mBody, mAccept, mCategory, mReason string
	amend := &cobra.Command{
		Use:   "amend <id>",
		Short: "Amend a story's frozen definition fields under the amend gate (title/body/acceptance/category)",
		Long: `Correct a wrong definition on a story that has already left its entry state.

title, body, acceptance_criteria and category freeze there, so an agent cannot
weaken its own acceptance criteria to make a gate pass. amend does not lift that
freeze — it makes the correction judged: --reason is mandatory, the repo's
amend_review reviewer accepts or rejects it, a reject changes nothing, and an
accepted change records each field's old and new value on the ledger. Declare no
amend_review hook and there is no amend.

Status never moves. amend runs from a performing state and from the parked
(blocked) state, so a story parked over a wrong definition is corrected and
resumed in place; terminal and cancelled are refused. --body and --acceptance
take "-" to read stdin.`,
		Args:        cobra.ExactArgs(1),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			f := cmd.Flags()
			req := map[string]any{"id": args[0]}
			putChanged(req, f, "title", "title")
			putChanged(req, f, "category", "category")
			// body/acceptance may be piped: a corrected AC list is exactly the
			// multi-line value that should not shell-quote through a flag.
			for _, m := range []struct{ flag, key string }{
				{"body", "body"}, {"acceptance", "acceptance_criteria"},
			} {
				if !f.Changed(m.flag) {
					continue
				}
				v, _ := f.GetString(m.flag)
				if v == "-" {
					data, err := io.ReadAll(cmd.InOrStdin())
					if err != nil {
						return fmt.Errorf("amend: read --%s - (stdin): %w", m.flag, err)
					}
					v = string(data)
				}
				req[m.key] = v
			}
			putIf(req, "reason", mReason)
			return dispatch(cmd, "story-amend", req)
		},
	}
	amend.Flags().StringVar(&mTitle, "title", "", "corrected title")
	amend.Flags().StringVar(&mBody, "body", "", `corrected body, or "-" to read it from stdin`)
	amend.Flags().StringVar(&mAccept, "acceptance", "", `corrected acceptance criteria, or "-" to read them from stdin`)
	amend.Flags().StringVar(&mCategory, "category", "", "corrected category (re-lanes the route; refused if the new lane has no such state)")
	amend.Flags().StringVar(&mReason, "reason", "", "why the definition was wrong (required; recorded and judged)")
	_ = amend.MarkFlagRequired("reason")
	return amend
}

// storyDocCommands builds the per-story document attachment surface: attach a
// typed markdown or binary doc, list a story's docs, and read one. Markdown
// dispatches to story-doc-*; binary uses story-doc-attach-binary / get-binary
// with sidecar metadata (sty_40e5a305). Multimodal gate judgment is out of scope.
func storyDocCommands() []*cobra.Command {
	var aName, aType, aBody, aFile, aBinaryFile, aContentType string
	attach := &cobra.Command{
		Use:   "attach <id>",
		Short: "Attach a typed document (markdown or binary) to a story",
		Long: `Attach a typed document to a story.

Markdown (--body / --file) is stored with frontmatter beside the story. Use
--file - to pipe the body in on STDIN, which is how a generated report or a
heredoc of evidence reaches the store without shell-quoting it through --body.
Binary (--binary-file) is stored byte-for-byte with a .satelle.json sidecar;
size cap and content-type allowlist are enforced in the verb (SVG/HTML denied
as executable if served). Gates receive a reference only — multimodal judgment
of binary content is out of scope.`,
		Args:        cobra.ExactArgs(1),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			// The root command tree is registered once (init → register); flag
			// variables persist across invocations. Always key off Changed, never
			// leftover string values from a prior call.
			bodySet := cmd.Flags().Changed("body")
			fileSet := cmd.Flags().Changed("file")
			binSet := cmd.Flags().Changed("binary-file")
			if (bodySet && fileSet) || (bodySet && binSet) || (fileSet && binSet) {
				return fmt.Errorf("attach: --body, --file, and --binary-file are mutually exclusive")
			}
			if binSet {
				ct := ""
				if cmd.Flags().Changed("content-type") {
					ct = aContentType
				}
				return attachBinary(cmd, args[0], aName, aType, aBinaryFile, ct)
			}
			if !bodySet && !fileSet {
				// Name the valid inputs rather than attach an empty document
				// (sty_ef8a896b): an agent that reached for an unsupported input
				// flag otherwise gets a silent empty attachment, not a fix.
				return fmt.Errorf(`attach: one of --body, --file (use "-" for stdin), or --binary-file is required`)
			}
			bodyIn, fileIn := "", ""
			if bodySet {
				bodyIn = aBody
			}
			if fileSet {
				fileIn = aFile
			}
			body, err := attachBody(cmd.InOrStdin(), bodyIn, fileIn)
			if err != nil {
				return err
			}
			req := map[string]any{"story_id": args[0]}
			putIf(req, "name", aName)
			putIf(req, "type", aType)
			putIf(req, "body", body)
			return dispatch(cmd, "story-doc-attach", req)
		},
	}
	attach.Flags().StringVar(&aName, "name", "", "document name (required; binary keeps its extension)")
	attach.Flags().StringVar(&aType, "type", "", "document type (plan|change|output|screenshot|…)")
	attach.Flags().StringVar(&aBody, "body", "", "document markdown body")
	attach.Flags().StringVar(&aFile, "file", "", `read the document body from a file, or "-" for stdin (alternative to --body)`)
	attach.Flags().StringVar(&aBinaryFile, "binary-file", "", "attach a binary file (PNG/PDF/…); mutually exclusive with --body/--file")
	attach.Flags().StringVar(&aContentType, "content-type", "", "MIME type for --binary-file (defaulted from extension when omitted; verb still sniffs)")
	attach.MarkFlagsMutuallyExclusive("body", "file")
	_ = attach.MarkFlagRequired("name")

	docs := &cobra.Command{
		Use:   "docs <id>",
		Short: "List a story's attached documents",
		Long: `List the documents attached to a story — plan, route, change records, step
summaries, and whatever evidence was attached by hand.

Names and types only; read one with satelle story doc <id> <name>. Reach for it
when a gate asks for evidence you are not sure exists: the attachment NAME is
what a presence check looks for.`,
		Args:        cobra.ExactArgs(1),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return dispatch(cmd, "story-doc-list", map[string]any{"story_id": args[0]})
		},
	}

	var docOut string
	var docForce bool
	doc := &cobra.Command{
		Use:   "doc <id> <name>",
		Short: "Read one of a story's attached documents",
		Long: `Read one attached document.

Markdown bodies print as JSON on stdout. Binary attachments never stream raw
bytes to stdout: pass --out <path> to write the file (refuse overwrite unless
--force), or the command fails with guidance.`,
		Args:        cobra.ExactArgs(2),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			if docOut != "" {
				return docWriteOut(cmd, args[0], args[1], docOut, docForce)
			}
			return dispatch(cmd, "story-doc-get", map[string]any{"story_id": args[0], "name": args[1]})
		},
	}
	doc.Flags().StringVar(&docOut, "out", "", "write the document body to this path (required for binary)")
	doc.Flags().BoolVar(&docForce, "force", false, "overwrite --out if the path already exists")

	// route — the story's route AND the reasoning behind every outcome, as one
	// artifact (sty_39e2d9df). Renders the attached document when the story has
	// moved; renders the route live from the governing workflow when it has not,
	// so "what is my route" is answerable from backlog. Read-only.
	route := &cobra.Command{
		Use:   "route <id>",
		Short: "Show a story's route — its ordered steps and the reasoning behind every outcome so far",
		Long: `route renders the single artifact a story carries about its own process.

The plan half is the ordered route: each step, the obligation it discharges, who
performs it under which rubrics, and the reviewers gating entry — including which
gates are present only because the story carries a tag.

The outcome half is appended as steps resolve: each reviewer's verdict, its
reasoning, and a pointer to the full output in the ledger.

Read-only, and answerable without opening any workflow file.`,
		Args:        cobra.ExactArgs(1),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := appFrom(cmd); err != nil {
				return err
			}
			fetchBeforeRead(cmd, args[0])
			body, err := verb.StoryRoute(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			body = withRemoteRunLine(cmd, args[0], body)
			fmt.Fprintln(cmd.OutOrStdout(), strings.TrimRight(body, "\n"))
			return nil
		},
	}

	// lessons — cross-story enumeration of typed lessons/lesson attachments
	// (offline friction corpus; never session-injected).
	lessons := &cobra.Command{
		Use:   "lessons",
		Short: "List typed lessons artifacts across all stories",
		Long: `List every typed lessons artifact across all stories — the accumulated friction
corpus, in one place.

Read it when you want the record of what went wrong before, deliberately: these
artifacts are never session-injected, so nothing surfaces them unless you ask.`,
		Args:        cobra.NoArgs,
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return dispatch(cmd, "story-lessons-list", map[string]any{})
		},
	}
	return []*cobra.Command{attach, docs, doc, route, lessons}
}

// storyStopRequestCommand builds `satelle story stop-request <id> --reason …`
// (sty_8426b9c0 AC5; sty_7b69954a names this as the LIVE-seat preemption path).
// Annotates the engagement lease; never kills a process.
func storyStopRequestCommand() *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "stop-request <id>",
		Short: "Request stop of another agent's engaged story (LIVE seat preemption; arbitrated at next step edge)",
		Long: `Request that another agent stop advancing an engaged story so the engagement seat can free.

This is the preemption path for a LIVE seat held by another agent — not for a stale seat
(use "satelle story seat release <id>" for that). The holder is refused forward engaging
moves at the next step edge and may park (blocked) or terminate; parking frees the seat.
Never cancel a healthy story to free the seat — cancelled is terminal.`,
		Args:        cobra.ExactArgs(1),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return dispatch(cmd, "story-stop-request", map[string]any{"id": args[0], "reason": reason})
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "why the stop is requested")
	return cmd
}

// storyQueueCommand builds `satelle story queue` and `satelle story queue
// withdraw <id> --reason …`: the queue of stories resting at a route wait step,
// read in the order the entries were recorded. Merging an entry is the repo's
// configured release task; this lists and withdraws only.
func storyQueueCommand() *cobra.Command {
	var state string
	cmd := &cobra.Command{
		Use:   "queue",
		Short: "List the open queue entries — stories resting at a wait step — oldest first (read-only)",
		Long: `List the open queue entries, oldest first: one line per story, with the wait
step it rests at, the branch and head sha its tree stood at on entry, and when.

A story is queued when it enters a step its route declares wait = true; the
entry is an event in the story's own history, so the queue needs no remote and
no second store. A release task reads this list in order, merges an entry with
whatever the repo configures, and withdraws it with 'satelle story queue
withdraw <id> --reason "…"'. --json prints {entries: [...]}.`,
		Args:        cobra.NoArgs,
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := json.Marshal(map[string]any{"state": state})
			if err != nil {
				return err
			}
			resp, err := verb.Dispatch(cmd.Context(), "story-queue", body)
			if err != nil {
				return err
			}
			if v, _ := cmd.Flags().GetBool("json"); v {
				return printJSON(cmd, resp)
			}
			var res struct {
				Entries []verb.QueueEntry `json:"entries"`
			}
			if err := json.Unmarshal(resp, &res); err != nil {
				return fmt.Errorf("story queue: decode response: %w", err)
			}
			out := cmd.OutOrStdout()
			if len(res.Entries) == 0 {
				fmt.Fprintln(out, "# queue is empty")
				return nil
			}
			for _, e := range res.Entries {
				fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\n", e.Story, e.State, e.Branch, e.HeadSHA, e.EnqueuedAt.UTC().Format(time.RFC3339))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&state, "state", "", "only entries resting at this wait step")

	var reason string
	withdraw := &cobra.Command{
		Use:   "withdraw <id> --reason \"…\"",
		Short: "Withdraw a story's open queue entry; the reason is recorded on the story",
		Long: `Close a story's open queue entry. The reason is required and is recorded as an
event in the story's history. The story's status is untouched: moving it on
from the wait step is the workflow's job.`,
		Args:        cobra.ExactArgs(1),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return dispatch(cmd, "story-queue-withdraw", map[string]any{"id": args[0], "reason": reason})
		},
	}
	withdraw.Flags().StringVar(&reason, "reason", "", "why the entry is withdrawn (required)")
	cmd.AddCommand(withdraw)
	return cmd
}

// storyWorktreeCommand builds `satelle story worktree <id>` (sty_804c566b): open
// the git worktree a story is engaged from and carry into it the gitignored
// paths the repo's [worktree] declaration names. satelle holds no convention —
// the branch and location templates and the include list are the repo's config.
func storyWorktreeCommand() *cobra.Command {
	var base, branch, path, existing, trunkBranch string
	cmd := &cobra.Command{
		Use:   "worktree <id> --base <ref>",
		Short: "Open a git worktree for a story, carrying the gitignored paths the repo declares it needs",
		Long: `Open the worktree a story is engaged from, carrying the gitignored paths the
repo's [worktree] table declares it needs, linked from the main tree.

--base is required and never defaults to HEAD: the epic's base branch, or trunk,
or a dependency's branch. A trunk base is first brought level with the remote;
--trunk-branch names the trunk when the remote's HEAD ref does not. --branch and
--path override the repo's templates.

--existing <path> applies the declaration to a worktree that already exists,
creating nothing. See satelle help worktree.`,
		Args:        cobra.ExactArgs(1),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := map[string]any{"id": args[0]}
			f := cmd.Flags()
			putChanged(req, f, "base", "base")
			putChanged(req, f, "branch", "branch")
			putChanged(req, f, "path", "path")
			putChanged(req, f, "existing", "existing")
			putChanged(req, f, "trunk-branch", "trunk_branch")
			return dispatch(cmd, "story-worktree", req)
		},
	}
	cmd.Flags().StringVar(&base, "base", "", "ref the new worktree's branch is cut from (required unless --existing)")
	cmd.Flags().StringVar(&branch, "branch", "", "new branch name (default: the [worktree] branch template)")
	cmd.Flags().StringVar(&path, "path", "", "worktree location (default: the [worktree] path template)")
	cmd.Flags().StringVar(&existing, "existing", "", "bring an existing worktree up to the declaration instead of creating one")
	cmd.Flags().StringVar(&trunkBranch, "trunk-branch", "", "trunk branch when the remote's HEAD ref does not name one (default: [trunk] branch)")
	return cmd
}

// storyRecoverCommand builds `satelle story recover <id> [--choice …]`
// (sty_f0ed2473): after a dispatch ends without a completion, report what it
// left; with --choice, record the driver's decision as one ledger row.
func storyRecoverCommand() *cobra.Command {
	var choice, reason string
	cmd := &cobra.Command{
		Use:   "recover <id>",
		Short: "Report what a dispatch that ended without a completion left; record the recovery choice",
		Long: `Report what a dispatch that ended without a completion left behind: its last
recorded event and the wall time since, and the files present since the
engagement anchor. Reach for it when a dispatch stalled, died or was reaped and
you must decide what to do next.

It REPORTS; it does not VOUCH. The transition did not commit and the work is
unverified; the file list is an enumeration, not a coherence check. Not a
resume: no live session is re-entered.

--choice redispatch|finish|park records your decision as one recovery_choice
ledger row and stops. It never dispatches, transitions or parks.

See: satelle help agent-dispatch`,
		Args:        cobra.ExactArgs(1),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := map[string]any{"id": args[0]}
			putIf(req, "choice", choice)
			putIf(req, "reason", reason)
			fetchBeforeWrite(cmd, args[0])
			resp, err := verb.Dispatch(cmd.Context(), "story-recover", mustJSON(req))
			if err != nil {
				return err
			}
			if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
				return printJSON(cmd, resp)
			}
			var res struct {
				Report string `json:"report"`
			}
			if err := json.Unmarshal(resp, &res); err != nil {
				return printJSON(cmd, resp)
			}
			fmt.Fprint(cmd.OutOrStdout(), res.Report)
			return nil
		},
	}
	cmd.Flags().StringVar(&choice, "choice", "", "record the recovery decision: redispatch, finish or park")
	cmd.Flags().StringVar(&reason, "reason", "", "why this choice (kept on the ledger row)")
	return cmd
}

// storySeatCommands builds `satelle story seat` (list) and
// `satelle story seat release <id>` — the agent/operator surface for the
// engagement seat (sty_1738f973 AC4).
func storySeatCommands() []*cobra.Command {
	seat := &cobra.Command{
		Use:   "seat",
		Short: "List engagement seat leases (reaps stale rows first)",
		Long: `List the engagement seat leases: who holds one, from which working tree, and how
old the heartbeat is. Stale rows are reaped before the list is rendered, so what
you see is live.

Reach for it when an engagement is refused. The heartbeat age is the reading
that matters: a LIVE holder is another agent still working, and the path is
stop-request; a STALE one is nobody, and the path is seat release.`,
		Args:        cobra.NoArgs,
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return dispatch(cmd, "story-seat-list", map[string]any{})
		},
	}
	release := &cobra.Command{
		Use:   "release <id>",
		Short: "Force-release an engagement seat by story/task id",
		Long: `Force-release an engagement seat by the id holding it.

For a STALE seat — no live agent behind it, which the heartbeat age in satelle
story seat shows. Releasing a LIVE holder's seat abandons work another agent
believes it is still doing: ask for it with stop-request instead, which lets the
holder park itself. Never cancel a healthy story to free a seat.`,
		Args:        cobra.ExactArgs(1),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return dispatch(cmd, "story-seat-release", map[string]any{"id": args[0]})
		},
	}
	seat.AddCommand(release)
	return []*cobra.Command{seat}
}

// storyMessageCommand is `satelle story message <id> --from <role> [--to <addressee>] --body <text>`.
func storyMessageCommand() *cobra.Command {
	var from, to, body, deadline, replyTo string
	cmd := &cobra.Command{
		Use:   "message <id>",
		Short: "Leave a message for a story, a role or a session on the board",
		Long: `Write a message onto a story's ledger (kind agent_message). It gets a msg_ id,
records the sending session's id, name and location, and shows on satelle board
with its state until it is answered.

--from is the sending role (orchestrator, executor, human, …). --to is story
(whoever holds it), role:<r>, session:<id>, or * (everyone, the default); a
bare role name means role:<name>. The next gate or performer payload carries
matching role messages in messages[], marking them delivered.

--deadline is a duration (30m) or RFC3339; unanswered past it is overdue.
--reply-to <msg_id> marks that message answered. A body is at most 64 KiB and
is data, never instructions.`,
		Args:        cobra.ExactArgs(1),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := map[string]any{"id": args[0], "from": from, "body": body}
			if to != "" {
				req["to"] = to
			}
			if deadline != "" {
				req["deadline"] = deadline
			}
			if replyTo != "" {
				req["in_reply_to"] = replyTo
			}
			id, name, location := senderIdentity()
			if id != "" {
				req["from_session_id"] = id
			}
			if name != "" {
				req["from_session_name"] = name
			}
			if location != "" {
				req["from_location"] = location
			}
			return dispatch(cmd, "story-message", req)
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "sending role (required)")
	cmd.Flags().StringVar(&to, "to", "", "story | role:<r> | session:<id> | * | a bare role (default *)")
	cmd.Flags().StringVar(&body, "body", "", "message body (required, at most 64 KiB)")
	cmd.Flags().StringVar(&deadline, "deadline", "", "duration from now (30m) or RFC3339; unanswered past it is overdue")
	cmd.Flags().StringVar(&replyTo, "reply-to", "", "msg_ id this answers; marks it answered")
	_ = cmd.MarkFlagRequired("from")
	_ = cmd.MarkFlagRequired("body")
	return cmd
}

// storyMessagesCommand is `satelle story messages <id> [--since RFC3339] [--to role]`.
func storyMessagesCommand() *cobra.Command {
	var since, to, state string
	var overdue bool
	cmd := &cobra.Command{
		Use:   "messages <id>",
		Short: "List the messages on a story with their state (read-only)",
		Long: `List agent_message ledger rows for a story, oldest first, each with its sender
(from, from_session_name), addressee (to), state (waiting, delivered, answered
or undeliverable), deadline and an overdue flag. Overdue is derived at read
time: the deadline has passed and the message is not answered.

Read-only — a dispatched agent may call this under Bash(satelle:*). --to keeps
that role plus *. --state keeps one state; --overdue keeps the overdue ones.
--since is RFC3339. No engagement window (the payload already applied that).
For every story at once, see satelle board.`,
		Args:        cobra.ExactArgs(1),
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := map[string]any{"id": args[0]}
			if since != "" {
				req["since"] = since
			}
			if to != "" {
				req["to"] = to
			}
			if state != "" {
				req["state"] = state
			}
			if overdue {
				req["overdue"] = true
			}
			return dispatch(cmd, "story-messages", req)
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "RFC3339 lower bound on created_at")
	cmd.Flags().StringVar(&to, "to", "", "receiving role (plus *)")
	cmd.Flags().StringVar(&state, "state", "", "only this state: waiting | delivered | answered | undeliverable")
	cmd.Flags().BoolVar(&overdue, "overdue", false, "only messages past their deadline and not answered")
	return cmd
}

// Help for the shared story/task/execution surface. One factory builds the three
// groups, so their help is written once and PARAMETERISED — the constraints that
// genuinely differ (seat arbitration and the definition freeze are story-only,
// --parent is execution-only) are named per group rather than repeated three
// times with two of them wrong (sty_a499e7f5).

func groupParentLong(group string) string {
	switch group {
	case "task":
		return `A task is an authored HEADER — a re-runnable definition of work and how its
success is verified — not a running lifecycle. The markdown file under
.satelle/tasks is the source of truth; the database only indexes it.

Each RUN of a task is an execution: satelle execution create --parent <tsk_id>.
Retire a superseded header with archive, which is record disposition, not a
workflow status.`
	case "execution":
		return `An execution is one isolated RUN of a task. Create it with --parent <tsk_id>:
without that parent it has no definition to run and no validators to gate it.

The run carries the lifecycle (its before/after validators gate entry and
close) while the task header it points at stays a stable definition. Record the
run output with satelle execution record <exe_id>.`
	}
	return `A story is the unit of work satelle governs: it carries the goal, the
acceptance criteria, and the route it walks to done. Edits to the repo require
one engaged in a performing state — that is what the edit gate enforces.

Reach for create/set to drive the lifecycle, get/list to read, attach/doc/docs
to carry evidence, and route to see the steps and every verdict so far. The
category chosen at create selects the route; see satelle help create-story.`
}

func groupCreateLong(group string) string {
	switch group {
	case "task":
		return `Create a task header — the definition, not a run. Use it when the work is
repeatable and you want one place that says what to do and how success is
checked; use story create for a one-off slice with acceptance criteria.

--title is required. The header is written as authored markdown under
.satelle/tasks and indexed from there, so the file remains the source of truth.`
	case "execution":
		return `Create an execution: one isolated run of an existing task.

--parent <tsk_id> is what makes it a run rather than an orphan, and the task
header it names supplies both the action and the validators that gate the run.
--input name=value (repeatable) sets an input the task declares in inputs_decl;
a missing required, undeclared or mistyped input refuses the create.
Record what the run produced with satelle execution record.`
	}
	return `Create a story. Reach for it before touching the repo: an edit needs an
engaged story, and engagement starts here.

--category is not cosmetic — it selects the ROUTE the story will walk, and it
freezes once the story leaves its entry state, so a wrong category then costs a
gated story amend at best. When [review] gate_create is on, the create-review
gate judges the draft and can REJECT it: the story is then not created, and the
notes say what to fix.`
}

func groupGetLong(group string) string {
	return `Print one ` + group + ` by id as JSON — every field, including tags and the
acceptance criteria. Reach for it when you need the full record; list gives you
the rows, this gives you the item.

Read-only: no gate runs and nothing is recorded.`
}

func groupListLong(group, plural string) string {
	return `List ` + plural + ` as JSON, newest first, capped at 500 rows unless --limit says
otherwise. Reach for it to find an id or survey state; use get for one item.

--tag matches a tag EXACTLY, and in a multi-value namespace it is an ANY-match:
--tag sprint:4 returns an item carrying sprint:4 among others.`
}

func groupSetLong(group string) string {
	if group == "story" {
		return `Update a story: only the flags you pass change, so a status move never
rewrites the body by accident.

--status is a workflow transition, not an assignment. Its gates run and can
REFUSE it, and a status that skips a step is refused before any gate. Called by
an agent, a gate that outlasts the call returns a handle and delivers its verdict
by notification: never poll (satelle help agent-dispatch). Engaging statuses
also take the engagement seat, which is arbitrated per project.
title/body/acceptance/category are FROZEN once the story leaves its entry
state; status, tags, priority, estimate and actual stay mutable. --tags
replaces the whole set, while --add-tags/--remove-tags are additive and may not
be combined with it.`
	}
	return `Update a ` + group + `: only the flags you pass change, so a status move never
rewrites the body by accident.

--status is a workflow transition whose gates run and can REFUSE it; a status
that skips a step is refused before any gate. --tags replaces the whole tag set,
while --add-tags/--remove-tags are additive and may not be combined with it.`
}
