package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/lease"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/wfdot"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// evaluateFirstSeat collapses evaluateSeat's live SET to the single seat these
// cases were written against — each drives exactly one lease, so "the live one"
// is unambiguous. Co-holding cases call evaluateSeat directly (sty_c098dc2d).
func evaluateFirstSeat(leases []lease.Lease, items []workitem.Item, wfs []docindex.Doc, now time.Time) (seatInfo, seatInfo, error) {
	live, other, err := evaluateSeat(leases, items, wfs, now)
	if len(live) == 0 {
		return seatInfo{}, other, err
	}
	return live[0], other, err
}

func doc(name, body string) docindex.Doc {
	return docindex.Doc{Kind: "principles", Name: name, Body: body}
}

const sessionFM = "---\nname: c\ntags: [kind:principle, principles:session]\n---\n# Body\nresident text\n"
const plainFM = "---\nname: p\ntags: [kind:principle]\n---\n# Other\nnot resident\n"

func TestFrontmatterTags_inlineAndBlock(t *testing.T) {
	inline := frontmatterTags("---\ntags: [a, principles:session, c]\n---\nx")
	if len(inline) != 3 || inline[1] != "principles:session" {
		t.Fatalf("inline parse: %v", inline)
	}
	block := frontmatterTags("---\nname: x\ntags:\n  - a\n  - principles:session\nother: y\n---\nbody")
	if len(block) != 2 || block[1] != "principles:session" {
		t.Fatalf("block parse: %v", block)
	}
	if frontmatterTags("no frontmatter here") != nil {
		t.Fatalf("expected nil tags for no frontmatter")
	}
}

func TestResolveContextHarness(t *testing.T) {
	claudeEvt := []byte(`{"hook_event_name":"SessionStart","transcript_path":"/home/u/.claude/projects/x/s.jsonl"}`)
	cases := []struct {
		name, flag string
		raw        []byte
		env        []string
		want       string
	}{
		{"flag wins", "grok", claudeEvt, nil, "grok"},
		{"event sniff", "", claudeEvt, nil, "claude"},
		{"env marker", "", []byte(`{}`), []string{"GROK_AGENT=1"}, "grok"},
		{"nothing recognised is unknown, not claude", "", []byte(`{}`), nil, "unknown"},
	}
	for _, c := range cases {
		if got := resolveContextHarness(c.flag, c.raw, c.env); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// Note: executorStates has been removed. The hook now uses wfdot.Spec.NonTerminalEngagingStates()
// which reads shape markers (Mdiamond=start, Msquare=terminal) from the DOT rather than
// hardcoding state names. See TestNonTerminalEngagingStates in wfdot package.

// TestCommitDenyReason (sty_577d292f): deny text teaches pre-execution semantics
// and plan/in_progress; fused engage+commit gets the split-into-two-calls text.
// Gate behavior is unchanged — both paths are still deny strings only.
func TestCommitDenyReason(t *testing.T) {
	// Base deny: pre-execution + both engaging statuses.
	base := commitDenyReason("git commit -m x")
	if base != noEngagedStoryCommitReason {
		t.Fatalf("plain commit deny: got %q", base)
	}
	for _, want := range []string{
		"BEFORE the command executes",
		"SEPARATE, PRIOR",
		"--status plan",
		"--status in_progress",
	} {
		if !strings.Contains(base, want) {
			t.Errorf("noEngagedStoryCommitReason missing %q:\n%s", want, base)
		}
	}

	// Fused pattern: engage + commit in one payload → explicit split message.
	fusedCmd := `./satelle story set sty_abc --status in_progress
git add .
git commit -m "docs (sty_abc)"`
	fused := commitDenyReason(fusedCmd)
	if fused != fusedEngageAndCommitReason {
		t.Fatalf("fused deny: got %q", fused)
	}
	for _, want := range []string{
		"fused engage+commit",
		"BEFORE any line runs",
		"TWO tool calls",
		"plan or in_progress",
	} {
		if !strings.Contains(fused, want) {
			t.Errorf("fusedEngageAndCommitReason missing %q:\n%s", want, fused)
		}
	}

	// story set without --status is not the engage pattern; base deny.
	if got := commitDenyReason("satelle story set sty_x\ngit push"); got != noEngagedStoryCommitReason {
		t.Errorf("story set without --status should use base deny, got %q", got)
	}
	if !isFusedEngageAndCommit(fusedCmd) {
		t.Error("isFusedEngageAndCommit(fused) = false, want true")
	}
	if isFusedEngageAndCommit("git commit -m only") {
		t.Error("isFusedEngageAndCommit(commit only) = true, want false")
	}
	if isFusedEngageAndCommit("satelle story set x --status plan") {
		t.Error("isFusedEngageAndCommit(engage only) = true, want false")
	}
}

func TestFilePathFromEvent(t *testing.T) {
	// Claude Code snake_case.
	if got := filePathFromEvent([]byte(`{"tool_input":{"file_path":"/a/b.go"}}`)); got != "/a/b.go" {
		t.Errorf("claude file_path = %q, want /a/b.go", got)
	}
	if got := filePathFromEvent([]byte(`{"tool_input":{"notebook_path":"/a/n.ipynb"}}`)); got != "/a/n.ipynb" {
		t.Errorf("claude notebook_path = %q, want /a/n.ipynb", got)
	}
	// Grok camelCase toolInput + nested filePath / path aliases (sty_0d3665ee).
	if got := filePathFromEvent([]byte(`{"toolInput":{"filePath":"/repo/.satelle/skills/x.md"}}`)); got != "/repo/.satelle/skills/x.md" {
		t.Errorf("grok filePath = %q", got)
	}
	if got := filePathFromEvent([]byte(`{"toolInput":{"path":"/tmp/scratch.go"}}`)); got != "/tmp/scratch.go" {
		t.Errorf("grok path = %q", got)
	}
	if got := filePathFromEvent([]byte(`{"toolInput":{"file_path":"/a/via-snake-under-camel.md"}}`)); got != "/a/via-snake-under-camel.md" {
		t.Errorf("grok toolInput.file_path = %q", got)
	}
	if got := filePathFromEvent([]byte(`{}`)); got != "" {
		t.Errorf("absent path should yield empty, got %q", got)
	}
}

func TestWithinRoot(t *testing.T) {
	const root = "/home/u/repo"
	cases := []struct {
		target string
		want   bool // true = inside repo; false = outside (gate REFUSES, sty_3026d890)
	}{
		{"/home/u/repo/internal/x.go", true},  // absolute, in-repo
		{"internal/x.go", true},               // relative, resolved under the repo cwd
		{"/tmp/claude/scratch/foo.sh", false}, // outside root (withinRoot helper only; fence uses gitRootOf)
		{"/home/u/other/x.go", false},         // outside root (foreign only if that path has a .git tree)
		{"", true},                            // empty target — stay conservative (no path → other rules)
	}
	for _, c := range cases {
		if got := withinRoot(root, c.target); got != c.want {
			t.Errorf("withinRoot(%q, %q) = %v, want %v", root, c.target, got, c.want)
		}
	}
}

// TestOutsideRepoRefusalMessage pins the foreign-tree lock copy (sty_a8454d10):
// names the path and foreign root; points at create/engage in THAT repo.
func TestOutsideRepoRefusalMessage(t *testing.T) {
	msg := outsideRepoEditErr("/home/u/satelle/internal/cli/cmd_publish.go", "/home/u/satelle").Error()
	for _, want := range []string{
		"another repo's tree",
		"/home/u/satelle/internal/cli/cmd_publish.go",
		"/home/u/satelle",
		"create/engage the story there",
		"satelle story create",
		"Temp/non-repo",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q:\n%s", want, msg)
		}
	}
}

// TestAnchorFrom (sty_aadd4d6c AC5): env pin beats cfgRoot; falls through when unset.
func TestAnchorFrom(t *testing.T) {
	pin := "/pinned/home"
	cfg := "/cfg/root"
	got := anchorFrom(func(k string) string {
		if k == "SATELLE_PROJECT_DIR" {
			return pin
		}
		return ""
	}, cfg)
	if got != pin {
		// Abs may expand; compare cleaned suffix.
		if !strings.HasSuffix(got, pin) && got != pin {
			// On Linux Abs of absolute is identity.
			if got != pin {
				// filepath.Abs on absolute path returns cleaned absolute.
				want, _ := filepath.Abs(pin)
				if got != want {
					t.Errorf("SATELLE_PROJECT_DIR pin: got %q want %q", got, want)
				}
			}
		}
	}
	got = anchorFrom(func(k string) string {
		if k == "CLAUDE_PROJECT_DIR" {
			return pin
		}
		return ""
	}, cfg)
	want, _ := filepath.Abs(pin)
	if got != want && got != pin {
		t.Errorf("CLAUDE_PROJECT_DIR pin: got %q want %q", got, want)
	}
	// Pin beats divergent cfgRoot.
	got = anchorFrom(func(k string) string {
		if k == "CLAUDE_PROJECT_DIR" {
			return pin
		}
		return ""
	}, "/other/cfg")
	if got != want && got != pin {
		t.Errorf("pin must beat cfgRoot: got %q", got)
	}
	// Fall through to cfgRoot when unset.
	got = anchorFrom(func(string) string { return "" }, cfg)
	wantCfg, _ := filepath.Abs(cfg)
	if got != wantCfg && got != cfg {
		t.Errorf("fallback cfgRoot: got %q want %q", got, wantCfg)
	}
	if got := anchorFrom(func(string) string { return "" }, ""); got != "" {
		t.Errorf("empty getenv+cfg: got %q", got)
	}
}

// TestOutsideAnchorBashReason names the path, foreign root, and opt-in (sty_a8454d10).
func TestOutsideAnchorBashReason(t *testing.T) {
	msg := outsideAnchorBashReason("/home/u/other/file.go", "/home/u/other")
	for _, want := range []string{
		"/home/u/other/file.go",
		"/home/u/other",
		"another repo's tree",
		"allow_outside_tree_edits",
		"open a session",
		"Temp/non-repo",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("outsideAnchorBashReason missing %q:\n%s", want, msg)
		}
	}
}

