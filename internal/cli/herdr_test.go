package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/bobmcallan/satelle/internal/buildinfo"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hostapp/herdr"
)

func isolateHerdr(t *testing.T, create bool) string {
	t.Helper()
	isolate(t)
	user := os.Getenv("HOME")
	t.Setenv("USERPROFILE", user)
	t.Setenv(herdr.EnvHome, "")
	home := filepath.Join(user, ".config", "herdr-app")
	if create {
		if err := os.MkdirAll(home, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	got := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() {
			got[rel+"/"] = ""
			return nil
		}
		b, err := os.ReadFile(p)
		got[rel] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func panelPath(home, rel string) string { return filepath.Join(home, filepath.FromSlash(rel)) }

func assertShipped(t *testing.T, home string, skip ...string) {
	t.Helper()
	for _, f := range herdr.Files() {
		if contains(skip, f.Rel) {
			continue
		}
		b, err := os.ReadFile(panelPath(home, f.Rel))
		if err != nil || !bytes.Equal(b, f.Body) {
			t.Errorf("%s is not the shipped content (%v)", f.Rel, err)
		}
	}
}

func ageInstall(t *testing.T, home string) {
	t.Helper()
	recPath := filepath.Join(config.GlobalDir(), herdr.RecordName)
	var rec struct {
		Homes map[string]map[string]string `json:"homes"`
	}
	b, err := os.ReadFile(recPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatal(err)
	}
	entry := rec.Homes[filepath.Clean(home)]
	if entry == nil {
		t.Fatalf("no record for %s in %s", home, b)
	}
	for _, f := range herdr.Files() {
		old := []byte("older satelle " + f.Rel)
		if err := os.WriteFile(panelPath(home, f.Rel), old, 0o644); err != nil {
			t.Fatal(err)
		}
		s := sha256.Sum256(old)
		entry[f.Rel] = hex.EncodeToString(s[:])
	}
	out, _ := json.Marshal(rec)
	if err := os.WriteFile(recPath, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHerdrInstallWritesTheFourFiles(t *testing.T) {
	home := isolateHerdr(t, true)
	write := func(rel, body string) {
		if err := os.MkdirAll(filepath.Dir(panelPath(home, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(panelPath(home, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("settings.json", `{"theme":"x"}`)
	write("mods/files/main.js", "herdr's own")
	before := snapshot(t, home)
	out := must(t, "herdr", "install")
	assertShipped(t, home)
	after := snapshot(t, home)
	for k, v := range before {
		if after[k] != v {
			t.Errorf("install changed %s", k)
		}
		delete(after, k)
	}
	want := map[string]bool{"mods/satelle/": true, "layouts/": true}
	for _, f := range herdr.Files() {
		want[filepath.FromSlash(f.Rel)] = true
		if !strings.Contains(out, "wrote "+panelPath(home, f.Rel)+"\n") {
			t.Errorf("install output does not name %s:\n%s", f.Rel, out)
		}
	}
	for k := range after {
		if !want[filepath.ToSlash(k)] && !want[k] {
			t.Errorf("install created %s", k)
		}
	}
	if len(after) != len(want) {
		var added []string
		for k := range after {
			added = append(added, k)
		}
		t.Errorf("install added %v, want %v", added, want)
	}
}

func TestHerdrRefreshUpdatesAnInstalledPanel(t *testing.T) {
	home := isolateHerdr(t, true)
	must(t, "herdr", "install")
	ageInstall(t, home)
	out := must(t, "agents", "refresh")
	assertShipped(t, home)
	for _, f := range herdr.Files() {
		if !strings.Contains(out, "refreshed herdr panel: "+panelPath(home, f.Rel)+"\n") {
			t.Errorf("refresh output does not name %s:\n%s", f.Rel, out)
		}
	}
}

func TestHerdrRefreshLeavesAMachineThatNeverAsked(t *testing.T) {
	home := isolateHerdr(t, true)
	if err := os.WriteFile(filepath.Join(home, "settings.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, home)
	out := must(t, "agents", "refresh")
	fakeInstall(t, false)
	releaseServer(t, "v"+buildinfo.Version, nil, nil, "")
	out += must(t, "update")
	if after := snapshot(t, home); len(after) != len(before) {
		t.Errorf("refresh wrote into a herdr home that never asked: %v", after)
	}
	if _, err := os.Stat(filepath.Join(home, "mods", "satelle")); err == nil {
		t.Error("refresh created mods/satelle")
	}
	if strings.Contains(out, "herdr panel") {
		t.Errorf("refresh spoke of a panel never installed:\n%s", out)
	}
}

func TestHerdrChangedOutsideSatelleIsLeft(t *testing.T) {
	for _, args := range [][]string{{"herdr", "install"}, {"agents", "refresh"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			home := isolateHerdr(t, true)
			must(t, "herdr", "install")
			ageInstall(t, home)
			edited := panelPath(home, "layouts/satelle.json")
			if err := os.WriteFile(edited, []byte("hand edited"), 0o644); err != nil {
				t.Fatal(err)
			}
			out := must(t, args...)
			if b, _ := os.ReadFile(edited); string(b) != "hand edited" {
				t.Errorf("%v replaced a file changed outside satelle", args)
			}
			if !strings.Contains(out, "left "+edited+": changed outside satelle; 'satelle herdr install --force' replaces it") {
				t.Errorf("%v output does not name the edited file:\n%s", args, out)
			}
			assertShipped(t, home, "layouts/satelle.json")
			must(t, "herdr", "install", "--force")
			assertShipped(t, home)
		})
	}
}

func TestHerdrInstallRecordsANonDefaultHome(t *testing.T) {
	def := isolateHerdr(t, true)
	cfg := config.GlobalConfigPath()
	must(t, "herdr", "install")
	if b, err := os.ReadFile(cfg); err == nil {
		var m map[string]any
		if _, err := toml.Decode(string(b), &m); err != nil {
			t.Fatal(err)
		}
		if _, ok := m["herdr"]; ok || strings.Contains(string(b), "herdr") {
			t.Errorf("install at the default home wrote a herdr key:\n%s", b)
		}
	}
	settings := must(t, "settings")
	if !strings.Contains(settings, `herdr.home        ""  (in effect: `+def+")") {
		t.Errorf("settings does not show herdr's default in effect:\n%s", settings)
	}

	other := filepath.Join(t.TempDir(), "herdr")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(herdr.EnvHome, other)
	out := must(t, "herdr", "install")
	if !strings.Contains(out, `recorded herdr.home = "`+other+`"`) {
		t.Errorf("install did not report recording the home:\n%s", out)
	}
	g, err := config.LoadGlobal()
	if err != nil || g.Herdr.Home != other {
		t.Fatalf("herdr.home = %q (%v), want %s", g.Herdr.Home, err, other)
	}
	t.Setenv(herdr.EnvHome, "")
	ageInstall(t, other)
	if err := os.RemoveAll(filepath.Join(def, "mods")); err != nil {
		t.Fatal(err)
	}
	must(t, "agents", "refresh")
	assertShipped(t, other)
	if _, err := os.Stat(filepath.Join(def, "mods", "satelle")); err == nil {
		t.Error("refresh without the variable wrote to the default home")
	}
	if settings := must(t, "settings"); !strings.Contains(settings, `herdr.home        "`+other+`"`) {
		t.Errorf("settings does not show the recorded home:\n%s", settings)
	}
}

func TestHerdrInstallWithoutHome(t *testing.T) {
	home := isolateHerdr(t, false)
	out, err := run(t, "", "herdr", "install")
	if err == nil || ExitCodeOf(err) != ExitError {
		t.Fatalf("install without a home: %v\n%s", err, out)
	}
	if msg := err.Error(); !strings.Contains(msg, home) || !strings.Contains(msg, "satelle settings set herdr.home") {
		t.Errorf("refusal does not name the folder and the setting: %s", msg)
	}
	if _, err := os.Stat(home); err == nil {
		t.Error("install created herdr's home")
	}
	if _, err := os.Stat(filepath.Join(config.GlobalDir(), herdr.RecordName)); err == nil {
		t.Error("a refused install wrote a record")
	}
}
