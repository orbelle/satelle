//go:build !windows

package remotenode

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/remote/provider"
	"github.com/bobmcallan/satelle/internal/remotedef"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

const detachDockerBody = `FAKE=$(dirname "$0")/nodes
map() {
  case "$1" in
    satelle-node-*:*) echo "$FAKE/${1%%:*}${1#*:}" ;;
    *) echo "$1" ;;
  esac
}
last() { for a; do l=$a; done; echo "$l"; }
cmd=$1; shift
case "$cmd" in
  build|info) exit 0 ;;
  run)
    while [ $# -gt 0 ]; do [ "$1" = --name ] && name=$2; shift; done
    mkdir -p "$FAKE/$name" ;;
  container)
    [ -f "$FAKE/../hang-inspect" ] && exec sleep 30
    [ -d "$FAKE/$(last "$@")" ] ;;
  rm) [ -f "$FAKE/../fail-rm" ] && { echo "Error: cannot remove" >&2; exit 1; }
    shift; for c; do rm -rf "$FAKE/$c"; done ;;
  cp)
    if [ "$2" = - ]; then
      case "$1" in */out/outcome.json) [ -f "$FAKE/../link.tar" ] && { cat "$FAKE/../link.tar"; exit 0; } ;; esac
      src=$(map "$1")
      [ -e "$src" ] || { echo "Error: no such file: $1" >&2; exit 1; }
      exec tar -C "$(dirname "$src")" -cf - "$(basename "$src")"
    fi
    cp -R "$(map "$1")" "$(map "$2")" ;;
  exec)
    [ "$1" = -d ] && exit 0
    [ "$1" = -i ] && shift
    [ -f "$FAKE/../hang-exec" ] && exec sleep 30
    name=$1; shift
    [ -d "$FAKE/$name" ] || { echo "Error: no such container: $name" >&2; exit 1; }
    case " $* " in *" run-local "*" --detach "*) cat >"$FAKE/$name/detach.stdin"; echo "node run started detached as process 0"; exit 0 ;; esac
    [ "$1" = ls ] && [ -f "$FAKE/../fail-ls" ] && { echo "Error: the daemon is gone" >&2; exit 1; }
    n=$#
    for a in "$@"; do
      case "$a" in /node*) a="$FAKE/$name$a" ;; esac
      set -- "$@" "$a"
    done
    shift $n
    exec "$@" ;;
  *) echo "fake docker: $cmd not faked" >&2; exit 1 ;;
esac
`

type detached struct {
	o       FireOptions
	run     *satellepb.ItemRun
	fake    string
	log     string
	nodeDir string
	out     *strings.Builder
}

