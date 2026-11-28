package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/bobmcallan/satelle/internal/integration/common"
)

const (
	Name = "claude"
	CLI  = "claude"
)

type Dispatch struct{}

func (Dispatch) Name() string         { return CLI }
func (Dispatch) EnforcesGrants() bool { return true }

func (Dispatch) Confinement() common.Confinement {
	return common.Confinement{
		ProjectFiles: []string{".claude/settings.json", ".claude/settings.local.json", ".mcp.json"},
		AuthEnv:      []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CONFIG_DIR"},
		Context:      true,
		FileTools:    true,
		Shell:        true,
	}
}

const TargetSandbox = `{"sandbox":{"enabled":true,"autoAllowBashIfSandboxed":true,"allowUnsandboxedCommands":false,"excludedCommands":[]}}`

func (Dispatch) Argv(req common.Request) ([]string, error) {
	b := req.Binding
	bin, extra, err := common.CommandFields(b)
	if err != nil {
		return nil, err
	}
	var offered []string
	for _, t := range b.Tools {
		base := t
		if common.IsScoped(t) {
			base = common.ToolBash
		}
		if !slices.Contains(offered, base) {
			offered = append(offered, base)
		}
	}
	argv := append([]string{bin}, extra...)
	argv = append(argv, "-p", "--output-format", "json")
	if b.Model != "" {
		argv = append(argv, "--model", b.Model)
	}
	if b.Effort != "" {
		argv = append(argv, "--effort", b.Effort)
	}
	argv = append(argv, "--tools", strings.Join(offered, ","))
	if len(b.Tools) > 0 {
		argv = append(argv, "--allowedTools", strings.Join(b.Tools, ","))
	}
	plain, scoped := common.SplitDenies(b.Tools, req.Denies())
	if deny := append(plain, scoped...); len(deny) > 0 {
		argv = append(argv, "--disallowedTools", strings.Join(deny, ","))
	}
	if t := req.Target; t != nil {
		argv = append(argv, "--setting-sources", "user", "--restricted")
		for _, d := range t.ReadDirs {
			argv = append(argv, "--add-dir", d)
		}
		if common.GrantsBash(b.Tools) {
			argv = append(argv, "--settings", TargetSandbox)
		}
	}
	if req.System != "" {
		argv = append(argv, "--append-system-prompt", req.System)
	}
	return argv, nil
}

func (c Dispatch) Run(ctx context.Context, req common.Request) (common.Result, error) {
	argv, err := c.Argv(req)
	if err != nil {
		return common.Result{}, err
	}
	out, err := common.RunOnce(ctx, argv, req.Prompt, req)
	if err != nil {
		return common.Result{}, err
	}
	return parse(out)
}

type envelope struct {
	Result  string `json:"result"`
	IsError bool   `json:"is_error"`
	Subtype string `json:"subtype"`
	Usage   *struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
	ModelUsage common.ModelUsage `json:"modelUsage"`
}

func (e envelope) tokens() (in, out int) {
	if e.Usage == nil {
		return 0, 0
	}
	u := e.Usage
	return u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens, u.OutputTokens
}

func parse(out []byte) (common.Result, error) {
	var env envelope
	if err := json.Unmarshal(out, &env); err != nil {
		return common.Result{Text: string(out)}, fmt.Errorf("claude: output is not a json envelope: %w", err)
	}
	res := common.Result{Text: env.Result, Model: env.ModelUsage.SelectModelByOutput()}
	res.TokensIn, res.TokensOut = env.tokens()
	if env.IsError || strings.HasPrefix(env.Subtype, "error") {
		return res, fmt.Errorf("claude: run ended with %s: %s", env.Subtype, env.Result)
	}
	return res, nil
}
