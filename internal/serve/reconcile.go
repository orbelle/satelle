// Mirror reconciliation (sty_e6e467fe).
//
// The mirror is push-fed: a mutating verb flushes change events plus one
// snapshot before its store closes, fail-silent and under a bounded budget. A
// push that does not land — the service was restarting, down, or unreachable —
// was previously lost forever, because nothing ever re-requested state. A story
// that reached done through a release restart therefore left the UI showing the
// pre-restart frame indefinitely, which reads as a stuck story rather than a
// stale view.
//
// CHOSEN SHAPE: reconcile on the SERVE side. The serving tier re-requests a full
// snapshot for every partition it renders — once at startup, then every
// mirror.DefaultReconcileInterval. This repairs EVERY dropped push, including
// ones the CLI never knew failed (a killed CLI, a network blip, a service that
// was simply down), and adds nothing to the mutating-verb path.
//
// REJECTED: reconcile on the CLI side (retry, or drain before the restart
// converges). It repairs only pushes the CLI survived to notice, and it puts
// retry latency into every mutating verb — exactly what the fail-silent bounded
// budget exists to prevent.
//
// The re-request EXECS THE CLI (`satelle workspace add`) rather than reading a
// repo database directly: the serve binary is forbidden from linking
// internal/cli, internal/app and internal/store (cmd/satelled/deps_test.go),
// and internal/cli imports this package, so exec is the mechanism, not a
// preference. The CLI stays the sole reader of a per-repo database and the
// mirror stays a read-only view.
package serve

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/mirror"
)

// reconcileTimeout bounds one re-seed. A snapshot of a large repo takes a second
// or two; anything beyond this is a hung child, not a slow one.
const reconcileTimeout = 2 * time.Minute

// syncBackoffBase is the first wait after a snapshot stops on a conflict; it
// doubles per repeat up to pushBackoffCap (sty_0367d20c).
const syncBackoffBase = time.Minute

// Retry cadence for a repo whose reseed failed or whose CLI push was marked
// dirty: the retry tick checks for due work, and a failed reseed waits
// defaultRetryDelay, doubling to the pass interval.
const (
	defaultRetryDelay = 30 * time.Second
	defaultRetryTick  = 10 * time.Second
)