func fireDetached(t *testing.T, o FireOptions) detached {
	t.Helper()
	script, log := fakeDockerFor(t, detachDockerBody)
	var out strings.Builder
	o.Docker, o.Runs, o.Out, o.Detach, o.Actor = provider.DockerLocal{Docker: script}, LocalRuns{Dir: t.TempDir()}, &out, true, "ada"
	run, err := FireLocal(context.Background(), o)
	if err != nil {
		t.Fatalf("FireLocal --detach = %v\n%s", err, out.String())
	}
	if run.GetState() != RunRunning || run.GetNodeId() == "" {
		t.Fatalf("the detached run = %v", run)
	}
	fake := filepath.Dir(script)
	d := detached{o: o, run: run, fake: fake, log: log, out: &out,
		nodeDir: filepath.Join(fake, "nodes", provider.ContainerName(run.GetNodeId()), "node")}
	if !strings.Contains(out.String(), "node "+run.GetNodeId()+" is running on "+o.Item+"; bring it back with satelle remote status") {
		t.Fatalf("fire output:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(d.nodeDir, "out")); err != nil {
		t.Fatalf("the node's out dir was not made before it started: %v", err)
	}
	return d
}

func (d detached) stdin(t *testing.T) *os.File {
	t.Helper()
	f, err := os.Open(filepath.Join(filepath.Dir(d.nodeDir), "detach.stdin"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func (d detached) conclude(t *testing.T) {
	t.Helper()
	if _, err := RunLocal(context.Background(), LocalOptions{Dir: d.nodeDir, Version: "1.0.0", Secrets: d.stdin(t)}); err != nil {
		t.Fatal(err)
	}
	d.exit(t, "0", "")
}

func (d detached) exit(t *testing.T, code, log string) {
	t.Helper()
	writeFiles(t, filepath.Join(d.nodeDir, "out"), map[string]string{"exit": code + "\n", "node.log": log})
}

func (d detached) bringBack(t *testing.T) (*satellepb.ItemRun, string, error) {
	t.Helper()
	return d.bringBackWith(t, context.Background(), 0)
}

func (d detached) bringBackWith(t *testing.T, ctx context.Context, bound time.Duration) (*satellepb.ItemRun, string, error) {
	t.Helper()
	var out strings.Builder
	run, err := BringBackLocal(ctx, BringBackOptions{Kernel: d.o.Kernel, Runs: d.o.Runs, Remote: d.o.Remote,
		Item: d.o.Item, Definition: d.o.Definition, Docker: d.o.Docker, Actor: "ada", Out: &out, Bound: bound})
	return run, out.String(), err
}

func (d detached) dockerLog(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(d.log)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (d detached) seated(t *testing.T) bool {
	t.Helper()
	events, err := d.o.Kernel.Ledger.Read(d.o.Item)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range item.FoldSeats(events) {
		if s.Session == d.run.GetNodeId() {
			return true
		}
	}
	return false
}

func (d detached) released(t *testing.T) bool {
	t.Helper()
	events, err := d.o.Kernel.Ledger.Read(d.o.Item)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Kind == "seat.released" && strings.Contains(string(e.Payload), d.run.GetNodeId()) {
			return true
		}
	}
	return false
}

func itemFire(t *testing.T) FireOptions {
	t.Helper()
	tree, k, it := localFixture(t, "1.0.0")
	root := filepath.Join(tree, ".satelle")
	def, bundle, err := LoadLocal(root, "probe")
	if err != nil {
		t.Fatal(err)
	}
	return FireOptions{Kernel: k, Tree: tree, DataDir: root, Remote: "probe", Definition: def, Bundle: bundle, Item: it.ID, Steps: 1}
}

func TestFireLocalDetachRefusals(t *testing.T) {
	f := secretFixture(t, "echo checked", nil, "")
	script, log := fakeDockerFor(t, "exit 0\n")
	o := f.def
	o.Docker, o.Runs, o.Detach = provider.DockerLocal{Docker: script}, LocalRuns{Dir: t.TempDir()}, true
	if _, err := FireLocal(context.Background(), o); err == nil || !strings.Contains(err.Error(), "--detach cannot hand a docker-local node its secrets: a docker-local detached start has no stdin; fire it attached") {
		t.Fatalf("FireLocal --detach of docker-local with [secrets] = %v", err)
	}
	if _, err := o.Runs.LoadDetached(o.Remote, o.Item); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a refused --detach saved a detached record: %v", err)
	}
	if err := detachRefusal(remotedef.Definition{Provider: remotedef.ProviderCommand, Secrets: o.Definition.Secrets}); err != nil {
		t.Fatalf("--detach of a command remote with [secrets] = %v", err)
	}
	for _, p := range remotedef.Providers {
		if def := (remotedef.Definition{Provider: p}); !RunsHere(def) {
			o.Definition = def
			if _, err := FireLocal(context.Background(), o); err == nil || !strings.Contains(err.Error(), "does not run locally") {
				t.Fatalf("FireLocal --detach of %s = %v", p, err)
			}
			if err := detachRefusal(def); err == nil || !strings.Contains(err.Error(), "--detach applies to a remote that runs here (docker-local or command)") {
				t.Fatalf("detachRefusal(%s) = %v", p, err)
			}
		}
	}
	if _, err := os.Stat(log); err == nil {
		t.Fatal("docker ran for a refused --detach")
	}
}

func TestBringBackLocalImportsAConcludedRun(t *testing.T) {
	d := fireDetached(t, itemFire(t))
	log := d.dockerLog(t)
	if !strings.Contains(log, "exec -i "+provider.ContainerName(d.run.GetNodeId())+" satelle node run-local --dir /node --detach --progress") ||
		strings.Contains(log, "exec -d") {
		t.Fatalf("the node was not started detached:\n%s", log)
	}
	if !d.seated(t) {
		t.Fatal("the detached node holds no seat")
	}

	run, _, err := d.bringBack(t)
	if err != nil || run.GetState() != RunRunning || !d.seated(t) {
		t.Fatalf("a node still working: %v, %v (seated %v)", run, err, d.seated(t))
	}

	d.conclude(t)
	run, out, err := d.bringBack(t)
	if err != nil || run.GetState() != RunConcluded || run.GetVerdict() != kernel.VerdictAccept || !strings.Contains(run.GetNotes(), "imported ") {
		t.Fatalf("BringBackLocal = %v, %v\n%s", run, err, out)
	}
	if d.seated(t) || !d.released(t) {
		t.Fatal("the node's seat is not released")
	}
	if got, err := d.o.Kernel.Get(d.o.Item); err != nil || got.Status != "b" {
		t.Fatalf("the item after the bring-back = %+v, %v", got, err)
	}
	if strings.Contains(d.dockerLog(t), "rm ") || !strings.Contains(out, "is left running; remove it with docker rm -f") {
		t.Fatalf("scope = \"job\" removed the container or printed no hint:\n%s", out)
	}
	if _, err := d.o.Runs.LoadDetached(d.o.Remote, d.o.Item); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the detached record is kept: %v", err)
	}
	again, _, err := d.bringBack(t)
	if err != nil || again.GetState() != RunConcluded {
		t.Fatalf("a second bring-back = %v, %v", again, err)
	}
}

func TestBringBackLocalRejectsAnAlteredChain(t *testing.T) {
	d := fireDetached(t, itemFire(t))
	d.conclude(t)
	p := filepath.Join(d.nodeDir, "runtime", "ledger", d.o.Item+".jsonl")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, bytes.Replace(b, []byte("probe me"), []byte("Probe me"), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	run, out, err := d.bringBack(t)
	if err != nil || run.GetState() != RunConcluded || run.GetVerdict() != kernel.VerdictReject || !strings.Contains(run.GetNotes(), "does not verify") {
		t.Fatalf("BringBackLocal of an altered chain = %v, %v\n%s", run, err, out)
	}
	if d.seated(t) {
		t.Fatal("the seat is held after a rejected run")
	}
}

func TestBringBackLocalStopsAFailedNode(t *testing.T) {
	d := fireDetached(t, itemFire(t))
	d.exit(t, "1", "starting\nthe node broke\n")
	run, out, err := d.bringBack(t)
	if err != nil || run.GetState() != RunStopped || !strings.Contains(run.GetReason(), "the node failed: exit 1") ||
		!strings.Contains(run.GetReason(), "the node broke") {
		t.Fatalf("BringBackLocal of a failed node = %v, %v\n%s", run, err, out)
	}
	if d.seated(t) || !d.released(t) {
		t.Fatal("the failed node's seat is not released")
	}
	if strings.Contains(d.dockerLog(t), "rm ") || !strings.Contains(out, "is left to inspect") {
		t.Fatalf("the failed node's container was not left to inspect:\n%s", out)
	}
}

func TestBringBackLocalLeavesTheRunWhenItCannotLook(t *testing.T) {
	d := fireDetached(t, itemFire(t))
	d.conclude(t)
	writeFiles(t, d.fake, map[string]string{"fail-ls": ""})
	if _, _, err := d.bringBack(t); err == nil || !strings.Contains(err.Error(), "the daemon is gone") {
		t.Fatalf("BringBackLocal with no listing = %v", err)
	}
	run, err := d.o.Runs.Load(d.o.Remote, d.o.Item)
	if err != nil || run.GetState() != RunRunning || !d.seated(t) {
		t.Fatalf("the run after a failed look = %v, %v (seated %v)", run, err, d.seated(t))
	}
}

func TestBringBackLocalTaskDriveHoldsNoSeat(t *testing.T) {
	f := newTaskFixture(t, "echo checked")
	d := fireDetached(t, f.def)
	d.conclude(t)
	run, out, err := d.bringBack(t)
	if err != nil || run.GetState() != RunConcluded || run.GetVerdict() != kernel.VerdictAccept {
		t.Fatalf("BringBackLocal of a task = %v, %v\n%s", run, err, out)
	}
	if d.released(t) {
		t.Fatal("a seat was released for a task drive, which is never seated")
	}
	if !strings.Contains(d.dockerLog(t), "rm -f "+provider.ContainerName(d.run.GetNodeId())) || !strings.Contains(out, "is removed") {
		t.Fatalf("scope = \"run\" did not remove the container:\n%s", out)
	}
}

func TestBringBackLocalStopsARunWhoseContainerIsGone(t *testing.T) {
	d := fireDetached(t, itemFire(t))
	if err := os.RemoveAll(filepath.Dir(d.nodeDir)); err != nil {
		t.Fatal(err)
	}
	run, out, err := d.bringBack(t)
	if err != nil || run.GetState() != RunStopped || run.GetReason() != "the node's container is gone" {
		t.Fatalf("BringBackLocal with the container gone = %v, %v", run, err)
	}
	if !strings.Contains(out, "releasing its seat") || d.seated(t) || !d.released(t) {
		t.Fatalf("the seat was not released:\n%s", out)
	}
}

func TestBringBackLocalRefusesALinkFromTheNode(t *testing.T) {
	d := fireDetached(t, itemFire(t))
	d.conclude(t)
	secret := filepath.Join(t.TempDir(), "host-file")
	const content = "content only the host holds"
	if err := os.WriteFile(secret, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "outcome.json", Typeflag: tar.TypeSymlink, Linkname: secret}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, d.fake, map[string]string{"link.tar": buf.String()})
	before, err := d.o.Kernel.Ledger.Read(d.o.Item)
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := d.bringBack(t)
	if err != nil || run.GetState() != RunStopped || !strings.Contains(run.GetReason(), "neither a file nor a directory") {
		t.Fatalf("BringBackLocal of a link = %v, %v", run, err)
	}
	if strings.Contains(run.GetReason()+run.GetNotes(), content) {
		t.Fatal("the host file was read through the node's link")
	}
	after, err := d.o.Kernel.Ledger.Read(d.o.Item)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range after[len(before):] {
		if e.Actor == "node:"+d.run.GetNodeId() {
			t.Fatalf("the node's event %s was imported", e.Kind)
		}
	}
}

func TestReadRegularAndRegularTreeRefuseLinks(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"tree/a": "a", "target": "t"})
	if err := os.Symlink(filepath.Join(dir, "target"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := readRegular(filepath.Join(dir, "link")); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("readRegular of a link = %v", err)
	}
	if b, err := readRegular(filepath.Join(dir, "target")); err != nil || string(b) != "t" {
		t.Fatalf("readRegular = %q, %v", b, err)
	}
	if err := regularTree(filepath.Join(dir, "tree")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "target"), filepath.Join(dir, "tree", "b")); err != nil {
		t.Fatal(err)
	}
	if err := regularTree(filepath.Join(dir, "tree")); err == nil {
		t.Fatal("regularTree passed a link")
	}
}

