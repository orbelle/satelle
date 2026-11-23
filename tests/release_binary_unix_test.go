//go:build integration && unix

package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

type binarySandbox struct {
	*candidateSandbox
	origin string
}

const binaryVersion = "satelle.version: 1.0.0\nsatelled.version: 1.0.0\nsatelle.build: 2026-01-01T00:00:00Z\n"

func binaryModule(t *testing.T) map[string]string {
	t.Helper()
	mod := repoText(t, "go.mod")
	goLine := regexp.MustCompile(`(?m)^go \S+$`).FindString(mod)
	gv := regexp.MustCompile(`(?m)^\s*go-version:\s*'([0-9.]+)'`).FindStringSubmatch(repoText(t, ".github/workflows/release.yml"))
	if goLine == "" || gv == nil {
		t.Fatalf("no go line in go.mod or go-version in release.yml")
	}
	main := "package main\n\nimport _ \"example.com/fix/internal/a\"\n\nfunc main() {}\n"
	return map[string]string{
		"go.mod":                 "module example.com/fix\n\n" + goLine + "\n",
		"cmd/satelle/main.go":    main,
		"cmd/satelled/main.go":   main,
		"internal/a/a.go":        "package a\n\nimport \"embed\"\n\n//go:embed topics\nvar Topics embed.FS\n",
		"internal/a/b.go":        "package a\n\nfunc B() {}\n",
		"internal/a/a_test.go":   "package a\n",
		"internal/a/topics/t.md": "t\n",
		"internal/a/topics/u.md": "u\n",
		"docs/x.md":              "x\n",
		".version":               binaryVersion,
		".github/workflows/release.yml": `jobs:
  build:
    strategy:
      matrix:
        include:
          - { artifact: satelle,  pkg: ./cmd/satelle,  goos: linux,   goarch: amd64 }
          - { artifact: satelle,  pkg: ./cmd/satelle,  goos: windows, goarch: amd64 }
          - { artifact: satelled, pkg: ./cmd/satelled, goos: linux,   goarch: amd64 }
          - { artifact: satelled, pkg: ./cmd/satelled, goos: windows, goarch: amd64 }
    steps:
      - uses: actions/setup-go@v6
        with:
          go-version: '` + gv[1] + `'
      - name: build
        env:
          CGO_ENABLED: '0'
        run: go build -o out "${{ matrix.pkg }}"
`,
	}
}

const binaryShim = `#!/bin/sh
installed() { # ver sver sha
  s12=$(printf '%s' "$3" | cut -c1-12)
  printf 'satelle %s (commit %s, built 2026-01-01T00:00:00Z)\n' "$1" "$s12" >"$FAKE_DIR/version.txt"
  printf 'satelled   answering at http://127.0.0.1:1 (200 OK)\nrunning    satelled %s (commit %s, built 2026-01-01T00:00:00Z)\ninstalled  satelled %s (commit %s, built 2026-01-01T00:00:00Z) [%s]\n' \
    "$2" "$s12" "$2" "$s12" "$FAKE_DIR/satelled" >"$FAKE_DIR/status.txt"
}
case "$1" in
  update)
    echo "update $2 $3" >>"$FAKE_DIR/satelle.log"
    sha=$(git -C "$ORIGIN" rev-parse main)
    installed "$(git -C "$ORIGIN" show main:.version | awk '$1 == "satelle.version:" { print $2 }')" \
      "$(git -C "$ORIGIN" show main:.version | awk '$1 == "satelled.version:" { print $2 }')" "$sha"
    exit 0 ;;
  version) cat "$FAKE_DIR/version.txt"; exit 0 ;;
  service)
    case "$2" in
      restart) echo "service restart" >>"$FAKE_DIR/satelle.log"; exit 0 ;;
      status) cat "$FAKE_DIR/status.txt"; exit 0 ;;
    esac ;;
  execution)
    if [ "$2" = list ] && [ -f "$FAKE_DIR/fail-execution-list" ]; then
      case " $* " in *" --status "*) ;; *) echo "shim: execution list failed" >&2; exit 1 ;; esac
    fi ;;
esac
exec "$REAL_SATELLE" "$@"
`

