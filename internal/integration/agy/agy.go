package agy

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

const CLI = "agy"

const (
	settingsPlaceholder = "<settings file>"
	guardPlaceholder    = "<guard dir>"
	dirPlaceholder      = "<run dir>"
	unavailableModel    = "unavailable (agy)"
	denyReason          = "denied by satelle dispatch grant"
	guardOwner          = "satelle-dispatch-guard"
	autoUpdateEnv       = "AGY_CLI_DISABLE_AUTO_UPDATE=true"
)

var actions = []string{"read_file", "write_file", "command", "unsandboxed", "mcp", "read_url", "execute_url", "download_url"}

var efforts = []string{"low", "medium", "high", "xhigh", "max"}

type Dispatch struct{}

func (Dispatch) Name() string         { return CLI }
func (Dispatch) EnforcesGrants() bool { return true }

type agyGrant struct {
	Allow []string
	Deny  []string
	Tools []string
}

func agyFor(b common.Binding, denies []string) (agyGrant, error) {
	plain, _ := common.SplitDenies(b.Tools, denies)
	grant := common.Granted(b, plain)
	has := func(t string) bool { return slices.Contains(grant, t) }
	if common.GrantsBash(grant) {
		return agyGrant{}, errors.New(`agy cannot hold a shell to satelle's performer denies: its command(...) deny rules match the command text, so a variable expansion such as CMD="satelle story"; $CMD set escapes them; grant no Bash (see satelle help agents)`)
	}
	if has(common.ToolNotebookEdit) {
		return agyGrant{}, fmt.Errorf("agy cannot express the grant %s", common.ToolNotebookEdit)
	}
	if !(has(common.ToolRead) && has(common.ToolGrep) && has(common.ToolGlob)) {
		return agyGrant{}, errors.New("agy grants Read, Grep and Glob together")
	}
	if has(common.ToolEdit) != has(common.ToolWrite) {
		return agyGrant{}, errors.New("agy has one write permission (write_file): grant Edit and Write together")
	}
	allowed := []string{"read_file"}
	tools := []string{"read_file"}
	if has(common.ToolEdit) {
		allowed = append(allowed, "write_file")
		tools = append(tools, "write_file", "edit_file")
	}
	g := agyGrant{Tools: tools}
	for _, a := range actions {
		rule := a + "(*)"
		if slices.Contains(allowed, a) {
			g.Allow = append(g.Allow, rule)
		} else {
			g.Deny = append(g.Deny, rule)
		}
	}
	return g, nil
}

func checkEffort(b common.Binding) error {
	if b.Effort == "" {
		return nil
	}
	if !slices.Contains(efforts, b.Effort) {
		return fmt.Errorf("agy effort must be one of %s; got %q", strings.Join(efforts, ", "), b.Effort)
	}
	for _, e := range efforts {
		if strings.HasSuffix(b.Model, "-"+e) {
			return fmt.Errorf("agy model %q already carries an effort; drop effort %q or use the base model id", b.Model, b.Effort)
		}
	}
	return nil
}

func (d Dispatch) Argv(req common.Request) ([]string, error) {
	return d.argv(req, settingsPlaceholder, guardPlaceholder, dirPlaceholder)
}

func (Dispatch) argv(req common.Request, settings, guard, dir string) ([]string, error) {
	b := req.Binding
	bin, extra, err := common.CommandFields(b)
	if err != nil {
		return nil, err
	}
	if err := checkEffort(b); err != nil {
		return nil, err
	}
	if _, err := agyFor(b, req.Denies()); err != nil {
		return nil, err
	}
	argv := append([]string{bin}, extra...)
	argv = append(argv, "--input-format", "stream-json", "--output-format", "stream-json", "--disable-slash-commands",
		"--settings", settings, "--add-dir", dir, "--add-dir", guard)
	if b.Model != "" {
		argv = append(argv, "--model", b.Model)
	}
	if b.Effort != "" {
		argv = append(argv, "--effort", b.Effort)
	}
	return argv, nil
}

func agyStdin(req common.Request) (string, error) {
	content := req.Prompt
	if req.System != "" {
		content = "<system>\n" + req.System + "\n</system>\n\n" + req.Prompt
	}
	line, err := json.Marshal(map[string]any{"event": "user", "message": map[string]any{"content": content}})
	if err != nil {
		return "", err
	}
	return string(line) + "\n", nil
}

