//go:build integration && linux

package tests

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/remote/provider"
)

const plainProbeImage = "debian:stable-slim"

const plainProbeWorkflow = `name = "plain"

[states.backlog]
start = true

[states.done]
terminal = true

[gates.plain-tool]
skill = "plain-tool-check"

[[transition]]
from = ["backlog"]
to = "done"
groups = [{ gates = ["plain-tool"] }]
`

const plainProbeCheck = `---
name: plain-tool-check
type: check
description: Proves the node's setup installed its tool and that satelle was there before setup.
---

` + "```sh\nprobe-tool && cat /tmp/setup-saw && satelle --version\n```\n"

const plainProbeSetup = `set -eu
satelle --version > /tmp/setup-saw
printf '#!/bin/sh\necho installed by setup\n' > /usr/local/bin/probe-tool
chmod +x /usr/local/bin/probe-tool
`

func TestRemoteFireOnAPlainImage(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	d := provider.DockerLocal{}
	if err := d.Available(ctx); err != nil {
		t.Skipf("docker daemon unavailable: %v", err)
	}
	if !d.HasImage(ctx, plainProbeImage) {
		if out, err := exec.CommandContext(ctx, "docker", "pull", plainProbeImage).CombinedOutput(); err != nil {
			t.Fatalf("docker pull %s: %v\n%s", plainProbeImage, err, out)
		}
	}

	s, _, _ := dockerLocalSandbox(t, false)
	for _, k := range []string{"DOCKER_HOST", "DOCKER_CONFIG", "XDG_RUNTIME_DIR"} {
		if v, ok := os.LookupEnv(k); ok {
			s.env[k] = v
		}
	}
	if _, ok := s.env["DOCKER_CONFIG"]; !ok {
		if home, err := os.UserHomeDir(); err == nil {
			s.env["DOCKER_CONFIG"] = filepath.Join(home, ".docker")
		}
	}
	s.write(".satelle/workflows/plain.toml", plainProbeWorkflow)
	s.write(".satelle/skills/plain-tool-check.md", plainProbeCheck)
	s.write(".satelle/remotes/plain/satelle.toml", "[workflows]\ndefault = \"plain\"\n")
	s.write(".satelle/remotes/plain/setup.sh", plainProbeSetup)
	task := s.create("task", "--title", "Probe a plain image", "--input-kind", "story", "--input-tag", "remote-probe").ID
	s.create("story", "--title", "a probe input", "--tag", "remote-probe")
	s.write(".satelle/remotes/plain/remote.toml", `version = 1
name = "plain"
provider = "docker-local"
satelle_version = "1.0.0"

[machine]
image = "`+plainProbeImage+`"

[drive]
kind = "task"
task = "`+task+`"

[setup]
on_create = ["sh .satelle/remotes/plain/setup.sh"]

[lifecycle]
scope = "run"
`)
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "plain probe")

	r := s.run("remote", "fire", "plain", task)
	if m := nodeLine.FindStringSubmatch(r.out); m != nil {
		t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", m[2]).Run() })
	}
	t.Logf("fire:\n%s%s", r.out, r.err)
	if r.code != 0 {
		t.Fatalf("fire on %s: exit %d\n%s%s", plainProbeImage, r.code, r.out, r.err)
	}
	m := nodeLine.FindStringSubmatch(r.out)
	if m == nil {
		t.Fatalf("fire output names no node:\n%s", r.out)
	}
	node := m[1]
	if !strings.Contains(r.out, "copying satelle into the node at /usr/local/bin/satelle") ||
		!strings.Contains(r.out, "plain on "+task+": concluded (node "+node+")") || !strings.Contains(r.out, "verdict: accept") {
		t.Fatalf("fire output:\n%s", r.out)
	}

	vout, err := exec.Command(satelleBin, "--version").Output()
	if err != nil {
		t.Fatal(err)
	}
	version := strings.TrimSpace(string(vout))
	execs := executionsOf(s, task)
	if len(execs) != 1 || execs[0].Status != "done" {
		t.Fatalf("executions of %s = %+v, want one at done", task, execs)
	}
	var notes string
	for _, e := range chainOf(s, execs[0].ID) {
		if e.Actor != "node:"+node {
			t.Fatalf("execution event %s by %q, want node:%s", e.Kind, e.Actor, node)
		}
		if e.Kind == "gate.run" && gateOf(t, e) == "plain-tool" {
			notes = payloadField(t, e, "notes")
		}
	}
	if !strings.Contains(notes, "installed by setup") || strings.Count(notes, version) != 2 {
		t.Fatalf("the plain-tool gate's notes do not show setup's tool and %q twice:\n%s", version, notes)
	}
	if v := s.must("ledger", "verify"); !strings.Contains(v, "chains intact") {
		t.Fatalf("ledger verify: %s", v)
	}
	if st := s.must("remote", "status", "plain", task); !strings.HasPrefix(st, "plain on "+task+": concluded (node "+node+")\nverdict: accept\n") {
		t.Fatalf("status:\n%s", st)
	}
}
