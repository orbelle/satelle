package cli

import (
	"context"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/bobmcallan/satelle/internal/cache"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/syncstate"
	"github.com/bobmcallan/satelle/internal/uipush"
	"github.com/bobmcallan/satelle/internal/workflow"
)

var written struct {
	sync.Mutex
	repo  *repo
	items map[string]bool
}

func watchWrites(k *kernel.Kernel, r repo) {
	k.Ledger.OnWrite = func(e ledger.Event) {
		written.Lock()
		defer written.Unlock()
		if written.items == nil {
			written.items = map[string]bool{}
		}
		written.repo = &r
		written.items[e.Item] = true
	}
}

func flushUI(ctx context.Context) {
	r, ids := takeWritten()
	if r == nil || len(ids) == 0 {
		return
	}
	_ = withUISource(*r, func(c uipush.Client, src uipush.Source) error {
		return c.Push(ctx, src, ids)
	})
}

func takeWritten() (*repo, []string) {
	written.Lock()
	r, items := written.repo, written.items
	written.repo, written.items = nil, nil
	written.Unlock()
	ids := make([]string, 0, len(items))
	for id := range items {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return r, ids
}

func confirmUI(ctx context.Context, r repo) {
	_, _ = takeWritten()
	_ = withUISource(r, func(c uipush.Client, src uipush.Source) error {
		return c.Confirm(ctx, src)
	})
}

func uiClient() (uipush.Client, bool) {
	gc, _ := config.LoadGlobal()
	ep := gc.Service.ResolveEndpoint()
	if ep == "" {
		return uipush.Client{}, false
	}
	return uipush.Client{Endpoint: ep, InstanceID: config.SafeCurrentInstanceID()}, true
}

func uiRepoRoot(r repo) string { return config.CanonicalRepoRoot(filepath.Dir(r.DataDir)) }

func recordSnapshot(root string, full bool, err error) {
	if err == nil {
		if full {
			_ = syncstate.RecordSnapshot(config.GlobalDir(), root, true, "", time.Now())
		}
		return
	}
	label := "push"
	if full {
		label = "seed"
	}
	_ = syncstate.RecordSnapshot(config.GlobalDir(), root, false, label+": "+err.Error(), time.Now())
}

func withUISource(r repo, fn func(uipush.Client, uipush.Source) error) error {
	c, ok := uiClient()
	if !ok {
		return nil
	}
	root := uiRepoRoot(r)
	c.Report = func(full bool, err error) { recordSnapshot(root, full, err) }
	led, ch, err := openRecord(r)
	if err != nil {
		recordSnapshot(root, true, err)
		return err
	}
	defer ch.Close()
	wfs, _ := workflow.Load(filepath.Join(r.DataDir, kernel.WorkflowsDir))
	cfg, _ := kernel.LoadConfig(r.DataDir)
	return fn(c, uipush.Source{
		Ledger: led, Cache: ch, Workflows: wfs, DataDir: r.DataDir,
		RepoRoot: root, RepoKey: config.RepoKey(root), Slug: config.RepoName(root),
		User: signedInUser(), Queues: kernel.QueueIndex(cfg.Settings, cfg.Queues),
	})
}

func openRecord(r repo) (*ledger.Store, *cache.Cache, error) {
	led, err := ledger.Open(r.Runtime)
	if err != nil {
		return nil, nil, err
	}
	ch, err := cache.Open(filepath.Join(r.Runtime, "cache.db"))
	if err != nil {
		return nil, nil, err
	}
	if err := ch.Sync(led); err != nil {
		ch.Close()
		return nil, nil, err
	}
	return led, ch, nil
}

func signedInUser() *mirror.User {
	gc, err := config.LoadGlobal()
	if err != nil {
		return nil
	}
	c, err := hosted.FileStore{}.Load(hosted.ConfiguredServer(gc))
	if err != nil || c.DisplayName+c.Email == "" {
		return nil
	}
	return &mirror.User{Name: c.DisplayName, Email: c.Email}
}
