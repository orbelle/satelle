package tests

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/remote/provider"
	"github.com/bobmcallan/satelle/internal/remotedef"
)

// A node on the docker-local provider runs the built satelle: the container
// carries the binary and the node's identity, the kernel walks a story inside
// it, and Stop removes it. Skipped when docker or its daemon is absent, or no
// small image is present locally (the test never pulls one).
func TestRemoteNodeOnDockerLocal(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("the mounted satelle binary is a linux build only on linux")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	d := provider.DockerLocal{}
	if err := d.Available(ctx); err != nil {
		t.Skipf("docker info fails: %v", err)
	}
	image := ""
	for _, img := range []string{"busybox:latest", "busybox", "alpine:3", "alpine:latest", "alpine"} {
		if d.HasImage(ctx, img) {
			image = img
			break
		}
	}
	if image == "" {
		t.Skip("no small image (busybox, alpine) present locally; not pulling one")
	}
	node := fmt.Sprintf("nod_it%d", time.Now().UnixNano()%1e9)
	def := remotedef.Definition{Provider: remotedef.ProviderDockerLocal, Machine: remotedef.Machine{Image: image},
		Setup: remotedef.Setup{OnCreate: []string{"mkdir -p /work /tmp/home"}}}
	h, err := d.Start(ctx, def, provider.Run{NodeID: node, Token: "tok-node", Binary: satelleBin})
	if h != nil {
		t.Cleanup(func() { _ = h.Stop(context.Background()) })
	}
	if err != nil {
		t.Fatal(err)
	}
	out, err := h.Exec(ctx, "sh", "-c", "echo $"+provider.EnvNodeID+"; satelle version")
	if err != nil || !strings.HasPrefix(string(out), node+"\nsatelle ") {
		t.Fatalf("node identity and binary: %q, %v", out, err)
	}
	walk := `set -e
export HOME=/tmp/home SATELLE_SERVER_ENDPOINT=none
cd /work
satelle init >/dev/null
id=$(satelle story create --title on-the-node --json | sed -n 's/.*"id": "\(.*\)".*/\1/p')
satelle story set "$id" --status done
satelle ledger verify`
	out, err = h.Exec(ctx, "sh", "-c", walk)
	if err != nil || !strings.Contains(string(out), "backlog -> done") || !strings.Contains(string(out), "1 chains intact") {
		t.Fatalf("the walk on the node: %q, %v", out, err)
	}
	if err := h.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(ctx, "true"); err == nil {
		t.Fatal("the container survived Stop")
	}
}