func TestLocalRunsRefusesAPathOutsideItsDir(t *testing.T) {
	base := t.TempDir()
	runs := LocalRuns{Dir: filepath.Join(base, "remotes")}
	sentinel := filepath.Join(base, "x.json")
	writeFiles(t, base, map[string]string{"x.json": "keep"})
	for _, tc := range []struct{ remote, item, want string }{
		{"probe", "../../x", "not an item id"},
		{"../x", "sto_1", "not a remote id"},
	} {
		run := &satellepb.ItemRun{State: RunRunning}
		for name, err := range map[string]error{
			"Save":         runs.Save(tc.remote, tc.item, run),
			"SaveDetached": runs.SaveDetached(tc.remote, tc.item, LocalDetached{}),
			"DropDetached": runs.DropDetached(tc.remote, tc.item),
		} {
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%s(%q, %q) = %v", name, tc.remote, tc.item, err)
			}
		}
		if _, err := runs.Load(tc.remote, tc.item); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Load(%q, %q) = %v", tc.remote, tc.item, err)
		}
		if _, err := runs.LoadDetached(tc.remote, tc.item); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("LoadDetached(%q, %q) = %v", tc.remote, tc.item, err)
		}
		if _, err := BringBackLocal(context.Background(), BringBackOptions{Runs: runs, Remote: tc.remote, Item: tc.item}); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("BringBackLocal(%q, %q) = %v", tc.remote, tc.item, err)
		}
	}
	if b, err := os.ReadFile(sentinel); err != nil || string(b) != "keep" {
		t.Fatalf("the file outside the runs dir = %q, %v", b, err)
	}
	if _, err := os.Stat(runs.Dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("something was written under %s: %v", runs.Dir, err)
	}
}

