package provider

import (
	"context"
	"errors"
	"io"

	"github.com/bobmcallan/satelle/internal/remotedef"
)

// ErrServerStarted: a fly-sprite machine is started by satelle-server, which
// holds the Sprites credential; fire the remote instead.
var ErrServerStarted = errors.New("a fly-sprite node is started by satelle-server: fire the remote (satelle remote fire)")

// piLink puts the pi the definition installed ahead of a sprite base image's
// own: the image puts its pi first on PATH at ~/.pi/agent/bin, so the link
// there names the installed one. Nothing happens when no pi was installed.
const piLink = `if [ -x "$HOME/.local/bin/pi" ]; then mkdir -p "$HOME/.pi/agent/bin" && ln -sf "$(readlink -f "$HOME/.local/bin/pi")" "$HOME/.pi/agent/bin/pi"; fi`

// FlySprite is the fly-sprite adapter: the node side of a sprite.
type FlySprite struct{}

// Name is fly-sprite.
func (FlySprite) Name() remotedef.Provider { return remotedef.ProviderFlySprite }

// Start refuses: satelle-server starts sprites.
func (FlySprite) Start(context.Context, remotedef.Definition, Run) (Handle, error) {
	return nil, ErrServerStarted
}

// Hold keeps the sprite awake while work is in flight: a sprite counts only
// inbound traffic and exec sessions as activity, so a node busy with outbound
// work would be paused mid-run (SpriteHold).
func (FlySprite) Hold(ctx context.Context, name string, out io.Writer) func() {
	return SpriteHold{Name: name, Out: out}.Start(ctx)
}

// SetupCommands link the installed pi ahead of the base image's.
func (FlySprite) SetupCommands() []string { return []string{piLink} }
