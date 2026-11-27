package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestResolveFindsTheCommandAndEveryPathItNames(t *testing.T) {
	bin := t.TempDir()
	exe := filepath.Join(bin, "claude")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := os.WriteFile(filepath.Join(home, "ext.ts"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "local.ts"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(home, "ext.ts")
	gone := filepath.Join(home, "nonexistent", "x.ts")
	for _, c := range []struct {
		command, want string
	}{
		{"claude", ""},
		{"claude --model x plain-word", ""},
		{"claude --ext " + abs, ""},
		{"claude --ext=" + abs, ""},
		{"claude --ext ./local.ts", ""},
		{"claude --ext ~/ext.ts", ""},
		{"satelle-no-such-agent", `command "satelle-no-such-agent" does not resolve`},
		{"claude --ext " + gone, "path " + strconv.Quote(gone) + " its command names does not exist"},
		{"claude --ext=" + gone, "path " + strconv.Quote(gone) + " its command names does not exist"},
		{"claude --ext ./missing.ts", `path "./missing.ts" its command names does not exist`},
		{"claude --ext ../missing.ts", `path "../missing.ts" its command names does not exist`},
		{"claude --ext ~/missing.ts", `path "~/missing.ts" its command names does not exist`},
		{"", "has no command"},
	} {
		err := Resolve(Binding{Name: "b", Command: c.command}, dir)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%q: %v", c.command, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%q: %v, want %q", c.command, err, c.want)
		}
	}
}
