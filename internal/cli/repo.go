package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/gitfacts"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/remote/provider"
	"github.com/bobmcallan/satelle/internal/remotenode"
	"github.com/bobmcallan/satelle/internal/seatlife"
)

// repo is where a command runs: the .satelle directory, the working tree and
// the home-keyed runtime directory holding the ledger and cache.
type repo struct {
	DataDir string // <tree>/.satelle
	Tree    string // the working tree (git top level, else DataDir's parent)
	Runtime string
}

var errNotInitialised = errors.New("no .satelle directory here or above; run `satelle init`")

// findRepo locates the repo from dir ("" = the working directory).
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

// open opens the kernel for the repo at the working directory. A repo with an
// invalid configuration refuses to open.
func openKernel() (*kernel.Kernel, repo, error) {
	r, err := findRepo("")
	if err != nil {
		return nil, r, err
	}
	k, err := r.open()
	return k, r, err
}

func (r repo) open() (*kernel.Kernel, error) {
	if err := os.MkdirAll(r.Runtime, 0o755); err != nil {
		return nil, err
	}
	_ = config.WriteRepoPathMarker(r.Runtime, filepath.Dir(r.DataDir))
	k, err := kernel.Open(kernel.Options{Root: r.DataDir, Runtime: r.Runtime, Dir: r.Tree})
	if err != nil {
		return nil, fmt.Errorf("%w\nrun `satelle validate` for every problem", err)
	}
	// A check gate placed on a docker-local remote runs on a node started
	// here; one on a fly-sprite remote on a node satelle-server starts for the
	// bound project, dialled only when such a check runs.
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
	// Every seat records this machine (an error leaves it unrecorded, so the
	// seat is unverified, never wrongly here), and a dispatched run records
	// its process as its session's owner.
	home := config.GlobalDir()
	k.Machine, _ = config.MachineID(home)
	k.Host, _ = os.Hostname()
	// The seat's user is read only when a seat binds, never on the gate
	// hook's path, which opens the kernel on every tool call.
	k.User = func() string { return seatUser(k.Host) }
	k.Sessions = seatlife.Recorder{Home: home}
	k.RemoteChecks = remotenode.GateChecker{Tree: r.Tree, DataDir: r.DataDir, Docker: provider.DockerLocal{}, Sprites: sprites, Out: os.Stderr}
	watchWrites(k, r)
	return k, nil
}

// canonical resolves symlinks so locations compare equal however a path was
// reached.
func canonical(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return filepath.Clean(p)
}

// sessionFromEnv is SATELLE_SESSION.
func sessionFromEnv() string { return config.SessionFromEnv() }

// seatUser is the satelle user a seat records: the signed-in identity (the
// login's cached email, else its display name), else <OS user>@<host>, or ""
// when there is no OS user.
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