// TestEmitPreToolUseDenyClaudeSchema (sty_5e4bc568 AC1/AC5): Claude-branch deny
// JSON must validate against Claude Code's PreToolUse output shape — root keys
// exactly {hookSpecificOutput}, nested object has the three required fields, and
// NO top-level decision/reason (those fail schema validation and inert the gate).
// Map-key assertion encodes additionalProperties=false without a JSON-schema lib.
func TestEmitPreToolUseDenyClaudeSchema(t *testing.T) {
	var buf bytes.Buffer
	reason := noEngagedStoryEditReason
	if err := emitPreToolUseDeny(&buf, "claude", reason); err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(buf.String())
	var root map[string]any
	if err := json.Unmarshal([]byte(line), &root); err != nil {
		t.Fatalf("deny JSON: %v\n%s", err, line)
	}
	if len(root) != 1 {
		t.Errorf("Claude deny root keys = %v, want exactly {hookSpecificOutput}", keysOf(root))
	}
	if _, ok := root["hookSpecificOutput"]; !ok {
		t.Fatalf("Claude deny missing hookSpecificOutput:\n%s", line)
	}
	if _, ok := root["decision"]; ok {
		t.Errorf("Claude deny must NOT have top-level decision (schema reject):\n%s", line)
	}
	if _, ok := root["reason"]; ok {
		t.Errorf("Claude deny must NOT have top-level reason (schema reject):\n%s", line)
	}
	hso, ok := root["hookSpecificOutput"].(map[string]any)
	if !ok {
		t.Fatalf("hookSpecificOutput not an object: %T", root["hookSpecificOutput"])
	}
	if len(hso) != 3 {
		t.Errorf("hookSpecificOutput keys = %v, want exactly 3", keysOf(hso))
	}
	if hso["hookEventName"] != "PreToolUse" {
		t.Errorf("hookEventName = %v", hso["hookEventName"])
	}
	if hso["permissionDecision"] != "deny" {
		t.Errorf("permissionDecision = %v", hso["permissionDecision"])
	}
	if hso["permissionDecisionReason"] != reason {
		t.Errorf("permissionDecisionReason mismatch")
	}
	// Canonical no-story edit copy: both clauses the operator asked for, plus the
	// session-contract language (sty_8c3d345c) — a story session opens on engage and
	// stays open until a terminal/parked state (done, cancelled, or blocked).
	for _, want := range []string{
		"mutating the tree without a performing story",
		"wrong tool for reading",
		"read_file",
		"search_replace",
		"Open a story session",
		"stays open",
		"done, cancelled, or blocked",
	} {
		if !strings.Contains(reason, want) {
			t.Errorf("canonical reason missing %q:\n%s", want, reason)
		}
	}
}

// TestEmitPreToolUseDenyGrok (sty_5e4bc568 AC2): Grok-branch deny is top-level
// decision=deny + reason with NO hookSpecificOutput.
func TestEmitPreToolUseDenyGrok(t *testing.T) {
	var buf bytes.Buffer
	reason := noEngagedStoryEditReason
	if err := emitPreToolUseDeny(&buf, "grok", reason); err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(buf.String())
	var root map[string]any
	if err := json.Unmarshal([]byte(line), &root); err != nil {
		t.Fatalf("deny JSON: %v\n%s", err, line)
	}
	if root["decision"] != "deny" {
		t.Errorf("Grok decision = %v, want deny", root["decision"])
	}
	if root["reason"] != reason {
		t.Errorf("Grok reason mismatch")
	}
	if _, ok := root["hookSpecificOutput"]; ok {
		t.Errorf("Grok deny must NOT carry hookSpecificOutput:\n%s", line)
	}
	if len(root) != 2 {
		t.Errorf("Grok deny root keys = %v, want {decision,reason}", keysOf(root))
	}
}

