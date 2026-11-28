package reporeg

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type bypass struct {
	in, display string
	spoof       bool
}

var originBypasses = []bypass{
	{"ssh://git%3Atok3n@h/o/r.git", "ssh://h/o/r.git", false},
	{"git%3Atok3n@h:o/r.git", "h:o/r.git", false},
	{"git:s3cret/tok3n@github.com:o/r.git", Unparseable, false},
	{"https://user:s3cret/tok3n@github.com/o/r.git", Unparseable, false},
	{"git:tok3n#x@github.com:o/r.git", Unparseable, false},
	{"git:tok3n?x@github.com:o/r.git", Unparseable, false},
	{"https://tok3n#x@github.com/o/r.git", Unparseable, false},
	{"https://u:1234?x@github.com/o/r.git", Unparseable, false},
	{"https://a:tok3n@b@github.com/o/r.git", Unparseable, false},
	{"git@tok3n@github.com:o/r.git", Unparseable, false},
	{"https://u:tok3n@github.com/o/r.git?token=q7x#f", "https://github.com/o/r.git", false},
	{"ssh://git:pw9@h/o/r", "ssh://h/o/r", false},
	{"git:pw9@github.com:o/r.git", Unparseable, false},
	{"https://github.com/o/r?next=x@tok3n/a/b", Unparseable, false},
	{"https://github.com/o/r#x@tok3n/a/b", Unparseable, false},
	{"git@github.com:o/r?q=a@tok3n:a/b", Unparseable, false},
	{"https://evil.com/a/b/@github.com/acme/payments", Unparseable, true},
	{"https://evil.com/a/b?x=@github.com/acme/payments", Unparseable, true},
	{"https://evil.com/a/b#x@github.com/acme/payments", Unparseable, true},
	{"ssh://evil.com/a/b/@github.com/acme/payments", Unparseable, true},
	{"evil.com:a/b/@github.com:acme/payments", Unparseable, true},
	{"file:///tmp/evil@github.com:acme/payments", Unparseable, true},
	{"git://evil.com/x@github.com:acme/payments", Unparseable, true},
	{`https://evil.com\@github.com/acme/payments`, Unparseable, true},
	{`ssh://evil.com\@github.com/acme/payments`, Unparseable, true},
	{`evil.com\@github.com:acme/payments`, Unparseable, true},
	{"https://evil.com @github.com/acme/payments", Unparseable, true},
}

var secrets = []string{"tok3n", "s3cret", "1234", "q7x", "pw9"}

var plainOrigins = []string{
	"git@github.com:o/r.git",
	"github.com:o/r",
	"ssh://git@github.com:22/o/r.git",
	"https://github.com/o/r",
	"https://gitlab.example.com/g/sub/r.git",
}

func holdsSecret(s string) string {
	for _, sec := range secrets {
		if strings.Contains(s, sec) {
			return sec
		}
	}
	return ""
}

