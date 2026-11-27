package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/ledgerstore"
	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/wire/chainpb"
)

const (
	followPoll     = 500 * time.Millisecond
	followSettle   = 200 * time.Millisecond
	followRetryMin = 250 * time.Millisecond
	followRetryMax = 2 * time.Second
)

func ledgerFollowCmd() *cobra.Command {
	return &cobra.Command{
		Use:         "follow",
		Short:       "Push this machine's writes and pull other locations' changes within seconds (run by satelle-follow.service)",
		Hidden:      true,
		Annotations: map[string]string{ownSubstrateAnnotation: "true"},
		Long: `Run in the foreground until signalled. For each of this machine's projects
(~/.satelle/config.toml [projects], reloaded every 30 seconds) whose ledger
store is satelle.dev and which is bound to a project, keep a subscription to
the store's Chain.Subscribe open and watch the local ledger for writes. A
local write, a streamed event this location does not already hold, and each
(re)connect of the subscription run one sync pass: the same push, then pull,
that satelle ledger autosync runs, recorded and confirmed to satelled the same
way. Streamed events are never applied directly. A dropped subscription is
re-dialled with a backoff of at most 2 seconds; a server that does not serve
Chain.Subscribe is retried every 30 seconds while local writes still push.
Seat releases and queue wakes stay with satelle-sync.timer, which also stays
the fallback: its pass pushes and pulls whatever this process missed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return followProjects(cmd.Context(), cmd.OutOrStdout())
		},
	}
}

type followOut struct {
	mu sync.Mutex
	w  io.Writer
}

func (o *followOut) printf(format string, args ...any) {
	o.mu.Lock()
	defer o.mu.Unlock()
	fmt.Fprintf(o.w, format+"\n", args...)
}

type followWatch struct {
	target binding
	cancel context.CancelFunc
	done   chan struct{}
}

func followProjects(ctx context.Context, w io.Writer) error {
	out := &followOut{w: w}
	watches := map[string]*followWatch{}
	stopAll := func() {
		for path, fw := range watches {
			fw.cancel()
			<-fw.done
			delete(watches, path)
		}
	}
	defer stopAll()
	tick := time.NewTicker(mirror.SyncPeriod)
	defer tick.Stop()
	for {
		reconcileFollows(ctx, out, watches)
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

func reconcileFollows(ctx context.Context, out *followOut, watches map[string]*followWatch) {
	want := map[string]followRepo{}
	if gc, err := config.LoadGlobal(); err == nil {
		for _, path := range gc.Projects.Paths {
			if fr, ok := followTargetAt(path); ok {
				want[path] = fr
			}
		}
	} else {
		out.printf("projects not loaded: %v", err)
	}
	for path, fw := range watches {
		if fr, ok := want[path]; ok && fr.b == fw.target {
			continue
		}
		fw.cancel()
		<-fw.done
		delete(watches, path)
	}
	for path, fr := range want {
		if _, ok := watches[path]; ok {
			continue
		}
		wctx, cancel := context.WithCancel(ctx)
		fw := &followWatch{target: fr.b, cancel: cancel, done: make(chan struct{})}
		watches[path] = fw
		go func() {
			defer close(fw.done)
			followRepoLoop(wctx, out, path, fr)
		}()
	}
}

type followRepo struct {
	r repo
	b binding
}

func followTargetAt(path string) (followRepo, bool) {
	if st, err := os.Stat(path); err != nil || !st.IsDir() {
		return followRepo{}, false
	}
	r, err := findOwnRepo(path)
	if err != nil || len(kernel.LegacyMarkers(r.DataDir)) > 0 {
		return followRepo{}, false
	}
	b, ok := followTarget(r)
	return followRepo{r: r, b: b}, ok
}

func followTarget(r repo) (binding, bool) {
	k, err := r.open()
	if err != nil {
		return binding{}, false
	}
	spec, _ := resolveStore(k)
	k.Close()
	if spec.Kind != ledgerstore.KindSatelleDev || spec.External() {
		return binding{}, false
	}
	b, ok, err := loadBinding(r)
	if err != nil || !ok {
		return binding{}, false
	}
	return b, true
}

type follower struct {
	out     *followOut
	path    string
	r       repo
	b       binding
	trigger chan struct{}
	mu      sync.Mutex
	base    map[string]ledger.Mark
}

func followRepoLoop(ctx context.Context, out *followOut, path string, fr followRepo) {
	f := &follower{out: out, path: path, r: fr.r, b: fr.b, trigger: make(chan struct{}, 1)}
	f.base = f.marks()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); f.subscribeLoop(ctx) }()
	go func() { defer wg.Done(); f.pollLocal(ctx) }()
	f.passLoop(ctx)
	wg.Wait()
}

func (f *follower) fire() {
	select {
	case f.trigger <- struct{}{}:
	default:
	}
}

func (f *follower) marks() map[string]ledger.Mark {
	s, err := ledger.OpenExisting(f.r.Runtime)
	if err != nil {
		return nil
	}
	m, err := s.Marks()
	if err != nil {
		return nil
	}
	return m
}

func (f *follower) pollLocal(ctx context.Context) {
	tick := time.NewTicker(followPoll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		now := f.marks()
		f.mu.Lock()
		changed := now != nil && !maps.Equal(now, f.base)
		f.mu.Unlock()
		if changed {
			f.fire()
		}
	}
}

func (f *follower) passLoop(ctx context.Context) {
	noSeatFix := func(*kernel.Kernel, string, func(string) bool) []string { return nil }
	for {
		select {
		case <-ctx.Done():
			return
		case <-f.trigger:
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(followSettle):
		}
		select {
		case <-f.trigger:
		default:
		}
		now := f.marks()
		f.mu.Lock()
		f.base = now
		f.mu.Unlock()
		pass, summary := syncPass(ctx, f.r, noSeatFix, nil)
		switch {
		case ctx.Err() != nil:
			return
		case pass.Reason != "":
			f.out.printf("%s: sync failing: %s", f.path, pass.Reason)
		case pass.Pushed > 0 || pass.Pulled > 0:
			f.out.printf("%s: %s", f.path, summary)
		}
	}
}

func (f *follower) subscribeLoop(ctx context.Context) {
	delay := followRetryMin
	unimplemented := false
	for {
		opened, err := f.subscribeOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		wait := delay
		switch {
		case status.Code(err) == codes.Unimplemented:
			if !unimplemented {
				f.out.printf("%s: %s does not serve Chain.Subscribe: local writes still push; pulls wait for satelle-sync.timer (retrying every %s)", f.path, f.b.Server, mirror.SyncPeriod)
			}
			unimplemented = true
			wait = mirror.SyncPeriod
		default:
			unimplemented = false
			if opened {
				delay = followRetryMin
				wait = delay
			}
			f.out.printf("%s: subscription dropped: %v; retrying in %s", f.path, err, wait)
			delay = min(delay*2, followRetryMax)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func (f *follower) subscribeOnce(ctx context.Context) (bool, error) {
	c, err := dial(f.b.Server)
	if err != nil {
		return false, err
	}
	defer c.Close()
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := c.Chain.Subscribe(sctx, &chainpb.SubscribeRequest{Project: f.b.Project})
	if err != nil {
		return false, err
	}
	f.out.printf("%s: subscribed to satelle.dev project %s at %s", f.path, f.b.Project, f.b.Server)
	f.fire()
	for {
		resp, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = errors.New("stream closed by the server")
			}
			return true, err
		}
		if !f.holds(resp.GetEvent()) {
			f.fire()
		}
	}
}

func (f *follower) holds(e *chainpb.Event) bool {
	if e == nil {
		return true
	}
	s, err := ledger.OpenExisting(f.r.Runtime)
	if err != nil {
		return false
	}
	head, err := s.Head(e.GetItemId())
	return err == nil && head != "" && head == e.GetHash()
}
