//go:build integration && unix

package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// driftCheckSandbox is a fresh repo whose substrate names only real commands,
// flags and topics, links an existing principle and an existing skill, and
// holds prose, a placeholder and a TOML [[transition]] that are not misses.
func driftCheckSandbox(t *testing.T) *sandbox {
	t.Helper()
	for _, tool := range []string{"bash", "jq", "git", "awk"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s is required by scripts/drift-check.sh and is not on PATH", tool)
		}
	}
	s := newSandbox(t)
	s.env["SATELLE_BIN"] = satelleBin
	s.must("init")
	s.write(".satelle/principles/good.md", `---
name: good
type: principle
description: A principle that names only what the binary has.
---

Read a story with `+"`satelle story get <id> --json`"+`; satelle runs them, and
`+"`satelle <command>`"+` is any command. See [[helper]] and [[good]].

    satelle help workflows

A TOML table such as `+"`[[transition]]`"+` is not a link.
`)
	s.write(".satelle/skills/helper.md", "---\nname: helper\ntype: skill\ndescription: A helper.\n---\n\n```sh\nsatelle story list --open --json | jq length\nsatelle execution seat \"$exe\" --base \"v$prev\"\n```\n\nThen `satelle validate`.\n")
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "substrate")
	return s
}

func driftCheck(t *testing.T, s *sandbox) result {
	t.Helper()
	script, err := filepath.Abs("../scripts/drift-check.sh")
	if err != nil {
		t.Fatal(err)
	}
	return s.exec("bash", "", script)
}

// A substrate that names only what the binary has: no line, exit 0.
func TestDriftCheckCleanFixtureExits0(t *testing.T) {
	t.Parallel()
	s := driftCheckSandbox(t)
	if r := driftCheck(t, s); r.code != 0 || r.out != "" {
		t.Fatalf("clean substrate: exit %d\n%s%s", r.code, r.out, r.err)
	}
}

// One planted miss of each kind: exactly one line each, exit 1. The script
// holds no rule: it prints satelle validate's substrate findings and maps its
// configuration problems (sto_a1681009 AC2).
func TestDriftCheckReportsEachKind(t *testing.T) {
	t.Parallel()
	s := driftCheckSandbox(t)
	s.write(".satelle/tasks/README.md", "Archive with `satelle task archive <id>`.\n")
	s.write(".satelle/skills/flag.md", "---\nname: flag\ntype: skill\ndescription: f\n---\n\nList with `satelle ledger list --story <id>`.\n")
	s.write(".satelle/skills/topic.md", "---\nname: topic\ntype: skill\ndescription: t\n---\n\nSee `satelle help no-such-topic`.\n")
	s.write(".satelle/principles/dead.md", "---\nname: dead\ntype: principle\ndescription: d\n---\n\nShip small ([[satelle-start-small]]).\n")
	wf, err := os.ReadFile(filepath.Join(s.dir, ".satelle/workflows/default.toml"))
	if err != nil {
		t.Fatal(err)
	}
	s.write(".satelle/workflows/default.toml", string(wf)+"\n[gates.ghost]\nskill = \"no-such-skill\"\n")
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "drift")

	r := driftCheck(t, s)
	if r.code != 1 {
		t.Fatalf("planted misses: exit %d, want 1\n%s%s", r.code, r.out, r.err)
	}
	lines := strings.Split(strings.TrimSpace(r.out), "\n")
	sort.Strings(lines)
	want := []string{
		".satelle/principles/dead.md:7: link: satelle-start-small — ",
		".satelle/skills/flag.md:7: flag: satelle ledger list --story — ",
		".satelle/skills/topic.md:7: topic: satelle help no-such-topic — ",
		".satelle/tasks/README.md:1: command: satelle task archive — ",
		".satelle/workflows/default.toml:",
	}
	if len(lines) != len(want) {
		t.Fatalf("want %d lines, one per kind, got %d:\n%s", len(want), len(lines), r.out)
	}
	for i, w := range want {
		if !strings.HasPrefix(lines[i], w) {
			t.Errorf("line %d = %q, want prefix %q", i, lines[i], w)
		}
	}
	if !strings.Contains(lines[4], ": skill: no-such-skill — ") {
		t.Errorf("workflow line = %q, want a skill miss naming no-such-skill", lines[4])
	}
}

// No satelle on PATH: the script cannot check and exits 2. The PATH holds
// only links to the tools it needs before that check.
func TestDriftCheckNoBinaryExits2(t *testing.T) {
	t.Parallel()
	s := newSandbox(t)
	script, err := filepath.Abs("../scripts/drift-check.sh")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	for _, tool := range []string{"bash", "git", "jq"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Fatalf("%s is required and is not on PATH", tool)
		}
		if err := os.Symlink(p, filepath.Join(bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(filepath.Join(bin, "bash"), script)
	cmd.Dir = s.dir
	cmd.Env = []string{"PATH=" + bin, "HOME=" + s.home}
	out, err := cmd.CombinedOutput()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 2 || !strings.Contains(string(out), "satelle is not on PATH") {
		t.Fatalf("no satelle on PATH: %v\n%s", err, out)
	}
}

// A binary whose validate --json has no substrate list (older than the
// substrate check) cannot check: exit 2, fail closed.
func TestDriftCheckOldBinaryExits2(t *testing.T) {
	t.Parallel()
	s := driftCheckSandbox(t)
	fake := filepath.Join(t.TempDir(), "satelle")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho '{\"problems\":[]}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.env["SATELLE_BIN"] = fake
	r := driftCheck(t, s)
	if r.code != 2 || !strings.Contains(r.err, "no problems or substrate list") {
		t.Fatalf("old binary: exit %d, want 2\n%s%s", r.code, r.out, r.err)
	}
}