func TestBringBackLocalReportsRunningWithProgress(t *testing.T) {
	d := fireDetached(t, itemFire(t))
	fired, err := d.o.Runs.LoadDetached(d.o.Remote, d.o.Item)
	if err != nil || fired.Answered.IsZero() {
		t.Fatalf("the fire recorded no answer: %+v, %v", fired, err)
	}

	run, _, err := d.bringBack(t)
	if err != nil || run.GetState() != RunRunning || run.GetNotes() != "" {
		t.Fatalf("a node with no progress yet = %v, %v", run, err)
	}

	writeFiles(t, filepath.Join(d.nodeDir, "out"), map[string]string{"progress": "dispatch perform\n"})
	run, _, err = d.bringBack(t)
	if err != nil || run.GetState() != RunRunning || run.GetNotes() != "progress: dispatch perform" || !d.seated(t) {
		t.Fatalf("a working node = %v, %v (seated %v)", run, err, d.seated(t))
	}
	side, err := d.o.Runs.LoadDetached(d.o.Remote, d.o.Item)
	if err != nil || side.Answered.IsZero() || side.Answered.Before(fired.Answered) {
		t.Fatalf("the answer was not recorded: %+v (fired %v), %v", side, fired.Answered, err)
	}
	stored, err := d.o.Runs.Load(d.o.Remote, d.o.Item)
	if err != nil || stored.GetState() != RunRunning || stored.GetNotes() != "" {
		t.Fatalf("the stored run was rewritten: %v, %v", stored, err)
	}
}

type snapshot struct {
	chain       int
	head        string
	run, side   string
	dockerLines int
}

func (d detached) snapshot(t *testing.T) snapshot {
	t.Helper()
	events, err := d.o.Kernel.Ledger.Read(d.o.Item)
	if err != nil {
		t.Fatal(err)
	}
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	return snapshot{chain: len(events), head: events[len(events)-1].Hash,
		run:         read(d.o.Runs.path(d.o.Remote, d.o.Item)),
		side:        read(d.o.Runs.detachedPath(d.o.Remote, d.o.Item)),
		dockerLines: strings.Count(d.dockerLog(t), "\n")}
}

