//go:build !darwin

package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSystemdNamesUnchanged(t *testing.T) {
	isolate(t)
	home, _ := os.UserHomeDir()
	user := filepath.Join(home, ".config", "systemd", "user")
	for got, want := range map[string]string{
		unitPath():       filepath.Join(user, "satelle.service"),
		syncUnitPath():   filepath.Join(user, "satelle-sync.timer"),
		daemonUnitDesc(): "satelle.service",
		syncUnitDesc():   "satelle-sync.timer",
		installedDaemon(renderUnit("/x/satelled", "127.0.0.1", 1)): "/x/satelled",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
	if got, want := renderUnit("/x/satelled", "127.0.0.1", 8787), `[Unit]
Description=satelled (satelle local web UI)
After=network.target

[Service]
ExecStart=/x/satelled --addr 127.0.0.1 --port 8787
Restart=always
RestartSec=2

[Install]
WantedBy=default.target
`; got != want {
		t.Errorf("unit:\n%s", got)
	}
	if got, want := renderSyncService("/x/satelle"), `[Unit]
Description=satelle ledger sync pass (push then pull each project on this machine)
After=network-online.target

[Service]
Type=oneshot
ExecStart=/x/satelle ledger autosync
TimeoutStartSec=300
`; got != want {
		t.Errorf("sync service:\n%s", got)
	}
	if got, want := renderSyncTimer(), `[Unit]
Description=satelle ledger sync every 30 s

[Timer]
OnBootSec=30s
OnUnitInactiveSec=30s
AccuracySec=1s
Unit=satelle-sync.service

[Install]
WantedBy=timers.target
`; got != want {
		t.Errorf("sync timer:\n%s", got)
	}
}
