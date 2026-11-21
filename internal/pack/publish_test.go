package pack

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func writeFile(t *testing.T, p, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

func skillDir(t *testing.T, skills, name string) {
	t.Helper()
	writeFile(t, filepath.Join(skills, name, "SKILL.md"), "---\nname: "+name+"\ntype: skill\ndescription: "+name+"\n---\n", 0o644)
}

func packFixture(t *testing.T, members string) (string, string) {
	t.Helper()
	root := t.TempDir()
	skills := filepath.Join(root, "skills")
	skillDir(t, skills, "rev")
	writeFile(t, filepath.Join(skills, "rev", "pack.toml"), "name = \"review\"\nversion = \"1.2.3\"\nskills = ["+members+"]\n", 0o644)
	writeFile(t, filepath.Join(skills, "rev", "scripts", "run.sh"), "#!/bin/sh\necho ok\n", 0o755)
	skillDir(t, skills, "rev-check")
	skillDir(t, skills, "other")
	writeFile(t, filepath.Join(root, "outside", "secret"), "secret\n", 0o644)
	return root, skills
}

func bundlePaths(t *testing.T, skills, entry string) []string {
	t.Helper()
	b, err := Collect(skills, entry)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	var out []string
	for _, f := range b.GetVersion().GetManifest().GetFiles() {
		out = append(out, f.GetPath())
	}
	slices.Sort(out)
	return out
}

func TestCollect(t *testing.T) {
	_, skills := packFixture(t, `"rev-check"`)
	b, err := Collect(skills, "rev")
	if err != nil {
		t.Fatal(err)
	}
	m := b.GetVersion().GetManifest()
	if m.GetName() != "review" || m.GetVersion() != "1.2.3" {
		t.Fatalf("manifest = %s@%s", m.GetName(), m.GetVersion())
	}
	want := []string{"rev-check/SKILL.md", "rev/SKILL.md", "rev/pack.toml", "rev/scripts/run.sh"}
	var got []string
	var entries []DigestEntry
	for i, f := range m.GetFiles() {
		got = append(got, f.GetPath())
		body, err := os.ReadFile(filepath.Join(skills, filepath.FromSlash(f.GetPath())))
		if err != nil {
			t.Fatal(err)
		}
		raw := sha256.Sum256(body)
		sum := hex.EncodeToString(raw[:])
		if f.GetSha256() != sum || f.GetSize() != int64(len(body)) {
			t.Errorf("%s sha256/size = %s/%d, want %s/%d", f.GetPath(), f.GetSha256(), f.GetSize(), sum, len(body))
		}
		pf := b.GetFiles()[i]
		if pf.GetPath() != f.GetPath() || string(pf.GetBody()) != string(body) || pf.GetSha256() != sum {
			t.Errorf("file %d = %s, want body of %s", i, pf.GetPath(), f.GetPath())
		}
		if runtime.GOOS != "windows" && f.GetExecutable() != (f.GetPath() == "rev/scripts/run.sh") {
			t.Errorf("%s executable = %v", f.GetPath(), f.GetExecutable())
		}
		entries = append(entries, DigestEntry{Path: f.GetPath(), SHA256: sum})
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("paths = %v, want %v", got, want)
	}
	if m.GetDigest() != ComputeDigest(entries) {
		t.Errorf("digest = %s, want %s", m.GetDigest(), ComputeDigest(entries))
	}
	if _, err := Verify(b); err != nil {
		t.Errorf("Verify: %v", err)
	}
}

func TestCollectSingleSkillPack(t *testing.T) {
	_, skills := packFixture(t, `"rev"`)
	if got, want := bundlePaths(t, skills, "rev"), []string{"rev/SKILL.md", "rev/pack.toml", "rev/scripts/run.sh"}; !slices.Equal(got, want) {
		t.Errorf("single-skill pack = %v, want %v", got, want)
	}
	_, skills = packFixture(t, `"rev", "rev-check"`)
	if got, want := bundlePaths(t, skills, "rev"), []string{"rev-check/SKILL.md", "rev/SKILL.md", "rev/pack.toml", "rev/scripts/run.sh"}; !slices.Equal(got, want) {
		t.Errorf("entry listed among members = %v, want %v", got, want)
	}
}

func TestCollectRefusesUnsafeInput(t *testing.T) {
	symlink := func(t *testing.T, target, link string) {
		t.Helper()
		if err := os.RemoveAll(link); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlink: %v", err)
		}
	}
	setMembers := func(skills, members string) func(t *testing.T) {
		return func(t *testing.T) {
			writeFile(t, filepath.Join(skills, "rev", "pack.toml"), "name = \"review\"\nversion = \"1.2.3\"\nskills = ["+members+"]\n", 0o644)
		}
	}
	cases := []struct {
		name  string
		entry string
		setup func(t *testing.T, root, skills string)
		dir   func(root, skills string) string
		want  string
	}{
		{name: "entry dot-dot", entry: "..", want: `".."`},
		{name: "entry escapes", entry: "../outside", want: "../outside"},
		{name: "entry nested", entry: "a/b", want: "a/b"},
		{name: "entry absolute", entry: "/tmp/x", want: "/tmp/x"},
		{name: "entry empty", entry: "", want: "empty"},
		{name: "member dot-dot", entry: "rev", setup: func(t *testing.T, _, s string) { setMembers(s, `".."`)(t) }, want: ".."},
		{name: "member escapes", entry: "rev", setup: func(t *testing.T, _, s string) { setMembers(s, `"../../outside"`)(t) }, want: "../../outside"},
		{name: "member missing", entry: "rev", setup: func(t *testing.T, _, s string) { setMembers(s, `"nope"`)(t) }, want: "nope"},
		{name: "member single-file", entry: "rev", setup: func(t *testing.T, _, s string) {
			writeFile(t, filepath.Join(s, "flat.md"), "---\nname: flat\n---\n", 0o644)
			setMembers(s, `"flat"`)(t)
		}, want: "flat"},
		{name: "member twice", entry: "rev", setup: func(t *testing.T, _, s string) { setMembers(s, `"rev-check", "rev-check"`)(t) }, want: "rev-check"},
		{name: "skills dir symlink", entry: "rev", setup: func(t *testing.T, root, s string) {
			if err := os.Rename(s, filepath.Join(root, "real")); err != nil {
				t.Fatal(err)
			}
			symlink(t, filepath.Join(root, "real"), s)
		}, want: "symlink"},
		{name: "entry symlink", entry: "evil", setup: func(t *testing.T, root, s string) {
			symlink(t, filepath.Join(root, "outside"), filepath.Join(s, "evil"))
		}, want: "symlink"},
		{name: "pack.toml symlink", entry: "rev", setup: func(t *testing.T, root, s string) {
			symlink(t, filepath.Join(root, "outside", "secret"), filepath.Join(s, "rev", "pack.toml"))
		}, want: "symlink"},
		{name: "entry without SKILL.md", entry: "rev", setup: func(t *testing.T, _, s string) {
			if err := os.Remove(filepath.Join(s, "rev", "SKILL.md")); err != nil {
				t.Fatal(err)
			}
		}, want: "no SKILL.md"},
		{name: "member symlink", entry: "rev", setup: func(t *testing.T, root, s string) {
			writeFile(t, filepath.Join(root, "outside", "SKILL.md"), "---\nname: x\n---\n", 0o644)
			symlink(t, filepath.Join(root, "outside"), filepath.Join(s, "rev-check"))
		}, want: "rev-check"},
		{name: "symlinked file in member", entry: "rev", setup: func(t *testing.T, root, s string) {
			symlink(t, filepath.Join(root, "outside", "secret"), filepath.Join(s, "rev-check", "leak"))
		}, want: "leak: symlink"},
		{name: "symlinked dir in entry", entry: "rev", setup: func(t *testing.T, root, s string) {
			symlink(t, filepath.Join(root, "outside"), filepath.Join(s, "rev", "sub"))
		}, want: "sub: symlink"},
		{name: "fifo in entry", entry: "rev", setup: func(t *testing.T, _, s string) {
			if runtime.GOOS == "windows" {
				t.Skip("no FIFOs")
			}
			if err := exec.Command("mkfifo", filepath.Join(s, "rev", "pipe")).Run(); err != nil {
				t.Skipf("mkfifo: %v", err)
			}
		}, want: "pipe: is not a regular file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, skills := packFixture(t, `"rev-check"`)
			if tc.setup != nil {
				tc.setup(t, root, skills)
			}
			b, err := Collect(skills, tc.entry)
			if err == nil {
				t.Fatalf("Collect succeeded with %d file(s)", len(b.GetFiles()))
			}
			if b != nil {
				t.Errorf("bundle returned with error %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name %q", err, tc.want)
			}
		})
	}
}