func (d detached) recordedNothing(t *testing.T, before snapshot) {
	t.Helper()
	after := d.snapshot(t)
	if after.chain != before.chain || after.head != before.head {
		t.Errorf("the item's chain changed: %d events, was %d", after.chain, before.chain)
	}
	if after.run != before.run || after.side != before.side {
		t.Errorf("the run or its detached record was rewritten:\n%s\n%s", after.run, after.side)
	}
	if !d.seated(t) || d.released(t) {
		t.Error("the node's seat was released")
	}
	added := strings.Join(strings.Split(d.dockerLog(t), "\n")[before.dockerLines:], "\n")
	if strings.Contains(added, "rm ") {
		t.Errorf("the container was removed:\n%s", added)
	}
	if run, err := d.o.Runs.Load(d.o.Remote, d.o.Item); err != nil || run.GetState() != RunRunning ||
		strings.Contains(run.GetReason(), "container is gone") {
		t.Errorf("the stored run = %v, %v", run, err)
	}
}

func TestBringBackLocalReportsALostNode(t *testing.T) {
	for _, hang := range []string{"hang-exec", "hang-inspect"} {
		t.Run(hang, func(t *testing.T) {
			d := fireDetached(t, itemFire(t))
			side, err := d.o.Runs.LoadDetached(d.o.Remote, d.o.Item)
			if err != nil {
				t.Fatal(err)
			}
			before := d.snapshot(t)
			writeFiles(t, d.fake, map[string]string{hang: ""})
			began := time.Now()
			run, _, err := d.bringBackWith(t, context.Background(), 300*time.Millisecond)
			if took := time.Since(began); took > 5*time.Second {
				t.Fatalf("status took %s", took)
			}
			if err != nil || run.GetState() != RunLost || !strings.Contains(run.GetReason(), "did not answer within 300ms") ||
				!strings.Contains(run.GetReason(), side.Answered.Format(time.RFC3339)) {
				t.Fatalf("a node that does not answer = %v, %v", run, err)
			}
			d.recordedNothing(t, before)

			if err := os.Remove(filepath.Join(d.fake, hang)); err != nil {
				t.Fatal(err)
			}
			d.conclude(t)
			run, out, err := d.bringBack(t)
			if err != nil || run.GetState() != RunConcluded || run.GetVerdict() != kernel.VerdictAccept {
				t.Fatalf("the bring-back after lost = %v, %v\n%s", run, err, out)
			}
		})
	}
	t.Run("no answer recorded", func(t *testing.T) {
		d := fireDetached(t, itemFire(t))
		if err := writeRunFile(d.o.Runs.detachedPath(d.o.Remote, d.o.Item), []byte(`{"chains":["`+d.o.Item+`"]}`)); err != nil {
			t.Fatal(err)
		}
		writeFiles(t, d.fake, map[string]string{"hang-exec": ""})
		run, _, err := d.bringBackWith(t, context.Background(), 300*time.Millisecond)
		if err != nil || run.GetState() != RunLost || !strings.Contains(run.GetReason(), "no answer is recorded") {
			t.Fatalf("lost with no recorded answer = %v, %v", run, err)
		}
	})
}

func TestBringBackLocalCancelledRecordsNothing(t *testing.T) {
	for _, hang := range []string{"hang-inspect", "hang-exec"} {
		t.Run(hang, func(t *testing.T) {
			d := fireDetached(t, itemFire(t))
			before := d.snapshot(t)
			writeFiles(t, d.fake, map[string]string{hang: ""})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			time.AfterFunc(200*time.Millisecond, cancel)
			began := time.Now()
			run, _, err := d.bringBackWith(t, ctx, 10*time.Second)
			if took := time.Since(began); took > 5*time.Second {
				t.Fatalf("status took %s", took)
			}
			if !errors.Is(err, context.Canceled) || run != nil {
				t.Fatalf("a cancelled status = %v, %v", run, err)
			}
			d.recordedNothing(t, before)
		})
	}
}

type progressSpy struct {
	inner agent.Dispatcher
	path  string
	reads []string
}

func (p *progressSpy) Dispatch(ctx context.Context, role agent.Role, req agent.Request) (agent.Result, error) {
	b, _ := os.ReadFile(p.path)
	p.reads = append(p.reads, string(b))
	return p.inner.Dispatch(ctx, role, req)
}

func stagedAtB(t *testing.T, node string) string {
	t.Helper()
	tree, k, it := localFixture(t, "1.0.0")
	if _, err := k.Transition(context.Background(), it.ID, "b", "ada"); err != nil {
		t.Fatal(err)
	}
	return stageNode(t, tree, k, it.ID, node, 0)
}

func lineAnswers() *answers {
	return &answers{text: map[string]string{"doer": "did it", "r1": `{"verdict":"accept","notes":"fine"}`}}
}

