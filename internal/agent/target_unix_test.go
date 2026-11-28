//go:build !windows

package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/integration"
	"github.com/bobmcallan/satelle/internal/integration/claude"
	"github.com/bobmcallan/satelle/internal/integration/common"
	"github.com/bobmcallan/satelle/internal/integration/cursor"
	"github.com/bobmcallan/satelle/internal/integration/pi"
)

var (
	readGrant  = []string{"Read", "Grep", "Glob"}
	writeGrant = []string{"Read", "Grep", "Glob", "Edit", "Write"}
	bashGrant  = []string{"Read", "Grep", "Glob", "Edit", "Write", "Bash"}
)

func targetDirs(t *testing.T) (worktree, scratch string) {
	t.Helper()
	resolve := func(p string) string {
		r, err := filepath.EvalSymlinks(p)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	return resolve(t.TempDir()), resolve(t.TempDir())
}

func targetReq(b Binding, worktree, scratch string, protected bool) Request {
	return Request{Binding: b, Prompt: "the work", Dir: worktree,
		Target: &Target{Repo: "tgt", Worktree: worktree, Scratch: scratch, Protected: protected}}
}

func shimRan(t *testing.T, argvFile string) bool {
	t.Helper()
	_, err := os.Stat(argvFile)
	return err == nil
}

func TestConfineTargetRejectsWorktree(t *testing.T) {
	wt, scratch := targetDirs(t)
	b := Binding{Name: "w", Command: "claude", Tools: readGrant}
	cases := map[string]Request{
		"empty":      {Binding: b, Dir: wt, Target: &Target{Scratch: scratch}},
		"relative":   {Binding: b, Dir: "tgt", Target: &Target{Worktree: "tgt", Scratch: scratch}},
		"mismatched": {Binding: b, Dir: scratch, Target: &Target{Worktree: wt, Scratch: scratch}},
		"no scratch": {Binding: b, Dir: wt, Target: &Target{Worktree: wt}},
		"equal":      {Binding: b, Dir: wt, Target: &Target{Worktree: wt, Scratch: wt}},
	}
	for name, req := range cases {
		if _, err := ConfineTarget(claude.Dispatch{}, req); err == nil {
			t.Errorf("%s: confined", name)
		}
	}
}

func TestConfineTargetMode(t *testing.T) {
	wt, scratch := targetDirs(t)
	out, err := ConfineTarget(claude.Dispatch{}, targetReq(Binding{Name: "p", Command: "claude", Tools: readGrant}, wt, scratch, false))
	if err != nil {
		t.Fatal(err)
	}
	if out.Dir != scratch || !out.Target.Outside || len(out.Target.ReadDirs) == 0 || out.Target.ReadDirs[0] != wt {
		t.Fatalf("read-only: Dir %s, target %+v", out.Dir, out.Target)
	}
	if !strings.Contains(out.Prompt, "is an empty scratch folder. Read the target work tree at "+wt) {
		t.Fatalf("read-only prompt %q", out.Prompt)
	}
	if out.Target.Environ == nil {
		t.Fatal("no confined environment")
	}
	out, err = ConfineTarget(claude.Dispatch{}, targetReq(Binding{Name: "c", Command: "claude", Tools: bashGrant}, wt, scratch, false))
	if err != nil {
		t.Fatal(err)
	}
	if out.Dir != wt || out.Target.Outside || !strings.Contains(out.Prompt, "Your working directory is the target work tree.") {
		t.Fatalf("writer: Dir %s, target %+v, prompt %q", out.Dir, out.Target, out.Prompt)
	}
	if req := (Request{Binding: Binding{Tools: readGrant}, Dir: "/x"}); true {
		got, err := ConfineTarget(claude.Dispatch{}, req)
		if err != nil || got.Dir != "/x" || got.Target != nil || got.Redact != nil {
			t.Fatalf("self request changed: %+v %v", got, err)
		}
	}
}

func TestConfineTargetRefusesProjectFiles(t *testing.T) {
	for _, p := range integration.ProjectFiles() {
		wt, scratch := targetDirs(t)
		path := filepath.Join(wt, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		cbin, cargv, _ := fake(t, "claude", "cat >/dev/null\necho '{\"result\":\"done\"}'\n")
		ubin, uargv, _ := fake(t, "cursor-agent", "cat >/dev/null\n")
		runs := []struct {
			role Role
			b    Binding
			argv string
		}{
			{Perform, Binding{Name: "c", Command: cbin, Tools: bashGrant}, cargv},
			{Perform, Binding{Name: "p", Command: cbin, Tools: readGrant}, cargv},
			{Judge, Binding{Name: "j", Command: cbin, Tools: readGrant}, cargv},
			{Judge, Binding{Name: "u", Command: ubin, Tools: readGrant}, uargv},
		}
		for _, r := range runs {
			_, err := Runner{}.Dispatch(context.Background(), r.role, targetReq(r.b, wt, scratch, false))
			if err == nil || !strings.Contains(err.Error(), "holds "+p+",") || !strings.Contains(err.Error(), wt) {
				t.Errorf("%s %s with %s: err %v", r.role, r.b.Name, p, err)
			}
			if shimRan(t, r.argv) {
				t.Errorf("%s %s with %s: the shim ran", r.role, r.b.Name, p)
			}
		}
	}
}

func TestConfineTargetFailsClosedOnLstat(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a 0000 directory")
	}
	wt, scratch := targetDirs(t)
	locked := filepath.Join(wt, ".claude")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	bin, argv, _ := fake(t, "claude", "cat >/dev/null\necho '{\"result\":\"done\"}'\n")
	_, err := Runner{}.Dispatch(context.Background(), Judge, targetReq(Binding{Name: "j", Command: bin, Tools: readGrant}, wt, scratch, false))
	if err == nil || !strings.Contains(err.Error(), locked) || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("err %v", err)
	}
	if shimRan(t, argv) {
		t.Fatal("the shim ran")
	}
}