const binaryFlow = `name = "flow"
[states.backlog]
start = true
[states.approved]
enqueue = ["release"]
[states.in_progress]
[states.done]
terminal = true
[gates.release-run]
skill = "satelle-release-run-check"
[gates.version-stamp]
skill = "satelle-version-stamp-check"
[gates.changelog-entry]
skill = "satelle-changelog-entry-check"
[gates.ci-published]
skill = "satelle-ci-published-check"
[gates.release-installed]
skill = "satelle-release-installed-check"
[[transition]]
from = ["backlog"]
to = "approved"
[[transition]]
from = ["approved"]
to = "in_progress"
[[transition]]
from = ["approved"]
to = "done"
groups = [{ mode = "sequential", gates = ["release-run", "version-stamp", "changelog-entry", "ci-published", "release-installed"] }]
`

const binaryRunbook = `name = "runbook"
[states.backlog]
start = true
[states.in_progress]
[states.done]
terminal = true
[states.cancelled]
terminal = true
[gates.release-kept]
skill = "satelle-release-kept-check"
[[transition]]
from = ["backlog"]
to = "in_progress"
[[transition]]
from = ["in_progress"]
to = "done"
groups = [{ mode = "sequential", gates = ["release-kept"] }]
[[transition]]
from = ["*"]
to = "cancelled"
`

var releaseGates = []string{"version-stamp", "changelog-entry", "ci-published", "release-installed"}

func newBinarySandbox(t *testing.T) *binarySandbox {
	t.Helper()
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatal("go is required by the binary decision and is not on PATH")
	}
	c := newCandidateSandboxWith(t, false, binaryModule(t))
	b := &binarySandbox{candidateSandbox: c, origin: c.origin}
	c.git("tag", "v1.0.0", c.base)
	c.git("tag", "serve-v1.0.0", c.base)
	c.git("push", "-q", "origin", "v1.0.0", "serve-v1.0.0")

	writeFile(t, filepath.Join(c.fake, "go"), `#!/bin/sh
if [ "$1" = build ]; then
  out=; prev=
  for a in "$@"; do [ "$prev" = -o ] && out=$a; prev=$a; done
  [ -n "$out" ] || { echo "fake go: unexpected $*" >&2; exit 1; }
  exec cp "$SATELLE_BIN" "$out"
fi
exec `+realGo+` "$@"
`, 0o755)
	writeFile(t, filepath.Join(c.fake, "satelle"), binaryShim, 0o755)
	writeFile(t, filepath.Join(c.fake, "satelled"), "#!/bin/sh\n", 0o755)
	c.env["REAL_SATELLE"] = satelleBin

	for _, name := range []string{"satelle-version-stamp-check", "satelle-changelog-entry-check", "satelle-ci-published-check",
		"satelle-release-installed-check", "satelle-release-kept-check"} {
		c.write(".satelle/skills/"+name+".md", rebindTask(t, ".satelle/skills/"+name+".md", c.task.ID))
	}
	c.write(".satelle/workflows/flow.toml", binaryFlow)
	c.write(".satelle/workflows/runbook.toml", binaryRunbook)
	c.must("validate")
	b.installed("1.0.0", c.base)
	return b
}

func (b *binarySandbox) installed(ver, sha string) {
	b.t.Helper()
	s12 := sha[:12]
	writeFile(b.t, filepath.Join(b.fake, "version.txt"), "satelle "+ver+" (commit "+s12+", built 2026-01-01T00:00:00Z)\n", 0o644)
	writeFile(b.t, filepath.Join(b.fake, "status.txt"), "satelled   answering at http://127.0.0.1:1 (200 OK)\n"+
		"running    satelled "+ver+" (commit "+s12+", built 2026-01-01T00:00:00Z)\n"+
		"installed  satelled "+ver+" (commit "+s12+", built 2026-01-01T00:00:00Z) ["+filepath.Join(b.fake, "satelled")+"]\n", 0o644)
}

func (b *binarySandbox) story(name, changelog string, files ...[2]string) (string, string) {
	b.t.Helper()
	head := b.branch(name, b.base, files...)
	id := b.create("story", "--title", "change "+name).ID
	if changelog != "" {
		b.attach(id, changelog)
	}
	b.must("story", "set", id, "--status", "approved")
	b.pr(id, head)
	b.ids = append(b.ids, id)
	return id, head
}

