package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/ledger"
)

func setupSessionWorkflow(t *testing.T) (dir, id string) {
	t.Helper()
	dir = isolateCursor(t)
	must(t, "init")
	writePlanWorkflow(t, dir)
	must(t, "validate")
	id = createID(t, "story", "create", "--title", "session work", "--workflow", "planned")
	must(t, "story", "set", id, "--status", "st-plan")
	return dir, id
}

func ledgerPerformerRuns(t *testing.T, id string) []kernel.PerformerRun {
	t.Helper()
	out := must(t, "ledger", "list", id, "--kind", kernel.KindPerformerRun, "--json")
	var evs []ledger.Event
	if err := json.Unmarshal([]byte(out), &evs); err != nil {
		t.Fatalf("decode ledger list: %v\n%s", err, out)
	}
	runs := make([]kernel.PerformerRun, 0, len(evs))
	for _, e := range evs {
		var pr kernel.PerformerRun
		if err := e.Decode(&pr); err != nil {
			t.Fatalf("decode performer.run: %v\n%s", err, e.Payload)
		}
		runs = append(runs, pr)
	}
	return runs
}

func TestSessionPerformerCLICursorModelExposed(t *testing.T) {
	t.Run("cursor-agent sessionStart preserved over preToolUse", func(t *testing.T) {
		dir, id := setupSessionWorkflow(t)
		const sess = "c-model-1"
		// Hook context with real cursor-sessionstart fixture (model = gemini-3.8-flash-high).
		cursorAnswer(t, cursorPayload(t, "sessionstart", sess, dir, "", ""), "context")
		// PreToolUse with real cursor-pretooluse-write fixture (model = gemini-3.8-flash) must not clobber.
		cursorAnswer(t, cursorPayload(t, "pretooluse-write", sess, dir, filepath.Join(dir, "ok.txt"), ""), "gate")

		must(t, "story", "seat", id, "--session", sess)
		must(t, "story", "set", id, "--status", "st-build", "--session", sess)

		runs := ledgerPerformerRuns(t, id)
		if len(runs) != 1 {
			t.Fatalf("got %d performer.run events, want 1: %+v", len(runs), runs)
		}
		got := runs[0]
		if got.State != "st-plan" || got.Binding != "agent-x9" || got.Skill != "work" ||
			got.Command != agent.PresetSession || got.Harness != "cursor" || got.Model != "gemini-3.8-flash-high" {
			t.Fatalf("performer.run = %+v", got)
		}
		must(t, "ledger", "verify")
	})

	t.Run("Cursor IDE model_id", func(t *testing.T) {
		dir, id := setupSessionWorkflow(t)
		const sess = "c-ide-1"
		payload, _ := json.Marshal(map[string]any{
			"hook_event_name": "sessionStart",
			"session_id":      sess,
			"conversation_id": sess,
			"cursor_version":  "3.28.7",
			"model_id":        "claude-4.5-sonnet",
			"workspace_roots": []string{dir},
		})
		cursorAnswer(t, string(payload), "context")

		must(t, "story", "seat", id, "--session", sess)
		must(t, "story", "set", id, "--status", "st-build", "--session", sess)

		runs := ledgerPerformerRuns(t, id)
		if len(runs) != 1 || runs[0].Command != agent.PresetSession || runs[0].Harness != "cursor" || runs[0].Model != "claude-4.5-sonnet" {
			t.Fatalf("performer.run = %+v", runs)
		}
		must(t, "ledger", "verify")
	})

	t.Run("prompt-time model switch", func(t *testing.T) {
		dir, id := setupSessionWorkflow(t)
		const sess = "c-switch-1"
		cursorAnswer(t, cursorPayload(t, "sessionstart", sess, dir, "", ""), "context")
		promptPayload, _ := json.Marshal(map[string]any{
			"hook_event_name": "beforeSubmitPrompt",
			"session_id":      sess,
			"conversation_id": sess,
			"cursor_version":  "2026.11.05-86047df",
			"model":           "claude-opus-5-5-high",
			"workspace_roots": []string{dir},
		})
		cursorAnswer(t, string(promptPayload), "prompt")

		must(t, "story", "seat", id, "--session", sess)
		must(t, "story", "set", id, "--status", "st-build", "--session", sess)

		runs := ledgerPerformerRuns(t, id)
		if len(runs) != 1 || runs[0].Command != agent.PresetSession || runs[0].Harness != "cursor" || runs[0].Model != "claude-opus-5-5-high" {
			t.Fatalf("performer.run = %+v", runs)
		}
		must(t, "ledger", "verify")
	})

	t.Run("cursor via claude hook without cursor install", func(t *testing.T) {
		dir, id := setupSessionWorkflow(t)
		const sess = "c-compat-1"
		if out, err := run(t, cursorPayload(t, "sessionstart", sess, dir, "", ""), "hook", "context", "--harness", "claude"); err != nil {
			t.Fatalf("hook context: %v\n%s", err, out)
		}
		must(t, "story", "seat", id, "--session", sess)
		must(t, "story", "set", id, "--status", "st-build", "--session", sess)

		runs := ledgerPerformerRuns(t, id)
		if len(runs) != 1 || runs[0].Command != agent.PresetSession || runs[0].Harness != "cursor" || runs[0].Model != "gemini-3.8-flash-high" {
			t.Fatalf("performer.run = %+v", runs)
		}
		must(t, "ledger", "verify")
	})
}

