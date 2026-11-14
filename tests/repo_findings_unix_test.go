//go:build integration && unix

package tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gittest"
	sub "github.com/bobmcallan/satelle/internal/substrate"
)

// repoFindingsGolden is what satelle validate and doctor report on this
// repo's tree, captured from the base binary on the base tree
// (sto_a5cdb024 AC4).
const repoFindingsGolden = "testdata/repo_findings.golden"

// The capture variables, read by this test process only and never passed to
// the binary: the checkout to copy instead of "..", the binary to run instead
// of satelleBin, and "1" to write the golden instead of comparing.
const (
	findingsTreeVar  = "SATELLE_FINDINGS_TREE"
	findingsBinVar   = "SATELLE_FINDINGS_BIN"
	findingsWriteVar = "SATELLE_FINDINGS_WRITE"
)

// findingsCaptureCommand is the command that writes the golden, recorded in
// its header.
const findingsCaptureCommand = findingsTreeVar + "=<base worktree> " + findingsBinVar + "=<binary built from it> " + findingsWriteVar + "=1 go test -tags integration -run '^TestRepoFindingsMatchTheRecordedBase$' ./tests/"

// machineChecks are doctor's checks that read the machine (installed
// binaries, the service unit and timer, the recorded sync pass, sessions),
// not the tree: their issues and notes are where build stamps and times
// appear, and none reads volatile.toml or doctor.toml.
var machineChecks = []string{"versions", "upgrade", "sync", "seats"}

// sto_a5cdb024 AC4: this repo's .satelle carries the dated and [resolve]
// keys, and satelle validate and doctor report on its tree exactly what the
// base binary reported on the base tree, recorded in the golden. Each binary
// only ever runs on its own tree, in a sandbox with a fresh home and ledger.
// The dated key is what the binary reads: a dated probe line is exempt with
// this repo's volatile.toml and reported once the key is removed.
func TestRepoFindingsMatchTheRecordedBase(t *testing.T) {
	tree := os.Getenv(findingsTreeVar)
	if tree == "" {
		tree = ".."
	}
	bin := satelleBin
	if b := os.Getenv(findingsBinVar); b != "" {
		bin = b
	}
	write := os.Getenv(findingsWriteVar) == "1"

	s := newSandbox(t)
	s.path = append([]string{filepath.Dir(bin)}, s.path...)
	copyCheckout(t, tree, s.dir)
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "this repo's tree")

	got := repoFindings(t, s, bin)
	if write {
		head := gittest.Git(t, tree, "rev-parse", "HEAD")
		body := "# base: " + head + "\n# captured by: " + findingsCaptureCommand + "\n" + strings.Join(got, "\n") + "\n"
		if err := os.WriteFile(repoFindingsGolden, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s: %d lines", repoFindingsGolden, len(got))
		return
	}
	want := readGolden(t)
	if !slices.Equal(got, want) {
		t.Fatalf("this repo's findings differ from %s:\n%s", repoFindingsGolden, lineDiff(want, got))
	}

	t.Run("dated key is live", func(t *testing.T) {
		const probe = ".satelle/skills/zz-dated-probe.md"
		s.write(probe, "---\nname: zz-dated-probe\ntype: skill\ndescription: A dated history line.\n---\n\n- 2026-11-08: released v1.0.57\n")
		if got := repoFindings(t, s, bin); !slices.Equal(got, want) {
			t.Fatalf("with this repo's volatile.toml the dated probe changed the findings:\n%s", lineDiff(want, got))
		}
		vt := filepath.Join(s.dir, ".satelle", "volatile.toml")
		b, err := os.ReadFile(vt)
		if err != nil {
			t.Fatal(err)
		}
		var kept []string
		removed := false
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "dated =") {
				removed = true
				continue
			}
			kept = append(kept, l)
		}
		if !removed {
			t.Fatal("this repo's volatile.toml has no dated key")
		}
		s.write(".satelle/volatile.toml", strings.Join(kept, "\n"))
		v, d := validateJSON(t, s, bin), doctorJSON(t, s, bin)
		if !slices.ContainsFunc(v.Volatile, func(f sub.Finding) bool { return f.File == probe }) {
			t.Errorf("dated removed: validate lists no volatile finding in %s: %+v", probe, v.Volatile)
		}
		if !slices.ContainsFunc(d.Issues, func(is doctorIssueJSON) bool {
			return is.Check == sub.VolatileField && strings.HasPrefix(is.Problem, probe+":")
		}) {
			t.Errorf("dated removed: doctor reports no volatile issue in %s: %+v", probe, d.Issues)
		}
	})
}

