package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/buildinfo"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/gitfacts"
	"github.com/bobmcallan/satelle/internal/hook"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/seatlife"
	"github.com/bobmcallan/satelle/internal/substrate"
	"github.com/bobmcallan/satelle/internal/syncstate"
)

const (
	doctorHookBudget   = 2 * time.Second
	doctorCheckTimeout = 2 * time.Minute
	doctorProbeTimeout = 2 * time.Second
)

type doctorIssue struct {
	Check   string `json:"check"`
	Problem string `json:"problem"`
	Fix     string `json:"fix"`
	Resolve string `json:"resolve,omitempty"`
	item    string
	session string
	path    string
	skew    bool
}

const (
	resolveInstallService = "install-service"
	resolveRestartService = "restart-service"
	resolveReleaseSeat    = "release-seat"
	resolveAsk            = "ask"
	resolveFixItem        = "fix-item"
	resolveRemoveProject  = "remove-project"
)

func unattended(is doctorIssue) bool {
	return is.Resolve == resolveReleaseSeat || (is.Resolve == resolveRestartService && is.skew)
}

func (is doctorIssue) key() string {
	if is.item != "" {
		return is.item + " " + is.session
	}
	return is.Problem
}

type doctorReport struct {
	Issues    []doctorIssue `json:"issues"`
	Unchecked []string      `json:"unchecked"`
	Notes     []string      `json:"notes"`
}

type doctorInput struct {
	inRepo  bool
	r       repo
	cfg     *kernel.Config
	cfgErr  error
	harness string
	legacy  []string

	sessionStart bool
}

type doctorCheck struct {
	name string
	run  func(ctx context.Context, in *doctorInput) ([]doctorIssue, []string)
}

func loadDoctorInput(dir string) *doctorInput {
	r, err := findRepo(dir)
	if err != nil {
		return &doctorInput{}
	}
	if m := kernel.LegacyMarkers(r.DataDir); len(m) > 0 {
		return &doctorInput{inRepo: true, r: r, legacy: m, cfgErr: errors.New(needsUpgradeLine(r.Tree, m)), harness: hook.Caller()}
	}
	cfg, cerr := kernel.LoadConfig(r.DataDir)
	return &doctorInput{inRepo: true, r: r, cfg: cfg, cfgErr: cerr, harness: hook.Caller()}
}

func doctorChecks(in *doctorInput) []doctorCheck {
	checks := []doctorCheck{{"versions", checkVersions}, {"upgrade", checkUpgrade}, {checkProjectsName, checkProjects}}
	if !in.inRepo || len(in.legacy) > 0 {
		return append(checks, doctorCheck{"sync", checkSyncTimerOnly})
	}
	checks = append(checks,
		doctorCheck{"config", checkConfig},
		doctorCheck{substrate.Field, checkSubstrate},
		doctorCheck{substrate.VolatileField, checkVolatile},
		doctorCheck{"init", checkInit},
		doctorCheck{"agents", checkAgents},
		doctorCheck{checkHarnessName, checkHarness},
	)
	if !in.sessionStart {
		checks = append(checks, doctorCheck{checkContextName, checkContext})
	}
	checks = append(checks,
		doctorCheck{"sync", checkSync},
		doctorCheck{"seats", checkSeats},
	)
	if in.cfg != nil {
		for _, c := range in.cfg.Doctor.Checks {
			c := c
			checks = append(checks, doctorCheck{"check:" + c.Skill, func(ctx context.Context, in *doctorInput) ([]doctorIssue, []string) {
				return runConfiguredCheck(ctx, in, c)
			}})
		}
		for _, o := range in.cfg.Doctor.Open {
			o := o
			checks = append(checks, doctorCheck{"open:" + o.Name, func(ctx context.Context, in *doctorInput) ([]doctorIssue, []string) {
				return openItems(in, o)
			}})
		}
	}
	return checks
}

var errUnchecked = errors.New("not checked within the budget")

