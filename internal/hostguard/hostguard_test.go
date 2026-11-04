package hostguard

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func capture(t *testing.T, r Roots) Snapshot {
	t.Helper()
	s, err := Capture(r)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestResolveRootsOrder(t *testing.T) {
	t.Setenv("HOME", "/home/real")
	t.Setenv("SATELLE_HOME", "/sandbox/satelle")
	t.Setenv("XDG_CONFIG_HOME", "/sandbox/xdg")
	t.Setenv(HostHomeEnv, "")

	got := ResolveRoots()
	want := Roots{Home: "/home/real", SatelleHome: "/home/real/.satelle", BinDir: "/home/real/.local/bin"}
	if got != want {
		t.Fatalf("with only HOME: got %+v, want %+v (SATELLE_HOME and XDG_* must be ignored)", got, want)
	}

	t.Setenv(HostHomeEnv, "/home/host")
	got = ResolveRoots()
	want = Roots{Home: "/home/host", SatelleHome: "/home/host/.satelle", BinDir: "/home/host/.local/bin"}
	if got != want {
		t.Fatalf("with %s set: got %+v, want %+v", HostHomeEnv, got, want)
	}
}

func TestIsRuntimeKeyAndIgnored(t *testing.T) {
	for _, n := range []string{"satelle-16882c39", "001-a1b2c3d4"} {
		if !IsRuntimeKey(n) {
			t.Errorf("%q should be a runtime key", n)
		}
	}
	for _, n := range []string{"config.toml", "serve", "satelle-xyz", "satelle-16882c3"} {
		if IsRuntimeKey(n) {
			t.Errorf("%q should not be a runtime key", n)
		}
	}
	for _, n := range []string{"document-sync-state.json", "serve", "run"} {
		if !Ignored(n) {
			t.Errorf("%q should be ignored", n)
		}
	}
	for _, n := range []string{"config.toml", "db-wal", "x.journal", "serve.log"} {
		if Ignored(n) {
			t.Errorf("%q must not be ignored: no suffix rule", n)
		}
	}
}

func TestFingerprintFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bin")
	if FingerprintFile(filepath.Join(dir, "absent")) != "" || FingerprintFile("") != "" || FingerprintFile(dir) != "" {
		t.Fatal("absent, empty path and directory must fingerprint to empty")
	}
	write(t, p, "v1")
	a := FingerprintFile(p)
	if !strings.HasPrefix(a, "sha256:") || !strings.HasSuffix(a, ":2") {
		t.Fatalf("unexpected fingerprint shape %q", a)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(p, future, future); err != nil {
		t.Fatal(err)
	}
	if FingerprintFile(p) != a {
		t.Fatal("a no-op touch must not change the fingerprint")
	}
	write(t, p, "v2")
	if FingerprintFile(p) == a {
		t.Fatal("content change must change the fingerprint")
	}
}

func TestCaptureMissingRoots(t *testing.T) {
	root := t.TempDir()
	s := capture(t, Roots{SatelleHome: filepath.Join(root, "nope"), BinDir: filepath.Join(root, "nobin")})
	if len(s.Entries) != 0 || len(s.Files) != 0 {
		t.Fatalf("missing satelle home must capture empty: %+v", s)
	}
	for _, n := range BinNames {
		if s.Bins[n] != "" {
			t.Fatalf("missing bin dir: %s = %q", n, s.Bins[n])
		}
	}
	if empty := capture(t, Roots{}); len(empty.Entries) != 0 {
		t.Fatalf("zero roots must capture empty: %+v", empty)
	}
}

func TestCaptureUnreadableRootErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads through mode 000")
	}
	home := filepath.Join(t.TempDir(), ".satelle")
	if err := os.Mkdir(home, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(home, 0o700)
	if _, err := Capture(Roots{SatelleHome: home}); err == nil {
		t.Fatal("an unreadable root must be an error")
	}
}

