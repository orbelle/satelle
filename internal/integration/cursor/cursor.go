package cursor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/bobmcallan/satelle/internal/integration/common"
)

const (
	Name = "cursor"
	CLI  = "cursor-agent"
)

type Dispatch struct{}

func (Dispatch) Name() string         { return CLI }
func (Dispatch) EnforcesGrants() bool { return true }

const cursorDenyAllShell = "Shell(**)"

type cursorGrant struct {
	ask   bool
	rules []string
}

func cursorFor(b common.Binding, denies []string) (cursorGrant, error) {
	plain, _ := common.SplitDenies(b.Tools, denies)
	grant := common.Granted(b, plain)
	has := func(t string) bool { return slices.Contains(grant, t) }
	if common.GrantsBash(grant) {
		return cursorGrant{}, errors.New("cursor-agent cannot hold a shell to satelle's performer denies: Cursor's Shell rules match the commands it parses, so a heredoc or here-string body or an editor variable escapes them; grant no Bash (see satelle help agents)")
	}
	if has(common.ToolNotebookEdit) {
		return cursorGrant{}, fmt.Errorf("cursor-agent cannot express the grant %s", common.ToolNotebookEdit)
	}
	if !(has(common.ToolRead) && has(common.ToolGrep) && has(common.ToolGlob)) {
		return cursorGrant{}, errors.New("cursor-agent grants Read, Grep and Glob together")
	}
	if has(common.ToolEdit) != has(common.ToolWrite) {
		return cursorGrant{}, errors.New("cursor-agent has one write permission: grant Edit and Write together")
	}
	return cursorGrant{ask: common.IsReadOnly(grant), rules: []string{cursorDenyAllShell}}, nil
}

func (Dispatch) Argv(req common.Request) ([]string, error) {
	b := req.Binding
	bin, extra, err := common.CommandFields(b)
	if err != nil {
		return nil, err
	}
	if b.Effort != "" {
		return nil, fmt.Errorf("cursor-agent carries effort in the model id (e.g. claude-opus-5-5-high); drop effort %q", b.Effort)
	}
	g, err := cursorFor(b, req.Denies())
	if err != nil {
		return nil, err
	}
	argv := append([]string{bin}, extra...)
	argv = append(argv, "-p", "--output-format", "stream-json", "--trust")
	if b.Model != "" {
		argv = append(argv, "--model", b.Model)
	}
	if g.ask {
		argv = append(argv, "--mode", "ask")
	} else {
		argv = append(argv, "--force")
	}
	return argv, nil
}

func cursorStdin(req common.Request) string {
	if req.System == "" {
		return req.Prompt
	}
	return "<system>\n" + req.System + "\n</system>\n\n" + req.Prompt
}

func (c Dispatch) Run(ctx context.Context, req common.Request) (common.Result, error) {
	argv, err := c.Argv(req)
	if err != nil {
		return common.Result{}, err
	}
	g, _ := cursorFor(req.Binding, req.Denies())
	if err := cursorProjectConfig(req.Dir); err != nil {
		return common.Result{}, err
	}
	cfg, cleanup, err := cursorConfigDir(req, g.rules)
	if err != nil {
		return common.Result{}, err
	}
	defer cleanup()
	req.Env = append(append([]string{}, req.Env...), "CURSOR_CONFIG_DIR="+cfg)
	out, err := common.RunOnce(ctx, argv, cursorStdin(req), req)
	if err != nil {
		return common.Result{}, cursorFailure(argv[0], out, err)
	}
	return parseCursor(out)
}

func cursorAuthDir(req common.Request) string {
	for i := len(req.Env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(req.Env[i], "CURSOR_CONFIG_DIR="); ok && v != "" {
			return v
		}
	}
	if v := os.Getenv("CURSOR_CONFIG_DIR"); v != "" {
		return v
	}
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "cursor")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "cursor")
}

