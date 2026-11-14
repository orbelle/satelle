package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gittest"
)

// The module fixture release-binary.sh decides over (sto_da9775c1): two
// binaries that link internal/a, which embeds internal/a/topics; a test file,
// an old test file, a file built only for windows, a Go and an assembly file
// every shipped target excludes (plan9); a tool neither binary imports; process
// files; a release.yml whose matrix builds both binaries for linux/amd64 and
// windows/amd64; and the tags v1.0.0 and serve-v1.0.0 on the base commit.

// fixtureGoVersion is this repo's go.mod go line and release.yml go-version,
// so the fixture never downloads a toolchain.
func fixtureGoVersion(t *testing.T) (goLine, setupGo string) {
	t.Helper()
	mod, err := os.ReadFile("../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^go \S+$`).FindString(string(mod))
	yml, err := os.ReadFile("../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	g := regexp.MustCompile(`(?m)^\s*go-version:\s*'([0-9.]+)'`).FindStringSubmatch(string(yml))
	if m == "" || g == nil {
		t.Fatalf("no go line in go.mod (%q) or go-version in release.yml (%v)", m, g)
	}
	return m, g[1]
}

func fixtureReleaseYML(goVersion, extraBuild string) string {
	return `name: release
jobs:
  build:
    strategy:
      matrix:
        include:
          # the shipped targets
          - { artifact: satelle,  pkg: ./cmd/satelle,  goos: linux,   goarch: amd64 }
          - { artifact: satelle,  pkg: ./cmd/satelle,  goos: windows, goarch: amd64 }
          - { artifact: satelled, pkg: ./cmd/satelled, goos: linux,   goarch: amd64 }
          - { artifact: satelled, pkg: ./cmd/satelled, goos: windows, goarch: amd64 }
    steps:
      - uses: actions/setup-go@v6
        with:
          go-version: '` + goVersion + `'
      - name: build
        env:
          GOOS:    ${{ matrix.goos }}
          GOARCH:  ${{ matrix.goarch }}
          CGO_ENABLED: '0'
        run: |
          go build -trimpath` + extraBuild + ` -o out "${{ matrix.pkg }}"
`
}

func fixtureFiles(t *testing.T) map[string]string {
	t.Helper()
	goLine, setupGo := fixtureGoVersion(t)
	main := "package main\n\nimport _ \"example.com/fix/internal/a\"\n\nfunc main() {}\n"
	files := map[string]string{
		"go.mod":                         "module example.com/fix\n\n" + goLine + "\n",
		"cmd/satelle/main.go":            main,
		"cmd/satelled/main.go":           main,
		"internal/a/a.go":                "package a\n\nimport \"embed\"\n\n//go:embed topics\nvar Topics embed.FS\n",
		"internal/a/b.go":                "package a\n\nfunc B() {}\n",
		"internal/a/a_test.go":           "package a\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {}\n",
		"internal/a/old_test.go":         "package a\n",
		"internal/a/a_windows.go":        "package a\n\nfunc W() {}\n",
		"internal/a/a_plan9.go":          "package a\n\nfunc P() {}\n",
		"internal/a/x_plan9.s":           "// plan9 only\n",
		"internal/a/topics/t.md":         "t\n",
		"internal/a/topics/u.md":         "u\n",
		"internal/a/topics/x_test.go":    "package topics\n",
		"scripts/tool/main.go":           "package main\n\nfunc main() {}\n",
		".satelle/x.toml":                "x = 1\n",
		"scripts/x.sh":                   "echo x\n",
		"docs/x.md":                      "x\n",
		".github/workflows/release.yml":  fixtureReleaseYML(setupGo, ""),
		".version":                       "satelle.version: 1.0.0\nsatelled.version: 1.0.0\n",
		"scripts/release-binary.sh":      readText(t, "release-binary.sh"),
		"scripts/check-serve-version.sh": readText(t, "check-serve-version.sh"),
	}
	return files
}

func readText(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// fixtureRepo is a git repo holding the fixture (with extra written over it)
// committed and tagged v1.0.0 and serve-v1.0.0.
type fixtureRepo struct {
	t   *testing.T
	dir string
}

func newFixtureRepo(t *testing.T, extra map[string]string) *fixtureRepo {
	t.Helper()
	r := &fixtureRepo{t: t, dir: t.TempDir()}
	files := fixtureFiles(t)
	for p, b := range extra {
		files[p] = b
	}
	for p, b := range files {
		r.write(p, b)
	}
	r.git("init", "-q")
	r.git("add", "-A")
	r.git("commit", "-q", "-m", "base")
	r.git("tag", "v1.0.0")
	r.git("tag", "serve-v1.0.0")
	return r
}

func (r *fixtureRepo) write(p, body string) {
	r.t.Helper()
	full := filepath.Join(r.dir, p)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		r.t.Fatal(err)
	}
	mode := os.FileMode(0o644)
	if strings.HasSuffix(p, ".sh") {
		mode = 0o755
	}
	if err := os.WriteFile(full, []byte(body), mode); err != nil {
		r.t.Fatal(err)
	}
}

func (r *fixtureRepo) git(args ...string) string {
	r.t.Helper()
	return gittest.Git(r.t, r.dir, args...)
}

// commit stages everything and commits.
func (r *fixtureRepo) commit() {
	r.t.Helper()
	r.git("add", "-A")
	r.git("commit", "-q", "--allow-empty", "-m", "change")
}

// decide runs release-binary.sh decide with env added to this process's.
func decide(t *testing.T, env []string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{"release-binary.sh", "decide"}, args...)...)
	cmd.Env = append(append(os.Environ(), gittest.Env()...), env...)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("release-binary.sh decide: %v", err)
		}
		code = ee.ExitCode()
	}
	return out.String(), errb.String(), code
}