// TestHarnessFromEvent (sty_5e4bc568 AC2, sty_719c4a7b AC3): a snake_case
// envelope corroborated by a .claude transcript or a Claude-only tool name →
// claude; camelCase-only toolInput → grok; a bare
// permission_mode with no such corroboration → unknown, because Grok's
// Claude-compat shim echoes that key too (real captured grok payloads carry
// it — see agentcli/testdata/hooks/README.md); ambiguous/empty → unknown,
// never claude by default (sty_37fd5470). The deny shape for unknown stays
// strict (TestDenyPreToolUseUnknownHarnessStaysStrict).
func TestHarnessFromEvent(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"claude envelope", `{"session_id":"s1","transcript_path":"/home/u/.claude/projects/p/s1.jsonl","hook_event_name":"PreToolUse","permission_mode":"default","tool_input":{"file_path":"/x.go"}}`, "claude"},
		{"claude tool name", `{"tool_name":"Edit","tool_input":{"command":"git commit -m x"}}`, "claude"},
		{"grok camelCase", `{"toolInput":{"path":"internal/x.go"}}`, "grok"},
		{"grok bash", `{"toolInput":{"command":"git push"}}`, "grok"},
		{"bare snake_case → unknown", `{"tool_input":{"file_path":"/x.go"}}`, "unknown"},
		{"both present → unknown", `{"tool_input":{},"toolInput":{}}`, "unknown"},
		{"empty", `{}`, "unknown"},
		{"null tool_input", `{"tool_input":null}`, "unknown"},
		{"bare permission_mode → unknown", `{"permission_mode":"default"}`, "unknown"},
		{"permission_mode + tool_input, no transcript → unknown", `{"permission_mode":"default","tool_input":{"file_path":"/x.go"}}`, "unknown"},
	}
	for _, c := range cases {
		if got := harnessFromEvent([]byte(c.raw)); got != c.want {
			t.Errorf("%s: harnessFromEvent = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestEmitPreToolUseDenyClaudeEnvelope: a non-Grok harness gets Claude's
// hookSpecificOutput deny, never the top-level decision shape.
func TestEmitPreToolUseDenyClaudeEnvelope(t *testing.T) {
	var buf bytes.Buffer
	if err := emitPreToolUseDeny(&buf, "claude", "no story engaged"); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("json: %v body=%s", err, buf.String())
	}
	hso, ok := doc["hookSpecificOutput"].(map[string]any)
	if !ok {
		t.Fatalf("want hookSpecificOutput: %s", buf.String())
	}
	if hso["permissionDecision"] != "deny" || hso["permissionDecisionReason"] != "no story engaged" || hso["hookEventName"] != "PreToolUse" {
		t.Errorf("hookSpecificOutput = %v", hso)
	}
	if _, ok := doc["decision"]; ok {
		t.Error("claude deny must not use top-level decision")
	}
}

// TestEmitPreToolUseDenyEmptyReasonFallback: an empty reason is replaced, so a
// deny never reaches the harness without one.
func TestEmitPreToolUseDenyEmptyReasonFallback(t *testing.T) {
	var buf bytes.Buffer
	if err := emitPreToolUseDeny(&buf, "claude", ""); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	_ = json.Unmarshal(buf.Bytes(), &doc)
	hso, _ := doc["hookSpecificOutput"].(map[string]any)
	if s, _ := hso["permissionDecisionReason"].(string); strings.TrimSpace(s) == "" {
		t.Fatalf("empty reason must be filled: %s", buf.String())
	}
}

// TestDenyPreToolUseUnknownHarnessStaysStrict (sty_37fd5470): an unrecognised
// envelope is recorded "unknown" but still gets the strict hookSpecificOutput
// deny shape and an error, so the sniff change cannot reopen the inert-gate bug.
func TestDenyPreToolUseUnknownHarnessStaysStrict(t *testing.T) {
	prev := hookHarnessFlag
	hookHarnessFlag = ""
	t.Cleanup(func() { hookHarnessFlag = prev })

	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	raw := []byte(`{"tool_input":{"file_path":"/tmp/x.go"}}`)
	if harnessFromEvent(raw) != "unknown" {
		t.Fatalf("precondition: envelope must sniff unknown")
	}
	if err := denyPreToolUse(cmd, raw, "no engaged story"); err == nil {
		t.Fatal("denyPreToolUse must return error")
	}
	var doc map[string]any
	if jerr := json.Unmarshal(buf.Bytes(), &doc); jerr != nil {
		t.Fatalf("json: %v body=%s", jerr, buf.String())
	}
	if hso, _ := doc["hookSpecificOutput"].(map[string]any); hso["permissionDecision"] != "deny" {
		t.Fatalf("unknown harness deny not strict: %s", buf.String())
	}
}

// TestDenyPreToolUseRespectsHarnessFlag: --harness grok forces Grok's top-level
// envelope even when the event would sniff as claude (snake_case tool_input).
func TestDenyPreToolUseRespectsHarnessFlag(t *testing.T) {
	prev := hookHarnessFlag
	hookHarnessFlag = "grok"
	t.Cleanup(func() { hookHarnessFlag = prev })

	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	raw := []byte(`{"tool_input":{"file_path":"/tmp/x.go"}}`)
	if err := denyPreToolUse(cmd, raw, "no engaged story"); err == nil {
		t.Fatal("denyPreToolUse must return error")
	}
	var doc map[string]any
	if jerr := json.Unmarshal(buf.Bytes(), &doc); jerr != nil {
		t.Fatalf("json: %v body=%s", jerr, buf.String())
	}
	if doc["decision"] != "deny" {
		t.Fatalf("got %v", doc)
	}
}

// keysOf returns sorted map keys for stable test diagnostics.
func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestResolveAbsTarget pins the relative-target fix (sty_8c3d345c): a repo-relative
// edit path (as Grok sends) resolves against the REPO ROOT, not against whatever
// narrower root a later containment test uses. This is what stops a relative
// "internal/x.go" from nesting under a tested ".satelle" root and looking exempt.
func TestResolveAbsTarget(t *testing.T) {
	const repo = "/home/u/repo"
	cases := []struct {
		target string
		want   string
	}{
		{"internal/config/sync.go", "/home/u/repo/internal/config/sync.go"}, // relative → under repo root
		{"/home/u/repo/internal/x.go", "/home/u/repo/internal/x.go"},        // absolute → unchanged (cleaned)
		{"/home/u/other/x.go", "/home/u/other/x.go"},                        // absolute outside → unchanged
		{"", ""}, // blank passes through
	}
	for _, c := range cases {
		if got := resolveAbsTarget(repo, c.target); got != c.want {
			t.Errorf("resolveAbsTarget(%q, %q) = %q, want %q", repo, c.target, got, c.want)
		}
	}
	// The crux: a relative code path, once resolved against the repo root, is NOT
	// classed as inside the data dir — the exact mis-classification that let Grok
	// (relative-path) edits bypass the gate before the fix.
	dataDir := "/home/u/repo/.satelle"
	if editExempt([]string{dataDir}, repo, resolveAbsTarget(repo, "internal/config/sync.go")) {
		t.Error("a relative product-code path resolved to inside the data dir (the pre-fix bypass)")
	}
	if !editExempt([]string{dataDir}, repo, resolveAbsTarget(repo, ".satelle/skills/plan.md")) {
		t.Error("a relative substrate path under an exempt prefix should classify exempt")
	}
}

func TestReadOnlyPreflightReasonDerivesFromConfig(t *testing.T) {
	withTmp := config.Config{Gate: config.GateConfig{
		EditExemptPaths: []string{"/tmp/"},
		EditExemptGlobs: []string{"sty_*_body.md", "sty_*_ac.md"},
	}}
	got := readOnlyPreflightReasonFrom(func() (config.Config, string, error) {
		return withTmp, "/home/u/repo/.satelle/satelle.toml", nil
	})
	for _, want := range []string{"/tmp/", "sty_*_body.md", "sty_*_ac.md"} {
		if !strings.Contains(got, want) {
			t.Errorf("configured clause missing %q: %s", want, got)
		}
	}
	empty := config.Config{Gate: config.GateConfig{
		EditExemptPaths: []string{},
		EditExemptGlobs: []string{},
	}}
	got = readOnlyPreflightReasonFrom(func() (config.Config, string, error) {
		return empty, "/home/u/repo/.satelle/satelle.toml", nil
	})
	if strings.Contains(got, "sty_*_body.md") {
		t.Errorf("empty authored exemptions must not promise globs: %s", got)
	}
	// Process temp is a mechanism drafting location even when the authored
	// list is empty (sty_e33f78fe) — in-repo paths stay gated.
	namedTemp := false
	for _, p := range tempDraftRoots() {
		if strings.Contains(got, p) {
			namedTemp = true
			break
		}
	}
	if !namedTemp {
		t.Errorf("empty authored exemptions should still name process temp: %s", got)
	}
}

// TestTempDraftTarget is the pure classifier for process-temp writes
// (sty_e33f78fe): out-of-tree temp drafts allow; in-repo product — including
// a repo that lives under the temp dir — does not.
func TestTempDraftTarget(t *testing.T) {
	tmp := os.TempDir()
	outside := "/home/u/repo"
	if withinRoot(tmp, outside) {
		outside = "/var/satelle-test-repo"
	}
	if !tempDraftTarget(outside, filepath.Join(tmp, "anything.txt")) {
		t.Errorf("temp draft under os.TempDir must allow for a repo outside the temp dir")
	}
	// A short-layout dispatch scratch dir under /tmp (sty_b666bc6e).
	if !tempDraftTarget(outside, "/tmp/satelle/0123456789ab/sty_0123abcd/89abcdef/note.md") {
		t.Error("write inside a dispatch scratch dir must stay a temp draft")
	}
	if !tempDraftTarget(outside, "/tmp/scratch-notes.md") {
		t.Error("/tmp draft must allow even when TMPDIR differs")
	}
	if tempDraftTarget(outside, filepath.Join(outside, "internal", "cli", "cmd_hook.go")) {
		t.Error("in-repo product must not be a temp draft")
	}
	tmpRepo := filepath.Join(tmp, "TestTempDraftRepo", "repo")
	if tempDraftTarget(tmpRepo, filepath.Join(tmpRepo, "internal", "cli", "cmd_hook.go")) {
		t.Error("in-tree product of a temp-dir repo must not be a temp draft")
	}
	if tempDraftTarget(outside, "/tmpfoo/x.md") {
		t.Error("/tmpfoo is not under /tmp")
	}
}

// TestEditExemptClassification proves exemption is CONFIG-only (sty_8c3d345c):
// a path under a configured exempt prefix is exempt, code outside every prefix is
// gated, no prefixes means nothing is exempt (no hardcoded data-dir leg), and a
// blank prefix must NOT silently exempt everything. Targets are absolute, as
// callers pass them (resolveAbsTarget runs first).
func TestEditExemptClassification(t *testing.T) {
	const repo = "/home/u/repo"
	exemptRoots := []string{"/home/u/repo/.satelle", "/home/u/repo/.claude"}
	tmpRoots := append(append([]string{}, exemptRoots...), "/tmp/")
	tmpRepo := "/tmp/TestRepo/repo"
	cases := []struct {
		name        string
		exemptRoots []string
		repoRoot    string
		target      string
		want        bool
	}{
		{"configured data dir exempt", exemptRoots, repo, "/home/u/repo/.satelle/skills/x.md", true},
		{"configured harness dir exempt", exemptRoots, repo, "/home/u/repo/.claude/skills/foo/SKILL.md", true},
		{"in-repo code stays gated", exemptRoots, repo, "/home/u/repo/internal/cli/app.go", false},
		{"no prefixes: data dir NOT exempt", nil, repo, "/home/u/repo/.satelle/skills/x.md", false},
		{"no prefixes: code not exempt", nil, repo, "/home/u/repo/internal/cli/app.go", false},
		{"blank prefix does not exempt everything", []string{"  "}, repo, "/home/u/repo/internal/cli/app.go", false},
		{"out-of-tree /tmp draft exempt", tmpRoots, repo, "/tmp/itest-epic-buildout/c1-title.txt", true},
		{"out-of-tree /tmp/x.md exempt", tmpRoots, repo, "/tmp/x.md", true},
		{"/tmpfoo is not under /tmp/", tmpRoots, repo, "/tmpfoo/x.md", false},
		{"/tmp/ prefix does not exempt product in a /tmp repo", append([]string{"/tmp/"}, tmpRepo+"/.satelle"), tmpRepo, tmpRepo + "/internal/cli/cmd_hook.go", false},
		{"no /tmp/ config: out-of-tree /tmp denied", exemptRoots, repo, "/tmp/x.md", false},
	}
	for _, c := range cases {
		if got := editExempt(c.exemptRoots, c.repoRoot, c.target); got != c.want {
			t.Errorf("%s: editExempt(%q) = %v, want %v", c.name, c.target, got, c.want)
		}
	}
}

// TestEditExemptPrefixUnchangedWhenGlobsPresent (sty_fefc88cd AC2): editExempt
// is prefix-only. A populated glob list is a different classifier — it must
// not change prefix results, including a dump name that the glob matcher
// would allow.
func TestEditExemptPrefixUnchangedWhenGlobsPresent(t *testing.T) {
	roots := []string{"/home/u/repo/.satelle"}
	if editExempt(roots, "/home/u/repo", "/home/u/repo/internal/cli/app.go") {
		t.Fatal("product path must stay prefix-gated")
	}
	if editExempt(roots, "/home/u/repo", "/home/u/repo/sty_abc_body.md") {
		t.Fatal("dump basename is not a prefix exemption")
	}
}

// TestEditExemptPatternClassification is the pure matcher table for
// edit_exempt_globs (sty_fefc88cd AC2/AC5): basename match, repo-relative
// when the pattern contains /, blank/bad patterns skipped conservatively.
func TestEditExemptPatternClassification(t *testing.T) {
	const root = "/home/u/repo"
	cases := []struct {
		name     string
		patterns []string
		target   string
		want     bool
	}{
		{"match root", []string{"sty_*_body.md"}, root + "/sty_abc_body.md", true},
		{"match subdir basename", []string{"sty_*_ac.md"}, root + "/notes/sty_abc_ac.md", true},
		{"non-match product", []string{"sty_*_body.md"}, root + "/internal/cli/cmd_hook.go", false},
		{"non-match near miss", []string{"sty_*_body.md"}, root + "/sty_abc_body.go", false},
		{"blank does not exempt everything", []string{"", "sty_*_ac.md"}, root + "/main.go", false},
		{"bad pattern skipped", []string{"sty_[_body.md"}, root + "/sty_x_body.md", false},
		{"path-form match", []string{"docs/sty_*_body.md"}, root + "/docs/sty_a_body.md", true},
		{"path-form does not match other dir", []string{"docs/sty_*_body.md"}, root + "/sty_a_body.md", false},
		{"path-form outside root skipped", []string{"docs/*.md"}, "/tmp/docs/x.md", false},
		{"nil patterns no-op", nil, root + "/sty_abc_body.md", false},
	}
	for _, c := range cases {
		if got := editExemptPattern(c.patterns, root, c.target); got != c.want {
			t.Errorf("%s: editExemptPattern(%v, %q) = %v, want %v", c.name, c.patterns, c.target, got, c.want)
		}
	}
}

// Note: TestExecutorStatesDOT, TestExecutorStatesNamedAgent, and TestExecutorStatesCoderNodeEngaged
// have been removed. The hook now uses wfdot.Spec.NonTerminalEngagingStates() which reads
// shape markers from the DOT. See tests in the wfdot package for shape-based engagement logic.

// routeWFs builds the two halves of a derived route — the only representation a
// lifecycle has (sty_d953c5d8). A category-specific lane is a `[<category>]`
// TABLE in done.toml rather than a second workflow file, and frontmatter is the
// `[meta]` table the TOML form uses (sty_81bb0dde).
func routeWFs(done, step string) []docindex.Doc {
	mk := func(name, what, body string) docindex.Doc {
		return docindex.Doc{Kind: "workflows", Name: name,
			Body: "[meta]\nname = \"" + name + "\"\ntype = \"workflow\"\nscope = \"project\"\ndescription = \"" +
				what + "\"\n\n" + body}
	}
	return []docindex.Doc{
		mk("done", "fixture declaration of done", done),
		mk("step", "fixture step catalogue", step),
	}
}

func TestAnyEngagedCountsTasks(t *testing.T) {
	// One wildcard workflow governs both the story and the task; commit_push is a
	// performing named-agent node.
	wfs := routeWFs(
		`["*"]
obligations = ["raised", "coded", "pushed", "closed"]

# The shipped task category stays in force under a repo file (sty_a4603ea2), so
# the task rides the same lane explicitly.
[task]
obligations = ["raised", "coded", "pushed", "closed"]
`,
		`[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
agent = "executor"
requires = ["raised"]

[pushed]
status = "commit_push"
agent = "commit-agent"
skills = ["commit-push"]
requires = ["coded"]

[closed]
status = "done"
terminal = true
requires = ["pushed"]
`)
	// A task in a performing state counts as engaged, exactly like a story.
	engaged, err := anyEngaged([]workitem.Item{
		{Kind: workitem.KindTask, Status: "commit_push"},
		{Kind: workitem.KindStory, Status: "backlog"},
	}, wfs)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !engaged {
		t.Error("a task in a performing state should count as engaged")
	}
	// Nothing engaged when no item is in a performing state.
	engaged, err = anyEngaged([]workitem.Item{
		{Kind: workitem.KindTask, Status: "backlog"},
		{Kind: workitem.KindStory, Status: "done"},
	}, wfs)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if engaged {
		t.Error("no item in a performing state should not count as engaged")
	}
}

// TestAnyEngagedPerStoryWorkflow: engagement is judged against the story's OWN
// governing workflow, not one global "primary" workflow (sty_f5bd176f). A feature
// story in a feature-workflow's performing `plan` state is engaged even though the
// wildcard workflow has no `plan` node — the exact gap that blocked a dispatched
// coder reached from `plan`.
func TestAnyEngagedPerStoryWorkflow(t *testing.T) {
	wfs := routeWFs(
		`["*"]
obligations = ["raised", "coded", "closed"]

[feature]
obligations = ["raised", "planned", "feat-coded", "feat-closed"]
`,
		`[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
agent = "executor"
requires = ["raised"]

[planned]
status = "plan"
agent = "executor"
requires = ["raised"]

[feat-coded]
status = "in_progress"
agent = "coder"
skills = ["code"]
requires = ["planned"]

[closed]
status = "done"
terminal = true
requires = ["coded"]

[feat-closed]
status = "done"
terminal = true
requires = ["feat-coded"]
`)
	// A feature story in plan is engaged via the feature workflow (plan performs).
	engaged, err := anyEngaged([]workitem.Item{{Kind: workitem.KindStory, Category: "feature", Status: "plan"}}, wfs)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !engaged {
		t.Error("a feature story in its workflow's performing plan state should be engaged")
	}
	// The same status under the wildcard workflow (no plan node) is NOT engaged —
	// engagement follows the governing workflow, not a global state list.
	engaged, err = anyEngaged([]workitem.Item{{Kind: workitem.KindStory, Category: "chore", Status: "plan"}}, wfs)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if engaged {
		t.Error("a chore story in plan is governed by the wildcard workflow (no plan state) — not engaged")
	}
}

// TestAnyEngagedFailClosedNoWorkflow: anyEngaged returns (false, error) — NOT
// (false, nil) — when an item has NO resolving workflow (AC3 fail-closed). With
// no workflow docs at all nothing governs the item, so the hook blocks the edit
// rather than silently allowing it. (A repo route file overlays the shipped route
// by name, so a partial file can no longer leave a category unresolved.)
func TestAnyEngagedFailClosedNoWorkflow(t *testing.T) {
	var wfs []docindex.Doc
	engaged, err := anyEngaged([]workitem.Item{{Kind: workitem.KindStory, Category: "chore", Status: "in_progress"}}, wfs)
	if engaged {
		t.Error("a chore item with no resolving workflow must not be engaged")
	}
	if err == nil {
		t.Fatal("anyEngaged must return an error when an item has no resolving workflow (fail-closed)")
	}
}

// TestAnyEngagedFailClosedNoDOT: anyEngaged returns (false, error) when the
// governing workflow body carries no DOT block (AC3 fail-closed) — a non-DOT
// workflow cannot be parsed for shape markers, so the hook blocks rather than guess.
func TestAnyEngagedFailClosedNoDOT(t *testing.T) {
	// A wildcard workflow (governs any item) whose body has no ```dot block.
	noDotBody := "---\nname: wild\napplies_to: [\"*\"]\n---\n# wild\n\nno dot block here\n"
	wfs := []docindex.Doc{{Kind: "workflows", Name: "wild", Body: noDotBody}}
	engaged, err := anyEngaged([]workitem.Item{{Kind: workitem.KindStory, Status: "in_progress"}}, wfs)
	if engaged {
		t.Error("an item governed by a non-DOT workflow must not be engaged")
	}
	if err == nil {
		t.Fatal("anyEngaged must return an error when the governing workflow has no DOT (fail-closed)")
	}
}

// --- Reinforcement hooks: gate-liveness self-check + Stop post-hoc detector
// (sty_949e8739) -------------------------------------------------------------

// TestSettingsWiresGate: a settings JSON counts as wiring the edit gate only when
// a PreToolUse Edit-matcher hook invokes `satelle hook gate`.
func TestSettingsWiresGate(t *testing.T) {
	wired := `{"hooks":{"PreToolUse":[{"matcher":"Edit|Write","hooks":[{"type":"command","command":"sh -c '#satelle-failvisible\nb hook gate'"}]}]}}`
	if !settingsWiresGate([]byte(wired)) {
		t.Errorf("wired settings should report gate wired")
	}
	// sty_adfb9862 script-file form (no "hook gate" substring in the command).
	script := `{"hooks":{"PreToolUse":[{"matcher":"Edit|Write","hooks":[{"type":"command","command":"sh .satelle/hooks/satelle-hook.sh gate claude"}]}]}}`
	if !settingsWiresGate([]byte(script)) {
		t.Errorf("script-file PreToolUse gate must count as wired")
	}
	// Bash-only gate (commitgate) but no Edit-matcher gate → not wired.
	noEdit := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"satelle hook commitgate || exit 2"}]}]}}`
	if settingsWiresGate([]byte(noEdit)) {
		t.Errorf("a Bash-only commitgate must NOT count as the edit gate")
	}
	// Edit matcher but a different command → not wired.
	wrongCmd := `{"hooks":{"PreToolUse":[{"matcher":"Edit","hooks":[{"type":"command","command":"echo hi"}]}]}}`
	if settingsWiresGate([]byte(wrongCmd)) {
		t.Errorf("an Edit matcher without `satelle hook gate` must NOT count")
	}
	if settingsWiresGate([]byte("{ not json")) {
		t.Errorf("malformed settings must not report a confident wire")
	}
}

// TestGateWiredInSettings: checked=false when no settings file exists (fail open);
// checked=true+wired reflects the committed files.
func TestGateWiredInSettings(t *testing.T) {
	empty := t.TempDir()
	if wired, checked := gateWiredInSettings(empty); wired || checked {
		t.Errorf("no settings file → want (false,false), got (%v,%v)", wired, checked)
	}

	wiredRepo := t.TempDir()
	mkfile(t, filepath.Join(wiredRepo, ".claude", "settings.json"),
		`{"hooks":{"PreToolUse":[{"matcher":"Edit|Write|MultiEdit|NotebookEdit","hooks":[{"type":"command","command":"sh -c '#satelle-failvisible b hook gate'"}]}]}}`)
	if wired, checked := gateWiredInSettings(wiredRepo); !wired || !checked {
		t.Errorf("wired .claude → want (true,true), got (%v,%v)", wired, checked)
	}

	// A settings file present but WITHOUT the edit gate → confident missing wire.
	unwired := t.TempDir()
	mkfile(t, filepath.Join(unwired, ".claude", "settings.json"),
		`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"satelle reindex"}]}]}}`)
	if wired, checked := gateWiredInSettings(unwired); wired || !checked {
		t.Errorf("present-but-unwired → want (false,true), got (%v,%v)", wired, checked)
	}
}

// TestStopHookActive honours both snake_case (Claude) and camelCase (Grok) forms.
func TestStopHookActive(t *testing.T) {
	if !stopHookActive([]byte(`{"stop_hook_active":true}`)) {
		t.Errorf("snake_case stop_hook_active=true not detected")
	}
	if !stopHookActive([]byte(`{"stopHookActive":true}`)) {
		t.Errorf("camelCase stopHookActive=true not detected")
	}
	if stopHookActive([]byte(`{"stop_hook_active":false}`)) {
		t.Errorf("false must not report active")
	}
	if stopHookActive([]byte(`{}`)) {
		t.Errorf("absent flag must not report active")
	}
}

// TestEmitStopBlock: the Stop block payload carries decision=block + the reason.
func TestEmitStopBlock(t *testing.T) {
	var buf bytes.Buffer
	if err := emitStopBlock(&buf, "ungated edits: a.go"); err != nil {
		t.Fatalf("emitStopBlock: %v", err)
	}
	var got stopBlockOut
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("stop block not valid JSON: %v (%s)", err, buf.String())
	}
	if got.Decision != "block" || !strings.Contains(got.Reason, "a.go") {
		t.Errorf("stop block = %+v, want decision=block + reason naming the file", got)
	}
}

// TestStopcheckHarnessDoesNotLeak: a cursor stopcheck run in this process
// leaves no harness behind, so a later Stop block is encoded for the default
// harness (the strict shuffled run caught TestEmitStopBlock inheriting cursor).
func TestStopcheckHarnessDoesNotLeak(t *testing.T) {
	t.Setenv("SATELLE_DISPATCH_AGENT", "leak-test") // skip cursor usage recording
	var sink bytes.Buffer
	_ = runHookStopcheck([]byte(`{"stop_hook_active":true,"hook_event_name":"stop","cursor_version":"2026.10.01-e373342","loop_count":0}`), &sink)
	if stopEmitHarness != "" {
		t.Fatalf("stopEmitHarness = %q after stopcheck, want empty", stopEmitHarness)
	}
	var buf bytes.Buffer
	if err := emitStopBlock(&buf, "ungated edits: a.go"); err != nil {
		t.Fatal(err)
	}
	var got stopBlockOut
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil || got.Decision != "block" {
		t.Errorf("stop block after a cursor stopcheck = %s, want decision=block", buf.String())
	}
}

// TestStopBlockShape (sty_5e4bc568 AC6): Stop schema honors top-level
// decision=block + reason (NOT PreToolUse hookSpecificOutput). Closed-key check.
func TestStopBlockShape(t *testing.T) {
	var buf bytes.Buffer
	if err := emitStopBlock(&buf, "ungated edits: a.go"); err != nil {
		t.Fatalf("emitStopBlock: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(buf.Bytes(), &root); err != nil {
		t.Fatalf("stop block not valid JSON: %v (%s)", err, buf.String())
	}
	if len(root) != 2 {
		t.Errorf("stop block root keys = %v, want {decision,reason}", keysOf(root))
	}
	if root["decision"] != "block" {
		t.Errorf("decision = %v, want block", root["decision"])
	}
	if reason, _ := root["reason"].(string); !strings.Contains(reason, "a.go") {
		t.Errorf("reason should name the file: %v", root["reason"])
	}
	if _, ok := root["hookSpecificOutput"]; ok {
		t.Errorf("Stop block must not use PreToolUse hookSpecificOutput shape")
	}
}

// TestStopcheckReasonCaps: a large dirty set is capped with a "+N more" suffix so
// the reason cannot flood the model.
func TestStopcheckReasonCaps(t *testing.T) {
	var many []string
	for i := 0; i < 25; i++ {
		many = append(many, "f")
	}
	r := stopcheckReason(many)
	if !strings.Contains(r, "+15 more") {
		t.Errorf("expected a +15 more cap suffix, got: %s", r)
	}
	if !strings.Contains(r, "NO story is engaged") {
		t.Errorf("stopcheck reason should state no story is engaged: %s", r)
	}
}

// TestRunHookPromptEmitsReminder: every prompt re-injects the concise rule via a
// UserPromptSubmit additionalContext payload.
func TestRunHookPromptEmitsReminder(t *testing.T) {
	// Prompt self-check may load global/config paths via GlobalDir (sty_c36c211f).
	t.Setenv("SATELLE_HOME", t.TempDir())
	var buf bytes.Buffer
	if err := runHookPrompt(&buf); err != nil {
		t.Fatalf("runHookPrompt: %v", err)
	}
	var got agentcli.HookContextOut
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("prompt output not valid JSON: %v (%s)", err, buf.String())
	}
	if got.HookSpecificOutput.HookEventName != "UserPromptSubmit" {
		t.Errorf("event = %q, want UserPromptSubmit", got.HookSpecificOutput.HookEventName)
	}
	if !strings.Contains(got.HookSpecificOutput.AdditionalContext, "edits require an ENGAGED story") {
		t.Errorf("prompt reminder missing the engaged-story rule: %s", got.HookSpecificOutput.AdditionalContext)
	}
}

func mkfile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestEvaluateSeatPerformingAndStale: the pure engagement predicate
// (sty_1738f973 AC2/AC5/AC6).
func TestEvaluateSeatPerformingAndStale(t *testing.T) {
	wfs := routeWFs(
		`["*"]
