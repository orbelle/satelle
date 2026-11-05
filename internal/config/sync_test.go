package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// [sync] hold_unpushed: missing means true, a boolean sets it, anything else is
// a config error (sty_7361569a).
func TestHoldUnpushed(t *testing.T) {
	cases := []struct {
		name    string
		sync    SyncTable
		want    bool
		wantErr bool
	}{
		{"missing", nil, true, false},
		{"blank", SyncTable{"hold_unpushed": " "}, true, false},
		{"true", SyncTable{"hold_unpushed": "true"}, true, false},
		{"false", SyncTable{"hold_unpushed": "false"}, false, false},
		{"bad", SyncTable{"hold_unpushed": "maybe"}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := HoldUnpushed(Config{Sync: tc.sync})
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Fatalf("HoldUnpushed = %v, want %v", got, tc.want)
			}
		})
	}
}

// A satelle.toml still carrying the retired [sync] hold_stale_after key (hosted
// holds are gone, sty_40853d02) loads without error, the key disturbs no other
// key and is never minted as an area.
func TestRetiredHoldKeyLoadsSilently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "satelle.toml")
	if err := os.WriteFile(path, []byte("[sync]\nstale_after = \"24h\"\nhold_stale_after = \"2h\"\nproject = \"p\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("a config carrying the retired key must load: %v", err)
	}
	if cfg.SyncProject() != "p" {
		t.Errorf("project = %q; the retired key must not disturb the other keys", cfg.SyncProject())
	}
	for _, a := range SyncAreas {
		if a == "hold_stale_after" {
			t.Error("hold_stale_after minted as a sync area")
		}
	}
}

// A bare TOML boolean loads, the overlay wins per key, and hold_unpushed is a
// reserved key rather than a sync area.
func TestSyncTableAcceptsBareBoolean(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "satelle.toml")
	if err := os.WriteFile(path, []byte("[sync]\nproject = \"p\"\nhold_unpushed = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Sync["project"] != "p" {
		t.Errorf("project = %q, want p", cfg.Sync["project"])
	}
	if got, err := HoldUnpushed(cfg); err != nil || got {
		t.Errorf("HoldUnpushed = %v, %v; want false, nil", got, err)
	}
	if !reservedSyncKeys[syncHoldUnpushedKey] {
		t.Error("hold_unpushed must be a reserved [sync] key")
	}
	for _, a := range SyncAreas {
		if a == syncHoldUnpushedKey {
			t.Errorf("hold_unpushed minted as a sync area")
		}
	}

	bad := filepath.Join(dir, "bad.toml")
	if err := os.WriteFile(bad, []byte("[sync]\nhold_unpushed = 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(bad); err == nil {
		t.Error("an integer [sync] value must be a config error")
	}
}

func TestParseScope(t *testing.T) {
	cases := map[string]Scope{"local": LocalScope, "personal": PersonalScope, "team": TeamScope}
	for raw, want := range cases {
		got, err := ParseScope(raw)
		if err != nil {
			t.Fatalf("ParseScope(%q) error: %v", raw, err)
		}
		if got != want {
			t.Errorf("ParseScope(%q) = %v, want %v", raw, got, want)
		}
		if got.String() != raw {
			t.Errorf("Scope(%v).String() = %q, want %q", got, got.String(), raw)
		}
	}
	if _, err := ParseScope("typo"); err == nil {
		t.Error("ParseScope(\"typo\") did not error")
	}
	if _, err := ParseScope("shared"); err == nil {
		t.Error("ParseScope(\"shared\") must error: the ladder is local|personal|team")
	}
}

// A repo with no [sync] project has no binding: the directory-name default of
// ResolveBoundProject is never the binding scope resolves from (AC2).
func TestUnboundRepoHasEmptyBinding(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "my-repo")
	cfg := Config{Sync: SyncTable{"stale_after": "24h"}}
	if got := cfg.SyncProject(); got != "" {
		t.Errorf("SyncProject = %q, want empty", got)
	}
	if got := ResolveBoundProject(cfg, repo); got != "my-repo" {
		t.Fatalf("fixture: ResolveBoundProject = %q, want the directory-name default", got)
	}
}

