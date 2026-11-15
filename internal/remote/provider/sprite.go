package provider

import (
	"context"
	"errors"
	"io"

	"github.com/bobmcallan/satelle/internal/remotedef"
)

var ErrServerStarted = errors.New("a fly-sprite node is started by satelle-server: fire the remote (satelle remote fire)")

const piLink = `if [ -x "$HOME/.local/bin/pi" ]; then mkdir -p "$HOME/.pi/agent/bin" && ln -sf "$(readlink -f "$HOME/.local/bin/pi")" "$HOME/.pi/agent/bin/pi"; fi`

type FlySprite struct{}

func (FlySprite) Name() remotedef.Provider { return remotedef.ProviderFlySprite }

func (FlySprite) Start(context.Context, remotedef.Definition, Run) (Handle, error) {
	return nil, ErrServerStarted
}

func (FlySprite) Hold(ctx context.Context, name string, out io.Writer) func() {
	return SpriteHold{Name: name, Out: out}.Start(ctx)
}

func (FlySprite) SetupCommands() []string { return []string{piLink} }
