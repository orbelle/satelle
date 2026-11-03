package serve

// One sync child per repo and kind (sty_0367d20c).
//
// satelled execs its own CLI for every sync leg: push, snapshot, follow, reseed,
// substrate and a story page's fetch. The Pusher and the Reconciler are each
// serial, but nothing stopped a fetch overlapping a fetch, or a child of one
// loop running while the same verb from another was still going. childGate is
// the single owner of that rule: every *ViaCLI helper runs its exec through
// gated, so no helper can start a child outside it.
//
// A background caller is skipped while its key is busy (errChildBusy): the next
// pass or push covers it. A story page's fetch waits for the running one, so the
// page still renders the history it fetched (web mirror_server.go).

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// errChildBusy is a background sync leg skipped because a child of the same
// kind is already running for the repo. It is not a failure.
var errChildBusy = errors.New("a sync child of this kind is already running for the repo")

// Child kinds, one gate key each per repo.
const (
	kindPush      = "push"
	kindSnapshot  = "snapshot"
	kindFollow    = "follow"
	kindReseed    = "reseed"
	kindSubstrate = "substrate"
	kindFetch     = "fetch"
	kindRechain   = "rechain"
)

type childKey struct{ repo, kind string }

// childGate admits at most one child per (repo, kind).
type childGate struct {
	mu   sync.Mutex
	busy map[childKey]chan struct{}
}

// children is satelled's one gate.
var children = &childGate{}

// acquire takes key, returning its release, or the channel that closes when the
// current holder releases.
func (g *childGate) acquire(k childKey) (release func(), wait <-chan struct{}) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if ch, ok := g.busy[k]; ok {
		return nil, ch
	}
	if g.busy == nil {
		g.busy = map[childKey]chan struct{}{}
	}
	ch := make(chan struct{})
	g.busy[k] = ch
	return func() {
		g.mu.Lock()
		delete(g.busy, k)
		g.mu.Unlock()
		close(ch)
	}, nil
}

// tryRun runs fn unless a child of kind is running for repo, in which case it
// returns errChildBusy without running it.
func (g *childGate) tryRun(repo, kind string, fn func() error) error {
	release, _ := g.acquire(childKey{repo, kind})
	if release == nil {
		return errChildBusy
	}
	defer release()
	return fn()
}

// run waits for any running child of kind for repo to end, then runs fn. It
// gives up when ctx ends.
func (g *childGate) run(ctx context.Context, repo, kind string, fn func() error) error {
	k := childKey{repo, kind}
	for {
		release, wait := g.acquire(k)
		if release != nil {
			defer release()
			return fn()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wait:
		}
	}
}

// resolveSatelle names the CLI binary every child runs; tests point it at a fake.
var resolveSatelle = satelleBinary

// gated resolves the CLI and runs fn under the gate for (repoPath, kind). wait
// is the story-page fetch's choice; every background leg passes false.
func gated(ctx context.Context, repoPath, kind string, wait bool, fn func(bin string) error) error {
	run := func() error {
		bin, err := resolveSatelle()
		if err != nil {
			return err
		}
		return fn(bin)
	}
	if wait {
		return children.run(ctx, repoPath, kind, run)
	}
	return children.tryRun(repoPath, kind, run)
}

// execVerb runs one CLI verb in dir with extra env appended, returning its
// stdout, and an error whose reason is its stderr — or its stdout when stderr
// says nothing.
func execVerb(ctx context.Context, bin, dir string, env []string, args ...string) ([]byte, error) {
	return execNamed(ctx, bin, dir, env, args, args)
}

// execNamed is execVerb with the argv the error names apart from the one run,
// so a flag satelled adds for itself stays out of the operator's log.
func execNamed(ctx context.Context, bin, dir string, env, name, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		reason := strings.TrimSpace(stderr.String())
		if reason == "" {
			reason = strings.TrimSpace(stdout.String())
		}
		return stdout.Bytes(), fmt.Errorf("%s %s: %w: %s", bin, strings.Join(name, " "), err, reason)
	}
	return stdout.Bytes(), nil
}

// execWithOutcome runs a verb with outcomeFlag and returns its parsed outcome.
// An older binary that refuses the flag is run again without it, and that run
// reads as clean — the behaviour before outcomes existed.
func execWithOutcome(ctx context.Context, bin, dir string, args ...string) (syncOutcome, error) {
	out, err := execNamed(ctx, bin, dir, nil, args, append(append([]string(nil), args...), outcomeFlag))
	if err != nil && strings.Contains(err.Error(), "unknown flag: "+outcomeFlag) {
		_, err = execVerb(ctx, bin, dir, nil, args...)
		return syncOutcome{}, err
	}
	if err != nil {
		return syncOutcome{}, err
	}
	o, _ := parseOutcome(out)
	return o, nil
}
