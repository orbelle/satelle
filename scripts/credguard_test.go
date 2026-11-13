package scripts_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/hosted"
)

// runGuard runs credguard.sh over a shell command with XDG_CONFIG_HOME pointing
// at xdg and returns its exit code and combined output.
func runGuard(t *testing.T, xdg, shellCmd string) (int, string) {
	t.Helper()
	return runGuardEnv(t, []string{"XDG_CONFIG_HOME=" + xdg}, shellCmd)
}

// runGuardEnv runs credguard.sh over a shell command with env appended to the
// test process's environment (later entries win).
func runGuardEnv(t *testing.T, env []string, shellCmd string) (int, string) {
	t.Helper()
	cmd := exec.Command("sh", "credguard.sh", "--", "sh", "-c", shellCmd)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, string(out)
	case errors.As(err, &ee):
		return ee.ExitCode(), string(out)
	default:
		t.Fatalf("run credguard: %v", err)
		return -1, ""
	}
}

func testCred(url string) hosted.Credential {
	return hosted.Credential{
		ServerURL:    url,
		AccessToken:  "access-1",
		RefreshToken: "refresh-1",
		TokenType:    "Bearer",
		Scope:        "mcp",
		DisplayName:  "Ada",
		Email:        "ada@example.com",
		PrincipalID:  "usr_1",
		ExpiresAt:    "2026-10-06T01:00:00Z",
		CreatedAt:    "2026-10-06T00:00:00Z",
	}
}

// credFile writes a credentials file holding creds at path (creating its
// directory) through the real store, so the fixture is the production shape.
func credFile(t *testing.T, path string, creds ...hosted.Credential) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	store := hosted.FileStore{Path: path}
	for _, c := range creds {
		if err := store.Save(c); err != nil {
			t.Fatal(err)
		}
	}
}

// TestCredguard proves the Makefile guard (sty_18403814, sty_5ba68e2c): it
// fails a run only for a credentials change a test could make — a credential
// added or removed, an identity changed, the file created, deleted or left
// unparseable — and passes a token rewrite that keeps every identity, the way an
// outside satelle process refreshes a hosted session. The wrapped command's own
// failure status is kept.
func TestCredguard(t *testing.T) {
	const keep = "https://keep.example.com"
	const other = "https://other.example.com"
	cred := func(xdg string) string { return filepath.Join(xdg, "satelle", "credentials.toml") }
	seed := func(t *testing.T, creds ...hosted.Credential) string {
		t.Helper()
		xdg := t.TempDir()
		if len(creds) == 0 {
			creds = []hosted.Credential{testCred(keep)}
		}
		credFile(t, cred(xdg), creds...)
		return xdg
	}
	// replaceWith returns a shell command that overwrites the host file with a
	// fixture holding creds — the test's stand-in for a write by the wrapped run.
	replaceWith := func(t *testing.T, creds ...hosted.Credential) string {
		t.Helper()
		fixture := filepath.Join(t.TempDir(), "fixture.toml")
		credFile(t, fixture, creds...)
		return `mkdir -p "$XDG_CONFIG_HOME/satelle" && cp ` + fixture + ` "$XDG_CONFIG_HOME/satelle/credentials.toml"`
	}
	const changed = "host credentials file changed by tests"
	wantFail := func(t *testing.T, xdg, cmd, reason string) {
		t.Helper()
		code, out := runGuard(t, xdg, cmd)
		if code != 1 || !strings.Contains(out, changed) || !strings.Contains(out, reason) {
			t.Fatalf("exit %d, want 1 with %q and %q; output %q", code, changed, reason, out)
		}
	}

	t.Run("unchanged file passes", func(t *testing.T) {
		if code, out := runGuard(t, seed(t), "true"); code != 0 {
			t.Fatalf("exit %d: %s", code, out)
		}
	})
	t.Run("absent and still absent passes", func(t *testing.T) {
		if code, out := runGuard(t, t.TempDir(), "true"); code != 0 {
			t.Fatalf("exit %d: %s", code, out)
		}
	})
	t.Run("token refresh passes", func(t *testing.T) {
		xdg := seed(t, testCred(keep), testCred(other))
		rotated := testCred(keep)
		rotated.AccessToken, rotated.RefreshToken = "access-2", "refresh-2"
		rotated.ExpiresAt, rotated.CreatedAt = "2026-10-06T02:00:00Z", "2026-10-06T01:00:00Z"
		cmd := replaceWith(t, rotated, testCred(other))
		before, _ := os.ReadFile(cred(xdg))
		if code, out := runGuard(t, xdg, cmd); code != 0 {
			t.Fatalf("token refresh must pass, exit %d: %s", code, out)
		}
		after, _ := os.ReadFile(cred(xdg))
		if string(before) == string(after) {
			t.Fatal("test setup: the refresh must change the file bytes")
		}
	})
	t.Run("server added fails", func(t *testing.T) {
		xdg := seed(t)
		wantFail(t, xdg, replaceWith(t, testCred(keep), testCred(other)), "server added "+other)
	})
	t.Run("server removed fails", func(t *testing.T) {
		xdg := seed(t, testCred(keep), testCred(other))
		wantFail(t, xdg, replaceWith(t, testCred(keep)), "server removed "+other)
	})
	t.Run("identity changed fails", func(t *testing.T) {
		xdg := seed(t)
		c := testCred(keep)
		c.Email = "grace@example.com"
		wantFail(t, xdg, replaceWith(t, c), "identity changed for "+keep)
	})
	t.Run("file deleted fails", func(t *testing.T) {
		xdg := seed(t)
		wantFail(t, xdg, `rm "$XDG_CONFIG_HOME/satelle/credentials.toml"`, "file removed")
	})
	t.Run("file left unparseable fails", func(t *testing.T) {
		xdg := seed(t)
		wantFail(t, xdg, `echo x >> "$XDG_CONFIG_HOME/satelle/credentials.toml"`, "unparseable")
	})
	t.Run("created from absent fails", func(t *testing.T) {
		wantFail(t, t.TempDir(), replaceWith(t, testCred(keep)), "file appeared")
	})
	t.Run("suite failure keeps its own exit code", func(t *testing.T) {
		if code, _ := runGuard(t, seed(t), "exit 3"); code != 3 {
			t.Fatalf("exit %d, want 3", code)
		}
	})
	t.Run("suite failure plus change keeps the suite code", func(t *testing.T) {
		xdg := seed(t)
		code, out := runGuard(t, xdg, `rm "$XDG_CONFIG_HOME/satelle/credentials.toml"; exit 3`)
		if code != 3 || !strings.Contains(out, changed) {
			t.Fatalf("exit %d, output %q", code, out)
		}
	})
	t.Run("fails closed", credguardFailsClosed)
}

