package remotedef

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// EnvSecretsFile is the environment variable a node's join sets, for the life
// of the fire, to the path of its secrets hand-off (WriteHandoff). A satelle a
// command of the run starts reads the hand-off from it (LoadHandoff), falling
// back to HandoffPath when the variable did not reach it. It carries a path,
// never a value.
const EnvSecretsFile = "SATELLE_NODE_SECRETS_FILE"

// handoff is the hand-off file's shape: the values join placed in its process
// environment, and the join that placed them, so a file a crashed join left is
// not read.
type handoff struct {
	PID     int               `json:"pid"`
	Secrets map[string]string `json:"secrets"`
}

// HandoffPath is where join writes its secrets hand-off: under the user's cache
// dir, outside any repo and node root, and not under TMPDIR, which a performer
// dispatch replaces with its own scratch dir.
func HandoffPath() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "satelle", "node", "handoff.json"), nil
}

// WriteHandoff writes the secrets join placed in its process environment for
// the fire to HandoffPath, readable by this user alone (dir 0700, file 0600),
// so a satelle started below a harness that does not pass those variables to
// its children still gets them (LoadHandoff). It returns the path written.
func WriteHandoff(secrets map[string]string) (string, error) {
	path, err := HandoffPath()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	b, err := json.Marshal(handoff{PID: os.Getpid(), Secrets: secrets})
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".handoff-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return "", err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}

// RemoveHandoff removes the hand-off at path; a missing file is not an error.
func RemoveHandoff(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// LoadHandoff reads the secrets a running join handed off: the file
// EnvSecretsFile names, else HandoffPath. It is read only when it is a regular
// file this user owns with mode 0600 and the join that wrote it is still a
// live process of this user's; anything else (no file, another owner, a wider
// mode, a join that has gone) is nil, so a run off a node is unchanged.
func LoadHandoff() map[string]string {
	path := os.Getenv(EnvSecretsFile)
	if path == "" {
		p, err := HandoffPath()
		if err != nil {
			return nil
		}
		path = p
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || !privateToMe(info) {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var h handoff
	if json.Unmarshal(b, &h) != nil || len(h.Secrets) == 0 {
		return nil
	}
	if !joinAlive(h.PID) {
		return nil
	}
	return h.Secrets
}
