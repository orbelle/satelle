package provider

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/remotedef"
)

// smallImages are tried in order; the test never pulls one.
var smallImages = []string{"busybox:latest", "busybox", "alpine:3", "alpine:latest", "alpine"}

// The reference provider against a real docker daemon: the definition runs as
// a container with the node's identity in its environment and its setup run
// inside, inspectable with docker exec, and Stop removes it. Skipped when
// docker is absent, its daemon is down, or no small image is present locally.
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
	if got := containerName("nod_1/x y"); got != "satelle-node-nod_1-x-y" {
		t.Errorf("containerName = %q", got)
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

// A fake docker CLI records the argv: the token never appears on it.
func TestDockerLocalKeepsTheTokenOffTheArgv(t *testing.T) {
	dir := t.TempDir()
	log := dir + "/argv"
	script := dir + "/docker"
	if err := writeScript(script, "#!/bin/sh\necho \"$@\" >> "+log+"\necho \"TOKEN=$"+EnvNodeToken+"\" >> "+log+"\n"); err != nil {
		t.Fatal(err)
	}
	def := remotedef.Definition{Network: remotedef.Network{}}
	h, err := DockerLocal{Docker: script}.Start(context.Background(), def, Run{NodeID: "nod_9", Token: "s3cret", Binary: "/bin/satelle", Join: true})
	if err != nil || h.ID() != "satelle-node-nod_9" {
		t.Fatalf("Start = %v, %v", h, err)
	}
	b, _ := readFile(log)
	lines := strings.Split(strings.TrimSpace(b), "\n")
	run := lines[0]
	if strings.Contains(run, "s3cret") || !strings.Contains(run, "-e "+EnvNodeToken+" ") || !strings.Contains(run, "--network none") ||
		!strings.Contains(run, "-v /bin/satelle:/usr/local/bin/satelle:ro") || !strings.Contains(run, " "+DefaultImage+" ") {
		t.Fatalf("docker run argv = %q", run)
	}
	if lines[1] != "TOKEN=s3cret" {
		t.Fatalf("the token did not reach docker's environment: %q", lines[1])
	}
	if !strings.Contains(b, "exec -d satelle-node-nod_9 satelle node join --no-setup") {
		t.Fatalf("no join started: %s", b)
	}
}
