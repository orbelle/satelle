package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
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

// Doctor's budgets. The session-start hook runs every check under one budget
// (a session waits on SessionStart output); a check unfinished at the
// deadline is listed as not checked and runs in full under `satelle doctor`.
// Each configured check is bounded on demand too.
const (
	doctorHookBudget   = 2 * time.Second
	doctorCheckTimeout = 2 * time.Minute
	doctorProbeTimeout = 2 * time.Second
)

// doctorIssue is one finding and the command that fixes it. Doctor never runs
// a fix; Resolve names what satelle doctor --resolve does about it ("" leaves
// it for the user). item and session name a seat finding's seat.
type doctorIssue struct {
	Check   string `json:"check"`
	Problem string `json:"problem"`
	Fix     string `json:"fix"`
	Resolve string `json:"resolve,omitempty"`
	item    string
	session string
}

// What satelle doctor --resolve does about an issue (doctorIssue.Resolve).
const (
	// resolveInstallService installs the service and the sync timer.
	resolveInstallService = "install-service"
	// resolveRestartService restarts satelled.
	resolveRestartService = "restart-service"
	// resolveReleaseSeat releases a seat proven orphaned, without asking: its
	// session is gone, or its item is in a terminal state wherever the session
	// ran.
	resolveReleaseSeat = "release-seat"
	// resolveAsk asks at the terminal whether to release an orphaned seat.
	resolveAsk = "ask"
	// resolveFixItem lists the finding on the one fix item.
	resolveFixItem = "fix-item"
)

// doctorReport is everything one doctor run found: issues, the checks it could
// not finish within its budget, and notes that are facts, not issues.
type doctorReport struct {
	Issues    []doctorIssue `json:"issues"`
	Unchecked []string      `json:"unchecked"`
	Notes     []string      `json:"notes"`
}

// doctorInput is what the checks read. inRepo is false outside a satelle repo,
// where only the machine checks run. cfg is what loaded (a partial load when
// the configuration is invalid).
type doctorInput struct {
	inRepo bool
	r      repo
	cfg    *kernel.Config
	cfgErr error
	// harness is the harness whose agent is asking, "" when it cannot be
	// told: hook.Caller from the environment, or the hook's --harness.
	harness string
	// legacy is the pre-1.0 marker files found in the repo's .satelle, none
	// for a v1 repo: its configuration is not loaded (sto_0f76dd4e).
	legacy []string
}

// doctorCheck is one named check; it returns its issues and notes.
type doctorCheck struct {
	name string
	run  func(ctx context.Context, in *doctorInput) ([]doctorIssue, []string)
}

// loadDoctorInput reads the repo at dir and its configuration without opening
// the kernel, the cache or the ledger for writing.
func loadDoctorInput(dir string) *doctorInput {
	r, err := findRepo(dir)
	if err != nil {
		return &doctorInput{}
	}
	// A pre-1.0 .satelle needs upgrading in the repo; loading it would only
	// list every key v1 does not know.
	if m := kernel.LegacyMarkers(r.DataDir); len(m) > 0 {
		return &doctorInput{inRepo: true, r: r, legacy: m, cfgErr: errors.New(needsUpgradeLine(r.Tree, m)), harness: hook.Caller()}
	}
	cfg, cerr := kernel.LoadConfig(r.DataDir)
	return &doctorInput{inRepo: true, r: r, cfg: cfg, cfgErr: cerr, harness: hook.Caller()}
}

// doctorChecks lists the checks for in: the machine checks always, the repo
// checks and the repo's doctor.toml entries inside a repo. In a pre-1.0 repo
// only the machine checks run: the repo checks read a v1 .satelle.
func doctorChecks(in *doctorInput) []doctorCheck {
	checks := []doctorCheck{{"versions", checkVersions}, {"upgrade", checkUpgrade}}
	if !in.inRepo || len(in.legacy) > 0 {
		return append(checks, doctorCheck{"sync", checkSyncTimerOnly})
	}
	checks = append(checks,
		doctorCheck{"config", checkConfig},
		doctorCheck{substrate.Field, checkSubstrate},
		doctorCheck{substrate.VolatileField, checkVolatile},
		doctorCheck{"init", checkInit},
		doctorCheck{checkHarnessName, checkHarness},
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

// errUnchecked marks a check cut by the run's deadline.
var errUnchecked = errors.New("not checked within the budget")

// runDoctor runs every check concurrently and collects what finished by ctx's
// deadline; the rest are listed as unchecked. It changes nothing.
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
		// Give a check that was killed at the deadline a moment to return.
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

// checkUpgrade names the pre-1.0 repos: the one doctor runs in is an issue,
// fixed only in that repo by the user; each other registered one is a note,
// as checkHarness splits the caller's harness from the others.
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
				out = append(out, doctorIssue{Check: "config", Problem: line, Fix: "satelle validate", Resolve: resolveFixItem})
			}
		}
	}
	return out, nil
}

