package serve

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// satelled is decoupled from the CLI: it links none of the CLI or the kernel's
// repo-writing stack, and it never execs a satelle verb.
func TestServeStaysOffTheCLI(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}
	for _, f := range []string{"internal/cli", "internal/kernel", "internal/hosted", "internal/uipush"} {
		if strings.Contains(string(out), "github.com/bobmcallan/satelle/"+f+"\n") {
			t.Errorf("internal/serve must not depend on %s", f)
		}
	}
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), `"os/exec"`) {
			t.Errorf("%s imports os/exec: satelled does not exec the CLI", f)
		}
	}
}
