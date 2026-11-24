//go:build unix

package remotenode

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const detachHelperEnv = "SATELLE_TEST_START_DETACHED"

func TestStartDetachedRunHelperProcess(t *testing.T) {
	if os.Getenv(detachHelperEnv) != "1" {
		return
	}
	err := StartDetachedRun(StartDetachedOptions{Dir: os.Getenv("DETACH_DIR"), Self: os.Getenv("DETACH_SELF"),
		Stdin: os.Stdin, Out: os.Stdout})
	if err != nil {
		os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
	os.Exit(0)
}

func selfScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "satelle")
	if err := os.WriteFile(p, []byte("#!/bin/sh\ndir=$4\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func waitFor(p string, within time.Duration) bool {
	for end := time.Now().Add(within); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

func sessionOf(pid int) string {
	out, err := exec.Command("ps", "-o", "sid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

var startedLine = regexp.MustCompile(`node run started detached as process (\d+)`)

func TestStartDetachedRunHandsStdinAndReturns(t *testing.T) {
	dir := t.TempDir()
	self := selfScript(t, `while [ ! -f "$dir/release" ]; do sleep 0.05; done
cat > "$dir/out/got"
ps -o sid= -p $$ > "$dir/out/sid" 2>/dev/null
exit 3
`)
	payload := []byte(`{"S":"the-value-handed-on-stdin"}`)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStartDetachedRunHelperProcess$")
	cmd.Env = append(os.Environ(), detachHelperEnv+"=1", "DETACH_DIR="+dir, "DETACH_SELF="+self)
	cmd.Stdin = bytes.NewReader(payload)
	cmd.WaitDelay = 3 * time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	began := time.Now()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("the starting process = %v (after %s)\n%s%s", err, time.Since(began), out, stderr.String())
	}
	m := startedLine.FindStringSubmatch(string(out))
	if m == nil {
		t.Fatalf("the starting process printed:\n%s", out)
	}
	pid, _ := strconv.Atoi(m[1])
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(dir, "release"), nil, 0o644)
		waitFor(filepath.Join(dir, "out", detachedExit), 10*time.Second)
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	})
	if _, err := os.Stat(filepath.Join(dir, "out", detachedExit)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the run ended before it was released: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if !waitFor(filepath.Join(dir, "out", detachedExit), 10*time.Second) {
		t.Fatal("the detached run wrote no exit status")
	}
	time.Sleep(50 * time.Millisecond)
	if b, err := os.ReadFile(filepath.Join(dir, "out", detachedExit)); err != nil || strings.TrimSpace(string(b)) != "3" {
		t.Fatalf("the exit file = %q, %v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "out", "got")); err != nil || !bytes.Equal(b, payload) {
		t.Fatalf("the run's stdin = %q, %v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "out", "sid")); err == nil {
		if sid, own := strings.TrimSpace(string(b)), sessionOf(os.Getpid()); sid != "" && own != "" && sid == own {
			t.Fatalf("the run is in the test's session %s", own)
		}
	}
}

func TestStartDetachedRunRefusesAnOversizedPayload(t *testing.T) {
	dir := t.TempDir()
	self := selfScript(t, "exit 0\n")
	err := StartDetachedRun(StartDetachedOptions{Dir: dir, Self: self,
		Stdin: bytes.NewReader(bytes.Repeat([]byte("x"), maxSecretPayload+1))})
	if err == nil || !strings.Contains(err.Error(), "over") {
		t.Fatalf("StartDetachedRun with an oversized payload = %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "out", detachedExit)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a child was started: %v", err)
	}
}

func TestStartDetachedRunFailsWhenTheChildDiesAtOnce(t *testing.T) {
	dir := t.TempDir()
	self := selfScript(t, "exit 0\n")
	done := make(chan error, 1)
	go func() {
		done <- StartDetachedRun(StartDetachedOptions{Dir: dir, Self: self,
			Stdin: bytes.NewReader(bytes.Repeat([]byte("x"), 128<<10))})
	}()
	select {
	case err := <-done:
		t.Logf("StartDetachedRun = %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("StartDetachedRun blocked writing to a child that is gone")
	}
	waitFor(filepath.Join(dir, "out", detachedExit), 5*time.Second)
}
