package buildinfo_test

// The Go toolchain is pinned in three places — go.mod's `go` directive, the
// tracked mise.toml, and `go-version` in the release workflow — and nothing made
// them agree (sty_02d79953). A partial bump drifts silently: local gates, CI and
// the released binary would each build on a different toolchain with no signal.
//
// This guard is deliberately TEST-ONLY. It knows about mise.toml and
// .github/workflows/, paths that exist because of how THIS repo is built, not
// because of anything satelle does for the repos it governs — so it must not
// enter the binary's importable surface (see the satelle-repo-agnostic
// principle). A gate that wants it on the release path names
// `go test ./internal/buildinfo/...`; no Go change is needed for that.
//
// It is a CHECK, never a generator: nothing here rewrites a pin.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// pin is one declared toolchain version and the file that declares it. Its
// GRANULARITY is whatever it wrote down: `1.27` declares a major.minor and
// floats the patch by design (that is how the CI workflows are pinned), while
// `1.27.0` declares a patch too. Two pins agree when they agree on every
// component they BOTH declare — so a minor-only pin is a wildcard over patch,
// two exact pins must match to the patch, and a CI pin that grows a patch is
// then compared at that granularity as well. Granularity is data the file
// carries, not a per-file rule in this code, so a fifth pin is one more entry.
type pin struct {
	file string
	raw  string
}

// toolchainPinFiles maps each pin's repo-relative path to its parser.
var toolchainPinFiles = []struct {
	path  string
	parse func([]byte) (string, error)
}{
	{"go.mod", parseGoModDirective},
	{"mise.toml", parseMiseGoPin},
	{filepath.Join(".github", "workflows", "release.yml"), parseWorkflowGoVersion},
}

var (
	goDirectiveRe = regexp.MustCompile(`(?m)^go[ \t]+([0-9][^\s]*)`)
	goVersionRe   = regexp.MustCompile(`(?m)^[ \t]*go-version:[ \t]*['"]?([0-9][^'"\s]*)['"]?`)
	miseGoPinRe   = regexp.MustCompile(`^go[ \t]*=[ \t]*['"]([^'"]+)['"]`)
	sectionRe     = regexp.MustCompile(`^\[([^\]]+)\]`)
)

// parseGoModDirective reads the language directive. It anchors at the start of a
// line so `toolchain go1.27.1` and anything inside a require block cannot match.
func parseGoModDirective(b []byte) (string, error) {
	m := goDirectiveRe.FindAllStringSubmatch(string(b), -1)
	if len(m) == 0 {
		return "", fmt.Errorf("no `go <version>` directive")
	}
	if len(m) > 1 {
		return "", fmt.Errorf("%d `go <version>` directives", len(m))
	}
	return m[0][1], nil
}

// parseMiseGoPin reads [tools] go. Section-aware rather than a bare `go =`
// match, so a pin under some other table can never be mistaken for the tool one.
func parseMiseGoPin(b []byte) (string, error) {
	section := ""
	for _, ln := range strings.Split(string(b), "\n") {
		trimmed := strings.TrimSpace(ln)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if m := sectionRe.FindStringSubmatch(trimmed); m != nil {
			section = m[1]
			continue
		}
		if section != "tools" {
			continue
		}
		if m := miseGoPinRe.FindStringSubmatch(trimmed); m != nil {
			return m[1], nil
		}
	}
	return "", fmt.Errorf("no `go = \"<version>\"` under [tools]")
}

// parseWorkflowGoVersion reads the setup-go pin. A workflow that grows a second
// setup-go step with a DIFFERENT version is itself drift, so it is refused here
// rather than silently resolved to the first match.
func parseWorkflowGoVersion(b []byte) (string, error) {
	m := goVersionRe.FindAllStringSubmatch(string(b), -1)
	if len(m) == 0 {
		return "", fmt.Errorf("no `go-version:` pin")
	}
	first := m[0][1]
	for _, got := range m[1:] {
		if got[1] != first {
			return "", fmt.Errorf("disagreeing go-version pins %q and %q in one workflow", first, got[1])
		}
	}
	return first, nil
}

// collectPins reads every pin under root.
func collectPins(root string) ([]pin, error) {
	pins := make([]pin, 0, len(toolchainPinFiles))
	for _, f := range toolchainPinFiles {
		b, err := os.ReadFile(filepath.Join(root, f.path))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.path, err)
		}
		raw, perr := f.parse(b)
		if perr != nil {
			return nil, fmt.Errorf("%s: %w", f.path, perr)
		}
		pins = append(pins, pin{file: f.path, raw: raw})
	}
	return pins, nil
}

