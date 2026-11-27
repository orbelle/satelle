package provider

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/remotedef"
)

const NodeImage = "satelle-node:local"

const keepAlive = "trap 'exit 0' TERM INT; while :; do sleep 3600 & wait $!; done"

const (
	EgressAlias = "satelle-egress"
	EgressPort  = 3128
	egressPath  = "/satelle-egress"
	egressPolls = 20
)

var egressPollGap = 250 * time.Millisecond

type DockerLocal struct {
	Docker string
	Egress string
}

func (DockerLocal) Name() remotedef.Provider { return remotedef.ProviderDockerLocal }

func (d DockerLocal) bin() string {
	if d.Docker != "" {
		return d.Docker
	}
	return "docker"
}

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

func (d DockerLocal) dockerIn(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, d.bin(), args...)
	cmd.Env = os.Environ()
	cmd.Stdin = stdin
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("docker %s: %w", args[0], err)
	}
	return out.Bytes(), nil
}

func (d DockerLocal) dockerOut(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, d.bin(), args...)
	cmd.Env = os.Environ()
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("docker %s: %w: %s", args[0], err, strings.TrimSpace(tailOf(errOut.String(), 2000)))
	}
	return out.Bytes(), nil
}

func (d DockerLocal) Available(ctx context.Context) error {
	_, err := d.docker(ctx, nil, "info", "--format", "{{.ServerVersion}}")
	return err
}

func (d DockerLocal) HasImage(ctx context.Context, image string) bool {
	_, err := d.docker(ctx, nil, "image", "inspect", "--format", "{{.Id}}", image)
	return err == nil
}

func (d DockerLocal) BuildNodeImage(ctx context.Context, tree string) error {
	_, err := d.docker(ctx, nil, "build", "-t", NodeImage, "-f", filepath.Join(tree, "Dockerfile"), tree)
	return err
}

func (d DockerLocal) Architecture(ctx context.Context) (string, error) {
	out, err := d.docker(ctx, nil, "info", "--format", "{{.Architecture}}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (d DockerLocal) networkSubnet(ctx context.Context, network string) (string, error) {
	out, err := d.docker(ctx, nil, "network", "inspect", "--format", "{{(index .IPAM.Config 0).Subnet}}", network)
	if err != nil {
		return "", err
	}
	cidr := strings.TrimSpace(string(out))
	if _, _, err := net.ParseCIDR(cidr); err != nil {
		return "", fmt.Errorf("docker network inspect %s: the subnet %q is not a CIDR", network, cidr)
	}
	return cidr, nil
}

func (d DockerLocal) Exists(ctx context.Context, name string) bool {
	_, err := d.docker(ctx, nil, "container", "inspect", "--format", "{{.Id}}", name)
	return err == nil
}

func (d DockerLocal) Node(def remotedef.Definition, nodeID string, egress bool) Handle {
	suffix := nodeSuffix(nodeID)
	h := &container{d: d, name: "satelle-node-" + suffix}
	if egress {
		h.egress, h.network = "satelle-egress-"+suffix, "satelle-net-"+suffix
	}
	return h
}

func (d DockerLocal) Start(ctx context.Context, def remotedef.Definition, run Run) (Handle, error) {
	image := def.Machine.Image
	if image == "" {
		image = NodeImage
	}
	suffix := nodeSuffix(run.NodeID)
	name := "satelle-node-" + suffix
	h := &container{d: d, name: name}
	args := []string{"run", "-d", "--name", name, "--label", "satelle.node=" + run.NodeID,
		"-e", EnvNodeID + "=" + run.NodeID, "-e", EnvServer + "=" + run.Server}
	var env []string
	if run.Token != "" {
		args = append(args, "-e", EnvNodeToken)
		env = append(env, EnvNodeToken+"="+run.Token)
	}
	if len(def.Network.Allow) == 0 {
		args = append(args, "--network", "none")
	} else {
		if d.Egress == "" {
			return nil, errors.New("[network] allow cannot be enforced here: no satelle binary to serve the [network] allow-list")
		}
		if err := d.startEgress(ctx, h, image, run.NodeID, suffix, def.Network.Allow); err != nil {
			return nil, err
		}
		proxy := fmt.Sprintf("http://%s:%d", EgressAlias, EgressPort)
		args = append(args, "--network", h.network)
		for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
			args = append(args, "-e", k+"="+proxy)
		}
		for _, k := range []string{"NO_PROXY", "no_proxy"} {
			args = append(args, "-e", k+"=localhost,127.0.0.1")
		}
	}
	if run.Binary != "" {
		args = append(args, "-v", run.Binary+":/usr/local/bin/satelle:ro")
	}
	args = append(args, "--entrypoint", "sh", image, "-c", keepAlive)
	if _, err := d.docker(ctx, env, args...); err != nil {
		if h.network != "" {
			return nil, errors.Join(err, h.teardown(context.WithoutCancel(ctx)))
		}
		return nil, err
	}
	phases := []struct {
		name string
		cmds []string
	}{{"on_create", def.Setup.OnCreate}, {"post_start", def.Setup.PostStart}}
	if run.NoSetup {
		phases = nil
	}
	for _, phase := range phases {
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

func (d DockerLocal) startEgress(ctx context.Context, h *container, image, nodeID, suffix string, allow []string) error {
	network, egress := "satelle-net-"+suffix, "satelle-egress-"+suffix
	label := "satelle.node=" + nodeID
	if _, err := d.docker(ctx, nil, "network", "create", "--internal", "--label", label, network); err != nil {
		return err
	}
	h.network = network
	fail := func(err error) error { return errors.Join(err, h.teardown(context.WithoutCancel(ctx))) }
	subnet, err := d.networkSubnet(ctx, network)
	if err != nil {
		return fail(err)
	}
	port := strconv.Itoa(EgressPort)
	if _, err := d.docker(ctx, nil, "create", "--name", egress, "--label", label, "--entrypoint", egressPath, image,
		"node", "egress", "--subnet", subnet, "--port", port, "--allow", strings.Join(allow, ",")); err != nil {
		return fail(err)
	}
	h.egress = egress
	steps := [][]string{
		{"cp", d.Egress, egress + ":" + egressPath},
		{"network", "connect", "--alias", EgressAlias, network, egress},
		{"start", egress},
	}
	for _, args := range steps {
		if _, err := d.docker(ctx, nil, args...); err != nil {
			return fail(err)
		}
	}
	var perr error
	for i := 0; i < egressPolls; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return fail(ctx.Err())
			case <-time.After(egressPollGap):
			}
		}
		if _, perr = d.docker(ctx, nil, "exec", egress, egressPath, "node", "egress", "--ready", "--subnet", subnet, "--port", port); perr == nil {
			return nil
		}
	}
	logs, _ := d.docker(context.WithoutCancel(ctx), nil, "logs", egress)
	return fail(fmt.Errorf("the egress proxy did not start in %s: %s", image, strings.TrimSpace(tailOf(string(logs), 2000))))
}

