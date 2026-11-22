package provider

import (
	"context"
	"fmt"
	"io"

	"github.com/bobmcallan/satelle/internal/remotedef"
)

const (
	EnvNodeID    = "SATELLE_NODE_ID"
	EnvNodeToken = "SATELLE_NODE_TOKEN"
	EnvServer    = "SATELLE_SERVER"
)

type Run struct {
	NodeID, Token, Server string
	Binary                string
	Join                  bool
	NoSetup               bool
}

type Handle interface {
	ID() string
	Exec(ctx context.Context, argv ...string) ([]byte, error)
	CopyIn(ctx context.Context, host, machine string) error
	CopyOut(ctx context.Context, machine, host string) error
	Logs(ctx context.Context) ([]byte, error)
	Stop(ctx context.Context) error
}

type StdinExec interface {
	ExecStdin(ctx context.Context, stdin io.Reader, argv ...string) ([]byte, error)
}

type Provider interface {
	Name() remotedef.Provider
	Start(ctx context.Context, def remotedef.Definition, run Run) (Handle, error)
	Hold(ctx context.Context, name string, out io.Writer) (release func())
	SetupCommands() []string
}

func For(name remotedef.Provider) (Provider, error) {
	switch name {
	case remotedef.ProviderDockerLocal:
		return DockerLocal{}, nil
	case remotedef.ProviderFlySprite:
		return FlySprite{}, nil
	}
	return nil, fmt.Errorf("provider %q has no adapter", name)
}
