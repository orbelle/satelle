package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// changelogItems runs changelog-items.sh with args, stdin on its input, and
// returns its stdout, stderr and exit code.
func changelogItems(t *testing.T, stdin string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{"changelog-items.sh"}, args...)...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("changelog-items.sh %v: %v", args, err)
		}
		code = ee.ExitCode()
	}
	return out.String(), errb.String(), code
}

// attachment writes an item's changelog attachment to a file and returns
// the <id>:<file> argument naming it.
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
	// A section may repeat; its entries are kept in order.
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
	// A malformed attachment refuses the whole render.
	bad := attachment(t, dir, "sto_c", "### Misc\n- odd\n")
	if out, errOut, code := changelogItems(t, "", "render", "1.2.3", "2026-11-07", a, bad); code != 1 || out != "" || !strings.Contains(errOut, "unknown section") {
		t.Fatalf("malformed: exit %d\n%s%s", code, out, errOut)
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