func runDoctor(ctx context.Context, in *doctorInput) doctorReport {
	checks := doctorChecks(in)
	type result struct {
		issues []doctorIssue
		notes  []string
		done   bool
	}
	results := make([]result, len(checks))
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i, c := range checks {
		wg.Add(1)
		go func(i int, c doctorCheck) {
			defer wg.Done()
			issues, notes := c.run(ctx, in)
			mu.Lock()
			defer mu.Unlock()
			if ctx.Err() != nil && len(issues) == 1 && issues[0].Problem == errUnchecked.Error() {
				return
			}
			results[i] = result{issues: issues, notes: notes, done: true}
		}(i, c)
	}
	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-ctx.Done():
		select {
		case <-finished:
		case <-time.After(250 * time.Millisecond):
		}
	}
	mu.Lock()
	defer mu.Unlock()
	rep := doctorReport{Issues: []doctorIssue{}, Unchecked: []string{}, Notes: []string{}}
	for i, c := range checks {
		if !results[i].done {
			rep.Unchecked = append(rep.Unchecked, c.name)
			continue
		}
		rep.Issues = append(rep.Issues, results[i].issues...)
		rep.Notes = append(rep.Notes, results[i].notes...)
	}
	return rep
}

func checkUpgrade(_ context.Context, in *doctorInput) ([]doctorIssue, []string) {
	var issues []doctorIssue
	var notes []string
	here := ""
	if in.inRepo {
		here = filepath.Dir(in.r.DataDir)
	}
	if len(in.legacy) > 0 {
		issues = append(issues, doctorIssue{Check: "upgrade", Problem: needsUpgradeLine(in.r.Tree, in.legacy), Fix: kernel.UpgradeCommand})
	}
	for _, p := range registeredLegacy() {
		if p.Root != here {
			notes = append(notes, "upgrade: "+needsUpgradeLine(p.Path, p.Markers))
		}
	}
	return issues, notes
}

func checkConfig(_ context.Context, in *doctorInput) ([]doctorIssue, []string) {
	var out []doctorIssue
	for _, err := range kernel.Validate(in.r.DataDir) {
		for _, line := range strings.Split(err.Error(), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				if strings.HasPrefix(line, kernel.AgentsLocalFile+": ") {
					out = append(out, doctorIssue{Check: "config", Problem: line, Fix: "edit .satelle/" + kernel.AgentsLocalFile + " (this machine's file)"})
					continue
				}
				out = append(out, doctorIssue{Check: "config", Problem: line, Fix: "satelle validate", Resolve: resolveFixItem})
			}
		}
	}
	return out, nil
}

func checkAgents(_ context.Context, in *doctorInput) ([]doctorIssue, []string) {
	var notes []string
	if in.cfg != nil {
		for _, o := range in.cfg.Overrides {
			eff := in.cfg.Agents[o.Name]
			var parts []string
			for _, f := range o.Fields {
				parts = append(parts, fmt.Sprintf("%s %s -> %s", f, bindingField(o.Tracked, f), bindingField(eff, f)))
			}
			notes = append(notes, fmt.Sprintf("agents: %s overridden by .satelle/%s: %s", o.Name, kernel.AgentsLocalFile, strings.Join(parts, ", ")))
		}
	}
	rel, err := filepath.Rel(in.r.Tree, filepath.Join(in.r.DataDir, kernel.AgentsLocalFile))
	if err != nil {
		return nil, notes
	}
	rel = filepath.ToSlash(rel)
	g := gitfacts.Repo{Dir: in.r.Tree}
	var out []doctorIssue
	if ignored, err := g.IgnoredBy(rel); err == nil && ignored[rel] == "" {
		out = append(out, doctorIssue{Check: "agents",
			Problem: "no ignore rule covers " + rel + ", so a machine's overrides would show as a change in git",
			Fix:     "add the line " + kernel.AgentsLocalFile + " to .satelle/.gitignore and commit it through this repo's workflow", Resolve: resolveFixItem})
	}
	if tracked, err := g.Tracks(rel); err == nil && tracked {
		out = append(out, doctorIssue{Check: "agents",
			Problem: rel + " is this machine's file but git tracks it",
			Fix:     "git rm --cached " + rel + " and commit, through this repo's workflow", Resolve: resolveFixItem})
	}
	return out, notes
}

