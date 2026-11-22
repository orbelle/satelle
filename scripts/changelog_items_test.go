//go:build !windows

package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runBashScript(t *testing.T, script, stdin string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{script}, args...)...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("%s %v: %v", script, args, err)
		}
		code = ee.ExitCode()
	}
	return out.String(), errb.String(), code
}

func changelogItems(t *testing.T, stdin string, args ...string) (string, string, int) {
	t.Helper()
	return runBashScript(t, "changelog-items.sh", stdin, args...)
}

func attachment(t *testing.T, dir, id, body string) string {
	t.Helper()
	p := filepath.Join(dir, id)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return id + ":" + p
}

func TestLinesAcceptsEverySection(t *testing.T) {
	sections := []string{"Added", "Changed", "Deprecated", "Removed", "Fixed", "Security", "Breaking"}
	var in, want strings.Builder
	for _, s := range sections {
		in.WriteString("### " + s + "\n- a " + s + " entry\n\n")
		want.WriteString(s + "\t- a " + s + " entry (sto_1)\n")
	}
	out, errOut, code := changelogItems(t, in.String(), "lines", "sto_1")
	if code != 0 || out != want.String() {
		t.Fatalf("exit %d\n%s%s\nwant\n%s", code, out, errOut, want.String())
	}
	out, _, code = changelogItems(t, "### Added\n- one\n### Fixed\n- two\n### Added\n- three\n", "lines", "sto_1")
	if code != 0 || out != "Added\t- one (sto_1)\nFixed\t- two (sto_1)\nAdded\t- three (sto_1)\n" {
		t.Fatalf("repeated section: exit %d\n%s", code, out)
	}
}

func TestLinesRefuses(t *testing.T) {
	for _, tc := range []struct{ name, in, line string }{
		{"empty", "\n\n", "no entries"},
		{"entry before heading", "- early\n### Added\n- fine\n", "line 1: an entry before any ### heading: - early"},
		{"unknown section", "### Added\n- fine\n### Misc\n- odd\n", "line 3: unknown section"},
		{"continuation", "### Added\n- first\n  continued\n", "line 3: not a ### heading or a single - entry line:   continued"},
		{"empty entry", "### Fixed\n- fine\n- \n", "line 3: an empty entry"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, errOut, code := changelogItems(t, tc.in, "lines", "sto_1")
			if code != 1 || out != "" || !strings.Contains(errOut, tc.line) {
				t.Fatalf("exit %d stdout %q stderr %q, want exit 1 naming %q", code, out, errOut, tc.line)
			}
		})
	}
}

func TestLinesAppendsIDOnce(t *testing.T) {
	out, _, code := changelogItems(t, "### Changed\n- already named (sto_1)\n- shared (sto_0, sto_1)\n- not named\n", "lines", "sto_1")
	want := "Changed\t- already named (sto_1)\nChanged\t- shared (sto_0, sto_1)\nChanged\t- not named (sto_1)\n"
	if code != 0 || out != want {
		t.Fatalf("exit %d\n%s\nwant\n%s", code, out, want)
	}
}

func TestRenderOrdersSectionsAndItems(t *testing.T) {
	dir := t.TempDir()
	a := attachment(t, dir, "sto_a", "### Fixed\n- a fix\n### Added\n- a feature\n")
	b := attachment(t, dir, "sto_b", "### Breaking\n- b break\n### Changed\n- b change (sto_b)\n### Added\n- b feature\n")
	out, errOut, code := changelogItems(t, "", "render", "1.2.3", "2026-11-07", a, b)
	want := `## [1.2.3] - 2026-11-07

### Added
- a feature (sto_a)
- b feature (sto_b)

### Changed
- b change (sto_b)

### Fixed
- a fix (sto_a)

### Breaking
- b break (sto_b)

`
	if code != 0 || out != want {
		t.Fatalf("exit %d\n%s%s\nwant\n%s", code, out, errOut, want)
	}
	bad := attachment(t, dir, "sto_c", "### Misc\n- odd\n")
	if out, errOut, code := changelogItems(t, "", "render", "1.2.3", "2026-11-07", a, bad); code != 1 || out != "" || !strings.Contains(errOut, "unknown section") {
		t.Fatalf("malformed: exit %d\n%s%s", code, out, errOut)
	}
}

