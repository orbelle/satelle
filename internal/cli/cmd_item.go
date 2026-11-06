package cli

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/cache"
	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/kernel"
)

// itemCmd is the command group for one item kind. Every kind is worked alike
// in its workflow (get, list, set and the work verbs); a creatable kind adds
// create, and task adds run (the only way an execution is created).
func itemCmd(kind string, creatable bool) *cobra.Command {
	cmd := &cobra.Command{Use: kind, Short: fmt.Sprintf("Work with %s items", kind)}
	cmd.AddCommand(itemGetCmd(kind), itemListCmd(kind), itemSetCmd(kind),
		attachCmd(kind), attachmentsCmd(kind), attachmentCmd(kind), routeCmd(kind), diffCmd(kind),
		seatCmd(kind), releaseCmd(kind), performCmd(kind), collectCmd(kind))
	if creatable {
		cmd.AddCommand(itemCreateCmd(kind))
	}
	if kind == "task" {
		cmd.AddCommand(taskRunCmd())
	}
	return cmd
}

// getItem loads id and checks it is of kind.
func getItem(k *kernel.Kernel, kind, id string) (item.Item, error) {
	it, err := k.Get(id)
	if err != nil {
		return it, err
	}
	if it.Kind != kind {
		return it, fmt.Errorf("%s is a %s, not a %s", id, it.Kind, kind)
	}
	return it, nil
}

// fieldFlags are the item fields create and set take.
type fieldFlags struct {
	title, body, category, parent, workflow, assignee string
	criteria, tags, addTags, removeTags               []string
	inKind, inStatus, inTag, inCategory               string
}

func (f *fieldFlags) register(cmd *cobra.Command, create bool) {
	fl := cmd.Flags()
	fl.StringVar(&f.title, "title", "", "title")
	fl.StringVar(&f.body, "body", "", "body (markdown); '-' reads stdin")
	fl.StringArrayVar(&f.criteria, "criteria", nil, "an acceptance criterion (repeatable; replaces the list on set)")
	fl.StringArrayVar(&f.tags, "tag", nil, "a tag (repeatable; replaces the list on set)")
	fl.StringVar(&f.category, "category", "", "category")
	fl.StringVar(&f.parent, "parent", "", "parent item id")
	fl.StringVar(&f.assignee, "assignee", "", "assignee (carried, no behaviour)")
	fl.StringVar(&f.inKind, "input-kind", "", "task input selection: item kind")
	fl.StringVar(&f.inStatus, "input-status", "", "task input selection: status")
	fl.StringVar(&f.inTag, "input-tag", "", "task input selection: tag")
	fl.StringVar(&f.inCategory, "input-category", "", "task input selection: category")
	if create {
		fl.StringVar(&f.workflow, "workflow", "", "workflow (default: the first matching rule, else the default)")
	} else {
		fl.StringArrayVar(&f.addTags, "add-tag", nil, "add a tag (repeatable)")
		fl.StringArrayVar(&f.removeTags, "remove-tag", nil, "remove a tag (repeatable)")
	}
}

