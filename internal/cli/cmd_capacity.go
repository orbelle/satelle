package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/capacity"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/integration"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/procgroup"
	"github.com/bobmcallan/satelle/internal/remote/provider"
	"github.com/bobmcallan/satelle/internal/remotedef"
	"github.com/bobmcallan/satelle/internal/remotenode"
	"github.com/bobmcallan/satelle/internal/workflow"
	"github.com/spf13/cobra"
)

var (
	readMachine  = capacity.Read
	processTable = procgroup.Table
)

const capacitySampleFile = "capacity-sample.json"

var remoteTimeout = 30 * time.Second

type capacityReport struct {
	Host         string            `json:"host"`
	MachineID    string            `json:"machine_id"`
	TakenAt      time.Time         `json:"taken_at"`
	Cores        int               `json:"cores"`
	MemTotal     uint64            `json:"mem_total"`
	MemAvailable uint64            `json:"mem_available"`
	Load         [3]float64        `json:"load"`
	Agents       map[string]int    `json:"agents"`
	Unavailable  map[string]string `json:"unavailable,omitempty"`
	Seats        []engagedSeat     `json:"seats"`
	Errors       []string          `json:"errors,omitempty"`
}

type reached struct {
	Remote string
	Report *capacityReport
	Err    error
}

type machineJSON struct {
	Remote      string          `json:"remote"`
	Report      *capacityReport `json:"report,omitempty"`
	Unreachable string          `json:"unreachable,omitempty"`
}