func TestFileShared(t *testing.T) {
	sharedFM := "---\ntype: skill\nshared: true\n---\nbody"
	unsetFM := "---\ntype: skill\n---\nbody"
	falseFM := "---\ntype: skill\nshared: false\n---\nbody"
	garbageFM := "---\ntype: skill\nshared: yes\n---\nbody"

	if !FileShared(PersonalScope, sharedFM) {
		t.Error("FileShared(personal, shared:true) = false, want true")
	}
	if FileShared(PersonalScope, unsetFM) {
		t.Error("FileShared(personal, no shared key) = true, want false")
	}
	if FileShared(PersonalScope, falseFM) {
		t.Error("FileShared(personal, shared:false) = true, want false")
	}
	if FileShared(PersonalScope, garbageFM) {
		t.Error("FileShared(personal, shared:yes) = true, want false (fail closed)")
	}
	// The flag is only meaningful inside a personal area — local/shared ignore it
	// entirely, even when the frontmatter itself says shared: true.
	if FileShared(LocalScope, sharedFM) {
		t.Error("FileShared(local, shared:true) = true, want false — flag ignored outside personal")
	}
	if FileShared(TeamScope, sharedFM) {
		t.Error("FileShared(team, shared:true) = true, want false — flag ignored outside personal")
	}
}

func TestSyncAreasNoDuplicates(t *testing.T) {
	seen := map[string]bool{}
	for _, a := range SyncAreas {
		if seen[a] {
			t.Errorf("SyncAreas contains duplicate %q", a)
		}
		seen[a] = true
	}
	for _, kind := range AuthoredKinds {
		if !seen[kind] {
			t.Errorf("SyncAreas missing AuthoredKind %q", kind)
		}
	}
}

// sty_195dd215 AC2: the tasks area carries only top-level task definitions — a
// task's execution runs and their outputs under tasks/<tsk>/ never go up.
func TestTasksAreaCarriesOnlyDefinitions(t *testing.T) {
	repo := t.TempDir()
	for rel, body := range map[string]string{
		"tasks/tsk_x.md":            "---\nid: tsk_x\n---\n",
		"tasks/tsk_x/report.md":     "a run output\n",
		"tasks/tsk_x/exe_1.md":      "---\nid: exe_1\n---\n",
		"tasks/tsk_x/deep/notes.md": "deeper\n",
		"tasks/notes.txt":           "not a definition\n",
	} {
		p := filepath.Join(repo, ".satelle", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files, err := AreaFiles(Config{}, repo, "tasks", Ignore{})
	if err != nil {
		t.Fatalf("AreaFiles: %v", err)
	}
	if got, want := bundlePaths(files), []string{"tasks/tsk_x.md"}; !reflect.DeepEqual(got, want) {
		t.Errorf("AreaFiles(tasks) = %v, want %v", got, want)
	}
}

// sty_7c01ca14 AC5: [sync] recent_window is removed. A file carrying it fails to
// load naming the key and the heal, FindSyncScopeKeys reports it, and
// RemoveSyncScopeKeys strips it, keeping every other line.
func TestSyncTableRefusesRecentWindow(t *testing.T) {
	for _, file := range []string{ConfigName, LocalConfigName} {
		t.Run(file, func(t *testing.T) {
			dir := t.TempDir()
			committed := "[sync]\nproject = \"p\"\n"
			if file == ConfigName {
				committed += "recent_window = \"168h\"\n"
			} else if err := os.WriteFile(filepath.Join(dir, LocalConfigName), []byte("[sync]\nrecent_window = \"24h\"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, ConfigName)
			if err := os.WriteFile(path, []byte(committed), 0o644); err != nil {
				t.Fatal(err)
			}
			_, _, err := Load(path)
			if err == nil {
				t.Fatalf("Load accepted [sync] recent_window in %s", file)
			}
			for _, w := range []string{"[sync] recent_window", "is removed", "satelle init", file} {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not name %q", err, w)
				}
			}
			found, err := FindSyncScopeKeys(dir)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(found[file], []string{RemovedRecentWindowKey}) {
				t.Errorf("FindSyncScopeKeys = %v, want %s in %s", found, RemovedRecentWindowKey, file)
			}
		})
	}
	got, removed := RemoveSyncScopeKeys("[sync]\nproject = \"p\"\nrecent_window = \"168h\"\nstale_after = \"6h\"\n")
	if want := "[sync]\nproject = \"p\"\nstale_after = \"6h\"\n"; got != want || !reflect.DeepEqual(removed, []string{RemovedRecentWindowKey}) {
		t.Errorf("RemoveSyncScopeKeys = %q, %v; want %q, [recent_window]", got, removed, want)
	}
}