// fields returns the Fields the flags set; cur is the item being updated (nil
// on create).
func (f *fieldFlags) fields(cmd *cobra.Command, cur *item.Item) (item.Fields, error) {
	var out item.Fields
	changed := cmd.Flags().Changed
	str := func(name string, v string, dst **string) {
		if changed(name) {
			s := v
			*dst = &s
		}
	}
	str("title", f.title, &out.Title)
	str("category", f.category, &out.Category)
	str("parent", f.parent, &out.Parent)
	str("assignee", f.assignee, &out.Assignee)
	str("workflow", f.workflow, &out.Workflow)
	if changed("body") {
		body := f.body
		if body == "-" {
			b, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return out, err
			}
			body = string(b)
		}
		out.Body = &body
	}
	if changed("criteria") {
		c := append([]string{}, f.criteria...)
		out.Criteria = &c
	}
	if changed("tag") || changed("add-tag") || changed("remove-tag") {
		var tags []string
		if changed("tag") {
			tags = append(tags, f.tags...)
		} else if cur != nil {
			tags = append(tags, cur.Tags...)
		}
		for _, t := range f.addTags {
			if !contains(tags, t) {
				tags = append(tags, t)
			}
		}
		kept := tags[:0]
		for _, t := range tags {
			if !contains(f.removeTags, t) {
				kept = append(kept, t)
			}
		}
		out.Tags = &kept
	}
	if changed("input-kind") || changed("input-status") || changed("input-tag") || changed("input-category") {
		sel := item.Selection{}
		if cur != nil && cur.Inputs != nil {
			sel = *cur.Inputs
		}
		if changed("input-kind") {
			sel.Kind = f.inKind
		}
		if changed("input-status") {
			sel.Status = f.inStatus
		}
		if changed("input-tag") {
			sel.Tag = f.inTag
		}
		if changed("input-category") {
			sel.Category = f.inCategory
		}
		out.Inputs = &sel
	}
	return out, nil
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func itemCreateCmd(kind string) *cobra.Command {
	var f fieldFlags
	var asJSON *bool
	cmd := &cobra.Command{
		Use:   "create --title T",
		Short: fmt.Sprintf("Create a %s in its workflow's start state", kind),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()
			fields, err := f.fields(cmd, nil)
			if err != nil {
				return err
			}
			kd := kind
			fields.Kind = &kd
			it, err := k.Create(fields, actor())
			if err != nil {
				return err
			}
			if *asJSON {
				return writeJSON(cmd.OutOrStdout(), it)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s created (%s, workflow %s)\n", it.ID, it.Status, it.Workflow)
			return nil
		},
	}
	f.register(cmd, true)
	asJSON = jsonFlag(cmd)
	return cmd
}

func itemGetCmd(kind string) *cobra.Command {
	var asJSON *bool
	cmd := &cobra.Command{
		Use:   "get <id>",
		Short: fmt.Sprintf("Show a %s", kind),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()
			it, err := getItem(k, kind, args[0])
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			atts, err := k.Cache.Attachments(it.ID)
			if err != nil {
				return err
			}
			seats, err := k.Cache.Seats("", it.ID)
			if err != nil {
				return err
			}
			if *asJSON {
				return writeJSON(out, struct {
					item.Item
					Attachments []item.Attachment `json:"attachments"`
					Seats       []item.Seat       `json:"seats"`
				}{it, atts, seats})
			}
			printItem(out, it)
			for _, a := range atts {
				fmt.Fprintf(out, "attachment %s (%s, %d bytes)\n", a.Name, a.MediaType, a.Size)
			}
			for _, s := range seats {
				fmt.Fprintf(out, "seat       %s at %s\n", s.Session, s.Location)
			}
			return nil
		},
	}
	asJSON = jsonFlag(cmd)
	return cmd
}

func printItem(w io.Writer, it item.Item) {
	fmt.Fprintf(w, "%s  %s\n", it.ID, it.Title)
	fmt.Fprintf(w, "kind       %s\nstatus     %s\nworkflow   %s\n", it.Kind, it.Status, it.Workflow)
	for _, kv := range [][2]string{{"category", it.Category}, {"parent", it.Parent}, {"assignee", it.Assignee}, {"tags", strings.Join(it.Tags, ", ")}} {
		if kv[1] != "" {
			fmt.Fprintf(w, "%-10s %s\n", kv[0], kv[1])
		}
	}
	if it.Inputs != nil {
		fmt.Fprintf(w, "inputs     kind=%q status=%q tag=%q category=%q\n", it.Inputs.Kind, it.Inputs.Status, it.Inputs.Tag, it.Inputs.Category)
	}
	if it.Body != "" {
		fmt.Fprintf(w, "\n%s\n", strings.TrimRight(it.Body, "\n"))
	}
	if len(it.Criteria) > 0 {
		fmt.Fprintln(w, "\nacceptance criteria")
		for i, c := range it.Criteria {
			fmt.Fprintf(w, "  %d. %s\n", i+1, c)
		}
	}
}