func TestCaptureAndDiff(t *testing.T) {
	root := t.TempDir()
	r := Roots{SatelleHome: filepath.Join(root, ".satelle"), BinDir: filepath.Join(root, "bin")}
	write(t, filepath.Join(r.SatelleHome, "config.toml"), "a = 1\n")
	write(t, filepath.Join(r.SatelleHome, "satelle-16882c39", "db-wal"), "w1")
	write(t, filepath.Join(r.SatelleHome, "serve", "mirror.db-wal"), "w1")
	write(t, filepath.Join(r.SatelleHome, "document-sync-state.json"), "{}")
	write(t, filepath.Join(r.BinDir, "satelle"), "v1")
	before := capture(t, r)

	if _, ok := before.Entries["serve"]; ok {
		t.Fatal("serve must be ignored")
	}
	if _, ok := before.Entries["document-sync-state.json"]; ok {
		t.Fatal("document-sync-state.json must be ignored")
	}
	if _, ok := before.KeyDirs()["satelle-16882c39"]; !ok || len(before.KeyDirs()) != 1 {
		t.Fatalf("KeyDirs = %v", before.KeyDirs())
	}
	if d := Diff(before, capture(t, r)); len(d) != 0 {
		t.Fatalf("identical captures must not diff: %v", d)
	}

	// Churn a live satelle process does on its own: ignored names and key-dir contents.
	write(t, filepath.Join(r.SatelleHome, "document-sync-state.json"), `{"n":2}`)
	write(t, filepath.Join(r.SatelleHome, "run", "healthz.json"), `{"ok":true}`)
	write(t, filepath.Join(r.SatelleHome, "serve", "mirror.db-wal"), "w2")
	write(t, filepath.Join(r.SatelleHome, "satelle-16882c39", "db-wal"), "w2")
	write(t, filepath.Join(r.SatelleHome, "satelle-16882c39", "db-journal"), "j")
	future := time.Now().Add(time.Hour)
	_ = os.Chtimes(filepath.Join(r.BinDir, "satelle"), future, future)
	if d := Diff(before, capture(t, r)); len(d) != 0 {
		t.Fatalf("live-satelle churn and a touch must not diff: %v", d)
	}

	cases := []struct {
		name   string
		mutate func(r Roots)
		want   string
	}{
		{"new top-level dir", func(r Roots) { write(t, filepath.Join(r.SatelleHome, "scratch-x", "f"), "x") }, "added entry scratch-x"},
		{"new key dir", func(r Roots) { write(t, filepath.Join(r.SatelleHome, "other-0123abcd", "f"), "x") }, "added entry other-0123abcd"},
		{"new top-level file", func(r Roots) { write(t, filepath.Join(r.SatelleHome, "new.toml"), "x") }, "added entry new.toml"},
		{"changed file", func(r Roots) { write(t, filepath.Join(r.SatelleHome, "config.toml"), "a = 2\n") }, "changed file config.toml"},
		{"removed entry", func(r Roots) { _ = os.Remove(filepath.Join(r.SatelleHome, "config.toml")) }, "removed entry config.toml"},
		{"bin changed", func(r Roots) { write(t, filepath.Join(r.BinDir, "satelle"), "v2") }, "changed bin satelle"},
		{"bin appeared", func(r Roots) { write(t, filepath.Join(r.BinDir, "satelled"), "d") }, "appeared bin satelled"},
		{"bin removed", func(r Roots) { _ = os.Remove(filepath.Join(r.BinDir, "satelle")) }, "removed bin satelle"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Each case mutates a fresh copy of the baseline layout.
			rr := Roots{SatelleHome: filepath.Join(t.TempDir(), ".satelle"), BinDir: filepath.Join(t.TempDir(), "bin")}
			write(t, filepath.Join(rr.SatelleHome, "config.toml"), "a = 1\n")
			write(t, filepath.Join(rr.SatelleHome, "satelle-16882c39", "db-wal"), "w1")
			write(t, filepath.Join(rr.BinDir, "satelle"), "v1")
			b := capture(t, rr)
			tc.mutate(rr)
			d := Diff(b, capture(t, rr))
			if len(d) != 1 || d[0] != tc.want {
				t.Fatalf("diff = %v, want [%s]", d, tc.want)
			}
		})
	}
}

// The guard make test and make integration run catches a repo added to the
// machine workspace registry (sty_e3a1c1d5).
func TestCaptureDetectsRegistryAdd(t *testing.T) {
	root := t.TempDir()
	r := Roots{Home: root, SatelleHome: filepath.Join(root, ".satelle"), BinDir: filepath.Join(root, ".local", "bin")}
	t.Setenv("SATELLE_HOME", r.SatelleHome)
	gc, err := config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SaveGlobal(gc); err != nil {
		t.Fatal(err)
	}
	before := capture(t, r)

	if !gc.Workspace.AddRepo("/tmp/satelle/abcdef012345/sty_x/1234abcd/repo") {
		t.Fatal("AddRepo reported the repo already registered")
	}
	if err := config.SaveGlobal(gc); err != nil {
		t.Fatal(err)
	}
	if d := Diff(before, capture(t, r)); len(d) != 1 || d[0] != "changed file config.toml" {
		t.Fatalf("diff = %v, want [changed file config.toml]", d)
	}
}

func TestMarshalParseRoundTrip(t *testing.T) {
	s := Snapshot{
		Entries: map[string]string{"config.toml": "file", "odd name\n": "dir", "satelle-16882c39": "dir"},
		Files:   map[string]string{"config.toml": "sha256:ab:3"},
		Bins:    map[string]string{"satelle": "sha256:cd:9", "satelled": ""},
	}
	got, err := Parse(s.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, s) {
		t.Fatalf("round trip: got %+v, want %+v", got, s)
	}
	for _, bad := range []string{"entry \"a\"\n", "zzz \"a\" \"b\"\n", "entry a b\n"} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("Parse(%q) should fail", bad)
		}
	}
}