func bindingField(b agent.Binding, field string) string {
	switch field {
	case "command":
		return fmt.Sprintf("%q", b.Command)
	case "model":
		return fmt.Sprintf("%q", b.Model)
	case "effort":
		return fmt.Sprintf("%q", b.Effort)
	case "tools":
		return "[" + strings.Join(b.Tools, " ") + "]"
	case "deny":
		return "[" + strings.Join(b.Deny, " ") + "]"
	case "placement":
		return fmt.Sprintf("%q", b.Placement)
	case "brief":
		return fmt.Sprintf("%q", b.Brief)
	}
	return ""
}

func checkSubstrate(_ context.Context, in *doctorInput) ([]doctorIssue, []string) {
	found, err := substrate.Check(in.r.DataDir, substrateTree(NewRootCmd()))
	if err != nil {
		return []doctorIssue{{Check: substrate.Field, Problem: err.Error(), Fix: "satelle validate", Resolve: resolveFixItem}}, nil
	}
	var out []doctorIssue
	for _, f := range found {
		out = append(out, doctorIssue{Check: substrate.Field, Problem: f.String(),
			Fix: "edit that line to name what this binary has (satelle help, satelle <command> --help)", Resolve: resolveFixItem})
	}
	return out, nil
}

func checkVolatile(_ context.Context, in *doctorInput) ([]doctorIssue, []string) {
	if in.cfg == nil || len(in.cfg.Volatile.Problems()) > 0 {
		return nil, nil
	}
	found, err := in.cfg.VolatileFindings()
	if err != nil {
		return []doctorIssue{{Check: substrate.VolatileField, Problem: err.Error(), Fix: "satelle validate", Resolve: resolveFixItem}}, nil
	}
	fix := "reword the line so it stays true"
	if d := in.cfg.Volatile.Dated; d != "" {
		fix += ", or, if it is history, give it a date `dated` matches (" + d + ")"
	}
	if m := in.cfg.Volatile.Exempt; m != "" {
		fix += ", or mark it " + m
	}
	fix += "; patterns: .satelle/" + kernel.VolatileFile
	var out []doctorIssue
	for _, f := range found {
		fx := fix
		if u, ok := in.cfg.Volatile.UpstreamOf(f.File); ok {
			fx = "this file is owned by " + u.Owner + ": fix the line there and bring the fixed file here"
			if u.Fix != "" {
				fx += " (" + u.Fix + ")"
			}
			fx += "; do not edit it in place"
		}
		out = append(out, doctorIssue{Check: substrate.VolatileField, Problem: f.String(), Fix: fx, Resolve: resolveFixItem})
	}
	return out, nil
}

func checkInit(_ context.Context, in *doctorInput) ([]doctorIssue, []string) {
	out, err := initIssues(in.r.DataDir)
	if err != nil {
		return []doctorIssue{{Check: "init", Problem: fmt.Sprintf("cannot read the init template: %v", err), Fix: "satelle update"}}, nil
	}
	return out, nil
}

const checkHarnessName = "harness"

func wireFix(h string) string { return "satelle agents install " + h }

func checkHarness(_ context.Context, in *doctorInput) ([]doctorIssue, []string) {
	tree := substrateTree(NewRootCmd())
	stale := staleHook(tree)
	var cat agent.Catalog
	optedOut := false
	if in.cfg != nil {
		cat = in.cfg.Agents
		optedOut = !in.cfg.Settings.Hook.Refreshes()
	}
	inUse := harnessesInUse(in.r.Tree, cat)
	var issues []doctorIssue
	var notes, unwired []string
	anyWired := false
	for _, w := range hook.WiringOf(in.r.Tree) {
		if w.Wired() {
			anyWired = true
		}
		if w.State == hook.WiringIgnored {
			notes = append(notes, fmt.Sprintf("%s: %s is installed, but %s hides %s from git, so a clone is not wired", checkHarnessName, w.Harness, w.Rule, w.File))
		}
		if slices.Contains(inUse, w.Harness) {
			problems := inUseHarnessProblems(in, w, tree, stale)
			if len(problems) == 0 {
				continue
			}
			suffix := ""
			if tracked, err := (gitfacts.Repo{Dir: in.r.Tree}).Tracks(w.File); err == nil && tracked {
				suffix += "; git tracks this file, so satelle update leaves it: reinstall and commit the change in this repo"
			}
			if optedOut {
				suffix += "; [hook] refresh = false, so satelle update leaves it"
			}
			for _, p := range problems {
				issues = append(issues, doctorIssue{Check: checkHarnessName, Problem: p + suffix, Fix: wireFix(w.Harness)})
			}
			continue
		}
		switch {
		case w.Wired():
		case w.Harness == in.harness:
			issues = append(issues, doctorIssue{Check: checkHarnessName, Problem: unwiredProblem(in, w), Fix: wireFix(w.Harness)})
		default:
			unwired = append(unwired, w.Harness)
		}
	}
	if in.harness == "" && !anyWired && len(inUse) == 0 {
		return []doctorIssue{{Check: checkHarnessName,
			Problem: fmt.Sprintf("no harness is wired (%s): no agent session here gets satelle's context or edit gate", strings.Join(hook.Harnesses, ", ")),
			Fix:     wireFix("<harness>")}}, notes
	}
	if len(unwired) > 0 {
		notes = append(notes, fmt.Sprintf("%s: not wired: %s (%s)", checkHarnessName, strings.Join(unwired, ", "), wireFix("<harness>")))
	}
	return issues, notes
}

