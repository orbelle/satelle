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

const localStoreLine = `ledger store is local: nothing replicates (set [ledger] store to "satelle.dev" or a store command to replicate)`

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

func devFor(k *kernel.Kernel, c *hosted.Client, project string) *ledgerrepl.Dev {
	return &ledgerrepl.Dev{Client: c, Project: project, Ledger: k.Ledger, Cache: k.Cache, Home: config.GlobalDir()}
}

func newReplicator(k *kernel.Kernel, s ledgerrepl.Store, out io.Writer) *ledgerrepl.Replicator {
	return &ledgerrepl.Replicator{Ledger: k.Ledger, Store: s, Refs: kernel.BlobRefs, Out: out, Cache: k.Cache, Choose: k, Mirrors: k}
}

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
