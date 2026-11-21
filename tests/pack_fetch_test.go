//go:build integration

package tests

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
)

func TestPackFetchEndToEnd(t *testing.T) {
	t.Parallel()
	srv := fakeserver.Start(t, devToken, "tok-other")
	s := newSandbox(t)
	s.env["SATELLE_TOKEN"] = devToken

	files := []fakeserver.PackFile{
		{Path: "SKILL.md", Body: []byte("# Pack skill\n")},
		{Path: "scripts/run.sh", Body: []byte("#!/bin/sh\necho ok\n"), Executable: true},
		{Path: "sub/readme.txt", Body: []byte("details\n")},
	}
	valid, digest := fakeserver.PackBundle("review", "1.0.0", files)
	srv.SeedPack("acme/review", "1.0.0", valid)

	t.Run("AC1 writes verified pack", func(t *testing.T) {
		newDir := filepath.Join(t.TempDir(), "pack-out")
		out := s.must("pack", "fetch", "acme/review", "--version", "1.0.0", "--dir", newDir, "--server", srv.URL)
		wantLine := "fetched acme/review@1.0.0 (3 file(s), digest " + digest[:12] + ") into " + newDir
		if strings.TrimSpace(out) != wantLine {
			t.Fatalf("stdout = %q, want %q", out, wantLine)
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

		emptyDir := filepath.Join(t.TempDir(), "empty-out")
		if err := os.MkdirAll(emptyDir, 0o755); err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Ref     string   `json:"ref"`
			Name    string   `json:"name"`
			Version string   `json:"version"`
			Digest  string   `json:"digest"`
			Dir     string   `json:"dir"`
			Files   []string `json:"files"`
		}
		s.mustJSON(&doc, "pack", "fetch", "acme/review", "--version", "1.0.0", "--dir", emptyDir, "--server", srv.URL)
		wantPaths := []string{"SKILL.md", "scripts/run.sh", "sub/readme.txt"}
		if doc.Ref != "acme/review" || doc.Name != "review" || doc.Version != "1.0.0" || doc.Digest != digest || doc.Dir != emptyDir || !slices.Equal(doc.Files, wantPaths) {
			t.Fatalf("json = %+v", doc)
		}
	})

	t.Run("AC2 rejects tampered file and bad digest", func(t *testing.T) {
		tampered, _ := fakeserver.PackBundle("tampered", "1.0.0", files)
		tampered.Files[0].Body = []byte("# Tampered\n")
		srv.SeedPack("acme/tampered", "1.0.0", tampered)

		badDigest, _ := fakeserver.PackBundle("baddigest", "1.0.0", files)
		wrongDigest := strings.Repeat("b", 64)
		badDigest.Version.Manifest.Digest = wrongDigest
		srv.SeedPack("acme/baddigest", "1.0.0", badDigest)

		for _, tc := range []struct {
			ref  string
			want []string
		}{
			{"acme/tampered", []string{"SKILL.md", "sha256"}},
			{"acme/baddigest", []string{"digest", wrongDigest, digest}},
		} {
			dir := filepath.Join(t.TempDir(), "nope")
			r := s.run("pack", "fetch", tc.ref, "--version", "1.0.0", "--dir", dir, "--server", srv.URL)
			if r.code == 0 {
				t.Fatalf("%s unexpectedly succeeded:\n%s", tc.ref, r.out)
			}
			for _, sub := range tc.want {
				if !strings.Contains(r.err, sub) {
					t.Errorf("%s stderr %q missing %q", tc.ref, r.err, sub)
				}
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("%s created dir %s: %v", tc.ref, dir, err)
			}

			emptyDir := filepath.Join(t.TempDir(), "empty")
			if err := os.MkdirAll(emptyDir, 0o755); err != nil {
				t.Fatal(err)
			}
			r = s.run("pack", "fetch", tc.ref, "--version", "1.0.0", "--dir", emptyDir, "--server", srv.URL)
			if r.code == 0 {
				t.Fatalf("%s on empty dir unexpectedly succeeded", tc.ref)
			}
			entries, err := os.ReadDir(emptyDir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("%s modified empty dir: %v (%v)", tc.ref, entries, err)
			}
		}
	})

	t.Run("AC3 rejects bad paths and duplicates", func(t *testing.T) {
		for _, tc := range []struct {
			ref     string
			badPath string
			files   []fakeserver.PackFile
		}{
			{"acme/abs", "/abs/SKILL.md", []fakeserver.PackFile{{Path: "/abs/SKILL.md", Body: []byte("x")}}},
			{"acme/dotdot", "../escape/SKILL.md", []fakeserver.PackFile{{Path: "../escape/SKILL.md", Body: []byte("x")}}},
			{"acme/nested-dotdot", "sub/../../escape", []fakeserver.PackFile{{Path: "sub/../../escape", Body: []byte("x")}}},
			{"acme/dup", "dup/SKILL.md", []fakeserver.PackFile{
				{Path: "dup/SKILL.md", Body: []byte("one")},
				{Path: "dup/SKILL.md", Body: []byte("two")},
			}},
		} {
			b, _ := fakeserver.PackBundle("bad", "1.0.0", tc.files)
			srv.SeedPack(tc.ref, "1.0.0", b)

			parent := t.TempDir()
			dir := filepath.Join(parent, "target")
			r := s.run("pack", "fetch", tc.ref, "--version", "1.0.0", "--dir", dir, "--server", srv.URL)
			if r.code == 0 {
				t.Fatalf("%s unexpectedly succeeded", tc.ref)
			}
			if !strings.Contains(r.err, tc.badPath) {
				t.Errorf("%s stderr %q missing %q", tc.ref, r.err, tc.badPath)
			}
			entries, err := os.ReadDir(parent)
			if err != nil || len(entries) != 0 {
				t.Fatalf("%s wrote under parent: %v (%v)", tc.ref, entries, err)
			}
		}
	})

	t.Run("AC4 reports unreadable and missing packs as not found", func(t *testing.T) {
		priv, _ := fakeserver.PackBundle("priv", "1.0.0", []fakeserver.PackFile{{Path: "SKILL.md", Body: []byte("secret")}})
		srv.SeedPack("acme/priv", "1.0.0", priv, "tok-other")

		for _, ref := range []string{"acme/priv", "acme/unknown"} {
			dir := filepath.Join(t.TempDir(), "out")
			r := s.run("pack", "fetch", ref, "--version", "1.0.0", "--dir", dir, "--server", srv.URL)
			if r.code == 0 {
				t.Fatalf("%s unexpectedly succeeded", ref)
			}
			if !strings.Contains(r.err, "not found") {
				t.Errorf("%s stderr %q missing 'not found'", ref, r.err)
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("%s created dir %s: %v", ref, dir, err)
			}
		}
	})
}
