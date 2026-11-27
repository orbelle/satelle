//go:build !windows

package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gittest"
)

func integrationCheckScript(t *testing.T) string {
	t.Helper()
	doc := readText(t, "../.satelle/skills/satelle-integration-check.md")
	_, rest, ok := strings.Cut(doc, "```check\n")
	if !ok {
		t.Fatal("satelle-integration-check.md has no check block")
	}
	body, _, ok := strings.Cut(rest, "\n```")
	if !ok {
		t.Fatal("satelle-integration-check.md's check block is not closed")
	}
	return body + "\n"
}

func checkPath(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	for _, tool := range []string{"bash", "sh", "go", "git", "make", "sed", "awk", "head", "stat", "readlink", "echo"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Fatalf("%s is required and is not on PATH", tool)
		}
		if err := os.Symlink(p, filepath.Join(bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	return bin
}

func runIntegrationCheck(t *testing.T, r *fixtureRepo) (string, int) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "check.sh")
	if err := os.WriteFile(script, []byte(integrationCheckScript(t)), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script)
	cmd.Dir = r.dir
	cmd.Env = append(append(os.Environ(), gittest.Env()...), "PATH="+checkPath(t), "GOFLAGS=", "GOWORK=off")
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("integration check: %v", err)
		}
		code = ee.ExitCode()
	}
	return string(out), code
}

func releaseTargetsRepo(t *testing.T, extra map[string]string) *fixtureRepo {
	t.Helper()
	_, setupGo := fixtureGoVersion(t)
	yml := strings.Replace(fixtureReleaseYML(setupGo, ""),
		"          - { artifact: satelled, pkg: ./cmd/satelled, goos: linux,   goarch: amd64 }\n",
		"          - { artifact: satelle,  pkg: ./cmd/satelle,  goos: darwin,  goarch: arm64 }\n"+
			"          - { artifact: satelled, pkg: ./cmd/satelled, goos: linux,   goarch: amd64 }\n"+
			"          - { artifact: satelled, pkg: ./cmd/satelled, goos: darwin,  goarch: arm64 }\n", 1)
	files := map[string]string{
		".github/workflows/release.yml": yml,
		"Makefile":                      "test:\n\t@echo suite ran\n",
	}
	for p, b := range extra {
		files[p] = b
	}
	return newFixtureRepo(t, files)
}

func TestIntegrationCheckRejectsAReleaseTargetThatDoesNotBuild(t *testing.T) {
	r := releaseTargetsRepo(t, map[string]string{
		"internal/a/stat_unix.go": "//go:build unix\n\npackage a\n\nimport \"syscall\"\n\nfunc Ctime(st *syscall.Stat_t) int64 { return st.Ctim.Sec }\n",
	})
	out, code := runIntegrationCheck(t, r)
	if code == 0 {
		t.Fatalf("integration check accepted a head that does not build for darwin:\n%s", out)
	}
	for _, want := range []string{
		"release target darwin/arm64 does not build ./cmd/satelle:",
		"release target darwin/arm64 does not build ./cmd/satelled:",
		"st.Ctim undefined",
		"OBJECTIVE 2 FAILED (release targets)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("integration check output lacks %q:\n%s", want, out)
		}
	}
	for _, ok := range []string{"release target linux/amd64 builds ./cmd/satelle", "release target windows/amd64 builds ./cmd/satelled"} {
		if !strings.Contains(out, ok) {
			t.Errorf("integration check output lacks %q:\n%s", ok, out)
		}
	}
	if strings.Contains(out, "suite ran") {
		t.Errorf("integration check ran the suite after a release target failed:\n%s", out)
	}
}

func TestIntegrationCheckAcceptsAHeadThatBuildsForEveryReleaseTarget(t *testing.T) {
	r := releaseTargetsRepo(t, nil)
	out, code := runIntegrationCheck(t, r)
	if code != 0 {
		t.Fatalf("integration check exited %d on a head that builds for every target:\n%s", code, out)
	}
	for _, want := range []string{
		"release target darwin/arm64 builds ./cmd/satelle",
		"release target windows/amd64 builds ./cmd/satelled",
		"suite ran",
		"objective 2 PASS",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("integration check output lacks %q:\n%s", want, out)
		}
	}
}

func TestReleaseTargetsListReadsThisReleaseYML(t *testing.T) {
	out, errb, code := runBashScript(t, "release-targets.sh", "", "list", "../.github/workflows/release.yml")
	if code != 0 {
		t.Fatalf("list exited %d: %s", code, errb)
	}
	for _, want := range []string{"./cmd/satelle darwin arm64", "./cmd/satelled darwin amd64", "./cmd/satelle windows amd64"} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("list lacks %q:\n%s", want, out)
		}
	}
}

func TestReleaseTargetsListRefusesAnEntryWithoutATarget(t *testing.T) {
	yml := filepath.Join(t.TempDir(), "release.yml")
	if err := os.WriteFile(yml, []byte("        include:\n          - { artifact: satelle, pkg: ./cmd/satelle, goos: linux }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, errb, code := runBashScript(t, "release-targets.sh", "", "list", yml)
	if code != 1 || !strings.Contains(errb, "a release.yml include entry lacks pkg, goos or goarch") {
		t.Fatalf("list on an entry with no goarch: exit %d: %s", code, errb)
	}
}

func TestReleaseTargetsRefusesAnEntryThatIsNotAModulePackage(t *testing.T) {
	for _, entry := range []string{
		"pkg: -toolexec=/bin/true, goos: linux, goarch: amd64",
		"pkg: -o=/tmp/x, goos: linux, goarch: amd64",
		"pkg: /abs/pkg, goos: linux, goarch: amd64",
		"pkg: ../x, goos: linux, goarch: amd64",
		"pkg: ./cmd/../../x, goos: linux, goarch: amd64",
		"pkg: ./cmd/satelle, goos: -toolexec=/bin/true, goarch: amd64",
		"pkg: ./cmd/satelle, goos: linux, goarch: AMD64",
	} {
		dir := t.TempDir()
		yml := filepath.Join(dir, ".github", "workflows", "release.yml")
		if err := os.MkdirAll(filepath.Dir(yml), 0o755); err != nil {
			t.Fatal(err)
		}
		body := "    env:\n      CGO_ENABLED: \"0\"\n        include:\n          - { artifact: satelle, " + entry + " }\n"
		if err := os.WriteFile(yml, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		want := "a release.yml include entry names a pkg that is not a ./ package path in the module"
		out, errb, code := runBashScript(t, "release-targets.sh", "", "list", yml)
		if code != 1 || !strings.Contains(errb, want) || out != "" {
			t.Errorf("list on %q: exit %d, stdout %q: %s", entry, code, out, errb)
		}
		out, errb, code = runBashScript(t, "release-targets.sh", "", "build", dir)
		if code != 1 || !strings.Contains(errb, want) || strings.Contains(out, "release target") {
			t.Errorf("build on %q: exit %d, stdout %q: %s", entry, code, out, errb)
		}
	}
}
