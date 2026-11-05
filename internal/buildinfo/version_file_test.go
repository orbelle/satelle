package buildinfo_test

// Test-only guards on this repo's .version and the skills that write and check
// its build stamp (sto_fa7df85b). Like toolchain_pins_test.go they know paths
// that exist because of how this repo is built, so they stay out of the binary.

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func versionFields(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, line := range strings.Split(readRepoFile(t, ".version"), "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

var semver = regexp.MustCompile(`^([0-9]+)\.[0-9]+\.[0-9]+$`)

// .version carries v1-or-later versions for both binaries and an RFC 3339 UTC
// build stamp. The values themselves are pinned by the commit that sets them;
// this checks the format so it survives the next release.
func TestVersionFileFormat(t *testing.T) {
	f := versionFields(t)
	for _, k := range []string{"satelle.version", "satelled.version"} {
		m := semver.FindStringSubmatch(f[k])
		if m == nil {
			t.Fatalf("%s = %q, want MAJOR.MINOR.PATCH", k, f[k])
		}
		if major, _ := strconv.Atoi(m[1]); major < 1 {
			t.Errorf("%s = %q, want major >= 1 (v1)", k, f[k])
		}
	}
	build := f["satelle.build"]
	ts, err := time.Parse(time.RFC3339, build)
	if err != nil || !strings.HasSuffix(build, "Z") || ts.Location() != time.UTC {
		t.Fatalf("satelle.build = %q, want an RFC 3339 UTC datetime (YYYY-MM-DDTHH:MM:SSZ): %v", build, err)
	}
}

// The version-stamp check accepts .version's stamp and refuses the old format,
// and the release skill writes the format the check reads.
func TestBuildStampFormatAgrees(t *testing.T) {
	check := readRepoFile(t, ".satelle/skills/satelle-version-stamp-check.md")
	m := regexp.MustCompile(`"\$BUILD" \| grep -qE '([^']+)'`).FindStringSubmatch(check)
	if m == nil {
		t.Fatal("no grep -qE pattern in the version-stamp check")
	}
	re, err := regexp.Compile(m[1])
	if err != nil {
		t.Fatal(err)
	}
	if b := versionFields(t)["satelle.build"]; !re.MatchString(b) {
		t.Errorf("the check's pattern %q refuses .version's stamp %q", m[1], b)
	}
	if re.MatchString("2026-11-05-06-59-14") {
		t.Errorf("the check's pattern %q still accepts the old stamp format", m[1])
	}
	const want, old = "%Y-%m-%dT%H:%M:%SZ", "%Y-%m-%d-%H-%M-%S"
	for _, rel := range []string{".satelle/skills/release.md", ".satelle/skills/satelle-version-stamp-check.md"} {
		body := readRepoFile(t, rel)
		if !strings.Contains(body, want) || strings.Contains(body, old) {
			t.Errorf("%s must write the stamp as date -u +%s and never %s", rel, want, old)
		}
	}
}
