package remotenode

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/remote/provider"
)

const plainImage = "debian:stable-slim"

const plainVersion = "satelle 9.9.9 (commit x, built y)"

func hostUname(t *testing.T) string {
	t.Helper()
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	}
	t.Skipf("no linux node architecture for %s", runtime.GOARCH)
	return ""
}

func otherUname(t *testing.T) string {
	if hostUname(t) == "x86_64" {
		return "aarch64"
	}
	return "x86_64"
}

func testBinary(t *testing.T) string {
	t.Helper()
	p, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func plainDocker(t *testing.T, uname, version string, cpCode int) (script, log string) {
	t.Helper()
	return fakeDockerFor(t, `case "$1" in
  exec)
    if [ "$2" = -i ]; then exit 0; fi
    case "$3" in
      uname) echo '`+uname+`'; exit 0 ;;
      satelle) echo '`+version+`'; exit 0 ;;
    esac
    exit 0 ;;
  cp)
    case "$3" in *:`+NodeSatellePath+`) exit `+strconv.Itoa(cpCode)+` ;; esac
    case "$2" in satelle-node-*) exit 1 ;; esac
    exit 0 ;;
  *) exit 0 ;;
esac
`)
}

func plainFire(t *testing.T, f taskFixture, image, binary, script string) FireOptions {
	t.Helper()
	o := f.def
	o.Definition.Machine.Image = image
	o.Docker, o.Runs = provider.DockerLocal{Docker: script}, LocalRuns{Dir: t.TempDir()}
	o.Binary, o.BinaryVersion = binary, plainVersion
	return o
}

func logLines(t *testing.T, log string) []string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func lineIndex(lines []string, match func(string) bool) int {
	for i, l := range lines {
		if match(l) {
			return i
		}
	}
	return -1
}

func TestLinuxArch(t *testing.T) {
	hostUname(t)
	if got, err := linuxArch(testBinary(t)); err != nil || got != runtime.GOARCH {
		t.Fatalf("linuxArch(test binary) = %q, %v; want %s", got, err, runtime.GOARCH)
	}
	sh := filepath.Join(t.TempDir(), "satelle")
	if err := os.WriteFile(sh, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := linuxArch(sh); err == nil || !strings.Contains(err.Error(), "not a linux binary") {
		t.Fatalf("linuxArch(a shell script) = %v", err)
	}
	if _, err := linuxArch(""); err == nil || !strings.Contains(err.Error(), "not a linux binary") {
		t.Fatalf("linuxArch(\"\") = %v", err)
	}
	for in, want := range map[string]string{"x86_64\n": "amd64", "aarch64": "arm64", "arm64": "arm64"} {
		if got, ok := nodeArch(in); !ok || got != want {
			t.Errorf("nodeArch(%q) = %q, %v", in, got, ok)
		}
	}
	if _, ok := nodeArch("riscv64"); ok {
		t.Error("nodeArch(riscv64) matched")
	}
}

func TestFireLocalGivesAPlainImageSatelleBeforeSetup(t *testing.T) {
	f := newTaskFixture(t, "echo checked")
	bin := testBinary(t)
	script, log := plainDocker(t, hostUname(t), plainVersion, 0)
	_, _ = FireLocal(context.Background(), plainFire(t, f, plainImage, bin, script))
	lines := logLines(t, log)
	cp := lineIndex(lines, func(l string) bool {
		return strings.HasPrefix(l, "cp "+bin+" satelle-node-") && strings.HasSuffix(l, ":"+NodeSatellePath)
	})
	version := lineIndex(lines, func(l string) bool {
		return strings.HasPrefix(l, "exec satelle-node-") && strings.HasSuffix(l, " satelle --version")
	})
	node := lineIndex(lines, func(l string) bool {
		return strings.HasPrefix(l, "exec -i satelle-node-") && strings.Contains(l, " satelle node run-local")
	})
	if cp < 0 || version < 0 || node < 0 || !(cp < version && version < node) {
		t.Fatalf("satelle was not copied and checked before the node ran its setup (cp %d, --version %d, run-local %d):\n%s", cp, version, node, strings.Join(lines, "\n"))
	}

	f2 := newTaskFixture(t, "echo checked")
	script, log = plainDocker(t, hostUname(t), plainVersion, 0)
	o := plainFire(t, f2, "", bin, script)
	_, _ = FireLocal(context.Background(), o)
	for _, l := range logLines(t, log) {
		if strings.Contains(l, NodeSatellePath) || strings.Contains(l, "uname") {
			t.Errorf("a satelle-node:local fire copied or checked satelle: %s", l)
		}
	}
}

func TestFireLocalRefusesAnImageItCannotGiveSatelle(t *testing.T) {
	sh := filepath.Join(t.TempDir(), "satelle")
	if err := os.WriteFile(sh, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, uname, version, reason string
		cpCode                       int
		shell                        bool
	}{
		{name: "another architecture", uname: otherUname(t), version: plainVersion, reason: "no satelle binary for"},
		{name: "no writable path", uname: hostUname(t), version: plainVersion, cpCode: 1, reason: "no writable path"},
		{name: "a copy that does not run as its version", uname: hostUname(t), version: "satelle 0.0.1", reason: "does not run as"},
		{name: "a firing satelle that is not linux", uname: hostUname(t), version: plainVersion, shell: true, reason: "not a linux binary"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTaskFixture(t, "echo checked")
			bin := testBinary(t)
			if tc.shell {
				bin = sh
			}
			script, log := plainDocker(t, tc.uname, tc.version, tc.cpCode)
			o := plainFire(t, f, plainImage, bin, script)
			before := chainLengths(t, f)
			run, err := FireLocal(context.Background(), o)
			if err == nil || !strings.Contains(err.Error(), "image "+plainImage+" cannot be given satelle") || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("FireLocal = %v; want the image and %q", err, tc.reason)
			}
			lines := logLines(t, log)
			if i := lineIndex(lines, func(l string) bool { return strings.HasPrefix(l, "exec -i ") }); i >= 0 {
				t.Errorf("the node ran: %s", lines[i])
			}
			if tc.shell {
				if run != nil || len(lines) > 0 {
					t.Errorf("refused before a container, yet run %v, docker %q", run, lines)
				}
				if _, err := o.Runs.Load(o.Remote, o.Item); err == nil {
					t.Error("a run record was written for a refusal before any container")
				}
			} else {
				if lineIndex(lines, func(l string) bool { return strings.HasPrefix(l, "rm -f satelle-node-") }) < 0 {
					t.Errorf("the container was not removed:\n%s", strings.Join(lines, "\n"))
				}
				saved, err := o.Runs.Load(o.Remote, o.Item)
				if err != nil || saved.GetState() != RunStopped || !strings.Contains(saved.GetReason(), tc.reason) {
					t.Errorf("the saved run = %v, %v", saved, err)
				}
			}
			if after := chainLengths(t, f); !maps.Equal(before, after) {
				t.Errorf("chains changed: %v -> %v", before, after)
			}
			events, err := f.k.Ledger.Read(f.task)
			if err != nil {
				t.Fatal(err)
			}
			if seats := item.FoldSeats(events); len(seats) > 0 {
				t.Errorf("the task holds seats %v", seats)
			}
		})
	}
}

func chainLengths(t *testing.T, f taskFixture) map[string]int {
	t.Helper()
	ids, err := f.k.Ledger.Items()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, id := range ids {
		ev, err := f.k.Ledger.Read(id)
		if err != nil {
			t.Fatal(err)
		}
		out[id] = len(ev)
	}
	return out
}
