//go:build !windows

package remotenode

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Each setup command runs once, in order, in dir, through sh, with no stdin.
func TestRunSetupRunsEachCommandInOrder(t *testing.T) {
	dir := t.TempDir()
	cmds := []string{
		"echo first >> order; cat > stdin-1",
		"echo second >> order; pwd > cwd",
	}
	if err := RunSetup(context.Background(), dir, SetupOnCreate, cmds, nil, time.Minute, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "order")); string(got) != "first\nsecond\n" {
		t.Errorf("order = %q, want each command once, in order", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "stdin-1")); len(got) != 0 {
		t.Errorf("a command read stdin %q, want none", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "cwd")); strings.TrimSpace(string(got)) == "" {
		t.Error("the second command did not run in dir")
	}
}

// A failing command stops the phase at its 1-based index; later ones never run.
func TestRunSetupStopsAtTheFailingCommand(t *testing.T) {
	dir := t.TempDir()
	cmds := []string{"echo one >> ran", "exit 3", "echo three >> ran"}
	err := RunSetup(context.Background(), dir, SetupPostStart, cmds, nil, time.Minute, nil)
	var se *SetupError
	if !errors.As(err, &se) || se.Phase != SetupPostStart || se.Index != 2 || se.Reason != "exit status 3" {
		t.Fatalf("err = %v, want SetupError post_start 2 with exit status 3", err)
	}
	if err.Error() != "setup post_start command 2: exit status 3" {
		t.Errorf("err = %q", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "ran")); string(got) != "one\n" {
		t.Errorf("ran = %q, want only the first command", got)
	}
}

func TestRunSetupTimesOut(t *testing.T) {
	start := time.Now()
	err := RunSetup(context.Background(), t.TempDir(), SetupOnCreate, []string{"sleep 5"}, nil, 50*time.Millisecond, nil)
	var se *SetupError
	if !errors.As(err, &se) || se.Index != 1 || !strings.HasPrefix(se.Reason, "timed out") {
		t.Fatalf("err = %v, want a timeout at command 1", err)
	}
	if time.Since(start) > 4*time.Second {
		t.Error("the timeout did not bound the command")
	}
}

// The error carries neither the command, nor its output, nor the exec error.
func TestSetupErrorHoldsNeitherCommandNorOutput(t *testing.T) {
	var log bytes.Buffer
	for _, c := range []string{
		"echo OUT-SENTINEL; echo ERR-SENTINEL >&2; exit 1 # ARGV-SENTINEL",
		"/nonexistent/ARGV-SENTINEL",
		" ",
	} {
		err := RunSetup(context.Background(), t.TempDir(), SetupOnCreate, []string{c}, nil, time.Minute, &log)
		if err == nil {
			t.Fatalf("%q: want a failure", c)
		}
		for _, s := range []string{"SENTINEL", "/nonexistent", "echo"} {
			if strings.Contains(err.Error(), s) {
				t.Errorf("%q: error %q holds %q", c, err, s)
			}
		}
	}
	if !strings.Contains(log.String(), "OUT-SENTINEL") || !strings.Contains(log.String(), "ERR-SENTINEL") {
		t.Errorf("the command's output did not go to the node log: %q", log.String())
	}
}

func TestRunSetupWithNoCommandsRunsNothing(t *testing.T) {
	for _, cmds := range [][]string{nil, {}} {
		if err := RunSetup(context.Background(), t.TempDir(), SetupOnCreate, cmds, nil, time.Minute, nil); err != nil {
			t.Errorf("RunSetup(%v) = %v, want nil", cmds, err)
		}
	}
}
