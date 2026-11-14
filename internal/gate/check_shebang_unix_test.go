//go:build !windows

package gate

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// sto_cb34e3e3 AC3: many #! checks run at once while other goroutines fork
// continuously, and none fails with "text file busy".
func TestShebangChecksRunConcurrentlyUnderForkLoad(t *testing.T) {
	stop := make(chan struct{})
	var forks sync.WaitGroup
	for range 8 {
		forks.Add(1)
		go func() {
			defer forks.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = exec.Command("true").Run()
				}
			}
		}()
	}
	defer func() { close(stop); forks.Wait() }()

	dir := t.TempDir()
	script := "#!/bin/sh\nexit 0\n"
	for round := range 20 {
		var wg sync.WaitGroup
		errs := make(chan error, 64)
		for range 64 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				code, out, err := RunCheckCode(context.Background(), script, dir, CheckEnv{}, 0)
				switch {
				case errors.Is(err, syscall.ETXTBSY):
					errs <- errors.New("text file busy: " + err.Error())
				case err != nil:
					errs <- err
				case code != 0:
					errs <- errors.New("exit " + strings.TrimSpace(out))
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("round %d: %v", round, err)
		}
	}
}

// sto_cb34e3e3 AC1: a #! script is read by its interpreter, never executed as
// a file: it has no execute bit, and it still gets its stdin payload.
func TestShebangCheckIsReadNotExecuted(t *testing.T) {
	script := "#!/bin/sh\nif [ -x \"$0\" ]; then echo executable; else echo read-only; fi\ncat\n"
	ok, notes, err := RunCheck(context.Background(), script, t.TempDir(), CheckEnv{Item: "itm_1"}, 0)
	if !ok || err != nil {
		t.Fatalf("ok %v err %v notes %q", ok, err, notes)
	}
	if !strings.Contains(notes, "read-only") || strings.Contains(notes, "executable") || !strings.Contains(notes, `"id":"itm_1"`) {
		t.Errorf("notes %q: want read-only and the payload", notes)
	}
}

// The one #! rule (Linux binfmt_script): the interpreter, then at most one
// argument, untouched.
func TestScriptInterpreter(t *testing.T) {
	for _, c := range []struct {
		script string
		want   []string
		ok     bool
		err    bool
	}{
		{"#!/bin/sh\necho hi\n", []string{"/bin/sh"}, true, false},
		{"#!/usr/bin/env bash\n", []string{"/usr/bin/env", "bash"}, true, false},
		{"#! /bin/sh -e \n", []string{"/bin/sh", "-e"}, true, false},
		{"#!/usr/bin/env -S bash -eu\n", []string{"/usr/bin/env", "-S bash -eu"}, true, false},
		{"#!/bin/sh", []string{"/bin/sh"}, true, false},
		{"#!sh\n", []string{"./sh"}, true, false},
		{"#!\necho\n", nil, true, true},
		{"#!   \t\n", nil, true, true},
		{"echo hi\n", nil, false, false},
	} {
		got, ok, err := ScriptInterpreter(c.script)
		if ok != c.ok || (err != nil) != c.err || !slices.Equal(got, c.want) {
			t.Errorf("%q: %v %v %v, want %v %v err=%v", c.script, got, ok, err, c.want, c.ok, c.err)
		}
	}
}

// A #! line naming no interpreter cannot start: code -1 and an error.
func TestRunCheckRefusesEmptyShebang(t *testing.T) {
	code, _, err := RunCheckCode(context.Background(), "#!\necho hi\n", t.TempDir(), CheckEnv{}, 0)
	if code != -1 || err == nil || !strings.Contains(err.Error(), "names no interpreter") {
		t.Errorf("code %d err %v", code, err)
	}
}
