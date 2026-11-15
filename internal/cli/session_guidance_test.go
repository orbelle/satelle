package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/substrate"
)

func guidanceLines(t *testing.T, dir string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, ".satelle", kernel.SessionFile))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	if len(out) == 0 {
		t.Fatalf("%s is empty", kernel.SessionFile)
	}
	return out
}

func writeGuidance(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".satelle", kernel.SessionFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func guidanceChannels(t *testing.T, dir string, grokStart, grokStop []byte) (texts, ids map[string]string) {
	t.Helper()
	texts, ids = map[string]string{}, map[string]string{}
	for _, h := range []string{"claude", "pi"} {
		session := "g-" + h
		id := stopAt(t, session, "in_progress", "approved")
		payload := fmt.Sprintf(`{"session_id":%q,"cwd":%q}`, session, dir)
		for _, ev := range []string{"context", "prompt"} {
			raw, err := run(t, payload, "hook", ev, "--harness", h)
			if err != nil {
				t.Fatal(err)
			}
			texts[h+" "+ev], ids[h+" "+ev] = hookText(raw), id
		}
		texts[h+" stop"], ids[h+" stop"] = blockReason(t, stopcheck(t, dir, session, h)), id
	}

	id := stopAt(t, "g-cursor", "in_progress", "approved")
	cursor := `{"conversation_id":"g-cursor","session_id":"g-cursor","cursor_version":"2026.11.05-86047df","workspace_roots":[` + jsonString(dir) + `]`
	_, doc := cursorAnswer(t, cursor+`,"hook_event_name":"sessionStart"}`, "context")
	texts["cursor sessionStart"], _ = doc["additional_context"].(string)
	_, doc = cursorAnswer(t, cursor+`,"hook_event_name":"stop","status":"completed"}`, "stopcheck")
	texts["cursor stop"], _ = doc["followup_message"].(string)
	ids["cursor sessionStart"], ids["cursor stop"] = id, id

	id = stopAt(t, "grok-session-0001", "in_progress", "approved")
	var start map[string]any
	if err := json.Unmarshal(grokStart, &start); err != nil {
		t.Fatal(err)
	}
	start["cwd"], start["workspaceRoot"] = dir, dir
	payload, _ := json.Marshal(start)
	raw, err := run(t, string(payload), "hook", "context", "--harness", "grok")
	if err != nil {
		t.Fatal(err)
	}
	var answer map[string]any
	if err := json.Unmarshal([]byte(raw), &answer); err != nil {
		t.Fatalf("grok SessionStart answer %q: %v", raw, err)
	}
	texts["grok systemMessage"], _ = answer["systemMessage"].(string)
	var stop map[string]any
	if err := json.Unmarshal(grokStop, &stop); err != nil {
		t.Fatal(err)
	}
	stop["cwd"], stop["workspaceRoot"] = dir, dir
	payload, _ = json.Marshal(stop)
	texts["grok stop"] = blockReason(t, stopWith(t, string(payload), "grok"))
	ids["grok systemMessage"], ids["grok stop"] = id, id
	return texts, ids
}

func TestSessionGuidanceReachesEveryChannel(t *testing.T) {
	grokStart, grokStop := grokFixture(t, "grok_session_start.json"), grokFixture(t, "grok_stop.json")
	dir := stopFixture(t)
	lines := guidanceLines(t, dir)
	texts, ids := guidanceChannels(t, dir, grokStart, grokStop)
	for ch, text := range texts {
		hasAll(t, ch, text, lines...)
		hasAll(t, ch, text, ids[ch])
		if strings.HasSuffix(ch, "stop") {
			hasAll(t, ch, text, "next is satelle queue fire release --session ")
		} else {
			hasAll(t, ch, text, "satelle session: ", "drive me")
		}
	}
	ctx := texts["claude context"]
	if i, j := strings.Index(ctx, "satelle session: g-claude"), strings.Index(ctx, lines[0]); i < 0 || j < i {
		t.Errorf("claude context does not print the session line before the guidance:\n%s", ctx)
	}
	reason := texts["claude stop"]
	if i, j := strings.Index(reason, "next is "), strings.Index(reason, lines[0]); i < 0 || j < i {
		t.Errorf("claude stop reason does not print the move before the guidance:\n%s", reason)
	}
}

func TestSessionGuidanceIsNotAGoLiteral(t *testing.T) {
	pkg, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := isolate(t)
	must(t, "init")
	var sentences []string
	for _, l := range guidanceLines(t, dir) {
		for _, s := range strings.Split(l, ". ") {
			if s = strings.TrimSuffix(strings.TrimSpace(s), "."); len(s) > 40 {
				sentences = append(sentences, s)
			}
		}
	}
	src, err := filepath.Glob(filepath.Join(pkg, "*.go"))
	if err != nil || len(src) == 0 {
		t.Fatalf("no Go source in %s: %v", pkg, err)
	}
	for _, p := range src {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range sentences {
			if strings.Contains(string(b), s) {
				t.Errorf("%s holds the guidance sentence %q", filepath.Base(p), s)
			}
		}
	}
}

func TestSessionGuidanceIsTheReposOwnWording(t *testing.T) {
	grokStart, grokStop := grokFixture(t, "grok_session_start.json"), grokFixture(t, "grok_stop.json")
	dir := stopFixture(t)
	shipped := guidanceLines(t, dir)
	writeGuidance(t, dir, "nonce-guidance-7c41: this repo's own words.\n")
	texts, _ := guidanceChannels(t, dir, grokStart, grokStop)
	for ch, text := range texts {
		hasAll(t, ch, text, "nonce-guidance-7c41: this repo's own words.")
		hasNone(t, ch, text, shipped...)
	}
}

func TestSessionGuidanceMissingFileIsNamed(t *testing.T) {
	grokStart, grokStop := grokFixture(t, "grok_session_start.json"), grokFixture(t, "grok_stop.json")
	dir := stopFixture(t)
	shipped := guidanceLines(t, dir)
	if err := os.Remove(filepath.Join(dir, ".satelle", kernel.SessionFile)); err != nil {
		t.Fatal(err)
	}
	texts, ids := guidanceChannels(t, dir, grokStart, grokStop)
	for ch, text := range texts {
		hasAll(t, ch, text, ".satelle/session.md is missing", ids[ch])
		hasNone(t, ch, text, shipped...)
		if strings.HasSuffix(ch, "stop") {
			hasAll(t, ch, text, "next is satelle queue fire release --session ")
		} else {
			hasAll(t, ch, text, "satelle session: ", "drive me")
		}
	}
	hasAll(t, "claude context", texts["claude context"], "satelle init would add .satelle/session.md")
}

func TestShippedSessionGuidanceNamesTheBinary(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	b, err := os.ReadFile(filepath.Join(dir, ".satelle", kernel.SessionFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range substrate.CheckText(".satelle/"+kernel.SessionFile, "md", string(b), substrateTree(NewRootCmd()), nil) {
		t.Error(f.String())
	}
}

func TestInjectFileFollowsTheGuidanceAtSessionStartOnly(t *testing.T) {
	dir := stopFixture(t)
	lines := guidanceLines(t, dir)
	skill := "---\nname: inject-probe\ndescription: a probe\n---\n\nnonce-inject-3f9a\n"
	if err := os.WriteFile(filepath.Join(dir, ".satelle", "skills", "inject-probe.md"), []byte(skill), 0o644); err != nil {
		t.Fatal(err)
	}
	writeHookSettings(t, dir, "inject = [\".satelle/skills/inject-probe.md\"]\n")
	stopAt(t, "s-inj", "in_progress", "approved")
	payload := fmt.Sprintf(`{"session_id":"s-inj","cwd":%q}`, dir)
	raw, err := run(t, payload, "hook", "context", "--harness", "claude")
	if err != nil {
		t.Fatal(err)
	}
	ctx := hookText(raw)
	if g, i := strings.Index(ctx, lines[len(lines)-1]), strings.Index(ctx, "nonce-inject-3f9a"); g < 0 || i < g {
		t.Errorf("session-start context lacks the inject file after the guidance:\n%s", ctx)
	}
	raw, err = run(t, payload, "hook", "prompt", "--harness", "claude")
	if err != nil {
		t.Fatal(err)
	}
	hasNone(t, "claude prompt", hookText(raw), "nonce-inject-3f9a")
	hasNone(t, "claude stop", blockReason(t, stopcheck(t, dir, "s-inj", "claude")), "nonce-inject-3f9a")
}
