package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
)

func TestPackFetchWritesVerifiedPack(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	newMachine(t)

	files := []fakeserver.PackFile{
		{Path: "SKILL.md", Body: []byte("# Review skill\n")},
		{Path: "scripts/check.sh", Body: []byte("#!/bin/sh\necho ok\n"), Executable: true},
		{Path: "sub/data.txt", Body: []byte("hello world\n")},
	}
	bundle, digest := fakeserver.PackBundle("review", "1.2.3", files)
	srv.SeedPack("acme/review", "1.2.3", bundle)

	newDir := filepath.Join(t.TempDir(), "dest")
	out := must(t, "pack", "fetch", "acme/review", "--version", "1.2.3", "--dir", newDir, "--server", srv.URL)
	wantLine := "fetched acme/review@1.2.3 (3 file(s), digest " + digest[:12] + ") into " + newDir
	if strings.TrimSpace(out) != wantLine {
		t.Fatalf("output = %q, want %q", out, wantLine)
	}
	for _, f := range files {
		p := filepath.Join(newDir, filepath.FromSlash(f.Path))
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", f.Path, err)
		}
		if string(got) != string(f.Body) {
			t.Errorf("%s = %q, want %q", f.Path, got, f.Body)
		}
		if runtime.GOOS != "windows" {
			st, err := os.Stat(p)
			if err != nil {
				t.Fatal(err)
			}
			if (st.Mode()&0o111 != 0) != f.Executable {
				t.Errorf("%s mode %o executable=%v, want %v", f.Path, st.Mode().Perm(), st.Mode()&0o111 != 0, f.Executable)
			}
		}
	}

	emptyDir := filepath.Join(t.TempDir(), "empty")
	if err := os.MkdirAll(emptyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	raw := must(t, "pack", "fetch", "acme/review", "--version", "1.2.3", "--dir", emptyDir, "--server", srv.URL, "--json")
	var got struct {
		Ref     string   `json:"ref"`
		Name    string   `json:"name"`
		Version string   `json:"version"`
		Digest  string   `json:"digest"`
		Dir     string   `json:"dir"`
		Files   []string `json:"files"`
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("json %v:\n%s", err, raw)
	}
	wantFiles := []string{"SKILL.md", "scripts/check.sh", "sub/data.txt"}
	if got.Ref != "acme/review" || got.Name != "review" || got.Version != "1.2.3" || got.Digest != digest || got.Dir != emptyDir || !slices.Equal(got.Files, wantFiles) {
		t.Fatalf("json = %+v", got)
	}
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(emptyDir, filepath.FromSlash(f.Path)))
		if err != nil || string(b) != string(f.Body) {
			t.Errorf("emptyDir %s = %q (%v)", f.Path, b, err)
		}
	}

	if _, err := run(t, "", "pack", "fetch", "acme/review", "--dir", filepath.Join(t.TempDir(), "x"), "--server", srv.URL); err == nil || !strings.Contains(err.Error(), "--version is required") {
		t.Errorf("missing --version: %v", err)
	}
	if _, err := run(t, "", "pack", "fetch", "acme/review", "--version", "1.2.3", "--server", srv.URL); err == nil || !strings.Contains(err.Error(), "--dir is required") {
		t.Errorf("missing --dir: %v", err)
	}

	wrongVer, _ := fakeserver.PackBundle("review", "2.0.0", files)
	srv.SeedPack("acme/review", "1.2.4", wrongVer)
	verDir := filepath.Join(t.TempDir(), "ver-mismatch")
	if _, err := run(t, "", "pack", "fetch", "acme/review", "--version", "1.2.4", "--dir", verDir, "--server", srv.URL); err == nil || !strings.Contains(err.Error(), "2.0.0") || !strings.Contains(err.Error(), "1.2.4") {
		t.Errorf("version mismatch: %v", err)
	}
	if _, err := os.Stat(verDir); !os.IsNotExist(err) {
		t.Errorf("verDir created on version mismatch: %v", err)
	}
}

