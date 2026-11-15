package remotenode

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	SetupOnCreate  = "on_create"
	SetupPostStart = "post_start"
)

type SetupError struct {
	Phase  string
	Index  int
	Reason string
}

func (e *SetupError) Error() string {
	return fmt.Sprintf("setup %s command %d: %s", e.Phase, e.Index, e.Reason)
}

func RunSetup(ctx context.Context, dir, phase string, cmds []string, env []string, timeout time.Duration, out io.Writer) error {
	for i, c := range cmds {
		if reason := runSetupCommand(ctx, dir, c, append(os.Environ(), env...), timeout, out); reason != "" {
			return &SetupError{Phase: phase, Index: i + 1, Reason: reason}
		}
	}
	return nil
}

func runSetupCommand(ctx context.Context, dir, command string, env []string, timeout time.Duration, out io.Writer) string {
	if strings.TrimSpace(command) == "" {
		return "empty command"
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "sh", "-c", command)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = nil
	if out != nil {
		cmd.Stdout, cmd.Stderr = out, out
	}
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if err == nil {
		return ""
	}
	if cctx.Err() != nil {
		if ctx.Err() != nil {
			return "interrupted"
		}
		return fmt.Sprintf("timed out after %s", timeout)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return fmt.Sprintf("exit status %d", exit.ExitCode())
	}
	return "could not start"
}