func capacityCmd() *cobra.Command {
	var here, asJSON bool
	cmd := &cobra.Command{
		Use:   "capacity",
		Short: "Show the cores, memory, load, running agent sessions and engaged seats of this machine and the machines it reaches",
		Long: `capacity prints this machine's figures: cores (the CPUs this process may
use), total and available memory, the 1, 5 and 15 minute load average, the
coding-agent sessions running per harness across every project on this
machine, and the satelle seats engaged here across every registered project.

The machine figures are read at most once per 30 second cadence and kept in the
satelle home as capacity-sample.json, readable by this user only, so every
project on the machine reuses them; the sampled line says when they were read.
Seats are read on every run. No process is started.

Agent sessions are counted from the process table by the harness commands
satelle knows: each integrated harness's CLI and the command of each
agents.toml binding of this repo and the registered projects. A process whose
parent is a session of the same harness is counted with that session.

The other machines reported are this repo's remotes with provider "command"
whose [connect] exec names no {node}: such an exec reaches one standing
machine. Each is asked with "satelle capacity --json --here" over that exec,
after its [connect] check when it has one, so satelle must be on that
machine's PATH. A machine that does not answer within the timeout, or answers
with no report, is named unreachable and the others are still reported.
--here reports this machine only.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			local := localReport()
			w := cmd.OutOrStdout()
			if here {
				if asJSON {
					return writeJSON(w, local)
				}
				printCapacity(w, local, "")
				return nil
			}
			var others []reached
			if r, err := findRepo(""); err == nil {
				others = reachMachines(cmd.Context(), r)
			}
			if asJSON {
				machines := []machineJSON{{Report: &local}}
				for _, m := range others {
					j := machineJSON{Remote: m.Remote, Report: m.Report}
					if m.Err != nil {
						j.Unreachable = m.Err.Error()
					}
					machines = append(machines, j)
				}
				return writeJSON(w, map[string][]machineJSON{"machines": machines})
			}
			printCapacity(w, local, "")
			for _, m := range others {
				fmt.Fprintln(w)
				if m.Err != nil {
					fmt.Fprintf(w, "machine   remote %s: unreachable: %v\n", m.Remote, m.Err)
					continue
				}
				printCapacity(w, *m.Report, m.Remote)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&here, "here", false, "report this machine only, reaching no other machine")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the report as JSON")
	return cmd
}

func localReport() capacityReport {
	repos, reposErr := capacityRepos()
	sn, keepErr := capacity.Reuse(filepath.Join(config.GlobalDir(), capacitySampleFile), time.Now(), func() capacity.Snapshot {
		return readMachine(processTable)
	})
	s := sn.Sample(knownHarnesses(repos))
	seats, seatErrs := machineSeats(repos)
	if keepErr != nil {
		seatErrs = append(seatErrs, fmt.Errorf("sample not kept: %w", keepErr))
	}
	host, _ := os.Hostname()
	id, ok := config.ReadMachineID(config.GlobalDir())
	if !ok {
		id = "no machine id"
	}
	rep := capacityReport{
		Host: host, MachineID: id, TakenAt: s.TakenAt, Cores: s.Cores,
		MemTotal: s.MemTotal, MemAvailable: s.MemAvailable, Load: s.Load,
		Agents: s.Agents, Seats: seats,
	}
	for fig, err := range s.Errs {
		if err != nil {
			if rep.Unavailable == nil {
				rep.Unavailable = map[string]string{}
			}
			rep.Unavailable[fig] = err.Error()
		}
	}
	for _, err := range append(seatErrs, reposErr) {
		if err != nil {
			rep.Errors = append(rep.Errors, err.Error())
		}
	}
	return rep
}

func remoteIDOf(rel string) (string, error) {
	if path.Base(rel) != kernel.RemoteFile || path.Dir(path.Dir(rel)) != kernel.RemotesDir {
		return "", fmt.Errorf("%q is not a remote definition path", rel)
	}
	return path.Base(path.Dir(rel)), nil
}

func standingMachine(def remotedef.Definition) bool {
	if def.Provider != remotedef.ProviderCommand || len(def.Connect.Exec) == 0 {
		return false
	}
	return !slices.ContainsFunc(def.Connect.Exec, func(a string) bool {
		return strings.Contains(a, remotedef.PlaceholderNode)
	})
}

func reachMachines(ctx context.Context, r repo) []reached {
	rels := kernel.RemoteFiles(r.DataDir)
	slices.Sort(rels)
	var out []reached
	for _, rel := range rels {
		id, err := remoteIDOf(rel)
		if err != nil {
			out = append(out, reached{Remote: rel, Err: fmt.Errorf("definition unreadable: %w", err)})
			continue
		}
		def, _, err := remotenode.LoadLocal(r.DataDir, id)
		if err != nil {
			out = append(out, reached{Remote: id, Err: fmt.Errorf("definition unreadable: %w", err)})
			continue
		}
		if !standingMachine(def) {
			continue
		}
		rep, err := askMachine(ctx, def)
		out = append(out, reached{Remote: id, Report: rep, Err: err})
	}
	return out
}

func askMachine(ctx context.Context, def remotedef.Definition) (*capacityReport, error) {
	ctx, cancel := context.WithTimeout(ctx, remoteTimeout)
	defer cancel()
	p := provider.Command{}
	if err := p.Check(ctx, def, ""); err != nil {
		return nil, err
	}
	out, err := p.Node(def, "").Exec(ctx, "satelle", "capacity", "--json", "--here")
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%w: %v", ctx.Err(), err)
		}
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if !strings.HasPrefix(lines[i], "{") {
			continue
		}
		var rep capacityReport
		if err := json.NewDecoder(strings.NewReader(strings.Join(lines[i:], "\n"))).Decode(&rep); err == nil {
			return &rep, nil
		}
	}
	return nil, fmt.Errorf("no capacity report in the answer: %s", tailText(string(out), 400))
}

func tailText(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = "..." + s[len(s)-n:]
	}
	return s
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

func printCapacity(w io.Writer, rep capacityReport, via string) {
	if via != "" {
		via = " via remote " + via
	}
	fmt.Fprintf(w, "machine   %s (%s)%s\n", rep.Host, rep.MachineID, via)
	if !rep.TakenAt.IsZero() {
		fmt.Fprintf(w, "sampled   %s (reused for %s)\n", rep.TakenAt.UTC().Format(time.RFC3339), capacity.Cadence)
	}
	fmt.Fprintf(w, "cores     %d\n", rep.Cores)
	if reason, ok := rep.Unavailable[capacity.FigMemory]; ok {
		fmt.Fprintf(w, "memory    unavailable: %s\n", reason)
	} else {
		fmt.Fprintf(w, "memory    %.1f GiB total, %.1f GiB available\n", float64(rep.MemTotal)/gib, float64(rep.MemAvailable)/gib)
	}
	if reason, ok := rep.Unavailable[capacity.FigLoad]; ok {
		fmt.Fprintf(w, "load      unavailable: %s\n", reason)
	} else {
		fmt.Fprintf(w, "load      %.2f %.2f %.2f (1, 5, 15 min)\n", rep.Load[0], rep.Load[1], rep.Load[2])
	}
	if reason, ok := rep.Unavailable[capacity.FigAgents]; ok {
		fmt.Fprintf(w, "agents    unavailable: %s\n", reason)
	} else {
		labels := slices.Sorted(maps.Keys(rep.Agents))
		parts := make([]string, len(labels))
		for i, l := range labels {
			parts[i] = fmt.Sprintf("%s %d", l, rep.Agents[l])
		}
		fmt.Fprintf(w, "agents    %s (all projects on this machine)\n", strings.Join(parts, ", "))
	}
	fmt.Fprintf(w, "seats     %d engaged here\n", len(rep.Seats))
	for _, st := range rep.Seats {
		fmt.Fprintf(w, "  %s  %s  %s\n", st.Item, st.Session, strings.TrimPrefix(st.Location, kernel.LocationLocal))
	}
	for _, e := range rep.Errors {
		fmt.Fprintf(w, "  unavailable: %s\n", e)
	}
}
