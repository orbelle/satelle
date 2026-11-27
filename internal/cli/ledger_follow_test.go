package cli

import (
	"path/filepath"
	"testing"

	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
)

func TestFollowTargetOnlyBoundSatelleDevRepos(t *testing.T) {
	t.Run("bound to satelle.dev", func(t *testing.T) {
		srv := fakeserver.Start(t, testToken)
		m := newMachine(t)
		must(t, "project", "create", "shared", "--bind", "--server", srv.URL)
		fr, ok := followTargetAt(m.dir)
		if !ok || fr.b.Project == "" || fr.b.Server != srv.URL {
			t.Fatalf("followTargetAt = %+v, %v", fr, ok)
		}
	})
	t.Run("unbound", func(t *testing.T) {
		m := newMachine(t)
		if fr, ok := followTargetAt(m.dir); ok {
			t.Fatalf("an unbound repo is followed: %+v", fr)
		}
	})
	t.Run("store local", func(t *testing.T) {
		srv := fakeserver.Start(t, testToken)
		m := newMachine(t)
		must(t, "project", "create", "shared", "--bind", "--server", srv.URL)
		appendLedger(t, m.dir, `store = "local"`)
		if fr, ok := followTargetAt(m.dir); ok {
			t.Fatalf("a local-store repo is followed: %+v", fr)
		}
	})
	t.Run("store command", func(t *testing.T) {
		srv := fakeserver.Start(t, testToken)
		m := newMachine(t)
		must(t, "project", "create", "shared", "--bind", "--server", srv.URL)
		appendLedger(t, m.dir, `store = { command = "sh", args = ["adapter"] }`)
		if fr, ok := followTargetAt(m.dir); ok {
			t.Fatalf("a store-command repo is followed: %+v", fr)
		}
	})
	t.Run("not a repo", func(t *testing.T) {
		newMachine(t)
		if fr, ok := followTargetAt(filepath.Join(t.TempDir(), "gone")); ok {
			t.Fatalf("a missing path is followed: %+v", fr)
		}
		if fr, ok := followTargetAt(t.TempDir()); ok {
			t.Fatalf("a directory with no repo is followed: %+v", fr)
		}
	})
}