// checkSubstrate reports each command, flag, help topic and link the repo's
// substrate names that this binary lacks: the rules satelle validate applies
// (internal/substrate). Each finding goes on the fix item
// satelle doctor --resolve opens.
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

// checkVolatile reports each line of the substrate, and of the files
// .satelle/volatile.toml names, that matches one of its volatile patterns
// (kernel.Config.VolatileFindings). A volatile.toml with a problem is the
// config check's to report. Each finding goes on the fix item
// satelle doctor --resolve opens.
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
			// A file another repo owns is fixed there and brought here, never
			// edited in place (sto_1ee018c8).
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
	plan, err := kernel.InitPlan(in.r.DataDir)
	if err != nil {
		return []doctorIssue{{Check: "init", Problem: fmt.Sprintf("cannot read the init template: %v", err), Fix: "satelle init", Resolve: resolveFixItem}}, nil
	}
	var out []doctorIssue
	for _, rel := range plan {
		out = append(out, doctorIssue{Check: "init", Problem: "satelle init would add .satelle/" + rel, Fix: "satelle init", Resolve: resolveFixItem})
	}
	return out, nil
}

// checkHarnessName is the harness check's name.
const checkHarnessName = "harness"

// wireFix is the command that wires harness h.
func wireFix(h string) string { return "satelle agents install " + h }

// checkHarness reports the harnesses of this repo (hook.WiringOf). Each
// harness in use (harnessesInUse: agents.toml runs it, or the repo has its
// configuration directory) whose hook file differs from what satelle agents
// install would write is an issue: absent, holding no satelle hooks, running a
// satelle command this binary lacks (one issue per command), or holding
// satelle hooks other than the current set; a file git tracks or a repo with
// [hook] refresh = false says satelle update leaves it. A harness not in use
// is judged as before: the calling harness unwired is an issue, any other
// unwired one a note. With the caller unknown, nothing wired and nothing in
// use, no harness wired is the issue. An installed hook file an ignore rule
// hides from git is a note naming the rule. It reads the hook files and asks
// git read-only questions.
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

// unwiredProblem is the problem of harness w holding no satelle hooks: its file
// is absent or holds none, and for the calling harness, what its session
// lacks.
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

// inUseHarnessProblems lists how in-use harness w's hook file differs from what
// satelle agents install would write (hook.Pending with stale): none when it
// is current.
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

