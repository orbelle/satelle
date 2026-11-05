package buildinfo

import (
	"runtime/debug"
	"testing"
)

func TestIsReleaseVersion(t *testing.T) {
	cases := map[string]bool{
		"0.0.1":               true,
		"1.2.3":               true,
		"dev":                 false,
		"":                    false,
		"0.0.0-dev+abc123":    false,
		"0.0.0-dev+abc-dirty": false,
	}
	for v, want := range cases {
		if got := IsReleaseVersion(v); got != want {
			t.Errorf("IsReleaseVersion(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestResolveFrom_ReleaseVerbatim(t *testing.T) {
	in := Info{Name: "satelled", Version: "0.0.5", Commit: "deadbeef", BuildTime: "2026-06-26"}
	// VCS settings present but must be ignored for a stamped release.
	got := resolveFrom(in, []debug.BuildSetting{{Key: "vcs.revision", Value: "ffffffffffffffff"}})
	if got != in {
		t.Errorf("release build mutated: got %+v, want %+v", got, in)
	}
}

func TestResolveFrom_DefaultName(t *testing.T) {
	got := resolveFrom(Info{Version: "0.0.5", Commit: "c", BuildTime: "t"}, nil)
	if got.Name != "satelle" {
		t.Errorf("Name = %q, want satelle default", got.Name)
	}
}

func TestResolveFrom_DevFallsBackToVCS(t *testing.T) {
	settings := []debug.BuildSetting{
		{Key: "vcs.revision", Value: "0123456789abcdef0000"},
		{Key: "vcs.modified", Value: "true"},
		{Key: "vcs.time", Value: "2026-06-26T00:00:00Z"},
	}
	got := resolveFrom(Info{Version: "dev", Commit: "none", BuildTime: "unknown"}, settings)
	if got.Version != "0.0.0-dev+0123456789ab-dirty" {
		t.Errorf("Version = %q, want dev+short-dirty", got.Version)
	}
	if got.Commit != "0123456789ab" {
		t.Errorf("Commit = %q, want short rev", got.Commit)
	}
	if got.BuildTime != "2026-06-26T00:00:00Z" {
		t.Errorf("BuildTime = %q, want vcs.time", got.BuildTime)
	}
}

func TestResolveFrom_DevNoVCS(t *testing.T) {
	in := Info{Version: "dev", Commit: "none", BuildTime: "unknown"}
	got := resolveFrom(in, nil)
	want := Info{Name: "satelle", Version: "dev", Commit: "none", BuildTime: "unknown"}
	if got != want {
		t.Errorf("no-VCS dev build: got %+v, want %+v", got, want)
	}
}

// String is the one identity line: name, version (dev marker kept), commit
// shortened to 12, build time.
func TestInfoString(t *testing.T) {
	rel := Info{Name: "satelle", Version: "1.0.0", Commit: "0123456789abcdef0123456789abcdef01234567", BuildTime: "2026-11-06T09:30:00Z"}
	if got, want := rel.String(), "satelle 1.0.0 (commit 0123456789ab, built 2026-11-06T09:30:00Z)"; got != want {
		t.Errorf("release String() = %q, want %q", got, want)
	}
	dev := Info{Name: "satelled", Version: "1.0.0+0123456789ab-dirty", Commit: "0123456789ab", BuildTime: "2026-11-06T09:30:00Z"}
	if got, want := dev.String(), "satelled 1.0.0+0123456789ab-dirty (commit 0123456789ab, built 2026-11-06T09:30:00Z)"; got != want {
		t.Errorf("dev String() = %q, want %q", got, want)
	}
}