// Reconciler re-requests a snapshot for each mirrored partition on an interval.
// Every collaborator is injectable so tests drive it without a real binary.
type Reconciler struct {
	// Interval between passes; zero means mirror.DefaultReconcileInterval.
	Interval time.Duration
	// RetryDelay is the first wait before a failed reseed is re-run on the
	// retry tick; it doubles per consecutive failure up to Interval. Zero means
	// defaultRetryDelay.
	RetryDelay time.Duration
	// RetryTick is how often due retries and dirty repos are checked; zero
	// means defaultRetryTick.
	RetryTick time.Duration
	// DirtyHome is the machine home whose mirror.DirtyDir the CLI marks when a
	// push fails; the retry tick reseeds each marked target. Empty disables it.
	DirtyHome string
	// Timeout bounds a single re-seed; zero means reconcileTimeout.
	Timeout time.Duration
	// Targets lists the repo paths to re-request, newest resolution each pass so
	// a partition added while serving is picked up without a restart.
	Targets func(ctx context.Context) []string
	// Reseed re-requests one repo's snapshot; nil means reseedViaCLI.
	Reseed func(ctx context.Context, repoPath string) error
	// Bound reports whether the repo at a path names its hosted project; nil means
	// config.RepoBoundAt. An unbound repo syncs nothing, so neither Snapshot nor
	// Follow runs for it (sty_0dc600e1).
	Bound func(repoPath string) bool
	// Snapshot execs the CLI workstate snapshot verb; nil means snapshotViaCLI.
	// It runs for a bound repo whose Follow is not running: the backstop for a
	// server without the subscription, or a follow that died.
	Snapshot func(ctx context.Context, repoPath string) error
	// Follow execs the long-running CLI workstate follow verb; nil means
	// followViaCLI. It is started for a bound repo after a pass's snapshot and
	// runs until it ends or ctx does; while it runs, no snapshot is taken.
	//
	// No pass pushes. A bound repo's changes leave by the Pusher, which local
	// appends drive, and the push sends only the rows this machine authored; the
	// periodic whole-repo push sent a stale machine's state (sty_0dc600e1).
	Follow func(ctx context.Context, repoPath string) error
	// SubstratePush execs the CLI config and documents push verbs
	// (substratepush.go); nil means substratePushViaCLI. Always invoked (opt-in,
	// binding and refusal are the verbs' decisions). Its outcome
	// is logged on its own streak and never reaches Record, which persists the
	// snapshot leg.
	SubstratePush func(ctx context.Context, repoPath string) error
	// Log receives a line when a repo STARTS failing, when its reason CHANGES,
	// and when it recovers — never once per pass per failing repo (sty_a2162ee3).
	// nil discards.
	Log func(format string, args ...any)
	// Record persists a snapshot-leg outcome (sty_30696eeb). Called on every
	// pass, including the ones Log suppresses. nil discards.
	Record func(repoPath string, success bool, reason string)
	// PushOff reports the machine-local push switch; true skips the SubstratePush
	// leg while Reseed, Snapshot and Follow keep the UI fed. nil means on.
	PushOff func() (off bool, setting string)
	// Now is the time source for the conflict backoff; nil means time.Now.
	Now func() time.Time

	// holdMu guards holdoff, which Touch reaches from the ingest handler.
	holdMu sync.Mutex
	// holdoff is, per repo path, the backoff a snapshot's conflict stop armed.
	holdoff map[string]*syncHoldoff

	// failing remembers, per repo, the reason it last failed and how many
	// consecutive passes have produced that same reason. It exists so silence
	// can mean "unchanged": a line is written on a transition, never on a
	// repetition. Lazily allocated; only pass() touches it, and pass() is only
	// ever called from Loop's single goroutine, so there is no lock here
	// because there is no concurrency to guard.
	failing map[string]*repoFailure
	// follows holds, per repo, the done channel of its running Follow. Only
	// pass() touches the map; the Follow goroutine only closes its channel.
	follows map[string]chan struct{}
	// retry holds, per repo whose reseed last failed, when the retry tick may
	// re-run it. Loop's goroutine only, like failing.
	retry map[string]*retryState
}

// retryState is one failing repo's next retry and the delay that armed it.
type retryState struct {
	at    time.Time
	delay time.Duration
}

// following reports whether the repo's Follow is still running.
func (r *Reconciler) following(path string) bool {
	done, ok := r.follows[path]
	if !ok {
		return false
	}
	select {
	case <-done:
		delete(r.follows, path)
		return false
	default:
		return true
	}
}

// startFollow runs follow for the repo until it ends or ctx does. A follow that
// ends with an error is logged; the next pass takes a snapshot and starts it again.
func (r *Reconciler) startFollow(ctx context.Context, path string, follow func(context.Context, string) error) {
	if r.follows == nil {
		r.follows = map[string]chan struct{}{}
	}
	done := make(chan struct{})
	r.follows[path] = done
	go func() {
		defer close(done)
		if err := follow(ctx, path); err != nil && ctx.Err() == nil && !errors.Is(err, errChildBusy) {
			r.logf("workstate follow %s: %v", path, err)
		}
	}()
}

// syncHoldoff is one repo's conflict backoff: no snapshot or follow restart
// before nextRun, and the attempt count the next delay doubles from.
type syncHoldoff struct {
	repoKey  string
	attempts int
	nextRun  time.Time
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// heldOff reports whether path's snapshot is backing off at now.
func (r *Reconciler) heldOff(path string, now time.Time) bool {
	r.holdMu.Lock()
	defer r.holdMu.Unlock()
	h := r.holdoff[path]
	return h != nil && now.Before(h.nextRun)
}

// holdOff arms the next, longer, backoff for path and returns its delay.
func (r *Reconciler) holdOff(path string, now time.Time) time.Duration {
	r.holdMu.Lock()
	defer r.holdMu.Unlock()
	if r.holdoff == nil {
		r.holdoff = map[string]*syncHoldoff{}
	}
	h := r.holdoff[path]
	if h == nil {
		h = &syncHoldoff{repoKey: config.RepoKey(path)}
		r.holdoff[path] = h
	}
	d := unconvergedDelay(h.attempts)
	h.attempts++
	h.nextRun = now.Add(d)
	return d
}

// clearHoldoff forgets path's backoff after a snapshot that converged.
func (r *Reconciler) clearHoldoff(path string) {
	r.holdMu.Lock()
	defer r.holdMu.Unlock()
	delete(r.holdoff, path)
}

// Touch resets the conflict backoff of every path under repoKey, so the next
// pass snapshots it again. serve calls it on each local-origin ingest: a local
// change is what can let a stopped snapshot converge.
func (r *Reconciler) Touch(repoKey string) {
	r.holdMu.Lock()
	defer r.holdMu.Unlock()
	for path, h := range r.holdoff {
		if h.repoKey == repoKey {
			delete(r.holdoff, path)
		}
	}
}

// unconvergedDelay is syncBackoffBase doubled per prior attempt, capped.
func unconvergedDelay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if attempt > 20 {
		return pushBackoffCap
	}
	d := syncBackoffBase << attempt
	if d > pushBackoffCap || d <= 0 {
		return pushBackoffCap
	}
	return d
}

