//go:build !windows

package procgroup

import (
	"bufio"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestKillReachesGrandchildren(t *testing.T) {
	cmd := exec.Command("sh", "-c", "sleep 300 & echo $!; wait")
	Set(cmd)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	grandchild, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("grandchild pid %q: %v", line, err)
	}

	if err := Kill(cmd); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()

	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(grandchild, 0) == nil {
		if time.Now().After(deadline) {
			_ = syscall.Kill(grandchild, syscall.SIGKILL)
			t.Fatalf("grandchild %d survived Kill", grandchild)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestKillBeforeStartIsHarmless(t *testing.T) {
	if err := Kill(exec.Command("true")); err != nil {
		t.Errorf("Kill on a command that never started = %v", err)
	}
}