func unwiredProblem(in *doctorInput, w hook.Wiring) string {
	what := "is absent"
	if w.Present {
		what = "holds no satelle hooks"
	}
	p := fmt.Sprintf("%s: %s %s", w.Harness, w.File, what)
	if w.Harness == in.harness {
		p += ", so this session has no satelle context or edit gate"
	}
	return p
}

func inUseHarnessProblems(in *doctorInput, w hook.Wiring, tree substrate.Tree, stale func(string) bool) []string {
	_, changed, err := hook.Pending(in.r.Tree, w.Harness, stale)
	switch {
	case errors.Is(err, hook.ErrNotOwned):
		return []string{fmt.Sprintf("%s: %s is not satelle's file, so %s sessions here get no satelle hooks", w.Harness, w.File, w.Harness)}
	case err != nil:
		return []string{fmt.Sprintf("%s: %v", w.Harness, err)}
	case !changed:
		return nil
	}
	var out []string
	cmds, _ := hook.Commands(in.r.Tree, w.Harness)
	for _, c := range cmds {
		if why, bad := staleCall(tree, c); bad {
			out = append(out, fmt.Sprintf("%s: %s runs `%s`, which this binary lacks (%s)", w.Harness, w.File, c, why))
		}
	}
	switch {
	case !w.Wired():
		out = append(out, unwiredProblem(in, w))
	case len(out) == 0:
		out = append(out, fmt.Sprintf("%s: %s's satelle hooks are not this binary's current set", w.Harness, w.File))
	}
	return out
}

func checkVersions(ctx context.Context, _ *doctorInput) ([]doctorIssue, []string) {
	unitBody, unitErr := os.ReadFile(daemonDefPath())
	path := ""
	if unitErr == nil {
		path = defDaemon(string(unitBody))
	}
	if path == "" {
		path = resolveDaemon("")
	}
	var out []doctorIssue
	if unitErr != nil {
		if _, err := os.Stat(path); err != nil {
			return nil, []string{"versions: no satelled " + defNoun + " or binary is installed on this machine (satelle service install)"}
		}
		out = append(out, doctorIssue{Check: "versions", Problem: fmt.Sprintf("%s is not installed, so %s (%s) does not run", daemonDefName, buildinfo.DaemonName, path),
			Fix: "satelle service install", Resolve: resolveInstallService})
	}
	home := config.GlobalDir()
	installed := buildinfo.Lookup(home, path)
	if !buildinfo.Known(installed) {
		pctx, cancel := context.WithTimeout(ctx, doctorProbeTimeout)
		if line, err := buildinfo.Probe(pctx, path); err == nil {
			installed = line
		}
		cancel()
	}
	cli := doctorCLILine(home, path)
	running := ""
	if unitErr == nil {
		gc, err := config.LoadGlobal()
		if err != nil {
			return []doctorIssue{{Check: "versions", Problem: err.Error(), Fix: "satelle settings"}}, nil
		}
		ep := fmt.Sprintf("http://127.0.0.1:%d/healthz", gc.Service.ResolvePort())
		r, ok := healthzBuild(ctx, ep)
		if !ok {
			out = append(out, doctorIssue{Check: "versions", Problem: fmt.Sprintf("satelled is installed but not answering at %s", ep), Fix: "satelle service restart", Resolve: resolveRestartService})
		} else {
			running = r
		}
	}
	for _, m := range buildinfo.Drift(cli, running, installed) {
		res := ""
		skew := slices.Contains(m.Lines, "running")
		if skew {
			res = resolveRestartService
		}
		out = append(out, doctorIssue{Check: "versions", Problem: m.Text, Fix: m.Remedy, Resolve: res, skew: skew})
	}
	return out, nil
}