// repoFailure is one repo's current failure streak.
type repoFailure struct {
	reason string // err.Error() of the last logged failure
	passes int    // consecutive passes that produced exactly this reason
}

func (r *Reconciler) interval() time.Duration {
	if r.Interval > 0 {
		return r.Interval
	}
	return mirror.DefaultReconcileInterval
}

func (r *Reconciler) retryDelay() time.Duration {
	if r.RetryDelay > 0 {
		return r.RetryDelay
	}
	return defaultRetryDelay
}

func (r *Reconciler) retryTick() time.Duration {
	if r.RetryTick > 0 {
		return r.RetryTick
	}
	return defaultRetryTick
}

// schedule arms or clears path's retry from one reseed outcome.
func (r *Reconciler) schedule(path string, err error) {
	if err == nil {
		delete(r.retry, path)
		return
	}
	if r.retry == nil {
		r.retry = map[string]*retryState{}
	}
	d := r.retryDelay()
	if prev := r.retry[path]; prev != nil {
		d = prev.delay * 2
	}
	if d > r.interval() {
		d = r.interval()
	}
	r.retry[path] = &retryState{at: r.now().Add(d), delay: d}
}

func (r *Reconciler) timeout() time.Duration {
	if r.Timeout > 0 {
		return r.Timeout
	}
	return reconcileTimeout
}

func (r *Reconciler) logf(format string, args ...any) {
	if r.Log != nil {
		r.Log(format, args...)
	}
}

// Loop reconciles once immediately — the pass that repairs a push dropped by the
// restart that just happened — then every interval until ctx ends. A failing
// repo is logged and never stops the loop.
func (r *Reconciler) Loop(ctx context.Context) {
	if _, disabled, set := config.ServerEndpointEnv(); set && disabled {
		// Same off-switch the CLI push path honours: no discovery, no push, and
		// so no background re-request either (hermetic tests set this).
		r.logf("mirror reconcile disabled by %s", config.EnvServerEndpoint)
		return
	}
	r.logf("mirror reconcile every %s", r.interval())
	r.pass(ctx)
	t := time.NewTicker(r.interval())
	defer t.Stop()
	retry := time.NewTicker(r.retryTick())
	defer retry.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.pass(ctx)
		case <-retry.C:
			r.retryPass(ctx)
		}
	}
}

