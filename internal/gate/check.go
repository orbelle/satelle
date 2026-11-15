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

const DefaultCheckTimeout = 10 * time.Minute

const outputBound = 2048

type CheckEnv struct {
	Item, From, To, Base string
	Gate, Result         string
	Binding, Notes       string
	Members              []CheckMember
}

type CheckMember struct {
	Binding string `json:"binding"`
	Result  string `json:"result"`
	Notes   string `json:"notes"`
}

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

func (e CheckEnv) Vars() []string {
	v := []string{"SATELLE_ITEM=" + e.Item, "SATELLE_FROM=" + e.From, "SATELLE_TO=" + e.To, "SATELLE_BASE=" + e.Base}
	if e.Gate != "" {
		v = append(v, "SATELLE_GATE="+e.Gate, "SATELLE_GATE_RESULT="+e.Result, "SATELLE_GATE_BINDING="+e.Binding)
	}
	return v
}

func RunCheck(ctx context.Context, script, dir string, env CheckEnv, timeout time.Duration) (bool, string, error) {
	code, out, err := RunCheckCode(ctx, script, dir, env, timeout)
	return err == nil && code == 0, out, err
}

func ScriptInterpreter(script string) (argv []string, ok bool, err error) {
	if !strings.HasPrefix(script, "#!") {
		return nil, false, nil
	}
	line, _, _ := strings.Cut(script[2:], "\n")
	line = strings.Trim(line, " \t\r")
	if line == "" {
		return nil, true, errors.New("check script names no interpreter after #!")
	}
	interp, arg := line, ""
	if i := strings.IndexAny(line, " \t"); i >= 0 {
		interp, arg = line[:i], line[i+1:]
	}
	if !strings.Contains(interp, "/") {
		interp = "./" + interp
	}
	argv = []string{interp}
	if arg = strings.Trim(arg, " \t"); arg != "" {
		argv = append(argv, arg)
	}
	return argv, true, nil
}

func RunCheckCode(ctx context.Context, script, dir string, env CheckEnv, timeout time.Duration) (int, string, error) {
	if timeout <= 0 {
		timeout = DefaultCheckTimeout
	}
	argv, shebang, err := ScriptInterpreter(script)
	if err != nil {
		return -1, "", err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var cmd *exec.Cmd
	if shebang {
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
		cmd = exec.CommandContext(ctx, argv[0], append(argv[1:], f.Name())...)
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
	err = cmd.Run()
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

type TimeoutError struct{ Bound time.Duration }

func (e *TimeoutError) Error() string { return fmt.Sprintf("check timed out after %v", e.Bound) }

func bound(b []byte) string {
	b = bytes.TrimSpace(b)
	if len(b) <= 2*outputBound {
		return string(b)
	}
	return fmt.Sprintf("%s\n… %d bytes elided …\n%s", b[:outputBound], len(b)-2*outputBound, b[len(b)-outputBound:])
}