// copyCheckout copies every file git lists in tree (tracked, and untracked
// but not ignored), read from the working tree, into dst: uncommitted edits
// are included, ignored machine state (ledger, logs, *.local.toml) is not.
func copyCheckout(t *testing.T, tree, dst string) {
	t.Helper()
	out, err := gittest.Command(t, tree, "ls-files", "-z", "--cached", "--others", "--exclude-standard").Output()
	if err != nil {
		t.Fatalf("git ls-files in %s: %v", tree, err)
	}
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel == "" {
			continue
		}
		src := filepath.Join(tree, filepath.FromSlash(rel))
		fi, err := os.Lstat(src)
		if os.IsNotExist(err) {
			continue // deleted in the working tree
		}
		if err != nil {
			t.Fatal(err)
		}
		to := filepath.Join(dst, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			t.Fatal(err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(src)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(link, to); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if !fi.Mode().IsRegular() {
			continue
		}
		b, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, to, string(b), fi.Mode().Perm())
	}
}

type validateReport struct {
	Valid     bool          `json:"valid"`
	Problems  []string      `json:"problems"`
	Substrate []sub.Finding `json:"substrate"`
	Volatile  []sub.Finding `json:"volatile"`
	code      int
}

type doctorIssueJSON struct {
	Check   string `json:"check"`
	Problem string `json:"problem"`
	Fix     string `json:"fix"`
	Resolve string `json:"resolve"`
}

type doctorReportJSON struct {
	Issues    []doctorIssueJSON `json:"issues"`
	Unchecked []string          `json:"unchecked"`
	Notes     []string          `json:"notes"`
}

func validateJSON(t *testing.T, s *sandbox, bin string) validateReport {
	t.Helper()
	r := s.exec(bin, "", "validate", "--json")
	var v validateReport
	if err := json.Unmarshal([]byte(r.out), &v); err != nil {
		t.Fatalf("validate --json: exit %d: %v\n%s%s", r.code, err, r.out, r.err)
	}
	v.code = r.code
	return v
}

func doctorJSON(t *testing.T, s *sandbox, bin string) doctorReportJSON {
	t.Helper()
	r := s.exec(bin, "", "doctor", "--json")
	var d doctorReportJSON
	if err := json.Unmarshal([]byte(r.out), &d); err != nil {
		t.Fatalf("doctor --json: exit %d: %v\n%s%s", r.code, err, r.out, r.err)
	}
	return d
}

// repoFindings runs validate --json and doctor --json with bin in s and
// returns the recorded lines: validate's exit, validity, problems and
// findings, and doctor's issues and notes of every check that reads the tree
// or the sandbox ledger, each group sorted, with the sandbox repo, home and
// the binary's directory replaced by <repo>, <home> and <bin>.
func repoFindings(t *testing.T, s *sandbox, bin string) []string {
	t.Helper()
	norm := strings.NewReplacer(s.dir, "<repo>", s.home, "<home>", filepath.Dir(bin), "<bin>").Replace
	group := func(prefix string, lines []string) []string {
		out := make([]string, 0, len(lines))
		for _, l := range lines {
			out = append(out, norm(prefix+l))
		}
		slices.Sort(out)
		return out
	}

	v := validateJSON(t, s, bin)
	out := []string{fmt.Sprintf("validate exit %d", v.code), fmt.Sprintf("validate valid %t", v.Valid)}
	out = append(out, group("validate problem ", v.Problems)...)
	var sub, vol []string
	for _, f := range v.Substrate {
		sub = append(sub, f.String())
	}
	for _, f := range v.Volatile {
		vol = append(vol, f.String())
	}
	out = append(out, group("validate substrate ", sub)...)
	out = append(out, group("validate volatile ", vol)...)

	d := doctorJSON(t, s, bin)
	if len(d.Unchecked) != 0 {
		t.Fatalf("doctor could not finish checks %v; the comparison needs every check", d.Unchecked)
	}
	var issues, notes []string
	for _, is := range d.Issues {
		if slices.Contains(machineChecks, is.Check) {
			continue
		}
		issues = append(issues, fmt.Sprintf("%s | %s | fix: %s | resolve: %s", is.Check, is.Problem, is.Fix, is.Resolve))
	}
	for _, n := range d.Notes {
		if slices.ContainsFunc(machineChecks, func(c string) bool { return strings.HasPrefix(n, c+":") }) {
			continue
		}
		notes = append(notes, n)
	}
	out = append(out, group("doctor issue ", issues)...)
	out = append(out, group("doctor note ", notes)...)
	return out
}

// readGolden is the golden's lines, its # header skipped.
func readGolden(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(repoFindingsGolden)
	if err != nil {
		t.Fatalf("%v (capture it: %s)", err, findingsCaptureCommand)
	}
	var out []string
	for _, l := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		if strings.HasPrefix(l, "#") {
			continue
		}
		out = append(out, l)
	}
	return out
}

// lineDiff shows the lines only in want (-) and only in got (+).
func lineDiff(want, got []string) string {
	var b bytes.Buffer
	for _, l := range want {
		if !slices.Contains(got, l) {
			fmt.Fprintf(&b, "- %s\n", l)
		}
	}
	for _, l := range got {
		if !slices.Contains(want, l) {
			fmt.Fprintf(&b, "+ %s\n", l)
		}
	}
	if b.Len() == 0 {
		fmt.Fprintf(&b, "(same lines, different order or count: want %d, got %d)\n", len(want), len(got))
	}
	return b.String()
}
