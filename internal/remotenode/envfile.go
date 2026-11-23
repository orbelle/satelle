package remotenode

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bobmcallan/satelle/internal/gitfacts"
)

const secretsFileName = ".satelle/.env"

var secretsFileKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var repoTracks = func(dir, path string) (bool, error) { return gitfacts.Repo{Dir: dir}.Tracks(path) }

func fileSecrets(dataDir string) (map[string]string, error) {
	p := filepath.Join(dataDir, ".env")
	fi, err := os.Lstat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return map[string]string{}, nil
	case err != nil:
		return nil, fmt.Errorf("%s: %w", secretsFileName, err)
	case !fi.Mode().IsRegular():
		return nil, fmt.Errorf("%s is not a regular file", secretsFileName)
	}
	tree := filepath.Dir(dataDir)
	tracked, err := repoTracks(tree, secretsFileName)
	if err != nil {
		return nil, fmt.Errorf("%s: cannot tell whether git tracks it: %w", secretsFileName, err)
	}
	if tracked {
		return nil, fmt.Errorf("%s is tracked by git: a secrets file must be untracked and ignored; no value was read from it", secretsFileName)
	}
	ignored, err := gitfacts.Repo{Dir: tree}.IgnoredBy(secretsFileName)
	if err != nil {
		return nil, fmt.Errorf("%s: cannot tell whether git ignores it: %w", secretsFileName, err)
	}
	if _, ok := ignored[secretsFileName]; !ok {
		return nil, fmt.Errorf("%s is not ignored by git: a secrets file must be untracked and ignored; no value was read from it", secretsFileName)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", secretsFileName, err)
	}
	return parseSecretsFile(b)
}

func parseSecretsFile(b []byte) (map[string]string, error) {
	out := map[string]string{}
	for i, raw := range strings.Split(string(b), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if ok && secretsFileKey.MatchString(k) {
			v, ok = unquote(v)
		} else {
			ok = false
		}
		if !ok {
			return nil, fmt.Errorf("%s line %d is not a blank line, a # comment or KEY=VALUE", secretsFileName, i+1)
		}
		out[k] = v
	}
	return out, nil
}

func unquote(v string) (string, bool) {
	if v == "" || (v[0] != '"' && v[0] != '\'') {
		return v, true
	}
	if len(v) < 2 || v[len(v)-1] != v[0] {
		return "", false
	}
	return v[1 : len(v)-1], true
}
