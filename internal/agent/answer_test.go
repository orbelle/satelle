package agent

import (
	"encoding/json"
	"testing"
)

func keepVerdict(raw json.RawMessage) bool {
	var v struct {
		Verdict string `json:"verdict"`
	}
	return json.Unmarshal(raw, &v) == nil && (v.Verdict == "accept" || v.Verdict == "reject")
}

func keepAny(json.RawMessage) bool { return true }

func TestLastObjectLenient(t *testing.T) {
	for _, c := range []struct{ text, notes string }{
		{`{"verdict":"accept","notes":"a \. b"}`, `a \. b`},
		{`{"verdict":"accept","notes":"\d+"}`, `\d+`},
		{`{"verdict":"accept","notes":"C:\Users"}`, `C:\Users`},
		{`{"verdict":"accept","notes":"x \u12 y"}`, `x \u12 y`},
		{`{"verdict":"reject","notes":"\. and \n and \\"}`, "\\. and \n and \\"},
	} {
		if _, ok := LastObject(c.text, keepVerdict); ok {
			t.Errorf("strict LastObject read %s", c.text)
		}
		raw, ok := LastObjectLenient(c.text, "notes", keepVerdict)
		if !ok || !json.Valid(raw) {
			t.Errorf("lenient %s: %q %v", c.text, raw, ok)
			continue
		}
		var v struct{ Notes string }
		if err := json.Unmarshal(raw, &v); err != nil || v.Notes != c.notes {
			t.Errorf("lenient %s: notes %q, want %q (%v)", c.text, v.Notes, c.notes, err)
		}
	}

	for _, text := range []string{
		`{"verdict":"accept","notes":"x","class":"A\C1"}`,
		`{"verdict":"acc\ept","notes":"x"}`,
		`{"verd\ict":"accept","notes":"x"}`,
		`{"verdict":"accept","not\es":"a \. b"}`,
		`{"verdict":"accept","notes":"a \. b","class":"A\C1"}`,
		`{"verdict":"accept","notes":"unterminated \.`,
	} {
		offered := 0
		LastObjectLenient(text, "notes", func(json.RawMessage) bool { offered++; return true })
		if offered != 0 {
			t.Errorf("%s: offered %d candidates, want none", text, offered)
		}
	}

	for _, text := range []string{
		"thinking {\"verdict\":\"reject\",\"notes\":\"first\"} then {\"x\":1} and finally {\"verdict\":\"accept\",\"notes\":\"ok\"} done",
		"```json\n{\"verdict\": \"accept\", \"notes\": \"a\\n\\t\\\"b\\\\.c\\/\\u00e9\"}\n```",
		`{"x":{"verdict":"accept","notes":"ok"}}`,
		`{"verdict":"\u0061ccept","notes":"x"}`,
		"no json here",
		`{"verdict":"maybe"}`,
	} {
		for _, keep := range []func(json.RawMessage) bool{keepVerdict, keepAny} {
			sraw, sok := LastObject(text, keep)
			lraw, lok := LastObjectLenient(text, "notes", keep)
			if sok != lok || string(sraw) != string(lraw) {
				t.Errorf("%s: strict %q %v, lenient %q %v", text, sraw, sok, lraw, lok)
			}
		}
	}
}

func TestRepairMemberRootOnly(t *testing.T) {
	text := `{"x":{"notes":"a \. b"}}`
	if out, ok := repairMember(text, "notes"); ok {
		t.Fatalf("outer root repaired a nested notes: %q", out)
	}
	out, ok := repairMember(text[5:], "notes")
	if !ok || out != `{"notes":"a \\. b"}` {
		t.Fatalf("inner root: %q %v", out, ok)
	}
	raw, ok := LastObjectLenient(text, "notes", keepAny)
	if !ok || string(raw) != `{"notes":"a \\. b"}` {
		t.Fatalf("scan: %q %v", raw, ok)
	}
}