func (DockerLocal) Hold(context.Context, string, io.Writer) func() { return func() {} }

func (DockerLocal) SetupCommands() []string { return nil }

type container struct {
	d               DockerLocal
	name            string
	egress, network string
}

func (c *container) ID() string { return c.name }

func (c *container) Exec(ctx context.Context, argv ...string) ([]byte, error) {
	return c.d.docker(ctx, nil, append([]string{"exec", c.name}, argv...)...)
}

func (c *container) ExecStdin(ctx context.Context, stdin io.Reader, argv ...string) ([]byte, error) {
	return c.d.dockerIn(ctx, stdin, append([]string{"exec", "-i", c.name}, argv...)...)
}

func (c *container) CopyIn(ctx context.Context, host, machine string) error {
	_, err := c.d.docker(ctx, nil, "cp", host, c.name+":"+machine)
	return err
}

func (c *container) CopyOut(ctx context.Context, machine, host string) error {
	out, err := c.d.dockerOut(ctx, "cp", c.name+":"+machine, "-")
	if err != nil {
		return err
	}
	return untar(bytes.NewReader(out), path.Base(machine), host)
}

func (c *container) Logs(ctx context.Context) ([]byte, error) {
	return c.d.docker(ctx, nil, "logs", c.name)
}

func (c *container) Stop(ctx context.Context) error {
	args := []string{"rm", "-f", c.name}
	if c.egress != "" {
		args = append(args, c.egress)
	}
	_, err := c.d.docker(ctx, nil, args...)
	if c.network != "" {
		_, nerr := c.d.docker(ctx, nil, "network", "rm", c.network)
		err = errors.Join(err, nerr)
	}
	return err
}

func (c *container) teardown(ctx context.Context) error {
	var errs []error
	if _, err := c.d.docker(ctx, nil, "rm", "-f", c.name); err != nil && !strings.Contains(strings.ToLower(err.Error()), "no such container") {
		errs = append(errs, err)
	}
	if c.egress != "" {
		if _, err := c.d.docker(ctx, nil, "rm", "-f", c.egress); err != nil {
			errs = append(errs, err)
		}
	}
	if c.network != "" {
		if _, err := c.d.docker(ctx, nil, "network", "rm", c.network); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func RemoveHint(h Handle) string {
	if hinted, ok := h.(interface{ RemoveHint() string }); ok {
		return hinted.RemoveHint()
	}
	c, ok := h.(*container)
	if !ok {
		return "docker rm -f " + h.ID()
	}
	hint := "docker rm -f " + c.name
	if c.egress != "" {
		hint += " " + c.egress
	}
	if c.network != "" {
		hint += " && docker network rm " + c.network
	}
	return hint
}

func ContainerName(nodeID string) string { return "satelle-node-" + nodeSuffix(nodeID) }

func nodeSuffix(nodeID string) string {
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
	return b.String()
}

func tailOf(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}