// retryPass reseeds, between full passes, every target whose failed reseed is
// due a retry and every target the CLI marked dirty after a push that did not
// land, so a dropped status change or dispatch start reaches the page within
// seconds. A dirty repo still backing off from a failed retry waits for it.
// A mark for a path that is no longer a target is dropped: the retry never
// seeds a store the full pass would not.
func (r *Reconciler) retryPass(ctx context.Context) {
	now := r.now()
	due := map[string]bool{}
	for path, st := range r.retry {
		if !now.Before(st.at) {
			due[path] = true
		}
	}
	var dirty map[string]bool
	if r.DirtyHome != "" {
		if marked := mirror.DirtyRepos(r.DirtyHome); len(marked) > 0 {
			dirty = map[string]bool{}
			for _, path := range marked {
				dirty[path] = true
			}
		}
	}
	if len(due) == 0 && len(dirty) == 0 {
		return
	}
	if r.Targets == nil {
		return
	}
	targets := map[string]bool{}
	for _, path := range r.Targets(ctx) {
		targets[path] = true
	}
	for path := range dirty {
		if !targets[path] {
			_ = mirror.ClearDirty(r.DirtyHome, path)
			continue
		}
		if st := r.retry[path]; st != nil && now.Before(st.at) {
			continue
		}
		due[path] = true
	}
	paths := make([]string, 0, len(due))
	for path := range due {
		if targets[path] {
			paths = append(paths, path)
		} else {
			delete(r.retry, path)
		}
	}
	sort.Strings(paths)
	reseed := r.Reseed
	if reseed == nil {
		reseed = reseedViaCLI
	}
	for _, path := range paths {
		if ctx.Err() != nil {
			return
		}
		if dirty[path] {
			_ = mirror.ClearDirty(r.DirtyHome, path)
		}
		runCtx, cancel := context.WithTimeout(ctx, r.timeout())
		err := reseed(runCtx, path)
		cancel()
		if errors.Is(err, errChildBusy) {
			// Another child holds the repo and may have read it before the
			// change; keep the mark so the next tick reseeds after it.
			if dirty[path] {
				_ = mirror.MarkDirty(r.DirtyHome, path)
			}
			continue
		}
		if err != nil && dirty[path] {
			_ = mirror.MarkDirty(r.DirtyHome, path)
		}
		r.report(path, err)
		r.logf("sync retry %s: %s", path, okOrError(err))
	}
}

// pass re-requests every target serially — a background repair must not spawn a
// process per partition at once on an operator's machine.
//
// WHICH LEG BACKS OFF (sty_a2162ee3, sty_0367d20c). Every target is re-seeded on
// every pass, including one whose reseed has failed identically for a day: that
// poll is how a repo an operator healed is noticed, and nothing else notifies
// the service of it.
//
// The snapshot leg is different. A snapshot that exits 0 having stopped on a
// story in conflict (in flight here, moved on hosted) cannot converge until
// something local changes, so re-running it each pass only burns a child. It
// waits syncBackoffBase, doubling to pushBackoffCap, and so does the follow
// restart. Recovery is still seen at once: a local-origin ingest calls Touch,
// which clears the wait for the next pass.
func (r *Reconciler) pass(ctx context.Context) {
	if r.Targets == nil {
		return
	}
	reseed := r.Reseed
	if reseed == nil {
		reseed = reseedViaCLI
	}
	snapshot := r.Snapshot
	if snapshot == nil {
		if r.Reseed == nil {
			snapshot = snapshotViaCLI
		} else {
			// Tests inject Reseed; do not exec the CLI unless they also set Snapshot.
			snapshot = func(context.Context, string) error { return nil }
		}
	}
	follow := r.Follow
	if follow == nil {
		if r.Reseed == nil {
			follow = followViaCLI
		} else {
			// Tests inject Reseed; do not exec the CLI unless they also set Follow.
			follow = func(context.Context, string) error { return nil }
		}
	}
	bound := r.Bound
	if bound == nil {
		bound = config.RepoBoundAt
	}
	substrate := r.SubstratePush
	if substrate == nil {
		if r.Reseed == nil {
			substrate = substratePushViaCLI
		} else {
			// Tests inject Reseed; do not exec the CLI unless they also set SubstratePush.
			substrate = func(context.Context, string) error { return nil }
		}
	}
	if r.PushOff != nil {
		if off, _ := r.PushOff(); off {
			substrate = func(context.Context, string) error { return nil }
		}
	}
	seen := map[string]bool{}
	for _, path := range r.Targets(ctx) {
		if ctx.Err() != nil {
			return
		}
		seen[path] = true
		seen[path+substrateKeySuffix] = true
		runCtx, cancel := context.WithTimeout(ctx, r.timeout())
		err := reseed(runCtx, path)
		if errors.Is(err, errChildBusy) {
			err = nil
		}
		leg, outcome, next := "reseed", "ok", r.interval()
		if bound(path) && !r.following(path) {
			now := r.now()
			if r.heldOff(path, now) {
				outcome = "held off"
			} else {
				leg = "snapshot"
				serr := snapshot(runCtx, path)
				if u, ok := unconverged(serr); ok {
					next = r.holdOff(path, now)
					outcome = u.Error()
					serr = nil
				} else if errors.Is(serr, errChildBusy) {
					outcome = "busy"
					serr = nil
				} else if serr == nil {
					r.clearHoldoff(path)
				}
				if serr != nil {
					if err != nil {
						err = fmt.Errorf("%v; snapshot: %w", err, serr)
					} else {
						err = serr
					}
				}
				r.startFollow(ctx, path, follow)
			}
		}
		serr := substrate(runCtx, path)
		if errors.Is(serr, errChildBusy) {
			serr = nil
		}
		cancel()
		r.report(path, err)
		r.reportSubstrate(path, serr)
		if err != nil {
			outcome = "error"
		}
		r.logf("sync %s %s: %s next in %s", leg, path, outcome, next)
		r.logf("sync substrate %s: %s next in %s", path, okOrError(serr), r.interval())
	}
	// Forget repos this pass no longer targets (partition removed, directory
	// gone). Keeping the entry would emit a bogus "recovered" line across a gap
	// the repo was not even being polled over.
	for path := range r.failing {
		if !seen[path] {
			delete(r.failing, path)
		}
	}
	for path := range r.retry {
		if !seen[path] {
			delete(r.retry, path)
		}
	}
}

