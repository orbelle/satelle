//go:build !windows

package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/remotedef"
)

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
		!strings.Contains(run, "-v /bin/satelle:/usr/local/bin/satelle:ro") || !strings.Contains(run, " "+NodeImage+" ") {
		t.Fatalf("docker run argv = %q", run)
	}
	if lines[1] != "TOKEN=s3cret" {
		t.Fatalf("the token did not reach docker's environment: %q", lines[1])
	}
	if !strings.Contains(b, "exec -d satelle-node-nod_9 satelle node join --no-setup") {
		t.Fatalf("no join started: %s", b)
	}
}

func TestDockerLocalBuildCopyArgv(t *testing.T) {
	dir := t.TempDir()
	log := dir + "/argv"
	script := dir + "/docker"
	if err := writeScript(script, "#!/bin/sh\necho \"$@\" >> "+log+"\n"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	d := DockerLocal{Docker: script}
	if err := d.BuildNodeImage(ctx, "/src/tree"); err != nil {
		t.Fatal(err)
	}
	if !d.Exists(ctx, "satelle-node-nod_1") {
		t.Fatal("Exists on an inspect that succeeds = false")
	}
	h, err := d.Start(ctx, remotedef.Definition{}, Run{NodeID: "nod_1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.CopyIn(ctx, "/tmp/in", "/node/in"); err != nil {
		t.Fatal(err)
	}
	if err := h.CopyOut(ctx, "/node/out/outcome.json", "/tmp/back/outcome.json"); err != nil {
		t.Fatal(err)
	}
	b, _ := readFile(log)
	lines := strings.Split(strings.TrimSpace(b), "\n")
	want := []string{
		"build -t " + NodeImage + " -f /src/tree/Dockerfile /src/tree",
		"container inspect --format {{.Id}} satelle-node-nod_1",
	}
	if len(lines) != 5 || lines[0] != want[0] || lines[1] != want[1] {
		t.Fatalf("argv = %q", lines)
	}
	if !strings.Contains(lines[2], "run -d --name satelle-node-nod_1 ") || !strings.Contains(lines[2], " --entrypoint sh "+NodeImage+" -c ") {
		t.Fatalf("docker run argv = %q", lines[2])
	}
	if lines[3] != "cp /tmp/in satelle-node-nod_1:/node/in" || lines[4] != "cp satelle-node-nod_1:/node/out/outcome.json /tmp/back/outcome.json" {
		t.Fatalf("docker cp argv = %q", lines[3:])
	}
	if err := writeScript(script, "#!/bin/sh\nexit 1\n"); err != nil {
		t.Fatal(err)
	}
	if d.Exists(ctx, "satelle-node-gone") {
		t.Fatal("Exists on an inspect that fails = true")
	}
}

func TestDockerLocalWithoutAnAllowListHasNoNetwork(t *testing.T) {
	d, log := fakeDockerScript(t, "exit 0\n")
	def := remotedef.Definition{Repos: []remotedef.Repo{{URL: "https://example.com/r.git"}}}
	if _, err := d.Start(context.Background(), def, Run{NodeID: "nod_5", Server: "https://satelle.example", NoSetup: true}); err != nil {
		t.Fatal(err)
	}
	b, _ := readFile(log)
	if !strings.Contains(b, "--network none") || strings.Contains(b, "network create") || strings.Contains(b, "SATELLE_NETWORK_ALLOW") {
		t.Fatalf("docker calls:\n%s", b)
	}
}

func TestDockerLocalRefusesAnAllowListWithNoEgress(t *testing.T) {
	d, log := fakeDockerScript(t, "exit 0\n")
	def := remotedef.Definition{Network: remotedef.Network{Allow: []string{"proxy.golang.org"}}}
	h, err := d.Start(context.Background(), def, Run{NodeID: "nod_6"})
	if err == nil || h != nil || !strings.Contains(err.Error(), "no satelle binary to serve the [network] allow-list") {
		t.Fatalf("Start = %v, %v", h, err)
	}
	if b, _ := readFile(log); b != "" {
		t.Fatalf("docker was called:\n%s", b)
	}
}
