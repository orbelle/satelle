package cli

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/kernel"
)

// The queue verbs beyond the item verbs every kind has: add, remove and fire.
// A queue is created, listed, shown and changed like any item (queue create,
// list, get|show, set).

func triggerText(t item.Trigger) string {
	target := "task " + t.Task
	if t.Workflow != "" {
		target = "workflow " + t.Workflow
	}
	when := "by hand only (min size 0)"
	if t.MinSize > 0 {
		when = fmt.Sprintf("at %d members", t.MinSize)
	}
	s := target + ", fires " + when
	if t.Base != "" {
		s += ", base " + t.Base
	}
	return s
}

func showQueue(k *kernel.Kernel, out io.Writer, q item.Item, atts []item.Attachment, seats []item.Seat, asJSON bool) error {
	_, st, ms, err := k.QueueMembers(q.ID)
	if err != nil {
		return err
	}
	type member struct {
		ID       string `json:"id"`
		Kind     string `json:"kind"`
		Status   string `json:"status"`
		Title    string `json:"title"`
		Explicit bool   `json:"explicit"`
	}
	type lapse struct {
		ID     string `json:"id"`
		Kind   string `json:"kind"`
		Status string `json:"status"`
		Title  string `json:"title"`
	}
	lapsed := []lapse{}
	for _, l := range ms.Lapsed {
		r := lapse{ID: l.ID, Status: l.Status}
		if it, err := k.Get(l.ID); err == nil {
			r.Kind, r.Title = it.Kind, it.Title
		}
		lapsed = append(lapsed, r)
	}
	rows := []member{}
	for _, id := range ms.Members {
		m := member{ID: id}
		if it, err := k.Get(id); err == nil {
			m.Kind, m.Status, m.Title = it.Kind, it.Status, it.Title
		}
		for _, e := range st.Explicit {
			m.Explicit = m.Explicit || e == id
		}
		rows = append(rows, m)
	}
	runStatus := ""
	if lf := st.LastFire; lf != nil && lf.Run != "" {
		if run, err := k.Get(lf.Run); err == nil {
			runStatus = run.Status
		} else {
			runStatus = "missing"
		}
	}
	definedBy := ""
	if def := k.QueueFile(q.ID); def != nil {
		definedBy = k.RelPath(def.File)
	}
	if asJSON {
		return writeJSON(out, struct {
			item.Item
			DefinedBy   string            `json:"defined_by,omitempty"`
			Members     []member          `json:"members"`
			Lapsed      []lapse           `json:"lapsed"`
			LastFire    *item.Fired       `json:"last_fire,omitempty"`
			RunStatus   string            `json:"last_run_status,omitempty"`
			Attachments []item.Attachment `json:"attachments"`
			Seats       []item.Seat       `json:"seats"`
		}{q, definedBy, rows, lapsed, st.LastFire, runStatus, atts, seats})
	}
	users, working := itemAssignees(q, seats)
	printItem(out, q, users, working)
	if definedBy != "" {
		fmt.Fprintf(out, "defined by %s\n", definedBy)
	}
	fmt.Fprintf(out, "\nmembers (%d)\n", len(rows))
	for _, m := range rows {
		how := "filter"
		if m.Explicit {
			how = "added"
		}
		fmt.Fprintf(out, "  %-14s %-14s %-7s %s\n", m.ID, m.Status, how, m.Title)
	}
	if len(lapsed) > 0 {
		fmt.Fprintf(out, "lapsed (%d)\n", len(lapsed))
		for _, l := range lapsed {
			fmt.Fprintf(out, "  %-14s left: %-14s %s\n", l.ID, l.Status, l.Title)
		}
	}
	if lf := st.LastFire; lf != nil {
		fmt.Fprintf(out, "last fire  %s by %s (%s): run %s, %s\n", lf.At.Format("2006-01-02 15:04"), lf.Actor, lf.Cause, lf.Run, runStatus)
	} else {
		fmt.Fprintln(out, "last fire  never")
	}
	return nil
}

