package cli

import (
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/capacity"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/integration"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/procgroup"
	"github.com/bobmcallan/satelle/internal/workflow"
	"github.com/spf13/cobra"
)

var (
	takeSample   = capacity.Take
	processTable = procgroup.Table
)

func capacityCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "capacity",
		Short: "Show this machine's cores, memory, load, running agent sessions and engaged seats",
		Long: `capacity reads this machine's figures once, when asked, and prints them:
cores (the CPUs this process may use), total and available memory, the 1, 5
and 15 minute load average, the coding-agent sessions running per harness
across every project on this machine, and the satelle seats engaged here
across every registered project. Nothing is stored and no process is started.

Agent sessions are counted from the process table by the harness commands
satelle knows: each integrated harness's CLI and the command of each
agents.toml binding of this repo and the registered projects. A process whose
parent is a session of the same harness is counted with that session.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			repos, reposErr := capacityRepos()
			s := takeSample(knownHarnesses(repos), processTable)
			seats, seatErrs := machineSeats(repos)
			printCapacity(cmd.OutOrStdout(), s, seats, append(seatErrs, reposErr))
			return nil
		},
	}
}

func capacityRepos() ([]repo, error) {
	var out []repo
	if r, err := findRepo(""); err == nil {
		out = append(out, r)
	}
	reg, err := registeredRepos()
	for _, r := range reg {
		if !slices.ContainsFunc(out, func(o repo) bool { return o.Tree == r.Tree }) {
			out = append(out, r)
		}
	}
	if err != nil {
		err = fmt.Errorf("registered projects unreadable: %w", err)
	}
	return out, err
}

func knownHarnesses(repos []repo) capacity.Known {
	known := capacity.Known{}
	for _, h := range integration.Hooks() {
		known[h.CLI()] = h.Name()
	}
	for _, r := range repos {
		for _, b := range repoCatalog(r) {
			p := agent.Preset(b)
			if p == "" || agent.IsSession(b) {
				continue
			}
			if _, ok := known[p]; !ok {
				known[p] = p
			}
		}
	}
	return known
}

func machineSeats(repos []repo) ([]engagedSeat, []error) {
	home := config.GlobalDir()
	here, _ := config.ReadMachineID(home)
	var out []engagedSeat
	var errs []error
	var seen []string
	for _, r := range repos {
		if slices.Contains(seen, r.Runtime) {
			continue
		}
		seen = append(seen, r.Runtime)
		items, err := readItemsAt(r.Runtime)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", r.Tree, err))
			continue
		}
		var wfs map[string]*workflow.Workflow
		if c, err := kernel.LoadConfig(r.DataDir); err == nil {
			wfs = c.Workflows
		}
		out = append(out, engagedHere(items, wfs, home, here)...)
	}
	return out, errs
}

const gib = 1 << 30

func printCapacity(w io.Writer, s capacity.Sample, seats []engagedSeat, errs []error) {
	host, _ := os.Hostname()
	id, ok := config.ReadMachineID(config.GlobalDir())
	if !ok {
		id = "no machine id"
	}
	fmt.Fprintf(w, "machine   %s (%s)\n", host, id)
	fmt.Fprintf(w, "cores     %d\n", s.Cores)
	if err := s.Errs[capacity.FigMemory]; err != nil {
		fmt.Fprintf(w, "memory    unavailable: %v\n", err)
	} else {
		fmt.Fprintf(w, "memory    %.1f GiB total, %.1f GiB available\n", float64(s.MemTotal)/gib, float64(s.MemAvailable)/gib)
	}
	if err := s.Errs[capacity.FigLoad]; err != nil {
		fmt.Fprintf(w, "load      unavailable: %v\n", err)
	} else {
		fmt.Fprintf(w, "load      %.2f %.2f %.2f (1, 5, 15 min)\n", s.Load[0], s.Load[1], s.Load[2])
	}
	if err := s.Errs[capacity.FigAgents]; err != nil {
		fmt.Fprintf(w, "agents    unavailable: %v\n", err)
	} else {
		labels := make([]string, 0, len(s.Agents))
		for l := range s.Agents {
			labels = append(labels, l)
		}
		slices.Sort(labels)
		parts := make([]string, len(labels))
		for i, l := range labels {
			parts[i] = fmt.Sprintf("%s %d", l, s.Agents[l])
		}
		fmt.Fprintf(w, "agents    %s (all projects on this machine)\n", strings.Join(parts, ", "))
	}
	fmt.Fprintf(w, "seats     %d engaged here\n", len(seats))
	for _, st := range seats {
		fmt.Fprintf(w, "  %s  %s  %s\n", st.Item, st.Session, strings.TrimPrefix(st.Location, kernel.LocationLocal))
	}
	for _, err := range errs {
		if err != nil {
			fmt.Fprintf(w, "  unavailable: %v\n", err)
		}
	}
}