obligations = ["raised", "planned", "coded", "closed"]
`,
		`[raised]
status = "backlog"
start = true

[planned]
status = "plan"
agent = "executor"
requires = ["raised"]

[coded]
status = "in_progress"
agent = "executor"
requires = ["planned"]

[closed]
status = "done"
terminal = true
requires = ["coded"]
`)
	now := time.Now().UTC()
	items := []workitem.Item{
		{ID: "sty_live", Kind: workitem.KindStory, Status: "in_progress", Category: "feature"},
		{ID: "sty_backlog", Kind: workitem.KindStory, Status: "backlog", Category: "feature"},
		{ID: "sty_done", Kind: workitem.KindStory, Status: "done", Category: "feature"},
	}

	// Settled live lease on in_progress → engaged.
	live, other, err := evaluateFirstSeat([]lease.Lease{{
		ItemID: "sty_live", State: "in_progress", Owner: "alice",
		AcquiredAt: now.Add(-time.Hour), HeartbeatAt: now.Add(-time.Minute),
	}}, items, wfs, now)
	if err != nil {
		t.Fatal(err)
	}
	if live.ItemID != "sty_live" || !live.Engaged {
		t.Fatalf("live seat: %+v", live)
	}
	if other.ItemID != "" {
		t.Fatalf("unexpected other: %+v", other)
	}

	// Settled lease on done → not engaged.
	live, other, err = evaluateFirstSeat([]lease.Lease{{
		ItemID: "sty_done", State: "done", Owner: "alice",
		AcquiredAt: now.Add(-time.Hour), HeartbeatAt: now.Add(-time.Minute),
	}}, items, wfs, now)
	if err != nil {
		t.Fatal(err)
	}
	if live.ItemID != "" {
		t.Fatalf("done lease must not be live: %+v", live)
	}
	if other.ItemID != "sty_done" {
		t.Fatalf("other should name residue: %+v", other)
	}

	// Stale orphan (in_flight, committed backlog, heartbeat past TTL) → not engaged.
	staleHB := now.Add(-lease.HeartbeatTTL - time.Minute)
	live, other, err = evaluateFirstSeat([]lease.Lease{{
		ItemID: "sty_backlog", State: "plan", Owner: "dead", InFlight: true,
		AcquiredAt: staleHB, HeartbeatAt: staleHB,
	}}, items, wfs, now)
	if err != nil {
		t.Fatal(err)
	}
	if live.ItemID != "" {
		t.Fatalf("stale orphan must not be live: %+v", live)
	}
	if other.ItemID != "sty_backlog" || !other.Stale {
		t.Fatalf("other must name stale orphan: %+v", other)
	}
	// formatSeat / seatSuffix name the holder + inspect path.
	desc := formatSeat(other, now)
	if !strings.Contains(desc, "sty_backlog") || !strings.Contains(desc, "stale") {
		t.Fatalf("formatSeat = %q", desc)
	}
	suf := seatSuffix(other, now)
	if !strings.Contains(suf, "satelle story seat") || !strings.Contains(suf, "seat release sty_backlog") {
		t.Fatalf("seatSuffix = %q", suf)
	}

	// Fresh in-flight at start state (backlog) → engaged (acquire-at-start window).
	// Must set InFlightAt + live InFlightPid so EffectiveInFlight is true
	// (sty_bf797fa9: raw InFlight alone is not enough).
	live, _, err = evaluateFirstSeat([]lease.Lease{{
		ItemID: "sty_backlog", State: "plan", Owner: "alice", InFlight: true,
		InFlightAt: now.Add(-time.Second), InFlightPid: os.Getpid(),
		AcquiredAt: now.Add(-time.Minute), HeartbeatAt: now.Add(-time.Second),
	}}, items, wfs, now)
	if err != nil {
		t.Fatal(err)
	}
	if live.ItemID != "sty_backlog" || !live.Engaged || !live.InFlight {
		t.Fatalf("fresh in-flight at start state must be live: %+v", live)
	}

	// Dead transitioning pid → not in-flight for gate purposes (sty_bf797fa9 AC3).
	// Heartbeat is still fresh (hook-refresh shape); InFlightAt is young — only
	// the dead pid makes EffectiveInFlight false.
	deadPID := 1<<30 - 1
	live, other, err = evaluateFirstSeat([]lease.Lease{{
		ItemID: "sty_backlog", State: "plan", Owner: "alice", InFlight: true,
		InFlightAt: now, InFlightPid: deadPID,
		AcquiredAt: now.Add(-time.Minute), HeartbeatAt: now,
	}}, items, wfs, now)
	if err != nil {
		t.Fatal(err)
	}
	if live.ItemID != "" {
		t.Fatalf("dead InFlightPid must not keep seat live mid-transition: %+v", live)
	}
	if other.ItemID != "sty_backlog" || other.InFlight {
		t.Fatalf("other must name residue with InFlight=false: %+v", other)
	}
	// Edit gate: dead-pid residue on an edit-capable state must permit the owner.
	// Plant a settled-shaped lease on plan (edit-capable in this fixture) that
	// only looked in-flight via a dead pid — EffectiveInFlight clears it so
	// Engaged falls through to performing-status check.
	live, _, err = evaluateFirstSeat([]lease.Lease{{
		ItemID: "sty_plan", State: "plan", Owner: "alice", InFlight: true,
		InFlightAt: now, InFlightPid: deadPID,
		AcquiredAt: now.Add(-time.Minute), HeartbeatAt: now,
	}}, []workitem.Item{{ID: "sty_plan", Status: "plan", Kind: workitem.KindStory}}, wfs, now)
	if err != nil {
		t.Fatal(err)
	}
	// status=plan is performing → live seat; InFlight effective false → editPermitted.
	if live.ItemID != "sty_plan" || !live.Engaged {
		t.Fatalf("performing status with dead-pid in_flight residue must still engage: %+v", live)
	}
	if live.InFlight {
		t.Fatalf("dead InFlightPid must surface InFlight=false on seatInfo: %+v", live)
	}
	if !editPermitted(live, dispatchMarker{}) {
		t.Fatalf("edit must be permitted when effective in_flight is false: %+v", live)
	}
}

// TestSeatInjectHelpers: SessionStart / prompt seat inject pure paths
// (sty_1738f973 AC6). Removing appendSeatToContext / appendSeatToPrompt /
// formatSeatBlock would fail these — the discoverability path the AC names.
func TestSeatInjectHelpers(t *testing.T) {
	now := time.Now().UTC()
	stale := seatInfo{
		ItemID: "sty_2db78343", State: "plan", Owner: "dead",
		AcquiredAt: now.Add(-40 * time.Minute), HeartbeatAt: now.Add(-40 * time.Minute),
		Stale: true,
	}
	block := formatSeatBlock(stale, now, config.ParallelNone)
	if !strings.Contains(block, "## Engagement seat") {
		t.Fatalf("block missing heading: %q", block)
	}
	if !strings.Contains(block, "sty_2db78343") || !strings.Contains(block, "stale") {
		t.Fatalf("block missing seat id/stale: %q", block)
	}
	if !strings.Contains(block, "satelle story seat release sty_2db78343") {
		t.Fatalf("block missing release path: %q", block)
	}

	merged := appendSeatToContext("principles here", block, 16384)
	if !strings.Contains(merged, "## Engagement seat") || !strings.HasPrefix(merged, "principles here") {
		t.Fatalf("appendSeatToContext = %q", merged)
	}
	// Ceiling too tight: keep content, drop seat.
	tight := appendSeatToContext("already long content that fills budget", block, 40)
	if strings.Contains(tight, "## Engagement seat") {
		t.Fatalf("ceiling should drop seat: %q", tight)
	}
	// Empty content still injects seat.
	only := appendSeatToContext("", block, 16384)
	if only != block {
		t.Fatalf("empty content: %q", only)
	}

	prompt := appendSeatToPrompt("edits require an ENGAGED story", stale, now)
	if !strings.Contains(prompt, "sty_2db78343") || !strings.Contains(prompt, "stale") {
		t.Fatalf("prompt seat line: %q", prompt)
	}
	if !strings.Contains(prompt, "satelle story seat") {
		t.Fatalf("prompt missing inspect: %q", prompt)
	}
	// Empty seatInfo is a no-op.
	if got := appendSeatToPrompt("base", seatInfo{}, now); got != "base" {
		t.Fatalf("empty info: %q", got)
	}

	// Live seat (no STALE) still injects owner+ages.
	live := seatInfo{
		ItemID: "sty_live", State: "in_progress", Owner: "alice",
		AcquiredAt: now.Add(-10 * time.Minute), HeartbeatAt: now.Add(-time.Minute),
		Engaged: true,
	}
	liveBlock := formatSeatBlock(live, now, config.ParallelNone)
	if strings.Contains(liveBlock, "STALE") || strings.Contains(strings.ToLower(liveBlock), "stale ") {
		// formatSeat uses "stale Nm" only when Stale=true; live must not.
		if strings.Contains(liveBlock, ", stale ") {
			t.Fatalf("live seat must not say stale: %q", liveBlock)
		}
	}
	if !strings.Contains(liveBlock, "alice") || !strings.Contains(liveBlock, "sty_live") {
		t.Fatalf("live block: %q", liveBlock)
	}
}

// TestSeatModeInject: the seat inject names the ACTIVE concurrency mode, and under
// a non-default mode says what a second engagement requires. An agent that only
// meets the mode in a refusal has already lost a turn to it (sty_050f3a19 AC3).
func TestSeatModeInject(t *testing.T) {
	// Default mode: one short factual line, no preconditions to state.
	none := seatModeLine(config.ParallelNone)
	if !strings.Contains(none, "none") || !strings.Contains(none, "one performing story at a time") {
		t.Errorf("none mode line: %q", none)
	}
	// Unset resolves to the default rather than an empty line.
	if seatModeLine("") != none {
		t.Errorf("unset mode must render as none: %q", seatModeLine(""))
	}
	// Epic states BOTH preconditions — same parent, distinct working tree.
	epic := seatModeLine(config.ParallelEpic)
	for _, want := range []string{"epic", "parent", "working tree"} {
		if !strings.Contains(epic, want) {
			t.Errorf("epic mode line missing %q: %q", want, epic)
		}
	}
	// The mode rides the seat block, so a held seat carries it.
	now := time.Now().UTC()
	held := seatInfo{ItemID: "sty_held", State: "in_progress", Owner: "alice",
		AcquiredAt: now.Add(-time.Minute), HeartbeatAt: now, Engaged: true}
	if b := formatSeatBlock(held, now, config.ParallelEpic); !strings.Contains(b, "working tree") {
		t.Errorf("held-seat block must carry the mode: %q", b)
	}
}

func TestDroppedSeatEditReason(t *testing.T) {
	got := droppedSeatEditReason("sty_abc", "plan")
	for _, want := range []string{"sty_abc", "plan", "engagement seat was dropped", "story set sty_abc --status plan"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "Open a story session") {
		t.Error("dropped-seat reason must not use generic create-story message")
	}
}

func TestEditGateDenyReasonPrefersDroppedSeat(t *testing.T) {
	// Pure string path when firstDroppedPerformingSeat finds nothing uses generic.
	// We cannot open real store without isolation; unit-test droppedSeatEditReason
	// content was covered. Here: editGateDenyReason with empty info and no store
	// home falls through to noEngagedStoryEditReason.
	t.Setenv("SATELLE_HOME", t.TempDir())
	got := editGateDenyReason(seatInfo{}, nil, "", time.Now().UTC())
	if !strings.Contains(got, "without a performing story") {
		t.Fatalf("empty store should use generic no-story reason: %q", got)
	}
	// droppedSeatEditReason must differ from generic.
	drop := droppedSeatEditReason("sty_x", "in_progress")
	if strings.Contains(drop, "without a performing story") {
		t.Fatal("dropped reason must not use generic phrase")
	}
	if !strings.Contains(drop, "seat was dropped") {
		t.Fatalf("want dropped: %q", drop)
	}
	if drop == noEngagedStoryEditReason {
		t.Fatal("dropped and generic must differ")
	}
}

// --- sty_e16a2cd7: per-turn engaged prompt injection ---

// TestPromptNoSeatIsUnchanged: no live seat → body is byte-identical to today's
// static reminder (AC1). Gate-not-wired warning path also unchanged.
func TestPromptNoSeatIsUnchanged(t *testing.T) {
	t.Setenv("SATELLE_HOME", t.TempDir())
	var buf bytes.Buffer
	if err := runHookPrompt(&buf); err != nil {
		t.Fatalf("runHookPrompt: %v", err)
	}
	var got agentcli.HookContextOut
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("prompt output not valid JSON: %v (%s)", err, buf.String())
	}
	if got.HookSpecificOutput.AdditionalContext != hookPromptReminder {
		t.Errorf("no-seat body not byte-identical to hookPromptReminder:\n got %q\nwant %q",
			got.HookSpecificOutput.AdditionalContext, hookPromptReminder)
	}
}

// TestFormatEngagedPromptNamesNextGate: AC2 — id, status, gates, story set; no static reminder.
func TestFormatEngagedPromptNamesNextGate(t *testing.T) {
	now := time.Now().UTC()
	info := seatInfo{
		ItemID:      "sty_live",
		State:       "in_progress",
		HeartbeatAt: now.Add(-6 * time.Second),
		Engaged:     true,
		Advance: []wfdot.Advance{
			{To: "integration", Gates: []string{"satelle-code-ac-review", "satelle-story-scope-review"}},
		},
	}
	out := formatEngagedPrompt(info, now)
	if out == "" {
		t.Fatal("formatEngagedPrompt returned empty")
	}
	for _, want := range []string{
		"sty_live",
		"in_progress",
		"integration",
		"satelle-code-ac-review",
		"satelle story set sty_live --status integration",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("engaged form missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "edits require an ENGAGED story") {
		t.Errorf("static create-and-engage reminder must be absent:\n%s", out)
	}
	if len(out) > promptEngagedCeiling {
		t.Errorf("engaged form length %d > ceiling %d:\n%s", len(out), promptEngagedCeiling, out)
	}
}

// TestEngagedPromptQuotesOwnWorkflow: AC3 — two invented workflows; each injection
// quotes its own edges/gates and none of the other's. Invented names cannot be
// hardcoded in Go.
func TestEngagedPromptQuotesOwnWorkflow(t *testing.T) {
	wfs := routeWFs(
		`[cat-a]
