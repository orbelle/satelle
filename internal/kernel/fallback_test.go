package kernel

import (
	"path/filepath"
	"strings"
	"testing"
)

const fallbackAgents = `
[agents.doer]
command = "claude"
model = "big"
tools = ["Read", "Edit"]
[agents.writer]
command = "claude"
model = "w"
tools = ["Read", "Edit"]
[agents.me]
command = "session"
[agents.r1]
command = "grok"
model = "m1"
tools = ["Read", "Grep", "Glob"]
[agents.r2]
command = "cursor-agent"
model = "m2"
tools = ["Read", "Grep", "Glob"]
[agents.r3]
command = "claude"
model = "m3"
tools = ["Read", "Grep", "Glob"]
`

func fallbackRoot(t *testing.T, judges, settings, fallbacks, local string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".satelle")
	write(t, filepath.Join(root, "satelle.toml"), "[workflows]\ndefault = \"flow\"\n[items]\nprefix = { thing = \"th\" }\n"+settings)
	write(t, filepath.Join(root, "agents.toml"), fallbacks)
	write(t, filepath.Join(root, "skills", "review.md"), "Review the change.\n")
	write(t, filepath.Join(root, "skills", "do.md"), "Do the work.\n")
	write(t, filepath.Join(root, "workflows", "flow.toml"), `
[states.open]
start = true
[states.work]
performer = { agent = "doer", skill = "do" }
[states.closed]
terminal = true
[gates.review]
skill = "review"
`+judges+`
[[transition]]
from = ["open"]
to = "work"
[[transition]]
from = ["work"]
to = "closed"
groups = [{ gates = ["review"] }]
`)
	if local != "" {
		write(t, filepath.Join(root, AgentsLocalFile), local)
	}
	return root
}

func TestValidateRefusesBadFallback(t *testing.T) {
	const tiers = "[review]\ntier_tag = \"risk\"\n[review.panels.review]\nhigh = [\"r2\", \"r3\"]\n"
	for _, c := range []struct {
		name, judges, settings, agents, local string
		want                                  []string
	}{
		{"valid", `agent = "r1"`, "", `r1 = "r2"`, "", nil},
		{"unknown", `agent = "r1"`, "", `r1 = "ghost"`, "", []string{"agents.r1", "ghost", "not in the agents catalog"}},
		{"self", `agent = "r1"`, "", `r1 = "r1"`, "", []string{"agents.r1", "names the agent itself"}},
		{"chain", `agent = "r1"`, "", "r1 = \"r2\"\nr2 = \"r3\"", "", []string{"agents.r1", `fallback "r2" names its own fallback "r3"`}},
		{"writer", `agent = "r1"`, "", `r1 = "writer"`, "", []string{`judge "r1" fallback`, `"writer"`, "may not judge"}},
		{"session", `agent = "r1"`, "", `r1 = "me"`, "", []string{`judge "r1" fallback`, `"me"`}},
		{"panel peer", "panel = [\"r1\", \"r2\"]", "", `r1 = "r2"`, "", []string{`judge "r1" fallback "r2" is also a judge of this gate`}},
		{"tier peer", `agent = "r1"`, tiers, `r2 = "r3"`, "", []string{`judge "r2" fallback "r3" is also a judge of this gate`}},
		{"shared fallback in a panel", "panel = [\"r1\", \"r2\"]", "", "r1 = \"r3\"\nr2 = \"r3\"", "", []string{`judges "r1" and "r2" name the same fallback "r3"`}},
		{"shared fallback in a tier", `agent = "r1"`, tiers, "r2 = \"r1\"\nr3 = \"r1\"", "", []string{`judges "r2" and "r3" name the same fallback "r1"`}},
		{"default names a tier judge", `agent = "r1"`, tiers, `r1 = "r2"`, "", nil},
		{"tier judge names the default", `agent = "r1"`, tiers, `r3 = "r1"`, "", nil},
		{"local override", `agent = "r1"`, "", "", "[agents.r1]\nfallback = \"ghost\"\n", []string{AgentsLocalFile, "agents.r1", "ghost", "overriding fallback"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := fallbackRoot(t, c.judges, c.settings, fallbackLines(c.agents), c.local)
			problems := Validate(root)
			var all []string
			for _, p := range problems {
				all = append(all, p.Error())
			}
			joined := strings.Join(all, "\n")
			if c.want == nil {
				if len(problems) != 0 {
					t.Fatalf("validate: %s", joined)
				}
				return
			}
			found := false
			for _, line := range all {
				ok := true
				for _, w := range c.want {
					ok = ok && strings.Contains(line, w)
				}
				found = found || ok
			}
			if !found {
				t.Fatalf("no problem holds %q:\n%s", c.want, joined)
			}
		})
	}
}

func fallbackLines(spec string) string {
	out := fallbackAgents
	if spec == "" {
		return out
	}
	for _, line := range strings.Split(spec, "\n") {
		name, value, _ := strings.Cut(line, " = ")
		header := "[agents." + name + "]\n"
		out = strings.Replace(out, header, header+"fallback = "+value+"\n", 1)
	}
	return out
}
