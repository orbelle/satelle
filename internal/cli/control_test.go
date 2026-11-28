//go:build unix

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/kernel"
)

const appOrigin = "https://github.com/o/app"

type mappedFix struct {
	ctl, target, wt string
	log             string
}

func mappedTarget(t *testing.T) mappedFix {
	t.Helper()
	f := mappedFix{ctl: repoControl(t, "", "s-1"), target: checkoutWith(t, appOrigin)}
	f.log = filepath.Join(canonical(t.TempDir()), "planted.log")
	writeExec(t, filepath.Join(f.target, "bin", "store"), "#!/bin/sh\necho TARGET >> '"+f.log+"'\ncat >/dev/null\n")
	writeFile(t, filepath.Join(f.target, ".claude", "settings.json"), `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"satelle not-a-command TARGET-PAYLOAD"}]}]}}`+"\n")
	writeFile(t, filepath.Join(f.target, "docs", "guide.md"), "TARGET-PLANTED\n")
	writeFile(t, filepath.Join(f.target, ".satelle-planted"), "x\n")
	gittest.Git(t, f.target, "add", "-A")
	gittest.Git(t, f.target, "commit", "-qm", "planted")
	must(t, "repo", "add", "app", "--origin", appOrigin, "--path", f.target, "--session", "s-1")
	registerProjects(t, f.ctl)
	f.wt = filepath.Join(canonical(t.TempDir()), "app wt")
	gittest.Git(t, f.target, "worktree", "add", "-q", f.wt, "-b", "wt")
	return f
}

func statusAt(t *testing.T, dir string) statusReport {
	t.Helper()
	var rep statusReport
	if err := json.Unmarshal([]byte(must(t, "-C", dir, "status", "--json")), &rep); err != nil {
		t.Fatal(err)
	}
	return rep
}

func wantStatus(t *testing.T, dir string, got statusReport, control, root, tree, target, runtime string) {
	t.Helper()
	if got.Control != control || got.Root != root || got.Tree != tree || got.Target != target || got.Runtime != runtime {
		t.Errorf("status --json at %s: control %q root %q tree %q target %q runtime %q\nwant control %q root %q tree %q target %q runtime %q",
			dir, got.Control, got.Root, got.Tree, got.Target, got.Runtime, control, root, tree, target, runtime)
	}
	text := must(t, "-C", dir, "status")
	for _, line := range []string{"control   " + control + "\n", "config    " + root + "\n", "tree      " + tree + "\n", "target    " + target + "\n", "runtime   " + runtime + "\n"} {
		if !strings.Contains(text, line) {
			t.Errorf("status at %s lacks %q:\n%s", dir, line, text)
		}
	}
}

func commitSatelle(t *testing.T, dir string) {
	t.Helper()
	gittest.Git(t, dir, "add", ".satelle")
	gittest.Git(t, dir, "commit", "-qm", "satelle")
}

func cleanTree(t *testing.T, dir string) string {
	t.Helper()
	return gittest.Git(t, dir, "status", "--porcelain", "--ignored", "--untracked-files=all")
}

func ledgerEvents(t *testing.T, ctl, runtime string, ids ...string) int {
	t.Helper()
	k, err := kernel.Open(kernel.Options{Root: filepath.Join(ctl, config.DefaultDataDir), Runtime: runtime, Dir: ctl, ReadOnly: "a test count"})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	if len(ids) == 0 {
		all, err := k.Ledger.Items()
		if err != nil {
			t.Fatal(err)
		}
		ids = all
	}
	n := 0
	for _, id := range ids {
		events, err := k.Ledger.Read(id)
		if err != nil {
			t.Fatal(err)
		}
		n += len(events)
	}
	return n
}

