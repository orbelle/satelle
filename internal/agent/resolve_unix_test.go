//go:build !windows

package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProbeDispatchesReadOnlyInATemporaryDirectory(t *testing.T) {
	bin, argvFile, stdinFile := fake(t, "claude", "cat > STDIN\npwd > PWD\necho '{\"result\":\"ok\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}'\n")
	pwdFile := filepath.Join(filepath.Dir(bin), "pwd")
	script := strings.Replace(strings.Replace(read(t, bin), "STDIN", stdinFile, 1), "PWD", pwdFile, 1)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	b := Binding{Name: "coder", Command: bin, Tools: []string{"Read", "Edit", "Write", "Bash"}}
	res, err := Probe(context.Background(), b, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "ok" {
		t.Fatalf("result %+v", res)
	}
	argv := read(t, argvFile)
	if !strings.Contains(argv, "Read,Grep,Glob") || strings.Contains(argv, "Edit") || strings.Contains(argv, "Write,") || strings.Contains(argv, "\nBash\n") {
		t.Fatalf("probe grant is not read-only:\n%s", argv)
	}
	if read(t, stdinFile) != ProbePrompt {
		t.Fatalf("stdin %q", read(t, stdinFile))
	}
	wd := strings.TrimSpace(read(t, pwdFile))
	if !strings.Contains(filepath.Base(wd), "satelle-probe-") {
		t.Fatalf("probe ran in %s", wd)
	}
	if _, err := os.Stat(wd); !os.IsNotExist(err) {
		t.Fatalf("probe directory %s left behind", wd)
	}
}

func TestProbeRefusesABlankAnswerAndAFailure(t *testing.T) {
	blank, _, _ := fake(t, "claude", "echo '{\"result\":\"  \"}'\n")
	if _, err := Probe(context.Background(), Binding{Name: "b", Command: blank, Tools: []string{"Read"}}, 10*time.Second); err == nil || !strings.Contains(err.Error(), "answered with nothing") {
		t.Fatalf("blank answer: %v", err)
	}
	fail, _, _ := fake(t, "claude", "echo 'Not logged in' >&2\nexit 1\n")
	if _, err := Probe(context.Background(), Binding{Name: "b", Command: fail, Tools: []string{"Read"}}, 10*time.Second); err == nil || !strings.Contains(err.Error(), "Not logged in") {
		t.Fatalf("failure: %v", err)
	}
	hang, _, _ := fake(t, "claude", "sleep 5\n")
	start := time.Now()
	if _, err := Probe(context.Background(), Binding{Name: "b", Command: hang, Tools: []string{"Read"}}, 200*time.Millisecond); err == nil {
		t.Fatal("a hung agent answered")
	}
	if d := time.Since(start); d > 4*time.Second {
		t.Fatalf("probe took %s past its timeout", d)
	}
}
