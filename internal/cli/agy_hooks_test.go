package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/hook"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/procgroup"
	"github.com/bobmcallan/satelle/internal/seatlife"
)

const (
	agyModel       = "gemini-3.8-flash-low"
	agyOtherPlugin = `{"other-plugin":{"Stop":[{"type":"command","command":"./mine.sh"}]}}`
)

func agyDoc(t *testing.T, fixture string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(integrationTestdata(hook.Agy), "agy-"+fixture+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func agyPayload(t *testing.T, fixture, session, root, path, command string) string {
	t.Helper()
	doc := agyDoc(t, fixture)
	doc["conversationId"], doc["cwd"], doc["workspacePaths"] = session, root, []string{root}
	if tc, ok := doc["toolCall"].(map[string]any); ok {
		args, _ := tc["args"].(map[string]any)
		if path != "" {
			args["Path"] = path
		}
		if command != "" {
			args["CommandLine"] = command
		}
	}
	out, _ := json.Marshal(doc)
	return string(out)
}

func inAgents(t *testing.T, dir string) {
	t.Helper()
	agents := filepath.Join(dir, ".agents")
	if err := os.MkdirAll(agents, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(agents)
}

func agyAnswer(t *testing.T, payload, verb string) (raw string, doc map[string]any) {
	t.Helper()
	out, err := run(t, payload, "hook", verb, "--harness", "agy")
	if err != nil {
		t.Fatalf("hook %s: %v\n%s", verb, err, out)
	}
	raw = strings.TrimSpace(out)
	if raw == "" {
		return raw, nil
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("hook %s answer %q: %v", verb, raw, err)
	}
	return raw, doc
}

func agyDeny(t *testing.T, raw string, doc map[string]any) string {
	t.Helper()
	reason, _ := doc["reason"].(string)
	if len(doc) != 2 || doc["decision"] != "deny" || reason == "" {
		t.Fatalf("not an agy deny: %q", raw)
	}
	return reason
}

func agyInjected(t *testing.T, raw string) []string {
	t.Helper()
	var doc struct {
		InjectSteps []struct {
			SystemMessage struct {
				SystemMessage string `json:"systemMessage"`
			} `json:"systemMessage"`
		} `json:"injectSteps"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil || len(doc.InjectSteps) == 0 {
		t.Fatalf("not an agy injectSteps answer: %q (%v)", raw, err)
	}
	var out []string
	for _, s := range doc.InjectSteps {
		out = append(out, s.SystemMessage.SystemMessage)
	}
	return out
}

func TestAgentsInstallAgy(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	path := filepath.Join(dir, ".agents", "hooks.json")
	putFile(t, path, agyOtherPlugin)
	if out := must(t, "agents", "install", "agy"); !strings.Contains(out, "installed agy hooks: "+path) {
		t.Fatalf("install: %s", out)
	}
	b := readString(t, path)
	for _, verb := range []string{hook.VerbGate, hook.VerbContext, hook.VerbPrompt, hook.VerbStopcheck} {
		if strings.Count(b, hook.Command(verb, hook.Agy)) != 1 {
			t.Errorf("%s entry missing:\n%s", verb, b)
		}
	}
	if !strings.Contains(b, "./mine.sh") || !strings.Contains(b, `"other-plugin"`) {
		t.Fatalf("other owner lost:\n%s", b)
	}
	if out := must(t, "agents", "install", "agy"); !strings.Contains(out, "agy hooks are current") {
		t.Errorf("re-install: %s", out)
	}
	var rep statusReport
	if err := json.Unmarshal([]byte(must(t, "status", "--json")), &rep); err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(rep.Harnesses, func(w hook.Wiring) bool { return w.Harness == hook.Agy })
	if i < 0 || rep.Harnesses[i].File != ".agents/hooks.json" || rep.Harnesses[i].State != hook.WiringInstalled {
		t.Errorf("status --json harnesses: %+v", rep.Harnesses)
	}
	if out := must(t, "agents", "remove", "agy"); !strings.Contains(out, "removed agy hooks: "+path) {
		t.Fatalf("remove: %s", out)
	}
	b = readString(t, path)
	if strings.Contains(b, "satelle hook") || !strings.Contains(b, "./mine.sh") {
		t.Fatalf("after remove:\n%s", b)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	must(t, "agents", "install", "agy")
	must(t, "agents", "remove", "agy")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("remove of a satelle-only file left it: %v", err)
	}
}

func TestAgyGateMatchesClaudeGate(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	writeGatedWorkflow(t, dir)
	id := createID(t, "story", "create", "--title", "gated", "--workflow", "gated")
	main := filepath.Join(dir, "main.go")
	inAgents(t, dir)

	deny := func(what, payload, claude string) string {
		t.Helper()
		raw, doc := agyAnswer(t, payload, "gate")
		got := agyDeny(t, raw, doc)
		if want := claudeDenyReason(t, claude); want == "" || got != want {
			t.Errorf("%s: agy reason\n%q\nclaude reason\n%q", what, got, want)
		}
		return got
	}
	for _, fx := range []string{"pretooluse-write_file", "pretooluse-edit_file"} {
		deny(fx, agyPayload(t, fx, "a-1", dir, main, ""), claudePayload("a-1", dir, "Edit", main, ""))
	}
	shellWrite := "echo x > " + main
	if got := deny("unseated shell write", agyPayload(t, "pretooluse-run_command", "a-1", dir, "", shellWrite), claudePayload("a-1", dir, "Bash", "", shellWrite)); !strings.Contains(got, "seat") {
		t.Errorf("unseated shell write reason: %q", got)
	}
	record := "satelle story record " + id + " --file note.md"
	if got := deny("operator record", agyPayload(t, "pretooluse-run_command", "a-1", dir, "", record), claudePayload("a-1", dir, "Bash", "", record)); got != hook.OperatorRecordDenied {
		t.Errorf("operator record: %q", got)
	}

	must(t, "story", "set", id, "--status", "st-two")
	must(t, "story", "seat", id, "--session", "a-1")
	for _, fx := range []string{"pretooluse-write_file", "pretooluse-edit_file"} {
		if raw, _ := agyAnswer(t, agyPayload(t, fx, "a-1", dir, main, ""), "gate"); raw != "" {
			t.Errorf("%s: a seated agy edit was answered: %s", fx, raw)
		}
	}
	if raw, _ := agyAnswer(t, agyPayload(t, "pretooluse-run_command", "a-1", dir, "", shellWrite), "gate"); raw != "" {
		t.Errorf("a seated agy shell write was answered: %s", raw)
	}
	locked := filepath.Join(dir, ".satelle", "x")
	deny("locked path", agyPayload(t, "pretooluse-write_file", "a-1", dir, locked, ""), claudePayload("a-1", dir, "Write", locked, ""))
	if got := deny("seated operator record", agyPayload(t, "pretooluse-run_command", "a-1", dir, "", record), claudePayload("a-1", dir, "Bash", "", record)); got != hook.OperatorRecordDenied {
		t.Error("a seat allowed the operator record verb")
	}
}

func TestAgyGateAttachmentsOnlyAndFence(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	writePlanWorkflow(t, dir)
	id := createID(t, "story", "create", "--title", "planned", "--workflow", "planned")
	must(t, "story", "seat", id, "--session", "a-2")
	must(t, "story", "set", id, "--status", "st-plan")
	main := filepath.Join(dir, "main.go")
	inAgents(t, dir)
	raw, doc := agyAnswer(t, agyPayload(t, "pretooluse-write_file", "a-2", dir, main, ""), "gate")
	got := agyDeny(t, raw, doc)
	if want := claudeDenyReason(t, claudePayload("a-2", dir, "Write", main, "")); got != want || !strings.Contains(got, "st-plan") {
		t.Errorf("attachments-only: agy reason\n%q\nclaude\n%q", got, want)
	}

	must(t, "story", "set", id, "--status", "st-build")
	foreign, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, foreign, "init", "-q")
	gittest.Git(t, foreign, "commit", "-q", "--allow-empty", "-m", "init")
	tempRootsOverride = []string{t.TempDir()}
	t.Cleanup(func() { tempRootsOverride = nil })
	if raw, _ := agyAnswer(t, agyPayload(t, "pretooluse-write_file", "a-2", dir, main, ""), "gate"); raw != "" {
		t.Fatalf("a seated agy edit in this repo was answered: %s", raw)
	}
	target := filepath.Join(foreign, "main.go")
	raw, doc = agyAnswer(t, agyPayload(t, "pretooluse-write_file", "a-2", dir, target, ""), "gate")
	got = agyDeny(t, raw, doc)
	if want := claudeDenyReason(t, claudePayload("a-2", dir, "Write", target, "")); got != want || !strings.Contains(got, "another repository's working tree") {
		t.Errorf("fence: agy reason\n%q\nclaude\n%q", got, want)
	}
}

func TestAgyContext(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	writeGatedWorkflow(t, dir)
	putFile(t, filepath.Join(dir, "principles.md"), "---\ndescription: principles-desc\n---\n\nnonce-a9e1\n")
	writeHookSettings(t, dir, "inject = [\"principles.md\"]\n")
	id := createID(t, "story", "create", "--title", "seated work", "--workflow", "gated")
	must(t, "story", "set", id, "--status", "st-two")
	must(t, "story", "seat", id, "--session", "a-ctx")
	inAgents(t, dir)

	raw, _ := agyAnswer(t, agyPayload(t, "sessionstart", "a-ctx", dir, "", ""), "context")
	text := agyInjected(t, raw)[0]
	hasAll(t, "agy context", text, "satelle session: a-ctx", id+"  seated work", "nonce-a9e1")
	rec, ok := seatlife.Load(config.GlobalDir(), "a-ctx")
	if !ok || rec.Harness != hook.Agy || rec.Model != agyModel {
		t.Errorf("session record: %+v, %v", rec, ok)
	}

	doc := agyDoc(t, "sessionstart")
	doc["conversationId"], doc["workspacePaths"] = "a-ctx", []string{dir}
	delete(doc, "cwd")
	b, _ := json.Marshal(doc)
	t.Chdir(t.TempDir())
	raw, _ = agyAnswer(t, string(b), "context")
	hasAll(t, "agy context from workspacePaths", agyInjected(t, raw)[0], "satelle session: a-ctx", "nonce-a9e1")

	putFile(t, filepath.Join(dir, ".satelle", kernel.HarnessFile), "[agy]\ncontext_limit_bytes = 200\n")
	t.Chdir(dir)
	must(t, "validate")
	inAgents(t, dir)
	raw, _ = agyAnswer(t, agyPayload(t, "sessionstart", "a-ctx", dir, "", ""), "context")
	text = agyInjected(t, raw)[0]
	hasAll(t, "agy limited context", text, "over agy's configured session-start limit", "principles.md — principles-desc")
	hasNone(t, "agy limited context", text, "nonce-a9e1")
}

func TestAgyPromptRemindsOncePerTurn(t *testing.T) {
	dir := isolate(t)
	anc := procgroup.Ancestors(os.Getpid())
	must(t, "init")
	inAgents(t, dir)
	raw, _ := agyAnswer(t, agyPayload(t, "preinvocation-turnstart", "a-p", dir, "", ""), "prompt")
	hasAll(t, "agy turn-start reminder", agyInjected(t, raw)[0], "satelle session: a-p")
	if raw, _ := agyAnswer(t, agyPayload(t, "preinvocation-midturn", "a-p", dir, "", ""), "prompt"); raw != "{}" {
		t.Errorf("mid-turn answer %q, want {}", raw)
	}
	doc := agyDoc(t, "preinvocation-midturn")
	doc["conversationId"], doc["cwd"], doc["workspacePaths"], doc["invocationNum"] = "a-p", dir, []string{dir}, 2
	b, _ := json.Marshal(doc)
	if raw, _ := agyAnswer(t, string(b), "prompt"); raw != "{}" {
		t.Errorf("continuation answer %q, want {}", raw)
	}
	if len(anc) > 0 {
		rec, _ := seatlife.Load(config.GlobalDir(), "a-p")
		if rec.Owner == nil || *rec.Owner != anc[0] {
			t.Errorf("the mid-turn prompt hook did not settle the owner: %+v, want %+v", rec.Owner, anc[0])
		}
	}
}

func TestAgyAutoModeUnavailable(t *testing.T) {
	isolate(t)
	must(t, "init")
	if out, err := run(t, "", "agents", "auto-mode", "agy"); err == nil || !strings.Contains(err.Error()+out, "agy: auto-mode guidance is unavailable") {
		t.Errorf("auto-mode agy: %v\n%s", err, out)
	}
}

func TestHarnessesInUseAgy(t *testing.T) {
	tree := t.TempDir()
	if got := harnessesInUse(tree, agent.Catalog{"r": {Name: "r", Command: "agy"}}); !slices.Equal(got, []string{hook.Agy}) {
		t.Errorf("an agy binding: %q, want agy", got)
	}
	if h, ok := hook.ForCLI("agy"); !ok || h != hook.Agy {
		t.Errorf("ForCLI(agy) = %q, %v", h, ok)
	}
}
