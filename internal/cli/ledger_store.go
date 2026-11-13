package cli

import (
	"fmt"
	"io"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/ledgerrepl"
	"github.com/bobmcallan/satelle/internal/ledgerstore"
)

// localStoreLine is what push, pull and status say for the local store, which
// makes no store or network call.
const localStoreLine = `ledger store is local: nothing replicates (set [ledger] store to "satelle.dev" or a store command to replicate)`

// resolveStore is the store [ledger] store names; unset, satelle.dev while
// signed in and local otherwise. Only push, pull and status call it: the sign-
// in check reads local files only, and only when no store is set.
func resolveStore(k *kernel.Kernel) (spec ledgerstore.Spec, defaulted bool) {
	spec = k.Settings.Ledger.Store
	if spec.Set() {
		return spec, false
	}
	signedIn := false
	if gc, err := config.LoadGlobal(); err == nil {
		signedIn = hosted.SignedIn(hosted.ConfiguredServer(gc))
	}
	return ledgerstore.Resolve(spec, signedIn), true
}

// openStore opens the store push, pull and status replicate with. For the
// local store it returns a nil Store and has made no call. satelle.dev needs
// the repo bound to a project and dials its server; a store command runs per
// operation. The returned func releases the store.
func openStore(k *kernel.Kernel, r repo, verb string) (ledgerrepl.Store, func(), error) {
	spec, defaulted := resolveStore(k)
	switch {
	case spec.External():
		c, err := storeCommand(k, spec, verb)
		if err != nil {
			return nil, func() {}, err
		}
		return &ledgerrepl.ObjectStore{Objects: c, Name: c.Path}, func() {}, nil
	case spec.Kind == ledgerstore.KindSatelleDev:
		b, ok, err := loadBinding(r)
		if err != nil {
			return nil, func() {}, err
		}
		if !ok {
			why := "ledger store is satelle.dev"
			if defaulted {
				why += " (signed in, no [ledger] store set)"
			}
			return nil, func() {}, &exitError{code: ExitError, msg: why + ` but this repo is not bound to a project; run satelle project bind <project>, or set [ledger] store = "local"`}
		}
		c, err := dial(b.Server)
		if err != nil {
			return nil, func() {}, err
		}
		d := devFor(k, c, b.Project)
		d.Server = b.Server
		return d, func() { c.Close() }, nil
	}
	return nil, func() {}, nil
}

// devFor is satelle.dev for project over c. It reads no setting; its Home is
// the satelle home, whose session records class the seats it summarises.
func devFor(k *kernel.Kernel, c *hosted.Client, project string) *ledgerrepl.Dev {
	return &ledgerrepl.Dev{Client: c, Project: project, Ledger: k.Ledger, Cache: k.Cache, Home: config.GlobalDir()}
}

// newReplicator is the one replicator the CLI builds, for every store: it
// refreshes the cache after a pull and places a pulled item that arrived
// without a workflow by this repo's rules.
func newReplicator(k *kernel.Kernel, s ledgerrepl.Store, out io.Writer) *ledgerrepl.Replicator {
	return &ledgerrepl.Replicator{Ledger: k.Ledger, Store: s, Refs: kernel.BlobRefs, Out: out, Cache: k.Cache, Choose: k, Mirrors: k}
}

// withStore runs fn with the replicator for the resolved store, after naming
// the store on header (nil names it nowhere); for the local store it says so
// on out and runs nothing.
func withStore(out, header io.Writer, verb string, fn func(k *kernel.Kernel, r *ledgerrepl.Replicator) error) error {
	k, rp, err := openKernel()
	if err != nil {
		return err
	}
	defer k.Close()
	s, closeFn, err := openStore(k, rp, verb)
	if err != nil {
		return err
	}
	defer closeFn()
	if s == nil {
		fmt.Fprintln(out, localStoreLine)
		return nil
	}
	if header != nil {
		fmt.Fprintf(header, "ledger store: %s\n", s.Describe())
	}
	return fn(k, newReplicator(k, s, out))
}
