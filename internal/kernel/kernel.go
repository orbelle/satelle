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
	"github.com/bobmcallan/satelle/internal/gitfacts"
	"github.com/bobmcallan/satelle/internal/kledger"
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
	Root       string // the .satelle directory
	Dir        string // the working tree checks and agents run in
	Settings   Settings
	Ledger     *kledger.Store
	Cache      *cache.Cache
	Workflows  map[string]*workflow.Workflow
	Rules      workflow.Rules
	Skills     map[string]workflow.Skill
	Agents     agent.Catalog
	Git        Git // nil outside a git work tree
	Dispatcher agent.Dispatcher
	Now        func() time.Time
	// CheckTimeout bounds each check script; zero is gate.DefaultCheckTimeout.
	CheckTimeout time.Duration
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
	led, err := kledger.Open(o.Runtime)
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
		Dispatcher: agent.Runner{}, Now: time.Now,
	}
	repo := gitfacts.Repo{Dir: o.Dir}
	if _, err := repo.TopLevel(); err == nil {
		k.Git = repo
	}
	led.Now = func() time.Time { return k.Now() }
	return k, nil
}

// Close closes the cache.
func (k *Kernel) Close() error { return k.Cache.Close() }

// Validate returns every problem with the kernel's configuration.
func (k *Kernel) Validate() []error {
	c := &Config{Root: k.Root, Settings: k.Settings, Workflows: k.Workflows, Skills: k.Skills, Agents: k.Agents}
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
