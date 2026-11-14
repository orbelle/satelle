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
	"github.com/spf13/pflag"

	"github.com/bobmcallan/satelle/internal/cache"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/procgroup"
	"github.com/bobmcallan/satelle/internal/seatlife"
)

// itemCmd is the command group for one item kind. Every kind is worked alike
// in its workflow (get, list, set and the work verbs); a creatable kind adds
// create, and task adds run (the only way an execution is created).
func itemCmd(kind string, creatable bool) *cobra.Command {
	cmd := &cobra.Command{Use: kind, Short: fmt.Sprintf("Work with %s items", kind)}
	cmd.AddCommand(itemGetCmd(kind), itemListCmd(kind), itemSetCmd(kind),
		attachCmd(kind), recordCmd(kind), attachmentsCmd(kind), attachmentCmd(kind), routeCmd(kind), diffCmd(kind),
		seatCmd(kind), releaseCmd(kind), performCmd(kind), collectCmd(kind))
	if creatable {
		cmd.AddCommand(itemCreateCmd(kind))
	}
	if kind == "task" {
		cmd.AddCommand(taskRunCmd())
	}
	if kind == item.QueueKind {
		cmd.AddCommand(queueAddCmd(), queueRemoveCmd(), queueFireCmd())
	}
	return cmd
}

// getItem loads id and checks it is of kind.
func getItem(k *kernel.Kernel, kind, id string) (item.Item, error) {
	if kind == item.QueueKind {
		id, _ = k.ResolveQueue(id)
	}
	it, err := k.Get(id)
	if err != nil {
		return it, err
	}
	if it.Kind != kind {
		return it, fmt.Errorf("%s is a %s, not a %s", id, it.Kind, kind)
	}
	return it, nil
}

// resolveItem is getItem's id: a queue's name becomes its chain id.
func resolveItem(k *kernel.Kernel, kind, ref string) (string, error) {
	it, err := getItem(k, kind, ref)
	return it.ID, err
}

// fieldFlags are the item fields create and set take.
type fieldFlags struct {
	title, body, category, parent, workflow  string
	criteria, tags, addTags, removeTags      []string
	addCriteria, removeCriteria              []string
	assignees, addAssignees, removeAssignees []string
	replace                                  bool
	inKind, inStatus, inTag, inCategory      string
	clearInputs                              bool
	trigTask, trigWorkflow, trigBase         string
	minSize                                  int
}

func (f *fieldFlags) register(cmd *cobra.Command, kind string, create bool) {
	fl := cmd.Flags()
	if kind == item.QueueKind {
		fl.StringVar(&f.trigTask, "trigger-task", "", "the task the queue's trigger runs (an execution under it)")
		fl.StringVar(&f.trigWorkflow, "trigger-workflow", "", "the workflow the queue's trigger starts a run in")
		fl.StringVar(&f.trigBase, "trigger-base", "", "the ref a fired run's base comes from (default HEAD)")
		fl.IntVar(&f.minSize, "min-size", 0, "fire when an enqueue leaves this many members (0: only on queue fire)")
	}
	if !create && (kind == "task" || kind == item.QueueKind) {
		fl.BoolVar(&f.clearInputs, "clear-inputs", false, "clear the input selection")
	}
	fl.StringVar(&f.title, "title", "", "title")
	fl.StringVar(&f.body, "body", "", "body (markdown); '-' reads stdin")
	fl.StringArrayVar(&f.criteria, "criteria", nil, "an acceptance criterion (repeatable; on set, replaces the list only with --replace)")
	fl.StringArrayVar(&f.tags, "tag", nil, "a tag (repeatable; on set, replaces the list only with --replace)")
	fl.StringVar(&f.category, "category", "", "category")
	fl.StringVar(&f.parent, "parent", "", "parent item id")
	fl.StringArrayVar(&f.assignees, "assignee", nil, "an assignee (repeatable; on set, replaces the list only with --replace)")
	fl.StringVar(&f.inKind, "input-kind", "", "input selection (a task's inputs, a queue's filter): item kind")
	fl.StringVar(&f.inStatus, "input-status", "", "input selection (a task's inputs, a queue's filter): status")
	fl.StringVar(&f.inTag, "input-tag", "", "input selection (a task's inputs, a queue's filter): tag")
	fl.StringVar(&f.inCategory, "input-category", "", "input selection (a task's inputs, a queue's filter): category")
	if create {
		fl.StringVar(&f.workflow, "workflow", "", "workflow (default: the first matching rule, else the default)")
	} else {
		fl.StringArrayVar(&f.addTags, "add-tag", nil, "add a tag (repeatable)")
		fl.StringArrayVar(&f.removeTags, "remove-tag", nil, "remove a tag (repeatable)")
		fl.StringArrayVar(&f.addCriteria, "add-criteria", nil, "add an acceptance criterion at the end of the list (repeatable)")
		fl.StringArrayVar(&f.removeCriteria, "remove-criteria", nil, "remove an acceptance criterion, by its exact text (repeatable)")
		fl.StringArrayVar(&f.addAssignees, "add-assignee", nil, "add an assignee (repeatable)")
		fl.StringArrayVar(&f.removeAssignees, "remove-assignee", nil, "remove an assignee (repeatable)")
		fl.BoolVar(&f.replace, "replace", false, "let --criteria / --tag / --assignee drop existing entries")
	}
}

