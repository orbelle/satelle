package herdr

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestEmbeddedFilesMatchShipped(t *testing.T) {
	var rels []string
	for _, f := range Files() {
		rels = append(rels, f.Rel)
		if len(f.Body) == 0 {
			t.Errorf("%s is empty", f.Rel)
		}
	}
	if got := strings.Join(rels, ","); got != "mods/satelle/main.js,mods/satelle/mod.json,layouts/satelle.json,layouts/satelle.svg" {
		t.Fatalf("shipped files = %s", got)
	}
	layout := string(Files()[2].Body)
	for _, want := range []string{`"mod": "satelle"`, `"preview": "layouts/satelle.svg"`} {
		if !strings.Contains(layout, want) {
			t.Errorf("layouts/satelle.json lacks %s", want)
		}
	}
}

func TestResolveHome(t *testing.T) {
	homes := map[string]string{"linux": "/home/u", "darwin": "/Users/u", "windows": `C:\Users\u`}
	defaults := map[string]string{"linux": "/home/u/.config/herdr-app", "darwin": "/Users/u/.config/herdr-app", "windows": `C:\Users\u\.config\herdr-app`}
	for _, goos := range []string{"linux", "darwin", "windows"} {
		for _, c := range []struct {
			name, env, setting, want string
			src                      Source
		}{
			{"nothing", "", "", defaults[goos], SourceDefault},
			{"env", "/env/herdr", "", "/env/herdr", SourceEnv},
			{"setting", "", "/set/herdr", "/set/herdr", SourceSetting},
			{"env+setting", "/env/herdr", "/set/herdr", "/set/herdr", SourceSetting},
		} {
			got, src := Resolve(goos, homes[goos], c.env, c.setting)
			if got != c.want || src != c.src {
				t.Errorf("%s/%s: Resolve = %q, %v; want %q, %v", goos, c.name, got, src, c.want, c.src)
			}
		}
	}
}

func write(t *testing.T, p string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInstallAndRefreshRule(t *testing.T) {
	home, sat := t.TempDir(), t.TempDir()
	if res, _ := Refresh(home, sat); !res.NotInstalled {
		t.Fatalf("refresh before install = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(home, "mods")); err == nil {
		t.Fatal("refresh before install wrote into herdr's home")
	}
	taken := filepath.Join(home, "layouts", "satelle.svg")
	write(t, taken, []byte("someone else's"))
	res, err := Install(home, sat, false)
	if err != nil || len(res.Written) != 3 || len(res.Kept) != 1 || res.Kept[0].Path != taken {
		t.Fatalf("install over a taken name = %+v, %v", res, err)
	}
	if b, _ := os.ReadFile(taken); string(b) != "someone else's" {
		t.Fatal("install replaced a taken name satelle never wrote")
	}
	if res, _ = Install(home, sat, true); len(res.Written) != 1 || len(res.Current) != 3 {
		t.Fatalf("install --force = %+v", res)
	}
	main := filepath.Join(home, "mods", "satelle", "main.js")
	if err := os.Remove(main); err != nil {
		t.Fatal(err)
	}
	res, err = Refresh(home, sat)
	if err != nil || len(res.Kept) != 1 || res.Kept[0].Path != main || res.Kept[0].Reason != ReasonMissing {
		t.Fatalf("refresh with a removed file = %+v, %v", res, err)
	}
	if _, err := os.Stat(main); err == nil {
		t.Fatal("refresh put back a file removed outside satelle")
	}
	if res, _ = Install(home, sat, false); len(res.Written) != 1 || res.Written[0] != main {
		t.Fatalf("install after a removal = %+v", res)
	}
	if b, _ := os.ReadFile(main); !bytes.Equal(b, Files()[0].Body) {
		t.Fatal("main.js is not the shipped content")
	}
	if err := os.RemoveAll(home); err != nil {
		t.Fatal(err)
	}
	if res, _ = Refresh(home, sat); !res.HomeGone {
		t.Fatalf("refresh of a gone home = %+v", res)
	}
	if res, err = Install(home, sat, false); err != ErrNoHome {
		t.Fatalf("install into a missing home = %+v, %v", res, err)
	}
	if _, err := os.Stat(home); err == nil {
		t.Fatal("install created herdr's home")
	}
}

// TestEmbeddedPanelReadsVersionEachRefresh runs without node: the shipped
// panel reads satelle version on every refresh, renders it in a footer, says
// unknown when it cannot, and carries no release version of its own.
func TestEmbeddedPanelReadsVersionEachRefresh(t *testing.T) {
	main := string(Files()[0].Body)
	for _, want := range []string{`exec("satelle", ["version"]`, `"satelle-footer"`, `"satelle version unknown"`} {
		if !strings.Contains(main, want) {
			t.Errorf("embedded main.js lacks %s", want)
		}
	}
	start := strings.Index(main, "  refresh(cx) {")
	end := strings.Index(main, "  watch(cwd, on, cx) {")
	if start < 0 || end < start {
		t.Fatalf("cannot locate refresh in embedded main.js (refresh at %d, watch at %d)", start, end)
	}
	if !strings.Contains(main[start:end], "this.loadVersion(") {
		t.Error("refresh does not call this.loadVersion, so the footer would not follow satelle update")
	}
	for _, v := range regexp.MustCompile(`\d+\.\d+\.\d+`).FindAllString(main, -1) {
		if v != "0.0.0" {
			t.Errorf("embedded main.js hard-codes version %s; the footer must come from satelle version", v)
		}
	}
}