func doctorCLILine(home, daemonPath string) string {
	cli := buildinfo.CLIBeside(daemonPath)
	if self, err := os.Executable(); err == nil {
		if a, b := canonicalFile(self), canonicalFile(cli); a != "" && a == b {
			return buildinfo.Resolve().String()
		}
	}
	return buildinfo.Lookup(home, cli)
}

func canonicalFile(p string) string {
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return ""
	}
	return r
}

func healthzBuild(ctx context.Context, ep string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, doctorProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep, nil)
	if err != nil {
		return "", false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	var info buildinfo.Info
	if json.NewDecoder(resp.Body).Decode(&info) != nil || info.Version == "" {
		return "unknown (this satelled reports no build identity on /healthz)", true
	}
	return info.String(), true
}

func replicates(st syncstate.State, ok bool) bool {
	return ok && st.SyncStore != "" && st.SyncStore != syncstate.LocalStore
}

func checkSync(_ context.Context, in *doctorInput) ([]doctorIssue, []string) {
	st, ok, err := syncstate.Load(config.GlobalDir(), uiRepoRoot(in.r))
	if err != nil {
		return []doctorIssue{{Check: "sync", Problem: err.Error(), Fix: "satelle ledger status"}}, nil
	}
	if !replicates(st, ok) {
		return nil, nil
	}
	var out []doctorIssue
	if ok && st.SyncReason != "" {
		out = append(out, doctorIssue{Check: "sync", Problem: "the last sync pass failed: " + st.SyncReason, Fix: "satelle ledger status"})
	}
	timer, notes := timerIssues()
	out = append(out, timer...)
	if ok && !st.SyncLastAttempt.IsZero() {
		notes = append(notes, fmt.Sprintf("sync: last pass %s with %s: pushed %d item(s), pulled %d item(s), forked %d%s",
			st.SyncLastAttempt.Local().Format("2006-01-02 15:04:05"), st.SyncStore, st.SyncPushed, st.SyncPulled, len(st.SyncForked), forkedList(st.SyncForked)))
	} else {
		notes = append(notes, "sync: no automatic pass recorded for this repo yet")
	}
	return out, notes
}

func forkedList(f []string) string {
	if len(f) == 0 {
		return ""
	}
	return ": " + strings.Join(f, ", ")
}

func checkSyncTimerOnly(context.Context, *doctorInput) ([]doctorIssue, []string) {
	if _, err := os.Stat(daemonDefPath()); err != nil {
		return nil, nil
	}
	return timerIssues()
}

func timerIssues() ([]doctorIssue, []string) {
	if !serviceManaged {
		return nil, []string{"sync: satelle manages the sync timer only under systemd and launchd; run `satelle ledger autosync` yourself"}
	}
	if _, err := os.Stat(syncDefPath()); err != nil {
		return []doctorIssue{{Check: "sync", Problem: syncDefName + " is not installed, so nothing replicates this repo's ledger on its own", Fix: "satelle service install", Resolve: resolveInstallService}}, nil
	}
	if s := syncTimerState(); !syncTimerLive(s) {
		return []doctorIssue{{Check: "sync", Problem: fmt.Sprintf("%s is %s", syncDefName, s), Fix: "satelle service install", Resolve: resolveInstallService}}, nil
	}
	return nil, nil
}

