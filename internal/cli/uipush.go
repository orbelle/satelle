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

// written records the items a command wrote to the ledger, so the command's
// end can tell satelled (flushUI). Every kernel mutation and a ledger pull
// write through the ledger, so watching it covers them all.
var written struct {
	sync.Mutex
	repo  *repo
	items map[string]bool
}

// watchWrites records k's ledger writes for flushUI.
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

// flushUI pushes the items the command wrote to satelled. It is silent: a
// satelled that is not running, or serves another home, changes nothing.
func flushUI(ctx context.Context) {
	written.Lock()
	r, items := written.repo, written.items
	written.repo, written.items = nil, nil
	written.Unlock()
	if r == nil || len(items) == 0 {
		return
	}
	ids := make([]string, 0, len(items))
	for id := range items {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	_ = withUISource(*r, func(c uipush.Client, src uipush.Source) error {
		return c.Push(ctx, src, ids)
	})
}

// uiClient is the client for this home's satelled; ok is false when the push
// is disabled.
func uiClient() (uipush.Client, bool) {
	gc, _ := config.LoadGlobal()
	ep := gc.Service.ResolveEndpoint()
	if ep == "" {
		return uipush.Client{}, false
	}
	return uipush.Client{Endpoint: ep, InstanceID: config.SafeCurrentInstanceID()}, true
}

// uiRepoRoot is r's canonical root: the repo path satelled's partition and
// the per-repo sync state (syncstate) are keyed on.
func uiRepoRoot(r repo) string { return config.CanonicalRepoRoot(filepath.Dir(r.DataDir)) }

// recordSnapshot records one snapshot outcome for root in the per-repo sync
// state, which `satelle status` reads. A full snapshot that lands clears a
// recorded failure; a delta that lands records nothing, since it does not
// prove the partition whole. Best effort, like the push itself.
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

// withUISource opens r's ledger and cache read-only for a push to satelled.
// It does nothing when the push is disabled. Each snapshot satelled refuses,
// and a source that cannot be built, is recorded for `satelle status`.
func withUISource(r repo, fn func(uipush.Client, uipush.Source) error) error {
	c, ok := uiClient()
	if !ok {
		return nil
	}
	root := uiRepoRoot(r)
	c.Report = func(full bool, err error) { recordSnapshot(root, full, err) }
	led, err := ledger.Open(r.Runtime)
	if err != nil {
		recordSnapshot(root, true, err)
		return err
	}
	ch, err := cache.Open(filepath.Join(r.Runtime, "cache.db"))
	if err != nil {
		recordSnapshot(root, true, err)
		return err
	}
	defer ch.Close()
	if err := ch.Sync(led); err != nil {
		recordSnapshot(root, true, err)
		return err
	}
	wfs, _ := workflow.Load(filepath.Join(r.DataDir, kernel.WorkflowsDir))
	cfg, _ := kernel.LoadConfig(r.DataDir)
	return fn(c, uipush.Source{
		Ledger: led, Cache: ch, Workflows: wfs, DataDir: r.DataDir,
		RepoRoot: root, RepoKey: config.RepoKey(root), Slug: config.RepoName(root),
		User: signedInUser(), Queues: kernel.QueueIndex(cfg.Settings, cfg.Queues),
	})
}

// signedInUser is who this machine's CLI is signed in as: the name and email
// stamped into the local credential store at login (no network call, never a
// token), or nil when it holds no login for the configured server.
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
