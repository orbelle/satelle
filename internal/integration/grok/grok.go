package grok

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/bobmcallan/satelle/internal/integration/common"
)

const (
	Name = "grok"
	CLI  = "grok"
)

type Dispatch struct{}

const promptFilePlaceholder = "<prompt file>"

func (Dispatch) Name() string         { return CLI }
func (Dispatch) EnforcesGrants() bool { return true }

var grokTools = map[string]string{
	common.ToolRead:  "read_file",
	common.ToolGrep:  "grep",
	common.ToolGlob:  "list_dir",
	common.ToolEdit:  "search_replace",
	common.ToolWrite: "write_file",
	common.ToolBash:  "run_terminal_cmd",
}

func (d Dispatch) Argv(req common.Request) ([]string, error) {
	return d.argv(req, promptFilePlaceholder)
}

func (Dispatch) argv(req common.Request, promptFile string) ([]string, error) {
	b := req.Binding
	bin, extra, err := common.CommandFields(b)
	if err != nil {
		return nil, err
	}
	plain, scoped := common.SplitDenies(b.Tools, req.Denies())
	if len(scoped) > 0 {
		return nil, fmt.Errorf("grok adapter cannot express the scoped deny %s on a grant that runs a shell", strings.Join(scoped, ", "))
	}
	tools, err := common.TranslateTools(CLI, common.SingleToolMap(grokTools), b.Tools)
	if err != nil {
		return nil, err
	}
	deny, err := common.TranslateTools(CLI, common.SingleToolMap(grokTools), plain)
	if err != nil {
		return nil, err
	}
	argv := append([]string{bin}, extra...)
	argv = append(argv, "--prompt-file", promptFile, "--output-format", "json", "--always-approve", "--no-subagents")
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

func (g Dispatch) Run(ctx context.Context, req common.Request) (common.Result, error) {
	f, err := os.CreateTemp("", "satelle-grok-prompt-*")
	if err != nil {
		return common.Result{}, err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(req.Prompt); err != nil {
		f.Close()
		return common.Result{}, err
	}
	if err := f.Close(); err != nil {
		return common.Result{}, err
	}
	argv, err := g.argv(req, f.Name())
	if err != nil {
		return common.Result{}, err
	}
	out, err := common.RunOnce(ctx, argv, "", req)
	if err != nil {
		return common.Result{}, err
	}
	return parse(out)
}

type envelope struct {
	Text  string `json:"text"`
	Usage *struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
	ModelUsage common.ModelUsage `json:"modelUsage"`
}

func parse(out []byte) (common.Result, error) {
	var env envelope
	if err := json.Unmarshal(out, &env); err != nil {
		return common.Result{Text: string(out)}, fmt.Errorf("grok: output is not a json envelope: %w", err)
	}
	res := common.Result{Text: env.Text}
	if u := env.Usage; u != nil {
		res.TokensIn = u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
		res.TokensOut = u.OutputTokens
	}
	res.Model = env.ModelUsage.SelectModelByOutput()
	return res, nil
}
