package provider

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/remotedef"
)

var smallImages = []string{"busybox:latest", "busybox", "alpine:3", "alpine:latest", "alpine"}

func TestDockerLocalRunsTheDefinition(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	d := DockerLocal{}
	if err := d.Available(ctx); err != nil {
		t.Skipf("docker daemon unavailable: %v", err)
	}
	image := ""
	for _, img := range smallImages {
		if d.HasImage(ctx, img) {
			image = img
			break
		}
	}
	if image == "" {
		t.Skip("no small image (busybox, alpine) present locally; not pulling one")
	}
	def := remotedef.Definition{
		Provider: remotedef.ProviderDockerLocal,
		Machine:  remotedef.Machine{Image: image},
		Setup:    remotedef.Setup{OnCreate: []string{"echo created > /tmp/on_create"}, PostStart: []string{"echo started >> /tmp/on_create"}},
	}
	h, err := d.Start(ctx, def, Run{NodeID: "nod_test" + time.Now().Format("150405"), Token: "tok-secret"})
	if h != nil {
		t.Cleanup(func() { _ = h.Stop(context.Background()) })
	}
	if err != nil {
		t.Fatal(err)
	}
	out, err := h.Exec(ctx, "cat", "/tmp/on_create")
	if err != nil || string(out) != "created\nstarted\n" {
		t.Fatalf("setup output = %q, %v", out, err)
	}
	out, err = h.Exec(ctx, "sh", "-c", "echo $"+EnvNodeID+" $"+EnvNodeToken)
	if err != nil || !strings.HasPrefix(string(out), "nod_test") || !strings.Contains(string(out), "tok-secret") {
		t.Fatalf("environment = %q, %v", out, err)
	}
	if _, err := h.Exec(ctx, "sh", "-c", "wget -q -T 2 -O /dev/null http://1.1.1.1 || exit 0"); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if err := h.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(ctx, "true"); err == nil {
		t.Fatal("the container survived Stop")
	}
}

func TestDockerLocalFailedSetupLeavesTheMachineToInspect(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	d := DockerLocal{}
	if err := d.Available(ctx); err != nil {
		t.Skipf("docker daemon unavailable: %v", err)
	}
	image := ""
	for _, img := range smallImages {
		if d.HasImage(ctx, img) {
			image = img
			break
		}
	}
	if image == "" {
		t.Skip("no small image present locally")
	}
	def := remotedef.Definition{Machine: remotedef.Machine{Image: image}, Setup: remotedef.Setup{OnCreate: []string{"true", "exit 3"}}}
	h, err := d.Start(ctx, def, Run{NodeID: "nod_fail" + time.Now().Format("150405")})
	if h == nil || err == nil || !strings.Contains(err.Error(), "on_create command 2") {
		t.Fatalf("Start = %v, %v", h, err)
	}
	defer h.Stop(context.Background())
	if _, err := h.Exec(ctx, "true"); err != nil {
		t.Fatalf("the machine is not inspectable after a failed setup: %v", err)
	}
}

func TestContainerNameAndProviders(t *testing.T) {
	if got := ContainerName("nod_1/x y"); got != "satelle-node-nod_1-x-y" {
		t.Errorf("ContainerName = %q", got)
	}
	for _, p := range remotedef.Providers {
		a, err := For(p)
		if err != nil || a.Name() != p {
			t.Errorf("For(%s) = %v, %v", p, a, err)
		}
	}
	if _, err := (FlySprite{}).Start(context.Background(), remotedef.Definition{}, Run{}); err != ErrServerStarted {
		t.Errorf("fly-sprite Start = %v", err)
	}
	if len((FlySprite{}).SetupCommands()) != 1 || len((DockerLocal{}).SetupCommands()) != 0 {
		t.Error("setup commands")
	}
}
