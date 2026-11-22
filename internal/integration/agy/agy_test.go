package agy

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/integration/common"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAgyForRules(t *testing.T) {
	ro, err := agyFor(common.Binding{Tools: []string{"Read", "Grep", "Glob"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ro.Allow, []string{"read_file(*)"}) ||
		!slices.Equal(ro.Deny, []string{"write_file(*)", "command(*)", "unsandboxed(*)", "mcp(*)", "read_url(*)", "execute_url(*)", "download_url(*)"}) ||
		!slices.Equal(ro.Tools, []string{"read_file"}) {
		t.Fatalf("read-only grant %+v", ro)
	}
	rw, err := agyFor(common.Binding{Tools: []string{"Read", "Grep", "Glob", "Edit", "Write"}}, []string{"Bash(satelle story set:*)"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rw.Allow, []string{"read_file(*)", "write_file(*)"}) ||
		!slices.Equal(rw.Deny, []string{"command(*)", "unsandboxed(*)", "mcp(*)", "read_url(*)", "execute_url(*)", "download_url(*)"}) ||
		!slices.Equal(rw.Tools, []string{"read_file", "write_file", "edit_file"}) {
		t.Fatalf("edit grant %+v", rw)
	}
	for _, g := range []agyGrant{ro, rw} {
		all := append(append([]string{}, g.Allow...), g.Deny...)
		slices.Sort(all)
		var want []string
		for _, a := range actions {
			want = append(want, a+"(*)")
		}
		slices.Sort(want)
		if !slices.Equal(all, want) {
			t.Errorf("allow and deny %v do not cover every action %v", all, want)
		}
	}
}

func TestParseAgyStream(t *testing.T) {
	res, err := parseAgy(fixture(t, "agy-stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "OK\n" || res.TokensIn != 10967 || res.TokensOut != 26 || res.Model != "gemini-4-hydrogen-high" {
		t.Fatalf("result %+v", res)
	}

	multi := fixture(t, "agy-stream-multiturn.jsonl")
	var usage struct{ InputTokens, OutputTokens, CacheReadTokens int }
	for _, l := range strings.Split(string(multi), "\n") {
		var e struct {
			Event  string `json:"event"`
			Result struct {
				Usage struct {
					InputTokens     int `json:"input_tokens"`
					OutputTokens    int `json:"output_tokens"`
					CacheReadTokens int `json:"cache_read_tokens"`
				} `json:"usage"`
			} `json:"result"`
		}
		if json.Unmarshal([]byte(l), &e) == nil && e.Event == "result" {
			usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens = e.Result.Usage.InputTokens, e.Result.Usage.OutputTokens, e.Result.Usage.CacheReadTokens
		}
	}
	if usage.CacheReadTokens == 0 {
		t.Fatal("the multi-turn fixture reads no cached tokens")
	}
	res, err = parseAgy(multi)
	if err != nil {
		t.Fatal(err)
	}
	if res.TokensIn != usage.InputTokens+usage.CacheReadTokens || res.TokensOut != usage.OutputTokens || res.Model == "" || strings.TrimSpace(res.Text) == "" {
		t.Fatalf("multi-turn result %+v, usage %+v", res, usage)
	}

	if _, err := parseAgy(fixture(t, "agy-stream-error.jsonl")); err == nil || !strings.Contains(err.Error(), "run ended with ERROR") || !strings.Contains(err.Error(), "does-not-exist-model") {
		t.Fatalf("error stream: %v", err)
	}

	lines := strings.SplitN(string(fixture(t, "agy-stream.jsonl")), "\n", 2)
	res, err = parseAgy([]byte(lines[1]))
	if err != nil || res.Model != "unavailable (agy)" {
		t.Fatalf("no init: %+v %v", res, err)
	}

	for want, stream := range map[string]string{
		"no answer":              `{"event":"result","result":{"status":"SUCCESS","response":"  \n"}}`,
		"without a result event": `{"event":"init","init":{"model":"m"}}`,
		"not a stream-json":      `plain text`,
	} {
		if _, err := parseAgy([]byte(stream + "\n")); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", want, err)
		}
	}
}
