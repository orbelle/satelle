// Package kernel is satelle's operations over work items: create, update,
// transition (through gates), attach, seat, release, perform and task run.
// Every operation appends to the ledger and refreshes the cache; the
// configuration under .satelle/ supplies every name.
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

// Git is what the kernel asks of the working tree. gitfacts.Repo is the real
// one.
type Git interface {
	Head() (string, error)
	ChangedPaths(base string) ([]string, error)
	Diff(base string, paths []string) (string, error)
}

// Kernel is an opened repo: its configuration, ledger and cache.
type Kernel struct {
	Root      string // the .satelle directory
	Dir       string // the working tree checks and agents run in
	Settings  Settings
	Ledger    *ledger.Store
	Cache     *cache.Cache
	Workflows map[string]*workflow.Workflow
	Rules     workflow.Rules
	Skills    map[string]workflow.Skill
	Agents    agent.Catalog
	// Queues are the queue files by name; queueFiles binds chain ids to them.
	Queues     map[string]*workflow.Queue
	queueFiles map[string]*workflow.Queue
	Git        Git // nil outside a git work tree
	Dispatcher agent.Dispatcher
	// Remote performs steps placed remote:<id>; nil refuses them.
	Remote RemotePerformer
	// RemoteChecks runs check gates placed remote:<id>; nil refuses them.
	RemoteChecks gate.RemoteChecker
	// Cloud starts steps placed in the cloud; nil says how to start one by hand.
	Cloud CloudLauncher
	Now   func() time.Time
	// CheckTimeout bounds each check script; zero is gate.DefaultCheckTimeout.
	CheckTimeout time.Duration
	// Logs is the runtime logs dir (<Runtime>/logs, what satelle logs reads):
	// where a judge's dispatch log is written. "" writes none.
	Logs string
	// Machine and Host are stamped on every seat this kernel binds: this
	// machine's id (config.MachineID) and host name. "" records none, and the
	// seat is then unverified wherever it is read.
	Machine, Host string
	// User is asked once per seat this kernel binds, for the satelle user who
	// bound it (item.Seat.User); nil records none.
	User func() string
	// Sessions records which process owns a dispatched run's session; nil
	// records nothing.
	Sessions SessionOwner
}

// SessionOwner records the process a session runs in, on this machine only
// (internal/seatlife): a dispatched run owns its session while it runs.
type SessionOwner interface {
	// Own records the calling process as session's owner.
	Own(session string) error
	// Forget removes session's record.
	Forget(session string) error
}

// Options say where a kernel lives.
type Options struct {
	Root    string // the .satelle directory
	Runtime string // ledger, blobs and cache; "" is <Root>/runtime
	Dir     string // working tree; "" is Root's parent
}

// Open loads and validates the configuration and opens the ledger and cache.
// A configuration with any problem refuses to open.
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
		Workflows: cfg.Workflows, Rules: cfg.Settings.Workflows, Skills: cfg.Skills, Agents: cfg.Agents,
		Queues: cfg.Queues, queueFiles: QueueIndex(cfg.Settings, cfg.Queues),
		Dispatcher: agent.Runner{}, Now: time.Now, Logs: config.LogsDir(o.Runtime),
	}
	repo := gitfacts.Repo{Dir: o.Dir}
	if _, err := repo.TopLevel(); err == nil {
		k.Git = repo
	}
	led.Now = func() time.Time { return k.Now() }
	if err := k.materializeQueues(); err != nil {
		c.Close()
		return nil, err
	}
	return k, nil
}

// Close closes the cache.
func (k *Kernel) Close() error { return k.Cache.Close() }

// Validate returns every problem with the kernel's configuration.
func (k *Kernel) Validate() []error {
	c := &Config{Root: k.Root, Settings: k.Settings, Workflows: k.Workflows, Skills: k.Skills, Agents: k.Agents, Queues: k.Queues}
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
