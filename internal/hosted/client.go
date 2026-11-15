package hosted

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

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/wire/chainpb"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

const DefaultServer = "https://satelle.dev"

func ConfiguredServer(gc config.GlobalConfig) string {
	if s := gc.Hosted.ResolveServer(); s != "" {
		return s
	}
	return DefaultServer
}

var ErrLoginRequired = errors.New("not signed in — run \"satelle login\"")

const MaxMessageBytes = 64 << 20

const unknownSubjectCode = "unknown_subject"

type UnknownSubjectError struct{ Message string }

func (e *UnknownSubjectError) Error() string { return e.Message }

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
	GitHub      satellepb.GitHubServiceClient
	Chain       chainpb.ChainClient
}

type Options struct {
	HTTP   *http.Client
	Dialer func(ctx context.Context, addr string) (net.Conn, error)
}

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
	c.GitHub = satellepb.NewGitHubServiceClient(conn)
	c.Chain = chainpb.NewChainClient(conn)
	return c, nil
}

func (c *Client) Server() string { return c.server }

func (c *Client) Close() error { return c.conn.Close() }

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

type bearerKey struct{}

type bearerCreds struct{}

func (bearerCreds) GetRequestMetadata(ctx context.Context, _ ...string) (map[string]string, error) {
	tok, _ := ctx.Value(bearerKey{}).(string)
	if tok == "" {
		return nil, nil
	}
	return map[string]string{"authorization": "Bearer " + tok}, nil
}

func (bearerCreds) RequireTransportSecurity() bool { return false }

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

func (c *Client) streamAuth(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	cred, err := c.credential()
	if err != nil {
		return nil, err
	}
	return streamer(context.WithValue(ctx, bearerKey{}, cred.AccessToken), desc, cc, method, opts...)
}

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

func IsNotFound(err error) bool { return status.Code(err) == codes.NotFound }

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

func Normalize(server string) string { return normalizeServerURL(server) }
