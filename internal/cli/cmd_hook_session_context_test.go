package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/testutil"
)

// sessionContextRepo is a governed temp repo with a marked constitution, so
// runHookContext can prove the principle set actually arrives.
func sessionContextRepo(t *testing.T) string {
	t.Helper()
	testutil.IsolateHome(t)
	t.Setenv("SATELLE_CONFIG", "")
	t.Setenv("SATELLE_SESSION", "")
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, config.DefaultDataDir), 0o755); err != nil {
		t.Fatal(err)
	}
	const body = "---\ntype: constitution\n---\n\n# Project constitution\n\nCONSTITUTION-MARKER-sty_507d3d9c\n"
	if err := os.WriteFile(filepath.Join(repo, config.DefaultDataDir, config.DefaultConstitutionName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	return repo
}

func writeHarnessTOML(t *testing.T, repo, body string) {
	t.Helper()
	path := filepath.Join(repo, config.DefaultDataDir, config.ConfigName)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func grokDualPayload(event, camel, session string) []byte {
	b, err := json.Marshal(map[string]any{
		"hook_event_name": event,
		"hookEventName":   camel,
		"session_id":      session,
		"sessionId":       session,
		"cwd":             "/tmp",
	})
	if err != nil {
		panic(err)
	}
	return b
}

func parseHookContext(t *testing.T, stdout string) (event, content string, ok bool) {
	t.Helper()
	if strings.TrimSpace(stdout) == "" {
		return "", "", false
	}
	var doc struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("hook emitted non-JSON %q: %v", stdout, err)
	}
	return doc.HookSpecificOutput.HookEventName, doc.HookSpecificOutput.AdditionalContext, true
}

func runContext(t *testing.T, harness string, raw []byte) (event, content, stderr string, emitted bool) {
	t.Helper()
	var out, errb bytes.Buffer
	if err := runHookContext(&out, &errb, harness, raw); err != nil {
		t.Fatalf("runHookContext must fail open, got %v", err)
	}
	event, content, emitted = parseHookContext(t, out.String())
	return event, content, errb.String(), emitted
}

func TestHookContext_GrokSessionStartEmitsNothing(t *testing.T) {
	repo := sessionContextRepo(t)
	raw := grokDualPayload("SessionStart", "session_start", "sess-start")
	if _, _, _, emitted := runContext(t, "grok", raw); emitted {
		t.Fatal("grok SessionStart must not deliver; the channel is not SessionStart")
	}
	rt := config.Config{}.ResolveRuntimeDir(repo).Dir
	if _, err := os.Stat(filepath.Join(rt, "session-context")); err == nil {
		t.Fatal("a non-matching SessionStart must not create the once-marker")
	}
}

func TestHookContext_UnknownHarnessWithEventEmitsNothing(t *testing.T) {
	sessionContextRepo(t)
	raw := grokDualPayload("PostToolUse", "post_tool_use", "sess-unknown")
	if _, _, _, emitted := runContext(t, "some-future-cli", raw); emitted {
		t.Fatal("a harness with no configured channel must emit nothing when an event is present")
	}
	if _, _, _, emitted := runContext(t, "some-future-cli", nil); !emitted {
		t.Fatal("a harness with no channel and no event still emits the diagnostic")
	}
}

// diagnosisPrincipleName names the fixture session principle.
const diagnosisPrincipleName = "satelle-diagnosis-is-hypothesis"

// diagnosisProofPhrase is a sentence fragment that lives in the principle body
// and must not appear in the description or the first heading. principleIndexLine
// copies only those, so an indexed emit cannot contain this phrase.
const diagnosisProofPhrase = "unproven until discovery proves"

// diagnosisPrincipleFixture is a self-contained session-tagged principle. The
// tests build it in a temp repo; they do not read this repo's gitignored
// substrate. The proof phrase sits in the body only, never in the description
// or the first heading.
const diagnosisPrincipleFixture = `---
name: satelle-diagnosis-is-hypothesis
type: principle
tags: [type:principle, principles:session]
applies_to: ["*"]
description: A fixture principle whose body carries a phrase the index line cannot copy.
---

# satelle-diagnosis-is-hypothesis — a fixture heading

What you perceive about the code is unproven until discovery proves it. Record
it as intent and evidence.
`

func diagnosisParts(body string) (desc, heading, prose string) {
	prose = body
	fm := frontmatter(body)
	if fm != "" {
		if i := strings.Index(body, fm); i >= 0 {
			prose = body[i+len(fm):]
		}
	}
	for _, ln := range strings.Split(fm, "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "description:") {
			desc = strings.Trim(strings.TrimSpace(strings.TrimPrefix(t, "description:")), `"'`)
		}
	}
	for _, ln := range strings.Split(prose, "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "#") {
			heading = strings.TrimSpace(strings.TrimLeft(t, "#"))
			break
		}
	}
	return desc, heading, prose
}

func assertDiagnosisInlined(t *testing.T, harness, event, wantEvent, content string, emitted bool) {
	t.Helper()
	if !emitted {
		t.Fatalf("%s emitted nothing", harness)
	}
	if event != wantEvent {
		t.Errorf("%s hookEventName = %q, want %q", harness, event, wantEvent)
	}
	if !strings.Contains(content, diagnosisPrincipleName) || !strings.Contains(content, diagnosisProofPhrase) {
		t.Errorf("%s additionalContext missing the principle name or the body phrase %q:\n%s", harness, diagnosisProofPhrase, content)
	}
}

// TestHookContext_SeatPrinciplesStayOutOfSessionStart (sty_bcd344fd AC1, AC2;
// sty_73a2dc68 AC1):
// the seat-resident baseline principles are handed to dispatched seats only.
// The SessionStart render names neither — not inlined, not as a read pointer —
// so they cost the in-loop session's byte budget nothing.
func TestHookContext_SeatPrinciplesStayOutOfSessionStart(t *testing.T) {
	sessionContextRepo(t)
	_, content, _, emitted := runContext(t, "claude", []byte(`{"hook_event_name":"SessionStart","session_id":"sess-seat-claude"}`))
	if !emitted {
		t.Fatal("claude emitted nothing")
	}
	for _, name := range []string{"satelle-story-shape", "satelle-start-small", "satelle-dispatch-pointers"} {
		if strings.Contains(content, name) {
			t.Errorf("SessionStart render carries seat-only principle %s:\n%s", name, content)
		}
	}
	for _, phrase := range []string{"The card states a behaviour", "Ship the smallest slice", "never content it can fetch"} {
		if strings.Contains(content, phrase) {
			t.Errorf("SessionStart render carries seat-only text %q", phrase)
		}
	}
}