func TestStatusMappedTarget(t *testing.T) {
	f := mappedTarget(t)
	ctlData := filepath.Join(f.ctl, config.DefaultDataDir)
	runtime := statusAt(t, f.ctl).Runtime
	wantStatus(t, f.target, statusAt(t, f.target), ctlData, ctlData, f.target, "app", runtime)

	gittest.Git(t, f.target, "remote", "set-url", "origin", "https://github.com/o/other")
	if out, err := run(t, "", "-C", f.target, "status"); err == nil || !strings.Contains(err.Error(), errNotInitialised.Error()) {
		t.Errorf("status at a checkout whose origin mismatches: %v\n%s", err, out)
	}
	gittest.Git(t, f.target, "remote", "set-url", "origin", appOrigin)

	gc, err := config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	gc.Projects.Paths = nil
	if err := config.SaveGlobal(gc); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, "", "-C", f.target, "status"); err == nil || !strings.Contains(err.Error(), errNotInitialised.Error()) {
		t.Errorf("status at a checkout of a control that is not one of this machine's projects: %v\n%s", err, out)
	}
	registerProjects(t, f.ctl)

	b := repoControl(t, canonical(t.TempDir()), "s-2")
	must(t, "-C", b, "repo", "add", "shared", "--origin", appOrigin, "--path", f.target, "--session", "s-2")
	registerProjects(t, b)
	_, err = run(t, "", "-C", f.target, "status")
	if err == nil {
		t.Fatal("status at a checkout two control repositories map resolved")
	}
	for _, want := range []string{f.ctl, b, "(as app)", "(as shared)", "onboarded by two control repositories"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("two-control error lacks %q: %v", want, err)
		}
	}
}

func TestStatusSelfTarget(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	data := filepath.Join(dir, config.DefaultDataDir)
	rep := statusAt(t, dir)
	wantStatus(t, dir, rep, data, data, dir, "self", rep.Runtime)
	if rep.Runtime == "" {
		t.Error("status reports no runtime")
	}
}

func TestStatusLinkedWorktree(t *testing.T) {
	f := mappedTarget(t)
	commitSatelle(t, f.ctl)
	ctlData := filepath.Join(f.ctl, config.DefaultDataDir)
	runtime := statusAt(t, f.ctl).Runtime

	own := filepath.Join(canonical(t.TempDir()), "ctl wt")
	gittest.Git(t, f.ctl, "worktree", "add", "-q", own, "-b", "own")
	wantStatus(t, own, statusAt(t, own), ctlData, filepath.Join(own, config.DefaultDataDir), own, "self", runtime)

	wantStatus(t, f.wt, statusAt(t, f.wt), ctlData, ctlData, f.wt, "app", runtime)
}

func TestSeatAtSelfLinkedWorktree(t *testing.T) {
	ctl := repoControl(t, "", "s-1")
	commitSatelle(t, ctl)
	wt := filepath.Join(canonical(t.TempDir()), "linked wt")
	gittest.Git(t, ctl, "worktree", "add", "-q", wt, "-b", "linked")
	id := createID(t, "story", "create", "--title", "in a worktree", "--workflow", "planned")
	must(t, "story", "set", id, "--status", "st-plan")
	must(t, "story", "set", id, "--status", "st-build")
	if out := must(t, "-C", wt, "story", "seat", id, "--session", "s-9"); !strings.Contains(out, "at local:"+wt) {
		t.Errorf("seat in a linked worktree: %s", out)
	}
	if out := must(t, "status", "--session", "s-9"); !strings.Contains(out, id) || !strings.Contains(out, "local:"+wt) {
		t.Errorf("status at the main checkout does not list the seat at the worktree:\n%s", out)
	}
}

func deniedCommands(id string) [][]string {
	return [][]string{
		{"story", "create", "--title", "from a target"},
		{"story", "seat", id, "--session", "s-1"},
		{"story", "set", id, "--status", "st-plan"},
		{"task", "run", "tsk_x"},
		{"queue", "add", "que_release", id},
		{"queue", "fire", "que_release"},
		{"gate", "run", "dry/ok", "--item", id, "--from", "st-one"},
		{"story", "collect", id, "--branch", "b"},
		{"remote", "fire", "rem_x", id},
		{"remote", "status", "rem_x", id},
		{"remote", "stop", "rem_x", id},
		{"ledger", "store-test"},
		{"ledger", "push"},
		{"agents", "install", "claude"},
		{"repo", "list"},
	}
}

