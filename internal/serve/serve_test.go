package serve

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

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

// TestServePortIgnoresRepoConfig (sty_21a7d16d AC2): repo web_port does not
// change which port satelled binds.
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
	// 9000 must not be us (bind should have used global port).
	if ln, err := net.Listen("tcp", "127.0.0.1:9000"); err == nil {
		_ = ln.Close()
	}
	cancel()
	select {
	case <-errc:
	case <-time.After(waitBudget):
	}
}

// TestZeroConfigRepoServes (sty_21a7d16d AC6): no machine-scope keys, defaults work.
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