// report turns one reseed outcome into at most one log line. A repo is reported
// when it STARTS failing, when the reason CHANGES, and when it recovers; a
// repetition of the same reason is counted and not written, so silence means
// unchanged rather than lost.
//
// It never touches whether the reseed ran — suppression is about the log line
// only. A repo whose line is suppressed is still re-seeded on every pass.
func (r *Reconciler) report(path string, err error) {
	r.schedule(path, err)
	if r.Record != nil {
		reason := ""
		if err != nil {
			reason = err.Error()
		}
		r.Record(path, err == nil, reason)
	}
	r.track(path, path, err)
}

// substrateKeySuffix keys a repo's substrate-leg streak apart from its
// work-state streak, so each is logged once and recovers on its own.
const substrateKeySuffix = " substrate"

// reportSubstrate is report for the substrate leg: the same one-line-per-streak
// log discipline, but nothing is recorded — Record persists the snapshot leg, and
// a substrate refusal (a behind area, an unreadable hosted copy) is not a
// work-state failure. A refusal that has a recordable state is already in
// syncstate, written by the push verb itself.
func (r *Reconciler) reportSubstrate(path string, err error) {
	key := path + substrateKeySuffix
	r.track(key, key, err)
}

// track is the streak bookkeeping both legs share: key identifies the streak,
// label is what the log line names.
func (r *Reconciler) track(key, label string, err error) {
	prev := r.failing[key]
	if err == nil {
		if prev != nil {
			r.logf("mirror reconcile %s: recovered after %d failed pass(es)", label, prev.passes)
			delete(r.failing, key)
		}
		// Steady-state success has never been logged and must not start being.
		return
	}
	reason := err.Error()
	if prev != nil && prev.reason == reason {
		prev.passes++
		return
	}
	// First failure, or the reason changed. The format is byte-identical to the
	// line this loop has always written, so an operator's grep still matches.
	r.logf("mirror reconcile %s: %v", label, err)
	if r.failing == nil {
		r.failing = map[string]*repoFailure{}
	}
	r.failing[key] = &repoFailure{reason: reason, passes: 1}
}

func okOrError(err error) string {
	if err != nil {
		return "error"
	}
	return "ok"
}

// mirrorTargets returns the repo paths of every partition the mirror renders
// whose directory still exists AND whose store this machine home can answer for.
// Driving from PARTITIONS rather than the workspace registry is deliberate: the
// registry accumulates paths that were never seeded (or no longer exist), and a
// background loop must never create or seed a store on their behalf.
func mirrorTargets(ms *mirror.Store, globalDir string) func(context.Context) []string {
	return func(ctx context.Context) []string {
		details, err := ms.ListPartitionDetails(ctx)
		if err != nil {
			return nil
		}
		var out []string
		for _, d := range details {
			path := strings.TrimSpace(d.Path)
			if path == "" {
				continue
			}
			if st, err := os.Stat(path); err != nil || !st.IsDir() {
				continue
			}
			if !storeExists(globalDir, path) {
				continue
			}
			out = append(out, path)
		}
		return out
	}
}

