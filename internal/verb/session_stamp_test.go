package verb

// The configuration stamp on agent_invocation rows (sty_9914df10 AC2): rows
// written through the reviewer builder (invocationPayload) and the executor
// builder (dispatchPayload) carry the substrate and bindings digests, and the
// substrate digest follows an edit to an authored document.

import (
	"os"
	"path/filepath"
	"testing"
)

func writeAuthored(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

type stampPair struct{ substrate, bindings string }
