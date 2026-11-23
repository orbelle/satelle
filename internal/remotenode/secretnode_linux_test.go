package remotenode

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/remote/provider"
)

func failingNodeDocker(t *testing.T, hold bool) (script, log string) {
	t.Helper()
	runLocal := "cat > /dev/null; echo node failed; exit 1"
	if hold {
		runLocal = "exec sleep 60"
	}
	return fakeDockerFor(t, `case "$1" in
  info) echo '`+hostUname(t)+`' ;;
  network) if [ "$2" = inspect ]; then echo 172.30.0.0/16; fi; exit 0 ;;
  exec) if [ "$2" = -i ]; then `+runLocal+`; fi; exit 0 ;;
  cp) case "$2" in satelle-node-*) exit 1 ;; esac; exit 0 ;;
  *) exit 0 ;;
esac
`)
}

type failedFire struct {
	out, log string
	node     string
}

func fireFailingNode(t *testing.T, f taskFixture, allow, hold bool, secret string) failedFire {
	t.Helper()
	script, log := failingNodeDocker(t, hold)
	var out strings.Builder
	o := f.def
	o.Docker, o.Runs, o.Out = provider.DockerLocal{Docker: script}, LocalRuns{Dir: t.TempDir()}, &out
	if allow {
		o.Definition.Network.Allow = []string{"example.com"}
		o.Docker.Egress = testBinary(t)
	}
	if secret != "" {
		o.Secrets = Secrets{Names: []string{probeSecret}, Values: map[string]string{probeSecret: secret}, Redact: []string{secret}}
	}
	ctx := context.Background()
	if hold {
		c, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		ctx = c
	}
	run, err := FireLocal(ctx, o)
	if err == nil || run == nil || run.GetState() != RunStopped {
		t.Fatalf("FireLocal = %v, %v; want a stopped run and an error", run, err)
	}
	return failedFire{out: out.String(), log: log, node: run.GetNodeId()}
}

func afterRunLocal(t *testing.T, log string) []string {
	t.Helper()
	lines := logLines(t, log)
	for i, l := range lines {
		if strings.HasPrefix(l, "exec -i ") {
			return lines[i+1:]
		}
	}
	t.Fatalf("the node was never run:\n%s", strings.Join(lines, "\n"))
	return nil
}

func TestFireLocalRemovesASecretNodeThatFails(t *testing.T) {
	for _, ending := range []struct {
		name string
		hold bool
	}{{"the node command fails", false}, {"the fire times out", true}} {
		for _, allow := range []bool{false, true} {
			name := ending.name
			if allow {
				name += " behind an egress proxy"
			}
			t.Run(name, func(t *testing.T) {
				f := secretFixture(t, "echo checked", nil, "")
				v := randomHex(t, 32)
				r := fireFailingNode(t, f, allow, ending.hold, v)
				name := provider.ContainerName(r.node)
				suffix := strings.TrimPrefix(name, "satelle-node-")
				want := "rm -f " + name
				if allow {
					want += " satelle-egress-" + suffix
				}
				after := afterRunLocal(t, r.log)
				rm := -1
				for i, l := range after {
					if l == want {
						rm = i
					}
				}
				if rm < 0 {
					t.Fatalf("no %q after the node ran:\n%s", want, strings.Join(after, "\n"))
				}
				if allow {
					net := "network rm satelle-net-" + suffix
					found := false
					for _, l := range after[rm+1:] {
						found = found || l == net
					}
					if !found {
						t.Errorf("no %q after the removal:\n%s", net, strings.Join(after, "\n"))
					}
				}
				if !strings.Contains(r.out, "the container "+name+" is removed") {
					t.Errorf("the fire did not say the container is removed:\n%s", r.out)
				}
				if strings.Contains(r.out, "left to inspect") {
					t.Errorf("the fire left a secret node to inspect:\n%s", r.out)
				}
				b, _ := os.ReadFile(r.log)
				if strings.Contains(string(b), v) || strings.Contains(r.out, v) {
					t.Error("the secret value reached docker's argv or the fire's output")
				}
			})
		}
	}
}

func TestFireLocalLeavesAFailedNodeWithoutSecrets(t *testing.T) {
	for _, allow := range []bool{false, true} {
		t.Run(map[bool]string{false: "no network", true: "behind an egress proxy"}[allow], func(t *testing.T) {
			f := newTaskFixture(t, "echo checked")
			r := fireFailingNode(t, f, allow, false, "")
			for _, l := range afterRunLocal(t, r.log) {
				if strings.HasPrefix(l, "rm ") || strings.HasPrefix(l, "network rm") {
					t.Fatalf("a failed node without secrets was torn down: %q", l)
				}
			}
			name := provider.ContainerName(r.node)
			hint := "docker rm -f " + name
			if allow {
				s := strings.TrimPrefix(name, "satelle-node-")
				hint += " satelle-egress-" + s + " && docker network rm satelle-net-" + s
			}
			want := "the container " + name + " is left to inspect; remove it with " + hint
			if !strings.Contains(r.out, want) {
				t.Errorf("want %q in:\n%s", want, r.out)
			}
		})
	}
}
