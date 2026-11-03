package cli

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bobmcallan/satelle/internal/snapsync"
)

// `sync config push --snapshot-only` and `sync documents push --snapshot-only`
// (sty_a66e1846): the unattended push satelled runs. It publishes only through
// the snapshot check; wherever that check cannot clear the push it uploads
// nothing and exits non-zero. Harness: sync_snapshot_test.go.

// faultOn wraps the fake hosted server so that, once the returned switch is set,
// every request match names answers 500: the snapshot or manifest is unreadable.
func faultOn(ts *httptest.Server, match func(*http.Request) bool) *atomic.Bool {
	armed := &atomic.Bool{}
	next := ts.Config.Handler
	ts.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if armed.Load() && match(r) {
			http.Error(w, "injected fault", http.StatusInternalServerError)
			return
		}
		next.ServeHTTP(w, r)
	})
	return armed
}

func routeIs(r *http.Request, kind string) (rest string, ok bool) {
	_, after, found := strings.Cut(r.URL.Path, "/api/v1/projects/probe/"+kind)
	if !found {
		return "", false
	}
	return strings.Trim(after, "/"), true
}

// configManifestFault fails the config manifest list.
func configManifestFault(r *http.Request) bool {
	rest, ok := routeIs(r, "config")
	return ok && r.Method == http.MethodGet && rest == ""
}

// recordReadFault fails every config content read, which is how a snapshot
// record is read once the manifest names one.
func recordReadFault(r *http.Request) bool {
	rest, ok := routeIs(r, "config")
	return ok && r.Method == http.MethodGet && rest != ""
}

// documentsListFault fails the documents manifest list.
func documentsListFault(r *http.Request) bool {
	rest, ok := routeIs(r, "documents")
	return ok && r.Method == http.MethodGet && rest == ""
}

// snapKind is one of the two push verbs and the file it publishes.
type snapKind struct {
	name string
	verb []string
	area string
	file string
	pull func(m *snapMachine)
	// unavailable are the faults that make this kind's hosted snapshot or
	// manifest unreadable, by name.
	unavailable map[string]func(*http.Request) bool
}

var snapKinds = []snapKind{
	{
		name: "config", verb: []string{"sync", "config", "push"}, area: "skills", file: "skills/x.md",
		pull: func(m *snapMachine) { m.deploy() },
		unavailable: map[string]func(*http.Request) bool{
			"manifest":      configManifestFault,
			"snapshot-read": recordReadFault,
		},
	},
	{
		name: "documents", verb: []string{"sync", "documents", "push"}, area: "documents", file: "documents/d.md",
		pull: func(m *snapMachine) { m.pullDocs() },
		unavailable: map[string]func(*http.Request) bool{
			"manifest":       configManifestFault,
			"documents-list": documentsListFault,
		},
	},
}

func (k snapKind) run(m *snapMachine, extra ...string) (string, error) {
	m.t.Helper()
	return m.runCLI(append(append([]string{}, k.verb...), extra...)...)
}

// refused asserts a --snapshot-only push exited non-zero and uploaded nothing.
func (k snapKind) refused(t *testing.T, f *snapFake, m *snapMachine) error {
	t.Helper()
	before := len(f.putLog())
	out, err := k.run(m, "--snapshot-only")
	if err == nil {
		t.Fatalf("%s: a refused --snapshot-only push exited zero:\n%s", k.name, out)
	}
	if n := len(f.putLog()) - before; n != 0 {
		t.Errorf("%s: a refused --snapshot-only push uploaded %d content PUT(s): %v", k.name, n, f.putLog()[before:])
	}
	return err
}

// AC3/AC5: a hosted snapshot or manifest that cannot be read refuses the push
// with a non-zero exit and uploads nothing, and records nothing — an offline
// machine never looks freshly checked.
func TestSnapshotOnlyRefusesUnavailable(t *testing.T) {
	for _, k := range snapKinds {
		for name, match := range k.unavailable {
			t.Run(k.name+"/"+name, func(t *testing.T) {
				ts, f := newSnapServer(t)
				seedCred(t, ts.URL)
				a := publishedMachine(t, ts.URL)
				_ = a
				// A machine with no recorded state of its own.
				c := newSnapMachine(t, ts.URL)
				c.write(k.file, "from C\n")
				fault := faultOn(ts, match)
				fault.Store(true)

				err := k.refused(t, f, c)
				if !strings.Contains(err.Error(), "snapshot-only") {
					t.Errorf("the refusal does not name --snapshot-only: %v", err)
				}
				// An area neither side holds a file of is verified from the manifest
				// alone; one the hosted copy holds a snapshot of was not verified.
				for area := range c.recorded() {
					if area == k.area || slices.Contains(publishedAreas, area) {
						t.Errorf("an unavailable refusal verified nothing of %s and must not record it: %v", area, c.recorded())
					}
				}
			})
		}
	}
}

// AC5: against a snapshot that cannot be read, a live sync status reports the
// area unavailable.
func TestSnapshotOnlyUnavailableShowsInStatus(t *testing.T) {
	ts, _ := newSnapServer(t)
	seedCred(t, ts.URL)
	publishedMachine(t, ts.URL)
	c := newSnapMachine(t, ts.URL)
	c.write("skills/x.md", "from C\n")
	faultOn(ts, configManifestFault).Store(true)

	out, err := c.status()
	if err == nil {
		t.Fatalf("an unreadable hosted copy must exit non-zero:\n%s", out)
	}
	wantState(t, out, "skills", "unavailable")
}