func itemListCmd(kind string) *cobra.Command {
	var q cache.Query
	var asJSON *bool
	var onlyOpen bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: fmt.Sprintf("List %s items", kind),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()
			q.Kind = kind
			items, err := k.List(q)
			if err != nil {
				return err
			}
			if onlyOpen {
				kept := items[:0]
				for _, it := range items {
					if w := k.Workflows[it.Workflow]; w == nil || !w.IsTerminal(it.Status) {
						kept = append(kept, it)
					}
				}
				items = kept
			}
			if *asJSON {
				if items == nil {
					items = []item.Item{}
				}
				return writeJSON(cmd.OutOrStdout(), items)
			}
			for _, it := range items {
				fmt.Fprintf(cmd.OutOrStdout(), "%-14s %-14s %s\n", it.ID, it.Status, it.Title)
			}
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&q.Status, "status", "", "only this status")
	fl.StringVar(&q.Tag, "tag", "", "only items with this tag")
	fl.StringVar(&q.Category, "category", "", "only this category")
	fl.StringVar(&q.Parent, "parent", "", "only children of this item")
	fl.StringVar(&q.Workflow, "workflow", "", "only items under this workflow")
	fl.BoolVar(&onlyOpen, "open", false, "only items not in a terminal state of their workflow")
	asJSON = jsonFlag(cmd)
	return cmd
}