// queueLabel is "name (id)" for a file-defined queue, else its id.
func queueLabel(name, id string) string {
	if name != "" {
		return fmt.Sprintf("%s (%s)", name, id)
	}
	return id
}

func printQueueOutcome(w io.Writer, o kernel.QueueOutcome) {
	q := queueLabel(o.Name, o.Queue)
	for _, id := range o.Added {
		fmt.Fprintf(w, "queued %s in %s\n", id, q)
	}
	for _, id := range o.Already {
		fmt.Fprintf(w, "%s is already in %s\n", id, q)
	}
	if o.Fire != nil {
		printFire(w, *o.Fire)
	}
	if o.Held != "" {
		fmt.Fprintf(w, "%s did not fire: %s\n", q, o.Held)
	}
}

func printFire(w io.Writer, f kernel.FireOutcome) {
	q := queueLabel(f.Name, f.Queue)
	if f.Run == "" {
		fmt.Fprintf(w, "%s has no members; nothing fired\n", q)
		return
	}
	fmt.Fprintf(w, "%s fired %s (%s) over %d items", q, f.Run, f.Status, len(f.Inputs))
	if f.Base != "" {
		from := f.BaseRef
		if from == "" {
			from = "detached HEAD"
		}
		fmt.Fprintf(w, ", base %s (%s)", short(f.Base), from)
	}
	fmt.Fprintln(w)
	for _, id := range f.Inputs {
		fmt.Fprintf(w, "  %s\n", id)
	}
}

// fireRefusal turns a held fire into ExitBlocked.
func fireRefusal(err error) error {
	if errors.Is(err, kernel.ErrRunOpen) || errors.Is(err, kernel.ErrQueueMoved) {
		return &exitError{code: ExitBlocked, msg: err.Error()}
	}
	return err
}

func queueAddCmd() *cobra.Command {
	var asJSON *bool
	cmd := &cobra.Command{
		Use:   "add <que> <id>...",
		Short: "Add items to a queue; fires its trigger here when it reaches its minimum size",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()
			o, err := k.Enqueue(args[0], args[1:], "", actor())
			if err != nil {
				return err
			}
			if *asJSON {
				return writeJSON(cmd.OutOrStdout(), o)
			}
			printQueueOutcome(cmd.OutOrStdout(), o)
			return nil
		},
	}
	asJSON = jsonFlag(cmd)
	return cmd
}

func queueRemoveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove <que> <id>...",
		Short: "Remove items added to a queue (a filter member is changed by its filter)",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()
			removed, err := k.Dequeue(args[0], args[1:], actor())
			for _, id := range removed {
				fmt.Fprintf(cmd.OutOrStdout(), "removed %s from %s\n", id, args[0])
			}
			return err
		},
	}
	return cmd
}

func queueFireCmd() *cobra.Command {
	var asJSON *bool
	var base string
	cmd := &cobra.Command{
		Use:   "fire <que>",
		Short: "Fire a queue's trigger by hand: start its run over the members now",
		Long: `fire runs the queue's trigger over its members now, in this process: it
records queue.fired on the queue and creates the run (an execution under the
trigger's task, or under the queue in the trigger's workflow) whose task.run
lists the members it took. Satelle schedules and watches nothing; a queue fires
only here, or when an enqueue reaches its minimum size.

It exits 0 when it fired or the queue had no members, and 2 when the last
fire's run is still open or the queue changed while firing.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()
			if args[0], err = resolveItem(k, item.QueueKind, args[0]); err != nil {
				return err
			}
			f, err := k.Fire(args[0], base, "by hand", actor())
			if err != nil {
				return fireRefusal(err)
			}
			if *asJSON {
				return writeJSON(cmd.OutOrStdout(), f)
			}
			printFire(cmd.OutOrStdout(), f)
			return nil
		},
	}
	cmd.Flags().StringVar(&base, "base", "", "the ref the run's base comes from (default: the trigger's base, else HEAD)")
	asJSON = jsonFlag(cmd)
	return cmd
}
