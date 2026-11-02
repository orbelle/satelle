//go:build unix

package remotedef

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
)

// isolateHandoff points the user cache dir at a temp dir and clears the path
// variable, so the test reads and writes only its own hand-off.
func isolateHandoff(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv(EnvSecretsFile, "")
	_ = os.Unsetenv(EnvSecretsFile)
	path, err := HandoffPath()
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHandoffRoundTripIsPrivateAndRemovable(t *testing.T) {
	want := map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "tok-roundtrip"}
	path := isolateHandoff(t)
	got, err := WriteHandoff(want)
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Errorf("WriteHandoff wrote %s, want %s", got, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("hand-off mode = %v, want 0600", info.Mode().Perm())
	}
	if dir, _ := os.Stat(filepath.Dir(path)); dir.Mode().Perm() != 0o700 {
		t.Errorf("hand-off dir mode = %v, want 0700", dir.Mode().Perm())
	}
	if m := LoadHandoff(); !reflect.DeepEqual(m, want) {
		t.Errorf("LoadHandoff = %v, want %v", m, want)
	}

	// The path variable wins over the default location.
	moved := filepath.Join(t.TempDir(), "elsewhere.json")
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	if m := LoadHandoff(); m != nil {
		t.Errorf("LoadHandoff with the file moved and no %s = %v, want nil", EnvSecretsFile, m)
	}
	t.Setenv(EnvSecretsFile, moved)
	if m := LoadHandoff(); !reflect.DeepEqual(m, want) {
		t.Errorf("LoadHandoff through %s = %v, want %v", EnvSecretsFile, m, want)
	}

	if err := RemoveHandoff(moved); err != nil {
		t.Fatal(err)
	}
	if err := RemoveHandoff(moved); err != nil {
		t.Errorf("removing a missing hand-off: %v, want nil", err)
	}
	if m := LoadHandoff(); m != nil {
		t.Errorf("LoadHandoff after removal = %v, want nil", m)
	}
}

func TestHandoffWithAWiderModeIsIgnored(t *testing.T) {
	path := isolateHandoff(t)
	if _, err := WriteHandoff(map[string]string{"K": "v"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if m := LoadHandoff(); m != nil {
		t.Errorf("LoadHandoff of a 0644 hand-off = %v, want nil", m)
	}
}

func TestHandoffThroughASymlinkIsIgnored(t *testing.T) {
	path := isolateHandoff(t)
	if _, err := WriteHandoff(map[string]string{"K": "v"}); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvSecretsFile, link)
	if m := LoadHandoff(); m != nil {
		t.Errorf("LoadHandoff through a symlink = %v, want nil", m)
	}
}

// otherOwner is a FileInfo for a 0600 file owned by another uid.
type otherOwner struct{ os.FileInfo }

func (otherOwner) Mode() os.FileMode { return 0o600 }
func (otherOwner) Sys() any          { return &syscall.Stat_t{Uid: uint32(os.Getuid() + 1)} }

func TestHandoffOwnedByAnotherUserIsNotPrivate(t *testing.T) {
	if privateToMe(otherOwner{}) {
		t.Error("a 0600 file owned by another uid passed as private")
	}
	path := isolateHandoff(t)
	if _, err := WriteHandoff(map[string]string{"K": "v"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !privateToMe(info) {
		t.Error("this user's own 0600 hand-off did not pass as private")
	}
}

func TestHandoffOfAJoinThatHasGoneIsIgnored(t *testing.T) {
	path := isolateHandoff(t)
	if _, err := WriteHandoff(map[string]string{"K": "v"}); err != nil {
		t.Fatal(err)
	}
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Skipf("no `true` to stand in for a finished join: %v", err)
	}
	b, err := json.Marshal(handoff{PID: gone.Process.Pid, Secrets: map[string]string{"K": "v"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	// Run reaped it, so its pid names no live process.
	if m := LoadHandoff(); m != nil {
		t.Errorf("LoadHandoff of a hand-off whose join has gone = %v, want nil", m)
	}
}
