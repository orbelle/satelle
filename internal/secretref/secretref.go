// Package secretref resolves a configuration value that names a secret by its
// Infisical location instead of holding it (sty_bd8d963c). A value of the form
//
//	infisical://<project>/<env>/<path…>/<KEY>
//
// is a reference: the first segment is the Infisical project id, the second the
// environment slug, the middle segments the folder path (default "/"), and the
// last the secret's key. It is resolved at the point of use by the infisical CLI
// under the caller's own sign-in (`infisical login`) or machine identity
// (INFISICAL_TOKEN); satelle stores no Infisical credential. Infisical is the
// only secret source, so this is the only scheme.
//
// The package parses and resolves only. It never prints, logs or writes a
// value: argv carries only the reference parts, the CLI's stderr is discarded,
// and an error names the reference and the sign-in to fix, never the output.
// Keeping a resolved value out of what satelle writes is the caller's job (the
// agentstep redactor).
package secretref

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Scheme is the prefix every reference starts with.
const Scheme = "infisical://"

// Binary is the CLI a reference resolves through, looked up on PATH.
const Binary = "infisical"

// SignInHint is the fix every resolution error names.
const SignInHint = "sign in with `infisical login` or set INFISICAL_TOKEN for a machine identity"

// Ref is a parsed reference. Every field is safe to print.
type Ref struct {
	Project string
	Env     string
	// Path is the folder path, always starting with "/".
	Path string
	Key  string
}

// String renders the reference in its config form.
func (r Ref) String() string {
	path := strings.Trim(r.Path, "/")
	if path == "" {
		return Scheme + r.Project + "/" + r.Env + "/" + r.Key
	}
	return Scheme + r.Project + "/" + r.Env + "/" + path + "/" + r.Key
}

// Parse reports whether s, as a whole, is a reference. A partial match, an
// empty segment, whitespace, or a reference embedded in a longer string is not
// one: such a value is ordinary config and passes through unresolved.
func Parse(s string) (Ref, bool) {
	rest, ok := strings.CutPrefix(s, Scheme)
	if !ok || rest == "" || strings.ContainsAny(rest, " \t\r\n") {
		return Ref{}, false
	}
	segs := strings.Split(rest, "/")
	if len(segs) < 3 {
		return Ref{}, false
	}
	for _, seg := range segs {
		if seg == "" {
			return Ref{}, false
		}
	}
	return Ref{
		Project: segs[0],
		Env:     segs[1],
		Path:    "/" + strings.Join(segs[2:len(segs)-1], "/"),
		Key:     segs[len(segs)-1],
	}, true
}

// Error is a reference that could not be resolved. It names the reference and
// the sign-in to fix — never the CLI's output, which could echo a value.
type Error struct {
	Ref    string
	Reason string
}

func (e *Error) Error() string {
	return fmt.Sprintf("secret %s could not be resolved (%s): %s", e.Ref, e.Reason, SignInHint)
}

// args is the infisical argv for ref: only the reference parts, never a value.
func args(ref Ref) []string {
	return []string{"secrets", "get", ref.Key,
		"--projectId", ref.Project, "--env", ref.Env, "--path", ref.Path,
		"--plain", "--silent"}
}

// Resolve reads ref's value through the infisical CLI. The child inherits the
// caller's environment, so it runs under the caller's own sign-in or machine
// identity. The value is held in memory only; one trailing newline is trimmed.
func Resolve(ctx context.Context, ref Ref) (string, error) {
	fail := func(reason string) (string, error) {
		return "", &Error{Ref: ref.String(), Reason: reason}
	}
	bin, err := exec.LookPath(Binary)
	if err != nil {
		return fail("the infisical CLI is not on PATH")
	}
	var stdout bytes.Buffer
	c := exec.CommandContext(ctx, bin, args(ref)...)
	c.Stdout = &stdout
	c.Stderr = io.Discard
	if err := c.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return fail(fmt.Sprintf("infisical exited with status %d", ee.ExitCode()))
		}
		if ctx.Err() != nil {
			return fail("cancelled")
		}
		return fail("infisical could not be run")
	}
	v := strings.TrimSuffix(stdout.String(), "\n")
	v = strings.TrimSuffix(v, "\r")
	if v == "" {
		return fail("infisical returned an empty value")
	}
	return v, nil
}

// ResolveEnv returns a copy of env with every reference value replaced by its
// resolved value, and resolved: reference string → value, the set a caller
// scrubs output of (its keys are safe to print). A value that is not a
// reference is copied unchanged. The first reference that cannot be resolved
// fails the whole call. env is not mutated; resolved is nil when env holds no
// reference.
func ResolveEnv(ctx context.Context, env map[string]string) (map[string]string, map[string]string, error) {
	if env == nil {
		return nil, nil, nil
	}
	out := make(map[string]string, len(env))
	var resolved map[string]string
	for k, v := range env {
		ref, ok := Parse(v)
		if !ok {
			out[k] = v
			continue
		}
		val, known := resolved[v]
		if !known {
			var err error
			if val, err = Resolve(ctx, ref); err != nil {
				return nil, nil, err
			}
			if resolved == nil {
				resolved = map[string]string{}
			}
			resolved[v] = val
		}
		out[k] = val
	}
	return out, resolved, nil
}
