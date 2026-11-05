package verb

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/ledger"
)

func init() {
	Register(&Verb{Name: "story-log", Description: "Record a typed telemetry/quality event against a story", Invoke: storyLog})
}

// telemetryEnvelope is the payload shape of every KindTelemetryEvent row: a
// caller-named event kind (e.g. "step-self-report", "agent-retry") plus its
// typed data fields. Shared by the writer (AppendTelemetry) and every reader
// (ComputeStoryCost, a future `ledger list --kind telemetry_event` consumer),
// so the shape is single-sourced.
type telemetryEnvelope struct {
	Kind string         `json:"kind"`
	Data map[string]any `json:"data,omitempty"`
}

// TelemetryPlanConsumed is the event kind recording that a dispatched performer's
// payload carried the document its step consumes (the plan). TelemetrySourceDispatch
// is the data["source"] marking a row the dispatch engine wrote itself, as opposed
// to the same kind logged by a driver with `satelle story log`.
const (
	TelemetryPlanConsumed   = "plan-consumed"
	TelemetrySourceDispatch = "dispatch"
)

// secretKeyPattern matches a data key that looks like it names a credential —
// refused outright regardless of its value (AC4: secrets/env are never
// recorded in a logged event). Deliberately excludes the bare word "token":
// this codebase's own cost payloads legitimately carry tokens_in/tokens_out/
// tokens_total (an LLM token COUNT, not a credential) — a compound like
// auth_token/access_token/bearer_token is still caught via "auth"/"bearer".
var secretKeyPattern = regexp.MustCompile(`(?i)(secret|password|passwd|api[_-]?key|credential|auth|bearer|access[_-]?token|refresh[_-]?token)`)

// looksLikeSecret reports whether a string VALUE is shaped like a credential:
// a long dense alphanumeric run (bearer tokens, API keys) or a well-known
// credential prefix. A generic telemetry event is caller-suppliable — unlike
// the purpose-built step_cost/agent_invocation payloads, which structurally
// carry only numbers and names — so it needs this explicit value-shape check
// too, not just a key-name check.
func looksLikeSecret(s string) bool {
	for _, prefix := range []string{"sk-", "ghp_", "gho_", "glpat-", "xox", "Bearer "} {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	if len(s) <= 40 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
		default:
			return false // punctuation/whitespace — not a dense token-shaped string
		}
	}
	return true
}

// validateTelemetryData refuses a data map carrying an apparent secret — a
// credential-ish key name, or a token-shaped value — so a logged event can
// never leak env/secrets (AC4, sty_b73c3236).
func validateTelemetryData(data map[string]any) error {
	for k, v := range data {
		if secretKeyPattern.MatchString(k) {
			return fmt.Errorf("verb: telemetry data key %q looks like a credential — refused", k)
		}
		if s, ok := v.(string); ok && looksLikeSecret(s) {
			return fmt.Errorf("verb: telemetry data key %q's value looks like a credential/token — refused", k)
		}
	}
	return nil
}

// AppendTelemetry records one typed telemetry/quality event against a story —
// the generic write primitive (AC1) that also backs the dispatch engine's
// structured retry/failure/timeout capture (AC2) via agentstep.Engine.SetTelemetry.
// Best-effort at the ledger layer (nil store is a no-op, matching appendLedger),
// but VALIDATES data before writing — a caller-suppliable event must never
// carry a secret onto the ledger.
func AppendTelemetry(ctx context.Context, storyID, actor, kind string, data map[string]any) error {
	if kind == "" {
		return fmt.Errorf("verb: telemetry kind required")
	}
	if err := validateTelemetryData(data); err != nil {
		return err
	}
	payload, err := json.Marshal(telemetryEnvelope{Kind: kind, Data: data})
	if err != nil {
		return err
	}
	now := time.Now()
	body := fmt.Sprintf("telemetry: %s (%s)", kind, joinTelemetryData(data))
	appendLedgerEntry(ctx, storyID, ledger.KindTelemetryEvent, actor, body, payload, now)
	return nil
}

// refusedTelemetryValue replaces a string value validateTelemetryData would
// refuse, so a row keeps its other fields (see AppendTelemetryKeepRow).
const refusedTelemetryValue = "[refused: credential-shaped]"

// AppendTelemetryKeepRow is AppendTelemetry for the engine's own dispatch
// telemetry (agent-retry/agent-failure/agent-timeout), whose rows carry the
// outcome class a diagnosis and the timeline read. When validation refuses the
// row, every credential-shaped string VALUE is replaced by a marker and the row
// is appended again, so a token-shaped cause never costs the row its outcome.
// A data key whose NAME looks like a credential is still dropped.
func AppendTelemetryKeepRow(ctx context.Context, storyID, actor, kind string, data map[string]any) error {
	err := AppendTelemetry(ctx, storyID, actor, kind, data)
	if err == nil || kind == "" {
		return err
	}
	kept := make(map[string]any, len(data))
	for k, v := range data {
		if secretKeyPattern.MatchString(k) {
			continue
		}
		if s, ok := v.(string); ok && looksLikeSecret(s) {
			v = refusedTelemetryValue
		}
		kept[k] = v
	}
	return AppendTelemetry(ctx, storyID, actor, kind, kept)
}

// joinTelemetryData renders data as a comma-separated "key=value" list for a
// ledger body, in sorted-key order for determinism.
func joinTelemetryData(data map[string]any) string {
	if len(data) == 0 {
		return ""
	}
	kv := make(map[string]string, len(data))
	for k, v := range data {
		kv[k] = fmt.Sprintf("%v", v)
	}
	pairs := make([]string, 0, len(kv))
	for _, k := range joinedKeys(kv) {
		pairs = append(pairs, fmt.Sprintf("%s=%s", k, kv[k]))
	}
	return strings.Join(pairs, ", ")
}

// storyLogReq is the request body for story-log: a story id, the event kind,
// and its typed data fields.
type storyLogReq struct {
	ID   string         `json:"id"`
	Kind string         `json:"kind"`
	Data map[string]any `json:"data,omitempty"`
}

// storyLog is the generic telemetry/quality event write verb (AC1,
// sty_b73c3236), retiring the narrow story-step-cost verb: any typed event —
// a step's self-reported cost, a quality signal a future prompted channel
// emits — is recorded the same way.
func storyLog(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	store, err := requireWorkItem()
	if err != nil {
		return nil, err
	}
	var req storyLogReq
	if err := decode(raw, &req); err != nil {
		return nil, err
	}
	if req.ID == "" {
		return nil, fmt.Errorf("verb: id required")
	}
	if req.Kind == "" {
		return nil, fmt.Errorf("verb: log requires --kind")
	}
	it, err := store.Get(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	if err := AppendTelemetry(ctx, it.ID, "executor", req.Kind, req.Data); err != nil {
		return nil, err
	}
	notifyChange(panelTopic(it.Kind))
	return json.Marshal(map[string]any{"story_id": it.ID, "kind": req.Kind, "recorded": true})
}