func (d Dispatch) Run(ctx context.Context, req common.Request) (common.Result, error) {
	if _, err := d.Argv(req); err != nil {
		return common.Result{}, err
	}
	g, _ := agyFor(req.Binding, req.Denies())
	home, err := os.UserHomeDir()
	if err != nil {
		return common.Result{}, err
	}
	dir := req.Dir
	if dir == "" {
		if dir, err = os.Getwd(); err != nil {
			return common.Result{}, err
		}
	}
	if dir, err = filepath.Abs(dir); err != nil {
		return common.Result{}, err
	}
	if err := agyHostConfig(dir, home); err != nil {
		return common.Result{}, err
	}
	stdin, err := agyStdin(req)
	if err != nil {
		return common.Result{}, err
	}
	tmp, err := os.MkdirTemp("", "satelle-agy-")
	if err != nil {
		return common.Result{}, err
	}
	defer os.RemoveAll(tmp)
	settings, guard, err := agyFiles(req, tmp, g)
	if err != nil {
		return common.Result{}, err
	}
	argv, err := d.argv(req, settings, guard, dir)
	if err != nil {
		return common.Result{}, err
	}
	req.Env = append(append([]string{}, req.Env...), autoUpdateEnv)
	out, err := common.RunOnce(ctx, argv, stdin, req)
	if err != nil {
		return common.Result{}, agyFailure(argv[0], out, err)
	}
	return parseAgy(out)
}

func agyFiles(req common.Request, tmp string, g agyGrant) (settings, guard string, err error) {
	perms, err := json.Marshal(map[string]any{"permissions": map[string]any{"allow": g.Allow, "deny": g.Deny}})
	if err != nil {
		return "", "", err
	}
	settings = filepath.Join(tmp, "settings.json")
	if err := os.WriteFile(settings, append(perms, '\n'), 0o600); err != nil {
		return "", "", err
	}
	hooks, err := guardHooks(g.Tools)
	if err != nil {
		return "", "", err
	}
	guard = filepath.Join(tmp, "guard")
	if err := os.MkdirAll(filepath.Join(guard, ".agents"), 0o700); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(filepath.Join(guard, ".agents", "hooks.json"), append(hooks, '\n'), 0o600); err != nil {
		return "", "", err
	}
	req.Trace.Line("agy permissions: ", perms)
	compact := &bytes.Buffer{}
	if json.Compact(compact, hooks) == nil {
		req.Trace.Line("agy guard: ", compact.Bytes())
	}
	return settings, guard, nil
}

var lifecycleEvents = []string{"PreInvocation", "PostInvocation", "Stop", "PostToolUse"}

func agyHostConfig(dir, home string) error {
	for d := dir; ; {
		if err := refuseHooksFile(filepath.Join(d, ".agents", "hooks.json")); err != nil {
			return err
		}
		if err := workspaceSettings(filepath.Join(d, ".antigravity", "settings.json")); err != nil {
			return err
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	if err := refuseHooksFile(filepath.Join(home, ".agents", "hooks.json")); err != nil {
		return err
	}
	for _, p := range []string{
		filepath.Join(home, ".gemini", "config", "hooks.json"),
		filepath.Join(home, ".gemini", "antigravity-cli", "hooks.json"),
	} {
		if err := userHooks(p); err != nil {
			return err
		}
	}
	return userSettings(filepath.Join(home, ".gemini", "antigravity-cli", "settings.json"))
}

func refuseHooksFile(p string) error {
	if !common.FileExists(p) {
		return nil
	}
	if b, err := os.ReadFile(p); err == nil && satelleOnly(b) {
		return nil
	}
	return fmt.Errorf("agy: %s defines hooks that run in every agy run under it; satelle will not run repository hook code in a dispatch", p)
}

func satelleOnly(b []byte) bool {
	var doc map[string]map[string][]json.RawMessage
	if json.Unmarshal(b, &doc) != nil || len(doc) != 1 {
		return false
	}
	events, ok := doc[owner]
	if !ok || len(events) == 0 {
		return false
	}
	for _, entries := range events {
		for _, raw := range entries {
			if !satelleEntry(raw) {
				return false
			}
		}
	}
	return true
}

func satelleEntry(raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	if _, grouped := fields["hooks"]; !grouped {
		return satelleHandler(raw)
	}
	for k := range fields {
		if k != "matcher" && k != "hooks" {
			return false
		}
	}
	var group struct {
		Matcher *string           `json:"matcher"`
		Hooks   []json.RawMessage `json:"hooks"`
	}
	if json.Unmarshal(raw, &group) != nil || len(group.Hooks) == 0 {
		return false
	}
	for _, h := range group.Hooks {
		if !satelleHandler(h) {
			return false
		}
	}
	return true
}

func satelleHandler(raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	for k := range fields {
		if k != "type" && k != "command" && k != "timeout" {
			return false
		}
	}
	var h struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Timeout *int   `json:"timeout"`
	}
	if json.Unmarshal(raw, &h) != nil || h.Type != "command" {
		return false
	}
	for _, verb := range common.Verbs {
		if h.Command == common.Command(verb, Name) {
			return true
		}
	}
	return false
}

func workspaceSettings(p string) error {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var doc struct {
		Permissions struct {
			Allow []string `json:"allow"`
			Ask   []string `json:"ask"`
		} `json:"permissions"`
		ToolPermission json.RawMessage `json:"toolPermission"`
		AgentMode      json.RawMessage `json:"agentMode"`
		Hooks          json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return fmt.Errorf("agy: %s does not decode (%v); satelle cannot hold the run to its grant there", p, err)
	}
	var set []string
	if len(doc.Permissions.Allow) > 0 {
		set = append(set, "permissions.allow")
	}
	if len(doc.Permissions.Ask) > 0 {
		set = append(set, "permissions.ask")
	}
	for _, f := range []struct {
		name string
		raw  json.RawMessage
	}{{"toolPermission", doc.ToolPermission}, {"agentMode", doc.AgentMode}, {"hooks", doc.Hooks}} {
		if len(f.raw) > 0 && string(f.raw) != "null" {
			set = append(set, f.name)
		}
	}
	if len(set) > 0 {
		return fmt.Errorf("agy: %s sets %s, which satelle's per-run settings would have to override; remove it to dispatch agy here", p, strings.Join(set, ", "))
	}
	return nil
}

func userHooks(p string) error {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var doc map[string]map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		return fmt.Errorf("agy: %s does not decode (%v); satelle cannot hold the run to its grant", p, err)
	}
	owners := make([]string, 0, len(doc))
	for o := range doc {
		owners = append(owners, o)
	}
	slices.Sort(owners)
	for _, o := range owners {
		events := make([]string, 0, len(doc[o]))
		for e := range doc[o] {
			events = append(events, e)
		}
		slices.Sort(events)
		for _, e := range events {
			if !slices.Contains(lifecycleEvents, e) {
				return fmt.Errorf("agy: %s defines a %s hook, which can decide a tool call; satelle cannot hold the run to its grant", p, e)
			}
		}
	}
	return nil
}