// comparePins is the decision: every pin must agree with every other on the
// components they both declare. The error names EVERY pin and the version it
// declares — an operator fixing drift needs the whole picture, not just the
// first file that disagreed.
func comparePins(pins []pin) error {
	var conflicts []string
	for i := 0; i < len(pins); i++ {
		for j := i + 1; j < len(pins); j++ {
			if !agreeOnSharedComponents(pins[i].raw, pins[j].raw) {
				conflicts = append(conflicts, fmt.Sprintf("%s declares %s but %s declares %s",
					pins[i].file, pins[i].raw, pins[j].file, pins[j].raw))
			}
		}
	}
	if len(conflicts) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("go toolchain pins disagree — every pin must agree on the version components it declares:\n")
	for _, p := range pins {
		fmt.Fprintf(&b, "  %s: %s\n", p.file, p.raw)
	}
	for _, c := range conflicts {
		fmt.Fprintf(&b, "  conflict: %s\n", c)
	}
	b.WriteString("  fix the pins by hand — this is a check, not a generator")
	return fmt.Errorf("%s", b.String())
}

// agreeOnSharedComponents compares two dotted versions over the components both
// spell out, so 1.27 matches 1.27.0 but 1.27.4 does not.
func agreeOnSharedComponents(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	n := min(len(as), len(bs))
	for i := 0; i < n; i++ {
		if as[i] != bs[i] {
			return false
		}
	}
	return n > 0
}

// moduleRoot walks up from the package directory to the module root.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
		if filepath.Dir(d) == d {
			t.Skip("no go.mod above package")
		}
	}
}

// TestToolchainPinsAgree (AC1) is the guard itself: the three pins this repo
// carries must agree at the granularity each declares.
func TestToolchainPinsAgree(t *testing.T) {
	pins, err := collectPins(moduleRoot(t))
	if err != nil {
		t.Fatalf("reading the toolchain pins: %v", err)
	}
	if len(pins) != len(toolchainPinFiles) {
		t.Fatalf("collected %d pins, want %d", len(pins), len(toolchainPinFiles))
	}
	if err := comparePins(pins); err != nil {
		t.Fatal(err)
	}
}

