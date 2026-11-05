package hosted

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
)

func withLocationState(t *testing.T) string {
	t.Helper()
	isolateHome(t)
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, "satelle", "location-state.json")
	LocationStatePathOverride = path
	t.Cleanup(func() { LocationStatePathOverride = "" })
	return path
}

func TestValidLocationID(t *testing.T) {
	ok := []string{"loc_abcd1234", "loc_" + strings.Repeat("a", 32), "A.b_c:d-e1"}
	for _, id := range ok {
		if !ValidLocationID(id) {
			t.Errorf("ValidLocationID(%q) = false, want true", id)
		}
	}
	bad := []string{"", "short", "has space xx", "loc/slash", strings.Repeat("x", 129), "loc_ab"}
	for _, id := range bad {
		if ValidLocationID(id) {
			t.Errorf("ValidLocationID(%q) = true, want false", id)
		}
	}
}

func TestLocationIDStableAndCharset(t *testing.T) {
	withLocationState(t)
	repo := t.TempDir()
	a, err := LocationID(repo)
	if err != nil {
		t.Fatal(err)
	}
	if !ValidLocationID(a) {
		t.Fatalf("derived id %q fails contract", a)
	}
	if !strings.HasPrefix(a, "loc_") || len(a) != 36 {
		t.Fatalf("id shape = %q (want loc_ + 32 hex)", a)
	}
	b, err := LocationID(repo)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("derive twice: %q vs %q", a, b)
	}
	other, err := LocationID(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if other == a {
		t.Fatal("different repo roots must not share a location id")
	}
}

func TestLocationIDPersistedWinsAfterMachineChange(t *testing.T) {
	path := withLocationState(t)
	repo := t.TempDir()
	first, err := LocationID(repo)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), first) {
		t.Fatalf("state file missing id %q: %s", first, raw)
	}
	// Corrupt machine_fallback to a new value; persisted id must still win.
	state, err := loadLocationState()
	if err != nil {
		t.Fatal(err)
	}
	state.MachineFallback = "changed-machine-id-value"
	if err := writeLocationState(state); err != nil {
		t.Fatal(err)
	}
	again, err := LocationID(repo)
	if err != nil {
		t.Fatal(err)
	}
	if again != first {
		t.Fatalf("persisted id lost after machine change: %q vs %q", first, again)
	}
}

func TestLocationIDRederivesInvalidPersisted(t *testing.T) {
	withLocationState(t)
	repo := t.TempDir()
	key := locationRepoKey(repo)
	state := locationStateFile{Locations: map[string]locationEntry{
		key: {ID: "bad id"},
	}}
	if err := writeLocationState(state); err != nil {
		t.Fatal(err)
	}
	got, err := LocationID(repo)
	if err != nil {
		t.Fatal(err)
	}
	if !ValidLocationID(got) {
		t.Fatalf("re-derived %q still invalid", got)
	}
	if got == "bad id" {
		t.Fatal("invalid persisted id was not replaced")
	}
}

func TestLocationRegisteredRoundTrip(t *testing.T) {
	withLocationState(t)
	repo := t.TempDir()
	if _, err := LocationID(repo); err != nil {
		t.Fatal(err)
	}
	if LocationRegisteredAs(repo, "https://satelle.dev/", "a") {
		t.Fatal("not yet registered")
	}
	if err := MarkLocationRegistered(repo, "https://satelle.dev/", "a"); err != nil {
		t.Fatal(err)
	}
	if !LocationRegisteredAs(repo, "https://satelle.dev", "a") {
		t.Fatal("normalized server should match trailing-slash key")
	}
	if LocationRegisteredAs(repo, "https://satelle.dev", "b") {
		t.Fatal("a different label must read as not registered")
	}
}

// A linked worktree and its main tree share host and repo name; only the linked
// tree adds its directory, and only a held story adds the story id.
func TestSessionName(t *testing.T) {
	main := t.TempDir()
	runGit(t, main, "init", "-q", "-b", "main")
	runGit(t, main, "-c", "user.email=a@b", "-c", "user.name=n", "commit", "-q", "--allow-empty", "-m", "i")
	if err := os.MkdirAll(filepath.Join(main, ".satelle"), 0o755); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(t.TempDir(), "sty_wt")
	runGit(t, main, "worktree", "add", "-q", "-b", "wt", linked)
	same := filepath.Join(t.TempDir(), "sty_same")
	runGit(t, main, "worktree", "add", "-q", "-b", "same", same)
	host, repo := ShortHost(), config.RepoName(main)
	cases := []struct {
		name, root, story, want string
	}{
		{"main, no story", main, "", host + " · " + repo},
		{"main, story", main, "sty_1", host + " · " + repo + " · sty_1"},
		{"linked, no story", linked, "", host + " · " + repo + " · sty_wt"},
		{"linked, story", linked, "sty_1", host + " · " + repo + " · sty_wt · sty_1"},
		{"linked, story named like worktree", same, "sty_same", host + " · " + repo + " · sty_same"},
	}
	for _, c := range cases {
		if got := SessionName(c.root, c.story); got != c.want {
			t.Errorf("%s: SessionName = %q, want %q", c.name, got, c.want)
		}
	}
	if got := LocationLabel(linked); got != SessionName(linked, "") {
		t.Errorf("LocationLabel = %q, want the story-less session name", got)
	}
}

func TestShortHostHasNoDomain(t *testing.T) {
	if h := ShortHost(); h == "" || strings.Contains(h, ".") {
		t.Fatalf("ShortHost = %q", h)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
