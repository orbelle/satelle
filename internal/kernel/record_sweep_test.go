//go:build linux || darwin

package kernel

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/procgroup"
)

func startSleep(t *testing.T) (*exec.Cmd, procgroup.Proc) {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_, start := procgroup.ProbeIdentity(cmd.Process.Pid)
	return cmd, procgroup.Proc{PID: cmd.Process.Pid, Start: start}
}

func exitedProc(t *testing.T) procgroup.Proc {
	t.Helper()
	cmd, p := startSleep(t)
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if procgroup.Probe(p) != procgroup.Gone {
		t.Fatalf("killed process %d still probes alive", p.PID)
	}
	return p
}

func liveProc(t *testing.T) procgroup.Proc {
	t.Helper()
	cmd, p := startSleep(t)
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	if procgroup.Probe(p) != procgroup.Alive {
		t.Fatalf("started process %d does not probe alive", p.PID)
	}
	return p
}

func setOwner(t *testing.T, dir string, body []byte) {
	t.Helper()
	if err := os.WriteFile(dir+ownerExt, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func ownedBy(t *testing.T, dir string, p procgroup.Proc) {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	setOwner(t, dir, b)
}

func openRun(t *testing.T, k *Kernel, id string) Record {
	t.Helper()
	r, err := k.OpenRecord(id)
	if err != nil {
		t.Fatal(err)
	}
	if r.Dir == "" {
		t.Fatal("OpenRecord wrote no folder")
	}
	t.Cleanup(func() { _ = r.Close() })
	if _, err := os.Stat(r.Dir + ownerExt); err != nil {
		t.Fatalf("no owner file beside %s: %v", r.Dir, err)
	}
	return r
}

func gone(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); !os.IsNotExist(err) {
		t.Errorf("%s still exists (%v)", p, err)
	}
}

func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out[rel+" mode"] = info.Mode().Perm().String()
		if !info.IsDir() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			out[rel] = string(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameSnapshot(t *testing.T, dir string, want map[string]string) {
	t.Helper()
	got := snapshot(t, dir)
	if len(got) != len(want) {
		t.Errorf("%s: %v, want %v", dir, got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %s = %q, want %q", dir, k, got[k], v)
		}
	}
}

func twoItems(t *testing.T) (*Kernel, *fakeDispatcher, item.Item, item.Item) {
	t.Helper()
	k, d, repo := multiState(t)
	a := toWork(t, k, repo)
	b := toWork(t, k, repo)
	mustAttach(t, k, a.ID, "plan", "a plan\n")
	mustAttach(t, k, b.ID, "plan", "b plan\n")
	return k, d, a, b
}

func performB(t *testing.T, k *Kernel, d *fakeDispatcher, b item.Item) recordSeen {
	t.Helper()
	seen := watch(d)
	d.reqs = nil
	if _, err := k.Perform(context.Background(), b.ID, "me"); err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 1 {
		t.Fatalf("dispatches %d", len(*seen))
	}
	s := (*seen)[0]
	prompt := d.reqs[len(d.reqs)-1].Prompt
	if !strings.HasPrefix(s.dir, filepath.Join(k.Root, runsDir, b.ID+"-")) {
		t.Fatalf("new run folder %q is not under runs/%s-", s.dir, b.ID)
	}
	if s.files["attachments/plan"] != "b plan\n" {
		t.Errorf("new run folder files: %v", s.files)
	}
	for rel, mode := range s.modes {
		want := os.FileMode(0o444)
		if _, isFile := s.files[rel]; !isFile {
			want = 0o555
		}
		if mode != want {
			t.Errorf("%s mode %o, want %o", rel, mode, want)
		}
		if _, isFile := s.files[rel]; isFile && !strings.Contains(prompt, filepath.Join(s.dir, rel)) {
			t.Errorf("the prompt does not name %s", rel)
		}
	}
	if _, err := os.Stat(s.dir); !os.IsNotExist(err) {
		t.Errorf("new run folder %s outlived its run", s.dir)
	}
	return s
}

func TestSweepRemovesAFolderWhoseOwnerExited(t *testing.T) {
	k, d, a, b := twoItems(t)
	left := openRun(t, k, a.ID)
	if m := snapshot(t, left.Dir); m[". mode"] != "-r-xr-xr-x" || m["attachments/plan mode"] != "-r--r--r--" {
		t.Fatalf("leftover is not read-only: %v", m)
	}
	ownedBy(t, left.Dir, exitedProc(t))
	performB(t, k, d, b)
	gone(t, left.Dir)
	gone(t, left.Dir+ownerExt)
	runsEmpty(t, k)
}

func TestSweepRemovesAFolderWithNoOwnerFile(t *testing.T) {
	k, d, _, b := twoItems(t)
	left := filepath.Join(k.Root, runsDir, "sto_old-45118248")
	if err := os.MkdirAll(filepath.Join(left, "attachments"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(left, "attachments", "plan"), []byte("old\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(left, "attachments"), left} {
		if err := os.Chmod(p, 0o555); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = removeRun(left) })
	performB(t, k, d, b)
	gone(t, left)
	runsEmpty(t, k)
}

func TestSweepKeepsAFolderWhoseOwnerIsAlive(t *testing.T) {
	k, d, a, b := twoItems(t)
	mine := openRun(t, k, a.ID)
	other := openRun(t, k, a.ID)
	ownedBy(t, other.Dir, liveProc(t))
	wantMine, wantOther := snapshot(t, mine.Dir), snapshot(t, other.Dir)
	if wantMine["attachments/plan"] != "a plan\n" || wantMine["attachments/plan mode"] != "-r--r--r--" {
		t.Fatalf("fixture: %v", wantMine)
	}
	performB(t, k, d, b)
	sameSnapshot(t, mine.Dir, wantMine)
	sameSnapshot(t, other.Dir, wantOther)
	for _, r := range []Record{mine, other} {
		if _, err := os.Stat(r.Dir + ownerExt); err != nil {
			t.Errorf("owner file of %s: %v", r.Dir, err)
		}
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
	}
	runsEmpty(t, k)
}

func TestSweepKeepsWhatItCannotProbe(t *testing.T) {
	k, d, a, b := twoItems(t)
	unknown := openRun(t, k, a.ID)
	ownedBy(t, unknown.Dir, procgroup.Proc{PID: os.Getpid()})
	bad := openRun(t, k, a.ID)
	setOwner(t, bad.Dir, []byte("not json"))
	orphan := filepath.Join(k.Root, runsDir, "sto_x-1")
	setOwner(t, orphan, []byte("not json"))
	wantUnknown, wantBad := snapshot(t, unknown.Dir), snapshot(t, bad.Dir)
	performB(t, k, d, b)
	sameSnapshot(t, unknown.Dir, wantUnknown)
	sameSnapshot(t, bad.Dir, wantBad)
	if _, err := os.Stat(orphan + ownerExt); err != nil {
		t.Errorf("an unparsable owner file without a folder was removed: %v", err)
	}
}

func TestSweepRemovesAnOwnerFileWhoseOwnerExited(t *testing.T) {
	k, d, _, b := twoItems(t)
	if err := os.MkdirAll(filepath.Join(k.Root, runsDir), 0o755); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(k.Root, runsDir, "sto_x-2")
	ownedBy(t, orphan, exitedProc(t))
	performB(t, k, d, b)
	gone(t, orphan+ownerExt)
	runsEmpty(t, k)
}
