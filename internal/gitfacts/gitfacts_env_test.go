//go:build unix

package gitfacts

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func fakeGit(t *testing.T) (bin, log string) {
	t.Helper()
	bin = t.TempDir()
	log = filepath.Join(t.TempDir(), "env")
	script := "#!/bin/sh\nenv > '" + log + "'\necho /top\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func TestRepoEnvIsTheWholeEnvironment(t *testing.T) {
	bin, log := fakeGit(t)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_PROBE_VALUE", "s3cret")
	env := []string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=/nowhere"}
	if top, err := (Repo{Dir: t.TempDir(), Env: env}).TopLevel(); err != nil || top != "/top" {
		t.Fatalf("TopLevel = %q, %v", top, err)
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if name, _, _ := strings.Cut(line, "="); name != "PWD" && name != "SHLVL" && name != "_" && name != "OLDPWD" {
			got = append(got, line)
		}
	}
	want := append(slices.Clone(env), "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("git ran with %q, want %q", got, want)
	}
}