// record is a parsed decide record.
type record struct {
	binary, reason string
	paths          []string // the build and other lines, sorted
	head           map[string][]string
}

func parseRecord(t *testing.T, out string) record {
	t.Helper()
	rec := record{head: map[string][]string{}}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "binary: ") {
		t.Fatalf("the record does not open with binary:\n%s", out)
	}
	rec.binary = strings.TrimPrefix(lines[0], "binary: ")
	for _, l := range lines[1:] {
		switch {
		case strings.HasPrefix(l, "build "), strings.HasPrefix(l, "other "):
			rec.paths = append(rec.paths, l)
		case strings.HasPrefix(l, "reason: "):
			rec.reason = strings.TrimPrefix(l, "reason: ")
		default:
			k, v, ok := strings.Cut(l, ": ")
			if !ok {
				t.Fatalf("record line %q is no header, build, other or reason line:\n%s", l, out)
			}
			rec.head[k] = append(rec.head[k], v)
		}
	}
	slices.Sort(rec.paths)
	return rec
}

type decideRow struct {
	name   string
	change func(r *fixtureRepo)
	binary string
	reason string   // a prefix the reason must carry; "" means no reason line
	paths  []string // every build and other line
	env    []string
	extra  map[string]string // written into the base commit
}

func edit(p string) func(*fixtureRepo) {
	return func(r *fixtureRepo) { r.write(p, readText(r.t, filepath.Join(r.dir, p))+"// edited\n") }
}

func add(p string) func(*fixtureRepo) {
	return func(r *fixtureRepo) { r.write(p, "added\n") }
}

func remove(ps ...string) func(*fixtureRepo) {
	return func(r *fixtureRepo) { r.git(append([]string{"rm", "-q"}, ps...)...) }
}

func runDecideRows(t *testing.T, rows []decideRow) {
	t.Helper()
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			r := newFixtureRepo(t, row.extra)
			row.change(r)
			r.commit()
			out, errOut, code := decide(t, row.env, r.dir)
			if code != 0 {
				t.Fatalf("exit %d, want 0\n%s%s", code, out, errOut)
			}
			rec := parseRecord(t, out)
			want := slices.Clone(row.paths)
			slices.Sort(want)
			if rec.binary != row.binary || !slices.Equal(rec.paths, want) {
				t.Fatalf("binary: %s with %q, want binary: %s with %q\n%s%s", rec.binary, rec.paths, row.binary, want, out, errOut)
			}
			if row.reason == "" && rec.reason != "" || row.reason != "" && !strings.HasPrefix(rec.reason, row.reason) {
				t.Fatalf("reason %q, want %q\n%s", rec.reason, row.reason, out)
			}
			if rec.binary == "changed" && !slices.ContainsFunc(rec.paths, func(l string) bool { return strings.HasPrefix(l, "build ") }) {
				t.Fatalf("a changed record with no build line:\n%s", out)
			}
			if c := rec.head["candidate"]; len(c) != 1 || c[0] != strings.TrimSpace(r.git("rev-parse", "HEAD")) {
				t.Fatalf("candidate %v is not HEAD\n%s", c, out)
			}
		})
	}
}