func TestNormaliseAndDisplayOrigin(t *testing.T) {
	for _, b := range originBypasses {
		if got := DisplayOrigin(b.in); got != b.display {
			t.Errorf("DisplayOrigin(%q) = %q, want %q", b.in, got, b.display)
		}
		n, err := NormaliseOrigin(b.in)
		if b.display == Unparseable && !errors.Is(err, ErrUnreadableOrigin) {
			t.Errorf("NormaliseOrigin(%q) = %q, %v; want ErrUnreadableOrigin", b.in, n, err)
		}
		if b.spoof && (n == "github.com/acme/payments" || strings.Contains(DisplayOrigin(b.in), "github.com")) {
			t.Errorf("%q spoofs github.com: %q / %q", b.in, n, DisplayOrigin(b.in))
		}
	}
	for _, s := range []string{
		"git@github.com:o/r.git",
		"git@GitHub.com:o/r",
		"ssh://git@github.com:22/o/r.git",
		"https://github.com/o/r",
		"https://github.com/o/r.git/",
		"https://u:p@github.com/o/r",
		"https://github.com/o/r.git?token=x",
		"http://github.com:8080/o/r#f",
	} {
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
	if got := DisplayOrigin("https://x@github.com/o/r"); got != "https://github.com/o/r" {
		t.Errorf("http(s) userinfo is not dropped: %q", got)
	}
	if got := DisplayOrigin("https://x‮@github.com/o/r"); got != Unparseable {
		t.Errorf("a non-URL character must make the origin unreadable: %q", got)
	}
	for _, in := range []string{"git:tok3n", "not a url", "ssh://git@h‮/o/r"} {
		if got := DisplayOrigin(in); got != Unparseable {
			t.Errorf("DisplayOrigin(%q) = %q, want %q", in, got, Unparseable)
		}
		if _, err := NormaliseOrigin(in); !errors.Is(err, ErrUnreadableOrigin) || strings.Contains(err.Error(), "tok3n") {
			t.Errorf("NormaliseOrigin(%q) = %v", in, err)
		}
	}
	for _, plain := range plainOrigins {
		if err := CheckOrigin("r", plain); err != nil {
			t.Errorf("CheckOrigin(%q): %v", plain, err)
		}
		if got := DisplayOrigin(plain); got != plain {
			t.Errorf("DisplayOrigin(%q) = %q, want it unchanged", plain, got)
		}
	}
	for _, narrowed := range []string{"ssh://git@[::1]/o/r", "HTTPS://github.com/o/r", "git://github.com/o/r", "file:///srv/r.git", "/srv/r.git", "https://github.com/r", ""} {
		if err := CheckOrigin("r", narrowed); err == nil {
			t.Errorf("CheckOrigin(%q) accepted", narrowed)
		}
	}
}

func TestRegistryRefusesCredentialedOrigin(t *testing.T) {
	inputs := []string{
		"https://u:tok3n@github.com/o/r",
		"https://tok3n@github.com/o/r",
		"ssh://git:tok3n@github.com/o/r",
		"ssh://tok3n%40x@github.com/o/r",
		"https://github.com/o/r?tok3n=1",
		"https://github.com/o/r#tok3n",
		"git@github.com:o/r.git?tok3n",
	}
	for _, b := range originBypasses {
		inputs = append(inputs, b.in)
	}
	for _, origin := range inputs {
		err := CheckOrigin("r", origin)
		if err == nil {
			t.Errorf("CheckOrigin(%q) accepted", origin)
			continue
		}
		reg := Registry{Tracked: map[string]Entry{"r": {URL: origin}}}
		text := err.Error() + "\n" + joined(reg.Problems())
		if sec := holdsSecret(text); sec != "" || strings.Contains(text, origin) {
			t.Errorf("problem text for %q holds %q or the URL:\n%s", origin, sec, text)
		}
		if sec := holdsSecret(DisplayOrigin(origin)); sec != "" {
			t.Errorf("DisplayOrigin(%q) = %q holds %q", origin, DisplayOrigin(origin), sec)
		}
	}
	if err := CheckOrigin("r", "https://u:tok3n@github.com/o/r"); err == nil || !strings.Contains(err.Error(), "carries credentials") {
		t.Errorf("reducible credentialed origin: %v", err)
	}
	if err := CheckOrigin("r", "ssh://git@github.com/o/r"); err != nil {
		t.Errorf("an ssh user name is not a credential: %v", err)
	}
}

func authorityHost(x string) string {
	rest, stop := x, "/?#:\\"
	if _, after, ok := strings.Cut(x, "://"); ok {
		rest, stop = after, "/?#\\"
	}
	if i := strings.IndexAny(rest, stop); i >= 0 {
		rest = rest[:i]
	}
	if i := strings.LastIndex(rest, "@"); i >= 0 {
		rest = rest[i+1:]
	}
	if i := strings.LastIndex(rest, ":"); i >= 0 {
		rest = rest[:i]
	}
	return strings.ToLower(rest)
}

func FuzzDisplayOrigin(f *testing.F) {
	for _, b := range originBypasses {
		f.Add(b.in)
	}
	for _, p := range plainOrigins {
		f.Add(p)
	}
	f.Fuzz(func(t *testing.T, x string) {
		d := DisplayOrigin(x)
		if d != Unparseable {
			if err := CheckOrigin("x", d); err != nil {
				t.Fatalf("DisplayOrigin(%q) = %q, which CheckOrigin refuses: %v", x, d, err)
			}
			if strings.ContainsAny(d, "%?#") || strings.Count(d, "@") > 1 {
				t.Fatalf("DisplayOrigin(%q) = %q holds a hiding place", x, d)
			}
		}
		n, err := NormaliseOrigin(x)
		if err != nil {
			return
		}
		host, _, _ := strings.Cut(n, "/")
		if want := authorityHost(x); host != want {
			t.Fatalf("NormaliseOrigin(%q) = %q, host %q is not the authority host %q", x, n, host, want)
		}
	})
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

func TestShellSafePath(t *testing.T) {
	if !ShellSafePath("/tmp/a-b_c.d/e+f@g") {
		t.Error("a safe path was refused")
	}
	for _, p := range []string{"", `/a"b`, "/a'b", "/a$b", "/a`b", `/a\b`, "/a b", "/a\tb", "/a;b", "/a&b", "/a|b", "/a<b", "/a>b", "/a*b", "/a?b", "/a\nb", "/a\x1bb", "/a‮b", "/aéb", "/a=b", "/a~b", "/a(b)"} {
		if ShellSafePath(p) {
			t.Errorf("ShellSafePath(%q) = true", p)
		}
	}
}