func TestRunLocalRecordsStepsAsItGoes(t *testing.T) {
	nodeDir := stagedAtB(t, "nod_p1")
	progress := filepath.Join(nodeDir, "out", detachedProgress)
	stub := lineAnswers()
	spy := &progressSpy{inner: stub, path: progress}
	out, err := RunLocal(context.Background(), LocalOptions{Dir: nodeDir, Version: "1.0.0", Progress: true, Dispatcher: spy})
	if err != nil || out.Verdict != kernel.VerdictAccept {
		t.Fatalf("RunLocal = %+v, %v", out, err)
	}
	if got := strings.Join(stub.calls, " "); got != "perform:doer judge:r1" {
		t.Fatalf("dispatched %s", got)
	}
	if strings.Join(spy.reads, "|") != "dispatch perform\n|dispatch judge\n" {
		t.Fatalf("progress at each dispatch = %q", spy.reads)
	}
	if b, err := os.ReadFile(progress); err != nil || string(b) != "concluded accept at c\n" {
		t.Fatalf("the last progress = %q, %v", b, err)
	}
}

func TestRunLocalWritesNoProgressWhenAttached(t *testing.T) {
	nodeDir := stagedAtB(t, "nod_p2")
	out, err := RunLocal(context.Background(), LocalOptions{Dir: nodeDir, Version: "1.0.0", Dispatcher: lineAnswers()})
	if err != nil || out.Verdict != kernel.VerdictAccept {
		t.Fatalf("RunLocal = %+v, %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(nodeDir, "out", detachedProgress)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("an attached run wrote progress: %v", err)
	}
}

func (d detached) stop(t *testing.T, actor string) (*satellepb.ItemRun, string, error) {
	t.Helper()
	var out strings.Builder
	run, err := StopLocal(context.Background(), BringBackOptions{Kernel: d.o.Kernel, Runs: d.o.Runs, Remote: d.o.Remote,
		Item: d.o.Item, Definition: d.o.Definition, Docker: d.o.Docker, Actor: actor, Out: &out})
	return run, out.String(), err
}

func (d detached) releasedBy(t *testing.T) string {
	t.Helper()
	events, err := d.o.Kernel.Ledger.Read(d.o.Item)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Kind == "seat.released" && strings.Contains(string(e.Payload), d.run.GetNodeId()) {
			return e.Actor
		}
	}
	return ""
}

func (d detached) stoppedBy(t *testing.T, actor string) {
	t.Helper()
	run, err := d.o.Runs.Load(d.o.Remote, d.o.Item)
	if err != nil || run.GetState() != RunStopped || !strings.Contains(run.GetReason(), "stopped by "+actor) {
		t.Fatalf("the stored run = %v, %v", run, err)
	}
	if d.seated(t) || d.releasedBy(t) != actor {
		t.Fatalf("the node's seat: seated %v, released by %q", d.seated(t), d.releasedBy(t))
	}
	if _, err := d.o.Runs.LoadDetached(d.o.Remote, d.o.Item); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the detached record is kept: %v", err)
	}
	again, _, err := d.bringBack(t)
	if err != nil || again.GetState() != RunStopped || again.GetReason() != run.GetReason() {
		t.Fatalf("a bring-back after the stop = %v, %v", again, err)
	}
}

func TestStopLocalEndsADetachedNode(t *testing.T) {
	d := fireDetached(t, itemFire(t))
	name := provider.ContainerName(d.run.GetNodeId())
	run, out, err := d.stop(t, "stopper")
	if err != nil || run.GetState() != RunStopped || !strings.Contains(run.GetReason(), "stopped by stopper") {
		t.Fatalf("StopLocal = %v, %v\n%s", run, err, out)
	}
	if _, err := os.Stat(filepath.Join(d.fake, "nodes", name)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the node's container is still there: %v", err)
	}
	if !strings.Contains(d.dockerLog(t), "rm -f "+name) || !strings.Contains(out, "the container "+name+" is removed") {
		t.Fatalf("the container was not removed:\n%s", out)
	}
	d.stoppedBy(t, "stopper")
}

