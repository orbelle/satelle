package agy_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/integration/agy"
	"github.com/bobmcallan/satelle/internal/integration/common"
)

func cmd(verb string) string { return common.Command(verb, agy.Name) }

func decodeDoc(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("%v:\n%s", err, b)
	}
	return doc
}

func TestAgyPlanWritesSatelleOwner(t *testing.T) {
	body, changed, err := agy.Hook{}.Plan(nil, true, nil, false)
	if err != nil || !changed {
		t.Fatalf("plan: changed=%v err=%v", changed, err)
	}
	want := map[string]any{"satelle": map[string]any{
		"PreToolUse": []any{map[string]any{
			"matcher": "write_file|edit_file|write_to_file|replace_file_content|multi_replace_file_content|sed_file|notebook_edit|run_command",
			"hooks":   []any{map[string]any{"type": "command", "command": cmd(common.VerbGate), "timeout": float64(10)}},
		}},
		"SessionStart":  []any{map[string]any{"type": "command", "command": cmd(common.VerbContext), "timeout": float64(10)}},
		"PreInvocation": []any{map[string]any{"type": "command", "command": cmd(common.VerbPrompt), "timeout": float64(10)}},
		"Stop":          []any{map[string]any{"type": "command", "command": cmd(common.VerbStopcheck), "timeout": float64(10)}},
	}}
	if got := decodeDoc(t, body); !reflect.DeepEqual(got, want) {
		t.Fatalf("plan body:\n%s", body)
	}
	if _, changed, _ := (agy.Hook{}).Plan(body, false, nil, false); changed {
		t.Error("a second plan on its own output changed the file")
	}
	if !(agy.Hook{}).Installed(body) || (agy.Hook{}).Installed([]byte(`{"other":{}}`)) {
		t.Error("Installed")
	}
}

func TestAgyPlanPreservesOtherOwners(t *testing.T) {
	prev := `{
  "other-plugin": {"Stop": [{"type": "command", "command": "./mine.sh"}], "Note": "keep"},
  "satelle": {
    "Stop": [{"type": "command", "command": "satelle hook stopcheck --harness agy"}, {"type": "command", "command": "./user-stop.sh"}],
    "PreToolUse": [{"matcher": "write_file", "hooks": [{"type": "command", "command": "satelle hook gate --harness agy"}]}],
    "SessionStart": [{"type": "command", "command": "satelle reindex"}]
  }
}`
	stale := func(c string) bool { return c == "satelle reindex" }
	body, changed, err := agy.Hook{}.Plan([]byte(prev), false, stale, false)
	if err != nil || !changed {
		t.Fatalf("plan: changed=%v err=%v", changed, err)
	}
	doc := decodeDoc(t, body)
	other := doc["other-plugin"].(map[string]any)
	if other["Note"] != "keep" || len(other["Stop"].([]any)) != 1 {
		t.Errorf("other-plugin changed:\n%s", body)
	}
	for _, c := range []string{"./mine.sh", "./user-stop.sh"} {
		if !strings.Contains(string(body), c) {
			t.Errorf("%s lost:\n%s", c, body)
		}
	}
	if strings.Contains(string(body), "satelle reindex") {
		t.Errorf("stale entry kept:\n%s", body)
	}
	for _, verb := range common.Verbs {
		if n := strings.Count(string(body), cmd(verb)); n != 1 {
			t.Errorf("%s appears %d times:\n%s", verb, n, body)
		}
	}
	cmds, err := commandsOf(t, body)
	if err != nil || !slices.Contains(cmds, "./mine.sh") || !slices.Contains(cmds, cmd(common.VerbGate)) {
		t.Errorf("Commands: %v %v", cmds, err)
	}

	kept, removeFile, err := agy.Hook{}.Remove(body)
	if err != nil || removeFile || strings.Contains(string(kept), "satelle hook") || !strings.Contains(string(kept), "./mine.sh") || !strings.Contains(string(kept), "./user-stop.sh") {
		t.Fatalf("remove: removeFile=%v err=%v\n%s", removeFile, err, kept)
	}
	only, _, _ := agy.Hook{}.Plan(nil, true, nil, false)
	if _, removeFile, err := (agy.Hook{}).Remove(only); err != nil || !removeFile {
		t.Errorf("remove of a satelle-only file: removeFile=%v err=%v", removeFile, err)
	}
}

func commandsOf(t *testing.T, body []byte) ([]string, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "hooks.json")
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := (agy.Hook{}).Commands(filepath.Join(t.TempDir(), "absent.json")); got != nil || err != nil {
		t.Errorf("absent file: %v %v", got, err)
	}
	return agy.Hook{}.Commands(p)
}

func TestAgyAnswers(t *testing.T) {
	h := agy.Hook{}
	for _, c := range []struct{ got, want string }{
		{string(h.Context(common.EventSessionStart, "T", "N")), `{"injectSteps":[{"systemMessage":{"systemMessage":"T"}},{"systemMessage":{"systemMessage":"N"}}]}`},
		{string(h.Context(common.EventPrompt, "T", "N")), `{"injectSteps":[{"systemMessage":{"systemMessage":"T"}}]}`},
		{string(h.Context(common.EventPrompt, "", "")), `{}`},
		{string(h.Deny("why")), `{"decision":"deny","reason":"why"}`},
		{string(h.StopBlock("owed")), `{"decision":"continue","reason":"owed"}`},
		{string(h.StopAnswer()), `{}`},
	} {
		if c.got != c.want {
			t.Errorf("got %s, want %s", c.got, c.want)
		}
	}
	if _, err := h.AutoModeDisplay(); err == nil || !strings.Contains(err.Error(), "agy: auto-mode guidance is unavailable") {
		t.Errorf("auto-mode: %v", err)
	}
}

func fixture(t *testing.T, name string, edit func(map[string]any)) []byte {
	t.Helper()
	doc := decodeDoc(t, []byte(read(t, filepath.Join("testdata", name))))
	if edit != nil {
		edit(doc)
	}
	b, _ := json.Marshal(doc)
	return b
}

func TestAgyParseStopAndInvocation(t *testing.T) {
	h := agy.Hook{}
	for _, c := range []struct {
		name string
		edit func(map[string]any)
		want string
	}{
		{"turn end", nil, common.TurnEndReason},
		{"continuation", func(d map[string]any) { d["continuationActive"] = true }, "continuation_active"},
		{"other termination", func(d map[string]any) { d["terminationReason"] = "MAX_STEPS" }, "MAX_STEPS"},
		{"no reason", func(d map[string]any) { delete(d, "terminationReason") }, "unknown"},
	} {
		in, ok := h.ParseInput(fixture(t, "agy-stop.json", c.edit))
		if !ok || in.StopReason != c.want || in.Event != "Stop" {
			t.Errorf("%s: %+v", c.name, in)
		}
	}
	for name, want := range map[string]bool{"agy-preinvocation-turnstart.json": false, "agy-preinvocation-midturn.json": true} {
		in, ok := h.ParseInput(fixture(t, name, nil))
		if !ok || in.MidTurn != want || in.Session != "149858c1-00ba-48ef-a77b-8fc82fbe5f6c" {
			t.Errorf("%s: %+v", name, in)
		}
	}
	if _, ok := h.ParseInput([]byte(`{"hookEventName":"SessionStart","sessionId":"g"}`)); ok {
		t.Error("agy claimed a payload with no conversationId and artifactDirectoryPath")
	}
}