func TestPackFetchRejectsTamperedFileAndBadDigest(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	newMachine(t)

	files := []fakeserver.PackFile{
		{Path: "SKILL.md", Body: []byte("# Good\n")},
		{Path: "scripts/run.sh", Body: []byte("#!/bin/sh\necho ok\n"), Executable: true},
	}

	tampered, _ := fakeserver.PackBundle("tampered", "1.0.0", files)
	tampered.Files[1].Body = []byte("#!/bin/sh\necho evil\n")
	srv.SeedPack("acme/tampered", "1.0.0", tampered)

	badDigest, validDigest := fakeserver.PackBundle("baddigest", "1.0.0", files)
	wrongDigest := strings.Repeat("a", 64)
	badDigest.Version.Manifest.Digest = wrongDigest
	srv.SeedPack("acme/baddigest", "1.0.0", badDigest)

	cases := []struct {
		name string
		ref  string
		want []string
	}{
		{"tampered file", "acme/tampered", []string{"scripts/run.sh", "sha256"}},
		{"bad digest", "acme/baddigest", []string{"digest", wrongDigest, validDigest}},
	}
	for _, tc := range cases {
		t.Run(tc.name+" non-existent dir", func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "out")
			_, err := run(t, "", "pack", "fetch", tc.ref, "--version", "1.0.0", "--dir", dir, "--server", srv.URL)
			if err == nil {
				t.Fatal("expected error")
			}
			for _, sub := range tc.want {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("error %q missing %q", err.Error(), sub)
				}
			}
			if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
				t.Fatalf("dir %s should not exist: %v", dir, statErr)
			}
		})

		t.Run(tc.name+" existing empty dir", func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "empty")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			_, err := run(t, "", "pack", "fetch", tc.ref, "--version", "1.0.0", "--dir", dir, "--server", srv.URL)
			if err == nil {
				t.Fatal("expected error")
			}
			for _, sub := range tc.want {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("error %q missing %q", err.Error(), sub)
				}
			}
			entries, rErr := os.ReadDir(dir)
			if rErr != nil || len(entries) != 0 {
				t.Fatalf("existing empty dir modified: %v (%v)", entries, rErr)
			}
		})
	}
}

func TestPackFetchRejectsBadPathsAndDuplicates(t *testing.T) {
	srv := fakeserver.Start(t, testToken)
	newMachine(t)

	cases := []struct {
		name    string
		ref     string
		badPath string
		files   []fakeserver.PackFile
	}{
		{
			name:    "absolute path",
			ref:     "acme/abs",
			badPath: "/abs/SKILL.md",
			files:   []fakeserver.PackFile{{Path: "/abs/SKILL.md", Body: []byte("bad")}},
		},
		{
			name:    "parent traversal",
			ref:     "acme/dotdot",
			badPath: "../escape/SKILL.md",
			files:   []fakeserver.PackFile{{Path: "../escape/SKILL.md", Body: []byte("bad")}},
		},
		{
			name:    "nested traversal",
			ref:     "acme/nested-dotdot",
			badPath: "sub/../../escape",
			files:   []fakeserver.PackFile{{Path: "sub/../../escape", Body: []byte("bad")}},
		},
		{
			name:    "duplicate path",
			ref:     "acme/dup",
			badPath: "dup/SKILL.md",
			files: []fakeserver.PackFile{
				{Path: "dup/SKILL.md", Body: []byte("first")},
				{Path: "dup/SKILL.md", Body: []byte("second")},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bundle, _ := fakeserver.PackBundle(tc.name, "1.0.0", tc.files)
			srv.SeedPack(tc.ref, "1.0.0", bundle)

			parent := t.TempDir()
			dir := filepath.Join(parent, "target")
			_, err := run(t, "", "pack", "fetch", tc.ref, "--version", "1.0.0", "--dir", dir, "--server", srv.URL)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.badPath) {
				t.Errorf("error %q does not name %q", err.Error(), tc.badPath)
			}
			entries, rErr := os.ReadDir(parent)
			if rErr != nil || len(entries) != 0 {
				t.Fatalf("something was written under parent %s: %v (%v)", parent, entries, rErr)
			}
		})
	}
}

func TestPackFetchUnreadableAndMissingIsNotFound(t *testing.T) {
	srv := fakeserver.Start(t, testToken, "tok-other")
	newMachine(t)

	bundle, _ := fakeserver.PackBundle("secret", "1.0.0", []fakeserver.PackFile{
		{Path: "SKILL.md", Body: []byte("private")},
	})
	srv.SeedPack("acme/secret", "1.0.0", bundle, "tok-other")

	for _, tc := range []struct {
		name string
		ref  string
	}{
		{"unreadable pack", "acme/secret"},
		{"unknown pack", "acme/does-not-exist"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newDir := filepath.Join(t.TempDir(), "out")
			_, err := run(t, "", "pack", "fetch", tc.ref, "--version", "1.0.0", "--dir", newDir, "--server", srv.URL)
			if err == nil || !strings.Contains(err.Error(), "not found") {
				t.Fatalf("got %v, want not found", err)
			}
			if _, statErr := os.Stat(newDir); !os.IsNotExist(statErr) {
				t.Fatalf("dir %s should not exist: %v", newDir, statErr)
			}

			emptyDir := filepath.Join(t.TempDir(), "empty")
			if err := os.MkdirAll(emptyDir, 0o755); err != nil {
				t.Fatal(err)
			}
			_, err = run(t, "", "pack", "fetch", tc.ref, "--version", "1.0.0", "--dir", emptyDir, "--server", srv.URL)
			if err == nil || !strings.Contains(err.Error(), "not found") {
				t.Fatalf("existing empty dir: got %v, want not found", err)
			}
			entries, rErr := os.ReadDir(emptyDir)
			if rErr != nil || len(entries) != 0 {
				t.Fatalf("existing empty dir modified: %v (%v)", entries, rErr)
			}
		})
	}
}