// checkVersions compares the installed satelle CLI, the running satelled and
// the installed satelled by buildinfo.Drift, the rule satelle service status
// and satelled's footer use. It reads the builds record and probes satelled's
// --version without recording, so it writes nothing.
func checkVersions(ctx context.Context, _ *doctorInput) ([]doctorIssue, []string) {
	unitBody, unitErr := os.ReadFile(unitPath())
	path := ""
	if unitErr == nil {
		path = unitDaemon(string(unitBody))
	}
	if path == "" {
		path = resolveDaemon("")
	}
	var out []doctorIssue
	if unitErr != nil {
		if _, err := os.Stat(path); err != nil {
			return nil, []string{"versions: no satelled unit or binary is installed on this machine (satelle service install)"}
		}
		out = append(out, doctorIssue{Check: "versions", Problem: fmt.Sprintf("%s is not installed, so %s (%s) does not run", serviceUnitName, buildinfo.DaemonName, path),
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
		// A running satelled behind the installed one is put right by a
		// restart; any other mismatch (the CLI's) needs satelle update, which
		// --resolve leaves for the user.
		res := ""
		if slices.Contains(m.Lines, "running") {
			res = resolveRestartService
		}
		out = append(out, doctorIssue{Check: "versions", Problem: m.Text, Fix: m.Remedy, Resolve: res})
	}
	return out, nil
}

// doctorCLILine is the installed satelle CLI's identity: this binary's own when
// it is the CLI beside satelled, else the record. It never runs the CLI, whose
// every run records itself.
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

// healthzBuild is the identity line satelled answers on /healthz.
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

// replicates reports whether this repo replicates its ledger, from its last
// recorded automatic pass: a store other than local. Doctor never reads the
// [ledger] store setting, which only the replication verbs read
// (TestOnlyReplicationVerbsReadTheStore).
func replicates(st syncstate.State, ok bool) bool {
	return ok && st.SyncStore != "" && st.SyncStore != syncstate.LocalStore
}

// checkSync reads the last automatic sync pass from the sync state (never the
// store) and the sync timer's state.
func checkSync(_ context.Context, in *doctorInput) ([]doctorIssue, []string) {
	st, ok, err := syncstate.Load(config.GlobalDir(), uiRepoRoot(in.r))
	if err != nil {
		return []doctorIssue{{Check: "sync", Problem: err.Error(), Fix: "satelle ledger status"}}, nil
	}
	if !replicates(st, ok) {
		return nil, nil
	}
	// A failed pass comes first: it is what the session-start line shows.
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

// checkSyncTimerOnly is the sync check outside a repo: the timer alone.
func checkSyncTimerOnly(context.Context, *doctorInput) ([]doctorIssue, []string) {
	if _, err := os.Stat(unitPath()); err != nil {
		return nil, nil
	}
	return timerIssues()
}

func timerIssues() ([]doctorIssue, []string) {
	if runtime.GOOS != "linux" {
		return nil, []string{"sync: the sync timer is systemd-only; run `satelle ledger autosync` yourself"}
	}
	if _, err := os.Stat(userUnitPath(syncTimerName)); err != nil {
		return []doctorIssue{{Check: "sync", Problem: syncTimerName + " is not installed, so nothing replicates this repo's ledger on its own", Fix: "satelle service install", Resolve: resolveInstallService}}, nil
	}
	if s := syncTimerState(); s != "active" {
		return []doctorIssue{{Check: "sync", Problem: fmt.Sprintf("%s is %s", syncTimerName, s), Fix: "satelle service install", Resolve: resolveInstallService}}, nil
	}
	return nil, nil
}

// checkSeats folds every chain read-only and classes each seat by
// seatlife.Classify: here, elsewhere, gone or unverified. A seat is orphaned
// when its session is gone, its item is terminal in its workflow, its local:
// worktree no longer exists and it was bound on this machine (or records no
// machine): another machine's path cannot be checked here, or it was bound
// here and its session never settled an owner and its hook processes have
// exited. Each orphan is reported with its evidence; a gone session (its
// settled owner exited) and a seat on a terminal item (wherever its session
// ran: nothing more can be done under it) are the two --resolve releases
// without asking, and every other orphan, the unsettled one included, is
// asked about. One note lists every seat's standing.
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

// seatMachine names the machine a seat was bound on, as doctor shows it.
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

// readItem is one folded chain and its bound seats.
type readItem struct {
	it    item.Item
	seats []item.Seat
}

// readItems folds every chain in the repo's runtime without creating it: a
// repo that never ran has no items.
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

// openItems reports each item an [[items]] entry selects that is not in a
// terminal state of its workflow.
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

// runConfiguredCheck runs a doctor.toml check skill in the tree: exit 0 is
// clean, exit 1 is one issue per output line, anything else could not check.
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
		// A doctor.toml check is the repo's own substrate check: its findings
		// go on the fix item.
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

// doctorGroups groups issues by check, the checks in first-seen order.
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

// doctorNotice is the user's short view of a report at session start: the
// issue count, then one line per check with its count and a fix, and no
// problem text (that stays in doctorBlock, the model's context). A check's fix
// is the first non-empty fix among its issues; when none of them has one, it
// is noticeFallbackFix, so every line names an action. It is empty when there
// are no issues; checks the budget cut alone give no notice.
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

// noticeFallbackFix is the notice's fix for a check none of whose issues
// records one: doctor itself lists each issue with what it knows.
const noticeFallbackFix = "satelle doctor (lists each issue)"

// doctorBlock is the session-start summary of a report: one line per check
// with its count, first problem and first fix, then any checks the budget cut.
// It is empty when there is nothing to say.
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
	if len(rep.Unchecked) > 0 {
		u := append([]string(nil), rep.Unchecked...)
		sort.Strings(u)
		fmt.Fprintf(&b, "  not checked within %s at session start: %s; `satelle doctor` runs them\n", budget, strings.Join(u, ", "))
	}
	return b.String()
}