// AC2: the decision's record: what the build does not compile or embed is
// other and leaves the binary unchanged; a compiled or embedded file is built.
func TestReleaseBinaryDecide(t *testing.T) {
	_, setupGo := fixtureGoVersion(t)
	runDecideRows(t, []decideRow{
		{name: "test file", change: edit("internal/a/a_test.go"), binary: "unchanged", paths: []string{"other internal/a/a_test.go"}},
		{name: "process files", change: func(r *fixtureRepo) { edit(".satelle/x.toml")(r); edit("scripts/x.sh")(r); edit("docs/x.md")(r) },
			binary: "unchanged", paths: []string{"other .satelle/x.toml", "other docs/x.md", "other scripts/x.sh"}},
		{name: "unlinked tool", change: edit("scripts/tool/main.go"), binary: "unchanged", paths: []string{"other scripts/tool/main.go"}},
		{name: "removed test file", change: remove("internal/a/old_test.go"), binary: "unchanged", paths: []string{"other internal/a/old_test.go"}},
		{name: "source", change: edit("internal/a/a.go"), binary: "changed", paths: []string{"build built internal/a/a.go"}},
		{name: "embedded topic", change: edit("internal/a/topics/t.md"), binary: "changed", paths: []string{"build built internal/a/topics/t.md"}},
		{name: "embedded test-named file", change: edit("internal/a/topics/x_test.go"), binary: "changed", paths: []string{"build built internal/a/topics/x_test.go"}},
		{name: "added topic", change: add("internal/a/topics/new.md"), binary: "changed", paths: []string{"build built internal/a/topics/new.md"}},

		// AC6: the conservative rules, one row each.
		{name: "excluded go", change: edit("internal/a/a_plan9.go"), binary: "changed", paths: []string{"build excluded internal/a/a_plan9.go"}},
		{name: "excluded asm", change: edit("internal/a/x_plan9.s"), binary: "changed", paths: []string{"build excluded internal/a/x_plan9.s"}},
		{name: "deleted package file", change: remove("internal/a/b.go"), binary: "changed", paths: []string{"build deleted internal/a/b.go"}},
		{name: "deleted embedded file", change: remove("internal/a/topics/t.md"), binary: "changed", paths: []string{"build deleted internal/a/topics/t.md"}},
		{name: "deleted embedded test-named file", change: remove("internal/a/topics/x_test.go"), binary: "changed", paths: []string{"build deleted internal/a/topics/x_test.go"}},
		{name: "embed area file the embed skips", change: add("internal/a/topics/_draft.md"), binary: "changed", paths: []string{"build embed internal/a/topics/_draft.md"}},
		{name: "module", change: add("go.sum"), binary: "changed", paths: []string{"build module go.sum"}},
		{name: "no serve tag", change: func(r *fixtureRepo) { edit("docs/x.md")(r); r.git("tag", "-d", "serve-v1.0.0") },
			binary: "changed", reason: "serve-v1.0.0 is not a published tag under the candidate", paths: []string{"build version .version"}},
		{name: "version without satelled", change: func(r *fixtureRepo) { r.write(".version", "satelle.version: 1.0.0\n") },
			binary: "changed", reason: ".version names no satelle.version or satelled.version", paths: []string{"build version .version"}},
		{name: "unpublished version", change: func(r *fixtureRepo) { r.write(".version", "satelle.version: 1.0.1\nsatelled.version: 1.0.0\n") },
			binary: "changed", reason: "v1.0.1 is not a published tag under the candidate", paths: []string{"build version .version"}},
		{name: "workflow", change: func(r *fixtureRepo) {
			r.write(".github/workflows/release.yml", "# a comment\n"+fixtureReleaseYML(setupGo, ""))
		}, binary: "changed", paths: []string{"build workflow .github/workflows/release.yml"}},
		{name: "workflow unparsed", change: func(r *fixtureRepo) {
			yml := strings.Replace(fixtureReleaseYML(setupGo, ""), "          - { artifact: satelle,  pkg: ./cmd/satelle,  goos: linux,   goarch: amd64 }\n",
				"          - artifact: satelle\n            pkg: ./cmd/satelle\n            goos: linux\n            goarch: amd64\n", 1)
			r.write(".github/workflows/release.yml", yml)
		}, binary: "changed", reason: "release.yml has no matrix include entry", paths: []string{"build workflow .github/workflows/release.yml"}},
		{name: "workflow unreproduced", change: func(r *fixtureRepo) {
			r.write(".github/workflows/release.yml", fixtureReleaseYML(setupGo, " -tags netgo"))
		}, binary: "changed", reason: "release.yml sets -tags, which the decision does not reproduce", paths: []string{"build workflow .github/workflows/release.yml"}},
		{name: "workflow toolchain", change: func(r *fixtureRepo) {
			r.write(".github/workflows/release.yml", fixtureReleaseYML("9.99", ""))
		}, binary: "changed", reason: "release.yml builds with go 9.99; this machine lists with ", paths: []string{"build workflow .github/workflows/release.yml"}},
		{name: "underived", change: func(r *fixtureRepo) {
			r.write("cmd/satelle/main.go", "package main\n\nimport _ \"example.com/fix/internal/nope\"\n\nfunc main() {}\n")
			edit("docs/x.md")(r)
		}, binary: "changed", reason: "the build set could not be derived", paths: []string{"build underived cmd/satelle/main.go", "build underived docs/x.md"}},
		{name: "underived embed with no match", change: remove("internal/a/topics/t.md", "internal/a/topics/u.md", "internal/a/topics/x_test.go"),
			binary: "changed", reason: "the build set could not be derived",
			paths: []string{"build underived internal/a/topics/t.md", "build underived internal/a/topics/u.md", "build underived internal/a/topics/x_test.go"}},
		{name: "any target", change: edit("internal/a/a_windows.go"), binary: "changed", paths: []string{"build built internal/a/a_windows.go"}},
		{name: "rename", change: func(r *fixtureRepo) { r.git("mv", "internal/a/b.go", "internal/a/c.go") },
			binary: "changed", paths: []string{"build deleted internal/a/b.go", "build built internal/a/c.go"}},
	})
}