func TestStopLocalRefusesAFinishedOrAttachedRun(t *testing.T) {
	d := fireDetached(t, itemFire(t))
	d.conclude(t)
	if run, _, err := d.bringBack(t); err != nil || run.GetState() != RunConcluded {
		t.Fatalf("BringBackLocal = %v, %v", run, err)
	}
	events, err := d.o.Kernel.Ledger.Read(d.o.Item)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Count(d.dockerLog(t), "\n")
	if _, _, err := d.stop(t, "stopper"); err == nil || !strings.Contains(err.Error(), "is already concluded; nothing to stop") {
		t.Fatalf("StopLocal of a concluded run = %v", err)
	}
	after, err := d.o.Kernel.Ledger.Read(d.o.Item)
	if err != nil || len(after) != len(events) || strings.Count(d.dockerLog(t), "\n") != lines {
		t.Fatalf("a refused stop recorded or ran something: %d events, was %d", len(after), len(events))
	}
	if run, err := d.o.Runs.Load(d.o.Remote, d.o.Item); err != nil || run.GetState() != RunConcluded {
		t.Fatalf("the stored run = %v, %v", run, err)
	}

	a := fireDetached(t, itemFire(t))
	if err := a.o.Runs.DropDetached(a.o.Remote, a.o.Item); err != nil {
		t.Fatal(err)
	}
	events, err = a.o.Kernel.Ledger.Read(a.o.Item)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.stop(t, "stopper"); err == nil || !strings.Contains(err.Error(), "is not detached; an attached fire is stopped by interrupting it") {
		t.Fatalf("StopLocal of an attached run = %v", err)
	}
	if after, err := a.o.Kernel.Ledger.Read(a.o.Item); err != nil || len(after) != len(events) || !a.seated(t) {
		t.Fatalf("a refused stop recorded something: %d events, was %d (seated %v)", len(after), len(events), a.seated(t))
	}
	if strings.Contains(a.dockerLog(t), "rm ") {
		t.Fatal("a refused stop removed the container")
	}
	if run, err := a.o.Runs.Load(a.o.Remote, a.o.Item); err != nil || run.GetState() != RunRunning {
		t.Fatalf("the stored run = %v, %v", run, err)
	}
}

func TestStopLocalLeavesTheRunWhenRemovalFails(t *testing.T) {
	d := fireDetached(t, itemFire(t))
	before := d.snapshot(t)
	writeFiles(t, d.fake, map[string]string{"fail-rm": ""})
	if _, _, err := d.stop(t, "stopper"); err == nil || !strings.Contains(err.Error(), "cannot remove") ||
		!strings.Contains(err.Error(), "docker rm -f") {
		t.Fatalf("StopLocal with a failing removal = %v", err)
	}
	after := d.snapshot(t)
	if after.chain != before.chain || after.head != before.head || after.run != before.run || after.side != before.side {
		t.Fatal("a failed stop recorded something")
	}
	if !d.seated(t) || d.released(t) {
		t.Fatal("a failed stop released the node's seat")
	}
	if run, err := d.o.Runs.Load(d.o.Remote, d.o.Item); err != nil || run.GetState() != RunRunning {
		t.Fatalf("the stored run = %v, %v", run, err)
	}
	if err := os.Remove(filepath.Join(d.fake, "fail-rm")); err != nil {
		t.Fatal(err)
	}
	if run, out, err := d.stop(t, "stopper"); err != nil || run.GetState() != RunStopped {
		t.Fatalf("StopLocal run again = %v, %v\n%s", run, err, out)
	}
	d.stoppedBy(t, "stopper")
}

func TestStopLocalWhenTheContainerIsAlreadyGone(t *testing.T) {
	d := fireDetached(t, itemFire(t))
	if err := os.RemoveAll(filepath.Dir(d.nodeDir)); err != nil {
		t.Fatal(err)
	}
	run, out, err := d.stop(t, "stopper")
	if err != nil || run.GetState() != RunStopped || !strings.Contains(out, "the node's container was already gone") {
		t.Fatalf("StopLocal with the container gone = %v, %v\n%s", run, err, out)
	}
	if strings.Contains(d.dockerLog(t), "rm ") {
		t.Fatal("a gone container was removed again")
	}
	d.stoppedBy(t, "stopper")
}

func (d detached) bringBackHolding(t *testing.T, s Secrets) (*satellepb.ItemRun, string, error) {
	t.Helper()
	var out strings.Builder
	run, err := BringBackLocal(context.Background(), BringBackOptions{Kernel: d.o.Kernel, Runs: d.o.Runs, Remote: d.o.Remote,
		Item: d.o.Item, Definition: d.o.Definition, Docker: d.o.Docker, Actor: "ada", Out: &out, Secrets: s})
	return run, out.String(), err
}

func (d detached) recordSecret(t *testing.T, names ...string) {
	t.Helper()
	side, err := d.o.Runs.LoadDetached(d.o.Remote, d.o.Item)
	if err != nil {
		t.Fatal(err)
	}
	side.SecretNames = names
	if err := d.o.Runs.SaveDetached(d.o.Remote, d.o.Item, side); err != nil {
		t.Fatal(err)
	}
}

func TestBringBackLocalRefusesANonIntegerExit(t *testing.T) {
	d := fireDetached(t, itemFire(t))
	d.exit(t, "3; leaked-text", "")
	run, out, err := d.bringBack(t)
	if err != nil || run.GetState() != RunStopped || !strings.Contains(run.GetReason(), "does not hold an exit status") {
		t.Fatalf("BringBackLocal of a non-integer exit = %v, %v\n%s", run, err, out)
	}
	if strings.Contains(run.GetReason()+out, "leaked-text") {
		t.Fatalf("the exit file's text was echoed:\n%s\n%s", run.GetReason(), out)
	}
}