obligations = ["started", "drafted", "worked", "shipped"]

[cat-b]
obligations = ["started", "triaged", "built", "verified"]
`,
		`[started]
status = "start"
start = true

[drafted]
status = "draft"
agent = "executor"
requires = ["started"]

[worked]
status = "work"
agent = "executor"
reviewers = ["wfa-work-gate"]
reviewer_agent = "reviewer"
requires = ["drafted"]

[shipped]
status = "ship"
terminal = true
requires = ["worked"]

[triaged]
status = "triage"
agent = "executor"
requires = ["started"]

[built]
status = "build"
agent = "executor"
reviewers = ["wfb-build-gate"]
reviewer_agent = "reviewer"
requires = ["triaged"]

[verified]
status = "verify"
terminal = true
requires = ["built"]
`)
	now := time.Now().UTC()
	items := []workitem.Item{
		{ID: "sty_a", Kind: workitem.KindStory, Status: "draft", Category: "cat-a"},
		{ID: "sty_b", Kind: workitem.KindStory, Status: "triage", Category: "cat-b"},
	}
	leases := []lease.Lease{
		{ItemID: "sty_a", State: "draft", Owner: "alice", AcquiredAt: now.Add(-time.Hour), HeartbeatAt: now.Add(-time.Minute)},
		{ItemID: "sty_b", State: "triage", Owner: "bob", AcquiredAt: now.Add(-time.Hour), HeartbeatAt: now.Add(-time.Minute)},
	}
	// evaluateSeat returns every live seat; probe each lease alone.
	for _, c := range []struct {
		lease    lease.Lease
		item     workitem.Item
		wantTo   string
		wantGate string
		notGate  string
		notState string
	}{
		{leases[0], items[0], "work", "wfa-work-gate", "wfb-build-gate", "build"},
		{leases[1], items[1], "build", "wfb-build-gate", "wfa-work-gate", "work"},
	} {
		live, _, err := evaluateFirstSeat([]lease.Lease{c.lease}, []workitem.Item{c.item}, wfs, now)
		if err != nil {
			t.Fatalf("%s: evaluateSeat: %v", c.item.ID, err)
		}
		if !live.Engaged || live.ItemID != c.item.ID {
			t.Fatalf("%s: live=%+v", c.item.ID, live)
		}
		if len(live.Advance) != 1 || live.Advance[0].To != c.wantTo {
			t.Fatalf("%s: Advance=%+v, want [{%s …}]", c.item.ID, live.Advance, c.wantTo)
		}
		out := formatEngagedPrompt(live, now)
		if !strings.Contains(out, c.wantGate) || !strings.Contains(out, c.wantTo) {
			t.Errorf("%s: engaged form missing own edge/gate:\n%s", c.item.ID, out)
		}
		if strings.Contains(out, c.notGate) || strings.Contains(out, c.notState) {
			t.Errorf("%s: engaged form leaked other workflow:\n%s", c.item.ID, out)
		}
		if strings.Contains(out, "edits require an ENGAGED story") {
			t.Errorf("%s: static reminder present:\n%s", c.item.ID, out)
		}
	}
}

// TestEngagedPromptWithinCeiling: AC4 — pathological DOT still ≤ ceiling or "".
func TestEngagedPromptWithinCeiling(t *testing.T) {
	now := time.Now().UTC()
	// Five long gate names + long id — force degradation ladder.
	longGates := make([]string, 5)
	for i := range longGates {
		longGates[i] = fmt.Sprintf("very-long-gate-name-number-%02d-xxxxxxxx", i)
	}
	info := seatInfo{
		ItemID:      "sty_pathologically_long_identifier_xx",
		State:       "in_progress_with_a_very_long_state_name",
		HeartbeatAt: now,
		Engaged:     true,
		Advance: []wfdot.Advance{
			{To: "next_state_also_quite_long_name_aa", Gates: longGates},
			{To: "next_state_also_quite_long_name_bb", Gates: longGates},
		},
	}
	out := formatEngagedPrompt(info, now)
	if out != "" && len(out) > promptEngagedCeiling {
		t.Errorf("length %d > ceiling %d:\n%s", len(out), promptEngagedCeiling, out)
	}
}

// TestEvaluateSeatAdvanceSkipsTerminalPark: AC5 at evaluateSeat level.
func TestEvaluateSeatAdvanceSkipsTerminalPark(t *testing.T) {
	wfs := routeWFs(
		`["*"]
