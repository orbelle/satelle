package grok

import (
	"slices"
	"testing"

	"github.com/bobmcallan/satelle/internal/integration/common"
)

func TestArgvTargetSandbox(t *testing.T) {
	req := common.Request{Binding: common.Binding{Name: "r", Command: "grok", Tools: []string{"Read", "Grep", "Glob"}}}
	argv, err := Dispatch{}.Argv(req)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(argv, "--sandbox") {
		t.Fatalf("self argv %q", argv)
	}
	req.Target = &common.Target{Worktree: "/tgt"}
	argv, err = Dispatch{}.Argv(req)
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.Index(argv, "--sandbox"); i < 0 || argv[i+1] != "workspace" || slices.Contains(argv, "/tgt") {
		t.Fatalf("target argv %q", argv)
	}
}

func TestParseEnvelope(t *testing.T) {
	res, err := parse([]byte(`{"text":"hi","usage":{"input_tokens":6435,"cache_read_input_tokens":512,"output_tokens":24},"modelUsage":{"grok-b":{"outputTokens":24}}}`))
	if err != nil || res.Text != "hi" || res.TokensIn != 6947 || res.TokensOut != 24 || res.Model != "grok-b" {
		t.Fatalf("grok %+v %v", res, err)
	}
}