func TestBringBackLocalCleansASecretRunsStrings(t *testing.T) {
	value := randomHex(t, 16)
	held := Secrets{Names: []string{"S"}, Values: map[string]string{"S": value}, Redact: []string{value}}
	stoppedThroughStop := func(t *testing.T, d detached) {
		t.Helper()
		if !strings.Contains(d.dockerLog(t), "rm -f "+provider.ContainerName(d.run.GetNodeId())) {
			t.Fatalf("the secret node was not stopped:\n%s", d.dockerLog(t))
		}
		if _, err := d.o.Runs.LoadDetached(d.o.Remote, d.o.Item); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("the detached record is kept after the stop: %v", err)
		}
	}
	t.Run("a node-named file", func(t *testing.T) {
		d := fireDetached(t, itemFire(t))
		d.recordSecret(t, "S")
		d.conclude(t)
		blobs := filepath.Join(d.nodeDir, "runtime", "blobs")
		if err := os.MkdirAll(blobs, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(blobs, ".."), filepath.Join(blobs, value)); err != nil {
			t.Fatal(err)
		}
		run, out, err := d.bringBackHolding(t, held)
		if err != nil || run.GetState() != RunStopped || !strings.Contains(run.GetReason(), "[secret]") {
			t.Fatalf("BringBackLocal = %v, %v\n%s", run, err, out)
		}
		if strings.Contains(run.GetReason()+out, value) {
			t.Fatalf("the value is in the reason or the output:\n%s\n%s", run.GetReason(), out)
		}
		stoppedThroughStop(t, d)
	})
	t.Run("a log cut across the value", func(t *testing.T) {
		d := fireDetached(t, itemFire(t))
		d.recordSecret(t, "S")
		d.exit(t, "1", strings.Repeat("x", 1990)+value+strings.Repeat("y", 100))
		run, out, err := d.bringBackHolding(t, held)
		if err != nil || run.GetState() != RunStopped || !strings.Contains(run.GetReason(), "the node failed: exit 1") {
			t.Fatalf("BringBackLocal = %v, %v\n%s", run, err, out)
		}
		for i := 0; i+4 <= len(value); i++ {
			if strings.Contains(run.GetReason()+out, value[i:i+4]) {
				t.Fatalf("the reason or output holds %q of the value at %d", value[i:i+4], i)
			}
		}
		stoppedThroughStop(t, d)
	})
}

func TestBringBackLocalRefusesUntakenRecordedSecrets(t *testing.T) {
	d := fireDetached(t, itemFire(t))
	d.recordSecret(t, "S")
	d.conclude(t)
	before := d.snapshot(t)
	if _, _, err := d.bringBack(t); err == nil || !strings.Contains(err.Error(), "secret S: secrets were not taken before the bring-back") {
		t.Fatalf("BringBackLocal with untaken secrets = %v", err)
	}
	if _, _, err := d.stop(t, "stopper"); err == nil || !strings.Contains(err.Error(), "secret S: secrets were not taken before the bring-back") {
		t.Fatalf("StopLocal with untaken secrets = %v", err)
	}
	if after := d.snapshot(t); after != before {
		t.Fatalf("a refused bring-back or stop ran or recorded something: %+v, was %+v", after, before)
	}
}

func TestLocalDetachedSidecarCarriesNoValue(t *testing.T) {
	runs := LocalRuns{Dir: t.TempDir()}
	const value = "the-secret-value-of-s"
	pub, priv, err := newRunSeal()
	if err != nil {
		t.Fatal(err)
	}
	if err := runs.SaveDetached("probe", "sto_1", LocalDetached{Chains: []string{"sto_1"}, Remove: true, Egress: true,
		SecretNames: []string{"S"}, Answered: time.Now(), SealKey: hex.EncodeToString(pub)}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(runs.detachedPath("probe", "sto_1"))
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]any
	if err := json.Unmarshal(b, &keys); err != nil {
		t.Fatal(err)
	}
	if got := slices.Sorted(maps.Keys(keys)); !slices.Equal(got, []string{"answered", "chains", "egress", "remove", "seal_key", "secret_names"}) {
		t.Fatalf("the detached record's keys = %v", got)
	}
	if k, err := hex.DecodeString(keys["seal_key"].(string)); err != nil || len(k) != ed25519.PublicKeySize {
		t.Fatalf("the detached record's seal key = %v, want an Ed25519 public key", keys["seal_key"])
	}
	if !strings.Contains(string(b), `"S"`) || strings.Contains(string(b), value) || strings.Contains(string(b), hex.EncodeToString(priv)) ||
		strings.Contains(string(b), hex.EncodeToString(priv.Seed())) {
		t.Fatalf("the detached record:\n%s", b)
	}
	back, err := runs.LoadDetached("probe", "sto_1")
	if err != nil || !slices.Equal(back.SecretNames, []string{"S"}) {
		t.Fatalf("LoadDetached = %+v, %v", back, err)
	}
}
