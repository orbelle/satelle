package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/buildinfo"
)

// releaseServer serves a GitHub-shaped release API and download tree:
// assets maps "<tag>/<asset>" to its body; a ".sha256" beside each is
// derived unless badSum names an asset whose checksum is wrong.
func releaseServer(t *testing.T, latest string, list []string, assets map[string]string, badSum string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/latest", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"tag_name":%q}`, latest)
	})
	mux.HandleFunc("/list", func(w http.ResponseWriter, _ *http.Request) {
		var parts []string
		for _, tag := range list {
			parts = append(parts, fmt.Sprintf(`{"tag_name":%q}`, tag))
		}
		fmt.Fprintf(w, "[%s]", strings.Join(parts, ","))
	})
	mux.HandleFunc("/dl/", func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/dl/")
		if base, ok := strings.CutSuffix(key, ".sha256"); ok {
			body, found := assets[base]
			if !found {
				http.NotFound(w, r)
				return
			}
			sum := sha256.Sum256([]byte(body))
			if base == badSum {
				sum = sha256.Sum256([]byte("tampered"))
			}
			fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), filepath.Base(base))
			return
		}
		body, found := assets[key]
		if !found {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv(envReleaseAPI, srv.URL+"/latest")
	t.Setenv(envReleaseListAPI, srv.URL+"/list")
	t.Setenv(envReleaseBase, srv.URL+"/dl")
}

// fakeInstall points update at a satelle (and optionally a satelled) in a
// temp dir and returns their paths.
func fakeInstall(t *testing.T, withDaemon bool) (cli, daemon string) {
	t.Helper()
	dir := t.TempDir()
	cli = filepath.Join(dir, "satelle"+exeSuffix())
	daemon = filepath.Join(dir, "satelled"+exeSuffix())
	if err := os.WriteFile(cli, []byte("old cli"), 0o755); err != nil {
		t.Fatal(err)
	}
	if withDaemon {
		if err := os.WriteFile(daemon, []byte("#!/bin/sh\necho 'satelled 0.1.0 (commit x, built y)'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	prev := updateTarget
	updateTarget = func() (string, error) { return cli, nil }
	t.Cleanup(func() { updateTarget = prev })
	return cli, daemon
}

func asset(name, version string) string {
	return fmt.Sprintf("%s-v%s-%s-%s%s", name, version, runtime.GOOS, runtime.GOARCH, exeSuffix())
}

func readString(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestUpdateReplacesTheBinaryAndSatelledBeside(t *testing.T) {
	isolate(t)
	if runtime.GOOS == "windows" {
		t.Skip("the fake satelled is a shell script")
	}
	cli, daemon := fakeInstall(t, true)
	releaseServer(t, "v2.0.0", []string{"v2.0.0", "serve-v0.2.0", "serve-v0.1.0"}, map[string]string{
		"v2.0.0/" + asset("satelle", "2.0.0"):        "new cli",
		"serve-v0.2.0/" + asset("satelled", "0.2.0"): "new daemon",
	}, "")
	out := must(t, "update")
	if !strings.Contains(out, "satelle "+buildinfo.Version+" → 2.0.0") || !strings.Contains(out, "satelled 0.1.0 → 0.2.0") ||
		!strings.Contains(out, "satelle service restart") {
		t.Errorf("update printed:\n%s", out)
	}
	if got := readString(t, cli); got != "new cli" {
		t.Errorf("cli = %q", got)
	}
	if got := readString(t, daemon); got != "new daemon" {
		t.Errorf("satelled = %q", got)
	}
	if fi, err := os.Stat(cli); err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("the new binary is not executable: %v %v", fi.Mode(), err)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(cli), ".*new*")); len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}
}

func TestUpdateToAVersionWithoutSatelled(t *testing.T) {
	isolate(t)
	cli, daemon := fakeInstall(t, false)
	releaseServer(t, "v2.0.0", nil, map[string]string{
		"v1.5.0/" + asset("satelle", "1.5.0"): "pinned cli",
	}, "")
	out := must(t, "update", "--version", "1.5.0")
	if !strings.Contains(out, "→ 1.5.0") || strings.Contains(out, "satelled") {
		t.Errorf("update printed:\n%s", out)
	}
	if got := readString(t, cli); got != "pinned cli" {
		t.Errorf("cli = %q", got)
	}
	if _, err := os.Stat(daemon); err == nil {
		t.Error("update created a satelled that was not there")
	}
}

func TestUpdateRefusesABadChecksum(t *testing.T) {
	isolate(t)
	cli, _ := fakeInstall(t, false)
	a := "v2.0.0/" + asset("satelle", "2.0.0")
	releaseServer(t, "v2.0.0", nil, map[string]string{a: "evil cli"}, a)
	if _, err := run(t, "", "update"); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("update with a bad checksum: %v", err)
	}
	if got := readString(t, cli); got != "old cli" {
		t.Errorf("the binary was replaced: %q", got)
	}
}

