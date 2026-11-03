package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
)

// embeddedCheckScript is the ```check block of an embedded skill.
func embeddedCheckScript(t *testing.T, name string) string {
	t.Helper()
	for _, d := range config.EmbeddedDefaults() {
		if d.Kind == "skills" && d.Name == name {
			m := regexp.MustCompile("(?s)```check\n(.*?)\n```").FindStringSubmatch(d.Body)
			if m == nil {
				t.Fatalf("%s has no check block", name)
			}
			return m[1]
		}
	}
	t.Fatalf("embedded %s not found", name)
	return ""
}

// buildSatelle builds the satelle binary of this tree into a temp dir and returns
// that dir, to be put first on the check's PATH.
func buildSatelle(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..")
	bin := t.TempDir()
	cmd := exec.Command("go", "build", "-o", filepath.Join(bin, "satelle"), "./cmd/satelle")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/satelle: %v\n%s", err, out)
	}
	return bin
}

// runCheck runs script in repo with the built binary first on PATH and the
// transition payload on stdin; it returns the combined output and the exit code.
func runCheck(t *testing.T, script, binDir, repo, id string) (string, int) {
	t.Helper()
	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = repo
	cmd.Stdin = strings.NewReader(`{"id":"` + id + `"}`)
	cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return string(out), 0
	case errors.As(err, &ee):
		return string(out), ee.ExitCode()
	default:
		t.Fatalf("run check: %v", err)
		return "", -1
	}
}
