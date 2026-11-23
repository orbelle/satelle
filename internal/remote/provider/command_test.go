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
  network)
    cat > "$out/network.stdin"
    if [ -f "$out/network.report" ]; then cat "$out/network.report"; else cat "$out/network.stdin"; fi ;;
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
		Check:   []string{bin, "check"},
		Start:   []string{bin, "start", "{node}"},
		Network: []string{bin, "network", "{node}"},
		CopyIn:  []string{bin, "copy_in", "{node}:{dst}", "src={src}"},
		Exec:    []string{bin, "exec", "{node}"},
		Stop:    []string{bin, "stop", "{node}"},
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
	if got := argvOf(t, dir); got != "argc 2\narg start\narg nod_1\nargc 2\narg network\narg nod_1\n" {
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

func TestCommandCallerArgvIsNotSubstituted(t *testing.T) {
	c, dir := recordingConnect(t)
	h, err := Command{}.Start(context.Background(), remotedef.Definition{Connect: c}, Run{NodeID: "nod_6"})
	if err != nil {
		t.Fatal(err)
	}
	argvOf(t, dir)
	if _, err := h.Exec(context.Background(), "true", "{node}", "{src}-{dst}"); err != nil {
		t.Fatal(err)
	}
	if got := argvOf(t, dir); got != "argc 5\narg exec\narg nod_6\narg true\narg {node}\narg {src}-{dst}\n" {
		t.Fatalf("exec argv:\n%s", got)
	}
	if _, err := h.(StdinExec).ExecStdin(context.Background(), strings.NewReader(""), "true", "{node}"); err != nil {
		t.Fatal(err)
	}
	if got := argvOf(t, dir); got != "argc 4\narg exec\narg nod_6\narg true\narg {node}\n" {
		t.Fatalf("exec-stdin argv:\n%s", got)
	}
	src := filepath.Join(t.TempDir(), "{node}")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "f"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "back")
	if err := h.CopyOut(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "f")); string(b) != "y" {
		t.Fatalf("copied out %q from a path holding {node}", b)
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

func TestCommandNetworkGetsTheAllowList(t *testing.T) {
	for name, tc := range map[string]struct {
		allow []string
		stdin string
	}{
		"allow":    {[]string{"api.example.com", "*.pkg.dev"}, "api.example.com\n*.pkg.dev\n"},
		"deny-all": {nil, ""},
	} {
		t.Run(name, func(t *testing.T) {
			c, dir := recordingConnect(t)
			def := remotedef.Definition{Connect: c, Network: remotedef.Network{Allow: tc.allow}}
			if _, err := (Command{}).Start(context.Background(), def, Run{NodeID: "nod_n"}); err != nil {
				t.Fatal(err)
			}
			if got := argvOf(t, dir); got != "argc 2\narg start\narg nod_n\nargc 2\narg network\narg nod_n\n" {
				t.Fatalf("argv after start:\n%s", got)
			}
			b, err := os.ReadFile(filepath.Join(dir, "network.stdin"))
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != tc.stdin {
				t.Fatalf("network stdin = %q, want %q", b, tc.stdin)
			}
		})
	}
}

func TestCommandNetworkReadBack(t *testing.T) {
	for name, tc := range map[string]struct {
		allow  []string
		report string
		fail   bool
		want   []string
	}{
		"equal as a set":   {allow: []string{"api.example.com", "*.pkg.dev"}, report: "*.pkg.dev\r\n\n  api.example.com\napi.example.com\n"},
		"deny-all applied": {report: "\n"},
		"other host":       {allow: []string{"api.example.com"}, report: "evil.example.com\n", fail: true, want: []string{"the node nod_r allows [evil.example.com]", "[network] allow is [api.example.com]"}},
		"open on deny-all": {report: "evil.example.com\n", fail: true, want: []string{"allows [evil.example.com]", "allow is [] (deny-all)"}},
		"nothing applied":  {allow: []string{"api.example.com"}, report: "", fail: true, want: []string{"allows [] (deny-all)", "allow is [api.example.com]"}},
	} {
		t.Run(name, func(t *testing.T) {
			c, dir := recordingConnect(t)
			if err := os.WriteFile(filepath.Join(dir, "network.report"), []byte(tc.report), 0o644); err != nil {
				t.Fatal(err)
			}
			def := remotedef.Definition{Connect: c, Network: remotedef.Network{Allow: tc.allow}}
			h, err := Command{}.Start(context.Background(), def, Run{NodeID: "nod_r"})
			if h == nil {
				t.Fatal("Start returned no handle to stop")
			}
			if (err != nil) != tc.fail {
				t.Fatalf("Start = %v, want failure %v", err, tc.fail)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Fatalf("error %q does not name %q", err, w)
				}
			}
		})
	}
	t.Run("network command fails", func(t *testing.T) {
		c, _ := recordingConnect(t)
		c.Network = []string{"sh", "-c", "echo refused >&2; exit 4"}
		_, err := Command{}.Start(context.Background(), remotedef.Definition{Connect: c}, Run{NodeID: "nod_f"})
		if err == nil || !strings.Contains(err.Error(), "[connect] network sh -c") || !strings.Contains(err.Error(), "failed: exit status 4") || !strings.Contains(err.Error(), "refused") {
			t.Fatalf("Start = %v, want the network command's failure", err)
		}
	})
}
