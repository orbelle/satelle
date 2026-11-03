// Hosted workstate push (sty_c526753a).
//
// Mutating CLI verbs publish locally (POST /ingest/snapshot). satelled forwards
// that to satelle.dev by exec'ing `satelle sync workstate push` in the repo —
// the same exec seam reconcile already uses for snapshot (reconcile.go). The
// command-path process never dials hosted; the short-lived sync child is the
// AC2 hatch.
//
// WHY BACKOFF HERE. A push that exits non-zero (drifted or refused rows, a
// transport failure) waits an increasing backoff before it runs again, and Run
// keeps a retry timer armed for the earliest one, so a repo left dirty by a
// failure is retried when its backoff ends with no other trigger. The next local
// mutation also marks it dirty, but never cuts the backoff short. The bound is
// memory, not time: state is one map entry per partition, never per mutation.
//
// WHAT NEVER TRIGGERS A PUSH (sty_0367d20c). A hosted-origin ingest — every
// write a sync child makes, including the push's own bookkeeping and satelled's
// reseed — marks nothing, or each push would schedule the next. A push that held
// only in-flight stories exits 0 and schedules nothing: held is in flight, not a
// failure (sty_c203fd62), and the push after the story leaves flight, itself a
// local change, publishes it.
package serve

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/mirror"
)

const (
	pushDebounce    = 2 * time.Second
	pushBackoffBase = 30 * time.Second
	pushBackoffCap  = 15 * time.Minute
)

// Pusher coalesces ingest notifications into hosted workstate pushes.
// Every collaborator is injectable so tests drive it without a real binary.
type Pusher struct {
	// Debounce after the last Notify before a flush; zero means pushDebounce.
	Debounce time.Duration
	// Timeout bounds a single push; zero means reconcileTimeout.
	Timeout time.Duration
	// Now is the time source; nil means time.Now. Tests inject a fake clock
	// so backoff assertions are not sleep tests.
	Now func() time.Time
	// After is the timer source for the debounce and the retry wait; nil means
	// time.After. A test pairs it with Now to run simulated time.
	After func(d time.Duration) <-chan time.Time
	// Keys lists repo_keys to mark dirty on Run entry (catch-up). nil seeds nothing.
	Keys func(ctx context.Context) []string
	// Bound reports whether the repo at a path names its hosted project; nil
	// means config.RepoBoundAt. A bound repo gets no catch-up push: it is pushed
	// only when a local append notifies it, and that push sends only the rows
	// this machine authored (sty_0dc600e1).
	Bound func(repoPath string) bool
	// Resolve maps a repo_key to a path this home can answer for. Missing,
	// not-a-dir, or no store here → ok=false and the key is dropped.
	Resolve func(ctx context.Context, repoKey string) (repoPath string, ok bool)
	// Push delivers one repo; nil means pushViaCLI.
	Push func(ctx context.Context, repoPath string) error
	// Log receives a line when a repo STARTS failing, when its reason CHANGES,
	// and when it recovers — same discipline as Reconciler.report. nil discards.
	Log func(format string, args ...any)
	// Record persists a push-leg outcome (sty_30696eeb). Called on every flush,
	// including the ones Log suppresses. nil discards.
	Record func(repoPath string, success bool, reason string)
	// PushOff reports the machine-local push switch and names it; true makes a
	// flush drop its dirty set without pushing or recording. nil means push on.
	PushOff func() (off bool, setting string)

	// wasOff is the last PushOff answer, so the switch logs on a transition only.
	// Touched only from Run's goroutine.
	wasOff  bool
	mu      sync.Mutex
	dirty   map[string]bool
	wake    chan struct{}
	backoff map[string]*pushState
	// retryAt is when flush left a key dirty to be tried again: the end of its
	// backoff, or a debounce after a busy skip. Run's retry timer reads it. Like
	// backoff, only Run's goroutine touches it.
	retryAt map[string]time.Time
}

// pushState is one repo's backoff + failure-streak. Touched only from Run's
// goroutine after the dirty snapshot, so there is no lock on it.
type pushState struct {
	reason    string
	attempts  int
	failUntil time.Time
}

func (p *Pusher) debounce() time.Duration {
	if p.Debounce > 0 {
		return p.Debounce
	}
	return pushDebounce
}

func (p *Pusher) timeout() time.Duration {
	if p.Timeout > 0 {
		return p.Timeout
	}
	return reconcileTimeout
}

func (p *Pusher) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Pusher) after(d time.Duration) <-chan time.Time {
	if p.After != nil {
		return p.After(d)
	}
	return time.After(d)
}

func (p *Pusher) logf(format string, args ...any) {
	if p.Log != nil {
		p.Log(format, args...)
	}
}