func cursorConfigDir(req common.Request, rules []string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "satelle-cursor-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	if auth := filepath.Join(cursorAuthDir(req), "auth.json"); common.FileExists(auth) {
		if err := os.Symlink(auth, filepath.Join(dir, "auth.json")); err != nil {
			cleanup()
			return "", nil, err
		}
	}
	if rules == nil {
		rules = []string{}
	}
	doc := map[string]any{"version": 1, "permissions": map[string]any{"allow": []string{}, "deny": rules}}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		cleanup()
		return "", nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "cli-config.json"), append(b, '\n'), 0o600); err != nil {
		cleanup()
		return "", nil, err
	}
	written, _ := json.Marshal(rules)
	req.Trace.Line("cursor permissions: ", written)
	return dir, cleanup, nil
}

func cursorProjectConfig(dir string) error {
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		dir = wd
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	for {
		p := filepath.Join(dir, ".cursor", "cli.json")
		if b, err := os.ReadFile(p); err == nil {
			var doc struct {
				Permissions struct {
					Allow []string `json:"allow"`
				} `json:"permissions"`
				ApprovalMode *json.RawMessage `json:"approvalMode"`
			}
			if err := json.Unmarshal(b, &doc); err != nil {
				return fmt.Errorf("cursor-agent: %s does not decode (%v); satelle cannot hold the run to its grant there", p, err)
			}
			if len(doc.Permissions.Allow) > 0 || doc.ApprovalMode != nil {
				return fmt.Errorf("cursor-agent: %s grants tools to every run under it (permissions.allow or approvalMode); satelle cannot hold the run to its grant there", p)
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
		dir = parent
	}
}

type cursorEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Model   string `json:"model"`
	Result  string `json:"result"`
	IsError bool   `json:"is_error"`
	Usage   *struct {
		InputTokens      int `json:"inputTokens"`
		OutputTokens     int `json:"outputTokens"`
		CacheReadTokens  int `json:"cacheReadTokens"`
		CacheWriteTokens int `json:"cacheWriteTokens"`
	} `json:"usage"`
}

func readCursorStream(out []byte) []cursorEvent {
	var evs []cursorEvent
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64*1024), 64*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var e cursorEvent
		if json.Unmarshal(line, &e) == nil && e.Type != "" {
			evs = append(evs, e)
		}
	}
	return evs
}

func parseCursor(out []byte) (common.Result, error) {
	evs := readCursorStream(out)
	if len(evs) == 0 {
		return common.Result{}, errors.New("cursor-agent: output is not a stream-json stream")
	}
	var res *cursorEvent
	model := ""
	for i := range evs {
		switch evs[i].Type {
		case "system":
			if model == "" {
				model = evs[i].Model
			}
		case "result":
			res = &evs[i]
		}
	}
	if res == nil {
		return common.Result{}, errors.New("cursor-agent: stream ended without a result event")
	}
	if res.Model != "" {
		model = res.Model
	}
	if res.IsError || strings.HasPrefix(res.Subtype, "error") {
		return common.Result{}, fmt.Errorf("cursor-agent: run ended with %s: %s", res.Subtype, res.Result)
	}
	if strings.TrimSpace(res.Result) == "" {
		return common.Result{}, errors.New("cursor-agent: run ended with no answer")
	}
	r := common.Result{Text: res.Result, Model: model}
	if u := res.Usage; u != nil {
		r.TokensIn = u.InputTokens + u.CacheReadTokens + u.CacheWriteTokens
		r.TokensOut = u.OutputTokens
	}
	return r, nil
}

var cursorSignIn = regexp.MustCompile(`(?i)login|authenticat|sign in`)

func cursorFailure(bin string, out []byte, err error) error {
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("cursor-agent is not installed or not on PATH (%s): %w", bin, err)
	}
	if _, perr := parseCursor(out); perr != nil && strings.HasPrefix(perr.Error(), "cursor-agent: run ended with") {
		return perr
	}
	if cursorSignIn.MatchString(string(out)) || cursorSignIn.MatchString(err.Error()) {
		return fmt.Errorf("cursor-agent is not signed in on this machine (run cursor-agent login): %w", err)
	}
	return err
}