// TestComparePinsNamesEveryFileOnMismatch (AC2) drives the decision directly:
// one drift case per file class, the exact-vs-minor granularity rule in both
// directions, and an all-agree control. Every failure must name all three files
// and every declared version — the message is part of the contract.
func TestComparePinsNamesEveryFileOnMismatch(t *testing.T) {
	pinsWith := func(goMod, mise, releaseYML string) []pin {
		return []pin{
			{file: "go.mod", raw: goMod},
			{file: "mise.toml", raw: mise},
			{file: ".github/workflows/release.yml", raw: releaseYML},
		}
	}
	cases := []struct {
		name   string
		pins   []pin
		wantOK bool
	}{
		{name: "all agree", pins: pinsWith("1.27.0", "1.27.0", "1.27"), wantOK: true},
		{name: "minor-only CI floats the patch", pins: pinsWith("1.27.9", "1.27.9", "1.27"), wantOK: true},
		{name: "a CI pin that names a patch may match it", pins: pinsWith("1.27.4", "1.27.4", "1.27.4"), wantOK: true},
		{name: "go.mod drifted", pins: pinsWith("1.28.0", "1.27.0", "1.27")},
		{name: "mise.toml drifted", pins: pinsWith("1.27.0", "1.26.0", "1.27")},
		{name: "release.yml drifted", pins: pinsWith("1.27.0", "1.27.0", "1.28")},
		{name: "same minor, different patch", pins: pinsWith("1.27.0", "1.27.3", "1.27")},
		{name: "a CI pin that names the wrong patch", pins: pinsWith("1.27.0", "1.27.0", "1.27.4")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := comparePins(c.pins)
			if c.wantOK {
				if err != nil {
					t.Fatalf("pins agree at their declared granularity, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("drift must fail the check")
			}
			msg := err.Error()
			for _, p := range c.pins {
				if !strings.Contains(msg, p.file) {
					t.Errorf("message must name %s:\n%s", p.file, msg)
				}
				if !strings.Contains(msg, p.raw) {
					t.Errorf("message must name the version %s declares (%s):\n%s", p.file, p.raw, msg)
				}
			}
			if !strings.Contains(msg, "check, not a generator") {
				t.Errorf("message should say the fix is by hand:\n%s", msg)
			}
		})
	}
}

// TestCollectPinsReadsEachFileClass (AC2) covers the PARSERS, not just the
// comparator: a temp tree in the real shapes, clean and then drifted one file at
// a time.
func TestCollectPinsReadsEachFileClass(t *testing.T) {
	const (
		goMod = "module example.com/x\n\ngo 1.27.0\n\nrequire (\n\tgithub.com/spf13/cobra v1.10.2\n)\n"
		mise  = "# comment header\n# go lives under [tools]\n[tools]\nnode = \"26.7.0\"\ngo = \"1.27.0\"\n"
		wf    = "name: test\njobs:\n  test:\n    steps:\n      - uses: actions/setup-go@v6\n        with:\n          go-version: '1.27'\n          check-latest: true\n"
	)
	write := func(root string, files map[string]string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0o755); err != nil {
			t.Fatal(err)
		}
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	clean := func() map[string]string {
		return map[string]string{
			"go.mod":                        goMod,
			"mise.toml":                     mise,
			".github/workflows/release.yml": wf,
		}
	}

	root := t.TempDir()
	write(root, clean())
	pins, err := collectPins(root)
	if err != nil {
		t.Fatalf("clean tree: %v", err)
	}
	if err := comparePins(pins); err != nil {
		t.Fatalf("clean tree should agree: %v", err)
	}
	for _, want := range []string{"1.27.0", "1.27.0", "1.27"} {
		found := false
		for _, p := range pins {
			if p.raw == want {
				found = true
			}
		}
		if !found {
			t.Errorf("no pin parsed as %q: %+v", want, pins)
		}
	}

	for _, c := range []struct{ name, file, body string }{
		{"go.mod", "go.mod", strings.Replace(goMod, "go 1.27.0", "go 1.28.0", 1)},
		{"mise.toml", "mise.toml", strings.Replace(mise, "1.27.0", "1.26.7", 1)},
		{"release.yml", ".github/workflows/release.yml", strings.Replace(wf, "'1.27'", "'1.28'", 1)},
	} {
		t.Run("drifted "+c.name, func(t *testing.T) {
			root := t.TempDir()
			files := clean()
			files[c.file] = c.body
			write(root, files)
			pins, err := collectPins(root)
			if err != nil {
				t.Fatalf("collect: %v", err)
			}
			err = comparePins(pins)
			if err == nil {
				t.Fatal("drift in " + c.file + " must fail the check")
			}
			if !strings.Contains(err.Error(), c.file) {
				t.Errorf("message must name the drifted file:\n%v", err)
			}
		})
	}
}

// TestPinParsersRejectTheAmbiguousCases pins parser behaviour on the shapes that
// would otherwise read the wrong number: a toolchain line beside the directive,
// a go pin under another table, and a workflow with two disagreeing setup-go
// steps.
func TestPinParsersRejectTheAmbiguousCases(t *testing.T) {
	// A `toolchain` line and a require block must not be mistaken for the directive.
	got, err := parseGoModDirective([]byte("module m\n\ngo 1.27.0\n\ntoolchain go1.27.3\n\nrequire (\n\tgo.uber.org/zap v1.0.0\n)\n"))
	if err != nil || got != "1.27.0" {
		t.Errorf("go.mod directive = (%q, %v), want 1.27.0", got, err)
	}
	if _, err := parseGoModDirective([]byte("module m\n")); err == nil {
		t.Error("a go.mod with no directive must be an error, not an empty pin")
	}

	// The [tools] table is what counts.
	got, err = parseMiseGoPin([]byte("[env]\ngo = \"not-a-tool-pin\"\n\n[tools]\ngo = \"1.27.0\"\n"))
	if err != nil || got != "1.27.0" {
		t.Errorf("mise pin = (%q, %v), want 1.27.0", got, err)
	}
	if _, err := parseMiseGoPin([]byte("[tools]\nnode = \"26.7.0\"\n")); err == nil {
		t.Error("a mise.toml with no go pin must be an error")
	}

	// Two setup-go steps that agree are fine; two that disagree are drift.
	agreeing := "      - uses: actions/setup-go@v6\n        with:\n          go-version: '1.27'\n      - uses: actions/setup-go@v6\n        with:\n          go-version: '1.27'\n"
	if got, err := parseWorkflowGoVersion([]byte(agreeing)); err != nil || got != "1.27" {
		t.Errorf("agreeing duplicate pins = (%q, %v), want 1.27", got, err)
	}
	disagreeing := strings.Replace(agreeing, "go-version: '1.27'\n      - uses", "go-version: '1.26'\n      - uses", 1)
	if _, err := parseWorkflowGoVersion([]byte(disagreeing)); err == nil {
		t.Error("a workflow pinning two different versions is itself drift")
	}
	if _, err := parseWorkflowGoVersion([]byte("name: test\n")); err == nil {
		t.Error("a workflow with no go-version pin must be an error")
	}
}
