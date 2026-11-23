package provider

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/remotedef"
)

func egressFake(t *testing.T, failOn string) (DockerLocal, string) {
	t.Helper()
	fail := ""
	if failOn != "" {
		fail = "case \"$*\" in\n\"" + failOn + "\"*) echo fake failure; exit 1;;\nesac\n"
	}
	d, log := fakeDockerScript(t, fail+"case \"$1 $2\" in\n\"network inspect\") echo 172.30.0.0/16;;\n\"logs satelle-egress-nod_1\") echo proxy said no;;\nesac\nexit 0\n")
	d.Egress = "/opt/satelle/bin/satelle"
	return d, log
}

func allowDef() remotedef.Definition {
	return remotedef.Definition{Machine: remotedef.Machine{Image: "golang:1.27-alpine"}, Network: remotedef.Network{Allow: []string{"proxy.golang.org", "*.example.org"}}}
}

func logOf(t *testing.T, log string) []string {
	t.Helper()
	b, _ := readFile(log)
	return strings.Split(strings.TrimSpace(b), "\n")
}

func indexOf(lines []string, prefix string, from int) int {
	for i := from; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], prefix) {
			return i
		}
	}
	return -1
}

func TestDockerLocalStartsTheEgressProxyBeforeTheNode(t *testing.T) {
	d, log := egressFake(t, "")
	h, err := d.Start(context.Background(), allowDef(), Run{NodeID: "nod_1", NoSetup: true})
	if err != nil {
		t.Fatal(err)
	}
	lines := logOf(t, log)
	order := []string{
		"network create --internal --label satelle.node=nod_1 satelle-net-nod_1",
		"network inspect --format {{(index .IPAM.Config 0).Subnet}} satelle-net-nod_1",
		"create --name satelle-egress-nod_1 --label satelle.node=nod_1 --entrypoint /satelle-egress golang:1.27-alpine node egress --subnet 172.30.0.0/16 --port 3128 --allow proxy.golang.org,*.example.org",
		"cp /opt/satelle/bin/satelle satelle-egress-nod_1:/satelle-egress",
		"network connect --alias satelle-egress satelle-net-nod_1 satelle-egress-nod_1",
		"start satelle-egress-nod_1",
		"exec satelle-egress-nod_1 /satelle-egress node egress --ready --subnet 172.30.0.0/16 --port 3128",
		"run -d --name satelle-node-nod_1 ",
	}
	at := 0
	for _, want := range order {
		i := indexOf(lines, want, at)
		if i < 0 {
			t.Fatalf("no %q after line %d in:\n%s", want, at, strings.Join(lines, "\n"))
		}
		at = i + 1
	}
	run := lines[at-1]
	for _, want := range []string{"--network satelle-net-nod_1", "-e HTTPS_PROXY=http://satelle-egress:3128", "-e HTTP_PROXY=http://satelle-egress:3128",
		"-e https_proxy=http://satelle-egress:3128", "-e http_proxy=http://satelle-egress:3128", "-e NO_PROXY=localhost,127.0.0.1", "-e no_proxy=localhost,127.0.0.1"} {
		if !strings.Contains(run, want) {
			t.Errorf("docker run lacks %q: %s", want, run)
		}
	}
	all := strings.Join(lines, "\n")
	for _, never := range []string{"--network none", "SATELLE_NETWORK_ALLOW", "0.0.0.0", " -p "} {
		if strings.Contains(all, never) {
			t.Errorf("the docker calls carry %q:\n%s", never, all)
		}
	}
	for _, l := range lines {
		if strings.HasPrefix(l, "run ") && !strings.Contains(l, "--network ") {
			t.Errorf("a docker run without --network: %s", l)
		}
	}
	if got := RemoveHint(h); got != "docker rm -f satelle-node-nod_1 satelle-egress-nod_1 && docker network rm satelle-net-nod_1" {
		t.Errorf("RemoveHint = %q", got)
	}
	n := len(lines)
	if err := h.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	lines = logOf(t, log)
	if strings.Join(lines[n:], "\n") != "rm -f satelle-node-nod_1 satelle-egress-nod_1\nnetwork rm satelle-net-nod_1" {
		t.Fatalf("Stop ran:\n%s", strings.Join(lines[n:], "\n"))
	}
}

func TestDockerLocalTearsDownAFailedEgressStart(t *testing.T) {
	gap := egressPollGap
	egressPollGap = time.Millisecond
	t.Cleanup(func() { egressPollGap = gap })
	for _, c := range []struct {
		step         string
		proxyCreated bool
	}{
		{"network inspect", false},
		{"create --name satelle-egress-nod_1", false},
		{"cp /opt/satelle/bin/satelle", true},
		{"network connect", true},
		{"start satelle-egress-nod_1", true},
		{"exec satelle-egress-nod_1 /satelle-egress node egress --ready", true},
		{"run -d", true},
	} {
		t.Run(c.step, func(t *testing.T) {
			d, log := egressFake(t, c.step)
			h, err := d.Start(context.Background(), allowDef(), Run{NodeID: "nod_1", NoSetup: true})
			if err == nil || h != nil {
				t.Fatalf("Start = %v, %v", h, err)
			}
			lines := logOf(t, log)
			failed := indexOf(lines, c.step, 0)
			if failed < 0 {
				t.Fatalf("the failing step never ran:\n%s", strings.Join(lines, "\n"))
			}
			rest := lines[failed+1:]
			wants := []string{"rm -f satelle-node-nod_1"}
			if c.proxyCreated {
				wants = append(wants, "rm -f satelle-egress-nod_1")
			}
			wants = append(wants, "network rm satelle-net-nod_1")
			at := 0
			for _, want := range wants {
				i := indexOf(rest, want, at)
				if i < 0 {
					t.Fatalf("no %q after the failure:\n%s", want, strings.Join(rest, "\n"))
				}
				at = i + 1
			}
			if !c.proxyCreated && indexOf(rest, "rm -f satelle-egress-nod_1", 0) >= 0 {
				t.Fatalf("removed a proxy that was never created:\n%s", strings.Join(rest, "\n"))
			}
			if strings.HasPrefix(c.step, "exec") {
				if indexOf(lines, "run -d", 0) >= 0 {
					t.Fatalf("the node started after the proxy failed:\n%s", strings.Join(lines, "\n"))
				}
				if !strings.Contains(err.Error(), "the egress proxy did not start in golang:1.27-alpine: proxy said no") {
					t.Fatalf("error = %v", err)
				}
			}
		})
	}
}
