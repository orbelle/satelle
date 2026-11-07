// Package provider is the seam between a remote definition and the machine a
// node runs on. A provider starts the definition's image as a node (Start,
// with a Handle to exec in it, read its logs and stop it) and, on the node,
// holds whatever the platform needs while work is in flight (Hold) and adds
// its own setup (SetupCommands). A provider's quirks live here, never in a
// definition.
//
// docker-local is the reference: the definition runs as a local container,
// inspectable with `docker exec`. fly-sprite is a thin adapter: satelle-server
// starts the sprite, and the adapter keeps it awake on the node.
package provider

import (
	"context"
	"fmt"
	"io"

	"github.com/bobmcallan/satelle/internal/remotedef"
)

// The environment a started node reads (`satelle node join` defaults).
const (
	EnvNodeID    = "SATELLE_NODE_ID"
	EnvNodeToken = "SATELLE_NODE_TOKEN"
	EnvServer    = "SATELLE_SERVER"
)

// Run is one node to start.
type Run struct {
	NodeID, Token, Server string
	// Binary, when set, is a satelle binary made available in the machine as
	// `satelle`; empty relies on the image's own.
	Binary string
	// Join starts `satelle node join` in the machine once setup is done.
	Join bool
}

// Handle is a started node.
type Handle interface {
	// ID names the machine (a container name, a sprite name).
	ID() string
	// Exec runs argv in the machine and returns its combined output.
	Exec(ctx context.Context, argv ...string) ([]byte, error)
	// CopyIn copies host, a file or a directory, to the path machine on the
	// machine; CopyOut copies the other way.
	CopyIn(ctx context.Context, host, machine string) error
	CopyOut(ctx context.Context, machine, host string) error
	// Logs returns the machine's own output so far.
	Logs(ctx context.Context) ([]byte, error)
	// Stop stops and removes the machine.
	Stop(ctx context.Context) error
}

// Provider starts a definition's machine and serves the node's platform needs.
type Provider interface {
	Name() remotedef.Provider
	// Start starts a machine for run from def: its image, environment, network
	// rules and setup. A setup failure returns the handle with the error, so
	// the machine can be inspected.
	Start(ctx context.Context, def remotedef.Definition, run Run) (Handle, error)
	// Hold keeps the machine awake while work is in flight, on the node; the
	// release ends it. Failures are written to out and never fail the work.
	Hold(ctx context.Context, name string, out io.Writer) (release func())
	// SetupCommands are run on the node after the definition's on_create.
	SetupCommands() []string
}

// For returns the provider a definition names.
func For(name remotedef.Provider) (Provider, error) {
	switch name {
	case remotedef.ProviderDockerLocal:
		return DockerLocal{}, nil
	case remotedef.ProviderFlySprite:
		return FlySprite{}, nil
	}
	return nil, fmt.Errorf("provider %q has no adapter", name)
}