func itemSetCmd(kind string) *cobra.Command {
	var f fieldFlags
	var status string
	var asJSON *bool
	cmd := &cobra.Command{
		Use:   "set <id>",
		Short: fmt.Sprintf("Update a %s's fields, or present a transition with --status", kind),
		Long: `set updates the fields its flags name. --status presents the transition from
the item's current state to the given one: its gates run in their configured
order and every result is recorded on the ledger. The item moves only when no
gate rejects; otherwise set exits 2 and lists each rejecting gate with its
notes.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()
			it, err := getItem(k, kind, args[0])
			if err != nil {
				return err
			}
			fields, err := f.fields(cmd, &it)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if fields != (item.Fields{}) {
				if it, err = k.Update(it.ID, fields, actor()); err != nil {
					return err
				}
				if !cmd.Flags().Changed("status") && !*asJSON {
					fmt.Fprintf(out, "%s updated\n", it.ID)
				}
			}
			if !cmd.Flags().Changed("status") {
				if *asJSON {
					return writeJSON(out, it)
				}
				return nil
			}
			res, err := k.Transition(cmd.Context(), it.ID, status, actor())
			if err != nil {
				return err
			}
			return reportTransition(out, res, *asJSON)
		},
	}
	f.register(cmd, false)
	cmd.Flags().StringVar(&status, "status", "", "present the transition to this state")
	asJSON = jsonFlag(cmd)
	return cmd
}

// reportTransition prints a transition's gate results; a rejected move is an
// ExitBlocked error naming every rejecting gate and its notes.
func reportTransition(w io.Writer, res kernel.TransitionResult, asJSON bool) error {
	if asJSON {
		if err := writeJSON(w, res); err != nil {
			return err
		}
	} else {
		for _, g := range res.Gates {
			fmt.Fprintf(w, "gate %-24s %-8s %s\n", g.Gate, g.Result, firstLine(g.Notes))
			if g.Warning != "" {
				fmt.Fprintf(w, "  warning: %s\n", g.Warning)
			}
		}
		if res.Moved {
			fmt.Fprintf(w, "%s: %s -> %s\n", res.Item.ID, res.From, res.To)
		}
	}
	if res.Moved {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s did not move %s -> %s: rejected by", res.Item.ID, res.From, res.To)
	for _, g := range res.Rejects() {
		fmt.Fprintf(&b, "\n  gate %s", g.Gate)
		if g.Binding != "" {
			fmt.Fprintf(&b, " (%s)", g.Binding)
		}
		if n := strings.TrimSpace(g.Notes); n != "" {
			fmt.Fprintf(&b, ":\n    %s", strings.ReplaceAll(n, "\n", "\n    "))
		}
	}
	return &exitError{code: ExitBlocked, msg: b.String()}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	return s
}

func attachCmd(kind string) *cobra.Command {
	var name, file, mediaType string
	cmd := &cobra.Command{
		Use:   "attach <id> --name N --file F",
		Short: "Attach a document to an item (recorded on the ledger)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()
			if _, err := getItem(k, kind, args[0]); err != nil {
				return err
			}
			var data []byte
			if file == "-" {
				data, err = io.ReadAll(cmd.InOrStdin())
			} else {
				data, err = os.ReadFile(file)
			}
			if err != nil {
				return err
			}
			if name == "" && file != "-" {
				name = filepath.Base(file)
			}
			if mediaType == "" && file != "-" {
				mediaType = mime.TypeByExtension(filepath.Ext(file))
			}
			a, err := k.Attach(args[0], name, data, mediaType, actor())
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "attached %s to %s (%d bytes, sha256 %s)\n", a.Name, args[0], a.Size, a.SHA256[:12])
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "attachment name (default: the file's base name)")
	cmd.Flags().StringVar(&file, "file", "", "file to attach; '-' reads stdin")
	cmd.Flags().StringVar(&mediaType, "type", "", "media type (default: from the file extension)")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

func attachmentsCmd(kind string) *cobra.Command {
	var asJSON *bool
	cmd := &cobra.Command{
		Use:   "attachments <id>",
		Short: "List an item's attachments",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()
			if _, err := getItem(k, kind, args[0]); err != nil {
				return err
			}
			atts, err := k.Cache.Attachments(args[0])
			if err != nil {
				return err
			}
			if *asJSON {
				if atts == nil {
					atts = []item.Attachment{}
				}
				return writeJSON(cmd.OutOrStdout(), atts)
			}
			for _, a := range atts {
				fmt.Fprintf(cmd.OutOrStdout(), "%-24s %-24s %8d  %s  %s\n", a.Name, a.MediaType, a.Size, a.At.Local().Format("2006-01-02 15:04"), a.SHA256[:12])
			}
			return nil
		},
	}
	asJSON = jsonFlag(cmd)
	return cmd
}

// attachmentCmd prints one version of an attachment's body, byte for byte.
func attachmentCmd(kind string) *cobra.Command {
	var sha string
	cmd := &cobra.Command{
		Use:   "attachment <id> <name>",
		Short: "Print an attachment's body (the newest version, or --sha)",
		Long: `attachment prints the body of an item's attachment exactly as stored: the
newest version of the name, or the version whose sha256 is --sha (the full
sha256 or a unique hex prefix of at least 7 characters).`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()
			if _, err := getItem(k, kind, args[0]); err != nil {
				return err
			}
			_, body, err := k.AttachmentBody(args[0], args[1], sha)
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(body)
			return err
		},
	}
	cmd.Flags().StringVar(&sha, "sha", "", "the version: its sha256 or a unique prefix of at least 7 hex characters")
	return cmd
}

func seatCmd(kind string) *cobra.Command {
	var session, location, base string
	cmd := &cobra.Command{
		Use:   "seat <id>",
		Short: "Bind this session, at this worktree, to an item",
		Long: `seat binds a session at a location to an item. While the item's current
state has a performer, the edit gate allows the seated session's edits at that
location. The session defaults to $SATELLE_SESSION (the context hook prints
it); the location defaults to local:<this worktree>.

The seat records the item's diff base: the commit its gates judge the change
from. It is HEAD unless --base names a branch, tag or commit (resolved now, and
it must be in HEAD's history). Seat a stacked branch with --base <the branch it
is stacked on> so its gates judge only its own change.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, r, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()
			if _, err := getItem(k, kind, args[0]); err != nil {
				return err
			}
			if session == "" {
				return errors.New("no session: pass --session or set SATELLE_SESSION")
			}
			if location == "" {
				location = localLocation(r.Tree)
			}
			s, err := k.Seat(args[0], session, location, base, actor())
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "seated %s on %s at %s", s.Session, s.Item, s.Location)
			if s.Base != "" {
				fmt.Fprintf(cmd.OutOrStdout(), " (base %s)", short(s.Base))
			}
			fmt.Fprintln(cmd.OutOrStdout())
			return nil
		},
	}
	cmd.Flags().StringVar(&session, "session", sessionFromEnv(), "session (default $SATELLE_SESSION)")
	cmd.Flags().StringVar(&location, "location", "", "local:<path>, remote:<node> or cloud:<url> (default: this worktree)")
	cmd.Flags().StringVar(&base, "base", "", "the commit the item's diff is judged from: a branch, tag or SHA, resolved now (default HEAD)")
	return cmd
}

