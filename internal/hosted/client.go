package hosted

// The client half of the wire with satelle-server. Every call is gRPC (the
// services in api/satelle); only OAuth's browser authorize, discovery and token
// endpoints stay HTTP (oauth.go, discovery.go). The bearer comes from the
// credential store through per-RPC credentials, and an UNAUTHENTICATED answer
// to a stored login is refreshed once through the OAuth token endpoint and the
// call retried.

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/bobmcallan/satelle/internal/wire/chainpb"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

// DefaultServer is the hosted server used when none is configured.
const DefaultServer = "https://satelle.dev"

// ErrLoginRequired is returned when there is no usable credential: no stored
// tokens, or a refresh that failed.
var ErrLoginRequired = errors.New("not signed in — run \"satelle login\"")

// MaxMessageBytes caps each unary message in both directions.
const MaxMessageBytes = 64 << 20

// unknownSubjectCode prefixes the UNAUTHENTICATED message the server answers
// when the token is valid but its subject has no account yet.
const unknownSubjectCode = "unknown_subject"

// UnknownSubjectError is the server's refusal of a valid token whose subject
// has no account yet. It is final: refreshing cannot help.
type UnknownSubjectError struct{ Message string }

func (e *UnknownSubjectError) Error() string { return e.Message }

// Client is a connection to one satelle-server with a stub for every service.
type Client struct {
	server string
	store  Store
	http   *http.Client
	conn   *grpc.ClientConn

	Account     satellepb.AccountServiceClient
	Projects    satellepb.ProjectServiceClient
	Workspaces  satellepb.WorkspaceServiceClient
	Locations   satellepb.LocationServiceClient
	Items       satellepb.ItemServiceClient
	Attachments satellepb.AttachmentServiceClient
	Remotes     satellepb.RemoteServiceClient
	Nodes       satellepb.NodeServiceClient
	Relay       satellepb.RelayServiceClient
	Chain       chainpb.ChainClient
}

// Options tune Dial; the zero value dials the server's own address.
type Options struct {
	// HTTP is the client for the OAuth token endpoint (refresh). Nil uses a
	// 30 second timeout.
	HTTP *http.Client
	// Dialer replaces the network dial (tests: a bufconn listener).
	Dialer func(ctx context.Context, addr string) (net.Conn, error)
}

// Dial opens a client for server with the bearer from store. server is a URL
// (https:// dials TLS on port 443 unless one is given; http:// dials without
// TLS on port 80 unless one is given) or a bare host:port (without TLS when the
// host is loopback). Nothing is sent until the first call.
func Dial(server string, store Store, o Options) (*Client, error) {
	server = normalizeServerURL(server)
	target, secure, err := grpcTarget(server)
	if err != nil {
		return nil, err
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	c := &Client{server: server, store: store, http: o.HTTP}
	var tc credentials.TransportCredentials = insecure.NewCredentials()
	if secure {
		tc = credentials.NewTLS(&tls.Config{ServerName: hostnameOf(target), MinVersion: tls.VersionTLS12})
	}
	dopts := []grpc.DialOption{
		grpc.WithTransportCredentials(tc),
		grpc.WithPerRPCCredentials(bearerCreds{}),
		grpc.WithUnaryInterceptor(c.unaryAuth),
		grpc.WithStreamInterceptor(c.streamAuth),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(MaxMessageBytes), grpc.MaxCallSendMsgSize(MaxMessageBytes)),
	}
	if o.Dialer != nil {
		dopts = append(dopts, grpc.WithContextDialer(o.Dialer))
		target = "passthrough:///" + target
	}
	conn, err := grpc.NewClient(target, dopts...)
	if err != nil {
		return nil, fmt.Errorf("hosted: dial %s: %w", target, err)
	}
	c.conn = conn
	c.Account = satellepb.NewAccountServiceClient(conn)
	c.Projects = satellepb.NewProjectServiceClient(conn)
	c.Workspaces = satellepb.NewWorkspaceServiceClient(conn)
	c.Locations = satellepb.NewLocationServiceClient(conn)
	c.Items = satellepb.NewItemServiceClient(conn)
	c.Attachments = satellepb.NewAttachmentServiceClient(conn)
	c.Remotes = satellepb.NewRemoteServiceClient(conn)
	c.Nodes = satellepb.NewNodeServiceClient(conn)
	c.Relay = satellepb.NewRelayServiceClient(conn)
	c.Chain = chainpb.NewChainClient(conn)
	return c, nil
}

// Server is the normalised server URL the client was dialled for.
func (c *Client) Server() string { return c.server }

// Close closes the connection.
func (c *Client) Close() error { return c.conn.Close() }

// grpcTarget maps the server onto a dial address and whether it uses TLS.
func grpcTarget(server string) (addr string, secure bool, err error) {
	if !strings.Contains(server, "://") {
		host, _, err := net.SplitHostPort(server)
		if err != nil || host == "" {
			return "", false, fmt.Errorf("hosted: invalid server %q", server)
		}
		return server, !isLoopbackHost(host), nil
	}
	u, err := url.Parse(server)
	if err != nil || strings.TrimSpace(u.Hostname()) == "" {
		return "", false, fmt.Errorf("hosted: invalid server %q", server)
	}
	port := u.Port()
	switch strings.ToLower(u.Scheme) {
	case "https":
		if port == "" {
			port = "443"
		}
		return net.JoinHostPort(u.Hostname(), port), true, nil
	case "http":
		if port == "" {
			port = "80"
		}
		return net.JoinHostPort(u.Hostname(), port), false, nil
	}
	return "", false, fmt.Errorf("hosted: unsupported scheme %q in %q", u.Scheme, server)
}

