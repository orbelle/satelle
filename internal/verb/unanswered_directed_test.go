package verb_test

import (
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/verb"
)

func TestUnansweredDirected(t *testing.T) {
	m := func(from, to, cc, body string) verb.AgentMessage {
		return verb.AgentMessage{From: from, To: to, Cc: cc, Body: body}
	}
	cases := []struct {
		name string
		msgs []verb.AgentMessage
		want []string
	}{
		{"directed and unanswered", []verb.AgentMessage{m("orchestrator", "coder", "", "a")}, []string{"a"}},
		{"answered by a later coder message", []verb.AgentMessage{m("orchestrator", "coder", "", "a"), m("coder", "consult", "*", "ok")}, nil},
		{"directed after the coder's last reply", []verb.AgentMessage{m("orchestrator", "coder", "", "a"), m("coder", "consult", "*", "ok"), m("orchestrator", "coder", "", "b")}, []string{"b"}},
		{"every unanswered one is kept", []verb.AgentMessage{m("orchestrator", "coder", "", "a"), m("developer", "coder", "", "b")}, []string{"a", "b"}},
		{"answered by a coder reply to another role, no cc", []verb.AgentMessage{m("orchestrator", "coder", "", "a"), m("coder", "orchestrator", "", "done")}, nil},
		{"broadcast only", []verb.AgentMessage{m("orchestrator", "*", "", "a")}, nil},
		{"relay transcript row is not a directive", []verb.AgentMessage{m("consult", "coder", "*", "READY")}, nil},
		{"addressed to someone else", []verb.AgentMessage{m("orchestrator", "planner", "", "a")}, nil},
	}
	for _, c := range cases {
		var got []string
		for _, x := range verb.UnansweredDirected(c.msgs, "coder") {
			got = append(got, x.Body)
		}
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