func checkSeats(_ context.Context, in *doctorInput) ([]doctorIssue, []string) {
	items, err := readItems(in)
	if err != nil {
		return []doctorIssue{{Check: "seats", Problem: err.Error(), Fix: "satelle ledger verify"}}, nil
	}
	home := config.GlobalDir()
	here, _ := config.ReadMachineID(home)
	var out []doctorIssue
	engaged := map[string]bool{}
	var elsewhere, gone, unverified []string
	seen := 0
	for _, ri := range items {
		terminal := false
		if w := in.cfg.Workflows[ri.it.Workflow]; w != nil && w.IsTerminal(ri.it.Status) {
			terminal = true
		}
		for _, s := range ri.seats {
			seen++
			rec, ok := seatlife.Load(home, s.Session)
			st, ev := seatlife.Classify(here, s, rec, ok)
			switch st {
			case seatlife.Here:
				if !terminal {
					engaged[ri.it.ID] = true
				}
			case seatlife.Elsewhere:
				elsewhere = append(elsewhere, fmt.Sprintf("%s %s %s", ri.it.ID, s.Session, ev))
			case seatlife.Gone:
				gone = append(gone, fmt.Sprintf("%s %s (%s)", ri.it.ID, s.Session, ev))
			default:
				unverified = append(unverified, fmt.Sprintf("%s %s (%s)", ri.it.ID, s.Session, ev))
			}
			why := ""
			switch {
			case st == seatlife.Gone:
				why = "session gone: " + string(ev)
			case terminal:
				why = fmt.Sprintf("its item is %s, a terminal state", ri.it.Status)
			case s.Machine == "" || s.Machine == here:
				if p, ok := strings.CutPrefix(s.Location, kernel.LocationLocal); ok {
					if _, err := os.Stat(p); err != nil {
						why = "its worktree " + p + " no longer exists"
					}
				}
			}
			if why == "" && s.Machine == here && ok && rec.Lapsed() {
				why = "session unverified: " + string(ev)
			}
			if why == "" {
				continue
			}
			res := resolveAsk
			if st == seatlife.Gone || terminal {
				res = resolveReleaseSeat
			}
			out = append(out, doctorIssue{Check: "seats",
				Problem: fmt.Sprintf("%s is seated by session %s at %s, but %s (item %s; machine: %s; session %s: %s (%s); bound %s)",
					ri.it.ID, s.Session, s.Location, why, ri.it.Status, seatMachine(s, here), s.Session, st, ev, seatBound(s)),
				Fix:     fmt.Sprintf("satelle %s release %s --session %s", ri.it.Kind, ri.it.ID, s.Session),
				Resolve: res, item: ri.it.ID, session: s.Session})
		}
	}
	if seen == 0 {
		return out, nil
	}
	note := fmt.Sprintf("seats: %d engaged here", len(engaged))
	for _, part := range []struct {
		name string
		list []string
	}{{"elsewhere", elsewhere}, {"gone", gone}, {"unverified", unverified}} {
		if len(part.list) > 0 {
			note += "; " + part.name + ": " + strings.Join(part.list, ", ")
		}
	}
	return out, []string{note}
}

func seatMachine(s item.Seat, here string) string {
	switch {
	case s.Machine == "":
		return "not recorded"
	case s.Machine == here:
		return "this machine"
	case s.Host != "":
		return fmt.Sprintf("%s (%s)", s.Host, s.Machine)
	}
	return s.Machine
}

func seatBound(s item.Seat) string {
	if s.BoundAt.IsZero() {
		return "at an unknown time"
	}
	return s.BoundAt.Local().Format("2006-01-02 15:04:05")
}

type readItem struct {
	it    item.Item
	seats []item.Seat
}

func readItems(in *doctorInput) ([]readItem, error) {
	st, err := ledger.OpenExisting(in.r.Runtime)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ids, err := st.Items()
	if err != nil {
		return nil, err
	}
	var out []readItem
	for _, id := range ids {
		evs, err := st.Read(id)
		if err != nil {
			continue
		}
		it, err := item.Fold(evs)
		if err != nil {
			continue
		}
		out = append(out, readItem{it: it, seats: item.FoldSeats(evs)})
	}
	return out, nil
}

func openItems(in *doctorInput, o kernel.DoctorOpen) ([]doctorIssue, []string) {
	items, err := readItems(in)
	if err != nil {
		return []doctorIssue{{Check: "open:" + o.Name, Problem: err.Error(), Fix: "satelle ledger verify"}}, nil
	}
	sel := o.Selection()
	var out []doctorIssue
	for _, ri := range items {
		if !sel.Matches(ri.it) {
			continue
		}
		if w := in.cfg.Workflows[ri.it.Workflow]; w != nil && w.IsTerminal(ri.it.Status) {
			continue
		}
		out = append(out, doctorIssue{Check: "open:" + o.Name,
			Problem: fmt.Sprintf("%s is %s: %s", ri.it.ID, ri.it.Status, ri.it.Title),
			Fix:     strings.ReplaceAll(o.Fix, "{id}", ri.it.ID)})
	}
	return out, nil
}

