//go:build unix

package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func gone(pid int) bool {
	if b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil {
		if i := bytes.LastIndexByte(b, ')'); i >= 0 && len(b) > i+2 && b[i+2] == 'Z' {
			return true
		}
	}
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the fixture child did not record its pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatalf("bad pid %q: %v", b, err)
	}
	return pid
}

func TestStallReportsHungTestByName(t *testing.T) {
	dir := t.TempDir()
	fixture := filepath.Join(dir, "events.json")
	if err := os.WriteFile(fixture, []byte(ev("run", "TestHang", "")), 0o644); err != nil {
		t.Fatal(err)
	}
	childPID, grandPID := filepath.Join(dir, "child.pid"), filepath.Join(dir, "grand.pid")
	script := `cat "$1"; echo $$ > "$2"; sleep 300 & echo $! > "$3"; wait`
	args := []string{"-stall", "200ms", "--", "sh", "-c", script, "sh", fixture, childPID, grandPID}

	var out, errb bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- run(args, &out, &errb) }()
	var got int
	select {
	case got = <-done:
	case <-time.After(20 * time.Second):
		for _, p := range []string{childPID, grandPID} {
			if b, err := os.ReadFile(p); err == nil {
				if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			}
		}
		t.Fatal("run did not return: the stalled test was not killed")
	}

	if got == 0 {
		t.Fatalf("exit 0, want non-zero for a stalled test:\n%s", errb.String())
	}
	for _, want := range []string{pkg, "TestHang", "stalled"} {
		if !strings.Contains(errb.String(), want) {
			t.Errorf("report is missing %q:\n%s", want, errb.String())
		}
	}
	for name, path := range map[string]string{"child": childPID, "grandchild": grandPID} {
		pid := readPID(t, path)
		deadline := time.Now().Add(5 * time.Second)
		for !gone(pid) {
			if time.Now().After(deadline) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
				t.Fatalf("the %s (pid %d) is still running: the process group was not killed", name, pid)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}
