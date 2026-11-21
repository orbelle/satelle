package pack_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/pack"
	"github.com/bobmcallan/satelle/internal/wire/fakeserver"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestComputeDigest(t *testing.T) {
	const emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got := pack.ComputeDigest(nil); got != emptySHA256 {
		t.Fatalf("ComputeDigest(nil) = %q, want %q", got, emptySHA256)
	}
	if got := pack.ComputeDigest([]pack.DigestEntry{}); got != emptySHA256 {
		t.Fatalf("ComputeDigest(empty) = %q, want %q", got, emptySHA256)
	}

	entries := []pack.DigestEntry{
		{Path: "a/file", SHA256: "4444444444444444444444444444444444444444444444444444444444444444"},
		{Path: "a/b", SHA256: "3333333333333333333333333333333333333333333333333333333333333333"},
		{Path: "a-b", SHA256: "2222222222222222222222222222222222222222222222222222222222222222"},
		{Path: "B/file", SHA256: "1111111111111111111111111111111111111111111111111111111111111111"},
	}
	origOrder := []string{entries[0].Path, entries[1].Path, entries[2].Path, entries[3].Path}

	wantInput := "1111111111111111111111111111111111111111111111111111111111111111  B/file\n" +
		"2222222222222222222222222222222222222222222222222222222222222222  a-b\n" +
		"3333333333333333333333333333333333333333333333333333333333333333  a/b\n" +
		"4444444444444444444444444444444444444444444444444444444444444444  a/file\n"
	want := sha256Hex([]byte(wantInput))

	if got := pack.ComputeDigest(entries); got != want {
		t.Fatalf("ComputeDigest = %q, want %q", got, want)
	}
	gotOrder := []string{entries[0].Path, entries[1].Path, entries[2].Path, entries[3].Path}
	if !slices.Equal(gotOrder, origOrder) {
		t.Fatalf("ComputeDigest mutated caller slice: %v != %v", gotOrder, origOrder)
	}
}

func TestValidatePath(t *testing.T) {
	for _, good := range []string{"SKILL.md", "scripts/run.sh", "a/b/c.txt", ".hidden", "dir/file.txt"} {
		if err := pack.ValidatePath(good); err != nil {
			t.Errorf("ValidatePath(%q) unexpected error: %v", good, err)
		}
	}

	invalid := []string{
		"",
		"/etc/passwd",
		"../escape",
		"a/../b",
		"a/..",
		"./a",
		".",
		"..",
		`a\b`,
		"C:/a",
		"c:/a",
		"a//b",
		"a/",
		"a\nb",
		"a\rb",
		"a\x00b",
	}
	for _, bad := range invalid {
		err := pack.ValidatePath(bad)
		if err == nil {
			t.Errorf("ValidatePath(%q) succeeded, want error", bad)
			continue
		}
		if !strings.Contains(err.Error(), strconv.Quote(bad)) {
			t.Errorf("ValidatePath(%q) error %q does not name path", bad, err.Error())
		}
	}
}

func TestVerifyValid(t *testing.T) {
	files := []fakeserver.PackFile{
		{Path: "scripts/run.sh", Body: []byte("#!/bin/sh\necho ok\n"), Executable: true},
		{Path: "SKILL.md", Body: []byte("# Skill\n")},
		{Path: "a/b/data.bin", Body: []byte{0x00, 0x01, 0xff}},
	}
	bundle, wantDigest := fakeserver.PackBundle("review-pack", "1.2.3", files)

	v, err := pack.Verify(bundle)
	if err != nil {
		t.Fatalf("Verify valid bundle: %v", err)
	}
	if v.Name() != "review-pack" || v.Version() != "1.2.3" {
		t.Fatalf("Name=%q Version=%q", v.Name(), v.Version())
	}
	if v.Digest() != wantDigest {
		t.Fatalf("Digest = %q, want %q", v.Digest(), wantDigest)
	}
	wantPaths := []string{"SKILL.md", "a/b/data.bin", "scripts/run.sh"}
	if !slices.Equal(v.Paths(), wantPaths) {
		t.Fatalf("Paths = %v, want %v", v.Paths(), wantPaths)
	}

	bundle.Files[0].Sha256 = ""
	if _, err := pack.Verify(bundle); err != nil {
		t.Fatalf("Verify with omitted PackFile.Sha256: %v", err)
	}
}

