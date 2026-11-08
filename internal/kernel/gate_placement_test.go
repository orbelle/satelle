package kernel

import (
	"path/filepath"
	"strings"
	"testing"
)

// A gate's placement: local and a docker-local remote validate on a check
// gate; cloud, a malformed value, a judge, an unknown remote id, an unparsable
// definition and a remote of another provider are each refused by name.
func TestGatePlacementValidation(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "agents.toml"), "[agents.rev]\ncommand = \"claude\"\nmodel = \"m\"\ntools = [\"Read\"]\n")
	write(t, filepath.Join(root, "skills", "chk.md"), "---\ntype: check\n---\n```sh\nexit 0\n```\n")
	write(t, filepath.Join(root, "skills", "jdg.md"), "judge\n")
	write(t, filepath.Join(root, "remotes", "box", "remote.toml"),
		"version = 1\nname = \"box\"\nprovider = \"docker-local\"\nsatelle_version = \"1.0.0\"\n[drive]\nkind = \"item\"\n[lifecycle]\nscope = \"run\"\n")
	write(t, filepath.Join(root, "remotes", "fly", "remote.toml"),
		"version = 1\nname = \"fly\"\nprovider = \"fly-sprite\"\nsatelle_version = \"1.0.0\"\n[drive]\nkind = \"item\"\n[lifecycle]\nscope = \"run\"\n")
	write(t, filepath.Join(root, "remotes", "broken", "remote.toml"), "version = 1\nmystery = true\n")
	wf := func(gates string) {
		write(t, filepath.Join(root, "workflows", "x.toml"), `
[states.a]
start = true
[states.z]
terminal = true
`+gates+`
[[transition]]
from = ["a"]
to = "z"
groups = [{ gates = ["g"] }]
`)
	}
	problems := func() string {
		var b strings.Builder
		for _, p := range Validate(root) {
			b.WriteString(p.Error() + "\n")
		}
		return b.String()
	}
	for _, ok := range []string{"", `placement = "local"`, `placement = "remote:box"`} {
		wf("[gates.g]\nskill = \"chk\"\n" + ok + "\n")
		if p := problems(); p != "" {
			t.Fatalf("%q refused:\n%s", ok, p)
		}
	}
	for _, c := range []struct{ gate, want string }{
		{"skill = \"chk\"\nplacement = \"cloud\"", `gate "g": placement "cloud" is not local or remote:<id>`},
		{"skill = \"chk\"\nplacement = \"remote:\"", `placement "remote:" is not local or remote:<id>`},
		{"skill = \"chk\"\nplacement = \"remote:../box\"", `"../box" is not a remote id`},
		{"skill = \"jdg\"\nagent = \"rev\"\nplacement = \"remote:box\"", `skill "jdg" is not a check; only a check gate runs on a remote`},
		{"skill = \"chk\"\nplacement = \"remote:nobody\"", `is placed on remote nobody, which has no .satelle/remotes/nobody/remote.toml`},
		{"skill = \"chk\"\nplacement = \"remote:broken\"", `is placed on remote broken: remote.toml is not a valid v1 definition`},
		{"skill = \"chk\"\nplacement = \"remote:fly\"", `is placed on remote fly, whose provider fly-sprite does not run gate checks here`},
	} {
		wf("[gates.g]\n" + c.gate + "\n")
		if p := problems(); !strings.Contains(p, c.want) {
			t.Errorf("%s: want %q in:\n%s", c.gate, c.want, p)
		}
	}
	// A kernel with a refused placement does not open.
	if _, err := Open(Options{Root: root}); err == nil || !strings.Contains(err.Error(), "fly-sprite") {
		t.Fatalf("open: %v", err)
	}
}
