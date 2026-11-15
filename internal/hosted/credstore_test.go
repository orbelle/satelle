package hosted

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func tempStore(t *testing.T) FileStore {
	t.Helper()
	return FileStore{Path: filepath.Join(t.TempDir(), "credentials.toml")}
}

func TestCredentialsPathXDG(t *testing.T) {
	x := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", x)
	p, err := CredentialsPath()
	if err != nil {
		t.Fatal(err)
	}
	if p != filepath.Join(x, "satelle", "credentials.toml") {
		t.Fatalf("path = %q", p)
	}
}

func panicMessage(f func()) (msg string) {
	defer func() {
		if r := recover(); r != nil {
			msg = fmt.Sprint(r)
		}
	}()
	f()
	return ""
}

func TestCredentialsPathFenceUnderTest(t *testing.T) {
	t.Run("xdg outside temp panics", func(t *testing.T) {
		const bad = "/nonexistent-satelle-xdg"
		t.Setenv("SATELLE_HOME", t.TempDir())
		t.Setenv("XDG_CONFIG_HOME", bad)
		msg := panicMessage(func() { _, _ = CredentialsPath() })
		if !strings.Contains(msg, filepath.Join(bad, "satelle", "credentials.toml")) {
			t.Fatalf("want panic naming the resolved path, got %q", msg)
		}
	})
	t.Run("home fallback outside temp panics", func(t *testing.T) {
		const bad = "/nonexistent-satelle-home"
		t.Setenv("SATELLE_HOME", t.TempDir())
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", bad)
		msg := panicMessage(func() { _, _ = CredentialsPath() })
		if !strings.Contains(msg, filepath.Join(bad, ".config", "satelle", "credentials.toml")) {
			t.Fatalf("want panic naming the resolved path, got %q", msg)
		}
	})
	t.Run("xdg under temp dir is allowed", func(t *testing.T) {
		x := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", x)
		var p string
		if msg := panicMessage(func() { p, _ = CredentialsPath() }); msg != "" {
			t.Fatalf("unexpected panic: %s", msg)
		}
		if p != filepath.Join(x, "satelle", "credentials.toml") {
			t.Fatalf("path = %q", p)
		}
	})
	t.Run("xdg under SATELLE_HOME is allowed", func(t *testing.T) {
		t.Setenv("TMPDIR", t.TempDir())
		h := t.TempDir()
		t.Setenv("SATELLE_HOME", h)
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, "xdg"))
		if msg := panicMessage(func() { _, _ = CredentialsPath() }); msg != "" {
			t.Fatalf("unexpected panic: %s", msg)
		}
	})
	t.Run("explicit FileStore path bypasses the fence", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "/nonexistent-satelle-xdg")
		s := FileStore{Path: filepath.Join(t.TempDir(), "c.toml")}
		if _, err := s.Load("https://h"); !errors.Is(err, ErrNoCredential) {
			t.Fatalf("Load = %v", err)
		}
	})
}