func isLoopbackHost(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func hostnameOf(addr string) string {
	h, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return h
}

// bearerKey carries the access token from the interceptor to bearerCreds.
type bearerKey struct{}

// bearerCreds is the per-RPC credential: the token the interceptor chose for
// this attempt.
type bearerCreds struct{}

func (bearerCreds) GetRequestMetadata(ctx context.Context, _ ...string) (map[string]string, error) {
	tok, _ := ctx.Value(bearerKey{}).(string)
	if tok == "" {
		return nil, nil
	}
	return map[string]string{"authorization": "Bearer " + tok}, nil
}

// RequireTransportSecurity is false so a loopback test server needs no TLS;
// Dial chooses TLS for every non-loopback https target.
func (bearerCreds) RequireTransportSecurity() bool { return false }

// credential loads the stored credential; none is ErrLoginRequired.
func (c *Client) credential() (Credential, error) {
	cred, err := c.store.Load(c.server)
	if errors.Is(err, ErrNoCredential) {
		return Credential{}, ErrLoginRequired
	}
	return cred, err
}

func (c *Client) unaryAuth(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	cred, err := c.credential()
	if err != nil {
		return err
	}
	err = invoker(context.WithValue(ctx, bearerKey{}, cred.AccessToken), method, req, reply, cc, opts...)
	if status.Code(err) != codes.Unauthenticated {
		return err
	}
	if refusal := c.refusal(cred, err); refusal != nil {
		return refusal
	}
	rotated, rErr := c.refreshAndPersist(ctx, cred)
	if rErr != nil {
		return rErr
	}
	err = invoker(context.WithValue(ctx, bearerKey{}, rotated.AccessToken), method, req, reply, cc, opts...)
	if status.Code(err) == codes.Unauthenticated {
		return fmt.Errorf("%w (the refreshed token was refused too)", ErrLoginRequired)
	}
	return err
}

// streamAuth attaches the bearer to a stream. A stream is not retried: its
// refusal arrives with its first message, after the call has returned.
func (c *Client) streamAuth(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	cred, err := c.credential()
	if err != nil {
		return nil, err
	}
	return streamer(context.WithValue(ctx, bearerKey{}, cred.AccessToken), desc, cc, method, opts...)
}

// refusal is the final reading of an UNAUTHENTICATED answer, or nil when a
// refresh may help.
func (c *Client) refusal(cred Credential, err error) error {
	msg := status.Convert(err).Message()
	if rest, ok := strings.CutPrefix(msg, unknownSubjectCode+": "); ok {
		return &UnknownSubjectError{Message: strings.TrimSpace(rest)}
	}
	if cred.IsSession() {
		return ErrSessionTokenRefused
	}
	if cred.IsNode() {
		return ErrNodeTokenRefused
	}
	return nil
}

// refreshAndPersist runs the refresh_token grant against the token endpoint
// stored with the credential and persists the rotated pair before the retry.
func (c *Client) refreshAndPersist(ctx context.Context, cred Credential) (Credential, error) {
	if cred.TokenEndpoint == "" {
		return Credential{}, fmt.Errorf("%w (stored credential has no token endpoint)", ErrLoginRequired)
	}
	tok, err := refreshGrant(ctx, c.http, cred)
	if err != nil {
		return Credential{}, fmt.Errorf("%w (refresh failed: %v)", ErrLoginRequired, err)
	}
	rotated := credentialFromToken(c.server, tok)
	rotated.DisplayName, rotated.Email, rotated.PrincipalID = cred.DisplayName, cred.Email, cred.PrincipalID
	rotated.TokenEndpoint, rotated.ClientID, rotated.Resource = cred.TokenEndpoint, cred.ClientID, cred.Resource
	if err := c.store.Save(rotated); err != nil {
		return Credential{}, fmt.Errorf("hosted: persist refreshed credential: %w", err)
	}
	return rotated, nil
}

// IsNotFound reports a NOT_FOUND answer.
func IsNotFound(err error) bool { return status.Code(err) == codes.NotFound }

// Describe turns a call's error into a message for the operator: a gRPC status
// becomes "<op>: <message>", anything else is wrapped as is.
func Describe(op string, err error) error {
	if err == nil {
		return nil
	}
	var us *UnknownSubjectError
	if errors.Is(err, ErrLoginRequired) || errors.Is(err, ErrNodeTokenRefused) || errors.As(err, &us) {
		return err
	}
	if st, ok := status.FromError(err); ok {
		return fmt.Errorf("%s: %s (%s)", op, st.Message(), st.Code())
	}
	return fmt.Errorf("%s: %w", op, err)
}

// Normalize is the form a server URL is keyed by in the credential store.
func Normalize(server string) string { return normalizeServerURL(server) }
