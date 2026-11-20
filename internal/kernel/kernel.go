package kernel

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/cache"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/gitfacts"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/workflow"
)

type Git interface {
	Head() (string, error)
	ChangedPaths(base string) ([]string, error)
	Untracked() ([]string, error)
	Diff(base string, paths []string) (string, error)
	StatPaths(base string) ([]gitfacts.FileStat, error)
}

type Kernel struct {
	Root          string
	Dir           string
	Settings      Settings
	Ledger        *ledger.Store
	Cache         *cache.Cache
	Workflows     map[string]*workflow.Workflow
	Rules         workflow.Rules
	Skills        map[string]workflow.Skill
	Agents        agent.Catalog
	Overrides     []agent.Override
	Queues        map[string]*workflow.Queue
	queueFiles    map[string]*workflow.Queue
	Harness       map[string]HarnessSettings
	Git           Git
	Dispatcher    agent.Dispatcher
	Remote        RemotePerformer
	RemoteChecks  gate.RemoteChecker
	Cloud         CloudLauncher
	Now           func() time.Time
	CheckTimeout  time.Duration
	Logs          string
	Machine, Host string
	User          func() string
	Sessions      SessionOwner
	SessionMeta   func(session string) (harness, model string)
	Progress      func(item string)
}

type SessionOwner interface {
	Own(session string) error
	Forget(session string) error
}

type Options struct {
	Root     string
	Runtime  string
	Dir      string
	ReadOnly string
}

func Open(o Options) (*Kernel, error) {
	cfg, err := LoadConfig(o.Root)
	if err != nil {
		return nil, err
	}
	if problems := cfg.Problems(); len(problems) > 0 {
		return nil, fmt.Errorf("configuration in %s is invalid:\n%w", o.Root, errors.Join(problems...))
	}
	if o.Runtime == "" {
		o.Runtime = filepath.Join(o.Root, "runtime")
	}
	if o.Dir == "" {
		o.Dir = filepath.Dir(filepath.Clean(o.Root))
	}
	led, err := ledger.Open(o.Runtime)
	if err != nil {
		return nil, err
	}
	c, err := cache.Open(filepath.Join(o.Runtime, "cache.db"))
	if err != nil {
		return nil, err
	}
	if err := c.Sync(led); err != nil {
		c.Close()
		return nil, err
	}
	k := &Kernel{
		Root: o.Root, Dir: o.Dir, Settings: cfg.Settings, Ledger: led, Cache: c,
		Workflows: cfg.Workflows, Rules: cfg.Settings.Workflows, Skills: cfg.Skills, Agents: cfg.Agents, Overrides: cfg.Overrides,
		Queues: cfg.Queues, queueFiles: QueueIndex(cfg.Settings, cfg.Queues), Harness: cfg.Harness,
		Dispatcher: agent.Runner{}, Now: time.Now, Logs: config.LogsDir(o.Runtime),
	}
	repo := gitfacts.Repo{Dir: o.Dir}
	if _, err := repo.TopLevel(); err == nil {
		k.Git = repo
	}
	led.Now = func() time.Time { return k.Now() }
	if o.ReadOnly != "" {
		led.SetReadOnly(o.ReadOnly)
	} else if err := k.materializeQueues(); err != nil {
		c.Close()
		return nil, err
	}
	return k, nil
}

func (k *Kernel) Close() error { return k.Cache.Close() }

func (k *Kernel) Validate() []error {
	c := &Config{Root: k.Root, Settings: k.Settings, Workflows: k.Workflows, Skills: k.Skills, Agents: k.Agents, Overrides: k.Overrides, Queues: k.Queues, Harness: k.Harness}
	c.Settings.Workflows = k.Rules
	return c.Problems()
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