func TestVerifyRejectsTamperedAndMismatched(t *testing.T) {
	base := []fakeserver.PackFile{
		{Path: "SKILL.md", Body: []byte("# Skill\n")},
		{Path: "scripts/run.sh", Body: []byte("#!/bin/sh\necho ok\n"), Executable: true},
	}

	t.Run("tampered body", func(t *testing.T) {
		b, _ := fakeserver.PackBundle("p", "1.0.0", base)
		b.Files[1].Body = []byte("#!/bin/sh\necho tampered\n")
		_, err := pack.Verify(b)
		if err == nil || !strings.Contains(err.Error(), "scripts/run.sh") || !strings.Contains(err.Error(), "sha256") {
			t.Fatalf("tampered body: got %v", err)
		}
	})

	t.Run("truncated body", func(t *testing.T) {
		b, _ := fakeserver.PackBundle("p", "1.0.0", base)
		b.Files[0].Body = []byte("# Sk")
		_, err := pack.Verify(b)
		if err == nil || !strings.Contains(err.Error(), "SKILL.md") || !strings.Contains(err.Error(), "sha256") {
			t.Fatalf("truncated body: got %v", err)
		}
	})

	t.Run("mismatched PackFile sha256", func(t *testing.T) {
		b, _ := fakeserver.PackBundle("p", "1.0.0", base)
		b.Files[0].Sha256 = strings.Repeat("0", 64)
		_, err := pack.Verify(b)
		if err == nil || !strings.Contains(err.Error(), "SKILL.md") || !strings.Contains(err.Error(), strings.Repeat("0", 64)) {
			t.Fatalf("mismatched PackFile sha256: got %v", err)
		}
	})

	t.Run("missing bundle file", func(t *testing.T) {
		b, _ := fakeserver.PackBundle("p", "1.0.0", base)
		b.Files = b.Files[:1]
		_, err := pack.Verify(b)
		if err == nil || !strings.Contains(err.Error(), "scripts/run.sh") {
			t.Fatalf("missing bundle file: got %v", err)
		}
	})

	t.Run("extra bundle file", func(t *testing.T) {
		b, _ := fakeserver.PackBundle("p", "1.0.0", base)
		extra := []byte("surprise")
		b.Files = append(b.Files, &satellepb.PackFile{Path: "extra.txt", Sha256: sha256Hex(extra), Body: extra})
		_, err := pack.Verify(b)
		if err == nil || !strings.Contains(err.Error(), "extra.txt") {
			t.Fatalf("extra bundle file: got %v", err)
		}
	})

	t.Run("wrong manifest digest", func(t *testing.T) {
		b, validDigest := fakeserver.PackBundle("p", "1.0.0", base)
		badDigest := strings.Repeat("f", 64)
		b.Version.Manifest.Digest = badDigest
		_, err := pack.Verify(b)
		if err == nil || !strings.Contains(err.Error(), "digest") || !strings.Contains(err.Error(), badDigest) || !strings.Contains(err.Error(), validDigest) {
			t.Fatalf("wrong manifest digest: got %v", err)
		}
	})

	t.Run("size mismatch", func(t *testing.T) {
		b, _ := fakeserver.PackBundle("p", "1.0.0", base)
		b.Version.Manifest.Files[0].Size = 999
		_, err := pack.Verify(b)
		if err == nil || !strings.Contains(err.Error(), "SKILL.md") || !strings.Contains(err.Error(), "size") {
			t.Fatalf("size mismatch: got %v", err)
		}
	})

	t.Run("missing manifest", func(t *testing.T) {
		for _, b := range []*satellepb.PackBundle{
			{},
			{Version: &satellepb.PackVersion{}},
			{Version: &satellepb.PackVersion{Manifest: &satellepb.PackManifest{}}},
		} {
			if _, err := pack.Verify(b); err == nil {
				t.Errorf("Verify(%+v) succeeded, want error", b)
			}
		}
	})
}