func TestWriteReleaseNotes(t *testing.T) {
	dir := t.TempDir()
	a := attachment(t, dir, "sto_a", "### Fixed\n- a fix\n### Added\n- a feature\n")
	b := attachment(t, dir, "sto_b", "### Breaking\n- b break\n### Changed\n- b change (sto_b)\n### Added\n- b feature\n")
	log := filepath.Join(dir, "CHANGELOG.md")
	archive := "## [1.2.2] - 2026-11-01\n\n### Added\n- old (sto_0)\n"
	if err := os.WriteFile(log, []byte(archive), 0o644); err != nil {
		t.Fatal(err)
	}
	notes := filepath.Join(dir, "docs", "releases", "v1.2.3.md")
	out, errOut, code := changelogItems(t, "", "write", notes, "1.2.3", "2026-11-07", a, b)
	want := `## [1.2.3] - 2026-11-07

### Added
- a feature (sto_a)
- b feature (sto_b)

### Changed
- b change (sto_b)

### Fixed
- a fix (sto_a)

### Breaking
- b break (sto_b)
`
	got, _ := os.ReadFile(notes)
	if code != 0 || string(got) != want {
		t.Fatalf("exit %d\n%s%s\nfile\n%q\nwant\n%q", code, out, errOut, got, want)
	}
	if after, _ := os.ReadFile(log); string(after) != archive {
		t.Fatalf("write changed CHANGELOG.md:\n%s", after)
	}
	for _, item := range []string{a, b} {
		if out, errOut, code := changelogItems(t, "", "has", notes, "1.2.3", item); code != 0 {
			t.Fatalf("has %s: exit %d\n%s%s", item, code, out, errOut)
		}
	}
	if _, errOut, code := changelogItems(t, "", "write", notes, "1.2.3", "2026-11-08", a); code != 1 || !strings.Contains(errOut, "exists") {
		t.Fatalf("second write: exit %d\n%s", code, errOut)
	}
	if again, _ := os.ReadFile(notes); string(again) != want {
		t.Fatalf("second write changed the notes:\n%s", again)
	}
	bad := attachment(t, dir, "sto_c", "### Misc\n- odd\n")
	other := filepath.Join(dir, "docs", "releases", "v1.2.4.md")
	if _, errOut, code := changelogItems(t, "", "write", other, "1.2.4", "2026-11-08", a, bad); code != 1 || !strings.Contains(errOut, "unknown section") {
		t.Fatalf("malformed: exit %d\n%s", code, errOut)
	}
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Fatalf("a malformed attachment created %s: %v", other, err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(other)); len(entries) != 1 {
		t.Fatalf("docs/releases holds %d entries, want only v1.2.3.md", len(entries))
	}
}

