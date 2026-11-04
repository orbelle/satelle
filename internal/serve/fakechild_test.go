package serve

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeCLI is a shell script standing in for the satelle CLI that
// resolveSatelle names for the test (sty_0367d20c). Every *ViaCLI helper then
// runs its real exec path against it. Each run appends "$*" to calls; the
// script's behaviour comes from env the test sets:
//
//	SATELLE_TEST_OUTCOME  a line printed last on stdout (the outcome line)
//	SATELLE_TEST_EXIT     the exit status (default 0)
//	SATELLE_TEST_SLEEP    seconds to sleep while holding the overlap lock
//	SATELLE_TEST_LOCK     a directory made for the run; a second concurrent run
//	                      that finds it appends "overlap" to calls
//	SATELLE_TEST_ORIGINS  a file the run appends $SATELLE_INGEST_ORIGIN to
//	SATELLE_TEST_ENDPOINTS a file the run appends $SATELLE_SERVER_ENDPOINT to
type fakeCLI struct {
	calls string
}

func newFakeCLI(t *testing.T) *fakeCLI {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake satelle is a shell script")
	}
	dir := t.TempDir()
	f := &fakeCLI{calls: filepath.Join(dir, "calls.log")}
	script := `#!/bin/sh
echo "$*" >> "` + f.calls + `"
if [ -n "$SATELLE_TEST_ORIGINS" ]; then echo "${SATELLE_INGEST_ORIGIN:-local}" >> "$SATELLE_TEST_ORIGINS"; fi
if [ -n "$SATELLE_TEST_ENDPOINTS" ]; then echo "$* ${SATELLE_SERVER_ENDPOINT:-unset}" >> "$SATELLE_TEST_ENDPOINTS"; fi
if [ -n "$SATELLE_TEST_LOCK" ]; then
  if mkdir "$SATELLE_TEST_LOCK" 2>/dev/null; then
    sleep "${SATELLE_TEST_SLEEP:-0}"
    rmdir "$SATELLE_TEST_LOCK"
  else
    echo overlap >> "` + f.calls + `"
  fi
fi
if [ -n "$SATELLE_TEST_OUTCOME" ]; then echo "progress line"; echo "$SATELLE_TEST_OUTCOME"; fi
exit "${SATELLE_TEST_EXIT:-0}"
`
	bin := filepath.Join(dir, "satelle")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	prev := resolveSatelle
	resolveSatelle = func() (string, error) { return bin, nil }
	t.Cleanup(func() { resolveSatelle = prev })
	return f
}

// lines is every line the fake has appended to calls so far.
func (f *fakeCLI) lines(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(f.calls)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

// count is how many runs had argv starting with prefix.
func (f *fakeCLI) count(t *testing.T, prefix string) int {
	t.Helper()
	n := 0
	for _, ln := range f.lines(t) {
		if strings.HasPrefix(ln, prefix) {
			n++
		}
	}
	return n
}

// writeScript replaces the fake's script with body.
func writeScript(t *testing.T, bin, body string) {
	t.Helper()
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}
