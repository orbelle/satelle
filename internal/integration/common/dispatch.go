package common

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/procgroup"
)

type Binding struct {
	Name      string   `toml:"-"`
	Command   string   `toml:"command"`
	Model     string   `toml:"model"`
	Effort    string   `toml:"effort"`
	Tools     []string `toml:"tools"`
	Deny      []string `toml:"deny"`
	Placement string   `toml:"placement"`
	Brief     string   `toml:"brief"`
	Fallback  string   `toml:"fallback"`
}

type Request struct {
	Binding Binding
	System  string
	Prompt  string
	Deny    []string
	Dir     string
	Env     []string
	Log     string
	Trace   *Log
	Redact  func(string) string
	Target  *Target
}

type Target struct {
	Repo, Worktree string
	Scratch        string
	ReadDirs       []string
	Protected      bool
	Environ        []string
	EnvNames       []string
	Narrowed       string
	Outside        bool
	Confined       bool
}

type Confinement struct {
	ProjectFiles []string
	AuthEnv      []string
	Context      bool
	OnRead       OnRead
	FileTools    bool
	Shell        bool
	Refuse       string
}

type OnRead struct {
	Paths    []string
	Nested   []string
	Anywhere []string
}

var ErrUnconfined = errors.New("target dispatch was not confined")

func Environ(req Request) []string {
	if req.Target == nil {
		return append(os.Environ(), req.Env...)
	}
	out := make([]string, 0, len(req.Target.Environ)+len(req.Env))
	out = append(out, req.Target.Environ...)
	return append(out, req.Env...)
}

func CheckConfined(req Request) error {
	if req.Target != nil && req.Target.Environ == nil {
		return ErrUnconfined
	}
	return nil
}

func ValueRedactor(values []string) func(string) string {
	var vs []string
	for _, v := range values {
		if v != "" && !slices.Contains(vs, v) {
			vs = append(vs, v)
		}
	}
	if len(vs) == 0 {
		return nil
	}
	slices.SortStableFunc(vs, func(a, b string) int { return len(b) - len(a) })
	return func(s string) string {
		for _, v := range vs {
			s = strings.ReplaceAll(s, v, "[secret]")
		}
		return s
	}
}

func Chain(a, b func(string) string) func(string) string {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	}
	return func(s string) string { return b(a(s)) }
}

func (r Request) Clean(s string) string {
	if r.Redact == nil {
		return s
	}
	return r.Redact(s)
}

func (r Request) Denies() []string {
	return append(append([]string{}, r.Binding.Deny...), r.Deny...)
}

type Result struct {
	Text      string
	TokensIn  int
	TokensOut int
	Model     string
	Command   string
	Duration  time.Duration
}

type DispatchAdapter interface {
	Name() string
	EnforcesGrants() bool
	Argv(req Request) ([]string, error)
	Run(ctx context.Context, req Request) (Result, error)
	Confinement() Confinement
}

const (
	ToolRead         = "Read"
	ToolGrep         = "Grep"
	ToolGlob         = "Glob"
	ToolEdit         = "Edit"
	ToolWrite        = "Write"
	ToolNotebookEdit = "NotebookEdit"
	ToolBash         = "Bash"
)

var (
	PlainTools = []string{ToolRead, ToolGrep, ToolGlob, ToolEdit, ToolWrite, ToolNotebookEdit, ToolBash}
	ReadTools  = []string{ToolRead, ToolGrep, ToolGlob}
	scopedBash = regexp.MustCompile(`^Bash\(([^()]+):\*\)$`)
)

func IsScoped(t string) bool { return scopedBash.MatchString(t) }

func IsReadOnly(grant []string) bool {
	for _, t := range grant {
		if !slices.Contains(ReadTools, t) {
			return false
		}
	}
	return true
}

func GrantsBash(grant []string) bool {
	return slices.ContainsFunc(grant, func(t string) bool { return t == ToolBash || IsScoped(t) })
}

func SplitDenies(grant, denies []string) (plain, scoped []string) {
	for _, d := range denies {
		switch {
		case IsScoped(d):
			if GrantsBash(grant) && !slices.Contains(scoped, d) {
				scoped = append(scoped, d)
			}
		case !slices.Contains(plain, d):
			plain = append(plain, d)
		}
	}
	return plain, scoped
}

func Granted(b Binding, plain []string) []string {
	var grant []string
	for _, t := range b.Tools {
		if !slices.Contains(plain, t) {
			grant = append(grant, t)
		}
	}
	return grant
}

func TranslateTools(adapter string, names map[string][]string, tools []string) ([]string, error) {
	var out []string
	for _, t := range tools {
		mapped, ok := names[t]
		if !ok {
			return nil, fmt.Errorf("%s adapter cannot express tool %q", adapter, t)
		}
		for _, m := range mapped {
			if !slices.Contains(out, m) {
				out = append(out, m)
			}
		}
	}
	return out, nil
}

func SingleToolMap(m map[string]string) map[string][]string {
	out := map[string][]string{}
	for k, v := range m {
		out[k] = []string{v}
	}
	return out
}

func CommandFields(b Binding) (bin string, args []string, err error) {
	f := strings.Fields(b.Command)
	if len(f) == 0 {
		return "", nil, fmt.Errorf("binding %q has no command", b.Name)
	}
	return f[0], f[1:], nil
}

func Preset(bin string) string {
	return strings.TrimSuffix(strings.ToLower(filepath.Base(bin)), ".exe")
}

func RunOnce(ctx context.Context, argv []string, stdin string, req Request) ([]byte, error) {
	if err := CheckConfined(req); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = req.Dir
	cmd.Env = Environ(req)
	procgroup.Set(cmd)
	cmd.Cancel = func() error { return procgroup.Kill(cmd) }
	cmd.WaitDelay = 2 * time.Second
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	req.Trace.Argv(argv)
	err := cmd.Run()
	req.Trace.Output("< ", out.Bytes())
	req.Trace.Output("stderr: ", errb.Bytes())
	if ctx.Err() != nil {
		return out.Bytes(), fmt.Errorf("%s: %w", argv[0], ctx.Err())
	}
	if err != nil {
		return out.Bytes(), fmt.Errorf("%s: %w: %s", argv[0], err, Tail([]byte(req.Clean(errb.String())), 2000))
	}
	return out.Bytes(), nil
}

func Tail(b []byte, n int) string {
	b = bytes.TrimSpace(b)
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return string(b)
}

type ModelUsage map[string]struct {
	OutputTokens int `json:"outputTokens"`
}

func (u ModelUsage) SelectModelByOutput() string {
	best, most := "", -1
	for name, m := range u {
		if m.OutputTokens > most || (m.OutputTokens == most && name < best) {
			best, most = name, m.OutputTokens
		}
	}
	return best
}

func FileExists(p string) bool { _, err := os.Stat(p); return err == nil }
