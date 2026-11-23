package remotenode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/remote/provider"
)

const egressPrefix = "[network] allow cannot be enforced here: "

func archDocker(t *testing.T, info string) (script, log string) {
	t.Helper()
	answer := "echo " + info
	if info == "" {
		answer = "echo no daemon; exit 1"
	}
	return fakeDockerFor(t, "case \"$1\" in\n  info) "+answer+" ;;\n  *) exit 0 ;;\nesac\n")
}

func noNodeStarted(t *testing.T, log string) {
	t.Helper()
	for _, l := range logLines(t, log) {
		if strings.HasPrefix(l, "run -d") || strings.HasPrefix(l, "network create") || strings.HasPrefix(l, "build") {
			t.Fatalf("docker ran %q before refusing", l)
		}
	}
}

func TestFireLocalRefusesAnUnenforceableAllowList(t *testing.T) {
	sh := filepath.Join(t.TempDir(), "satelle")
	if err := os.WriteFile(sh, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, egress, info, reason string
	}{
		{name: "no egress binary", info: hostUname(t), reason: "no satelle binary"},
		{name: "not a linux binary", egress: sh, info: hostUname(t), reason: "not a linux binary"},
		{name: "docker info fails", egress: testBinary(t), reason: "docker info"},
		{name: "an architecture satelle does not build", egress: testBinary(t), info: "riscv64", reason: "the docker host reports architecture riscv64"},
		{name: "another architecture", egress: testBinary(t), info: otherUname(t), reason: "the docker host is linux/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTaskFixture(t, "echo checked")
			script, log := archDocker(t, tc.info)
			o := f.def
			o.Definition.Network.Allow = []string{"example.com"}
			o.Docker = provider.DockerLocal{Docker: script, Egress: tc.egress}
			o.Runs = LocalRuns{Dir: t.TempDir()}
			o.Binary, o.BinaryVersion = testBinary(t), plainVersion
			_, err := FireLocal(context.Background(), o)
			if err == nil || !strings.Contains(err.Error(), egressPrefix) || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("FireLocal = %v; want %q and %q", err, egressPrefix, tc.reason)
			}
			noNodeStarted(t, log)
			if _, err := o.Runs.Load(o.Remote, o.Item); err == nil {
				t.Fatal("a refused fire saved a run record")
			}
		})
	}
}

func TestGateCheckRefusesAnUnenforceableAllowList(t *testing.T) {
	f := newSpriteFixture(t)
	writeFiles(t, filepath.Join(f.dataDir, RemotesDir, "integration"), map[string]string{DefinitionFile: f.orig})
	script, log := archDocker(t, otherUname(t))
	g := GateChecker{Tree: f.tree, DataDir: f.dataDir, Docker: provider.DockerLocal{Docker: script, Egress: testBinary(t)}}
	_, err := g.RunRemote(testCtx(t), gate.RemoteCheck{Remote: "integration", Gate: "integration-check", Script: "exit 0", Env: gate.CheckEnv{Item: "sto_1"}, Head: f.head})
	if err == nil || !strings.Contains(err.Error(), egressPrefix+"the docker host is linux/") {
		t.Fatalf("RunRemote = %v", err)
	}
	noNodeStarted(t, log)
}
