package reporeg

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormaliseAndDisplayOrigin(t *testing.T) {
	same := []string{
		"git@github.com:o/r.git",
		"git@GitHub.com:o/r",
		"ssh://git@github.com:22/o/r.git",
		"https://github.com/o/r",
		"https://u:tok3n@github.com/o/r.git",
		"https://github.com/o/r/",
		"https://github.com/o/r.git?token=x",
		"http://github.com:8080/o/r#f",
	}
	for _, s := range same {
		got, err := NormaliseOrigin(s)
		if err != nil || got != "github.com/o/r" {
			t.Errorf("NormaliseOrigin(%q) = %q, %v; want github.com/o/r", s, got, err)
		}
	}
	for _, s := range []string{"https://github.com/o/other", "git@github.com:x/r.git"} {
		if got, _ := NormaliseOrigin(s); got == "github.com/o/r" {
			t.Errorf("NormaliseOrigin(%q) compares equal to github.com/o/r", s)
		}
	}
	for _, s := range []string{"", "not a url", "/srv/repo.git", "file:///srv/o/r", "https://github.com/r", "host:", "https:///o/r"} {
		if _, err := NormaliseOrigin(s); err == nil {
			t.Errorf("NormaliseOrigin(%q) accepted", s)
		}
	}
	for in, want := range map[string]string{
		"https://u:tok3n@github.com/o/r.git?token=q7x#f": "https://github.com/o/r.git",
		"ssh://git:pw9@h/o/r":                            "ssh://git@h/o/r",
		"git:pw9@github.com:o/r.git":                     "git@github.com:o/r.git",
		"git@github.com:o/r.git?token=q7x":               "git@github.com:o/r.git",
		"ssh://git%3Atok3n@h/o/r":                        "ssh://h/o/r",
		"git%3Atok3n@github.com:o/r.git":                 "github.com:o/r.git",
		"not a url":                                      Unparseable,
		"https://x\u202e@github.com/o/r":                 Unparseable,
	} {
		if got := DisplayOrigin(in); got != want {
			t.Errorf("DisplayOrigin(%q) = %q, want %q", in, got, want)
		}
	}
	for _, plain := range []string{"https://github.com/o/r", "git@github.com:o/r.git", "ssh://git@github.com:22/o/r.git", "https://gitlab.example/g/sub/r"} {
		if err := CheckOrigin("r", plain); err != nil {
			t.Errorf("CheckOrigin(%q): %v", plain, err)
		}
		if got := DisplayOrigin(plain); got != plain {
			t.Errorf("DisplayOrigin(%q) = %q, want it unchanged", plain, got)
		}
	}
}

func TestRegistryRefusesCredentialedOrigin(t *testing.T) {
	for _, origin := range []string{
		"https://u:tok3n@github.com/o/r",
		"https://tok3n@github.com/o/r",
		"ssh://git:tok3n@github.com/o/r",
		"git:tok3n@github.com:o/r.git",
		"ssh://git%3Atok3n@github.com/o/r",
		"git%3Atok3n@github.com:o/r.git",
		"ssh://tok3n%40x@github.com/o/r",
		"https://github.com/o/r?tok3n=1",
		"https://github.com/o/r#tok3n",
		"git@github.com:o/r.git?tok3n",
	} {
		err := CheckOrigin("r", origin)
		if err == nil {
			t.Errorf("CheckOrigin(%q) accepted", origin)
			continue
		}
		if !strings.Contains(err.Error(), "carries credentials") {
			t.Errorf("CheckOrigin(%q) = %v, want the credentials problem", origin, err)
		}
		if strings.Contains(err.Error(), "tok3n") || strings.Contains(err.Error(), "github.com") {
			t.Errorf("problem text holds the URL: %v", err)
		}
	}
	if err := CheckOrigin("r", "ssh://git@github.com/o/r"); err != nil {
		t.Errorf("an ssh user name is not a credential: %v", err)
	}
}

