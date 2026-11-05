package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/gitfacts"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/workflow"
)

// routeView is `story route --json`: the item's workflow with its position and
// the latest recorded result of every gate on each transition.
type routeView struct {
	Item        string           `json:"item"`
	Workflow    string           `json:"workflow"`
	Status      string           `json:"status"`
	States      []stateView      `json:"states"`
	Transitions []transitionView `json:"transitions"`
}

type stateView struct {
	Name      string `json:"name"`
	Start     bool   `json:"start,omitempty"`
	Terminal  bool   `json:"terminal,omitempty"`
	Park      bool   `json:"park,omitempty"`
	Resumes   bool   `json:"resumes,omitempty"`
	Performer string `json:"performer,omitempty"` // agent/skill
	Current   bool   `json:"current,omitempty"`
}

type transitionView struct {
	From  []string   `json:"from"`
	To    string     `json:"to"`
	Gates []gateView `json:"gates,omitempty"`
}

type gateView struct {
	Group  int    `json:"group"`
	Mode   string `json:"mode"`
	Gate   string `json:"gate"`
	Result string `json:"result,omitempty"` // the latest recorded result on this move, if any
	Notes  string `json:"notes,omitempty"`
}

// buildRoute renders w for an item at status with its recorded gate results.
func buildRoute(w *workflow.Workflow, it item.Item, results []gate.Result) routeView {
	v := routeView{Item: it.ID, Workflow: w.Name, Status: it.Status}
	names := make([]string, 0, len(w.States))
	for n := range w.States {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		a, b := w.States[names[i]], w.States[names[j]]
		if a.Start != b.Start {
			return a.Start
		}
		if a.Terminal != b.Terminal {
			return b.Terminal
		}
		return names[i] < names[j]
	})
	for _, n := range names {
		st := w.States[n]
		sv := stateView{Name: n, Start: st.Start, Terminal: st.Terminal, Park: st.Park, Resumes: st.Resumes, Current: n == it.Status}
		if st.Performer != nil {
			sv.Performer = st.Performer.Agent + "/" + st.Performer.Skill
		}
		v.States = append(v.States, sv)
	}
	latest := func(from, to, g string) (gate.Result, bool) {
		for i := len(results) - 1; i >= 0; i-- {
			r := results[i]
			if r.Gate == g && r.Transition == gate.TransitionKey(from, to) {
				return r, true
			}
		}
		return gate.Result{}, false
	}
	for _, t := range w.Transitions {
		tv := transitionView{From: t.From, To: t.To}
		for gi, grp := range t.Groups {
			for _, g := range grp.Gates {
				gv := gateView{Group: gi + 1, Mode: grp.Mode, Gate: g}
				for _, from := range t.From {
					if from == workflow.AnyState {
						from = it.Status
					}
					if r, ok := latest(from, t.To, g); ok {
						gv.Result, gv.Notes = r.Result, r.Notes
					}
				}
				tv.Gates = append(tv.Gates, gv)
			}
		}
		v.Transitions = append(v.Transitions, tv)
	}
	return v
}

func printRoute(w io.Writer, v routeView) {
	if v.Status != "" {
		fmt.Fprintf(w, "%s  workflow %s, at %s\n\nstates\n", v.Item, v.Workflow, v.Status)
	} else {
		fmt.Fprintf(w, "workflow %s  (%s)\n\nstates\n", v.Workflow, v.Item)
	}
	for _, s := range v.States {
		mark := "  "
		if s.Current {
			mark = "▶ "
		}
		var tags []string
		for _, kv := range []struct {
			on   bool
			name string
		}{{s.Start, "start"}, {s.Park, "park"}, {s.Resumes, "resumes"}, {s.Terminal, "terminal"}} {
			if kv.on {
				tags = append(tags, kv.name)
			}
		}
		if s.Performer != "" {
			tags = append(tags, "performer "+s.Performer)
		}
		extra := ""
		if len(tags) > 0 {
			extra = "  (" + strings.Join(tags, ", ") + ")"
		}
		fmt.Fprintf(w, "%s%s%s\n", mark, s.Name, extra)
	}
	fmt.Fprintln(w, "\ntransitions")
	for _, t := range v.Transitions {
		fmt.Fprintf(w, "  %s -> %s\n", strings.Join(t.From, "|"), t.To)
		for _, g := range t.Gates {
			res := g.Result
			if res == "" {
				res = "-"
			}
			fmt.Fprintf(w, "      [%d %s] %-24s %-8s %s\n", g.Group, g.Mode, g.Gate, res, firstLine(g.Notes))
		}
	}
}

func routeCmd(kind string) *cobra.Command {
	var asJSON *bool
	cmd := &cobra.Command{
		Use:   "route <id>",
		Short: "Show the item's workflow, its position and its gate results",
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
			w, ok := k.Workflows[it.Workflow]
			if !ok {
				return fmt.Errorf("%s names workflow %q, which does not exist", it.ID, it.Workflow)
			}
			results, err := gateResults(k, it.ID)
			if err != nil {
				return err
			}
			v := buildRoute(w, it, results)
			if *asJSON {
				return writeJSON(cmd.OutOrStdout(), v)
			}
			printRoute(cmd.OutOrStdout(), v)
			return nil
		},
	}
	asJSON = jsonFlag(cmd)
	return cmd
}

func diffCmd(kind string) *cobra.Command {
	var asJSON *bool
	cmd := &cobra.Command{
		Use:   "diff <id>",
		Short: "List the paths changed since the item's base commit",
		Long: `diff lists the paths changed in this worktree since the item's base: the
latest seat's base commit, else the commit the item was created at.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, r, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()
			it, err := getItem(k, kind, args[0])
			if err != nil {
				return err
			}
			events, err := k.Ledger.Read(it.ID)
			if err != nil {
				return err
			}
			base := it.Base
			seats := item.FoldSeats(events)
			for i := len(seats) - 1; i >= 0; i-- {
				if seats[i].Base != "" {
					base = seats[i].Base
					break
				}
			}
			if base == "" {
				return fmt.Errorf("%s has no base commit (created outside a git work tree)", it.ID)
			}
			paths, err := gitfacts.Repo{Dir: r.Tree}.ChangedPaths(base)
			if err != nil {
				return err
			}
			if *asJSON {
				if paths == nil {
					paths = []string{}
				}
				return writeJSON(cmd.OutOrStdout(), map[string]any{"item": it.ID, "base": base, "paths": paths})
			}
			for _, p := range paths {
				fmt.Fprintln(cmd.OutOrStdout(), p)
			}
			return nil
		},
	}
	asJSON = jsonFlag(cmd)
	return cmd
}
