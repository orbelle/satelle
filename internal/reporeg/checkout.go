package reporeg

import (
	"os"

	"github.com/bobmcallan/satelle/internal/gitfacts"
)

const (
	CheckoutOK         = "ok"
	CheckoutUnmapped   = "unmapped"
	CheckoutMissing    = "missing"
	CheckoutMismatch   = "mismatch"
	CheckoutUnreadable = "unreadable"
	CheckoutInvalid    = "invalid"
	CheckoutUnsafe     = "unsafe path"
)

type Checkout struct {
	State  string
	Path   string
	Actual string
	Remote bool
}

func (r Registry) CheckoutOf(name string, env []string) Checkout {
	if !r.EntryOK(name) {
		return Checkout{State: CheckoutInvalid}
	}
	path, mapped, ok := r.LocalPath(name)
	switch {
	case !mapped:
		return Checkout{State: CheckoutUnmapped}
	case !ok:
		return Checkout{State: CheckoutInvalid}
	}
	if !ShellSafePath(path) {
		return Checkout{State: CheckoutUnsafe, Path: path}
	}
	if _, err := os.Stat(path); err != nil {
		return Checkout{State: CheckoutMissing, Path: path}
	}
	actual, err := gitfacts.Repo{Dir: path, Env: env, Untrusted: true}.RemoteURL("origin")
	if err != nil {
		return Checkout{State: CheckoutUnreadable, Path: path}
	}
	got, err := NormaliseOrigin(actual)
	if err != nil {
		return Checkout{State: CheckoutUnreadable, Path: path, Remote: true}
	}
	if want, _ := NormaliseOrigin(r.Tracked[name].URL); got != want {
		return Checkout{State: CheckoutMismatch, Path: path, Actual: actual, Remote: true}
	}
	return Checkout{State: CheckoutOK, Path: path, Actual: actual, Remote: true}
}
