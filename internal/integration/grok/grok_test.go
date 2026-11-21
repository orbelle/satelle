package grok

import "testing"

func TestParseEnvelope(t *testing.T) {
	res, err := parse([]byte(`{"text":"hi","usage":{"input_tokens":6435,"cache_read_input_tokens":512,"output_tokens":24},"modelUsage":{"grok-b":{"outputTokens":24}}}`))
	if err != nil || res.Text != "hi" || res.TokensIn != 6947 || res.TokensOut != 24 || res.Model != "grok-b" {
		t.Fatalf("grok %+v %v", res, err)
	}
}