func (b *binarySandbox) tested() (string, string, string, string) {
	b.t.Helper()
	run, rd := b.fire()
	b.mustBlock("### Build the candidate")
	b.mustBlock("### Send back")
	b.driftGate()
	b.mustNodeTest()
	var sha string
	for _, l := range strings.Split(b.nodeFile("out/result"), "\n") {
		if v, ok := strings.CutPrefix(l, "tested "); ok {
			sha = v
		}
	}
	if sha == "" {
		b.t.Fatalf("the node recorded no tested candidate:\n%s", b.nodeFile("out/result"))
	}
	return run, rd, b.work(), sha
}

func (b *binarySandbox) fakeLog(name string) string {
	b.t.Helper()
	got, err := os.ReadFile(filepath.Join(b.fake, name))
	if err != nil && !os.IsNotExist(err) {
		b.t.Fatal(err)
	}
	return string(got)
}

func (b *binarySandbox) tags() string {
	b.t.Helper()
	b.git("fetch", "-q", "origin", "--tags")
	return b.git("tag", "-l")
}

func (b *binarySandbox) done(id string) result {
	b.t.Helper()
	return b.run("story", "set", id, "--status", "done")
}

func (b *binarySandbox) refused(id, gate, want string) {
	b.t.Helper()
	if r := b.done(id); r.code == 0 {
		b.t.Fatalf("%s reached done; want %s to refuse it naming %q", id, gate, want)
	}
	if g := latestRun(b.sandbox, id, gate); g.Result != "reject" || !strings.Contains(g.Notes, want) {
		b.t.Fatalf("%s on %s = %s (%q), want a reject naming %q", gate, id, g.Result, g.Notes, want)
	}
}

func recordLines(rec string) (string, []string) {
	lines := strings.Split(strings.TrimRight(rec, "\n"), "\n")
	var paths []string
	for _, l := range lines[1:] {
		if strings.HasPrefix(l, "build ") || strings.HasPrefix(l, "other ") || strings.HasPrefix(l, "reason: ") {
			paths = append(paths, l)
		}
	}
	slices.Sort(paths)
	return lines[0], paths
}

