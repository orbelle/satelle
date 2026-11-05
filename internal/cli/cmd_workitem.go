package cli

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/spf13/cobra"
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
			return dispatch(cmd, group+"-create", req)
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
			_, err := dispatchResponse(cmd, group+"-set", req)
			return err
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
		parent.AddCommand(storySeatCommands()...)
		parent.AddCommand(storyDriveCommand())
	}
	return parent
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

	return []*cobra.Command{attach, docs, doc}
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
category chosen at create selects the route; see satelle help workflows.`
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
