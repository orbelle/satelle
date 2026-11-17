package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gittest"
)

func agentsDoctor(t *testing.T) doctorReport {
	t.Helper()
	out, _ := run(t, "", "doctor", "--json")
	var rep doctorReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("doctor --json: %v\n%s", err, out)
	}
	return rep
}

func agentsIssues(rep doctorReport) []doctorIssue {
	var out []doctorIssue
	for _, i := range rep.Issues {
		if i.Check == "agents" {
			out = append(out, i)
		}
	}
	return out
}

func writeLocalFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLocalAgentsFileIsIgnoredAndListed(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	writeLocalFile(t, filepath.Join(dir, ".satelle", "agents.toml"), "[agents.r]\ncommand = \"claude\"\nmodel = \"m\"\ntools = [\"Read\"]\n")
	gittest.Git(t, dir, "add", "-A")
	gittest.Git(t, dir, "commit", "-q", "-m", "satelle")

	local := filepath.Join(dir, ".satelle", "agents.local.toml")
	writeLocalFile(t, local, "[agents.r]\nmodel = \"n\"\n")
	writeLocalFile(t, local, "[agents.r]\nmodel = \"n\"\ntools = [\"Read\", \"Grep\"]\n")
	if st := strings.TrimSpace(gittest.Git(t, dir, "status", "--porcelain")); st != "" {
		t.Fatalf("creating and editing the local file dirtied the tree:\n%s", st)
	}
	if src := gittest.Git(t, dir, "check-ignore", "-v", ".satelle/agents.local.toml"); !strings.Contains(src, ".satelle/.gitignore") {
		t.Errorf("check-ignore names %q, want .satelle/.gitignore", src)
	}
	rep := agentsDoctor(t)
	if is := agentsIssues(rep); len(is) != 0 {
		t.Errorf("agents issues with the line present: %+v", is)
	}
	found := false
	for _, n := range rep.Notes {
		if strings.Contains(n, "agents: r overridden by .satelle/agents.local.toml") && strings.Contains(n, "model \"m\" -> \"n\"") && strings.Contains(n, "tools [Read] -> [Read Grep]") {
			found = true
		}
	}
	if !found {
		t.Errorf("doctor does not list the override field by field: %v", rep.Notes)
	}
}

func TestDoctorAgentsIgnoreState(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	gi := filepath.Join(dir, ".satelle", ".gitignore")
	writeLocalFile(t, gi, "runs/\n")
	gittest.Git(t, dir, "add", "-A")
	gittest.Git(t, dir, "commit", "-q", "-m", "satelle")
	for _, withFile := range []bool{false, true} {
		if withFile {
			writeLocalFile(t, filepath.Join(dir, ".satelle", "agents.local.toml"), "")
		}
		is := agentsIssues(agentsDoctor(t))
		if len(is) != 1 || !strings.Contains(is[0].Problem, "no ignore rule covers .satelle/agents.local.toml") || !strings.Contains(is[0].Fix, "add the line agents.local.toml to .satelle/.gitignore") || is[0].Resolve != resolveFixItem {
			t.Errorf("file present %v: agents issues %+v", withFile, is)
		}
	}
	run(t, "", "doctor", "--resolve")
	b, _ := os.ReadFile(gi)
	if string(b) != "runs/\n" {
		t.Errorf("doctor changed .satelle/.gitignore: %q", b)
	}

	writeLocalFile(t, filepath.Join(dir, ".gitignore"), "*.local.toml\n")
	if is := agentsIssues(agentsDoctor(t)); len(is) != 0 {
		t.Errorf("a repo's own *.local.toml rule should satisfy the check: %+v", is)
	}
	gittest.Git(t, dir, "add", "-f", ".satelle/agents.local.toml")
	tracked := false
	for _, i := range agentsIssues(agentsDoctor(t)) {
		tracked = tracked || (strings.Contains(i.Problem, "is this machine's file but git tracks it") && strings.Contains(i.Fix, "git rm --cached"))
	}
	if !tracked {
		t.Error("a tracked local file is not reported as tracked")
	}
}

func TestBadLocalAgentsFileIsThisMachines(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	writeLocalFile(t, filepath.Join(dir, ".satelle", "agents.local.toml"), "[agents.ghost]\nmodel = \"m\"\n")
	if out, err := run(t, "", "validate"); err == nil || !strings.Contains(out+err.Error(), "agents.local.toml: agents.ghost") {
		t.Errorf("validate: %v\n%s", err, out)
	}
	var cfg []doctorIssue
	for _, i := range agentsDoctor(t).Issues {
		if i.Check == "config" && strings.Contains(i.Problem, "agents.local.toml") {
			cfg = append(cfg, i)
		}
	}
	if len(cfg) == 0 || !strings.Contains(cfg[0].Fix, "this machine's file") || cfg[0].Resolve != "" {
		t.Errorf("config issue for the local file: %+v", cfg)
	}
}
