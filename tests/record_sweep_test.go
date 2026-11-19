//go:build integration && (linux || darwin)

package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func fakeHanger(t *testing.T, s *sandbox) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "claude"), `#!/bin/sh
cat > "$FAKE_DIR/prompt.$$"
echo $$ > "$FAKE_DIR/pid.tmp" && mv "$FAKE_DIR/pid.tmp" "$FAKE_DIR/pid"
if [ -e "$FAKE_DIR/hang" ]; then touch "$FAKE_DIR/started"; exec sleep 300; fi
printf '{"type":"result","subtype":"success","is_error":false,"result":"{\\"verdict\\":\\"accept\\",\\"notes\\":\\"ok\\"}","usage":{"input_tokens":3,"output_tokens":2}}\n'
`, 0o755)
	s.path = append(s.path, dir)
	s.env["FAKE_DIR"] = dir
	return dir
}

func recordFolder(t *testing.T, prompt string) string {
	t.Helper()
	const lead = "The item's record is in the read-only folder "
	i := strings.Index(prompt, lead)
	if i < 0 {
		t.Fatalf("the prompt names no record folder:\n%s", prompt)
	}
	rest := prompt[i+len(lead):]
	j := strings.Index(rest, ":\n")
	if j < 0 {
		t.Fatalf("record lead line is cut: %q", rest)
	}
	return rest[:j]
}

func killPid(fake string) {
	b, err := os.ReadFile(filepath.Join(fake, "pid"))
	if err != nil {
		return
	}
	if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

func TestAKilledRunsFolderIsRemovedByTheNextRun(t *testing.T) {
	t.Parallel()
	s := newSandbox(t)
	fake := fakeHanger(t, s)
	s.must("init")
	s.write(".satelle/agents.toml", "[agents.reviewer]\ncommand = \"claude\"\nmodel = \"rv-1\"\ntools = [\"Read\"]\n")
	s.write(".satelle/workflows/planned.toml", plannedWorkflow)
	s.write(".satelle/skills/judge.md", "---\nname: judge\n---\nJudge the plan against the criteria.\n")
	s.must("validate")
	runs := filepath.Join(s.dir, ".satelle", "runs")

	story := func(title string) string {
		id := s.create("story", "--title", title, "--workflow", "planned", "--criteria", "AC1: keep the ledger").ID
		if r := s.exec(satelleBin, "the plan\n", "story", "attach", id, "--name", "plan", "--file", "-"); r.code != 0 {
			t.Fatalf("attach: %s%s", r.out, r.err)
		}
		return id
	}
	id1 := story("killed")
	writeFile(t, filepath.Join(fake, "hang"), "", 0o644)

	cmd := exec.Command(satelleBin, "story", "set", id1, "--status", "in_progress")
	cmd.Dir = s.dir
	cmd.Env = s.environ()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		killPid(fake)
		filepath.Walk(runs, func(p string, info os.FileInfo, err error) error {
			if err == nil && info.IsDir() {
				_ = os.Chmod(p, 0o755)
			}
			return nil
		})
	})
	deadline := time.Now().Add(60 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(fake, "started")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the judge never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	pid, _ := os.ReadFile(filepath.Join(fake, "pid"))
	prompt, err := os.ReadFile(filepath.Join(fake, "prompt."+strings.TrimSpace(string(pid))))
	if err != nil {
		t.Fatal(err)
	}
	killed := recordFolder(t, string(prompt))
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("the killed run exited cleanly")
	}
	killPid(fake)

	if !strings.HasPrefix(killed, filepath.Join(runs, id1+"-")) {
		t.Fatalf("killed run's folder %s is not under %s", killed, runs)
	}
	if info, err := os.Stat(killed); err != nil || info.Mode().Perm() != 0o555 {
		t.Fatalf("precondition: the killed run's folder is not left read-only: %v %v", info, err)
	}
	if info, err := os.Stat(filepath.Join(killed, "attachments", "plan")); err != nil || info.Mode().Perm() != 0o444 {
		t.Fatalf("precondition: the killed run's plan copy: %v %v", info, err)
	}
	if _, err := os.Stat(killed + ".owner"); err != nil {
		t.Fatalf("precondition: the killed run's owner file: %v", err)
	}

	if err := os.Remove(filepath.Join(fake, "hang")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(fake, "pid")); err != nil {
		t.Fatal(err)
	}
	id2 := story("next")
	s.must("story", "set", id2, "--status", "in_progress")

	if _, err := os.Lstat(killed); !os.IsNotExist(err) {
		t.Errorf("the killed run's folder %s is still there: %v", killed, err)
	}
	if _, err := os.Lstat(killed + ".owner"); !os.IsNotExist(err) {
		t.Errorf("the killed run's owner file is still there: %v", err)
	}
	pid2, _ := os.ReadFile(filepath.Join(fake, "pid"))
	prompt2, err := os.ReadFile(filepath.Join(fake, "prompt."+strings.TrimSpace(string(pid2))))
	if err != nil {
		t.Fatal(err)
	}
	if next := recordFolder(t, string(prompt2)); !strings.HasPrefix(next, filepath.Join(runs, id2+"-")) {
		t.Errorf("the next run's folder %s is not under %s", next, filepath.Join(runs, id2+"-"))
	}
	if got := s.get(id2).Status; got != "in_progress" {
		t.Errorf("status = %s", got)
	}
	if entries, err := os.ReadDir(runs); err != nil || len(entries) != 0 {
		t.Errorf(".satelle/runs after the next run: %v %v", entries, err)
	}
}
