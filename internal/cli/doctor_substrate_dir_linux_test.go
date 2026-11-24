//go:build linux

package cli

import (
	"crypto/sha256"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/substrate"
)

func substrateDirReport(t *testing.T) (issues []doctorIssue, notes []string) {
	t.Helper()
	out, _ := run(t, "", "doctor", "--json", "--session", "s-subdir")
	var rep doctorReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("doctor --json: %v\n%s", err, out)
	}
	for _, is := range rep.Issues {
		if strings.HasPrefix(is.Check, substrate.RelativeField) || strings.HasPrefix(is.Problem, substrate.RelativeField) {
			issues = append(issues, is)
		}
	}
	for _, n := range rep.Notes {
		if strings.HasPrefix(n, substrate.RelativeField+":") {
			notes = append(notes, n)
		}
	}
	return issues, notes
}

func treeHashes(t *testing.T, root string) map[string][32]byte {
	t.Helper()
	out := map[string][32]byte{}
	err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[p] = sha256.Sum256(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDoctorReportsNoRelativeSubstrateReadInAFreshRepo(t *testing.T) {
	isolate(t)
	must(t, "init")
	issues, notes := substrateDirReport(t)
	if len(issues) != 0 || len(notes) != 0 {
		t.Fatalf("a freshly initialised repo: want no %s finding, got issues %+v notes %q", substrate.RelativeField, issues, notes)
	}
}

func TestDoctorNotesEachRelativeSubstrateReadAndChangesNoFile(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	writeFile(t, filepath.Join(dir, ".satelle", "skills", "rel-check.md"), "---\nname: rel-check\n---\n\n```sh\ncat .satelle/doctor.toml\n```\n")
	writeFile(t, filepath.Join(dir, ".satelle", "tasks", "t.md"), "# t\n\n```bash\ntest -f .satelle/doctor.toml\n```\n")
	writeFile(t, filepath.Join(dir, ".satelle", "remotes", "r", "remote.toml"), "version = 1\n\n[setup]\non_create = [\"test -f .satelle/satelle.toml\"]\n")
	before := treeHashes(t, dir)
	issues, notes := substrateDirReport(t)
	want := []string{
		`substrate-dir: .satelle/remotes/r/remote.toml:4: reads .satelle/satelle.toml relative to the working directory; use "$SATELLE_SUBSTRATE_DIR/satelle.toml"`,
		`substrate-dir: .satelle/skills/rel-check.md:6: reads .satelle/doctor.toml relative to the working directory; use "$SATELLE_SUBSTRATE_DIR/doctor.toml"`,
		`substrate-dir: .satelle/tasks/t.md:4: reads .satelle/doctor.toml relative to the working directory; use "$SATELLE_SUBSTRATE_DIR/doctor.toml"`,
	}
	if len(issues) != 0 {
		t.Errorf("want no %s issue, got %+v", substrate.RelativeField, issues)
	}
	if strings.Join(notes, "\n") != strings.Join(want, "\n") {
		t.Errorf("notes =\n%s\nwant\n%s", strings.Join(notes, "\n"), strings.Join(want, "\n"))
	}
	after := treeHashes(t, dir)
	for p, h := range before {
		if after[p] != h {
			t.Errorf("doctor changed %s", p)
		}
	}
	for p := range after {
		if _, ok := before[p]; !ok && !strings.Contains(p, string(filepath.Separator)+".git"+string(filepath.Separator)) {
			t.Errorf("doctor wrote %s", p)
		}
	}
	if text, _ := run(t, "", "doctor", "--session", "s-subdir"); !strings.Contains(text, "note: "+want[1]+"\n") {
		t.Errorf("plain doctor does not print the note:\n%s", text)
	}
}

func TestSessionStartDoctorSkipsTheRelativeSubstrateCheck(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	in := loadDoctorInput(dir)
	for _, start := range []bool{false, true} {
		in.sessionStart = start
		found := false
		for _, c := range doctorChecks(in) {
			found = found || c.name == substrate.RelativeField
		}
		if found == start {
			t.Errorf("sessionStart=%v: the %s check runs = %v", start, substrate.RelativeField, found)
		}
	}
}