obligations = ["raised", "coded", "integrated", "released", "closed"]
cancel = { state = "cancelled", gate = "cancel" }
recover = { step = "in_progress" }
`,
		`[raised]
status = "backlog"
start = true

[coded]
status = "in_progress"
agent = "executor"
requires = ["raised"]

[integrated]
status = "integration"
agent = "executor"
reviewers = ["ac-rev"]
reviewer_agent = "reviewer"
requires = ["coded"]

[released]
status = "release"
agent = "executor"
reviewers = ["int-rev"]
reviewer_agent = "reviewer"
requires = ["integrated"]

[closed]
status = "done"
reviewers = ["rel-rev"]
reviewer_agent = "reviewer"
terminal = true
requires = ["released"]
`)
	now := time.Now().UTC()
	// release: only terminal + back-edge → Advance empty.
	live, _, err := evaluateFirstSeat([]lease.Lease{{
		ItemID: "sty_rel", State: "release", Owner: "a",
		AcquiredAt: now.Add(-time.Hour), HeartbeatAt: now.Add(-time.Minute),
	}}, []workitem.Item{{ID: "sty_rel", Kind: workitem.KindStory, Status: "release", Category: "feature"}}, wfs, now)
	if err != nil {
		t.Fatal(err)
	}
	if !live.Engaged {
		t.Fatal("release seat should be engaged")
	}
	if len(live.Advance) != 0 {
		t.Fatalf("release Advance = %+v, want empty", live.Advance)
	}
	// in_progress → integration only.
	live, _, err = evaluateFirstSeat([]lease.Lease{{
		ItemID: "sty_ip", State: "in_progress", Owner: "a",
		AcquiredAt: now.Add(-time.Hour), HeartbeatAt: now.Add(-time.Minute),
	}}, []workitem.Item{{ID: "sty_ip", Kind: workitem.KindStory, Status: "in_progress", Category: "feature"}}, wfs, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(live.Advance) != 1 || live.Advance[0].To != "integration" {
		t.Fatalf("in_progress Advance = %+v", live.Advance)
	}
}

// TestPromptFailsOpen: AC6 — when evaluateSeat cannot resolve the governing
// workflow DOT, runHookPrompt still emits the static reminder and returns nil.
// Empty SATELLE_HOME is the clean no-seat path (covered by TestPromptNoSeatIsUnchanged);
// this forces a real resolve error via a DOT-less workflow doc.
func TestPromptFailsOpen(t *testing.T) {
	repo := tempRepo(t)
	t.Chdir(repo)
	wfDir := filepath.Join(repo, ".satelle", "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Workflow with applies_to but NO fenced dot block — GoverningWorkflow finds
	// it, Parse fails, evaluateSeat returns an error.
	wfBody := "---\nname: broken-wf\ntype: workflow\napplies_to: [\"*\"]\n---\n\nNo DOT here.\n"
	if err := os.WriteFile(filepath.Join(wfDir, "broken-wf.md"), []byte(wfBody), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := db.DocIndex.Sync(ctx, map[string]string{"workflows": wfDir}, time.Now().UTC()); err != nil {
		_ = db.Close()
		t.Fatalf("doc sync: %v", err)
	}
	sty, err := db.Stories.Create(ctx, workitem.CreateInput{
		Kind: workitem.KindStory, Title: "x", Body: "b", AcceptanceCriteria: "1. a",
		Status: "in_progress", Category: "chore",
	}, time.Now().UTC())
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	owner := lease.ResolveOwner()
	if _, _, _, err := db.Leases.Acquire(ctx, sty.ID, "story", owner, "in_progress", true); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Leases.Confirm(ctx, sty.ID, "in_progress"); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	_ = db.Close()

	out, err := runRootIn(t, `{}`, "hook", "prompt")
	if err != nil {
		t.Fatalf("runHookPrompt must return nil on DOT resolve failure: %v\n%s", err, out)
	}
	if !strings.Contains(out, "edits require an ENGAGED story") {
		t.Errorf("reminder missing on fail-open path:\n%s", out)
	}
	// Must not crash into an engaged form that invents gates from a missing DOT.
	if strings.Contains(out, "advance:") {
		t.Errorf("fail-open path must not emit engaged advance form:\n%s", out)
	}
}