func TestVerifyRejectsBadPathsDuplicatesAndCollisions(t *testing.T) {
	t.Run("duplicate in manifest", func(t *testing.T) {
		b, _ := fakeserver.PackBundle("p", "1.0.0", []fakeserver.PackFile{
			{Path: "dup/SKILL.md", Body: []byte("one")},
			{Path: "dup/SKILL.md", Body: []byte("two")},
		})
		_, err := pack.Verify(b)
		if err == nil || !strings.Contains(err.Error(), "dup/SKILL.md") {
			t.Fatalf("duplicate in manifest: got %v", err)
		}
	})

	t.Run("duplicate in bundle", func(t *testing.T) {
		b, _ := fakeserver.PackBundle("p", "1.0.0", []fakeserver.PackFile{{Path: "dup/SKILL.md", Body: []byte("one")}})
		b.Files = append(b.Files, &satellepb.PackFile{Path: "dup/SKILL.md", Sha256: b.Files[0].Sha256, Body: []byte("one")})
		_, err := pack.Verify(b)
		if err == nil || !strings.Contains(err.Error(), "dup/SKILL.md") {
			t.Fatalf("duplicate in bundle: got %v", err)
		}
	})

	t.Run("file and dir collision", func(t *testing.T) {
		b, _ := fakeserver.PackBundle("p", "1.0.0", []fakeserver.PackFile{
			{Path: "a", Body: []byte("file")},
			{Path: "a-x", Body: []byte("between")},
			{Path: "a/b", Body: []byte("nested")},
		})
		_, err := pack.Verify(b)
		if err == nil || !strings.Contains(err.Error(), `"a"`) || !strings.Contains(err.Error(), `"a/b"`) {
			t.Fatalf("collision: got %v", err)
		}
	})

	t.Run("bad path in manifest", func(t *testing.T) {
		b, _ := fakeserver.PackBundle("p", "1.0.0", []fakeserver.PackFile{{Path: "/etc/passwd", Body: []byte("x")}})
		_, err := pack.Verify(b)
		if err == nil || !strings.Contains(err.Error(), "/etc/passwd") {
			t.Fatalf("bad path in manifest: got %v", err)
		}
	})

	t.Run("bad path in bundle", func(t *testing.T) {
		b, _ := fakeserver.PackBundle("p", "1.0.0", []fakeserver.PackFile{{Path: "SKILL.md", Body: []byte("x")}})
		b.Files = append(b.Files, &satellepb.PackFile{Path: "../escape", Body: []byte("x")})
		_, err := pack.Verify(b)
		if err == nil || !strings.Contains(err.Error(), "../escape") {
			t.Fatalf("bad path in bundle: got %v", err)
		}
	})
}

func TestWrite(t *testing.T) {
	files := []fakeserver.PackFile{
		{Path: "SKILL.md", Body: []byte("# Skill\n")},
		{Path: "scripts/run.sh", Body: []byte("#!/bin/sh\necho hi\n"), Executable: true},
		{Path: "nested/sub/file.txt", Body: []byte("payload")},
	}
	bundle, _ := fakeserver.PackBundle("demo", "1.0.0", files)
	v, err := pack.Verify(bundle)
	if err != nil {
		t.Fatal(err)
	}

	checkWritten := func(t *testing.T, dir string) {
		t.Helper()
		for _, f := range files {
			p := filepath.Join(dir, filepath.FromSlash(f.Path))
			got, err := os.ReadFile(p)
			if err != nil {
				t.Fatalf("read %s: %v", f.Path, err)
			}
			if !bytes.Equal(got, f.Body) {
				t.Errorf("%s body = %q, want %q", f.Path, got, f.Body)
			}
			if runtime.GOOS != "windows" {
				st, err := os.Stat(p)
				if err != nil {
					t.Fatalf("stat %s: %v", f.Path, err)
				}
				exec := st.Mode()&0o111 != 0
				if exec != f.Executable {
					t.Errorf("%s executable=%v (mode %o), want %v", f.Path, exec, st.Mode().Perm(), f.Executable)
				}
			}
		}
		parentEntries, err := os.ReadDir(filepath.Dir(dir))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range parentEntries {
			if strings.HasPrefix(e.Name(), ".satelle-pack-") {
				t.Errorf("leftover staging entry %q in parent", e.Name())
			}
		}
	}

	t.Run("creates new directory", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "nested", "target")
		if err := pack.Write(dir, v); err != nil {
			t.Fatalf("Write new dir: %v", err)
		}
		checkWritten(t, dir)
	})

	t.Run("writes into existing empty directory", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "existing")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := pack.Write(dir, v); err != nil {
			t.Fatalf("Write existing empty dir: %v", err)
		}
		checkWritten(t, dir)
	})

	t.Run("rejects non-empty directory", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "nonempty")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		existing := filepath.Join(dir, "keep.txt")
		if err := os.WriteFile(existing, []byte("keep"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := pack.Write(dir, v); err == nil || !strings.Contains(err.Error(), "not empty") {
			t.Fatalf("non-empty dir: got %v", err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 1 || entries[0].Name() != "keep.txt" {
			t.Fatalf("non-empty dir was modified: %v (%v)", entries, err)
		}
	})

	t.Run("rejects regular file", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := pack.Write(target, v); err == nil || !strings.Contains(err.Error(), "not a directory") {
			t.Fatalf("regular file target: got %v", err)
		}
	})

	t.Run("rejects symlink", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks require elevated privileges on Windows")
		}
		root := t.TempDir()
		real := filepath.Join(root, "real")
		if err := os.MkdirAll(real, 0o755); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "link")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		if err := pack.Write(link, v); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("symlink target: got %v", err)
		}
	})
}