// AC6: the local Go settings never reach the decision: internal/d is linked
// only through a file a -tags=x build excludes, so an edit to it is built
// whatever GOFLAGS the caller exports or its go env file holds.
func TestReleaseBinaryDecideIgnoresLocalGoSettings(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "go.env")
	if err := os.WriteFile(envFile, []byte("GOFLAGS=-tags=x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	extra := map[string]string{
		"internal/a/tagged.go": "//go:build !x\n\npackage a\n\nimport _ \"example.com/fix/internal/d\"\n",
		"internal/d/d.go":      "package d\n",
	}
	runDecideRows(t, []decideRow{
		{name: "GOFLAGS exported", extra: extra, env: []string{"GOFLAGS=-tags=x"}, change: edit("internal/d/d.go"),
			binary: "changed", paths: []string{"build built internal/d/d.go"}},
		{name: "GOENV file", extra: extra, env: []string{"GOENV=" + envFile}, change: edit("internal/d/d.go"),
			binary: "changed", paths: []string{"build built internal/d/d.go"}},
	})
}

// decide's only non-zero exit is a usage error, with no record.
func TestReleaseBinaryDecideUsage(t *testing.T) {
	for _, args := range [][]string{nil, {t.TempDir()}} {
		out, _, code := decide(t, nil, args...)
		if code != 2 || out != "" {
			t.Fatalf("decide %v: exit %d with %q, want 2 and no record", args, code, out)
		}
	}
}

// This repo's release.yml is a layout decide reads: with it in the base,
// both binaries' packages are listed for every target (an edit to either
// main file is built), and it names no setting decide refuses.
func TestReleaseBinaryReadsThisReleaseYML(t *testing.T) {
	yml := readText(t, "../.github/workflows/release.yml")
	for _, pkg := range []string{"cmd/satelle", "cmd/satelled"} {
		t.Run(pkg, func(t *testing.T) {
			t.Parallel()
			r := newFixtureRepo(t, map[string]string{".github/workflows/release.yml": yml})
			edit(pkg + "/main.go")(r)
			r.commit()
			out, errOut, code := decide(t, nil, r.dir)
			rec := parseRecord(t, out)
			if code != 0 || rec.reason != "" || rec.binary != "changed" || !slices.Equal(rec.paths, []string{"build built " + pkg + "/main.go"}) {
				t.Fatalf("exit %d\n%s%s", code, out, errOut)
			}
			tg := rec.head["targets"]
			if len(tg) != 1 || !strings.Contains(tg[0], "linux/amd64") || !strings.Contains(tg[0], "windows/amd64") {
				t.Fatalf("targets %v", tg)
			}
		})
	}
}
