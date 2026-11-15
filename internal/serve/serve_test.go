package serve

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/buildinfo"
	"github.com/bobmcallan/satelle/internal/config"
)

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func waitHealthz(t *testing.T, port int) {
	t.Helper()
	url := fmt.Sprintf("http://127.0.0.1:%d/healthz", port)
	eventually(t, waitBudget, func() bool {
		resp, err := http.Get(url)
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, fmt.Sprintf("healthz on :%d never answered", port))
}

func TestServePortIgnoresRepoConfig(t *testing.T) {
	isolateHome(t)
	port := freePort(t)
	if err := config.SaveGlobal(config.GlobalConfig{Service: config.ServiceConfig{Port: port, Addr: "127.0.0.1"}}); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	dataDir := filepath.Join(repo, config.DefaultDataDir)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, config.ConfigName), []byte("web_port = 9000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- Run(ctx, Options{}) }()
	waitHealthz(t, port)
	if ln, err := net.Listen("tcp", "127.0.0.1:9000"); err == nil {
		_ = ln.Close()
	}
	cancel()
	select {
	case <-errc:
	case <-time.After(waitBudget):
	}
}

func TestZeroConfigRepoServes(t *testing.T) {
	isolateHome(t)
	port := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- Run(ctx, Options{Addr: "127.0.0.1", Port: port}) }()
	waitHealthz(t, port)
	cancel()
	select {
	case <-errc:
	case <-time.After(waitBudget):
	}
}

func TestServeAddrUsesGlobalWhenEmpty(t *testing.T) {
	isolateHome(t)
	port := freePort(t)
	if err := config.SaveGlobal(config.GlobalConfig{Service: config.ServiceConfig{Port: port, Addr: "127.0.0.1"}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- Run(ctx, Options{}) }()
	waitHealthz(t, port)
	cancel()
	select {
	case <-errc:
	case <-time.After(waitBudget):
	}
}

func TestInstalledBuilds(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	daemon := filepath.Join(dir, "satelled")
	cli := filepath.Join(dir, "satelle")
	for _, p := range []string{daemon, cli} {
		if err := os.WriteFile(p, []byte("bin"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	read := installedBuilds(home, daemon, nil)
	got := read()
	if got.Daemon != buildinfo.Resolve().String() {
		t.Errorf("Daemon = %q, want this build %q", got.Daemon, buildinfo.Resolve().String())
	}
	if !strings.Contains(got.CLI, "has not run since it was installed") {
		t.Errorf("CLI before it ran = %q", got.CLI)
	}
	line := "satelle 9.9.9 (commit c, built b)"
	if err := buildinfo.Record(home, cli, line); err != nil {
		t.Fatal(err)
	}
	if got := read(); got.CLI != line {
		t.Errorf("CLI after it recorded = %q, want %q", got.CLI, line)
	}
	if err := os.WriteFile(daemon, []byte("a replaced binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := read(); !strings.Contains(got.Daemon, "changed since it last ran") {
		t.Errorf("Daemon after replacement = %q", got.Daemon)
	}
	if got := installedBuilds(home, "", errors.New("no exe"))(); !strings.HasPrefix(got.CLI, "unknown (") || !strings.HasPrefix(got.Daemon, "unknown (") {
		t.Errorf("no executable: %+v", got)
	}
}