func TestUpdateMissingAssetFails(t *testing.T) {
	isolate(t)
	cli, _ := fakeInstall(t, false)
	releaseServer(t, "v3.0.0", nil, map[string]string{}, "")
	if _, err := run(t, "", "update"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("update with no asset: %v", err)
	}
	if got := readString(t, cli); got != "old cli" {
		t.Errorf("the binary was replaced: %q", got)
	}
}

func TestNewestServeTagComparesNumerically(t *testing.T) {
	for _, c := range []struct {
		tags []string
		want string
		ok   bool
	}{
		{[]string{"v1.0.10", "serve-v1.0.9", "v1.0.9", "serve-v1.0.10"}, "serve-v1.0.10", true},
		{[]string{"serve-v0.0.297", "serve-v1.0.2", "serve-v1.0.10", "serve-v1.0.9"}, "serve-v1.0.10", true},
		{[]string{"serve-vnext", "serve-v1.0.1"}, "serve-v1.0.1", true},
		{[]string{"serve-v1.0", "serve-v1.0.1"}, "serve-v1.0.1", true},
		{[]string{"v2.0.0", "v1.0.0"}, "", false},
	} {
		got, ok := newestServeTag(c.tags)
		if got != c.want || ok != c.ok {
			t.Errorf("newestServeTag(%v) = %q %v, want %q %v", c.tags, got, ok, c.want, c.ok)
		}
	}
}

// GitHub lists serve-v1.0.9 before serve-v1.0.10; update takes 1.0.10.
func TestUpdateInstallsTheNewestSatelledNotTheFirstListed(t *testing.T) {
	isolate(t)
	if runtime.GOOS == "windows" {
		t.Skip("the fake satelled is a shell script")
	}
	_, daemon := fakeInstall(t, true)
	releaseServer(t, "v1.0.10", []string{"v1.0.10", "serve-v1.0.9", "v1.0.9", "serve-v1.0.10"}, map[string]string{
		"v1.0.10/" + asset("satelle", "1.0.10"):        "cli 1.0.10",
		"serve-v1.0.9/" + asset("satelled", "1.0.9"):   "daemon 1.0.9",
		"serve-v1.0.10/" + asset("satelled", "1.0.10"): "daemon 1.0.10",
	}, "")
	out := must(t, "update")
	if !strings.Contains(out, "satelled 0.1.0 → 1.0.10") {
		t.Errorf("update printed:\n%s", out)
	}
	if got := readString(t, daemon); got != "daemon 1.0.10" {
		t.Errorf("satelled = %q", got)
	}
}

func TestUpdatePinsSatelledToTheVersion(t *testing.T) {
	isolate(t)
	if runtime.GOOS == "windows" {
		t.Skip("the fake satelled is a shell script")
	}
	cli, daemon := fakeInstall(t, true)
	releaseServer(t, "v2.0.0", []string{"v2.0.0", "serve-v2.0.0"}, map[string]string{
		"v1.5.0/" + asset("satelle", "1.5.0"):        "pinned cli",
		"serve-v1.5.0/" + asset("satelled", "1.5.0"): "pinned daemon",
		"serve-v2.0.0/" + asset("satelled", "2.0.0"): "newer daemon",
	}, "")
	out := must(t, "update", "--version", "1.5.0")
	if !strings.Contains(out, "satelled 0.1.0 → 1.5.0") {
		t.Errorf("update printed:\n%s", out)
	}
	if got := readString(t, cli); got != "pinned cli" {
		t.Errorf("cli = %q", got)
	}
	if got := readString(t, daemon); got != "pinned daemon" {
		t.Errorf("satelled = %q", got)
	}
}

func TestUpdatePinnedSatelledWithoutAssetRefuses(t *testing.T) {
	isolate(t)
	if runtime.GOOS == "windows" {
		t.Skip("the fake satelled is a shell script")
	}
	cli, daemon := fakeInstall(t, true)
	before := readString(t, daemon)
	releaseServer(t, "v2.0.0", []string{"v2.0.0", "serve-v2.0.0"}, map[string]string{
		"v1.5.0/" + asset("satelle", "1.5.0"):        "pinned cli",
		"serve-v2.0.0/" + asset("satelled", "2.0.0"): "newer daemon",
	}, "")
	_, err := run(t, "", "update", "--version", "1.5.0")
	if err == nil || !strings.Contains(err.Error(), "serve-v1.5.0") || !strings.Contains(err.Error(), asset("satelled", "1.5.0")) {
		t.Fatalf("pinned update without a satelled asset: %v", err)
	}
	if got := readString(t, cli); got != "old cli" {
		t.Errorf("the cli was replaced: %q", got)
	}
	if got := readString(t, daemon); got != before {
		t.Errorf("satelled was replaced: %q", got)
	}
}
