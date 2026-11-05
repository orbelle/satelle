package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// claude runs `claude -p --output-format json`, the prompt on stdin. --tools
// limits the tools offered, --allowedTools approves the grant, and
// --disallowedTools carries every deny.
type claude struct{}

func (claude) Name() string         { return PresetClaude }
func (claude) EnforcesGrants() bool { return true }

func (claude) Argv(req Request) ([]string, error) {
	b := req.Binding
	bin, extra, err := commandFields(b)
	if err != nil {
		return nil, err
	}
	var offered []string
	for _, t := range b.Tools {
		base := t
		if IsScoped(t) {
			base = ToolBash
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
	plain, scoped := splitDenies(b.Tools, req.denies())
	if deny := append(plain, scoped...); len(deny) > 0 {
		argv = append(argv, "--disallowedTools", strings.Join(deny, ","))
	}
	if req.System != "" {
		argv = append(argv, "--append-system-prompt", req.System)
	}
	return argv, nil
}

func (c claude) Run(ctx context.Context, req Request) (Result, error) {
	argv, err := c.Argv(req)
	if err != nil {
		return Result{}, err
	}
	out, err := runOnce(ctx, argv, req.Prompt, req)
	if err != nil {
		return Result{}, err
	}
	return parseClaude(out)
}

// claudeEnvelope is the final object of `claude -p --output-format json`.
// Cache tokens are disjoint parts of the prompt and count as input.
type claudeEnvelope struct {
	Result  string `json:"result"`
	IsError bool   `json:"is_error"`
	Subtype string `json:"subtype"`
	Usage   *struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
	ModelUsage map[string]struct {
		OutputTokens int `json:"outputTokens"`
	} `json:"modelUsage"`
}

func (e claudeEnvelope) tokens() (in, out int) {
	if e.Usage == nil {
		return 0, 0
	}
	u := e.Usage
	return u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens, u.OutputTokens
}

// model is the model that produced the most output.
func (e claudeEnvelope) model() string {
	best, most := "", -1
	for name, m := range e.ModelUsage {
		if m.OutputTokens > most || (m.OutputTokens == most && name < best) {
			best, most = name, m.OutputTokens
		}
	}
	return best
}

func parseClaude(out []byte) (Result, error) {
	var env claudeEnvelope
	if err := json.Unmarshal(out, &env); err != nil {
		return Result{Text: string(out)}, fmt.Errorf("claude: output is not a json envelope: %w", err)
	}
	res := Result{Text: env.Result, Model: env.model()}
	res.TokensIn, res.TokensOut = env.tokens()
	if env.IsError || strings.HasPrefix(env.Subtype, "error") {
		return res, fmt.Errorf("claude: run ended with %s: %s", env.Subtype, env.Result)
	}
	return res, nil
}