func TestMappedTargetDeniedByDefault(t *testing.T) {
	f := mappedTarget(t)
	id := createID(t, "story", "create", "--title", "denied", "--workflow", "planned")
	runtime := statusAt(t, f.ctl).Runtime
	configureLedger(t, f.ctl, `store = { command = "bin/store" }`)
	before := map[string]string{f.target: cleanTree(t, f.target), f.wt: cleanTree(t, f.wt)}
	events := ledgerEvents(t, f.ctl, runtime)

	for _, dir := range []string{f.target, f.wt} {
		for _, args := range deniedCommands(id) {
			out, err := run(t, "", append([]string{"-C", dir}, args...)...)
			if err == nil || !strings.Contains(err.Error(), errNotInitialised.Error()) {
				t.Errorf("satelle %s at %s: %v, want the not-initialised error\n%s", strings.Join(args, " "), dir, err, out)
			}
		}
		edit := `{"session_id":"s-1","cwd":"` + dir + `","hook_event_name":"PreToolUse","tool_name":"Write","tool_input":{"file_path":"` + filepath.Join(dir, "x.go") + `","content":"x"}}`
		if out, err := run(t, edit, "-C", dir, "hook", "gate"); err != nil || strings.TrimSpace(out) != "" {
			t.Errorf("hook gate at %s gave a verdict: %v\n%s", dir, err, out)
		}
	}

	registerProjects(t, f.target, f.wt)
	_, _ = run(t, "", "agents", "refresh")
	out, _ := run(t, "", "ledger", "autosync")
	for _, dir := range []string{f.target, f.wt} {
		if !strings.Contains(out, dir+": skipped: ") {
			t.Errorf("ledger autosync did not skip %s:\n%s", dir, out)
		}
	}

	for dir, was := range before {
		if now := cleanTree(t, dir); now != was {
			t.Errorf("the checkout %s changed:\nbefore\n%s\nafter\n%s", dir, was, now)
		}
	}
	if b, err := os.ReadFile(f.log); err == nil || len(b) > 0 {
		t.Errorf("a planted file in the target ran: %q %v", b, err)
	}
	if n := ledgerEvents(t, f.ctl, runtime); n != events {
		t.Errorf("the control ledger gained %d event(s) from a mapped target", n-events)
	}
}

func TestQueueChainUnchangedAtMappedTarget(t *testing.T) {
	f := mappedTarget(t)
	queue := filepath.Join(f.ctl, config.DefaultDataDir, kernel.QueuesDir, "release.toml")
	writeFile(t, queue, "title = \"Release A\"\n[filter]\nkind = \"story\"\n")
	runtime := statusAt(t, f.ctl).Runtime
	chain := func() int { return ledgerEvents(t, f.ctl, runtime, "que_release") }
	if chain() != 1 {
		t.Fatalf("the control's status wrote %d events for que_release, want the genesis", chain())
	}
	writeFile(t, queue, "title = \"Release B\"\n[filter]\nstatus = \"done\"\n")
	before := chain()
	for _, dir := range []string{f.target, f.wt} {
		must(t, "-C", dir, "status")
		sessionContextAt(t, dir)
		_, _ = run(t, "", "-C", dir, "doctor")
		for _, args := range deniedCommands("sto_x") {
			_, _ = run(t, "", append([]string{"-C", dir}, args...)...)
		}
		if n := chain(); n != before {
			t.Fatalf("commands at %s moved the queue chain to %d events, want %d", dir, n, before)
		}
	}
	must(t, "-C", f.ctl, "status")
	if n := chain(); n != before+1 {
		t.Fatalf("the control case did not mirror: chain %d events, want %d", n, before+1)
	}
}