func TestPruneLoopback(t *testing.T) {
	s := tempStore(t)
	stampless := []string{"http://127.0.0.1:41001", "http://localhost:41002", "http://[::1]:41003"}
	keep := []Credential{
		{ServerURL: "http://127.0.0.1:8787", AccessToken: "a", RefreshToken: "r", CreatedAt: "2026-10-01T00:00:00Z", Email: "dev@x.io", DisplayName: "Dev"},
		{ServerURL: "https://hosted.example", AccessToken: "a2", RefreshToken: "r2"},
		{ServerURL: "https://other.example", AccessToken: "a3", RefreshToken: "r3", CreatedAt: "2026-10-02T00:00:00Z", Email: "o@x.io"},
	}
	for _, u := range stampless {
		if err := s.Save(Credential{ServerURL: u, AccessToken: "t", RefreshToken: "t"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range keep {
		if err := s.Save(c); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := s.PruneLoopback()
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 3 {
		t.Fatalf("removed = %v, want the 3 stamp-less loopback URLs", removed)
	}
	for i, u := range stampless {
		if removed[i] != u {
			t.Errorf("removed[%d] = %q, want %q", i, removed[i], u)
		}
		if _, err := s.Load(u); !errors.Is(err, ErrNoCredential) {
			t.Errorf("%s survived the prune: %v", u, err)
		}
	}
	for _, want := range keep {
		got, err := s.Load(want.ServerURL)
		if err != nil || got != want {
			t.Errorf("%s changed: got %+v err %v, want %+v", want.ServerURL, got, err, want)
		}
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(s.Path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
		}
	}
	if again, err := s.PruneLoopback(); err != nil || len(again) != 0 {
		t.Fatalf("second prune = %v, %v", again, err)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	s := tempStore(t)
	in := Credential{ServerURL: "https://h/", AccessToken: "a", RefreshToken: "r", Scope: "x"}
	if err := s.Save(in); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load("https://h")
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "a" || got.RefreshToken != "r" {
		t.Fatalf("got %+v", got)
	}
	if got.ServerURL != "https://h" {
		t.Errorf("server_url not normalized on save: %q", got.ServerURL)
	}
}

func TestSaveLoadIdentityFields(t *testing.T) {
	s := tempStore(t)
	in := Credential{ServerURL: "https://h", AccessToken: "a", RefreshToken: "r",
		DisplayName: "Dev User", Email: "dev@x.io"}
	if err := s.Save(in); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load("https://h")
	if err != nil {
		t.Fatal(err)
	}
	if got.DisplayName != "Dev User" || got.Email != "dev@x.io" {
		t.Fatalf("identity fields not round-tripped: %+v", got)
	}
}

func TestSaveLoadPrincipalID(t *testing.T) {
	s := tempStore(t)
	in := Credential{ServerURL: "https://h", AccessToken: "a", RefreshToken: "r",
		PrincipalID: "prin_p"}
	if err := s.Save(in); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load("https://h")
	if err != nil {
		t.Fatal(err)
	}
	if got.PrincipalID != "prin_p" {
		t.Fatalf("PrincipalID = %q", got.PrincipalID)
	}
}

func TestLoadMissingPrincipalIDIsEmpty(t *testing.T) {
	s := tempStore(t)
	if err := os.WriteFile(s.Path, []byte(`
[[credential]]
server_url = "https://h"
access_token = "a"
refresh_token = "r"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load("https://h")
	if err != nil {
		t.Fatal(err)
	}
	if got.PrincipalID != "" {
		t.Fatalf("missing principal_id loaded as %q", got.PrincipalID)
	}
}

func TestSaveUpsertByServer(t *testing.T) {
	s := tempStore(t)
	_ = s.Save(Credential{ServerURL: "https://a", AccessToken: "1", RefreshToken: "1"})
	_ = s.Save(Credential{ServerURL: "https://b", AccessToken: "2", RefreshToken: "2"})
	_ = s.Save(Credential{ServerURL: "https://a", AccessToken: "1b", RefreshToken: "1b"})

	cf, _, err := s.read()
	if err != nil {
		t.Fatal(err)
	}
	if len(cf.Credential) != 2 {
		t.Fatalf("expected 2 credentials after upsert, got %d", len(cf.Credential))
	}
	a, _ := s.Load("https://a")
	if a.AccessToken != "1b" {
		t.Fatalf("upsert did not replace: %+v", a)
	}
}

func TestSaveRejectsIncomplete(t *testing.T) {
	s := tempStore(t)
	if err := s.Save(Credential{ServerURL: "https://h", AccessToken: "a"}); err == nil {
		t.Fatal("expected error for missing refresh token")
	}
	if err := s.Save(Credential{AccessToken: "a", RefreshToken: "r"}); err == nil {
		t.Fatal("expected error for missing server_url")
	}
}

func TestLoadMissingIsErrNoCredential(t *testing.T) {
	s := tempStore(t)
	if _, err := s.Load("https://none"); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("expected ErrNoCredential, got %v", err)
	}
}

func TestDeleteRemovesEntry(t *testing.T) {
	s := tempStore(t)
	_ = s.Save(Credential{ServerURL: "https://a", AccessToken: "1", RefreshToken: "1"})
	_ = s.Save(Credential{ServerURL: "https://b", AccessToken: "2", RefreshToken: "2"})
	if err := s.Delete("https://a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load("https://a"); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("a should be gone, got %v", err)
	}
	if _, err := s.Load("https://b"); err != nil {
		t.Fatalf("b should remain: %v", err)
	}
	if err := s.Delete("https://none"); err != nil {
		t.Fatalf("delete absent: %v", err)
	}
}

func TestSaveFilePerms0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix perms")
	}
	s := tempStore(t)
	_ = s.Save(Credential{ServerURL: "https://h", AccessToken: "a", RefreshToken: "r"})
	fi, err := os.Stat(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("perms = %o, want 600", fi.Mode().Perm())
	}
}
