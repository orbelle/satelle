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

// The [setup] phases a node runs, named as the definition names them.
const (
	SetupOnCreate  = "on_create"
	SetupPostStart = "post_start"
)

// SetupError is a [setup] command that failed. Phase is its list (on_create or
// post_start), Index its place in it, counted from 1, and Reason a fixed
// classification. None holds the command, its output or the environment it ran
// in, so the error is safe to report to satelle.dev.
type SetupError struct {
	Phase  string
	Index  int
	Reason string
}

func (e *SetupError) Error() string {
	return fmt.Sprintf("setup %s command %d: %s", e.Phase, e.Index, e.Reason)
}

// RunSetup runs each of a [setup] phase's commands in order in dir, each one
// string through `sh -c`, with no stdin, each bounded by timeout, and stops at
// the first that fails. The commands' output goes to out (the node's own log)
// and nowhere else. The environment is the node's own plus env.
func RunSetup(ctx context.Context, dir, phase string, cmds []string, env []string, timeout time.Duration, out io.Writer) error {
	for i, c := range cmds {
		if reason := runSetupCommand(ctx, dir, c, append(os.Environ(), env...), timeout, out); reason != "" {
			return &SetupError{Phase: phase, Index: i + 1, Reason: reason}
		}
	}
	return nil
}

// runSetupCommand runs one command, in exactly the environment env, and returns
// why it failed, "" when it did not.
// The reason is a fixed classification, never the command or the raw exec error.
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
	// A command that ignores the kill and holds the pipes open must not outlive the bound.
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