func TestConfineTargetRefusesHarness(t *testing.T) {
	cases := []struct {
		name, refuse string
		grants       [][]string
	}{
		{"agy", "GEMINI.md", [][]string{writeGrant}},
		{"grok", "AGENTS.md", [][]string{writeGrant, bashGrant}},
		{"cursor-agent", "auth.json", [][]string{writeGrant}},
	}
	for _, c := range cases {
		for _, grant := range c.grants {
			wt, scratch := targetDirs(t)
			bin, argv, _ := fake(t, c.name, "cat >/dev/null\n")
			b := Binding{Name: "coder", Command: bin, Tools: grant}
			_, err := Runner{}.Dispatch(context.Background(), Perform, targetReq(b, wt, scratch, false))
			if err == nil || !strings.Contains(err.Error(), " on "+c.name+":") || !strings.Contains(err.Error(), c.refuse) {
				t.Errorf("%s %v: err %v", c.name, grant, err)
			}
			if shimRan(t, argv) {
				t.Errorf("%s %v: the shim ran", c.name, grant)
			}
		}
		wt, scratch := targetDirs(t)
		bin, argv, _ := fake(t, c.name, "cat >/dev/null\n")
		_, _ = Runner{}.Dispatch(context.Background(), Perform, Request{Binding: Binding{Name: "coder", Command: bin, Tools: writeGrant}, Prompt: "p", Dir: wt})
		if !shimRan(t, argv) {
			t.Errorf("%s self writer did not run", c.name)
		}
		bin, argv, _ = fake(t, c.name, "printf '%s' \"$PWD\" > \"${0%/*}/pwd\"\ncat >/dev/null\n")
		_, _ = Runner{}.Dispatch(context.Background(), Judge, targetReq(Binding{Name: "j", Command: bin, Tools: readGrant}, wt, scratch, false))
		if !shimRan(t, argv) {
			t.Errorf("%s read-only target dispatch did not run", c.name)
			continue
		}
		if got := read(t, filepath.Join(filepath.Dir(bin), "pwd")); got != scratch {
			t.Errorf("%s read-only target ran in %s, want %s", c.name, got, scratch)
		}
	}
}

func TestConfineTargetNarrows(t *testing.T) {
	wt, scratch := targetDirs(t)
	for _, a := range []Adapter{claude.Dispatch{}, cursor.Dispatch{}} {
		out, err := ConfineTarget(a, targetReq(Binding{Name: "c", Command: a.Name(), Tools: writeGrant}, wt, scratch, true))
		if err != nil {
			t.Fatalf("%s protected: %v", a.Name(), err)
		}
		if !common.IsReadOnly(out.Binding.Tools) || out.Dir != scratch || !strings.Contains(out.Target.Narrowed, "protected repository") ||
			!strings.Contains(out.Prompt, "read-only for this run") {
			t.Fatalf("%s protected: %+v %+v", a.Name(), out.Binding, out.Target)
		}
	}
	out, err := ConfineTarget(pi.Dispatch{}, targetReq(Binding{Name: "c", Command: "pi", Tools: bashGrant}, wt, scratch, false))
	if err != nil {
		t.Fatal(err)
	}
	if !common.IsReadOnly(out.Binding.Tools) || out.Dir != scratch || !strings.Contains(out.Target.Narrowed, "pi harness record") {
		t.Fatalf("pi: %+v %+v", out.Binding, out.Target)
	}
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-key-value")
	first, err := ConfineTarget(claude.Dispatch{}, targetReq(Binding{Name: "c", Command: "claude", Tools: bashGrant}, wt, scratch, true))
	if err != nil {
		t.Fatal(err)
	}
	second, err := ConfineTarget(claude.Dispatch{}, first)
	if err != nil {
		t.Fatal(err)
	}
	if second.Prompt != first.Prompt || second.Dir != first.Dir || strings.Join(second.Target.Environ, "\n") != strings.Join(first.Target.Environ, "\n") ||
		second.Clean("x anthropic-key-value") != "x [secret]" || first.Clean("x anthropic-key-value") != "x [secret]" {
		t.Fatalf("not idempotent: %q / %q", first.Prompt, second.Prompt)
	}
}
