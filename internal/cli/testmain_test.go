package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/gittest"
)

var refusedManagers = []string{"systemctl", "launchctl"}

var guardDir string

func TestMain(m *testing.M) { os.Exit(runGuarded(m)) }

func runGuarded(m *testing.M) int {
	os.Unsetenv("XDG_RUNTIME_DIR")
	os.Unsetenv("DBUS_SESSION_BUS_ADDRESS")
	dir, err := os.MkdirTemp("", "satelle-guard-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)
	for _, name := range refusedManagers {
		body := fmt.Sprintf("#!/bin/sh\necho '%s is refused in tests: put a fake %s first on PATH' >&2\nexit 1\n", name, name)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	guardDir = dir
	os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	refuseDefaultPort()
	return gittest.Main(m)
}

var errOwnSatelled = errors.New("127.0.0.1:" + strconv.Itoa(config.DefaultWebPort) + " is the machine's own satelled; a test starts its own on its own port")

func refuseDefaultPort() {
	base := http.DefaultTransport.(*http.Transport)
	dial := base.DialContext
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	guarded := base.Clone()
	guarded.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if refusedAddr(addr) {
			return nil, errOwnSatelled
		}
		return dial(ctx, network, addr)
	}
	http.DefaultTransport = guarded
}

func refusedAddr(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port != strconv.Itoa(config.DefaultWebPort) {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsUnspecified())
}
