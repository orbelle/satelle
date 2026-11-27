//go:build linux

package cli

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
)

func TestTestsNeverReachTheUserManager(t *testing.T) {
	isolate(t)
	for _, k := range []string{"XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"} {
		if v := os.Getenv(k); v != "" {
			t.Errorf("%s=%q in the test process", k, v)
		}
	}
	for _, name := range refusedManagers {
		p, err := exec.LookPath(name)
		if err != nil || filepath.Dir(p) != guardDir {
			t.Errorf("%s resolves to %q (%v), want the guard in %s", name, p, err, guardDir)
		}
	}
	if systemdUser() {
		t.Fatal("systemdUser() is true in a test with no fake systemctl")
	}
	err := restartUnit(&bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "systemctl is refused in tests") {
		t.Fatalf("restartUnit = %v, want the guard's refusal", err)
	}
	if err := applyService(&bytes.Buffer{}, serviceSpec{Daemon: "/bin/true", Port: 1, CLI: "/bin/true"}, false); !errors.Is(err, errNoSystemd) {
		t.Fatalf("applyService = %v, want errNoSystemd", err)
	}
}

func TestSystemctlRefusesAReachableUserManagerInTests(t *testing.T) {
	for _, k := range []string{"XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"} {
		t.Run(k, func(t *testing.T) {
			isolate(t)
			bin := t.TempDir()
			log := filepath.Join(t.TempDir(), "systemctl.log")
			writeExec(t, filepath.Join(bin, "systemctl"), "#!/bin/sh\necho \"$@\" >> "+log+"\n")
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv(k, t.TempDir())
			func() {
				defer func() {
					if r := recover(); r == nil {
						t.Error("systemctl did not panic with the user manager reachable")
					}
				}()
				_, _ = systemctl("restart", serviceUnitName)
			}()
			if _, err := os.Stat(log); !os.IsNotExist(err) {
				t.Fatalf("the fake systemctl ran: %v", err)
			}
		})
	}
}

func TestDefaultSatelledPortIsRefusedInTests(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "localhost"} {
		resp, err := http.Get("http://" + host + ":" + strconv.Itoa(config.DefaultWebPort) + "/healthz")
		if err == nil {
			resp.Body.Close()
			t.Fatalf("%s reached the default satelled port", host)
		}
		if !errors.Is(err, errOwnSatelled) {
			t.Fatalf("%s: %v, want the guard's refusal", host, err)
		}
	}
	own := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer own.Close()
	resp, err := http.Get(own.URL + "/healthz")
	if err != nil {
		t.Fatalf("own server: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("own server status %d", resp.StatusCode)
	}
}
