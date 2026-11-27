//go:build !windows

package remotenode

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/remotedef"
	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

func (d detached) outcome(t *testing.T) LocalOutcome {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(d.nodeDir, "out", localOutcome))
	if err != nil {
		t.Fatal(err)
	}
	var out LocalOutcome
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func (d detached) writeOutcome(t *testing.T, out LocalOutcome) {
	t.Helper()
	if err := writeOutcome(filepath.Join(d.nodeDir, "out"), out); err != nil {
		t.Fatal(err)
	}
}

func (d detached) refusedWithNothingImported(t *testing.T, why string) {
	t.Helper()
	before, err := os.ReadFile(d.o.Kernel.Ledger.Path(d.o.Item))
	if err != nil {
		t.Fatal(err)
	}
	run, out, err := d.bringBack(t)
	if err != nil || run.GetState() != RunConcluded || run.GetVerdict() != kernel.VerdictReject ||
		!strings.Contains(run.GetNotes(), "is not sealed by its run") || !strings.Contains(run.GetNotes(), why) ||
		!strings.Contains(run.GetNotes(), "nothing of the run was imported") {
		t.Fatalf("BringBackLocal = %v, %v\n%s", run, err, out)
	}
	after, err := d.o.Kernel.Ledger.Read(d.o.Item)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range after {
		if e.Actor == "node:"+d.run.GetNodeId() {
			t.Fatalf("the node's event %s was imported", e.Kind)
		}
	}
	if b, err := os.ReadFile(d.o.Kernel.Ledger.Path(d.o.Item)); err != nil || !bytes.HasPrefix(b, before) {
		t.Fatalf("the starter's chain of %s was rewritten: %v", d.o.Item, err)
	}
}

func signWith(t *testing.T, priv ed25519.PrivateKey, out LocalOutcome) LocalOutcome {
	t.Helper()
	s, err := sealOutcome(priv, out)
	if err != nil {
		t.Fatal(err)
	}
	out.Seal = s
	return out
}

func TestDetachedStartHandsThePrivateKeyOnStdinAndKeepsThePublicKey(t *testing.T) {
	d := fireDetached(t, itemFire(t))
	var given map[string]string
	b, err := os.ReadFile(filepath.Join(filepath.Dir(d.nodeDir), "detach.stdin"))
	if err != nil || json.Unmarshal(b, &given) != nil {
		t.Fatalf("the detached start's stdin = %q, %v", b, err)
	}
	priv, err := hex.DecodeString(given[sealPayloadKey])
	if err != nil || len(priv) != ed25519.PrivateKeySize {
		t.Fatalf("the stdin's %s = %q", sealPayloadKey, given[sealPayloadKey])
	}
	side, err := d.o.Runs.LoadDetached(d.o.Remote, d.o.Item)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := hex.DecodeString(side.SealKey)
	if err != nil || len(pub) != ed25519.PublicKeySize || !bytes.Equal(pub, ed25519.PrivateKey(priv).Public().(ed25519.PublicKey)) {
		t.Fatalf("the record's seal key = %q, want the public half of the key handed on stdin", side.SealKey)
	}
	raw, err := os.ReadFile(d.o.Runs.detachedPath(d.o.Remote, d.o.Item))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), given[sealPayloadKey]) || strings.Contains(string(raw), hex.EncodeToString(priv[:ed25519.SeedSize])) {
		t.Fatalf("the detached record holds the private key:\n%s", raw)
	}
	d.conclude(t)
	if run, out, err := d.bringBack(t); err != nil || run.GetVerdict() != kernel.VerdictAccept || !strings.Contains(run.GetNotes(), "imported ") {
		t.Fatalf("BringBackLocal of a sealed run = %v, %v\n%s", run, err, out)
	}
}