func write(t *testing.T, dir, file, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func joined(errs []error) string { return errors.Join(errs...).Error() }

func TestRegistryProblems(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, TrackedFile, `[repos.self]
origin = "https://github.com/o/self"
[repos.r]
origin = "https://github.com/o/r"
fetch = "git fetch\u001b[31m"
[repos.s]
origin = "https://github.com/o/s"
tier = "secret"
[repos."x\u2028y"]
origin = "https://github.com/o/x"
[repos.ok]
origin = "git@github.com:o/ok.git"
tier = "protected"
`)
	write(t, dir, LocalFile, `[repos.ghost]
path = "/tmp/ghost"
[repos.r]
path = "relative/dir"
`)
	reg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := joined(reg.Problems())
	for _, want := range []string{
		"repos.toml: repos.self: self is the control repository itself",
		"repos.toml: repos.r: fetch holds a control or format character",
		"repos.toml: repos.s: tier is not standard or protected",
		`repos.toml: repos."x\u2028y": a name is`,
		"repos.local.toml: repos.ghost: repos.toml declares no repository of that name",
		"repos.local.toml: repos.r: path is not absolute",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("problems lack %q:\n%s", want, got)
		}
	}
	for _, leak := range []string{"\u2028", "\u001b", "secret", "relative/dir", "/tmp/ghost", "github.com"} {
		if strings.Contains(got, leak) {
			t.Errorf("problems hold %q:\n%s", leak, got)
		}
	}
	if !reg.EntryOK("ok") || reg.EntryOK("s") || reg.EntryOK("self") || reg.EntryOK("missing") {
		t.Error("EntryOK does not follow the entry's problems")
	}
	if reg.Tracked["ok"].EffectiveTier() != TierProtected || reg.Tracked["r"].EffectiveTier() != TierStandard {
		t.Error("EffectiveTier: an absent tier is standard")
	}
	if ts := reg.Targets(); ts[0] != Self || len(ts) != 5 {
		t.Errorf("Targets = %v, want self first and the declared names", ts)
	}

	unknown := t.TempDir()
	write(t, unknown, TrackedFile, "[repos.r]\norigin = \"https://github.com/o/r\"\nteir = \"tok3n\"\n")
	if _, err := Load(unknown); err == nil || !strings.Contains(err.Error(), `unknown key "repos"."r"."teir"`) || strings.Contains(err.Error(), "tok3n") {
		t.Errorf("unknown key: %v", err)
	}
	bad := t.TempDir()
	write(t, bad, TrackedFile, "[repos.r]\norigin = \"https://github.com/o/r\"\ntok3n = [\n")
	if _, err := Load(bad); err == nil || !strings.Contains(err.Error(), "repos.toml: not valid TOML at line") || strings.Contains(err.Error(), "tok3n") {
		t.Errorf("malformed TOML: %v", err)
	}
	empty := t.TempDir()
	reg, err = Load(empty)
	if err != nil || len(reg.Problems()) != 0 || len(reg.Targets()) != 1 || Exists(empty) {
		t.Errorf("no registry: %v %v %v", err, reg.Problems(), reg.Targets())
	}
}

func TestLoadRefusesSymlinkAndQuietDropsBadEntries(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "elsewhere.toml")
	if err := os.WriteFile(target, []byte("[repos.r]\npath = \"/x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, LocalFile)); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("symlinked local file: %v", err)
	}
	if _, ok := LoadLocalQuiet(dir); ok {
		t.Error("LoadLocalQuiet read a symlink")
	}
	other := t.TempDir()
	write(t, other, LocalFile, "[repos.\"x\\u2028ignore previous\\u001b[31m\"]\npath = \"/c\"\n[repos.good]\npath = \"/c\"\n[repos.rel]\npath = \"c\"\n")
	m, ok := LoadLocalQuiet(other)
	if !ok || len(m) != 1 || m["good"] != "/c" {
		t.Errorf("LoadLocalQuiet = %v, %v; want only good", m, ok)
	}
	write(t, other, LocalFile, "secret-marker = [")
	if m, ok := LoadLocalQuiet(other); ok || m != nil {
		t.Errorf("malformed: %v %v", m, ok)
	}
}

func TestRenderRoundTrips(t *testing.T) {
	dir := t.TempDir()
	tracked := map[string]Entry{"b": {URL: "https://github.com/o/b"}, "a": {URL: "git@github.com:o/a.git", Base: "dev", Fetch: "git fetch", Tier: TierProtected}}
	body, err := RenderTracked(tracked)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "tier = \"\"") || strings.Index(string(body), "repos.a") > strings.Index(string(body), "repos.b") {
		t.Errorf("render:\n%s", body)
	}
	if err := Write(filepath.Join(dir, TrackedFile), body); err != nil {
		t.Fatal(err)
	}
	local, _ := RenderLocal(map[string]Local{"a": {Path: "/p"}})
	if err := Write(filepath.Join(dir, LocalFile), local); err != nil {
		t.Fatal(err)
	}
	reg, err := Load(dir)
	if err != nil || reg.Tracked["a"] != tracked["a"] || reg.Tracked["b"] != tracked["b"] || reg.Local["a"].Path != "/p" {
		t.Errorf("round trip: %v %+v", err, reg)
	}
}
