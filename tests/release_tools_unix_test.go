//go:build integration && unix

package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const releaseToolsScript = ".satelle/skills/satelle-release-node/scripts/tools.sh"

func TestReleaseToolsPinTheirDigests(t *testing.T) {
	text := repoText(t, releaseToolsScript)
	for _, name := range []string{"GO_SHA256", "GH_SHA256"} {
		if !regexp.MustCompile(`(?m)^` + name + `=[0-9a-f]{64}$`).MatchString(text) {
			t.Errorf("tools.sh pins no %s digest", name)
		}
	}
	if strings.Contains(text, "checksums.txt") || strings.Contains(text, "mode=json") {
		t.Error("tools.sh fetches a digest at run time")
	}
	check, untar := strings.Index(text, "sha256sum -c"), strings.Index(text, "tar -C")
	if check < 0 || untar < 0 || check > untar {
		t.Error("tools.sh does not check each tarball with sha256sum -c before it extracts one")
	}
	if gv := regexp.MustCompile(`(?m)^\s*go-version:\s*'([0-9.]+)'`).FindStringSubmatch(repoText(t, ".github/workflows/release.yml")); gv == nil ||
		!strings.Contains(text, "GO_VERSION="+gv[1]+".") && !strings.Contains(text, "GO_VERSION="+gv[1]+"\n") {
		t.Errorf("tools.sh's go is not release.yml's go-version %v", gv)
	}
}

func TestReleaseToolsRefuseAMismatchedTarball(t *testing.T) {
	prefix, bin := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(bin, "curl"), `#!/bin/sh
out=; prev=
for a in "$@"; do [ "$prev" = -o ] && out=$a; prev=$a; done
printf 'not the pinned tarball\n' >"$out"
`, 0o755)
	script, err := filepath.Abs("../" + releaseToolsScript)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", script)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "SATELLE_TOOLS_PREFIX="+prefix,
		"SATELLE_TOOLS_SKIP_APT=1", "SATELLE_TOOLS_SKIP_USER=1")
	out, err := cmd.CombinedOutput()
	if err == nil || cmd.ProcessState.ExitCode() != 1 || !strings.Contains(string(out), "tools: go tarball sha256 mismatch") {
		t.Fatalf("tools.sh with a mismatched tarball: %v\n%s", err, out)
	}
	for _, p := range []string{"usr/local/go", "usr/local/bin/gh"} {
		if _, err := os.Stat(filepath.Join(prefix, p)); !os.IsNotExist(err) {
			t.Fatalf("%s exists after a refused tarball: %v", p, err)
		}
	}
}
