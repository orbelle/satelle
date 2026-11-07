package provider

// docker-local, the reference provider. Every docker call in satelle is in this
// file, through the docker CLI.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/bobmcallan/satelle/internal/remotedef"
)

// NodeImage is the node image BuildNodeImage builds from the repo's Dockerfile;
// a definition with no [machine] image runs it.
const NodeImage = "satelle-node:local"

// keepAlive is the container's main process: it idles until stopped, so the
// machine stays inspectable with `docker exec`.
const keepAlive = "trap 'exit 0' TERM INT; while :; do sleep 3600 & wait $!; done"

// DockerLocal runs a definition as a local container.
type DockerLocal struct {
	// Docker is the docker CLI; empty is "docker" on PATH.
	Docker string
}

// Name is docker-local.
func (DockerLocal) Name() remotedef.Provider { return remotedef.ProviderDockerLocal }

func (d DockerLocal) bin() string {
	if d.Docker != "" {
		return d.Docker
	}
	return "docker"
}

// docker runs one docker command; env is added to the CLI's own environment
// (a value passed by name with -e KEY never enters the argv).
func (d DockerLocal) docker(ctx context.Context, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, d.bin(), args...)
	cmd.Env = append(os.Environ(), env...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("docker %s: %w: %s", args[0], err, strings.TrimSpace(tailOf(out.String(), 2000)))
	}
	return out.Bytes(), nil
}

// Available reports whether the docker CLI answers and its daemon is up.
func (d DockerLocal) Available(ctx context.Context) error {
	_, err := d.docker(ctx, nil, "info", "--format", "{{.ServerVersion}}")
	return err
}

// HasImage reports whether image is present locally (nothing is pulled).
func (d DockerLocal) HasImage(ctx context.Context, image string) bool {
	_, err := d.docker(ctx, nil, "image", "inspect", "--format", "{{.Id}}", image)
	return err == nil
}

// BuildNodeImage builds NodeImage from tree's Dockerfile, with tree as the
// build context.
func (d DockerLocal) BuildNodeImage(ctx context.Context, tree string) error {
	_, err := d.docker(ctx, nil, "build", "-t", NodeImage, "-f", filepath.Join(tree, "Dockerfile"), tree)
	return err
}

// Exists reports whether the container name exists (running or not).
func (d DockerLocal) Exists(ctx context.Context, name string) bool {
	_, err := d.docker(ctx, nil, "container", "inspect", "--format", "{{.Id}}", name)
	return err == nil
}

// Start runs the definition's image detached, with the node's identity in its
// environment (the token passed by name, never on the argv), the definition's
// network rules as far as docker can hold them, then runs setup.on_create and
// setup.post_start inside it, and, with run.Join, `satelle node join`.
//
// Network rules are best effort: docker cannot hold a hostname allowlist, so a
// definition that reaches nothing (no allow, no repo, no server) runs with no
// network at all, and any other runs on the default bridge with the allowlist
// in SATELLE_NETWORK_ALLOW for inspection.
func (d DockerLocal) Start(ctx context.Context, def remotedef.Definition, run Run) (Handle, error) {
	image := def.Machine.Image
	if image == "" {
		image = NodeImage
	}
	name := ContainerName(run.NodeID)
	args := []string{"run", "-d", "--name", name, "--label", "satelle.node=" + run.NodeID,
		"-e", EnvNodeID + "=" + run.NodeID, "-e", EnvServer + "=" + run.Server,
		"-e", "SATELLE_NETWORK_ALLOW=" + strings.Join(def.Network.Allow, ",")}
	var env []string
	if run.Token != "" {
		args = append(args, "-e", EnvNodeToken)
		env = append(env, EnvNodeToken+"="+run.Token)
	}
	if len(def.Network.Allow) == 0 && len(def.Repos) == 0 && run.Server == "" {
		args = append(args, "--network", "none")
	}
	if run.Binary != "" {
		args = append(args, "-v", run.Binary+":/usr/local/bin/satelle:ro")
	}
	args = append(args, "--entrypoint", "sh", image, "-c", keepAlive)
	if _, err := d.docker(ctx, env, args...); err != nil {
		return nil, err
	}
	h := &container{d: d, name: name}
	for _, phase := range []struct {
		name string
		cmds []string
	}{{"on_create", def.Setup.OnCreate}, {"post_start", def.Setup.PostStart}} {
		for i, c := range phase.cmds {
			if _, err := h.Exec(ctx, "sh", "-c", c); err != nil {
				return h, fmt.Errorf("setup %s command %d failed in %s: %w", phase.name, i+1, name, err)
			}
		}
	}
	if run.Join {
		if _, err := d.docker(ctx, nil, "exec", "-d", name, "satelle", "node", "join", "--no-setup"); err != nil {
			return h, err
		}
	}
	return h, nil
}

// Hold is a no-op: a local container does not sleep.
func (DockerLocal) Hold(context.Context, string, io.Writer) func() { return func() {} }

// SetupCommands are none.
func (DockerLocal) SetupCommands() []string { return nil }

type container struct {
	d    DockerLocal
	name string
}

func (c *container) ID() string { return c.name }

func (c *container) Exec(ctx context.Context, argv ...string) ([]byte, error) {
	return c.d.docker(ctx, nil, append([]string{"exec", c.name}, argv...)...)
}

// CopyIn copies host (a file or a directory) to machine in the container.
func (c *container) CopyIn(ctx context.Context, host, machine string) error {
	_, err := c.d.docker(ctx, nil, "cp", host, c.name+":"+machine)
	return err
}

// CopyOut copies machine (a file or a directory) in the container to host.
func (c *container) CopyOut(ctx context.Context, machine, host string) error {
	_, err := c.d.docker(ctx, nil, "cp", c.name+":"+machine, host)
	return err
}

func (c *container) Logs(ctx context.Context) ([]byte, error) {
	return c.d.docker(ctx, nil, "logs", c.name)
}

func (c *container) Stop(ctx context.Context) error {
	_, err := c.d.docker(ctx, nil, "rm", "-f", c.name)
	return err
}

// ContainerName is satelle-node-<node id>, reduced to docker's name alphabet;
// a node without an id gets a random suffix.
func ContainerName(nodeID string) string {
	var b strings.Builder
	for _, r := range nodeID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		var n [4]byte
		_, _ = rand.Read(n[:])
		b.WriteString(hex.EncodeToString(n[:]))
	}
	return "satelle-node-" + b.String()
}

func tailOf(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}
