package remotenode

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/filebundle"
)

// fixtureRemoteToml is the v1 item definition in the remotedef package's
// testdata; every bundle here carries it as its remote.toml.
func fixtureRemoteToml(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "remotedef", "testdata", "remote.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// definitionFiles is a small definition: a remote.toml and an overlay.
func definitionFiles(t *testing.T) map[string]string {
	return map[string]string{
		"remote.toml":           fixtureRemoteToml(t),
		"workflows/remote.toml": "workflow",
		"skills/remote-work.md": "skill",
		"agents.toml":           "agents",
	}
}

// bundleOf encodes files as a format-1 bundle, in path order.
func bundleOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	entries := make([]filebundle.Entry, len(paths))
	for i, p := range paths {
		entries[i] = filebundle.Entry{Path: p, Content: files[p]}
	}
	b, err := filebundle.Encode(entries)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// listing is every file under dir with its content.
func listing(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out[filepath.ToSlash(rel)] = string(body)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// The bundle's files overlay the data dir, the data dir's other files stay,
// and remote.toml is read but not installed.
func TestInstallDefinitionOverlaysTheDataDir(t *testing.T) {
	files := definitionFiles(t)
	dataDir := t.TempDir()
	writeFiles(t, dataDir, map[string]string{
		"skills/remote-work.md": "the repo's",
		"satelle.toml":          "kept",
	})
	def, err := InstallDefinition(dataDir, bundleOf(t, files))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"satelle.toml": "kept"}
	for rel, body := range files {
		if rel != DefinitionFile {
			want[rel] = body
		}
	}
	if got := listing(t, dataDir); !reflect.DeepEqual(got, want) {
		t.Errorf("data dir = %v, want %v", got, want)
	}
	if e := def.SecretEntries(); len(e) != 1 || e[0].Key != "CLAUDE_CODE_OAUTH_TOKEN" || len(def.Repos) != 1 ||
		!reflect.DeepEqual(def.Allow(), []string{"api.example.com", "*.example.org"}) {
		t.Errorf("definition = %+v", def)
	}
}

// AC3: a bundle that does not check out installs nothing and names the cause.
func TestInstallDefinitionRefusesAndInstallsNothing(t *testing.T) {
	good := definitionFiles(t)
	with := func(path, body string) map[string]string {
		m := map[string]string{}
		for k, v := range good {
			m[k] = v
		}
		m[path] = body
		return m
	}
	without := func(path string) map[string]string {
		m := with(path, "")
		delete(m, path)
		return m
	}
	for _, tc := range []struct {
		name   string
		bundle []byte
		want   string
	}{
		{"another format", []byte(`{"format":2,"files":[]}`), "unknown bundle format 2"},
		{"not a bundle", []byte(`<html>`), "invalid character"},
		{"path escapes", bundleOf(t, with("../x", "x")), "escapes"},
		{"absolute path", bundleOf(t, with("/etc/x", "x")), "not a plain relative path"},
		{"no remote.toml", bundleOf(t, without("remote.toml")), "no remote.toml"},
		{"malformed remote.toml", bundleOf(t, with("remote.toml", "[[secret\nkey = 1")), "remote.toml"},
		{"wrong-typed allow", bundleOf(t, with("remote.toml", "[network]\nallow = \"x\"\n")), "remote.toml"},
		{"wrong-typed repo url", bundleOf(t, with("remote.toml", "[[repo]]\nurl = 3\n")), "remote.toml"},
		{"unknown key", bundleOf(t, with("remote.toml", "colour = \"x\"\n")), "colour: unknown key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			before := map[string]string{"workflows/keep.toml": "installed earlier", "config/x.toml": "x"}
			writeFiles(t, dataDir, before)

			_, err := InstallDefinition(dataDir, tc.bundle)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("InstallDefinition = %v, want an error containing %q", err, tc.want)
			}
			if got := listing(t, dataDir); !reflect.DeepEqual(got, before) {
				t.Errorf("a refused bundle changed the data dir: %v, want %v", got, before)
			}
		})
	}
}
