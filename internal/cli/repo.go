package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/gitfacts"
	"github.com/bobmcallan/satelle/internal/kernel"
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

// localLocation is the seat location of a working tree.
func localLocation(tree string) string { return "local:" + canonical(tree) }

// sessionFromEnv is SATELLE_SESSION.
func sessionFromEnv() string { return config.SessionFromEnv() }
