//go:build !windows

package provider

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/remotedef"
)

const recorder = `#!/bin/sh
out='@OUT@'
echo "argc $#" >> "$out/argv"
for a; do echo "arg $a" >> "$out/argv"; done
env > "$out/env"
cmd=$1; shift
case "$cmd" in
  exec) shift; exec "$@" ;;
esac
exit 0
`

func recordingConnect(t *testing.T) (remotedef.Connect, string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "rec")
	if err := os.WriteFile(bin, []byte(strings.ReplaceAll(recorder, "@OUT@", dir)), 0o755); err != nil {
		t.Fatal(err)
	}
	return remotedef.Connect{
		Check:  []string{bin, "check"},
		Start:  []string{bin, "start", "{node}"},
		CopyIn: []string{bin, "copy_in", "{node}:{dst}", "src={src}"},
		Exec:   []string{bin, "exec", "{node}"},
		Stop:   []string{bin, "stop", "{node}"},
	}, dir
}

func argvOf(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "argv"))
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(dir, "argv"))
	return string(b)
}

func TestForCommand(t *testing.T) {
	p, err := For(remotedef.ProviderCommand)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(Command); !ok || p.Name() != remotedef.ProviderCommand {
		t.Fatalf("For(command) = %T", p)
	}
}

func TestCommandSubstitutesEachElement(t *testing.T) {
	c, dir := recordingConnect(t)
	ctx := context.Background()
	h, err := Command{}.Start(ctx, remotedef.Definition{Connect: c}, Run{NodeID: "nod_1"})
	if err != nil {
		t.Fatal(err)
	}
	if got := argvOf(t, dir); got != "argc 2\narg start\narg nod_1\n" {
		t.Fatalf("start argv:\n%s", got)
	}
	if err := h.CopyIn(ctx, "/host/a b", "/node/in"); err != nil {
		t.Fatal(err)
	}
	if got := argvOf(t, dir); got != "argc 3\narg copy_in\narg nod_1:/node/in\narg src=/host/a b\n" {
		t.Fatalf("copy_in argv:\n%s", got)
	}
	if err := h.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if got := argvOf(t, dir); got != "argc 2\narg stop\narg nod_1\n" {
		t.Fatalf("stop argv:\n%s", got)
	}
	if hint := RemoveHint(h); hint != filepath.Join(dir, "rec")+" stop nod_1" {
		t.Fatalf("hint = %q", hint)
	}
}

func TestCommandRunsNoShell(t *testing.T) {
	c, dir := recordingConnect(t)
	t.Setenv("SATELLE_COMMAND_TEST_ENV", "from-the-firing-shell")
	h, err := Command{}.Start(context.Background(), remotedef.Definition{Connect: c}, Run{NodeID: "nod_2"})
	if err != nil {
		t.Fatal(err)
	}
	argvOf(t, dir)
	literal := "a b; $(echo x) `echo y` > z"
	out, err := h.Exec(context.Background(), "printf", "%s", literal)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != literal {
		t.Fatalf("exec output = %q, want the argument literally", out)
	}
	if got := argvOf(t, dir); got != "argc 5\narg exec\narg nod_2\narg printf\narg %s\narg "+literal+"\n" {
		t.Fatalf("exec argv:\n%s", got)
	}
	env, err := os.ReadFile(filepath.Join(dir, "env"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(env), "SATELLE_COMMAND_TEST_ENV=from-the-firing-shell\n") ||
		!strings.Contains(string(env), "PATH="+os.Getenv("PATH")+"\n") {
		t.Fatalf("the command did not run with the firing environment:\n%s", env)
	}
}

func TestCommandCopyOut(t *testing.T) {
	c, _ := recordingConnect(t)
	h, err := Command{}.Start(context.Background(), remotedef.Definition{Connect: c}, Run{NodeID: "nod_3"})
	if err != nil {
		t.Fatal(err)
	}
	src, dst := t.TempDir(), t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(src, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("outcome.json", `{"verdict":"accept"}`)
	write("ledger/a.jsonl", "one\n")
	write("ledger/sub/b.jsonl", "two\n")
	if err := h.CopyOut(context.Background(), filepath.Join(src, "outcome.json"), filepath.Join(dst, "got.json")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "got.json")); string(b) != `{"verdict":"accept"}` {
		t.Fatalf("file copied out = %q", b)
	}
	if err := h.CopyOut(context.Background(), filepath.Join(src, "ledger"), filepath.Join(dst, "back")); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]string{"back/a.jsonl": "one\n", "back/sub/b.jsonl": "two\n"} {
		if b, _ := os.ReadFile(filepath.Join(dst, rel)); string(b) != want {
			t.Fatalf("%s = %q, want %q", rel, b, want)
		}
	}
	if err := h.CopyOut(context.Background(), filepath.Join(src, "missing"), filepath.Join(dst, "missing")); err == nil {
		t.Fatal("a missing node path copied out")
	}
}

func TestCommandCopyOutRefusesAnEscape(t *testing.T) {
	for name, entry := range map[string]string{"dotdot": "../x", "absolute": "/etc/x", "sibling": "other/x", "inner dotdot": "ledger/../../x"} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)
			if err := tw.WriteHeader(&tar.Header{Name: entry, Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			archive := filepath.Join(t.TempDir(), "evil.tar")
			if err := os.WriteFile(archive, buf.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
			h := &commandNode{c: remotedef.Connect{Exec: []string{"sh", "-c", `cat "$0"`, archive}}, node: "nod_4"}
			dst := t.TempDir()
			err := h.CopyOut(context.Background(), "/node/runtime/ledger", filepath.Join(dst, "ledger"))
			if err == nil || !strings.Contains(err.Error(), "outside ledger") {
				t.Fatalf("CopyOut of %q = %v, want a refusal", entry, err)
			}
			if entries, _ := os.ReadDir(dst); len(entries) != 0 {
				t.Fatalf("CopyOut wrote %v", entries)
			}
		})
	}
}

func TestCommandCheckNamesTheCommandAndItsTail(t *testing.T) {
	def := remotedef.Definition{Connect: remotedef.Connect{Check: []string{"sh", "-c", "head -c 5000 /dev/zero | tr '\\0' x; echo; echo END; exit 3"}}}
	err := Command{}.Check(context.Background(), def, "nod_5")
	if err == nil {
		t.Fatal("a failing check passed")
	}
	head, tail, _ := strings.Cut(err.Error(), "\n")
	if !strings.HasPrefix(head, `[connect] check sh -c "head -c 5000`) || !strings.HasSuffix(head, "failed: exit status 3") {
		t.Fatalf("error head = %q", head)
	}
	if !strings.HasSuffix(tail, "END") || len(tail) > commandTail {
		t.Fatalf("error tail is %d bytes, ending %q", len(tail), tail[max(0, len(tail)-10):])
	}
	if err := (Command{}).Check(context.Background(), remotedef.Definition{}, "nod_5"); err != nil {
		t.Fatalf("no check command: %v", err)
	}
}