// fakeHost builds a temporary HOME holding the layout the host guard watches
// (sty_1b739a74) and returns the env that points the guard at it. The Go
// caches are pinned to the real ones so the helper still builds offline.
//
// Go telemetry is switched off in the fake HOME (sty_095d1f5b): with the default
// "local" mode the go command that credguard.sh runs writes counter files under
// $XDG_CONFIG_HOME/go/telemetry and starts a detached child that creates
// telemetry/upload after the guard has exited, racing t.TempDir's RemoveAll
// ("directory not empty"). The mode file is the go command's own switch and
// leaves the pinned GOCACHE/GOMODCACHE and the watched host surface untouched.
func fakeHost(t *testing.T) (home string, env []string) {
	t.Helper()
	home = t.TempDir()
	for rel, body := range map[string]string{
		".satelle/config.toml":              "a = 1\n",
		".satelle/document-sync-state.json": `{"n":1}`,
		".satelle/repo-0123abcd/db-wal":     "w1",
		".satelle/serve/mirror.db-wal":      "w1",
		".local/bin/satelle":                "v1",
		".config/go/telemetry/mode":         "off",
	} {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	goenv := func(name string) string {
		out, err := exec.Command("go", "env", name).Output()
		if err != nil {
			t.Fatalf("go env %s: %v", name, err)
		}
		return strings.TrimSpace(string(out))
	}
	t.Cleanup(func() {
		// Runs before t.TempDir's RemoveAll: anything beside the seeded mode
		// file means the go command wrote telemetry into the fake HOME.
		dir := filepath.Join(home, ".config", "go", "telemetry")
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Errorf("read %s: %v", dir, err)
			return
		}
		for _, e := range entries {
			if e.Name() != "mode" {
				t.Errorf("go telemetry wrote %s into the fake HOME", filepath.Join(dir, e.Name()))
			}
		}
	})
	return home, []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"GOCACHE=" + goenv("GOCACHE"),
		"GOMODCACHE=" + goenv("GOMODCACHE"),
	}
}

// credguardFailsClosed proves, as the "fails closed" group of TestCredguard,
// that the guard exits 2 with a "failing closed" message when it cannot build
// its helper or cannot take the host snapshot, rather than letting the wrapped
// command pass unguarded.
func credguardFailsClosed(t *testing.T) {
	failsClosed := func(t *testing.T, env []string, msg string) {
		t.Helper()
		code, out := runGuardEnv(t, env, "true")
		if code != 2 || !strings.Contains(out, "failing closed") || !strings.Contains(out, msg) {
			t.Fatalf("exit %d, want 2 with %q and %q; output %q", code, "failing closed", msg, out)
		}
	}
	t.Run("helper cannot be built: no Go toolchain", func(t *testing.T) {
		_, env := fakeHost(t)
		bin := t.TempDir()
		for _, tool := range []string{"sh", "mktemp", "rm", "dirname", "sed"} {
			p, err := exec.LookPath(tool)
			if err != nil {
				t.Skipf("%s not on PATH: %v", tool, err)
			}
			if err := os.Symlink(p, filepath.Join(bin, tool)); err != nil {
				t.Fatal(err)
			}
		}
		failsClosed(t, append(env, "PATH="+bin), "cannot build scripts/credfp")
	})
	t.Run("helper cannot be built: broken build", func(t *testing.T) {
		_, env := fakeHost(t)
		failsClosed(t, append(env, "GOFLAGS=-mod=nonexistent"), "cannot build scripts/credfp")
	})
}
