package agent

import (
	"strings"
	"testing"
)

func TestParseArtifact(t *testing.T) {
	cases := []struct {
		name, text string
		want       *Artifact
		err        string // substring of the error; "" means no error
	}{
		{name: "fenced final object", text: "Here is the plan.\n```json\n{\"artifact\":{\"name\":\"plan\",\"type\":\"plan\",\"body\":\"# P\"}}\n```\n",
			want: &Artifact{Name: "plan", Type: "plan", Body: "# P"}},
		{name: "sibling answers key", text: `{"artifact":{"name":"plan","type":"plan","body":"# P"},"answers":[{"id":"f1","answer":"x"}]}`,
			want: &Artifact{Name: "plan", Type: "plan", Body: "# P"}},
		{name: "example then final", text: `e.g. {"artifact":{"name":"example","type":"plan","body":"x"}} then {"artifact":{"name":"plan","type":"plan","body":"# Real"}}`,
			want: &Artifact{Name: "plan", Type: "plan", Body: "# Real"}},
		{name: "escaped mention in body", text: `{"artifact":{"name":"plan","type":"plan","body":"the \"artifact\": shape"}}`,
			want: &Artifact{Name: "plan", Type: "plan", Body: `the "artifact": shape`}},
		{name: "plain prose", text: "I made the change."},
		{name: "verdict only", text: `{"verdict":"reject","notes":"premise fails"}`},
		{name: "the word artifact", text: "no artifact needed here"},
		{name: "missing name", text: `{"artifact":{"type":"plan","body":"# P"}}`, err: "name is missing"},
		{name: "blank body", text: `{"artifact":{"name":"plan","type":"plan","body":"  "}}`, err: "body is missing"},
		{name: "bad type", text: `{"artifact":{"name":"plan","type":"a plan","body":"# P"}}`, err: "not a valid media-type parameter"},
		{name: "artifact not an object", text: `{"artifact":"plan"}`, err: "must be an object"},
		{name: "truncated", text: `{"artifact":{"name":"plan","type":"plan","body":"# P`, err: "not valid JSON"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseArtifact(c.text)
			if c.err != "" {
				if err == nil || !strings.HasPrefix(err.Error(), "malformed artifact:") || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("err = %v, want malformed artifact: …%s…", err, c.err)
				}
				if got != nil {
					t.Fatalf("got %+v with an error", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestArtifactMediaType(t *testing.T) {
	if got := (Artifact{Type: "plan"}).MediaType(); got != "text/markdown; type=plan" {
		t.Fatalf("MediaType = %q", got)
	}
}

func TestValidType(t *testing.T) {
	if !ValidType("deploy-record") || ValidType("bad type") || ValidType("") {
		t.Error("ValidType")
	}
}