// editList is the list a --<flag> / --add-<flag> / --remove-<flag> edit
// leaves: set when --<flag> is given, else cur, plus each added entry not
// already listed, less each removed one (removing an unlisted entry is
// ignored). Entries are trimmed and blank ones dropped. Nil when no flag of
// the three is given.
func editList(changed func(string) bool, flag string, set, add, remove, cur []string) *[]string {
	if !changed(flag) && !changed("add-"+flag) && !changed("remove-"+flag) {
		return nil
	}
	var list []string
	if changed(flag) {
		list = append(list, set...)
	} else {
		list = append(list, cur...)
	}
	list = append(list, add...)
	var drop []string
	for _, e := range remove {
		drop = append(drop, strings.TrimSpace(e))
	}
	kept := []string{}
	for _, e := range list {
		e = strings.TrimSpace(e)
		if e != "" && !contains(kept, e) && !contains(drop, e) {
			kept = append(kept, e)
		}
	}
	return &kept
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
	if changed("criteria") || changed("add-criteria") || changed("remove-criteria") {
		var crit []string
		if changed("criteria") {
			crit = append(crit, f.criteria...)
		} else if cur != nil {
			crit = append(crit, cur.Criteria...)
		}
		for _, c := range f.addCriteria {
			if !contains(crit, c) {
				crit = append(crit, c)
			}
		}
		var missing []string
		for _, c := range f.removeCriteria {
			if !contains(crit, c) && (cur == nil || !contains(cur.Criteria, c)) {
				missing = append(missing, c)
			}
		}
		if len(missing) > 0 {
			var b strings.Builder
			b.WriteString("--remove-criteria names a criterion the item does not have:")
			for _, c := range missing {
				fmt.Fprintf(&b, "\n  - %s", c)
			}
			b.WriteString("\ncurrent criteria:")
			if cur == nil || len(cur.Criteria) == 0 {
				b.WriteString(" (none)")
			} else {
				for _, c := range cur.Criteria {
					fmt.Fprintf(&b, "\n  - %s", c)
				}
			}
			return out, errors.New(b.String())
		}
		kept := crit[:0]
		for _, c := range crit {
			if !contains(f.removeCriteria, c) {
				kept = append(kept, c)
			}
		}
		out.Criteria = &kept
	}
	var curTags, curAssignees []string
	if cur != nil {
		curTags, curAssignees = cur.Tags, cur.Assignees
	}
	out.Tags = editList(changed, "tag", f.tags, f.addTags, f.removeTags, curTags)
	out.Assignees = editList(changed, "assignee", f.assignees, f.addAssignees, f.removeAssignees, curAssignees)
	if cur != nil && !f.replace {
		var msg strings.Builder
		guard := func(flag, name string, have []string, next *[]string, removed []string) {
			if !changed(flag) || next == nil {
				return
			}
			var drop []string
			for _, e := range have {
				if !contains(*next, e) && !contains(removed, e) {
					drop = append(drop, e)
				}
			}
			if len(drop) == 0 {
				return
			}
			if msg.Len() > 0 {
				msg.WriteString("\n")
			}
			fmt.Fprintf(&msg, "set would drop existing %s; give --replace to replace the list, or use --add-%s/--remove-%s:", name, flag, flag)
			for _, e := range drop {
				fmt.Fprintf(&msg, "\n  - %s", e)
			}
		}
		guard("criteria", "criteria", cur.Criteria, out.Criteria, f.removeCriteria)
		guard("tag", "tags", cur.Tags, out.Tags, f.removeTags)
		guard("assignee", "assignees", cur.Assignees, out.Assignees, f.removeAssignees)
		if msg.Len() > 0 {
			return out, errors.New(msg.String())
		}
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
	if changed("clear-inputs") && f.clearInputs {
		if out.Inputs != nil {
			return out, errors.New("--clear-inputs cannot be given with an --input-* flag")
		}
		out.Inputs = &item.Selection{}
	}
	if changed("trigger-task") || changed("trigger-workflow") || changed("trigger-base") || changed("min-size") {
		t := item.Trigger{}
		if cur != nil && cur.Trigger != nil {
			t = *cur.Trigger
		}
		if changed("trigger-task") {
			t.Task, t.Workflow = f.trigTask, ""
		}
		if changed("trigger-workflow") {
			t.Workflow = f.trigWorkflow
			if !changed("trigger-task") {
				t.Task = ""
			}
		}
		if changed("trigger-base") {
			t.Base = f.trigBase
		}
		if changed("min-size") {
			t.MinSize = f.minSize
		}
		out.Trigger = &t
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
	f.register(cmd, kind, true)
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
			if kind == item.QueueKind {
				return showQueue(k, out, it, atts, seats, *asJSON)
			}
			held, err := k.HoldOf(it.ID)
			if err != nil {
				return err
			}
			users, working := itemAssignees(it, seats)
			if *asJSON {
				return writeJSON(out, struct {
					item.Item
					// Assignees shadows the record's explicit list with every
					// assignee, seated users included (item.AssigneesOf).
					Assignees   []string          `json:"assignees"`
					Working     []string          `json:"working"`
					Attachments []item.Attachment `json:"attachments"`
					Seats       []item.Seat       `json:"seats"`
					Held        *item.Hold        `json:"held,omitempty"`
				}{it, users, working, atts, seats, held})
			}
			printItem(out, it, users, working)
			if held != nil {
				fmt.Fprintf(out, "held       after %d rejected rounds leaving %s; dissent %s; accepted %s\n",
					held.Rounds, held.State, dissentKeys(*held), orNone(held.Accepted))
			}
			for _, a := range atts {
				fmt.Fprintf(out, "attachment %s (%s, %d bytes)\n", a.Name, a.MediaType, a.Size)
			}
			for _, s := range seats {
				fmt.Fprintf(out, "seat       %s at %s", s.Session, s.Location)
				if s.User != "" {
					fmt.Fprintf(out, " by %s", s.User)
				}
				fmt.Fprintln(out)
			}
			return nil
		},
	}
	if kind == item.QueueKind {
		cmd.Aliases = []string{"show"}
		cmd.Short = "Show a queue: its filter, trigger, members and last fire"
	}
	asJSON = jsonFlag(cmd)
	return cmd
}

