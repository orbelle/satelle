//go:build !windows

package remotenode

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/gittest"
)

func TestGateWrapperRunsTheCheck(t *testing.T) {
	env := gate.CheckEnv{Item: "sto_1", From: "a", To: "b's", Base: "abc"}
	for _, c := range []struct {
		name, script string
		exit, log    string
		notLog       string
	}{
		{"sh", "echo \"$SATELLE_ITEM $SATELLE_FROM $SATELLE_TO $SATELLE_BASE $(basename \"$PWD\")\"; cat; exit 3", "3",
			`sto_1 a b's abc work` + "\n" + `{"base":"abc","from":"a","id":"sto_1","to":"b's"}`, ""},
		{"shebang", "#!/bin/sh\necho \"$0\" >&2\nif [ -x \"$0\" ]; then echo executable; else echo read-only; fi\nexit 0\n", "0", "in/check", "executable"},
	} {
		root := t.TempDir()
		for _, d := range []string{"in", "work"} {
			if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		wrapper, err := gateWrapperScript(env, c.script, "", "")
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		for name, body := range map[string]string{
			gateScript: c.script, gatePayload: string(env.Payload()), gateWrapper: wrapper,
		} {
			if err := os.WriteFile(filepath.Join(root, "in", name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if out, err := exec.Command("sh", filepath.Join(root, "in", gateWrapper), root).CombinedOutput(); err != nil {
			t.Fatalf("%s: wrapper: %v %s", c.name, err, out)
		}
		exit, _ := os.ReadFile(filepath.Join(root, "out", gateExit))
		log, _ := os.ReadFile(filepath.Join(root, "out", gateLog))
		if strings.TrimSpace(string(exit)) != c.exit || !strings.Contains(string(log), c.log) {
			t.Fatalf("%s: exit %q log %q", c.name, exit, log)
		}
		if c.notLog != "" && (strings.Contains(string(log), c.notLog) || !strings.Contains(string(log), "read-only")) {
			t.Fatalf("%s: the check was executed as a file: log %q", c.name, log)
		}
		if c.name == "shebang" && strings.Contains(wrapper, "chmod") {
			t.Fatalf("%s: the wrapper makes the check executable:\n%s", c.name, wrapper)
		}
		if strings.Contains(wrapper, LocalNodeDir) {
			t.Fatalf("%s: the wrapper names the node dir", c.name)
		}
	}
	if _, err := gateWrapperScript(env, "#!\n", "", ""); err == nil {
		t.Error("a #! naming no interpreter was accepted")
	}
}

func TestGateWrapperAndStageGateSkillDir(t *testing.T) {
	t.Run("normal directory check via stageGate", func(t *testing.T) {
		tree := t.TempDir()
		gittest.Git(t, tree, "init", "-q", "-b", "main")
		skillDir := filepath.Join(tree, ".satelle", "skills", "my-check")
		if err := os.MkdirAll(filepath.Join(skillDir, "scripts"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(skillDir, "scripts", "run.sh"), []byte("printf 'dir=%s\\n' \"$SATELLE_SKILL_DIR\"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		gittest.Git(t, tree, "add", "-A")
		gittest.Git(t, tree, "commit", "-q", "-m", "init")
		head := gittest.Git(t, tree, "rev-parse", "HEAD")

		root := t.TempDir()
		inDir := filepath.Join(root, "in")
		workDir := filepath.Join(root, "work")
		if err := os.MkdirAll(filepath.Join(workDir, ".satelle", "skills", "my-check", "scripts"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(workDir, ".satelle", "skills", "my-check", "scripts", "run.sh"), []byte("printf 'dir=%s\\n' \"$SATELLE_SKILL_DIR\"\n"), 0o755); err != nil {
			t.Fatal(err)
		}

		c := gate.RemoteCheck{
			Gate:   "my-check",
			Script: "sh scripts/run.sh\n",
			Env:    gate.CheckEnv{Item: "sto_1", From: "a", To: "b", Base: head, SkillDir: skillDir},
			Head:   gate.Head{Ref: "refs/heads/main", Commit: head},
		}
		if _, err := stageGate(tree, inDir, c); err != nil {
			t.Fatalf("stageGate: %v", err)
		}
		stagedScript, err := os.ReadFile(filepath.Join(inDir, gateScript))
		if err != nil {
			t.Fatal(err)
		}
		if string(stagedScript) != "sh \"$SATELLE_SKILL_DIR\"/scripts/run.sh\n" {
			t.Errorf("staged script = %q", stagedScript)
		}
		stagedPayload, err := os.ReadFile(filepath.Join(inDir, gatePayload))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(stagedPayload), "skill_dir") || strings.Contains(string(stagedPayload), skillDir) {
			t.Errorf("payload leaked host skill dir: %s", stagedPayload)
		}
		stagedWrapper, err := os.ReadFile(filepath.Join(inDir, gateWrapper))
		if err != nil {
			t.Fatal(err)
		}
		wantExport := `export SATELLE_SKILL_DIR="$root/work/"'.satelle/skills/my-check'`
		if !strings.Contains(string(stagedWrapper), wantExport) {
			t.Errorf("wrapper lacks %q:\n%s", wantExport, stagedWrapper)
		}
		if strings.Contains(string(stagedWrapper), skillDir) {
			t.Errorf("wrapper leaked host skill dir %q:\n%s", skillDir, stagedWrapper)
		}
		if out, err := exec.Command("sh", filepath.Join(inDir, gateWrapper), root).CombinedOutput(); err != nil {
			t.Fatalf("wrapper exec: %v\n%s", err, out)
		}
		exit, _ := os.ReadFile(filepath.Join(root, "out", gateExit))
		log, _ := os.ReadFile(filepath.Join(root, "out", gateLog))
		wantLog := "dir=" + root + "/work/.satelle/skills/my-check\n"
		if strings.TrimSpace(string(exit)) != "0" || string(log) != wantLog {
			t.Fatalf("exit=%q log=%q, want 0 and %q", exit, log, wantLog)
		}
	})

	t.Run("adversarial skill directory name", func(t *testing.T) {
		rel := ".satelle/skills/x$(echo pwn)`echo pwn2`\" ' s"
		env := gate.CheckEnv{Item: "sto_1", From: "a", To: "b", Base: "abc"}
		script := "printf '%s' \"$SATELLE_SKILL_DIR\"\n"
		wrapper, err := gateWrapperScript(env, script, rel, "")
		if err != nil {
			t.Fatalf("gateWrapperScript: %v", err)
		}
		root := t.TempDir()
		for _, d := range []string{"in", "work"} {
			if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		for name, body := range map[string]string{
			gateScript: script, gatePayload: string(env.Payload()), gateWrapper: wrapper,
		} {
			if err := os.WriteFile(filepath.Join(root, "in", name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if out, err := exec.Command("sh", filepath.Join(root, "in", gateWrapper), root).CombinedOutput(); err != nil {
			t.Fatalf("wrapper exec: %v\n%s", err, out)
		}
		exit, _ := os.ReadFile(filepath.Join(root, "out", gateExit))
		log, _ := os.ReadFile(filepath.Join(root, "out", gateLog))
		want := root + "/work/" + rel
		if strings.TrimSpace(string(exit)) != "0" || string(log) != want {
			t.Fatalf("exit=%q log=%q, want 0 and %q", exit, log, want)
		}
	})

	t.Run("invalid skillRel refused", func(t *testing.T) {
		env := gate.CheckEnv{Item: "sto_1"}
		for _, bad := range []string{"/abs/path", ".", "..", "../outside", "a/../../outside"} {
			if _, err := gateWrapperScript(env, "exit 0\n", bad, ""); err == nil {
				t.Errorf("gateWrapperScript accepted invalid skillRel %q", bad)
			}
		}
		tree := t.TempDir()
		outside := t.TempDir()
		c := gate.RemoteCheck{
			Gate:   "g",
			Script: "exit 0\n",
			Env:    gate.CheckEnv{Item: "sto_1", SkillDir: outside},
			Head:   gate.Head{Ref: "refs/heads/main", Commit: "abc"},
		}
		if _, err := stageGate(tree, t.TempDir(), c); err == nil {
			t.Error("stageGate accepted SkillDir outside tree")
		}
	})
}

func TestStageGateNamesTheNodeSubstrate(t *testing.T) {
	tree := t.TempDir()
	gittest.Git(t, tree, "init", "-q", "-b", "main")
	if err := os.MkdirAll(filepath.Join(tree, ".satelle"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, ".satelle", "satelle.toml"), []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, tree, "add", "-A")
	gittest.Git(t, tree, "commit", "-q", "-m", "init")
	head := gittest.Git(t, tree, "rev-parse", "HEAD")
	hostSubstrate := filepath.Join(tree, ".satelle")

	root := t.TempDir()
	inDir := filepath.Join(root, "in")
	if err := os.MkdirAll(filepath.Join(root, "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := gate.RemoteCheck{
		Gate:   "g",
		Script: "printf '%s' \"$SATELLE_SUBSTRATE_DIR\"\n",
		Env:    gate.CheckEnv{Item: "sto_1", From: "a", To: "b", Base: head, Substrate: hostSubstrate},
		Head:   gate.Head{Ref: "refs/heads/main", Commit: head},
	}
	if _, err := stageGate(tree, inDir, c); err != nil {
		t.Fatalf("stageGate: %v", err)
	}
	wrapper, err := os.ReadFile(filepath.Join(inDir, gateWrapper))
	if err != nil {
		t.Fatal(err)
	}
	want := `export SATELLE_SUBSTRATE_DIR="$root/work/"'.satelle'`
	if !strings.Contains(string(wrapper), want) {
		t.Errorf("wrapper lacks %q:\n%s", want, wrapper)
	}
	if strings.Contains(string(wrapper), hostSubstrate) {
		t.Errorf("wrapper leaked the host substrate %q:\n%s", hostSubstrate, wrapper)
	}
	if out, err := exec.Command("sh", filepath.Join(inDir, gateWrapper), root).CombinedOutput(); err != nil {
		t.Fatalf("wrapper exec: %v\n%s", err, out)
	}
	log, _ := os.ReadFile(filepath.Join(root, "out", gateLog))
	if got := string(log); got != root+"/work/.satelle" {
		t.Errorf("check saw SATELLE_SUBSTRATE_DIR=%q, want %q", got, root+"/work/.satelle")
	}

	c.Env.Substrate = t.TempDir()
	_, err = stageGate(tree, t.TempDir(), c)
	if err == nil || !strings.Contains(err.Error(), "substrate directory") {
		t.Errorf("stageGate with a substrate outside the tree = %v, want a refusal naming the substrate directory", err)
	}
}

func TestParseExit(t *testing.T) {
	for in, want := range map[string]int{"0\n": 0, " 3 ": 3, "137": 137} {
		if got, err := parseExit([]byte(in)); err != nil || got != want {
			t.Errorf("parseExit(%q) = %d, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "\n", "x", "-1", "1 2"} {
		if _, err := parseExit([]byte(in)); err == nil {
			t.Errorf("parseExit(%q) accepted", in)
		}
	}
}
