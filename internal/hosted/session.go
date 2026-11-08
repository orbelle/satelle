package hosted

// Non-interactive sessions (sty_6ed6318d). A session with no browser — a cloud
// session, a CI job, a headless box — acts as the signed-in user with a
// server-minted, scoped, expiring, revocable token (satelle-server
// POST /api/v1/me/tokens) supplied in SATELLE_TOKEN. The token replaces the
// stored login credential for every hosted client; it is never refreshed and
// never persisted.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
)

// SessionTokenEnv is the one environment variable that carries a session token.
// hosted is the only package that names it.
const SessionTokenEnv = "SATELLE_TOKEN"

// sessionTokenType marks a Credential as the session token rather than a stored
// OAuth login (whose token_type is whatever the server's token response says).
const sessionTokenType = "session"

// SessionToken returns the session token from the environment, or "".
func SessionToken() string { return strings.TrimSpace(os.Getenv(SessionTokenEnv)) }

// IsSession reports whether the credential is the environment session token.
func (c Credential) IsSession() bool { return c.TokenType == sessionTokenType }

// SessionFingerprint is the one-way identifier a session credential is cached
// under: the hex SHA-256 of the token, so the token itself is never on disk.
// Empty for a non-session credential.
func SessionFingerprint(c Credential) string {
	if !c.IsSession() || c.AccessToken == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(c.AccessToken))
	return hex.EncodeToString(sum[:])
}

// sessionRefusedError is the session-token refusal. It matches
// ErrLoginRequired (the caller has no usable credential) but its message names
// the token, so a caller is never told to run a browser login it cannot do.
type sessionRefusedError struct{}

func (sessionRefusedError) Error() string {
	return "session token refused (expired, revoked, or a route the token does not cover) — mint a new one (Account → Sessions)"
}

func (sessionRefusedError) Is(target error) bool { return target == ErrLoginRequired }

// ErrSessionTokenRefused is returned at once when the server answers 401 /
// Unauthenticated to a session token: it is never refreshed.
var ErrSessionTokenRefused error = sessionRefusedError{}

// nodeTokenType marks a Credential as a remote node's token: the bearer the
// launcher handed `satelle node join`, accepted on Sync and Chain for the node's
// project and life. Like the session token it is never refreshed or persisted.
const nodeTokenType = "node"

// IsNode reports whether the credential is a remote node's token.
func (c Credential) IsNode() bool { return c.TokenType == nodeTokenType }

// ErrNodeTokenRefused is returned at once when the server answers
// Unauthenticated to a node token: the node has ended or was killed. It is
// never refreshed, and it does not match ErrLoginRequired, since a node cannot
// log in.
var ErrNodeTokenRefused = errors.New("hosted: the node token was refused: the node has ended or was killed")

// NodeTokenStore is the Store of a remote node: it serves the node token for
// Server and nothing else. Save and Delete are no-ops, so no refresh or stamp
// path can persist the token.
type NodeTokenStore struct {
	Server, Token string
}

// Load returns the node credential for Server; any other server has none.
func (s NodeTokenStore) Load(serverURL string) (Credential, error) {
	server := normalizeServerURL(serverURL)
	if s.Token == "" || server != normalizeServerURL(s.Server) {
		return Credential{}, ErrNoCredential
	}
	return Credential{ServerURL: server, AccessToken: s.Token, TokenType: nodeTokenType}, nil
}

// Save is a no-op: a node token is never persisted.
func (NodeTokenStore) Save(Credential) error { return nil }

// Delete is a no-op: there is nothing stored.
func (NodeTokenStore) Delete(string) error { return nil }

// EnvStore is the default Store: SATELLE_TOKEN when set, else the per-user file
// store. While the session token is active Save and Delete are no-ops, so a
// stamp or refresh path can never persist it.
type EnvStore struct {
	File FileStore
}

// DefaultStore is the store every hosted client the CLI builds uses, other than
// login and logout, which manage the file credential directly.
func DefaultStore() Store { return EnvStore{} }

// Load returns the session credential when SATELLE_TOKEN is set, else the file
// credential.
func (s EnvStore) Load(serverURL string) (Credential, error) {
	if tok := SessionToken(); tok != "" {
		return Credential{ServerURL: normalizeServerURL(serverURL), AccessToken: tok, TokenType: sessionTokenType}, nil
	}
	return s.File.Load(serverURL)
}

// Save delegates to the file store unless a session token is active.
func (s EnvStore) Save(cred Credential) error {
	if SessionToken() != "" {
		return nil
	}
	return s.File.Save(cred)
}

// Delete delegates to the file store unless a session token is active.
func (s EnvStore) Delete(serverURL string) error {
	if SessionToken() != "" {
		return nil
	}
	return s.File.Delete(serverURL)
}

// SignedIn reports whether this machine holds a credential for server:
// SATELLE_TOKEN, else a stored login (satelle login). It reads only local
// state and makes no network call; an unreadable credentials file is not
// signed in.
func SignedIn(server string) bool {
	c, err := DefaultStore().Load(server)
	return err == nil && c.AccessToken != ""
}
