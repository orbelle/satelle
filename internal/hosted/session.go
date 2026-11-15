package hosted

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
)

const SessionTokenEnv = "SATELLE_TOKEN"

const sessionTokenType = "session"

func SessionToken() string { return strings.TrimSpace(os.Getenv(SessionTokenEnv)) }

func (c Credential) IsSession() bool { return c.TokenType == sessionTokenType }

func SessionFingerprint(c Credential) string {
	if !c.IsSession() || c.AccessToken == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(c.AccessToken))
	return hex.EncodeToString(sum[:])
}

type sessionRefusedError struct{}

func (sessionRefusedError) Error() string {
	return "session token refused (expired, revoked, or a route the token does not cover) — mint a new one (Account → Sessions)"
}

func (sessionRefusedError) Is(target error) bool { return target == ErrLoginRequired }

var ErrSessionTokenRefused error = sessionRefusedError{}

const nodeTokenType = "node"

func (c Credential) IsNode() bool { return c.TokenType == nodeTokenType }

var ErrNodeTokenRefused = errors.New("hosted: the node token was refused: the node has ended or was killed")

type NodeTokenStore struct {
	Server, Token string
}

func (s NodeTokenStore) Load(serverURL string) (Credential, error) {
	server := normalizeServerURL(serverURL)
	if s.Token == "" || server != normalizeServerURL(s.Server) {
		return Credential{}, ErrNoCredential
	}
	return Credential{ServerURL: server, AccessToken: s.Token, TokenType: nodeTokenType}, nil
}

func (NodeTokenStore) Save(Credential) error { return nil }

func (NodeTokenStore) Delete(string) error { return nil }

type EnvStore struct {
	File FileStore
}

func DefaultStore() Store { return EnvStore{} }

func (s EnvStore) Load(serverURL string) (Credential, error) {
	if tok := SessionToken(); tok != "" {
		return Credential{ServerURL: normalizeServerURL(serverURL), AccessToken: tok, TokenType: sessionTokenType}, nil
	}
	return s.File.Load(serverURL)
}

func (s EnvStore) Save(cred Credential) error {
	if SessionToken() != "" {
		return nil
	}
	return s.File.Save(cred)
}

func (s EnvStore) Delete(serverURL string) error {
	if SessionToken() != "" {
		return nil
	}
	return s.File.Delete(serverURL)
}

func SignedIn(server string) bool {
	c, err := DefaultStore().Load(server)
	return err == nil && c.AccessToken != ""
}