// OnIngest is the mirror's ingest hook. A change materialised from the hosted
// project is never pushed back to it, so a hosted-origin ingest marks nothing.
func (p *Pusher) OnIngest(repoKey string, origin mirror.IngestOrigin) {
	if origin == mirror.OriginHosted {
		return
	}
	p.Notify(repoKey)
}

// Notify marks repoKey dirty and wakes the worker. Never blocks: a full wake
// channel means a flush is already pending, which is the coalescing we want.
func (p *Pusher) Notify(repoKey string) {
	repoKey = strings.TrimSpace(repoKey)
	if repoKey == "" {
		return
	}
	wake := p.markDirty(repoKey)
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (p *Pusher) markDirty(repoKey string) chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dirty == nil {
		p.dirty = map[string]bool{}
	}
	if p.wake == nil {
		p.wake = make(chan struct{}, 1)
	}
	p.dirty[repoKey] = true
	return p.wake
}

func (p *Pusher) takeDirty() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.dirty) == 0 {
		return nil
	}
	out := make([]string, 0, len(p.dirty))
	for k := range p.dirty {
		out = append(out, k)
	}
	p.dirty = map[string]bool{}
	return out
}

func (p *Pusher) dirtyCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.dirty)
}

// Run owns the worker goroutine. Honours SATELLE_SERVER_ENDPOINT=none so
// hermetic tests never spawn satelle sync. Marks every current target dirty
// on entry, but a bound repo, so a down-window of mutations rides the first
// flush.
func (p *Pusher) Run(ctx context.Context) {
	if _, disabled, set := config.ServerEndpointEnv(); set && disabled {
		p.logf("hosted push disabled by %s", config.EnvServerEndpoint)
		return
	}
	if p.Keys != nil {
		bound := p.Bound
		if bound == nil {
			bound = config.RepoBoundAt
		}
		for _, k := range p.Keys(ctx) {
			if p.Resolve != nil {
				if path, ok := p.Resolve(ctx, k); ok && bound(path) {
					continue
				}
			}
			p.Notify(k)
		}
	}
	p.mu.Lock()
	if p.wake == nil {
		p.wake = make(chan struct{}, 1)
	}
	wake := p.wake
	p.mu.Unlock()

	for {
		var retry <-chan time.Time
		if d, ok := p.nextRetry(); ok {
			retry = p.after(d)
		}
		select {
		case <-ctx.Done():
			return
		case <-retry:
		case <-wake:
			select {
			case <-ctx.Done():
				return
			case <-p.after(p.debounce()):
			}
		}
		select {
		case <-wake:
		default:
		}
		p.flush(ctx)
	}
}

// nextRetry is how long until the earliest key flush left dirty may be tried
// again. ok is false when there is none, so Run waits for Notify alone: a key a
// Notify dirtied rides the debounce, not this timer.
func (p *Pusher) nextRetry() (time.Duration, bool) {
	if len(p.retryAt) == 0 {
		return 0, false
	}
	now := p.now()
	best, ok := time.Duration(0), false
	for _, at := range p.retryAt {
		d := max(at.Sub(now), 0)
		if !ok || d < best {
			best, ok = d, true
		}
	}
	return best, ok
}

// retryLater keeps key dirty and arms Run's retry timer for it at at.
func (p *Pusher) retryLater(key string, at time.Time) {
	if p.retryAt == nil {
		p.retryAt = map[string]time.Time{}
	}
	p.retryAt[key] = at
	p.markDirty(key)
}

func (p *Pusher) flush(ctx context.Context) {
	keys := p.takeDirty()
	if p.pushOff() {
		// The dirty set is dropped, so nothing is left to retry.
		p.retryAt = nil
		return
	}
	push := p.Push
	if push == nil {
		push = pushViaCLI
	}
	now := p.now()
	var leftover []string
	for i, key := range keys {
		if ctx.Err() != nil {
			leftover = append(leftover, keys[i:]...)
			break
		}
		if st := p.backoff[key]; st != nil && now.Before(st.failUntil) {
			p.retryLater(key, st.failUntil)
			continue
		}
		delete(p.retryAt, key)
		if p.Resolve == nil {
			continue
		}
		path, ok := p.Resolve(ctx, key)
		if !ok {
			continue
		}
		runCtx, cancel := context.WithTimeout(ctx, p.timeout())
		err := push(runCtx, path)
		cancel()
		if errors.Is(err, errChildBusy) {
			// The running push may predate the change that dirtied this key.
			p.logf("sync push %s: busy next in %s", path, p.debounce())
			p.retryLater(key, p.now().Add(p.debounce()))
			continue
		}
		if heldCount(err) > 0 {
			p.report(key, path, nil)
			p.logf("sync push %s: held %d next in on change", path, heldCount(err))
			continue
		}
		p.report(key, path, err)
		if err != nil {
			until := p.backoff[key].failUntil
			p.retryLater(key, until)
			p.logf("sync push %s: error next in %s", path, until.Sub(p.now()))
			continue
		}
		p.logf("sync push %s: ok next in on change", path)
	}
	for _, k := range leftover {
		p.markDirty(k)
	}
}

