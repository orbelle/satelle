//go:build !windows

package gate

import (
	"context"
	"slices"
	"strings"
	"testing"
)

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