func TestHas(t *testing.T) {
	dir := t.TempDir()
	a := attachment(t, dir, "sto_a", "### Added\n- a feature\n### Fixed\n- a fix\n")
	log := filepath.Join(dir, "CHANGELOG.md")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(log, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("## [1.0.1] - 2026-11-07\n\n### Added\n- other (sto_z)\n- a feature (sto_a)\n\n### Fixed\n- a fix (sto_a)\n\n## [1.0.0] - 2026-01-01\n\n### Added\n- old\n")
	if out, errOut, code := changelogItems(t, "", "has", log, "1.0.1", a); code != 0 {
		t.Fatalf("accepts: exit %d\n%s%s", code, out, errOut)
	}
	for _, tc := range []struct{ name, body, ver, want string }{
		{"missing entry", "## [1.0.1] - 2026-11-07\n\n### Added\n- a feature (sto_a)\n", "1.0.1", `no "- a fix (sto_a)" under ### Fixed`},
		{"wrong section", "## [1.0.1] - 2026-11-07\n\n### Added\n- a feature (sto_a)\n### Changed\n- a fix (sto_a)\n", "1.0.1", `no "- a fix (sto_a)" under ### Fixed`},
		{"other version", "## [1.0.1] - 2026-11-07\n\n### Added\n- a feature (sto_a)\n\n## [1.0.0] - 2026-01-01\n\n### Fixed\n- a fix (sto_a)\n", "1.0.1", `no "- a fix (sto_a)" under ### Fixed`},
		{"no section", "## [1.0.0] - 2026-01-01\n\n### Added\n- a feature (sto_a)\n### Fixed\n- a fix (sto_a)\n", "1.0.1", `has no "## [1.0.1] - <date>" section`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			write(tc.body)
			out, errOut, code := changelogItems(t, "", "has", log, tc.ver, a)
			if code != 1 || !strings.Contains(errOut, tc.want) {
				t.Fatalf("exit %d\n%s%s\nwant %q", code, out, errOut, tc.want)
			}
		})
	}
}

func TestSkillChangelogLinesMatchesChangelogItems(t *testing.T) {
	skillScript := filepath.Join("..", ".satelle", "skills", "satelle-pr-ready-check", "scripts", "changelog-lines.sh")
	var allSections strings.Builder
	for _, s := range []string{"Added", "Changed", "Deprecated", "Removed", "Fixed", "Security", "Breaking"} {
		allSections.WriteString("### " + s + "\n- a " + s + " entry\n\n")
	}
	for _, tc := range []struct {
		name string
		in   string
		code int
	}{
		{"every section", allSections.String(), 0},
		{"repeated section", "### Added\n- one\n### Fixed\n- two\n### Added\n- three\n", 0},
		{"id already present", "### Changed\n- already named (sto_1)\n- shared (sto_0, sto_1)\n- not named\n", 0},
		{"crlf and spaces", "### Added  \r\n-   trimmed entry  \r\n\r\n", 0},
		{"empty", "\n\n", 1},
		{"only heading", "### Added\n", 1},
		{"entry before heading", "- early\n### Added\n- fine\n", 1},
		{"unknown section", "### Added\n- fine\n### Misc\n- odd\n", 1},
		{"continuation", "### Added\n- first\n  continued\n", 1},
		{"empty entry", "### Fixed\n- fine\n- \n", 1},
		{"bare dash", "### Fixed\n-\n", 1},
		{"heading without space", "###Added\n- x\n", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantOut, wantErr, wantCode := changelogItems(t, tc.in, "lines", "sto_1")
			if wantCode != tc.code {
				t.Fatalf("changelog-items.sh exit %d, want %d\nstdout %q\nstderr %q", wantCode, tc.code, wantOut, wantErr)
			}
			gotOut, gotErr, gotCode := runBashScript(t, skillScript, tc.in, "sto_1")
			if gotCode != wantCode || gotOut != wantOut || gotErr != wantErr {
				t.Fatalf("parity mismatch:\nchangelog-items.sh: exit %d stdout %q stderr %q\nchangelog-lines.sh: exit %d stdout %q stderr %q",
					wantCode, wantOut, wantErr, gotCode, gotOut, gotErr)
			}
		})
	}
	for _, args := range [][]string{nil, {""}, {"sto_1", "extra"}} {
		_, _, wantCode := changelogItems(t, "### Added\n- x\n", append([]string{"lines"}, args...)...)
		_, _, gotCode := runBashScript(t, skillScript, "### Added\n- x\n", args...)
		if wantCode != 2 || gotCode != 2 {
			t.Fatalf("args %v: changelog-items.sh exit %d, changelog-lines.sh exit %d, want 2", args, wantCode, gotCode)
		}
	}
}