// pushOff asks the machine-local switch, logging once on each transition.
func (p *Pusher) pushOff() bool {
	if p.PushOff == nil {
		return false
	}
	off, setting := p.PushOff()
	if off != p.wasOff {
		if off {
			p.logf("hosted push off by %s", setting)
		} else {
			p.logf("hosted push back on")
		}
		p.wasOff = off
	}
	return off
}

// report logs on failure-reason transition and recovered, never on a repeat.
// On failure it also arms per-repo exponential backoff. flush adds one
// unsuppressed "sync push" line per run after it, naming the outcome and when
// the repo runs next.
func (p *Pusher) report(key, path string, err error) {
	if p.Record != nil {
		reason := ""
		if err != nil {
			reason = err.Error()
		}
		p.Record(path, err == nil, reason)
	}
	if p.backoff == nil {
		p.backoff = map[string]*pushState{}
	}
	prev := p.backoff[key]
	if err == nil {
		if prev != nil {
			p.logf("hosted push %s: recovered after %d failed attempt(s)", path, prev.attempts)
			delete(p.backoff, key)
		}
		return
	}
	reason := err.Error()
	attempt := 0
	if prev != nil {
		attempt = prev.attempts
	}
	delay := backoffDelay(attempt)
	st := &pushState{
		reason:    reason,
		attempts:  attempt + 1,
		failUntil: p.now().Add(delay),
	}
	if prev == nil || prev.reason != reason {
		p.logf("hosted push %s: %v", path, err)
	}
	p.backoff[key] = st
}

func backoffDelay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if attempt > 20 {
		return pushBackoffCap
	}
	d := pushBackoffBase << attempt
	if d > pushBackoffCap || d <= 0 {
		return pushBackoffCap
	}
	return d
}

// pushViaCLI runs `satelle sync workstate push` inside repoPath, skipped as
// errChildBusy while another push runs there. The CLI no-ops when work-state
// areas are local — opt-in is its call.
func pushViaCLI(ctx context.Context, repoPath string) error {
	return gated(ctx, repoPath, kindPush, false, func(bin string) error {
		return pushWithBinary(ctx, bin, repoPath)
	})
}

// pushWithBinary is pushViaCLI with the binary named. A failure's reason is the
// push's stderr, where its refusals go; stdout carries progress, including the
// stories held until their code is on the remote, which the push records apart
// and which are no failure (sty_c203fd62). Stdout is the reason only when stderr
// says nothing. A push that exits 0 having held stories returns errHeldOnly.
func pushWithBinary(ctx context.Context, bin, repoPath string) error {
	o, err := execWithOutcome(ctx, bin, repoPath, "sync", "workstate", "push")
	if err != nil {
		return err
	}
	if o.Held > 0 {
		return errHeldOnly{n: o.Held}
	}
	return nil
}

// resolvePushPath maps repo_key → path with the same guards as mirrorTargets:
// directory must exist and this home must have a store, or we skip (wipe-risk).
func resolvePushPath(ms *mirror.Store, globalDir string) func(context.Context, string) (string, bool) {
	return func(ctx context.Context, repoKey string) (string, bool) {
		details, err := ms.ListPartitionDetails(ctx)
		if err != nil {
			return "", false
		}
		for _, d := range details {
			if d.RepoKey != repoKey {
				continue
			}
			path := strings.TrimSpace(d.Path)
			if path == "" {
				return "", false
			}
			if st, err := os.Stat(path); err != nil || !st.IsDir() {
				return "", false
			}
			if !storeExists(globalDir, path) {
				return "", false
			}
			return path, true
		}
		return "", false
	}
}

// pushKeys returns every partition repo_key the mirror currently knows.
// Resolve filters the ones this home cannot answer for.
func pushKeys(ms *mirror.Store) func(context.Context) []string {
	return func(ctx context.Context) []string {
		details, err := ms.ListPartitionDetails(ctx)
		if err != nil {
			return nil
		}
		var out []string
		for _, d := range details {
			if k := strings.TrimSpace(d.RepoKey); k != "" {
				out = append(out, k)
			}
		}
		return out
	}
}
