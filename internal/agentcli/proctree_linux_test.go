//go:build linux

package agentcli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/testutil"
)

// startGroup (sty_2d2c4c03) starts `sh -c script` in its own process group and
// returns the command plus an idempotent cleanup that SIGKILLs the whole group
// (so forked subshells and sleeps die with the root), reaps the root, and
// reports any group member still alive. It is also registered with t.Cleanup.
func startGroup(t *testing.T, script string) (*exec.Cmd, func()) {
	t.Helper()
	cmd := exec.Command("sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pgid := cmd.Process.Pid
	done := false
	cleanup := func() {
		if done {
			return
		}
		done = true
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		_ = cmd.Wait()
		if !waitGroupGone(pgid, testutil.WaitBudget) {
			t.Errorf("process group %d still has live members after cleanup", pgid)
		}
	}
	t.Cleanup(cleanup)
	return cmd, cleanup
}

// waitGroupGone polls until no process remains in group pgid (ESRCH).
func waitGroupGone(pgid int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for {
		if syscall.Kill(-pgid, 0) == syscall.ESRCH {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond) // poll tick
	}
}

// groupMembers counts processes in group pgid by scanning /proc/*/stat.
func groupMembers(pgid int) int {
	entries, _ := os.ReadDir("/proc")
	n := 0
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		s := string(b)
		rest := strings.Fields(s[strings.LastIndexByte(s, ')')+1:]) // state ppid pgrp ...
		// A zombie is not live: a sandbox whose pid 1 does not reap leaves one.
		if len(rest) > 2 && rest[0] != "Z" && rest[2] == strconv.Itoa(pgid) {
			n++
		}
	}
	return n
}

// TestProcTreeGroupCleanupLeavesNoDescendants (sty_2d2c4c03): cleaning up a
// grouped process tree leaves no forked descendant behind.
func TestProcTreeGroupCleanupLeavesNoDescendants(t *testing.T) {
	cmd, cleanup := startGroup(t, "(while :; do :; done) & sleep 30 & wait")
	pgid := cmd.Process.Pid
	deadline := time.Now().Add(testutil.WaitBudget)
	for groupMembers(pgid) < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("descendants never appeared (group has %d members)", groupMembers(pgid))
		}
		time.Sleep(20 * time.Millisecond) // poll tick
	}
	cleanup()
	if !waitGroupGone(pgid, testutil.WaitBudget) {
		t.Fatalf("process group %d survived cleanup", pgid)
	}
}

// TestProcTreeCPUAdvancesAndIncludesDescendants (sty_db62a3b9): the CPU time
// of a shell whose busy work runs in a forked child still advances the root's
// tree total.
func TestProcTreeCPUAdvancesAndIncludesDescendants(t *testing.T) {
	cmd, _ := startGroup(t, "(while :; do :; done) & wait")
	pid := cmd.Process.Pid

	first, ok := procTreeCPU(pid)
	if !ok {
		t.Fatal("running root not found in /proc")
	}
	deadline := time.Now().Add(testutil.WaitBudget)
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond) // poll tick
		if cur, ok := procTreeCPU(pid); ok && cur > first {
			return
		}
	}
	t.Fatalf("tree CPU never advanced past %d ticks (descendant not counted?)", first)
}

// TestProcTreeCPUKeepsReapedChildCPU (sty_db62a3b9): CPU burned by a child that
// has already exited and been reaped stays in the tree total, so the total does
// not fall when the child ends (which would make the next sample look like no
// progress). The root shell itself burns almost nothing: the CPU is spent in an
// inner shell that the root runs to completion and reaps before sleeping.
//
// The root touches a marker once it has reaped the busy child, so the test
// samples until that observable event and then once more after it, instead of
// guessing how long a slow machine needs to get the child through its busy loop.
//
// The child burns a fixed amount of CPU, not wall time: it spins in bursts and
// checks its own utime (field 14 of /proc/$$/stat) until it reaches 25 ticks.
// A wall-clock loop on a CPU-starved single core accrues only a fraction of the
// CPU it was meant to, which fell below the 20-tick assertion.
func TestProcTreeCPUKeepsReapedChildCPU(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "reaped")
	busy := `while :; do i=0; while [ $i -lt 20000 ]; do i=$((i+1)); done; set -- $(cat /proc/$$/stat); shift 13; [ "$1" -ge 25 ] && break; done`
	cmd, _ := startGroup(t, "sh -c '"+busy+"'; touch '"+marker+"'; sleep 30")
	pid := cmd.Process.Pid

	var peak uint64
	sample := func() {
		t.Helper()
		cur, ok := procTreeCPU(pid)
		if !ok {
			t.Fatal("running root not found in /proc")
		}
		if cur < peak {
			t.Fatalf("tree CPU dropped from %d to %d ticks after the child was reaped", peak, cur)
		}
		peak = cur
	}
	deadline := time.Now().Add(testutil.WaitBudget)
	for {
		sample()
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the busy child was never reaped")
		}
		time.Sleep(20 * time.Millisecond) // poll tick
	}
	sample() // after the reap: the child's CPU must still be in the total
	if peak < 20 {
		t.Fatalf("only %d ticks observed; the busy child's CPU was not counted", peak)
	}
}

// fakeProcs is an injected /proc: list reports a root (pid 100) and its child
// (pid 101); the child's stat is always gone (reaped). rootStat supplies the
// root's reading for its n-th read (0-based).
func fakeProcs(rootStat func(n int) procStat) (list func() ([]int, error), stat func(int) (procStat, bool), calls *int) {
	calls, reads := new(int), 0
	list = func() ([]int, error) { return []int{100, 101}, nil }
	stat = func(pid int) (procStat, bool) {
		*calls++
		if pid != 100 {
			return procStat{}, false
		}
		reads++
		return rootStat(reads - 1), true
	}
	return list, stat, calls
}

// TestProcTreeCPURetakesTornScan (sty_673ec869): a reap that lands mid-scan (the
// root read before the reap, its child gone by the time it is read) is detected
// and the scan re-taken, so the reaped child's CPU is in the returned total.
func TestProcTreeCPURetakesTornScan(t *testing.T) {
	list, stat, _ := fakeProcs(func(n int) procStat {
		if n == 0 { // first scan read: the child is not reaped yet
			return procStat{ppid: 1, self: 1}
		}
		return procStat{ppid: 1, self: 1, children: 25}
	})
	got, ok := procTreeCPUWith(100, list, stat)
	if !ok || got != 26 {
		t.Fatalf("procTreeCPUWith = %d, %v; want 26, true (the reaped child's 25 ticks included)", got, ok)
	}
}

// TestProcTreeCPUBoundedRescan (sty_673ec869): a tree that reaps on every pass
// still returns, after a bounded number of attempts.
func TestProcTreeCPUBoundedRescan(t *testing.T) {
	list, stat, calls := fakeProcs(func(n int) procStat {
		return procStat{ppid: 1, children: uint64(n)} // grows on every read
	})
	if _, ok := procTreeCPUWith(100, list, stat); !ok {
		t.Fatal("ok = false for a present root")
	}
	// Per attempt: a scan read of each listed pid, then a verify read of each in-tree pid.
	if limit := procTreeScanAttempts * (2 + 1); *calls > limit {
		t.Fatalf("%d stat reads; want at most %d (%d attempts)", *calls, limit, procTreeScanAttempts)
	}
}

// TestProcTreeCPUGoneRoot: a reaped root reports ok=false.
func TestProcTreeCPUGoneRoot(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if _, ok := procTreeCPU(cmd.Process.Pid); ok {
		t.Error("reaped process must report ok=false")
	}
}
