package gate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/procgroup"
)

// DefaultCheckTimeout bounds a check script when the caller sets none.
const DefaultCheckTimeout = 10 * time.Minute

// outputBound is how much of a check's output head and tail the notes keep.
const outputBound = 2048

// CheckEnv is what a check script sees of its transition. Gate, Result,
// Binding, Notes and Members are set only for a gate's after check: the gate it
// follows, that gate's verdict, and each judge's own answer (none for a check
// gate or a carry).
type CheckEnv struct {
	Item, From, To, Base string
	Gate, Result         string
	Binding, Notes       string
	Members              []CheckMember
}

// CheckMember is one judge's own verdict and notes, as an after check sees it.
type CheckMember struct {
	Binding string `json:"binding"`
	Result  string `json:"result"`
	Notes   string `json:"notes"`
}

// Payload is the JSON object a check reads on stdin.
func (e CheckEnv) Payload() []byte {
	m := map[string]any{"id": e.Item, "from": e.From, "to": e.To, "base": e.Base}
	if e.Gate != "" {
		m["gate"], m["result"], m["binding"], m["notes"] = e.Gate, e.Result, e.Binding, e.Notes
	}
	if len(e.Members) > 0 {
		m["members"] = e.Members
	}
	b, _ := json.Marshal(m)
	return b
}

// Vars are the SATELLE_* variables a check sees, as KEY=value.
func (e CheckEnv) Vars() []string {
	v := []string{"SATELLE_ITEM=" + e.Item, "SATELLE_FROM=" + e.From, "SATELLE_TO=" + e.To, "SATELLE_BASE=" + e.Base}
	if e.Gate != "" {
		v = append(v, "SATELLE_GATE="+e.Gate, "SATELLE_GATE_RESULT="+e.Result, "SATELLE_GATE_BINDING="+e.Binding)
	}
	return v
}

// RunCheck runs a check skill's script and returns whether it exited 0 and its
// combined output, bounded to head and tail. See RunCheckCode.
func RunCheck(ctx context.Context, script, dir string, env CheckEnv, timeout time.Duration) (bool, string, error) {
	code, out, err := RunCheckCode(ctx, script, dir, env, timeout)
	return err == nil && code == 0, out, err
}

// RunCheckCode runs a check skill's script in dir: a script starting with "#!"
// is executed as a file (its interpreter decides), any other through sh. The
// transition is in the environment and, as JSON, on stdin. It returns the
// script's exit code (-1 when a signal killed it) and its combined output,
// bounded to head and tail. A timeout, or a script that cannot start, is an
// error with code -1.
func RunCheckCode(ctx context.Context, script, dir string, env CheckEnv, timeout time.Duration) (int, string, error) {
	if timeout <= 0 {
		timeout = DefaultCheckTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var cmd *exec.Cmd
	if strings.HasPrefix(script, "#!") {
		f, err := os.CreateTemp("", "satelle-check-*")
		if err != nil {
			return -1, "", err
		}
		defer os.Remove(f.Name())
		_, werr := f.WriteString(script)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return -1, "", werr
		}
		if err := os.Chmod(f.Name(), 0o700); err != nil {
			return -1, "", err
		}
		cmd = exec.CommandContext(ctx, f.Name())
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", script)
	}
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader(env.Payload())
	cmd.Env = append(os.Environ(), env.Vars()...)
	procgroup.Set(cmd)
	cmd.Cancel = func() error { return procgroup.Kill(cmd) }
	cmd.WaitDelay = 2 * time.Second
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	notes := bound(out.Bytes())
	if ctx.Err() != nil {
		return -1, notes, &TimeoutError{Bound: timeout}
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), notes, nil
	}
	if err != nil {
		return -1, notes, err
	}
	return 0, notes, nil
}

// TimeoutError is a check cut at its bound.
type TimeoutError struct{ Bound time.Duration }

func (e *TimeoutError) Error() string { return fmt.Sprintf("check timed out after %v", e.Bound) }

func bound(b []byte) string {
	b = bytes.TrimSpace(b)
	if len(b) <= 2*outputBound {
		return string(b)
	}
	return fmt.Sprintf("%s\n… %d bytes elided …\n%s", b[:outputBound], len(b)-2*outputBound, b[len(b)-outputBound:])
}