// storeExists reports whether a repo database for path exists under this
// service's home. It STATS the file and never opens it — the CLI stays the sole
// reader. The check exists because a service whose SATELLE_HOME differs from the
// one that seeded a partition would otherwise re-seed it from an empty store and
// wipe the very view it is meant to repair.
func storeExists(globalDir, repoPath string) bool {
	candidates := []string{
		filepath.Join(globalDir, config.RepoKey(repoPath), "satelle.db"),
		filepath.Join(repoPath, ".satelle", "satelle.db"), // legacy in-repo layout
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return true
		}
	}
	return false
}

// reseedViaCLI runs `satelle workspace add` inside repoPath. That verb already
// builds the full snapshot and posts it to the ingest endpoint, so reconcile
// needs no second code path and no new transport. Its ingest is stamped hosted
// origin (mirror.EnvIngestOrigin): it only re-reads the store, and as local it
// would set off a push for every target on every pass (sty_0367d20c).
func reseedViaCLI(ctx context.Context, repoPath string) error {
	return gated(ctx, repoPath, kindReseed, false, func(bin string) error {
		_, err := execVerb(ctx, bin, repoPath, reseedEnv, "workspace", "add")
		return err
	})
}

// reseedEnv is the environment the reseed child adds to satelled's own.
var reseedEnv = []string{mirror.EnvIngestOrigin + "=" + string(mirror.OriginHosted)}

// snapshotArgs is the flag-free verb each pass runs. It pages the project and
// sends the last pass's since-token, and checkpoints every page, so a child cut
// off at reconcileTimeout resumes where it stopped rather than starting over
// (sty_2cb4e1f6). The loop adds no flag of its own.
var snapshotArgs = []string{"sync", "workstate", "snapshot"}

// snapshotViaCLI runs `satelle sync workstate snapshot` inside repoPath. The
// CLI no-ops when work-state areas are local. A snapshot that exits 0 having
// stopped on stories in conflict returns errUnconverged.
func snapshotViaCLI(ctx context.Context, repoPath string) error {
	return gated(ctx, repoPath, kindSnapshot, false, func(bin string) error {
		o, err := execWithOutcome(ctx, bin, repoPath, snapshotArgs...)
		if err != nil {
			return err
		}
		if o.Conflict > 0 {
			return errUnconverged{n: o.Conflict, what: "conflict"}
		}
		return nil
	})
}

// followViaCLI runs `satelle sync workstate follow` inside repoPath until it
// ends or ctx does. The CLI exits non-zero for a server without the subscription.
func followViaCLI(ctx context.Context, repoPath string) error {
	return gated(ctx, repoPath, kindFollow, false, func(bin string) error {
		_, err := execVerb(ctx, bin, repoPath, nil, "sync", "workstate", "follow")
		return err
	})
}

// fetchViaCLI runs `satelle sync workstate fetch <id>` inside repoPath, which
// brings back a story's history the checkout left on the server and refreshes
// the mirror. The CLI no-ops for a held story and a local-only checkout. A
// second page loading at once waits for the running fetch, within fetchTimeout.
func fetchViaCLI(ctx context.Context, repoPath, id string) error {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	return gated(ctx, repoPath, kindFetch, true, func(bin string) error {
		_, err := execVerb(ctx, bin, repoPath, nil, "sync", "workstate", "fetch", id)
		return err
	})
}

// fetchTimeout bounds a story page's wait on a fetch.
const fetchTimeout = 20 * time.Second

// satelleBinary resolves the CLI as a sibling of this executable first — the
// service runs the installed satelled and its satelle is installed beside
// it, so a dev build must not reconcile through whatever is on PATH.
func satelleBinary() (string, error) {
	self, err := os.Executable()
	if err != nil {
		self = ""
	}
	return satelleBinaryFrom(self)
}

func satelleBinaryFrom(self string) (string, error) {
	name := "satelle"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if self != "" {
		cand := filepath.Join(filepath.Dir(self), name)
		if st, err := os.Stat(cand); err == nil && !st.IsDir() {
			return cand, nil
		}
	}
	bin, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("no satelle binary beside the service or on PATH: %w", err)
	}
	return bin, nil
}
