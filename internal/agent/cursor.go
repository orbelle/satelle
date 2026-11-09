package agent

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
)

// cursor runs Cursor's own headless CLI: `cursor-agent -p --output-format
// stream-json --trust`, the skill and the work on stdin. Every run gets its own
// Cursor config directory (CURSOR_CONFIG_DIR), holding the machine's sign-in
// and a cli-config.json whose permissions.deny is the run's rules, removed when
// the run ends: nothing is written to the working tree, and runs in the same
// directory never share a config. A read-only grant runs `--mode ask` with
// every shell command denied (ask mode still runs commands Cursor deems
// read-only); any other grant runs `--force` with its denies. Cursor shows
// every tool call in its stream, so the dispatch log records each one.
type cursor struct{}

func (cursor) Name() string         { return PresetCursor }
func (cursor) EnforcesGrants() bool { return true }

// cursorGrant is how a binding's grant is expressed to cursor-agent: ask mode
// (read-only) or force mode with deny rules.
type cursorGrant struct {
	ask   bool
	rules []string // the run's Cursor permission deny rules
}

// cursorFor maps a grant and its denies to cursor-agent, refusing what Cursor
// cannot hold the agent to: its read and search tools come together, its one
// write permission covers Edit and Write together, and its shell can write
// files, so Bash needs Edit and Write.
func cursorFor(b Binding, denies []string) (cursorGrant, error) {
	plain, _ := splitDenies(b.Tools, denies)
	var grant []string
	for _, t := range b.Tools {
		if !slices.Contains(plain, t) {
			grant = append(grant, t)
		}
	}
	has := func(t string) bool { return slices.Contains(grant, t) }
	for _, t := range grant {
		if t == ToolNotebookEdit || IsScoped(t) {
			return cursorGrant{}, fmt.Errorf("cursor-agent cannot express the grant %s", t)
		}
	}
	if !(has(ToolRead) && has(ToolGrep) && has(ToolGlob)) {
		return cursorGrant{}, errors.New("cursor-agent grants Read, Grep and Glob together")
	}
	if has(ToolEdit) != has(ToolWrite) {
		return cursorGrant{}, errors.New("cursor-agent has one write permission: grant Edit and Write together")
	}
	if has(ToolBash) && !has(ToolEdit) {
		return cursorGrant{}, errors.New("cursor-agent's shell can write files; grant Edit and Write with Bash")
	}
	rules, err := CursorDenies(grant, denies)
	if err != nil {
		return cursorGrant{}, err
	}
	return cursorGrant{ask: IsReadOnly(grant), rules: rules}, nil
}

var cursorGlobChars = regexp.MustCompile(`[*?\[\]{}]`)

// CursorDenies is the Cursor permission deny list a force-mode run carries for
// grant and denies: Shell(**) when the grant runs no shell; otherwise one rule
// Shell(**W**) per scoped deny, W its first word, which matches every command
// whose text contains W, so a denied command is refused at any word position,
// by any path, behind any wrapper and inside any shell program. It over-denies
// by design: a performer granted Bash runs no command naming satelle at all.
func CursorDenies(grant, denies []string) ([]string, error) {
	if !grantsBash(grant) {
		return []string{"Shell(**)"}, nil
	}
	_, scoped := splitDenies(grant, denies)
	var out []string
	for _, d := range scoped {
		m := scopedBash.FindStringSubmatch(d)
		f := strings.Fields(m[1])
		if len(f) == 0 {
			return nil, fmt.Errorf("cannot express %s as a Cursor rule: it names no command", d)
		}
		if cursorGlobChars.MatchString(f[0]) {
			return nil, fmt.Errorf("cannot express %s as a Cursor glob", d)
		}
		r := "Shell(**" + f[0] + "**)"
		if !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (cursor) Argv(req Request) ([]string, error) {
	b := req.Binding
	bin, extra, err := commandFields(b)
	if err != nil {
		return nil, err
	}
	if b.Effort != "" {
		return nil, fmt.Errorf("cursor-agent carries effort in the model id (e.g. claude-opus-5-5-high); drop effort %q", b.Effort)
	}
	g, err := cursorFor(b, req.denies())
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

// cursorStdin is the run's stdin: the skill as a system block, then the work.
func cursorStdin(req Request) string {
	if req.System == "" {
		return req.Prompt
	}
	return "<system>\n" + req.System + "\n</system>\n\n" + req.Prompt
}

func (c cursor) Run(ctx context.Context, req Request) (Result, error) {
	argv, err := c.Argv(req)
	if err != nil {
		return Result{}, err
	}
	g, _ := cursorFor(req.Binding, req.denies())
	cfg, cleanup, err := cursorConfigDir(req, g.rules)
	if err != nil {
		return Result{}, err
	}
	defer cleanup()
	req.Env = append(append([]string{}, req.Env...), "CURSOR_CONFIG_DIR="+cfg)
	out, err := runOnce(ctx, argv, cursorStdin(req), req)
	if err != nil {
		return Result{}, cursorFailure(argv[0], out, err)
	}
	return parseCursor(out)
}

// cursorAuthDir is where this machine's cursor-agent keeps its sign-in: the
// run's environment's CURSOR_CONFIG_DIR, then the process's, then
// $XDG_CONFIG_HOME/cursor, else ~/.config/cursor.
func cursorAuthDir(req Request) string {
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

// cursorConfigDir makes the run's own Cursor config directory: the machine's
// auth.json linked in (so the run is signed in as the machine is) and a
// cli-config.json denying rules. The user's own CLI settings are not carried:
// the run is cold. cleanup removes the directory.
func cursorConfigDir(req Request, rules []string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "satelle-cursor-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	if auth := filepath.Join(cursorAuthDir(req), "auth.json"); fileExists(auth) {
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
	req.log.line("cursor permissions: ", written)
	return dir, cleanup, nil
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// cursorEvent is the union of the stream-json events read here.
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

// readCursorStream decodes every JSON-object line of out.
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

// parseCursor reads the final result event of a stream: its text and usage;
// the model from the system init event. An error result, a stream with no
// result, or a success with no answer is an error, never an empty verdict.
func parseCursor(out []byte) (Result, error) {
	evs := readCursorStream(out)
	if len(evs) == 0 {
		return Result{}, errors.New("cursor-agent: output is not a stream-json stream")
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
		return Result{}, errors.New("cursor-agent: stream ended without a result event")
	}
	if res.Model != "" {
		model = res.Model
	}
	if res.IsError || strings.HasPrefix(res.Subtype, "error") {
		return Result{}, fmt.Errorf("cursor-agent: run ended with %s: %s", res.Subtype, res.Result)
	}
	if strings.TrimSpace(res.Result) == "" {
		return Result{}, errors.New("cursor-agent: run ended with no answer")
	}
	r := Result{Text: res.Result, Model: model}
	if u := res.Usage; u != nil {
		r.TokensIn = u.InputTokens + u.CacheReadTokens + u.CacheWriteTokens
		r.TokensOut = u.OutputTokens
	}
	return r, nil
}

var cursorSignIn = regexp.MustCompile(`(?i)login|authenticat|sign in`)

// cursorFailure names why a run that did not exit cleanly failed: cursor-agent
// missing, an error result, no sign-in, else runOnce's error (with stderr).
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