func TestSessionPerformerCLIUnavailableModelAcrossHarnesses(t *testing.T) {
	grokFixture := func(t *testing.T, dir, sess string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(hookTestdata, "grok_session_start.json"))
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatal(err)
		}
		doc["sessionId"] = sess
		doc["cwd"] = dir
		doc["workspaceRoot"] = dir
		out, _ := json.Marshal(doc)
		return string(out)
	}

	cases := []struct {
		harness string
		payload func(t *testing.T, dir, sess string) string
	}{
		{
			harness: "grok",
			payload: grokFixture,
		},
		{
			harness: "claude",
			payload: func(t *testing.T, dir, sess string) string {
				b, _ := json.Marshal(map[string]any{"hook_event_name": "SessionStart", "session_id": sess, "cwd": dir})
				return string(b)
			},
		},
		{
			harness: "pi",
			payload: func(t *testing.T, dir, sess string) string {
				b, _ := json.Marshal(map[string]any{"hook_event_name": "SessionStart", "session_id": sess, "cwd": dir})
				return string(b)
			},
		},
		{
			harness: "cursor",
			payload: func(t *testing.T, dir, sess string) string {
				b, _ := json.Marshal(map[string]any{
					"hook_event_name": "sessionStart",
					"session_id":      sess,
					"conversation_id": sess,
					"cursor_version":  "2026.11.05-86047df",
					"workspace_roots": []string{dir},
				})
				return string(b)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.harness, func(t *testing.T) {
			dir, id := setupSessionWorkflow(t)
			sess := "s-" + tc.harness
			if out, err := run(t, tc.payload(t, dir, sess), "hook", "context", "--harness", tc.harness); err != nil {
				t.Fatalf("hook context (%s): %v\n%s", tc.harness, err, out)
			}
			must(t, "story", "seat", id, "--session", sess)
			must(t, "story", "set", id, "--status", "st-build", "--session", sess)

			runs := ledgerPerformerRuns(t, id)
			wantModel := kernel.UnavailableModel(tc.harness)
			if len(runs) != 1 || runs[0].Command != agent.PresetSession || runs[0].Harness != tc.harness || runs[0].Model != wantModel {
				t.Fatalf("%s performer.run = %+v, want command=session harness=%q model=%q", tc.harness, runs, tc.harness, wantModel)
			}
			must(t, "ledger", "verify")
		})
	}

	t.Run("env fallback with no hook run", func(t *testing.T) {
		_, id := setupSessionWorkflow(t)
		t.Setenv("GROK_AGENT", "1")
		const sess = "s-env-grok"
		must(t, "story", "seat", id, "--session", sess)
		must(t, "story", "set", id, "--status", "st-build", "--session", sess)

		runs := ledgerPerformerRuns(t, id)
		if len(runs) != 1 || runs[0].Command != agent.PresetSession || runs[0].Harness != "grok" || runs[0].Model != "unavailable (grok)" {
			t.Fatalf("env fallback performer.run = %+v", runs)
		}
		must(t, "ledger", "verify")
	})

	t.Run("hook record overrides ambient env marker", func(t *testing.T) {
		dir, id := setupSessionWorkflow(t)
		t.Setenv("GROK_AGENT", "1")
		const sess = "s-rec-over-env"
		cursorAnswer(t, cursorPayload(t, "sessionstart", sess, dir, "", ""), "context")
		must(t, "story", "seat", id, "--session", sess)
		must(t, "story", "set", id, "--status", "st-build", "--session", sess)

		runs := ledgerPerformerRuns(t, id)
		if len(runs) != 1 || runs[0].Harness != "cursor" || runs[0].Model != "gemini-3.8-flash-high" {
			t.Fatalf("hook record should override env marker: %+v", runs)
		}
	})
}
