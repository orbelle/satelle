package remotenode

var lineConfig = map[string]string{
	"satelle.toml": "[workflows]\ndefault = \"line\"\n",
	"agents.toml": `[agents.doer]
command = "claude"
tools = ["Read", "Edit"]
placement = "remote:ci"
[agents.r1]
command = "claude"
tools = ["Read"]
`,
	"skills/do.md":     "Do the work.\n",
	"skills/review.md": "Review.\n",
	"workflows/line.toml": `[states.a]
start = true
[states.b]
performer = { agent = "doer", skill = "do" }
[states.c]
terminal = true
[gates.review]
skill = "review"
agent = "r1"
[[transition]]
from = ["a"]
to = "b"
[[transition]]
from = ["b"]
to = "c"
groups = [{ gates = ["review"] }]
`,
}

func ptr[T any](v T) *T { return &v }
