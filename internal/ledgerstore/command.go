package ledgerstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/procgroup"
)

const (
	OpPutIfAbsent = "put-if-absent"
	OpGet         = "get"
	OpList        = "list"
)

const (
	ExitOK        = 0
	ExitTransient = 1
	ExitPermanent = 2
	ExitExists    = 3
	ExitNotFound  = 4
)

const DefaultOpTimeout = 5 * time.Minute

const maxStderr = 64 << 10

var causeLine = regexp.MustCompile(`^error: (auth|not-found|conflict|unavailable|invalid): .+$`)

type Command struct {
	Path     string
	Args     []string
	Dir      string
	Withhold []string
	Timeout  time.Duration
}

type Error struct {
	Op        string
	Key       string
	Exit      int
	Retryable bool
	Code      string
	Line      string
	Reason    string
}

func (e *Error) Error() string {
	head := "ledger store: " + e.Op
	if e.Key != "" {
		head += " " + e.Key
	}
	if e.Exit >= 0 {
		head += fmt.Sprintf(": exit %d", e.Exit)
	}
	if e.Retryable {
		head += " (transient)"
	} else {
		head += " (permanent)"
	}
	if e.Line != "" {
		return head + ": " + e.Line
	}
	return head + ": " + e.Reason
}

type result struct {
	exit      int
	stdout    []byte
	line      string
	code      string
	stderrLen int
}

func (c Command) PutIfAbsent(ctx context.Context, key string, body []byte) (existing []byte, created bool, err error) {
	if err := CheckKey(key); err != nil {
		return nil, false, err
	}
	if err := CheckBody(len(body)); err != nil {
		return nil, false, err
	}
	ops := []string{key}
	r, err := c.run(ctx, OpPutIfAbsent, ops, body)
	if err != nil {
		return nil, false, err
	}
	switch r.exit {
	case ExitOK:
		return nil, true, nil
	case ExitExists:
		return r.stdout, false, nil
	}
	return nil, false, failure(OpPutIfAbsent, ops, r)
}

func (c Command) Get(ctx context.Context, key string) (body []byte, found bool, err error) {
	if err := CheckKey(key); err != nil {
		return nil, false, err
	}
	ops := []string{key}
	r, err := c.run(ctx, OpGet, ops, nil)
	if err != nil {
		return nil, false, err
	}
	switch r.exit {
	case ExitOK:
		return r.stdout, true, nil
	case ExitNotFound:
		return nil, false, nil
	}
	return nil, false, failure(OpGet, ops, r)
}

func (c Command) List(ctx context.Context, prefix, after string) ([]string, error) {
	if err := CheckKey(prefix); err != nil {
		return nil, fmt.Errorf("prefix: %w", err)
	}
	ops := []string{prefix}
	if after != "" {
		if err := CheckKey(after); err != nil {
			return nil, fmt.Errorf("after-key: %w", err)
		}
		ops = append(ops, after)
	}
	r, err := c.run(ctx, OpList, ops, nil)
	if err != nil {
		return nil, err
	}
	if r.exit != ExitOK {
		return nil, failure(OpList, ops, r)
	}
	keys := []string{}
	out := string(r.stdout)
	if out == "" {
		return keys, nil
	}
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	bad := func(format string, args ...any) error {
		return &Error{Op: OpList, Key: strings.Join(ops, " "), Exit: ExitOK, Reason: "the command broke the contract: " + fmt.Sprintf(format, args...)}
	}
	for i, k := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if err := CheckKey(k); err != nil {
			return nil, bad("line %d is not a key: %v", i+1, err)
		}
		if !strings.HasPrefix(k, prefix) {
			return nil, bad("line %d key %q is not under the prefix %q", i+1, k, prefix)
		}
		if after != "" && k <= after {
			return nil, bad("line %d key %q is not after the after-key %q", i+1, k, after)
		}
		if n := len(keys); n > 0 && k <= keys[n-1] {
			return nil, bad("line %d key %q does not sort after %q (keys are listed in ascending byte order, each once)", i+1, k, keys[n-1])
		}
		keys = append(keys, k)
	}
	return keys, nil
}

func failure(op string, ops []string, r result) error {
	e := &Error{Op: op, Key: strings.Join(ops, " "), Exit: r.exit}
	switch r.exit {
	case ExitTransient, ExitPermanent:
		e.Retryable = r.exit == ExitTransient
		if r.line != "" {
			e.Line, e.Code = r.line, r.code
			return e
		}
		e.Reason = fmt.Sprintf("the command broke the contract: it printed no error: <code>: <message> line (stderr was %d bytes)", r.stderrLen)
		return e
	}
	e.Reason = fmt.Sprintf("the command broke the contract: exit %d is not an outcome of %s (exit 1 is transient, 2 permanent)", r.exit, op)
	return e
}

func (c Command) run(ctx context.Context, op string, operands []string, stdin []byte) (result, error) {
	key := strings.Join(operands, " ")
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultOpTimeout
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	argv := append(append(slices.Clone(c.Args), op), operands...)
	cmd := exec.CommandContext(tctx, c.Path, argv...)
	cmd.Dir = c.Dir
	cmd.Env = withhold(os.Environ(), c.Withhold)
	cmd.Stdin = bytes.NewReader(stdin)
	procgroup.Set(cmd)
	cmd.Cancel = func() error { return procgroup.Kill(cmd) }
	cmd.WaitDelay = 2 * time.Second
	stdout := &capped{max: MaxBodyBytes + 1}
	stderr := &tail{max: maxStderr}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return result{}, fmt.Errorf("ledger store: %s %s: %w", op, key, ctx.Err())
	}
	if tctx.Err() != nil {
		return result{}, &Error{Op: op, Key: key, Exit: -1, Retryable: true, Reason: fmt.Sprintf("timed out after %v", timeout)}
	}
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		return result{}, &Error{Op: op, Key: key, Exit: -1, Reason: fmt.Sprintf("could not run the store command: %v", err)}
	}
	r := result{exit: 0, stdout: stdout.buf, stderrLen: stderr.total}
	if exit != nil {
		r.exit = exit.ExitCode()
		if r.exit < 0 {
			reason := "the command broke the contract: a signal ended it before it exited"
			if desc := signalEnd(cmd); desc != "" {
				reason += " (" + desc + ")"
			}
			return result{}, &Error{Op: op, Key: key, Exit: -1, Reason: reason}
		}
	}
	if stdout.total > MaxBodyBytes {
		return result{}, &Error{Op: op, Key: key, Exit: r.exit, Reason: fmt.Sprintf("the command broke the contract: it printed more than %d bytes (64 MiB) to stdout", MaxBodyBytes)}
	}
	r.line, r.code = cause(stderr.buf)
	return r, nil
}

func cause(stderr []byte) (string, string) {
	lines := strings.Split(string(stderr), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if m := causeLine.FindStringSubmatch(lines[i]); m != nil {
			return m[0], m[1]
		}
	}
	return "", ""
}

func withhold(env, names []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if slices.Contains(names, name) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

type capped struct {
	buf   []byte
	max   int
	total int
}

func (w *capped) Write(p []byte) (int, error) {
	w.total += len(p)
	if room := w.max - len(w.buf); room > 0 {
		w.buf = append(w.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

type tail struct {
	buf   []byte
	max   int
	total int
}

func (w *tail) Write(p []byte) (int, error) {
	w.total += len(p)
	w.buf = append(w.buf, p...)
	if len(w.buf) > w.max {
		w.buf = append(w.buf[:0:0], w.buf[len(w.buf)-w.max:]...)
	}
	return len(p), nil
}