func releaseCmd(kind string) *cobra.Command {
	var session string
	cmd := &cobra.Command{
		Use:   "release <id>",
		Short: "Release this session's seat on an item",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()
			if _, err := getItem(k, kind, args[0]); err != nil {
				return err
			}
			if session == "" {
				return errors.New("no session: pass --session or set SATELLE_SESSION")
			}
			if err := k.Release(args[0], session, actor()); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "released %s from %s\n", session, args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&session, "session", sessionFromEnv(), "session (default $SATELLE_SESSION)")
	return cmd
}

func performCmd(kind string) *cobra.Command {
	return &cobra.Command{
		Use:   "perform <id>",
		Short: "Dispatch the performer of the item's current state",
		Long: `perform dispatches the performer of the item's current state where its binding
is placed: here (local), on a node of a remote (remote:<id>, through the bound
project), or in a cloud session (cloud), whose branch 'collect' records.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, r, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()
			release, err := withRemote(k, r)
			if err != nil {
				return err
			}
			defer release()
			if _, err := getItem(k, kind, args[0]); err != nil {
				return err
			}
			res, err := k.Perform(cmd.Context(), args[0], actor())
			if res.Text != "" {
				fmt.Fprintln(cmd.OutOrStdout(), strings.TrimRight(res.Text, "\n"))
			}
			return err
		},
	}
}

func taskRunCmd() *cobra.Command {
	var asJSON *bool
	var base string
	cmd := &cobra.Command{
		Use:   "run <id>",
		Short: "Run a task once: create an execution over its input selection",
		Long: `run creates an execution over the task's input selection and records its
base: the commit the execution's diff is judged from, printed with the branch it
came from. It is HEAD unless --base names a branch, tag or SHA (resolved now). A
run worked on another branch than the one it is cut on, such as a release worked
on main, passes --base <that branch>.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()
			if _, err := getItem(k, "task", args[0]); err != nil {
				return err
			}
			exec, rec, err := k.TaskRun(args[0], base, actor())
			if err != nil {
				return err
			}
			if *asJSON {
				return writeJSON(cmd.OutOrStdout(), struct {
					item.Item
					TaskRun kernel.TaskRunRecord `json:"task_run"`
				}{exec, rec})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s created (%s) over %d items", exec.ID, exec.Status, len(rec.Inputs))
			if exec.Base != "" {
				from := rec.BaseRef
				if from == "" {
					from = "detached HEAD"
				}
				fmt.Fprintf(cmd.OutOrStdout(), ", base %s (%s)", short(exec.Base), from)
			}
			fmt.Fprintln(cmd.OutOrStdout())
			for _, id := range rec.Inputs {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", id)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&base, "base", "", "the commit the execution's diff is judged from: a branch, tag or SHA, resolved now (default HEAD)")
	asJSON = jsonFlag(cmd)
	return cmd
}

// gateResults returns the item's recorded gate results, oldest first.
func gateResults(k *kernel.Kernel, id string) ([]gate.Result, error) {
	events, err := k.Ledger.Read(id)
	if err != nil {
		return nil, err
	}
	var out []gate.Result
	for _, e := range events {
		var r gate.Result
		if e.Kind == gate.KindRun && e.Decode(&r) == nil {
			out = append(out, r)
		}
	}
	return out, nil
}