func TestHookContextMappedTargetReadsControl(t *testing.T) {
	f := mappedTarget(t)
	toml := filepath.Join(f.ctl, config.DefaultDataDir, "satelle.toml")
	body := readFileString(t, toml)
	start := strings.Index(body, "inject = [")
	end := start + strings.Index(body[start:], "\n")
	writeFile(t, toml, body[:start]+`inject = ["docs/guide.md"]`+body[end:])
	writeFile(t, filepath.Join(f.ctl, "docs", "guide.md"), "CONTROL-GUIDE\n")
	checkLog := withCheck(t, f.ctl)
	runtime := statusAt(t, f.ctl).Runtime
	events := ledgerEvents(t, f.ctl, runtime)

	for _, dir := range []string{f.target, f.wt} {
		out := sessionContextAt(t, dir)
		for _, want := range []string{"is target app of the control repository " + f.ctl, "CONTROL-GUIDE", "satelle session: s-hook"} {
			if !strings.Contains(out, want) {
				t.Errorf("hook context at %s lacks %q:\n%s", dir, want, out)
			}
		}
		for _, not := range []string{"TARGET-PLANTED", "satelle doctor:"} {
			if strings.Contains(out, not) {
				t.Errorf("hook context at %s holds %q:\n%s", dir, not, out)
			}
		}
	}
	if b, _ := os.ReadFile(checkLog); len(b) > 0 {
		t.Errorf("hook context at a target ran the control's doctor check: %q", b)
	}
	if n := ledgerEvents(t, f.ctl, runtime); n != events {
		t.Errorf("hook context at a target appended %d event(s)", n-events)
	}

	writeFile(t, filepath.Join(f.ctl, config.DefaultDataDir, "harness.toml"), "[claude]\ncontext_limit_bytes = 64\n")
	if out := sessionContextAt(t, f.target); !strings.Contains(out, "paths are relative to "+f.ctl+")") || strings.Contains(out, "TARGET-PLANTED") {
		t.Errorf("index form at a target:\n%s", out)
	}
	writeFile(t, filepath.Join(f.ctl, config.DefaultDataDir, "harness.toml"), "")

	if out := sessionContextAt(t, f.ctl); !strings.Contains(out, "CONTROL-GUIDE") || strings.Contains(out, "is target") {
		t.Errorf("hook context in the control repository:\n%s", out)
	}
	if b, _ := os.ReadFile(checkLog); !strings.Contains(string(b), "CHECK") {
		t.Errorf("hook context in the control repository ran no doctor check: %q", b)
	}
}

func withCheck(t *testing.T, ctl string) string {
	t.Helper()
	log := filepath.Join(canonical(t.TempDir()), "check.log")
	writeFile(t, filepath.Join(ctl, config.DefaultDataDir, "skills", "fixture-check.md"), "---\nname: fixture-check\ntype: check\n---\n\n```sh\necho CHECK >> '"+log+"'\nexit 0\n```\n")
	doctorToml := filepath.Join(ctl, config.DefaultDataDir, "doctor.toml")
	writeFile(t, doctorToml, readFileString(t, doctorToml)+"\n[[check]]\nskill = \"fixture-check\"\nfix = \"fix what it names\"\n")
	return log
}

func TestDoctorMappedTargetReportsMapping(t *testing.T) {
	f := mappedTarget(t)
	checkLog := withCheck(t, f.ctl)
	_, rep, out, _ := doctorRepos(t, "-C", f.target)
	note := ""
	for _, n := range rep.Notes {
		if strings.Contains(n, "is target app of the control repository "+f.ctl) {
			note = n
		}
	}
	if !strings.Contains(note, "satelle -C "+f.ctl+" doctor") {
		t.Errorf("doctor at a target notes %v", rep.Notes)
	}
	if strings.Contains(out, "TARGET-PAYLOAD") {
		t.Errorf("doctor at a target read the target's hook file:\n%s", out)
	}
	for _, is := range rep.Issues {
		if is.Check == "harness" || is.Check == "config" || strings.HasPrefix(is.Check, "check:") {
			t.Errorf("doctor at a target ran a repository check: %+v", is)
		}
	}
	if b, _ := os.ReadFile(checkLog); len(b) > 0 {
		t.Errorf("doctor at a target ran the control's check: %q", b)
	}

	_, rep, _, _ = doctorRepos(t, "-C", f.ctl)
	if b, _ := os.ReadFile(checkLog); !strings.Contains(string(b), "CHECK") {
		t.Errorf("doctor in the control repository ran no check: %q", b)
	}
	for _, n := range rep.Notes {
		if strings.Contains(n, "is target") {
			t.Errorf("doctor in the control repository notes a target: %s", n)
		}
	}
}