func runConfiguredCheck(ctx context.Context, in *doctorInput, c kernel.DoctorCheck) ([]doctorIssue, []string) {
	name := "check:" + c.Skill
	sk, ok := in.cfg.Skills[c.Skill]
	if !ok || !sk.IsCheck() {
		return []doctorIssue{{Check: name, Problem: fmt.Sprintf("skill %s is not a check skill", c.Skill), Fix: "satelle validate"}}, nil
	}
	code, out, err := gate.RunCheckCode(ctx, sk.Script, in.r.Tree, gate.CheckEnv{}, doctorCheckTimeout)
	if ctx.Err() != nil {
		return []doctorIssue{{Check: name, Problem: errUnchecked.Error()}}, nil
	}
	switch {
	case err != nil:
		return []doctorIssue{{Check: name, Problem: "could not check: " + err.Error(), Fix: "run the script in .satelle/skills/" + c.Skill + ".md"}}, nil
	case code == 0:
		return nil, nil
	case code == 1:
		var issues []doctorIssue
		for _, line := range strings.Split(out, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				issues = append(issues, doctorIssue{Check: name, Problem: line, Fix: c.Fix, Resolve: resolveFixItem})
			}
		}
		if len(issues) == 0 {
			issues = append(issues, doctorIssue{Check: name, Problem: "the check failed and printed nothing", Fix: c.Fix, Resolve: resolveFixItem})
		}
		return issues, nil
	default:
		return []doctorIssue{{Check: name, Problem: fmt.Sprintf("could not check (exit %d): %s", code, strings.TrimSpace(out)), Fix: "run the script in .satelle/skills/" + c.Skill + ".md"}}, nil
	}
}

func doctorGroups(issues []doctorIssue) (order []string, byCheck map[string][]doctorIssue) {
	byCheck = map[string][]doctorIssue{}
	for _, is := range issues {
		if _, ok := byCheck[is.Check]; !ok {
			order = append(order, is.Check)
		}
		byCheck[is.Check] = append(byCheck[is.Check], is)
	}
	return order, byCheck
}

func doctorNotice(rep doctorReport) string {
	if len(rep.Issues) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "satelle doctor: %d issue(s); run `satelle doctor` for each\n", len(rep.Issues))
	order, byCheck := doctorGroups(rep.Issues)
	for _, c := range order {
		fix := noticeFallbackFix
		for _, is := range byCheck[c] {
			if is.Fix != "" {
				fix = is.Fix
				break
			}
		}
		fmt.Fprintf(&b, "  %s (%d) — fix: %s\n", c, len(byCheck[c]), fix)
	}
	return b.String()
}

const noticeFallbackFix = "satelle doctor (lists each issue)"

func doctorBlock(rep doctorReport, budget time.Duration) string {
	if len(rep.Issues) == 0 && len(rep.Unchecked) == 0 {
		return ""
	}
	var b strings.Builder
	if len(rep.Issues) > 0 {
		fmt.Fprintf(&b, "satelle doctor: %d issue(s) (read-only; `satelle doctor` lists each with its fix)\n", len(rep.Issues))
		order, byCheck := doctorGroups(rep.Issues)
		for _, c := range order {
			first := byCheck[c][0]
			fmt.Fprintf(&b, "  %s (%d): %s — fix: %s\n", c, len(byCheck[c]), first.Problem, first.Fix)
		}
	}
	if n := userDecisions(rep.Issues); n > 0 {
		fmt.Fprintf(&b, "  substrate findings (%d): fixing them changes the substrate. %s\n", n, substrateAsk)
	}
	if len(rep.Unchecked) > 0 {
		u := append([]string(nil), rep.Unchecked...)
		sort.Strings(u)
		fmt.Fprintf(&b, "  not checked within %s at session start: %s; `satelle doctor` runs them\n", budget, strings.Join(u, ", "))
	}
	return b.String()
}
