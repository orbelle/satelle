package claude

import "testing"

func TestParseEnvelope(t *testing.T) {
	res, err := parse([]byte(`{"type":"result","subtype":"success","is_error":false,"result":"ok","usage":{"input_tokens":22,"cache_creation_input_tokens":11000,"cache_read_input_tokens":2500,"output_tokens":300},"modelUsage":{"claude-x":{"outputTokens":300}}}`))
	if err != nil || res.Text != "ok" || res.TokensIn != 13522 || res.TokensOut != 300 || res.Model != "claude-x" {
		t.Fatalf("claude %+v %v", res, err)
	}
	if _, err := parse([]byte(`{"subtype":"error_max_turns","is_error":true}`)); err == nil {
		t.Fatal("error envelope accepted")
	}
}