func TestReleaseBinaryUnchangedLandsWithoutAVersion(t *testing.T) {
	b := newBinarySandbox(t)
	idA, _ := b.story("a", "### Changed\n- A tunes an agent\n", [2]string{".satelle/agents.toml", "[agents.executor]\ncommand = \"session\"\n"})
	idB, _ := b.story("b", "### Fixed\n- B fixes a test\n", [2]string{"internal/a/a_test.go", "package a\n\n// B\n"}, [2]string{"docs/x.md", "x, B\n"})
	idN, _ := b.story("n", "", [2]string{"docs/n.md", "n\n"})
	oldMain, tagsBefore := b.rev("origin/main"), b.tags()

	run, _, _, tested := b.tested()
	rec := b.decide()
	first, paths := recordLines(rec)
	if want := []string{"other .satelle/agents.toml", "other docs/n.md", "other docs/x.md", "other internal/a/a_test.go"}; first != "binary: unchanged" || !slices.Equal(paths, want) {
		t.Fatalf("decide: %s with %q, want binary: unchanged with %q\n%s", first, paths, want, rec)
	}
	b.mustLand(run)
	b.git("fetch", "-q", "origin")
	if got := b.rev("origin/main"); got != tested {
		t.Fatalf("origin/main %s, want the tested candidate %s", got, tested)
	}
	if got := b.must("execution", "attachment", run, "release-binary"); got != rec {
		t.Fatalf("release-binary on %s:\n%s\nwant the decision\n%s", run, got, rec)
	}
	if r := b.exec("git", "", "diff", "--quiet", oldMain, "origin/main", "--", ".version", "docs/releases"); r.code != 0 {
		t.Fatal(".version or docs/releases changed on a binary-unchanged run")
	}
	if got := b.tags(); got != tagsBefore {
		t.Fatalf("tags after the run:\n%s\nbefore:\n%s", got, tagsBefore)
	}

	b.installed("1.0.0", strings.Repeat("0", 40))
	b.refused(idA, "release-installed", "satelle version is not satelle")
	b.installed("1.0.0", b.rev("v1.0.0^{commit}"))
	for _, id := range []string{idA, idB} {
		if r := b.done(id); r.code != 0 {
			t.Fatalf("done %s: exit %d\n%s%s", id, r.code, r.out, r.err)
		}
		if g := latestRun(b.sandbox, id, "release-run"); g.Result != "accept" {
			t.Fatalf("release-run on %s: %+v", id, g)
		}
		for _, gate := range releaseGates {
			if g := latestRun(b.sandbox, id, gate); g.Result != "accept" || !strings.Contains(g.Notes, "binary unchanged") {
				t.Fatalf("%s on %s = %s (%q), want accept on the binary-unchanged decision", gate, id, g.Result, g.Notes)
			}
		}
	}
	b.refused(idN, "changelog-entry", "no changelog attachment on "+idN)
	b.attach(idN, "### Added\n- N adds a doc\n")
	if r := b.done(idN); r.code != 0 {
		t.Fatalf("done %s with its lines: exit %d\n%s%s", idN, r.code, r.out, r.err)
	}
	if r := b.run("execution", "set", run, "--status", "done"); r.code != 0 {
		t.Fatalf("run done: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if log := b.fakeLog("gh.log"); strings.Contains(log, "workflow") || strings.Contains(log, "release view v1.0.1") {
		t.Fatalf("gh was asked to publish on a binary-unchanged run:\n%s", log)
	}
	if log := b.fakeLog("satelle.log"); log != "" {
		t.Fatalf("a binary-unchanged run installed or restarted:\n%s", log)
	}

	idT, _ := b.story("t", "### Changed\n- T edits a topic\n", [2]string{"internal/a/topics/t.md", "t, edited\n"})
	runT, _, _, _ := b.tested()
	rec = b.decide()
	want := []string{"build built internal/a/topics/t.md", "other .satelle/agents.toml", "other docs/n.md", "other docs/x.md", "other internal/a/a_test.go"}
	if first, paths := recordLines(rec); first != "binary: changed" || !slices.Equal(paths, want) {
		t.Fatalf("decide on an edited topic: %s with %q\n%s", first, paths, rec)
	}
	if r := b.nodeSteps("guard", "push"); r.code != 0 {
		t.Fatalf("guard and push with the bump skipped: exit %d\n%s%s", r.code, r.out, r.err)
	}
	b.attachDecision(runT)
	b.refused(idT, "version-stamp", "v1.0.0 is already published at")
	if log := b.fakeLog("satelle.log"); log != "" {
		t.Fatalf("satelle.log:\n%s", log)
	}
}

func TestReleaseBinaryChangedStillRefusesSkippedSteps(t *testing.T) {
	b := newBinarySandbox(t)
	idX, _ := b.story("x", "### Fixed\n- X fixes the source\n", [2]string{"internal/a/a.go", "package a\n\nimport \"embed\"\n\n//go:embed topics\nvar Topics embed.FS\n\n// X\n"})
	idY, _ := b.story("y", "### Added\n- Y adds a doc\n", [2]string{"docs/y.md", "y\n"})
	run, _, wt, _ := b.tested()
	rec := b.decide()
	if first, paths := recordLines(rec); first != "binary: changed" || !slices.Equal(paths, []string{"build built internal/a/a.go", "other docs/y.md"}) {
		t.Fatalf("decide: %s with %q\n%s", first, paths, rec)
	}
	of := func() string {
		r := b.exec("bash", "", "scripts/release-binary.sh", "of", b.task.ID, idX)
		if r.code != 0 {
			t.Fatalf("of: exit %d\n%s%s", r.code, r.out, r.err)
		}
		return r.out
	}

	b.exec("git", "", "-C", wt, "push", "-q", "origin", "HEAD:refs/heads/main")
	b.git("fetch", "-q", "origin")
	if got := of(); got != "run: "+run+"\nbinary: unrecorded\n" {
		t.Fatalf("of with no record: %q", got)
	}
	b.refused(idX, "version-stamp", "v1.0.0 is already published at")

	b.exec("git", "", "-C", wt, "tag", "-d", "serve-v1.0.0")
	r := b.exec("bash", "", filepath.Join(wt, "scripts/release-binary.sh"), "decide", wt)
	b.exec("git", "", "-C", wt, "fetch", "-q", "origin", "--tags")
	if r.code != 0 || !strings.Contains(r.out, "binary: changed\n") || !strings.Contains(r.out, "\nreason: serve-v1.0.0 is not a published tag") {
		t.Fatalf("decide without the serve tag: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if a := b.exec(satelleBin, r.out, "execution", "attach", run, "--name", "release-binary", "--type", "text/plain", "--file", "-"); a.code != 0 {
		t.Fatalf("attach: %s%s", a.out, a.err)
	}
	if got := of(); !strings.Contains(got, "\nreason: ") {
		t.Fatalf("of with the reason record: %q", got)
	}
	b.refused(idX, "version-stamp", "the bump was skipped")

	b.attachDecision(run)
	if got := of(); got != "run: "+run+"\n"+rec {
		t.Fatalf("of with the node's decision:\n%s\nwant\n%s", got, rec)
	}
	b.refused(idX, "version-stamp", "the bump was skipped")

	writeFile(t, filepath.Join(b.nodeRoot(), "in", "notes-ids"), idX+"\n", 0o600)
	b.mustNode("bump")
	if got := b.exec("git", "", "-C", wt, "show", "HEAD:.version").out; !strings.HasPrefix(got, "satelle.version: 1.0.1\nsatelled.version: 1.0.1\nsatelle.build: ") {
		t.Fatalf("the bump wrote .version:\n%s", got)
	}
	b.exec("git", "", "-C", wt, "push", "-q", "origin", "HEAD:refs/heads/main")
	b.refused(idX, "ci-published", "no successful release.yml run")
	if g := latestRun(b.sandbox, idX, "version-stamp"); g.Result != "accept" {
		t.Fatalf("version-stamp with the bump: %+v", g)
	}
	b.refused(idY, "changelog-entry", "lacks "+idY+"'s lines")

	if r := b.exec(filepath.Join(b.fake, "gh"), "", "workflow", "run", "release.yml", "--ref", "main"); r.code != 0 {
		t.Fatalf("fake dispatch: %s%s", r.out, r.err)
	}
	b.refused(idX, "release-installed", "satelle version is not satelle 1[.]0[.]1")
	if strings.Count(b.fakeLog("gh.log"), "workflow run") != 1 {
		t.Fatalf("gh.log:\n%s", b.fakeLog("gh.log"))
	}
	if g := latestRun(b.sandbox, idX, "ci-published"); g.Result != "accept" {
		t.Fatalf("ci-published once published: %+v", g)
	}
}

func TestReleaseCarriesUnchangedRunsChangelog(t *testing.T) {
	b := newBinarySandbox(t)
	idA, _ := b.story("a", "### Added\n- A adds a doc\n", [2]string{"docs/a.md", "a\n"})
	runU, _, _, _ := b.tested()
	if first, _ := recordLines(b.decide()); first != "binary: unchanged" {
		t.Fatalf("run U decided %s", first)
	}
	b.mustLand(runU)
	if r := b.done(idA); r.code != 0 {
		t.Fatalf("done %s: exit %d\n%s%s", idA, r.code, r.out, r.err)
	}
	b.must("execution", "set", runU, "--status", "done")
	b.git("fetch", "-q", "origin")
	if got := strings.TrimSpace(b.git("ls-tree", "--name-only", "origin/main", "docs/releases/")); got != "docs/releases/v1.0.0.md" {
		t.Fatal("run U wrote release notes")
	}

	idB, _ := b.story("b", "### Fixed\n- B fixes the source\n", [2]string{"internal/a/b.go", "package a\n\nfunc B() { _ = 1 }\n"})
	runC, _, _, tested := b.tested()
	if got := strings.Fields(b.nodeFile("in/notes-ids")); !slices.Equal(got, []string{idA, idB}) {
		t.Fatalf("notes ids %q, want the carried %s first, then %s", got, idA, idB)
	}
	writeFile(t, filepath.Join(b.fake, "fail-execution-list"), "", 0o644)
	if r := b.block(nodeInputHeading); r.code == 0 || !strings.Contains(r.err, "cannot list the items earlier binary-unchanged runs carried") {
		t.Fatalf("the node's input with a failing run list: exit %d %q\n%s", r.code, r.out, r.err)
	}
	os.Remove(filepath.Join(b.fake, "fail-execution-list"))
	if first, _ := recordLines(b.decide()); first != "binary: changed" {
		t.Fatalf("run C decided %s", first)
	}
	b.mustLand(runC)
	if strings.Contains(b.nodeFile("out/result"), "tested ") && !strings.Contains(b.nodeFile("out/result"), "tested "+tested) {
		t.Fatalf("tested moved:\n%s", b.nodeFile("out/result"))
	}
	b.git("fetch", "-q", "origin", "--tags")
	section := b.git("show", "origin/main:docs/releases/v1.0.1.md")
	if !strings.HasPrefix(section, "## [1.0.1] - ") || !strings.Contains(section, "- A adds a doc ("+idA+")\n") || !strings.Contains(section, "- B fixes the source ("+idB+")\n") {
		t.Fatalf("the [1.0.1] section:\n%s", section)
	}
	attachment := filepath.Join(b.fake, "a.changelog")
	writeFile(t, attachment, b.must("story", "attachment", idA, "changelog"), 0o644)
	main := filepath.Join(b.fake, "v1.0.1.md")
	writeFile(t, main, section, 0o644)
	if r := b.exec("bash", "", "scripts/changelog-items.sh", "has", main, "1.0.1", idA+":"+attachment); r.code != 0 {
		t.Fatalf("[1.0.1] on origin/main lacks %s's lines: %s%s", idA, r.out, r.err)
	}
	if r := b.exec("bash", "", "scripts/release-binary.sh", "carried", b.task.ID, b.dir, b.rev("origin/main")); r.code != 0 || r.out != "" {
		t.Fatalf("carried after v1.0.1: exit %d %q\n%s", r.code, r.out, r.err)
	}
}

func TestReleaseBinaryChangedBumpsPublishesInstallsOnce(t *testing.T) {
	if got := fencedAfter(t, "## 7."); got != goldenStep7 {
		t.Errorf("release.md ## 7. block changed:\n%s\nwant\n%s", got, goldenStep7)
	}
	b := newBinarySandbox(t)
	id, _ := b.story("x", "### Fixed\n- X fixes the source\n", [2]string{"internal/a/b.go", "package a\n\nfunc B() { _ = 2 }\n"})
	oldMain := b.rev("origin/main")
	run, _, _, _ := b.tested()
	if first, paths := recordLines(b.decide()); first != "binary: changed" || !slices.Equal(paths, []string{"build built internal/a/b.go"}) {
		t.Fatalf("decide: %s %q", first, paths)
	}
	if got := strings.Fields(b.nodeFile("in/notes-ids")); !slices.Equal(got, []string{id}) {
		t.Fatalf("notes ids with no earlier run %q, want %s", got, id)
	}
	tagsBefore := strings.Fields(b.tags())
	b.mustLand(run)
	result := b.nodeFile("out/result")
	for _, want := range []string{"version 1.0.1\n", "sver 1.0.1\n", "release-yml https://ci.example/7 ", " success\n", "tag v1.0.1 ", "tag serve-v1.0.1 ", "main-before " + oldMain + "\n", "main-after "} {
		if !strings.Contains(result, want) {
			t.Fatalf("the node's result lacks %q:\n%s", want, result)
		}
	}

	block := bindSession(fencedAfter(t, "## 7."))
	for k, v := range map[string]string{"<ver>": "1.0.1", `<the path in the [...] of the "installed" line of satelle service status>`: filepath.Join(b.fake, "satelled")} {
		block = strings.ReplaceAll(block, k, v)
	}
	if r := b.exec("bash", "", "-c", block); r.code != 0 {
		t.Fatalf("## 7.: exit %d\n%s%s", r.code, r.out, r.err)
	}

	b.git("fetch", "-q", "origin", "--tags")
	main := b.rev("origin/main")
	var bumps []string
	for _, c := range strings.Fields(b.git("rev-list", oldMain+"..origin/main", "--", ".version")) {
		bumps = append(bumps, c)
		if files := strings.Fields(b.git("diff", "--name-only", c+"^", c)); !slices.Equal(files, []string{".version", "docs/releases/v1.0.1.md"}) {
			t.Fatalf("the bump commit %s touches %v", c, files)
		}
	}
	if len(bumps) != 1 || bumps[0] != main {
		t.Fatalf("bump commits %v, want one at origin/main %s", bumps, main)
	}
	if n := strings.Count(b.fakeLog("gh.log"), "workflow run release.yml --ref release/"+run+" --repo orbelle/satelle\n"); n != 1 || strings.Count(b.fakeLog("gh.log"), "workflow run") != 1 {
		t.Fatalf("%d release.yml dispatches on release/%s:\n%s", n, run, b.fakeLog("gh.log"))
	}
	if !strings.Contains(b.fakeLog("gh.log"), "main at dispatch "+oldMain+"\n") {
		t.Fatalf("release.yml was dispatched after main moved:\n%s", b.fakeLog("gh.log"))
	}
	var newTags []string
	for _, tg := range strings.Fields(b.git("tag", "-l")) {
		if !slices.Contains(tagsBefore, tg) {
			newTags = append(newTags, tg)
			if b.rev(tg+"^{commit}") != main {
				t.Fatalf("tag %s is not at origin/main", tg)
			}
		}
	}
	if slices.Sort(newTags); !slices.Equal(newTags, []string{"serve-v1.0.1", "v1.0.1"}) {
		t.Fatalf("new tags %v, want serve-v1.0.1 and v1.0.1", newTags)
	}
	if got, want := b.fakeLog("satelle.log"), "update --version 1.0.1\nupdate --version 1.0.1\nservice restart\n"; got != want {
		t.Fatalf("satelle.log %q, want %q", got, want)
	}
	runtime := strings.TrimSpace(b.must("runtime", "path"))
	if backups, _ := os.ReadDir(filepath.Join(runtime, "backups", "bin")); len(backups) != 1 {
		t.Fatalf("%d backups, want 1", len(backups))
	}
	if r := b.done(id); r.code != 0 {
		t.Fatalf("done %s: exit %d\n%s%s", id, r.code, r.out, r.err)
	}
	for _, gate := range releaseGates {
		if g := latestRun(b.sandbox, id, gate); g.Result != "accept" || strings.Contains(g.Notes, "binary unchanged") {
			t.Fatalf("%s on %s = %s (%q), want today's accept", gate, id, g.Result, g.Notes)
		}
	}
}

func fencedAfter(t *testing.T, heading string) string {
	t.Helper()
	doc := repoText(t, ".satelle/skills/release.md")
	i := strings.Index(doc, "\n"+heading)
	if i < 0 {
		t.Fatalf("release.md has no %q", heading)
	}
	_, rest, ok := strings.Cut(doc[i:], "```sh\n")
	if !ok {
		t.Fatalf("release.md has no sh block after %q", heading)
	}
	block, _, _ := strings.Cut(rest, "```")
	return block
}

const goldenStep7 = "session=<session>\n" +
	"sbin=$(command -v satelle)\n" +
	"dbin=<the path in the [...] of the \"installed\" line of satelle service status>\n" +
	"bk=\"$(satelle runtime path --session \"$session\")/backups/bin/$(date -u +%Y%m%dT%H%M%SZ)\"; mkdir -p \"$bk\"; cp -p \"$sbin\" \"$dbin\" \"$bk/\"\n" +
	"printf 'restore: install -m 0755 %s %s && install -m 0755 %s %s && satelle service restart --session <session>\\n' \"$bk/satelle\" \"$sbin\" \"$bk/satelled\" \"$dbin\"\n" +
	"satelle update --version <ver> --session \"$session\"\n" +
	"\"$sbin\" update --version <ver> --session \"$session\"\n" +
	"satelle service restart --session \"$session\"\n"
