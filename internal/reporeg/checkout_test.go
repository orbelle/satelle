package reporeg

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bobmcallan/satelle/internal/gittest"
)

func checkoutReg(t *testing.T, path string) Registry {
	t.Helper()
	reg := Registry{Tracked: map[string]Entry{"web": {URL: "https://github.com/o/web"}}}
	if path != "" {
		reg.Local = map[string]Local{"web": {Path: path}}
	}
	return reg
}

func TestCheckoutOf(t *testing.T) {
	repo := func(origin string) string {
		dir := t.TempDir()
		gittest.Git(t, dir, "init", "-q")
		if origin != "" {
			gittest.Git(t, dir, "remote", "add", "origin", origin)
		}
		return dir
	}
	ok := repo("https://github.com/o/web")
	if c := checkoutReg(t, ok).CheckoutOf("web", nil); c.State != CheckoutOK || c.Path != ok || !c.Remote {
		t.Errorf("ok: %+v", c)
	}
	if c := checkoutReg(t, "").CheckoutOf("web", nil); c.State != CheckoutUnmapped {
		t.Errorf("unmapped: %+v", c)
	}
	if c := checkoutReg(t, filepath.Join(t.TempDir(), "gone")).CheckoutOf("web", nil); c.State != CheckoutMissing {
		t.Errorf("missing: %+v", c)
	}
	if c := checkoutReg(t, repo("https://github.com/o/other")).CheckoutOf("web", nil); c.State != CheckoutMismatch {
		t.Errorf("mismatch: %+v", c)
	}
	if c := checkoutReg(t, repo("")).CheckoutOf("web", nil); c.State != CheckoutUnreadable {
		t.Errorf("unreadable: %+v", c)
	}
	if c := checkoutReg(t, "relative/path").CheckoutOf("web", nil); c.State != CheckoutInvalid {
		t.Errorf("invalid: %+v", c)
	}
	for _, name := range []string{"a b", "a$b"} {
		dir := filepath.Join(t.TempDir(), name)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		gittest.Git(t, dir, "init", "-q")
		gittest.Git(t, dir, "remote", "add", "origin", "https://github.com/o/web")
		if c := checkoutReg(t, dir).CheckoutOf("web", []string{"PATH=" + t.TempDir()}); c.State != CheckoutUnsafe || c.Path != dir {
			t.Errorf("unsafe %q: %+v", name, c)
		}
	}
}
