package hosted

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

var ErrNoCredential = errors.New("hosted: no stored credential (run \"satelle login\")")

type Credential struct {
	ServerURL     string `toml:"server_url"`
	AccessToken   string `toml:"access_token"`
	RefreshToken  string `toml:"refresh_token"`
	TokenType     string `toml:"token_type,omitempty"`
	Scope         string `toml:"scope,omitempty"`
	DisplayName   string `toml:"display_name,omitempty"`
	Email         string `toml:"email,omitempty"`
	PrincipalID   string `toml:"principal_id,omitempty"`
	ExpiresAt     string `toml:"expires_at,omitempty"`
	CreatedAt     string `toml:"created_at,omitempty"`
	TokenEndpoint string `toml:"token_endpoint,omitempty"`
	ClientID      string `toml:"client_id,omitempty"`
	Resource      string `toml:"resource,omitempty"`
}

func (c Credential) SessionID() string {
	parts := strings.Split(c.AccessToken, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return ""
	}
	var claims struct {
		Sid string `json:"sid"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	return claims.Sid
}

type credentialsFile struct {
	Credential []Credential `toml:"credential"`
}

type FileStore struct {
	Path string
}

type Store interface {
	Load(serverURL string) (Credential, error)
	Save(cred Credential) error
	Delete(serverURL string) error
}

func CredentialsPath() (string, error) {
	p, err := resolveCredentialsPath()
	if err != nil {
		return "", err
	}
	if testing.Testing() {
		guardTestPath(p)
	}
	return p, nil
}

func resolveCredentialsPath() (string, error) {
	if x := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); x != "" {
		return filepath.Join(x, "satelle", "credentials.toml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("hosted: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".config", "satelle", "credentials.toml"), nil
}

func guardTestPath(p string) {
	roots := []string{os.TempDir()}
	if h := strings.TrimSpace(os.Getenv("SATELLE_HOME")); h != "" {
		roots = append(roots, h)
	}
	rp := resolveExisting(p)
	for _, r := range roots {
		if pathUnder(rp, resolveExisting(r)) {
			return
		}
	}
	panic(fmt.Sprintf("hosted: test resolved credentials path %s outside the temp dir/SATELLE_HOME — isolate XDG_CONFIG_HOME (testutil.IsolateHome); refusing to touch the operator's credentials", p))
}

func resolveExisting(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for cur := p; ; {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

func pathUnder(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (s FileStore) path() (string, error) {
	if strings.TrimSpace(s.Path) != "" {
		return s.Path, nil
	}
	return CredentialsPath()
}

func normalizeServerURL(u string) string {
	return strings.TrimRight(strings.TrimSpace(u), "/")
}

func (s FileStore) read() (credentialsFile, string, error) {
	path, err := s.path()
	if err != nil {
		return credentialsFile{}, "", err
	}
	var cf credentialsFile
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return credentialsFile{}, path, nil
		}
		return credentialsFile{}, path, fmt.Errorf("hosted: read %s: %w", path, err)
	}
	if err := toml.Unmarshal(b, &cf); err != nil {
		return credentialsFile{}, path, fmt.Errorf("hosted: parse %s: %w", path, err)
	}
	return cf, path, nil
}

func (s FileStore) Load(serverURL string) (Credential, error) {
	cf, _, err := s.read()
	if err != nil {
		return Credential{}, err
	}
	want := normalizeServerURL(serverURL)
	for _, c := range cf.Credential {
		if normalizeServerURL(c.ServerURL) == want {
			return c, nil
		}
	}
	return Credential{}, ErrNoCredential
}

func (s FileStore) Save(cred Credential) error {
	cred.ServerURL = normalizeServerURL(cred.ServerURL)
	if cred.ServerURL == "" {
		return fmt.Errorf("hosted: credential has no server_url")
	}
	if cred.AccessToken == "" || cred.RefreshToken == "" {
		return fmt.Errorf("hosted: credential missing access/refresh token")
	}
	cf, path, err := s.read()
	if err != nil {
		return err
	}
	replaced := false
	for i := range cf.Credential {
		if normalizeServerURL(cf.Credential[i].ServerURL) == cred.ServerURL {
			cf.Credential[i] = cred
			replaced = true
			break
		}
	}
	if !replaced {
		cf.Credential = append(cf.Credential, cred)
	}
	return writeCredentials(path, cf)
}

func (s FileStore) Delete(serverURL string) error {
	cf, path, err := s.read()
	if err != nil {
		return err
	}
	want := normalizeServerURL(serverURL)
	kept := cf.Credential[:0]
	for _, c := range cf.Credential {
		if normalizeServerURL(c.ServerURL) != want {
			kept = append(kept, c)
		}
	}
	cf.Credential = kept
	return writeCredentials(path, cf)
}

func (s FileStore) PruneLoopback() ([]string, error) {
	cf, path, err := s.read()
	if err != nil {
		return nil, err
	}
	var removed []string
	kept := cf.Credential[:0]
	for _, c := range cf.Credential {
		if isLoopbackURL(c.ServerURL) && strings.TrimSpace(c.CreatedAt) == "" && strings.TrimSpace(c.Email) == "" {
			removed = append(removed, c.ServerURL)
			continue
		}
		kept = append(kept, c)
	}
	if len(removed) == 0 {
		return nil, nil
	}
	cf.Credential = kept
	if err := writeCredentials(path, cf); err != nil {
		return nil, err
	}
	return removed, nil
}

func isLoopbackURL(raw string) bool {
	u, err := url.Parse(normalizeServerURL(raw))
	if err != nil {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func writeCredentials(path string, cf credentialsFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("hosted: create %s: %w", filepath.Dir(path), err)
	}
	var sb strings.Builder
	if err := toml.NewEncoder(&sb).Encode(cf); err != nil {
		return fmt.Errorf("hosted: encode credentials: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(sb.String()), 0o600); err != nil {
		return fmt.Errorf("hosted: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("hosted: rename %s: %w", path, err)
	}
	return nil
}
