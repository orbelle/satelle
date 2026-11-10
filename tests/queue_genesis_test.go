//go:build integration

package tests

import (
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
)

// Two machines on one project each run a command over the same queue file
// before either has synced. Both write the same que_release genesis, so push
// and pull go through with no divergence, and a later add on one reaches the
// other (sto_2b0b59cd AC3).
func TestQueueFileGenesisSyncsBetweenTwoMachines(t *testing.T) {
	t.Parallel()
	srv := fakeserver.Start(t, devToken)
	a := signedIn(t)
	a.must("project", "create", "shared", "--bind", "--server", srv.URL)
	b := signedIn(t)
	b.must("project", "bind", "shared", "--server", srv.URL)

	file := "title = \"Release\"\n[filter]\nkind = \"story\"\n"
	for _, s := range []*sandbox{a, b} {
		s.write(".satelle/queues/release.toml", file)
		s.must("queue", "show", "release")
		if n := len(s.hashes()["que_release"]); n != 1 {
			t.Fatalf("que_release has %d events before any sync, want its genesis", n)
		}
	}

	clean := func(s *sandbox, args ...string) {
		t.Helper()
		r := s.run(args...)
		if r.code != 0 || strings.Contains(r.out+r.err, "diverged:") {
			t.Fatalf("satelle %s: exit %d\n%s%s", strings.Join(args, " "), r.code, r.out, r.err)
		}
	}
	clean(a, "ledger", "push")
	clean(b, "ledger", "pull")
	clean(b, "ledger", "push")
	ha, hb := a.hashes()["que_release"], b.hashes()["que_release"]
	if strings.Join(ha, ",") != strings.Join(hb, ",") {
		t.Fatalf("que_release differs\na: %v\nb: %v", ha, hb)
	}
	if n := len(srv.Events("shared", "que_release")); n != 1 {
		t.Fatalf("server holds %d que_release events, want 1", n)
	}

	s := a.create("story", "--title", "ship it")
	a.must("queue", "add", "release", s.ID)
	clean(a, "ledger", "push")
	clean(b, "ledger", "pull")
	ha, hb = a.hashes()["que_release"], b.hashes()["que_release"]
	if len(ha) < 2 || strings.Join(ha, ",") != strings.Join(hb, ",") {
		t.Fatalf("after the add que_release differs\na: %v\nb: %v", ha, hb)
	}
	a.must("ledger", "verify")
	b.must("ledger", "verify")
}