func TestBringBackRefusesAnOutcomeTheRunDidNotSeal(t *testing.T) {
	for _, tc := range []struct {
		name, why string
		forge     func(t *testing.T, d detached)
	}{
		{"a rewritten verdict", "does not verify", func(t *testing.T, d detached) {
			out := d.outcome(t)
			out.Verdict = kernel.VerdictReject
			d.writeOutcome(t, out)
		}},
		{"an event appended after the seal", "does not end at the heads", func(t *testing.T, d detached) {
			l, err := ledger.Open(filepath.Join(d.nodeDir, "runtime"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := l.Append(d.o.Item, "item.updated", "node:"+d.run.GetNodeId(), map[string]any{"title": "late"}); err != nil {
				t.Fatal(err)
			}
		}},
		{"a key the check generated", "does not verify", func(t *testing.T, d detached) {
			_, priv, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			d.writeOutcome(t, signWith(t, priv, d.outcome(t)))
		}},
		{"the record's public key read as a seed", "does not verify", func(t *testing.T, d detached) {
			side, err := d.o.Runs.LoadDetached(d.o.Remote, d.o.Item)
			if err != nil {
				t.Fatal(err)
			}
			seed, err := hex.DecodeString(side.SealKey)
			if err != nil {
				t.Fatal(err)
			}
			d.writeOutcome(t, signWith(t, ed25519.NewKeyFromSeed(seed), d.outcome(t)))
		}},
		{"no seal", "carries no seal", func(t *testing.T, d detached) {
			out := d.outcome(t)
			out.Seal = ""
			d.writeOutcome(t, out)
		}},
		{"a record with no seal key", "no seal key recorded", func(t *testing.T, d detached) {
			side, err := d.o.Runs.LoadDetached(d.o.Remote, d.o.Item)
			if err != nil {
				t.Fatal(err)
			}
			side.SealKey = ""
			if err := d.o.Runs.SaveDetached(d.o.Remote, d.o.Item, side); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := fireDetached(t, itemFire(t))
			d.conclude(t)
			tc.forge(t, d)
			d.refusedWithNothingImported(t, tc.why)
		})
	}
}

func TestBringBackRefusesARunTheNodeDidNotSign(t *testing.T) {
	d := fireDetached(t, itemFire(t))
	if _, err := RunLocal(context.Background(), LocalOptions{Dir: d.nodeDir, Version: "1.0.0", Secrets: strings.NewReader("{}")}); err != nil {
		t.Fatal(err)
	}
	d.exit(t, "0", "")
	d.refusedWithNothingImported(t, "carries no seal")
}

func TestReadNodeInputKeepsTheSealKeyOutOfTheEnvironment(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := stdinPayload(Secrets{}, priv)
	if err != nil {
		t.Fatal(err)
	}
	values, got, err := readNodeInput(remotedef.Definition{}, bytes.NewReader(payload))
	if err != nil || len(values) != 0 || !bytes.Equal(got, priv) {
		t.Fatalf("readNodeInput = %v, %x, %v", values, got, err)
	}
	for _, kv := range os.Environ() {
		if strings.Contains(kv, hex.EncodeToString(priv)) || strings.HasPrefix(kv, sealPayloadKey+"=") {
			t.Fatalf("the seal key reached the environment: %s", kv)
		}
	}
	if _, _, err := readNodeInput(remotedef.Definition{}, strings.NewReader(`{"satelle.seal":"00"}`)); err == nil {
		t.Fatal("a seal key of the wrong size was taken")
	}
}

func TestACheckCannotForgeTheOutcome(t *testing.T) {
	f := newTaskFixture(t, `printf '%s' "$(env)" > ../check-env; printf '{"verdict":"accept","notes":"forged"}' > ../out/outcome.json; echo checked`)
	d := fireDetached(t, f.def)
	d.conclude(t)
	env, err := os.ReadFile(filepath.Join(d.nodeDir, "check-env"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(env), sealPayloadKey) {
		t.Fatalf("the check's environment holds the seal key:\n%s", env)
	}
	if out := d.outcome(t); out.Notes == "forged" || out.Seal == "" {
		t.Fatalf("the node did not replace the check's outcome with its own sealed one: %+v", out)
	}
	if run, out, err := d.bringBack(t); err != nil || run.GetVerdict() != kernel.VerdictAccept || !strings.Contains(run.GetNotes(), "imported ") {
		t.Fatalf("BringBackLocal = %v, %v\n%s", run, err, out)
	}
}

func TestACheckThatBreaksTheRunLeavesNothingToImport(t *testing.T) {
	f := newTaskFixture(t, `printf '{"verdict":"accept","notes":"forged"}' > ../out/outcome.json; printf '{"kind":"node"}\n' > ../runtime/runtime.json; echo checked`)
	d := fireDetached(t, f.def)
	_, err := RunLocal(context.Background(), LocalOptions{Dir: d.nodeDir, Version: "1.0.0", Secrets: d.stdin(t)})
	if err == nil || !strings.Contains(err.Error(), "changed during the run") {
		t.Fatalf("RunLocal = %v, want the changed stamp refused", err)
	}
	if _, err := os.Stat(filepath.Join(d.nodeDir, "out", localOutcome)); !os.IsNotExist(err) {
		t.Fatalf("the check's outcome was left in place: %v", err)
	}
	if err := os.WriteFile(filepath.Join(d.nodeDir, "out", localOutcome), []byte(`{"verdict":"accept","notes":"forged"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	d.exit(t, "1", "")
	d.refusedWithNothingImported(t, "carries no seal")
}

type rewriteStamp struct {
	inner agent.Dispatcher
	dir   string
}

func (r rewriteStamp) Dispatch(ctx context.Context, role agent.Role, req agent.Request) (agent.Result, error) {
	if role == agent.Perform {
		_ = os.WriteFile(filepath.Join(r.dir, "runtime", config.RuntimeStampName), []byte(`{"kind":"node"}`), 0o644)
	}
	return r.inner.Dispatch(ctx, role, req)
}

func TestJoinPushesNothingWhenTheRunDoesNotVerify(t *testing.T) {
	l := newLocalRepo(t)
	it, err := l.k.Create(item.Fields{Kind: ptr("story"), Title: ptr("remote work")}, "ada")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.k.Transition(context.Background(), it.ID, "b", "ada"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.repl().Push(context.Background()); err != nil {
		t.Fatal(err)
	}
	l.srv.AddNode(&fakeserver.Node{ID: "nod_9", Token: "node-tok", Bundle: nodeBundle(t), Version: "v3",
		Secrets: map[string]string{"NOTE": nodeSecret}, Run: &satellepb.RunDescriptor{Mode: ModeItem, Project: l.project, ItemId: it.ID}})
	dir := t.TempDir()
	d := rewriteStamp{inner: &answers{text: map[string]string{"doer": "did it", "r1": `{"verdict":"accept","notes":"fine"}`}}, dir: dir}
	var log bytes.Buffer
	_, err = Join(context.Background(), JoinOptions{Server: l.srv.URL, NodeID: "nod_9", Token: "node-tok", Dir: dir,
		Log: &log, Version: "0.0.900", Dispatcher: d})
	if err == nil || !strings.Contains(err.Error(), "changed during the run") {
		t.Fatalf("Join = %v, want the changed stamp refused\n%s", err, log.String())
	}
	st := l.srv.NodeStatuses("nod_9")
	if last := st[len(st)-1]; last.GetVerdict() != kernel.VerdictReject || !strings.Contains(last.GetNotes(), "changed during the run") {
		t.Fatalf("conclusion = %+v", last)
	}
	for _, m := range methods(l.srv, "node-tok") {
		if m == "Append" || m == "PutItems" {
			t.Fatalf("the node pushed its ledger (%s) after its run failed to verify", m)
		}
	}
}
