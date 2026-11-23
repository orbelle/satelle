package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validCommandRemote = `version = 1
provider = "command"
satelle_version = "0.0.709"

[drive]
kind = "item"

[connect]
start = ["node-cli", "start", "{node}"]
network = ["node-cli", "network", "{node}"]
copy_in = ["node-cli", "copy", "{node}", "{src}", "{dst}"]
exec = ["node-cli", "exec", "{node}"]
stop = ["node-cli", "stop", "{node}"]
`

func writeRemote(t *testing.T, root, id, body string) {
	t.Helper()
	dir := filepath.Join(root, RemotesDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, RemoteFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteDefinitionProblems(t *testing.T) {
	root := t.TempDir()
	if errs := RemoteDefinitionProblems(root); len(errs) != 0 {
		t.Fatalf("no remotes dir: %v", errs)
	}
	writeRemote(t, root, "good", validCommandRemote)
	if err := os.MkdirAll(filepath.Join(root, RemotesDir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if errs := RemoteDefinitionProblems(root); len(errs) != 0 {
		t.Fatalf("a valid tree: %v", errs)
	}
	writeRemote(t, root, "bad", strings.Replace(validCommandRemote, `stop = ["node-cli", "stop", "{node}"]`, "", 1))
	errs := RemoteDefinitionProblems(root)
	if len(errs) != 1 || !strings.HasPrefix(errs[0].Error(), "remotes/bad/remote.toml: connect.stop: required") {
		t.Fatalf("problems = %v", errs)
	}
}
