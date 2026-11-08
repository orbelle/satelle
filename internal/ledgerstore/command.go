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

// The adapter operations an external store command implements.
const (
	OpPutIfAbsent = "put-if-absent"
	OpGet         = "get"
	OpList        = "list"
)

// The exit codes of the contract.
const (
	ExitOK        = 0 // put-if-absent created, get found, list listed
	ExitTransient = 1 // a transient failure: satelle may retry
	ExitPermanent = 2 // a permanent failure: bad config, access denied, unknown operation
	ExitExists    = 3 // put-if-absent: the key exists; its body is on stdout
	ExitNotFound  = 4 // get: no such key
)

// DefaultOpTimeout bounds one operation when Command.Timeout is unset.
const DefaultOpTimeout = 5 * time.Minute

// maxStderr is how much of the command's stderr is kept to find its cause line.
const maxStderr = 64 << 10

// causeLine is the one stderr line an adapter prints on exit 1 or 2.
var causeLine = regexp.MustCompile(`^error: (auth|not-found|conflict|unavailable|invalid): .+$`)

// Command is an external store command: [ledger] store = { command, args }.
// Each operation runs it once as Path Args... <op> <operands...>, with no
// shell, in Dir, with the parent's environment minus the names in Withhold.
type Command struct {
	Path     string
	Args     []string
	Dir      string
	Withhold []string
	Timeout  time.Duration
}

// Error is a failed store operation. Exit is the command's exit code, -1 when
// it has none (it could not start, a signal killed it, or it timed out). Line
// is the command's 'error: <code>: <message>' line, verbatim, when it printed
// one; Reason says what went wrong otherwise. Retryable is true for exit 1 and
// a timeout. The command's stdout, raw stderr and environment are never held.
type Error struct {
	Op        string
	Key       string // the operands, space-separated
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

// result is one run of the command.
type result struct {
	exit      int
	stdout    []byte
	line      string // the last cause line on stderr, or ""
	code      string
	stderrLen int
}

// PutIfAbsent creates key with body unless it exists. Exit 0 is created; exit
// 3 is already present, with the existing body returned.
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

// Get reads key. Exit 0 is found, with the body; exit 4 is no such key, with a
// nil error.
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

// List returns the keys under prefix, sorted in byte order, starting after
// after when it is not "". It checks the command's output against the
// contract: every line a valid key under the prefix, strictly ascending, and
// greater than after.
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

// failure is the error of a run that exited with a code its operation does not
// define as success: exit 1 or 2 with the command's cause line, or a protocol
// error naming the code.
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

// run executes the command once: Path Args... op operands..., with stdin as
// its standard input. It returns the exit code, the stdout and the cause line.
// An error is a run that has no exit code to read (it could not start, timed
// out, was killed by a signal, or printed more than MaxBodyBytes).
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
			return result{}, &Error{Op: op, Key: key, Exit: -1, Reason: "the command broke the contract: a signal ended it before it exited"}
		}
	}
	if stdout.total > MaxBodyBytes {
		return result{}, &Error{Op: op, Key: key, Exit: r.exit, Reason: fmt.Sprintf("the command broke the contract: it printed more than %d bytes (64 MiB) to stdout", MaxBodyBytes)}
	}
	r.line, r.code = cause(stderr.buf)
	return r, nil
}

// cause is the last stderr line matching the contract's error line, and its code.
func cause(stderr []byte) (string, string) {
	lines := strings.Split(string(stderr), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if m := causeLine.FindStringSubmatch(lines[i]); m != nil {
			return m[0], m[1]
		}
	}
	return "", ""
}

// withhold is env without the variables named in names. Nothing is added.
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

// capped keeps the first max bytes written and counts them all.
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

// tail keeps the last max bytes written and counts them all.
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