// itemAssignees is it's assignees and those of them working it, from seats
// (the item's bound seats) less those proven gone on this machine.
func itemAssignees(it item.Item, seats []item.Seat) (users, working []string) {
	if len(seats) == 0 {
		return item.AssigneesOf(it.Assignees, nil)
	}
	home := config.GlobalDir()
	here, _ := config.ReadMachineID(home)
	return item.AssigneesOf(it.Assignees, seatlife.Working(home, here, seats))
}

// assigneesText is users joined by ", ", each user working it marked
// " (working)".
func assigneesText(users, working []string) string {
	parts := make([]string, len(users))
	for i, u := range users {
		parts[i] = u
		if contains(working, u) {
			parts[i] += " (working)"
		}
	}
	return strings.Join(parts, ", ")
}

// printItem prints it, with its assignees (users) and those of them working it.
func printItem(w io.Writer, it item.Item, users, working []string) {
	fmt.Fprintf(w, "%s  %s\n", it.ID, it.Title)
	fmt.Fprintf(w, "kind       %s\nstatus     %s\nworkflow   %s\n", it.Kind, it.Status, it.Workflow)
	for _, kv := range [][2]string{{"category", it.Category}, {"parent", it.Parent}, {"assignees", assigneesText(users, working)}, {"tags", strings.Join(it.Tags, ", ")}} {
		if kv[1] != "" {
			fmt.Fprintf(w, "%-10s %s\n", kv[0], kv[1])
		}
	}
	if it.Inputs != nil {
		label := "inputs"
		if it.Kind == item.QueueKind {
			label = "filter"
		}
		fmt.Fprintf(w, "%-10s kind=%q status=%q tag=%q category=%q\n", label, it.Inputs.Kind, it.Inputs.Status, it.Inputs.Tag, it.Inputs.Category)
	}
	if it.Trigger != nil {
		fmt.Fprintf(w, "trigger    %s\n", triggerText(*it.Trigger))
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
				seats, err := k.Cache.Seats("", "")
				if err != nil {
					return err
				}
				byItem := map[string][]item.Seat{}
				for _, s := range seats {
					byItem[s.Item] = append(byItem[s.Item], s)
				}
				type row struct {
					item.Item
					// Assignees shadows the record's explicit list, as in get.
					Assignees []string `json:"assignees"`
					Working   []string `json:"working"`
				}
				rows := []row{}
				for _, it := range items {
					users, working := itemAssignees(it, byItem[it.ID])
					rows = append(rows, row{it, users, working})
				}
				return writeJSON(cmd.OutOrStdout(), rows)
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
	var status, reselect string
	var asJSON *bool
	cmd := &cobra.Command{
		Use:   "set <id>",
		Short: fmt.Sprintf("Update a %s's fields, or present a transition with --status", kind),
		Long: `set updates the fields its flags name. --status presents the transition from
the item's current state to the given one: its gates run in their configured
order and every result is recorded on the ledger. The item moves only when no
gate rejects; otherwise set exits 2 and lists each rejecting gate with its
notes.

--status runs the transition's gates in this command before it returns. A
gate with a long timeout (see satelle workflow show) can outlast a harness's
bound on one shell command. Run such a move as the harness's background
command, with that command's own timeout above the sum of the sequential
gates' bounds.

--reselect <workflow> moves the item to that workflow's start state, when the
repo's rules now select that workflow for it and its own workflow has neither
moved nor judged it; it takes no other field flag and no --status. A refusal
for the evidence on the item's chain exits 2; any other refusal exits 1.
Nothing is written on a refusal.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("reselect") {
				var others []string
				cmd.Flags().Visit(func(fl *pflag.Flag) {
					if fl.Name != "reselect" && fl.Name != "json" && fl.Name != sessionFlag && fl.Name != directoryFlag {
						others = append(others, "--"+fl.Name)
					}
				})
				if len(others) > 0 {
					return fmt.Errorf("--reselect takes no other field flag and no --status (got %s)", strings.Join(others, ", "))
				}
			}
			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()
			it, err := getItem(k, kind, args[0])
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("reselect") {
				from := it.Status
				moved, err := k.Reselect(it.ID, reselect, actor())
				if errors.Is(err, kernel.ErrKeepsWorkflow) {
					return &exitError{code: ExitBlocked, msg: err.Error()}
				}
				if err != nil {
					return err
				}
				if *asJSON {
					return writeJSON(cmd.OutOrStdout(), moved)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s moved to workflow %s (%s -> %s)\n", moved.ID, moved.Workflow, from, moved.Status)
				return nil
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
	f.register(cmd, kind, false)
	cmd.Flags().StringVar(&status, "status", "", "present the transition to this state")
	cmd.Flags().StringVar(&reselect, "reselect", "", "move the item to this workflow's start state, when the rules now select it and its workflow has neither moved nor judged it")
	asJSON = jsonFlag(cmd)
	return cmd
}

// reportTransition prints a transition's gate results; a refused move is an
// ExitBlocked error naming every rejecting gate, and every gate whose judge
// could not be asked (result error), with its notes.
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
		for _, q := range res.Queued {
			printQueueOutcome(w, q)
		}
	}
	if res.Moved {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s did not move %s -> %s:", res.Item.ID, res.From, res.To)
	rejects, errs := res.Rejects(), res.Errors()
	if len(rejects) > 0 || len(errs) == 0 {
		b.WriteString(" rejected by")
		writeRefusals(&b, rejects)
	}
	if len(errs) > 0 {
		if len(rejects) > 0 {
			b.WriteString("\n ")
		}
		b.WriteString(" could not judge (error)")
		writeRefusals(&b, errs)
	}
	if h := res.Held; h != nil {
		writeHold(&b, res.Item, *h)
	}
	return &exitError{code: ExitBlocked, msg: b.String()}
}

// holdStop is the line a hold report ends with: the driving session stops at
// a hold; the move out of it is the operator's.
const holdStop = "held for the operator: the driving session reports this hold and stops (satelle help workflows, Hold)"

// writeHold reports a hold: where the item is held and after how many rounds,
// each dissenting verdict with its class, and the verdicts that accepted.
func writeHold(b *strings.Builder, it item.Item, h item.Hold) {
	fmt.Fprintf(b, "\n%s held at %s after %d rejected rounds leaving %s", it.ID, it.Status, h.Rounds, h.State)
	for _, d := range h.Dissent {
		fmt.Fprintf(b, "\n  dissent: %s rejected %d of %d rounds; class %s in %d", d.Key(), d.Rejected, h.Rounds, d.Class, d.ClassRounds)
	}
	fmt.Fprintf(b, "\n  accepted: %s", orNone(h.Accepted))
	fmt.Fprintf(b, "\n%s", holdStop)
}

func dissentKeys(h item.Hold) string {
	var keys []string
	for _, d := range h.Dissent {
		keys = append(keys, d.Key())
	}
	return orNone(keys)
}

func orNone(xs []string) string {
	if len(xs) == 0 {
		return "none"
	}
	return strings.Join(xs, ", ")
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
			if args[0], err = resolveItem(k, kind, args[0]); err != nil {
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

// recordCmd records the operator's word on an item in an operator state: the
// attachment of the state's declared type that its transitions out need. It
// refuses an agent session, and asks for the item id retyped on the
// controlling terminal before it writes anything.
func recordCmd(kind string) *cobra.Command {
	var name, file, mediaType string
	cmd := &cobra.Command{
		Use:   "record <id> --file F",
		Short: "Record the operator's word on an item in an operator state (operator only)",
		Long: `record attaches the operator's word to an item whose current state is the
operator's (performer = { operator = "<type>" }): the file is recorded with the
state's declared type, marked as the operator's, and only such a record since
the item entered the state lets it leave. The operator runs it in their own
terminal with no session named (SATELLE_SESSION unset, no --session); it asks for the item id to be retyped on
the controlling terminal and never reads stdin. An agent session may not run it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()
			if args[0], err = resolveItem(k, kind, args[0]); err != nil {
				return err
			}
			session := sessionFromEnv()
			if err := kernel.OperatorSession(session); err != nil {
				return err
			}
			it, typ, err := k.OperatorState(args[0])
			if err != nil {
				return err
			}
			data, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			if mediaType == "" {
				mediaType = mime.TypeByExtension(filepath.Ext(file))
			}
			if err := confirmOnTerminal(it.ID, it.Status, typ); err != nil {
				return err
			}
			a, err := k.RecordOperator(it.ID, name, data, mediaType, actor(), session)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "recorded the operator's %s on %s (%d bytes, sha256 %s)\n", typ, it.ID, a.Size, a.SHA256[:12])
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "attachment name (default: the state's declared type)")
	cmd.Flags().StringVar(&file, "file", "", "file to record")
	cmd.Flags().StringVar(&mediaType, "type-media", "", "media type (default: from the file extension, else text/markdown); the declared type is added as its type parameter")
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
			if args[0], err = resolveItem(k, kind, args[0]); err != nil {
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
			if args[0], err = resolveItem(k, kind, args[0]); err != nil {
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
	var location, base string
	cmd := &cobra.Command{
		Use:   "seat <id>",
		Short: "Bind this session, at this worktree, to an item",
		Long: `seat binds a session at a location to an item. While the item's current
state has a performer, the edit gate allows the seated session's edits at that
location. The session is --session <id> (the context hook prints it; it
defaults to $SATELLE_SESSION); the location defaults to local:<this worktree>.

The seat records the item's diff base: the commit its gates judge the change
from. It is HEAD unless --base names a branch, tag or commit (resolved now, and
it must be in HEAD's history). Seat a stacked branch with --base <the branch it
is stacked on> so its gates judge only its own change.

A base behind the item's base branch (the --base ref, else origin/main) is
judged from HEAD's merge base with that branch, so merging main into the
item's branch does not widen the diff.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, r, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()
			if args[0], err = resolveItem(k, kind, args[0]); err != nil {
				return err
			}
			session := sessionFromEnv()
			if session == "" {
				return errors.New("no session: pass --session <id>")
			}
			if location == "" {
				location = kernel.LocalLocation(r.Tree)
			}
			// The seat command runs in the session's harness too: settle the
			// session's owner from its chain (best effort).
			_ = seatlife.Resolve(config.GlobalDir(), session, procgroup.Ancestors(os.Getpid()))
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
	cmd.Flags().StringVar(&location, "location", "", "local:<path>, remote:<node> or cloud:<url> (default: this worktree)")
	cmd.Flags().StringVar(&base, "base", "", "the commit the item's diff is judged from: a branch, tag or SHA, resolved now (default HEAD)")
	return cmd
}

func releaseCmd(kind string) *cobra.Command {
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
			if args[0], err = resolveItem(k, kind, args[0]); err != nil {
				return err
			}
			session := sessionFromEnv()
			if session == "" {
				return errors.New("no session: pass --session <id>")
			}
			if err := k.Release(args[0], session, actor()); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "released %s from %s\n", session, args[0])
			return nil
		},
	}
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
			if args[0], err = resolveItem(k, kind, args[0]); err != nil {
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
			if args[0], err = resolveItem(k, "task", args[0]); err != nil {
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

// writeRefusals lists refusing gates, each with its binding and notes.
func writeRefusals(b *strings.Builder, gates []gate.Result) {
	for _, g := range gates {
		fmt.Fprintf(b, "\n  gate %s", g.Gate)
		if g.Binding != "" {
			fmt.Fprintf(b, " (%s)", g.Binding)
		}
		if n := strings.TrimSpace(g.Notes); n != "" {
			fmt.Fprintf(b, ":\n    %s", strings.ReplaceAll(n, "\n", "\n    "))
		}
	}
}