func userSettings(p string) error {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var doc struct {
		Hooks json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return fmt.Errorf("agy: %s does not decode (%v); satelle cannot hold the run to its grant", p, err)
	}
	if len(doc.Hooks) > 0 && string(doc.Hooks) != "null" {
		return fmt.Errorf("agy: %s sets hooks, which can decide a tool call; satelle cannot hold the run to its grant", p)
	}
	return nil
}

type agyEvent struct {
	Event string `json:"event"`
	Init  *struct {
		Model string `json:"model"`
	} `json:"init"`
	Result *struct {
		Status         string `json:"status"`
		Response       string `json:"response"`
		Error          string `json:"error"`
		FinishedReason string `json:"finished_reason"`
		Usage          *struct {
			InputTokens     int `json:"input_tokens"`
			OutputTokens    int `json:"output_tokens"`
			ThinkingTokens  int `json:"thinking_tokens"`
			CacheReadTokens int `json:"cache_read_tokens"`
			TotalTokens     int `json:"total_tokens"`
		} `json:"usage"`
	} `json:"result"`
}

func readAgyStream(out []byte) []agyEvent {
	var evs []agyEvent
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64*1024), 64*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var e agyEvent
		if json.Unmarshal(line, &e) == nil && e.Event != "" {
			evs = append(evs, e)
		}
	}
	return evs
}

func parseAgy(out []byte) (common.Result, error) {
	evs := readAgyStream(out)
	if len(evs) == 0 {
		return common.Result{}, errors.New("agy: output is not a stream-json stream")
	}
	model := ""
	var res *agyEvent
	for i := range evs {
		switch {
		case evs[i].Event == "init" && evs[i].Init != nil && model == "":
			model = evs[i].Init.Model
		case evs[i].Event == "result" && evs[i].Result != nil:
			res = &evs[i]
		}
	}
	if res == nil {
		return common.Result{}, errors.New("agy: stream ended without a result event")
	}
	r := res.Result
	if r.Status != "SUCCESS" || r.Error != "" || r.FinishedReason == "error" {
		return common.Result{}, fmt.Errorf("agy: run ended with %s: %s", r.Status, r.Error)
	}
	if strings.TrimSpace(r.Response) == "" {
		return common.Result{}, errors.New("agy: run ended with no answer")
	}
	if model == "" {
		model = unavailableModel
	}
	got := common.Result{Text: r.Response, Model: model}
	if u := r.Usage; u != nil {
		got.TokensIn = u.InputTokens + u.CacheReadTokens
		got.TokensOut = u.OutputTokens
	}
	return got, nil
}

var agySignIn = regexp.MustCompile(`(?i)login|authenticat|sign in`)

func agyFailure(bin string, out []byte, err error) error {
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("agy is not installed or not on PATH (%s): %w", bin, err)
	}
	if _, perr := parseAgy(out); perr != nil && strings.HasPrefix(perr.Error(), "agy: run ended with") {
		return perr
	}
	if agySignIn.MatchString(string(out)) || agySignIn.MatchString(err.Error()) {
		return fmt.Errorf("agy is not signed in on this machine: %w", err)
	}
	return err
}
