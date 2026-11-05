package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// grok runs grok in command mode: `grok -p <prompt> --output-format json` with
// --tools naming grok's own tools for the grant and --deny for each deny.
type grok struct{}

func (grok) Name() string         { return PresetGrok }
func (grok) EnforcesGrants() bool { return true }

// grokTools translates the grant vocabulary into grok's tool names.
var grokTools = map[string]string{
	ToolRead:  "read_file",
	ToolGrep:  "grep",
	ToolGlob:  "list_dir",
	ToolEdit:  "search_replace",
	ToolWrite: "write_file",
	ToolBash:  "run_terminal_cmd",
}

func translate(adapter string, names map[string][]string, tools []string) ([]string, error) {
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

func single(m map[string]string) map[string][]string {
	out := map[string][]string{}
	for k, v := range m {
		out[k] = []string{v}
	}
	return out
}

func (grok) Argv(req Request) ([]string, error) {
	b := req.Binding
	bin, extra, err := commandFields(b)
	if err != nil {
		return nil, err
	}
	plain, scoped := splitDenies(b.Tools, req.denies())
	if len(scoped) > 0 {
		return nil, fmt.Errorf("grok adapter cannot express the scoped deny %s on a grant that runs a shell", strings.Join(scoped, ", "))
	}
	tools, err := translate(PresetGrok, single(grokTools), b.Tools)
	if err != nil {
		return nil, err
	}
	deny, err := translate(PresetGrok, single(grokTools), plain)
	if err != nil {
		return nil, err
	}
	argv := append([]string{bin}, extra...)
	argv = append(argv, "-p", req.Prompt, "--output-format", "json", "--always-approve", "--no-subagents")
	if b.Model != "" {
		argv = append(argv, "-m", b.Model)
	}
	if b.Effort != "" {
		argv = append(argv, "--reasoning-effort", b.Effort)
	}
	if req.System != "" {
		argv = append(argv, "--system-prompt-override", req.System)
	}
	argv = append(argv, "--tools", strings.Join(tools, ","))
	for _, d := range deny {
		argv = append(argv, "--deny", d)
	}
	return argv, nil
}

func (g grok) Run(ctx context.Context, req Request) (Result, error) {
	argv, err := g.Argv(req)
	if err != nil {
		return Result{}, err
	}
	out, err := runOnce(ctx, argv, "", req)
	if err != nil {
		return Result{}, err
	}
	return parseGrok(out)
}

// grokEnvelope is grok's `--output-format json` object: the reply in text,
// Anthropic-shaped usage when grok reports it.
type grokEnvelope struct {
	Text  string `json:"text"`
	Usage *struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
	ModelUsage map[string]struct {
		OutputTokens int `json:"outputTokens"`
	} `json:"modelUsage"`
}

func parseGrok(out []byte) (Result, error) {
	var env grokEnvelope
	if err := json.Unmarshal(out, &env); err != nil {
		return Result{Text: string(out)}, fmt.Errorf("grok: output is not a json envelope: %w", err)
	}
	res := Result{Text: env.Text}
	if u := env.Usage; u != nil {
		res.TokensIn = u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
		res.TokensOut = u.OutputTokens
	}
	res.Model = claudeEnvelope{ModelUsage: env.ModelUsage}.model()
	return res, nil
}