// AC3: --snapshot-only changes nothing about a push that is in step: it
// publishes like the plain verb.
func TestSnapshotOnlyPushesWhenInStep(t *testing.T) {
	for _, k := range snapKinds {
		t.Run(k.name, func(t *testing.T) {
			ts, f := newSnapServer(t)
			seedCred(t, ts.URL)
			a := publishedMachine(t, ts.URL)
			a.write(k.file, "edited\n")
			before := len(f.putLog())
			out, err := k.run(a, "--snapshot-only")
			if err != nil {
				t.Fatalf("an in-step --snapshot-only push: %v\n%s", err, out)
			}
			if len(f.putLog()) == before {
				t.Errorf("an in-step --snapshot-only push uploaded nothing:\n%s", out)
			}
			if got := a.recorded()[k.area].State; got != "in step" {
				t.Errorf("recorded = %q, want in step", got)
			}
		})
	}
}

// AC4: without --snapshot-only a push whose snapshot or manifest is unreadable
// still falls back to the plain upload, exactly as before.
func TestPushWithoutSnapshotOnlyStillFallsBackToPlainUpload(t *testing.T) {
	for _, k := range snapKinds {
		t.Run(k.name, func(t *testing.T) {
			ts, f := newSnapServer(t)
			seedCred(t, ts.URL)
			a := newSnapMachine(t, ts.URL)
			a.write(k.file, "plain\n")
			faultOn(ts, configManifestFault).Store(true)

			before := len(f.putLog())
			out, err := k.run(a)
			if err != nil {
				t.Fatalf("the plain-upload fallback failed: %v\n%s", err, out)
			}
			if len(f.putLog()) == before {
				t.Errorf("the fallback uploaded nothing:\n%s", out)
			}
		})
	}
}

// requireReconcileRemedy asserts a refusal of a diverged area names the command
// that keeps the developer's work (sty_aeff3dc7): reconcile shows both sides and
// backs up the one it overrides, where a rehydrate or a pull overwrites the local
// tree.
func requireReconcileRemedy(t *testing.T, label string, err error) {
	t.Helper()
	requireRemedy(t, label, err, "satelle sync reconcile", "satelle sync pull")
}

// requirePullRemedy asserts a refusal of an area that is only behind (nothing
// here changed) names the pull, which overwrites nothing local (sty_0fa4f732).
func requirePullRemedy(t *testing.T, label string, err error) {
	t.Helper()
	requireRemedy(t, label, err, "satelle sync pull", "satelle sync reconcile")
}

func requireRemedy(t *testing.T, label string, err error, want, notWant string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: the push was not refused", label)
	}
	msg := err.Error()
	if !strings.Contains(msg, want) {
		t.Errorf("%s: refusal does not name %s: %q", label, want, msg)
	}
	for _, bad := range []string{notWant, "sync rehydrate", "sync documents pull", "sync config deploy"} {
		if strings.Contains(msg, bad) {
			t.Errorf("%s: refusal names %q: %q", label, bad, msg)
		}
	}
}

// A push behind the hosted copy — plain, and the unattended --snapshot-only one
// satelled runs — whose area this machine also changed names reconcile, for both
// the config and the documents areas.
func TestRefusedPushNamesReconcile(t *testing.T) {
	for _, k := range snapKinds {
		t.Run(k.name, func(t *testing.T) {
			ts, f := newSnapServer(t)
			seedCred(t, ts.URL)
			a := publishedMachine(t, ts.URL)
			b := newSnapMachine(t, ts.URL)
			b.deploy()
			b.pullDocs()
			a.write(k.file, "from A\n")
			a.pushConfig()
			a.pushDocs()
			b.write(k.file, "from B\n")

			_, err := k.run(b)
			requireReconcileRemedy(t, k.name+" plain push", err)
			requireReconcileRemedy(t, k.name+" --snapshot-only push", k.refused(t, f, b))
		})
	}
}

// Hosted changes this machine has not applied — the snapshot versions agree,
// but the hosted copy holds a file this tree and its base lack — refuse the push.
// This machine changed nothing against its base, so the pull is the way forward.
func TestPendingPathsRefusalNamesPull(t *testing.T) {
	ts, f := newSnapServer(t)
	seedCred(t, ts.URL)
	a := newSnapMachine(t, ts.URL)
	a.write("skills/x.md", "x\n")
	a.pushConfig()
	// A snapshot naming skills/y.md whose blob never landed: B's deploy leaves the
	// path incomplete, so its recorded base is at the hosted version without it.
	v := f.forgeRecord(t, "skills", 1, map[string]string{"skills/x.md": snapsync.SHA([]byte("x\n")), "skills/y.md": snapsync.SHA([]byte("y\n"))})

	b := newSnapMachine(t, ts.URL)
	b.deploy()
	base, ok := b.base("skills")
	if !ok || base.Version != v {
		t.Fatalf("B's base = %+v (recorded %v), want version %d", base, ok, v)
	}
	if _, has := base.Files["skills/y.md"]; has || b.has("skills/y.md") {
		t.Fatalf("test premise: the deploy took the incomplete skills/y.md: %+v", base)
	}

	_, err := b.tryPushConfig()
	if err == nil || !strings.Contains(err.Error(), "not yet applied here") {
		t.Fatalf("want the pending-paths refusal, got %v", err)
	}
	requirePullRemedy(t, "pending paths", err)
}
