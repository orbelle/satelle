package gate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
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
	SkillDir             string
	Members              []CheckMember
	Redact               func(string) string
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
	if e.SkillDir != "" {
		m["skill_dir"] = e.SkillDir
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
	if e.SkillDir != "" {
		v = append(v, "SATELLE_SKILL_DIR="+e.SkillDir)
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

func isShellScript(script string) bool {
	argv, shebang, err := ScriptInterpreter(script)
	if err != nil {
		return false
	}
	if !shebang {
		return true
	}
	base := path.Base(filepath.ToSlash(argv[0]))
	if base == "env" && len(argv) > 1 {
		base = ""
		for _, f := range strings.Fields(argv[1]) {
			if strings.HasPrefix(f, "-") || strings.Contains(f, "=") {
				continue
			}
			base = path.Base(filepath.ToSlash(f))
			break
		}
	}
	switch base {
	case "sh", "bash", "dash", "ash", "zsh", "ksh":
		return true
	}
	return false
}

func ResolveSkillScript(script string) string {
	if !isShellScript(script) {
		return script
	}
	var out strings.Builder
	out.Grow(len(script))
	var stack []byte
	var prev byte
	for i := 0; i < len(script); {
		top := byte(0)
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		c := script[i]
		switch top {
		case '\'':
			out.WriteByte(c)
			if c == '\'' {
				stack = stack[:len(stack)-1]
				prev = '\''
			}
			i++
		case '"':
			switch {
			case c == '\\' && i+1 < len(script):
				out.WriteByte(c)
				out.WriteByte(script[i+1])
				i += 2
			case c == '"':
				out.WriteByte(c)
				stack = stack[:len(stack)-1]
				prev = '"'
				i++
			case c == '$' && i+1 < len(script) && script[i+1] == '(':
				out.WriteString("$(")
				stack = append(stack, '(')
				prev = '('
				i += 2
			default:
				out.WriteByte(c)
				i++
			}
		default:
			switch {
			case c == '#' && isCommentStart(prev):
				end := strings.IndexByte(script[i:], '\n')
				if end < 0 {
					out.WriteString(script[i:])
					return out.String()
				}
				out.WriteString(script[i : i+end+1])
				prev = '\n'
				i += end + 1
			case c == '\\' && i+1 < len(script):
				out.WriteByte(c)
				out.WriteByte(script[i+1])
				prev = 'a'
				i += 2
			case c == '\'':
				if isSkillPathBoundary(prev) {
					if kind, n, ok := matchSkillPathPrefix(script[i+1:]); ok && hasPathTail(script[i+1+n:], '\'') {
						out.WriteString(`"$SATELLE_SKILL_DIR"/'` + kind)
						stack = append(stack, '\'')
						i += 1 + n
						continue
					}
				}
				out.WriteByte(c)
				stack = append(stack, '\'')
				i++
			case c == '"':
				if isSkillPathBoundary(prev) {
					if kind, n, ok := matchSkillPathPrefix(script[i+1:]); ok && hasPathTail(script[i+1+n:], '"') {
						out.WriteString(`"$SATELLE_SKILL_DIR/` + kind)
						stack = append(stack, '"')
						i += 1 + n
						continue
					}
				}
				out.WriteByte(c)
				stack = append(stack, '"')
				i++
			case c == '(' && (prev == '$' || top == '('):
				out.WriteByte(c)
				stack = append(stack, '(')
				prev = '('
				i++
			case c == ')' && top == '(':
				out.WriteByte(c)
				stack = stack[:len(stack)-1]
				prev = ')'
				i++
			default:
				if isSkillPathBoundary(prev) {
					if kind, n, ok := matchSkillPathPrefix(script[i:]); ok && hasPathTail(script[i+n:], 0) {
						out.WriteString(`"$SATELLE_SKILL_DIR"/` + kind)
						prev = '/'
						i += n
						continue
					}
				}
				out.WriteByte(c)
				prev = c
				i++
			}
		}
	}
	return out.String()
}

func isCommentStart(prev byte) bool {
	switch prev {
	case 0, '\n', '\r', ' ', '\t', ';', '|', '&', '(', ')', '`':
		return true
	}
	return false
}

func isSkillPathBoundary(prev byte) bool {
	if prev == 0 {
		return true
	}
	if prev == '/' || prev == '$' || prev == '_' || prev == '.' || prev == '-' || prev == '"' || prev == '\'' {
		return false
	}
	if (prev >= 'a' && prev <= 'z') || (prev >= 'A' && prev <= 'Z') || (prev >= '0' && prev <= '9') {
		return false
	}
	return true
}

func matchSkillPathPrefix(s string) (kind string, consume int, ok bool) {
	for _, c := range []struct {
		prefix string
		kind   string
	}{
		{"./scripts/", "scripts/"},
		{"./assets/", "assets/"},
		{"scripts/", "scripts/"},
		{"assets/", "assets/"},
	} {
		if strings.HasPrefix(s, c.prefix) {
			return c.kind, len(c.prefix), true
		}
	}
	return "", 0, false
}

func hasPathTail(rest string, quote byte) bool {
	if rest == "" {
		return false
	}
	c := rest[0]
	switch quote {
	case '\'':
		return c != '\'' && c != '\n' && c != '\r'
	case '"':
		return c != '"' && c != '\n' && c != '\r'
	default:
		switch c {
		case ' ', '\t', '\r', '\n', ';', '|', '&', ')', '<', '>', '#', '`':
			return false
		}
		return true
	}
}

func RunCheckCode(ctx context.Context, script, dir string, env CheckEnv, timeout time.Duration) (int, string, error) {
	if timeout <= 0 {
		timeout = DefaultCheckTimeout
	}
	if env.SkillDir != "" {
		if !filepath.IsAbs(env.SkillDir) {
			if abs, err := filepath.Abs(env.SkillDir); err == nil {
				env.SkillDir = abs
			}
		}
		script = ResolveSkillScript(script)
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
	raw := out.String()
	if env.Redact != nil {
		raw = env.Redact(raw)
	}
	notes := bound([]byte(raw))
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
