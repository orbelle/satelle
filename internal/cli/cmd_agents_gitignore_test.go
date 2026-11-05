package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitignoreRepo is a committed git repo whose .gitignore is body.
func gitignoreRepo(t *testing.T, body string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := tempRepo(t)
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInitCommit(t, repo)
	t.Chdir(repo)
	return repo
}

// managedBlock returns the lines between the managed .gitignore markers.
func managedBlock(t *testing.T, repo string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	i := strings.Index(s, gitignoreMarker)
	if i < 0 {
		return nil
	}
	j := strings.Index(s[i:], gitignoreMarkerEnd)
	if j < 0 {
		t.Fatalf("managed block has no end marker:\n%s", s)
	}
	return strings.Split(s[i:i+j], "\n")
}

func hasLine(lines []string, want string) bool {
	for _, l := range lines {
		if strings.TrimSpace(l) == want {
			return true
		}
	}
	return false
}

// sty_d15f6713 AC4: `agents install pi` adds .pi/ inside the managed block and
// says so; a second run leaves .gitignore byte-identical.
func TestAgentsInstallPiIgnoresScaffold(t *testing.T) {
	repo := gitignoreRepo(t, "node_modules/\n")
	out, err := runRootIn(t, "", "agents", "install", "pi")
	if err != nil {
		t.Fatalf("agents install pi: %v\n%s", err, out)
	}
	if !hasLine(managedBlock(t, repo), ".pi/") {
		t.Fatalf(".pi/ must be added inside the managed block:\n%s", out)
	}
	if !strings.Contains(out, "updated .gitignore (added: ") || !strings.Contains(out, ".pi/") {
		t.Errorf("install must print the added line:\n%s", out)
	}
	before, _ := os.ReadFile(filepath.Join(repo, ".gitignore"))
	out, err = runRootIn(t, "", "agents", "install", "pi")
	if err != nil {
		t.Fatalf("second install: %v\n%s", err, out)
	}
	after, _ := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if string(before) != string(after) {
		t.Errorf("second install changed .gitignore:\n--- before\n%s\n--- after\n%s", before, after)
	}
	if strings.Contains(out, "updated .gitignore") {
		t.Errorf("second install must report no .gitignore change:\n%s", out)
	}
}
