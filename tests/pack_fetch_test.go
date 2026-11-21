//go:build integration

package tests

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

type itPackFile struct {
	path       string
	body       []byte
	executable bool
}

func itSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func itPackBundle(name, version string, files []itPackFile) (*satellepb.PackBundle, string) {
	type entry struct {
		path string
		sum  string
	}
	refs := make([]*satellepb.PackFileRef, len(files))
	pfiles := make([]*satellepb.PackFile, len(files))
	entries := make([]entry, len(files))
	for i, f := range files {
		sum := itSHA256(f.body)
		refs[i] = &satellepb.PackFileRef{
			Path:       f.path,
			Sha256:     sum,
			Size:       int64(len(f.body)),
			Executable: f.executable,
		}
		pfiles[i] = &satellepb.PackFile{
			Path:   f.path,
			Sha256: sum,
			Body:   slices.Clone(f.body),
		}
		entries[i] = entry{path: f.path, sum: sum}
	}
	slices.SortFunc(entries, func(a, b entry) int {
		return strings.Compare(a.path, b.path)
	})
	var buf strings.Builder
	for _, e := range entries {
		buf.WriteString(e.sum)
		buf.WriteString("  ")
		buf.WriteString(e.path)
		buf.WriteByte('\n')
	}
	digest := itSHA256([]byte(buf.String()))
	return &satellepb.PackBundle{
		Version: &satellepb.PackVersion{
			Manifest: &satellepb.PackManifest{
				Name:    name,
				Version: version,
				Files:   refs,
				Digest:  digest,
			},
		},
		Files: pfiles,
	}, digest
}

func TestPackFetchEndToEnd(t *testing.T) {
	t.Parallel()
	srv := fakeserver.Start(t, devToken, "tok-other")
	s := newSandbox(t)
	s.env["SATELLE_TOKEN"] = devToken

	files := []itPackFile{
		{path: "SKILL.md", body: []byte("# Pack skill\n")},
		{path: "scripts/run.sh", body: []byte("#!/bin/sh\necho ok\n"), executable: true},
		{path: "sub/readme.txt", body: []byte("details\n")},
	}
	valid, digest := itPackBundle("review", "1.0.0", files)
	srv.SeedPack("acme/review", "1.0.0", valid)

	t.Run("AC1 writes verified pack", func(t *testing.T) {
		newDir := filepath.Join(t.TempDir(), "pack-out")
		out := s.must("pack", "fetch", "acme/review", "--version", "1.0.0", "--dir", newDir, "--server", srv.URL)
		wantLine := "fetched acme/review@1.0.0 (3 file(s), digest " + digest[:12] + ") into " + newDir
		if strings.TrimSpace(out) != wantLine {
			t.Fatalf("stdout = %q, want %q", out, wantLine)
		}
		for _, f := range files {
			p := filepath.Join(newDir, filepath.FromSlash(f.path))
			got, err := os.ReadFile(p)
			if err != nil {
				t.Fatalf("read %s: %v", f.path, err)
			}
			if string(got) != string(f.body) {
				t.Errorf("%s = %q, want %q", f.path, got, f.body)
			}
			if runtime.GOOS != "windows" {
				st, err := os.Stat(p)
				if err != nil {
					t.Fatal(err)
				}
				if (st.Mode()&0o111 != 0) != f.executable {
					t.Errorf("%s mode %o executable=%v, want %v", f.path, st.Mode().Perm(), st.Mode()&0o111 != 0, f.executable)
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
		tampered, _ := itPackBundle("tampered", "1.0.0", files)
		tampered.Files[0].Body = []byte("# Tampered\n")
		srv.SeedPack("acme/tampered", "1.0.0", tampered)

		badDigest, _ := itPackBundle("baddigest", "1.0.0", files)
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
			files   []itPackFile
		}{
			{"acme/abs", "/abs/SKILL.md", []itPackFile{{path: "/abs/SKILL.md", body: []byte("x")}}},
			{"acme/dotdot", "../escape/SKILL.md", []itPackFile{{path: "../escape/SKILL.md", body: []byte("x")}}},
			{"acme/nested-dotdot", "sub/../../escape", []itPackFile{{path: "sub/../../escape", body: []byte("x")}}},
			{"acme/dup", "dup/SKILL.md", []itPackFile{
				{path: "dup/SKILL.md", body: []byte("one")},
				{path: "dup/SKILL.md", body: []byte("two")},
			}},
		} {
			b, _ := itPackBundle("bad", "1.0.0", tc.files)
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
		priv, _ := itPackBundle("priv", "1.0.0", []itPackFile{{path: "SKILL.md", body: []byte("secret")}})
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
