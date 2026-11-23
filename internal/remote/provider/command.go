package provider

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bobmcallan/satelle/internal/remotedef"
)

const commandTail = 2000

type Command struct{}

func (Command) Name() remotedef.Provider { return remotedef.ProviderCommand }

func (Command) Hold(context.Context, string, io.Writer) func() { return func() {} }

func (Command) SetupCommands() []string { return nil }

func (Command) Check(ctx context.Context, def remotedef.Definition, node string) error {
	if len(def.Connect.Check) == 0 {
		return nil
	}
	_, err := runConnect(ctx, "check", def.Connect.Check, nil, connectVars(node, "", ""), nil, false)
	return err
}

func (Command) Node(def remotedef.Definition, node string) Handle {
	return &commandNode{c: def.Connect, node: node}
}

func (Command) Start(ctx context.Context, def remotedef.Definition, run Run) (Handle, error) {
	n := &commandNode{c: def.Connect, node: run.NodeID}
	if _, err := runConnect(ctx, "start", def.Connect.Start, nil, n.vars("", ""), nil, false); err != nil {
		return n, err
	}
	return n, nil
}

type commandNode struct {
	c    remotedef.Connect
	node string
}

func (n *commandNode) vars(src, dst string) *strings.Replacer { return connectVars(n.node, src, dst) }

func (n *commandNode) ID() string { return n.node }

func (n *commandNode) Exec(ctx context.Context, argv ...string) ([]byte, error) {
	return runConnect(ctx, "exec", n.c.Exec, argv, n.vars("", ""), nil, false)
}

func (n *commandNode) ExecStdin(ctx context.Context, stdin io.Reader, argv ...string) ([]byte, error) {
	return runConnect(ctx, "exec", n.c.Exec, argv, n.vars("", ""), stdin, false)
}

func (n *commandNode) CopyIn(ctx context.Context, host, machine string) error {
	_, err := runConnect(ctx, "copy_in", n.c.CopyIn, nil, n.vars(host, machine), nil, false)
	return err
}

func (n *commandNode) CopyOut(ctx context.Context, machine, host string) error {
	base := path.Base(machine)
	out, err := runConnect(ctx, "exec", n.c.Exec, []string{"tar", "-C", path.Dir(machine), "-cf", "-", base}, n.vars("", ""), nil, true)
	if err != nil {
		return err
	}
	return untar(bytes.NewReader(out), base, host)
}

func (n *commandNode) Logs(context.Context) ([]byte, error) { return nil, nil }

func (n *commandNode) Stop(ctx context.Context) error {
	_, err := runConnect(ctx, "stop", n.c.Stop, nil, n.vars("", ""), nil, false)
	return err
}

func (n *commandNode) RemoveHint() string { return renderArgv(substitute(n.c.Stop, n.vars("", ""))) }

func connectVars(node, src, dst string) *strings.Replacer {
	return strings.NewReplacer(remotedef.PlaceholderNode, node, remotedef.PlaceholderSrc, src, remotedef.PlaceholderDst, dst)
}

func substitute(argv []string, r *strings.Replacer) []string {
	out := make([]string, len(argv))
	for i, a := range argv {
		out[i] = r.Replace(a)
	}
	return out
}

func renderArgv(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		if a == "" || strings.ContainsAny(a, " \t\n'\"\\$`") {
			a = strconv.Quote(a)
		}
		parts[i] = a
	}
	return strings.Join(parts, " ")
}

func runConnect(ctx context.Context, key string, template, extra []string, vars *strings.Replacer, stdin io.Reader, stdoutOnly bool) ([]byte, error) {
	if len(template) == 0 || strings.TrimSpace(template[0]) == "" {
		return nil, fmt.Errorf("[connect] %s names no command", key)
	}
	argv := append(substitute(template, vars), extra...)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = os.Environ()
	cmd.Stdin = stdin
	var stdout, stderr, combined bytes.Buffer
	if stdoutOnly {
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
	} else {
		cmd.Stdout, cmd.Stderr = &combined, &combined
	}
	if err := cmd.Run(); err != nil {
		tail := combined.String()
		if stdoutOnly {
			tail = stderr.String()
		}
		msg := fmt.Sprintf("[connect] %s %s failed: %v", key, renderArgv(argv), err)
		if t := strings.TrimSpace(tailOf(tail, commandTail)); t != "" {
			msg += "\n" + t
		}
		return combined.Bytes(), errors.New(msg)
	}
	if stdoutOnly {
		return stdout.Bytes(), nil
	}
	return combined.Bytes(), nil
}

func untar(r io.Reader, base, host string) error {
	tr := tar.NewReader(r)
	wrote := false
	for {
		hd, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("copying %s out of the node: %w", base, err)
		}
		name := strings.TrimSuffix(strings.TrimPrefix(hd.Name, "./"), "/")
		clean := path.Clean(name)
		if strings.Contains(name, `\`) || path.IsAbs(name) || clean != name || clean == ".." || strings.HasPrefix(clean, "../") {
			return fmt.Errorf("copying %s out of the node: the archive holds %q, outside %s", base, hd.Name, base)
		}
		rel, ok := strings.CutPrefix(clean, base)
		if !ok || rel != "" && (!strings.HasPrefix(rel, "/") || !filepath.IsLocal(filepath.FromSlash(rel[1:]))) {
			return fmt.Errorf("copying %s out of the node: the archive holds %q, outside %s", base, hd.Name, base)
		}
		dst := host + filepath.FromSlash(rel)
		switch hd.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			_, cerr := io.Copy(f, tr)
			if err := errors.Join(cerr, f.Close()); err != nil {
				return err
			}
		default:
			return fmt.Errorf("copying %s out of the node: %q is neither a file nor a directory", base, hd.Name)
		}
		wrote = true
	}
	if !wrote {
		return fmt.Errorf("copying %s out of the node: the archive is empty", base)
	}
	return nil
}
