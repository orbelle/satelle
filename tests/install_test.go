//go:build integration && linux

package tests

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
)

// installFixture is a machine about to run scripts/install.sh pinned to
// 9.9.9: a file:// release tree holding the built satelle as that version, an
// install dir already holding satelled (a pinned install fetches the CLI
// alone), a logging fake systemctl first on PATH, and [service] on a port
// nothing answers on.
type installFixture struct {
	s           *sandbox
	script, log string
	installDir  string
	unitDir     string
}

func newInstallFixture(t *testing.T) *installFixture {
	t.Helper()
	s := newSandbox(t)
	f := &installFixture{s: s, installDir: filepath.Join(s.home, ".local", "bin"), unitDir: filepath.Join(s.home, ".config", "systemd", "user")}
	script, err := filepath.Abs(filepath.Join("..", "scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	f.script = script
	tree := t.TempDir()
	name := "satelle-v9.9.9-linux-" + runtime.GOARCH
	body, err := os.ReadFile(satelleBin)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	writeFile(t, filepath.Join(tree, "v9.9.9", name), string(body), 0o755)
	writeFile(t, filepath.Join(tree, "v9.9.9", name+".sha256"), hex.EncodeToString(sum[:])+"  "+name+"\n", 0o644)
	daemon, err := os.ReadFile(satelledBin)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(f.installDir, "satelled"), string(daemon), 0o755)
	fake := t.TempDir()
	f.log = filepath.Join(t.TempDir(), "systemctl.log")
	writeFile(t, filepath.Join(fake, "systemctl"), "#!/bin/sh\necho \"$@\" >> "+f.log+"\n", 0o755)
	s.path = append(s.path, fake)
	writeFile(t, filepath.Join(s.env["SATELLE_HOME"], config.GlobalConfigName), "[service]\nport = 1\n", 0o644)
	s.env["SATELLE_RELEASE_BASE"] = "file://" + tree
	s.env["SATELLE_INSTALL_DIR"] = f.installDir
	return f
}

func (f *installFixture) install(args ...string) result {
	f.s.t.Helper()
	return f.s.exec("sh", "", append([]string{f.script}, args...)...)
}

// By default install.sh installs and starts the service and sync timer and
// prints the status; --no-service and SATELLE_NO_SERVICE=1 change no unit and
// say so; an unknown option is refused (sto_1b6a7ed3 AC1, AC2, AC3).
func TestInstallScriptInstallsTheService(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		f := newInstallFixture(t)
		r := f.install("9.9.9")
		if r.code != 0 {
			t.Fatalf("install.sh exited %d:\n%s\n%s", r.code, r.out, r.err)
		}
		for _, name := range []string{"satelle.service", "satelle-sync.service", "satelle-sync.timer"} {
			if _, err := os.Stat(filepath.Join(f.unitDir, name)); err != nil {
				t.Errorf("%s not installed: %v", name, err)
			}
		}
		unit, _ := os.ReadFile(filepath.Join(f.unitDir, "satelle.service"))
		if !strings.Contains(string(unit), "ExecStart="+filepath.Join(f.installDir, "satelled")+" ") {
			t.Errorf("satelled's unit does not run the installed satelled:\n%s", unit)
		}
		calls, err := os.ReadFile(f.log)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"--user daemon-reload\n", "--user enable satelle.service\n", "--user restart satelle.service\n", "--user enable --now satelle-sync.timer\n"} {
			if !strings.Contains(string(calls), want) {
				t.Errorf("systemctl was not called with %q:\n%s", want, calls)
			}
		}
		for _, want := range []string{"\nunit ", "\nsync ", "\nconfig ", "--no-service", "SATELLE_NO_SERVICE=1"} {
			if !strings.Contains(r.out, want) {
				t.Errorf("the output lacks %q:\n%s", want, r.out)
			}
		}
		if strings.Contains(r.out, "satelle service install  #") || strings.Contains(r.out, "satelle service restart  #") {
			t.Errorf("the closing text still asks for a service step:\n%s", r.out)
		}
	})
	for _, tc := range []struct{ name, env, arg, why string }{
		{name: "flag", arg: "--no-service", why: "--no-service"},
		{name: "env", env: "1", why: "SATELLE_NO_SERVICE=1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newInstallFixture(t)
			args := []string{"9.9.9"}
			if tc.arg != "" {
				args = append([]string{tc.arg}, args...)
			}
			if tc.env != "" {
				f.s.env["SATELLE_NO_SERVICE"] = tc.env
			}
			r := f.install(args...)
			if r.code != 0 {
				t.Fatalf("install.sh exited %d:\n%s\n%s", r.code, r.out, r.err)
			}
			if _, err := os.Stat(f.unitDir); !os.IsNotExist(err) {
				t.Errorf("a unit dir was created: %v", err)
			}
			if _, err := os.Stat(f.log); !os.IsNotExist(err) {
				t.Errorf("systemctl ran: %v", err)
			}
			if !strings.Contains(r.out, "service: not installed or changed ("+tc.why+")") {
				t.Errorf("the skip line does not name %s:\n%s", tc.why, r.out)
			}
			if _, err := os.Stat(filepath.Join(f.installDir, "satelle")); err != nil {
				t.Errorf("the binary was not installed: %v", err)
			}
		})
	}
	t.Run("unknown option", func(t *testing.T) {
		f := newInstallFixture(t)
		r := f.install("--bogus", "9.9.9")
		if r.code == 0 || !strings.Contains(r.err, "--bogus") {
			t.Errorf("--bogus was not refused by name (exit %d):\n%s\n%s", r.code, r.out, r.err)
		}
	})
}

// The installer's next steps for a new repo wire a harness after init, then
// check with doctor (sto_f58ed5fa AC3).
func TestInstallScriptNextStepsWireAHarness(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	_, next, ok := strings.Cut(string(b), `echo "Next (a new repo):"`)
	if !ok {
		t.Fatal("install.sh has no Next (a new repo) block")
	}
	next, _, _ = strings.Cut(next, "Repos you already have")
	i, j, k := strings.Index(next, "satelle init"), strings.Index(next, "satelle agents install <harness>"), strings.Index(next, "satelle doctor")
	if i < 0 || j < i || k < j {
		t.Fatalf("Next (a new repo) is not init, agents install, doctor in that order:\n%s", next)
	}
}
