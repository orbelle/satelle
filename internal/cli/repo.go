package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/gitfacts"
	"github.com/bobmcallan/satelle/internal/hook"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/remote/provider"
	"github.com/bobmcallan/satelle/internal/remotenode"
	"github.com/bobmcallan/satelle/internal/seatlife"
)

type repo struct {
	DataDir string
	Tree    string
	Runtime string
}

var errNotInitialised = errors.New("no .satelle directory here or above; run `satelle init`")

func findRepo(dir string) (repo, error) {
	dataDir, ok := config.FindDataDir(dir)
	if !ok {
		return repo{}, errNotInitialised
	}
	tree := canonical(filepath.Dir(dataDir))
	if top, err := (gitfacts.Repo{Dir: tree}).TopLevel(); err == nil && top != "" {
		tree = canonical(top)
	}
	return repo{DataDir: dataDir, Tree: tree, Runtime: config.RuntimeDir(filepath.Dir(dataDir))}, nil
}

func openKernel() (*kernel.Kernel, repo, error) {
	r, err := findRepo("")
	if err != nil {
		return nil, r, err
	}
	k, err := r.open()
	return k, r, err
}

const dryRunReadOnly = "a dry run (satelle gate run) started this command: it may read the ledger, not write it"

func (r repo) open() (*kernel.Kernel, error) {
	readOnly := ""
	if kernel.UnderDryRun(r.Runtime) {
		readOnly = dryRunReadOnly
	}
	return r.openWith(readOnly)
}

func (r repo) openWith(readOnly string) (*kernel.Kernel, error) {
	if err := os.MkdirAll(r.Runtime, 0o755); err != nil {
		return nil, err
	}
	_ = config.WriteRepoPathMarker(r.Runtime, filepath.Dir(r.DataDir))
	k, err := kernel.Open(kernel.Options{Root: r.DataDir, Runtime: r.Runtime, Dir: r.Tree, ReadOnly: readOnly})
	if err != nil {
		return nil, fmt.Errorf("%w\nrun `satelle validate` for every problem", err)
	}
	sprites := remotenode.ServerSprites{Placer: func(context.Context) (remotenode.NodePlacer, func(), error) {
		p, closeFn, ok, err := hostedPlacer(k, r)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			return nil, nil, errNotBound
		}
		return p, closeFn, nil
	}}
	home := config.GlobalDir()
	k.Machine, _ = config.MachineID(home)
	k.Host, _ = os.Hostname()
	k.User = func() string { return seatUser(k.Host) }
	k.Sessions = seatlife.Recorder{Home: home}
	k.SessionMeta = func(session string) (string, string) { return sessionMeta(home, session) }
	k.RemoteChecks = remotenode.GateChecker{Tree: r.Tree, DataDir: r.DataDir, Docker: provider.DockerLocal{Egress: firingBinary()}, Sprites: sprites, Out: os.Stderr}
	watchWrites(k, r)
	k.Progress = func(id string) { pushItem(r, id) }
	return k, nil
}

func canonical(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return filepath.Clean(p)
}

func sessionFromEnv() string { return config.SessionFromEnv() }

func sessionMeta(home, session string) (harness, model string) {
	if rec, ok := seatlife.Load(home, session); ok {
		harness, model = rec.Harness, rec.Model
	}
	if harness == "" {
		harness = hook.Caller()
	}
	return harness, model
}

func seatUser(host string) string {
	if u := signedInUser(); u != nil {
		if u.Email != "" {
			return u.Email
		}
		return u.Name
	}
	u := osUser()
	if u == "" {
		return ""
	}
	return u + "@" + host
}
