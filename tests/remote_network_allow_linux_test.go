//go:build integration && linux

package tests

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/remote/provider"
	"github.com/bobmcallan/satelle/internal/remotedef"
)

func realDocker(t *testing.T, ctx context.Context) provider.DockerLocal {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not installed")
	}
	d := provider.DockerLocal{Egress: satelleBin}
	if err := d.Available(ctx); err != nil {
		t.Skipf("docker info fails: %v", err)
	}
	return d
}

func dockerOut(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func uniqueNode(prefix string) string {
	return fmt.Sprintf("nod_%s%d", prefix, time.Now().UnixNano()%1e9)
}

func helperServer(t *testing.T, ctx context.Context, image, name, body string) string {
	t.Helper()
	serve := fmt.Sprintf("while :; do printf 'HTTP/1.0 200 OK\\r\\nContent-Length: %d\\r\\n\\r\\n%s' | nc -l -p 8080; done", len(body), body)
	if out, err := dockerOut(ctx, "run", "-d", "--name", name, "--entrypoint", "sh", image, "-c", serve); err != nil {
		t.Fatalf("starting helper %s: %v: %s", name, err, out)
	}
	t.Cleanup(func() { _, _ = dockerOut(context.Background(), "rm", "-f", name) })
	ip, err := dockerOut(ctx, "inspect", "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", name)
	if err != nil || net.ParseIP(ip) == nil {
		t.Fatalf("helper %s address = %q, %v", name, ip, err)
	}
	return ip
}

func execShell(ctx context.Context, h provider.Handle, script string) string {
	out, _ := h.Exec(ctx, "sh", "-c", script+" 2>&1; echo exit=$?")
	return strings.TrimSpace(string(out))
}

func TestDockerNodeReachesOnlyItsAllowedHosts(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	d := realDocker(t, ctx)
	image := ""
	for _, img := range []string{"busybox:latest", "busybox", "alpine:3", "alpine:latest", "alpine", "golang:1.27-alpine"} {
		if d.HasImage(ctx, img) {
			image = img
			break
		}
	}
	if image == "" {
		t.Skip("no busybox or alpine image present locally; not pulling one")
	}
	node := uniqueNode("allow")
	ipA := helperServer(t, ctx, image, "satelle-it-"+node+"-a", "from-a")
	ipB := helperServer(t, ctx, image, "satelle-it-"+node+"-b", "from-b")
	def := remotedef.Definition{Provider: remotedef.ProviderDockerLocal, Machine: remotedef.Machine{Image: image},
		Network: remotedef.Network{Allow: []string{ipA}}}
	h, err := d.Start(ctx, def, provider.Run{NodeID: node})
	if h != nil {
		t.Cleanup(func() { _ = h.Stop(context.Background()) })
	}
	if err != nil {
		t.Fatal(err)
	}
	if got := execShell(ctx, h, "wget -q -T 10 -O- http://"+ipA+":8080/"); !strings.HasPrefix(got, "from-a") || !strings.HasSuffix(got, "exit=0") {
		t.Fatalf("the node did not reach the allowed host %s: %q", ipA, got)
	}
	if got := execShell(ctx, h, "wget -q -T 10 -O- http://"+ipB+":8080/"); strings.HasSuffix(got, "exit=0") || !strings.Contains(got, "403") || strings.Contains(got, "from-b") {
		t.Fatalf("the node reached %s, which the list does not name: %q", ipB, got)
	}
	if got := execShell(ctx, h, "unset http_proxy HTTP_PROXY; wget -q -T 5 -O- http://"+ipB+":8080/"); strings.HasSuffix(got, "exit=0") || strings.Contains(got, "from-b") {
		t.Fatalf("the node has a direct route to %s: %q", ipB, got)
	}
	egress := "satelle-egress-" + node
	bridgeIP, err := dockerOut(ctx, "inspect", "--format", "{{with index .NetworkSettings.Networks \"bridge\"}}{{.IPAddress}}{{end}}", egress)
	if err != nil || net.ParseIP(bridgeIP) == nil {
		t.Fatalf("the proxy's bridge address = %q, %v", bridgeIP, err)
	}
	if out, err := dockerOut(ctx, "exec", "satelle-it-"+node+"-b", "sh", "-c",
		"http_proxy=http://"+bridgeIP+":3128 wget -q -T 5 -O- http://"+ipA+":8080/"); err == nil || strings.Contains(out, "from-a") {
		t.Fatalf("the proxy answers on the default bridge at %s: %q", bridgeIP, out)
	}
	if err := h.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if out, err := dockerOut(ctx, "network", "inspect", "satelle-net-"+node); err == nil {
		t.Fatalf("the node's network survived Stop: %s", out)
	}
}

func TestIntegrationRemoteDownloadsAGoModuleThroughItsAllowList(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	b, err := os.ReadFile(filepath.Join("..", ".satelle", "remotes", "integration", "remote.toml"))
	if err != nil {
		t.Fatal(err)
	}
	def, err := remotedef.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if def.Provider != remotedef.ProviderDockerLocal || len(def.Network.Allow) == 0 {
		t.Fatalf("the integration remote is %s with allow %v", def.Provider, def.Network.Allow)
	}
	d := realDocker(t, ctx)
	c, err := net.DialTimeout("tcp", "proxy.golang.org:443", 5*time.Second)
	if err != nil {
		t.Skipf("without network: %v", err)
	}
	c.Close()
	if !d.HasImage(ctx, def.Machine.Image) {
		if out, err := dockerOut(ctx, "pull", def.Machine.Image); err != nil {
			t.Skipf("pulling %s needs the network: %v: %s", def.Machine.Image, err, out)
		}
	}
	h, err := d.Start(ctx, def, provider.Run{NodeID: uniqueNode("integ")})
	if h != nil {
		t.Cleanup(func() { _ = h.Stop(context.Background()) })
	}
	if err != nil {
		t.Fatalf("starting the integration node (its setup goes through the allow-list): %v", err)
	}
	if got := execShell(ctx, h, "cd /tmp && GOFLAGS=-modcacherw go mod download -json golang.org/x/mod@v0.20.0"); !strings.HasSuffix(got, "exit=0") || !strings.Contains(got, `"Version": "v0.20.0"`) {
		t.Fatalf("go mod download through the allow-list:\n%s", got)
	}
	if got := execShell(ctx, h, "wget -q -T 5 -O- https://example.com/"); strings.HasSuffix(got, "exit=0") {
		t.Fatalf("the node reached example.com, which the list does not name: %q", got)
	}
}

const allowSecretFake = `#!/bin/sh
echo "$*" >> '@LOG@'
case "$1 $2" in
  "network create") exit 0 ;;
  "network inspect") exit 1 ;;
  "network rm") exit 0 ;;
esac
case "$1" in
  info) echo '@ARCH@' ;;
  image) exit 1 ;;
  build) exit 0 ;;
  rm) exit 0 ;;
  container) exit 1 ;;
  *) echo "fake docker: $1 not faked" >&2; exit 1 ;;
esac
`

func TestDockerLocalFireSecretWithAllowNotRefused(t *testing.T) {
	t.Parallel()
	arch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[runtime.GOARCH]
	if arch == "" {
		t.Skipf("no docker architecture name for %s", runtime.GOARCH)
	}
	b := secretSandbox(t, true, func(def string) string { return def + "\n[network]\nallow = [\"example.com\"]\n" })
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	writeFile(t, filepath.Join(dir, "docker"), strings.NewReplacer("@LOG@", log, "@ARCH@", arch).Replace(allowSecretFake), 0o755)
	b.s.path = append([]string{dir}, b.s.path...)
	r := b.fire()
	if strings.Contains(r.err, "may allow no host") || strings.Contains(r.err, "cannot be enforced here") {
		t.Fatalf("a secret beside an allow entry was refused: exit %d\n%s%s", r.code, r.out, r.err)
	}
	got, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("docker never ran: %v\n%s%s", err, r.out, r.err)
	}
	lines := strings.Split(strings.TrimSpace(string(got)), "\n")
	create, remove := -1, -1
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "network create --internal") && create < 0:
			create = i
		case strings.HasPrefix(l, "network rm satelle-net-") && create >= 0:
			remove = i
		case strings.HasPrefix(l, "run -d"):
			t.Fatalf("the node started:\n%s", got)
		}
	}
	if create < 0 || remove < 0 {
		t.Fatalf("the fire did not build and tear down the node's network:\n%s\n%s%s", got, r.out, r.err)
	}
}
