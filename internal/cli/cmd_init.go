// `satelle init` — scaffold a repo for satelle, idempotently. It ensures the
// .satelle/ directory, a documented satelle.toml (created if missing, never
// clobbered), the authored-markdown dirs the directory monitor watches, the
// home-keyed runtime database (~/.satelle/<repo-key>/satelle.db), and a managed
// .gitignore block in the tracked-substrate shape: .satelle/ is tracked in git
// and only its machine-local state (local.toml, logs, hooks, pinned binary,
// execution records) is ignored. Re-running is safe: authored files are
// preserved; the managed .gitignore block gains missing entries and a retired
// block is refreshed (sty_45bf867b). It ends by VALIDATING
// the deployed system (sty_d0d6bb67) and exits non-zero when the deployment
// does not validate green.

package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/wfdot"
	"github.com/bobmcallan/satelle/internal/wfgovern"
)

func init() {
	var configArg string
	var noWorkspace bool
	var harnessFlag string
	cmd := &cobra.Command{
		Use: "init",
		// No `install` alias — it collided with `satelle service install`.
		// Invoking the removed spelling fails closed naming this command.
		Short: "Scaffold this repo for satelle (.satelle/, config, database, authored dirs)",
		Long: `init makes a repo ready for satelle, idempotently. It ensures:

  - the .satelle/ directory,
  - a satelle.toml (created if missing, left intact if present) — every setting
    has a default, so the file ships fully commented and the repo runs zero-config,
  - the authored-markdown dirs (documents, workflows, principles, skills) the
    directory monitor watches and indexes,
  - the per-repo SQLite database on the home-keyed runtime plane
    (~/.satelle/<repo-key>/satelle.db; created and migrated),
  - a managed .gitignore block: .satelle/ is tracked in git, only its
    machine-local state (local.toml, logs, hooks, pinned binary) ignored; your
    own .satelle/ ignore is reported.
  - process hooks ON DEMAND for coding harnesses in use (epic:minimal-harness-footprint):
    scaffolds only when the repo already has .claude/ or .grok/, or when
    --harness names them (claude,grok). Never from PATH and never a silent
    claude default. An empty repo with no flag gets zero harness scaffolds.
    Use-time lazy install: store-backed verbs detect the harness session
    marker and install the matching scaffold if missing (first session
    of a new harness may run without hooks; they take effect next session).
  - registration of this repo in the local workspace registry (opt out with
    --no-workspace) so 'satelle serve' and the /workspace view see it.

init/install end by VALIDATING the deployed system — the agents layer must load
and every substrate artifact must pass its deterministic structure check — and
exit non-zero when validation fails (broken configuration refuses to run).

Re-running is safe: authored files are preserved and .gitignore only gains
missing entries; your .satelle and harness-dir settings are kept. The report
shows what was added versus already present.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			forced, err := parseHarnessFlag(harnessFlag)
			if err != nil {
				return err
			}
			return runInit(cmd.OutOrStdout(), initRepoRoot(configArg), noWorkspace, forced)
		},
	}
	cmd.Flags().StringVar(&configArg, "config", "", "path to satelle.toml (resolves the repo root; default: walk up from CWD)")
	cmd.Flags().BoolVar(&noWorkspace, "no-workspace", false, "skip registering this repo in the local workspace registry")
	cmd.Flags().StringVar(&harnessFlag, "harness", "", "comma-separated harness scaffolds to install (claude,grok); when empty, only existing .claude/.grok dirs are scaffolded — never PATH")
	register(cmd)
}

// parseHarnessFlag parses --harness claude,grok into a unique ordered list.
func parseHarnessFlag(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	var out []string
	seen := map[string]bool{}
	for _, p := range strings.Split(s, ",") {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		switch p {
		case "claude", "grok":
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		default:
			return nil, fmt.Errorf("init: unknown --harness %q (want claude and/or grok)", p)
		}
	}
	return out, nil
}

// initRepoRoot resolves the repo to scaffold: the directory holding an existing
// .satelle/ (via config resolution), else the current directory for a fresh repo.
func initRepoRoot(configArg string) string {
	if _, path, err := config.Load(configArg); err == nil && path != "" {
		return config.RepoRootFromConfigPath(path)
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

// runInit performs the idempotent scaffold for repoRoot.
// noWorkspace skips registration in the machine-local workspace registry
// (sty_3bdbdc38); default is false (register).
// forcedHarness is from --harness (nil/empty = dirs only, no PATH default).
//
// Every substrate file the run creates or changes is recorded in one
// substrate_seeded row in repoRoot's own ledger (sty_ce858cb3), so a story
// engaged meanwhile does not count init's bytes as its own change. init runs
// under no store-backed command and `init --all` targets other repos, so the
// row goes to the ledger opened here, never the wired one.
func runInit(out io.Writer, repoRoot string, noWorkspace bool, forcedHarness []string) error {
	return runInitWith(out, repoRoot, noWorkspace, forcedHarness, true)
}

// runInitWith is runInit with the process-hook step switchable. processHooks
// false skips ensureProcessHooks, for a caller (node join) that learns which
// harness to install only after init, and installs the hooks itself then.
func runInitWith(out io.Writer, repoRoot string, noWorkspace bool, forcedHarness []string, processHooks bool) error {
	_, err := runInitReport(out, repoRoot, noWorkspace, forcedHarness, processHooks)
	return err
}

// runInitReport is runInit returning, beside its error, the synced areas the run
// changed but could not publish: the heal succeeded and its push did not, which
// `init --all` reports per repo and never counts as a failed heal.
func runInitReport(out io.Writer, repoRoot string, noWorkspace bool, forcedHarness []string, processHooks bool) ([]string, error) {
	return nil, runInitBody(out, repoRoot, noWorkspace, forcedHarness, processHooks)
}

// initTomlHealHook, when set, rewrites satelle.toml after the built-in heals; nil
// in production. Every built-in heal reads its trigger from the file itself, so
// a test can make a second run rewrite init's own bytes only through this seam.
var initTomlHealHook func([]byte) []byte

func runInitBody(out io.Writer, repoRoot string, noWorkspace bool, forcedHarness []string, processHooks bool) error {
	// 1. .satelle/ directory.
	dataDir := filepath.Join(repoRoot, config.DefaultDataDir)
	created, err := ensureDir(dataDir)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, initLine(created, config.DefaultDataDir+"/"))

	// 2. satelle.toml — created only if absent; never overwritten.
	tomlPath := filepath.Join(dataDir, config.ConfigName)
	switch _, statErr := os.Stat(tomlPath); {
	case statErr == nil:
		fmt.Fprintln(out, initLine(false, config.DefaultDataDir+"/"+config.ConfigName))
	case os.IsNotExist(statErr):
		if werr := os.WriteFile(tomlPath, []byte(scaffoldToml), 0o644); werr != nil {
			return fmt.Errorf("init: write %s: %w", tomlPath, werr)
		}
		fmt.Fprintln(out, initLine(true, config.DefaultDataDir+"/"+config.ConfigName))
	default:
		return fmt.Errorf("init: stat %s: %w", tomlPath, statErr)
	}

	// 2b. agents.toml — created only if absent.
	agentsPath := filepath.Join(dataDir, config.AgentsConfigDir, config.AgentsConfigName)
	agentsRel := config.DefaultDataDir + "/" + config.AgentsRel
	switch _, statErr := os.Stat(agentsPath); {
	case statErr == nil:
		fmt.Fprintln(out, initLine(false, agentsRel))
	case os.IsNotExist(statErr):
		if mkErr := os.MkdirAll(filepath.Join(dataDir, config.AgentsConfigDir), 0o755); mkErr != nil {
			return fmt.Errorf("init: mkdir %s: %w", config.AgentsConfigDir, mkErr)
		}
		if werr := os.WriteFile(agentsPath, []byte(scaffoldAgentsToml), 0o644); werr != nil {
			return fmt.Errorf("init: write %s: %w", agentsPath, werr)
		}
		fmt.Fprintln(out, initLine(true, agentsRel))
	default:
		return fmt.Errorf("init: stat %s: %w", agentsPath, statErr)
	}

	// 3. Authored-markdown dirs, each with a README.md keep-file.
	for _, kind := range config.AuthoredKinds {
		dir := filepath.Join(dataDir, kind)
		dirCreated, derr := ensureDir(dir)
		if derr != nil {
			return derr
		}
		readmeCreated, rerr := ensureReadme(dir, kind)
		if rerr != nil {
			return rerr
		}
		fmt.Fprintln(out, initLine(dirCreated || readmeCreated, config.DefaultDataDir+"/"+kind+"/"))
	}

	// 4. The per-repo database on the home-keyed runtime plane.
	cfg, _, _ := config.Load(tomlPath)
	rt := cfg.ResolveRuntimeDir(repoRoot)
	if err := os.MkdirAll(rt.Dir, 0o755); err != nil {
		return fmt.Errorf("init: mkdir runtime dir %s: %w", rt.Dir, err)
	}
	dbPath := filepath.Join(rt.Dir, config.DefaultDBName)
	dbExisted := fileExists(dbPath)
	db, derr := store.Open(dbPath)
	if derr != nil {
		return fmt.Errorf("init: open database: %w", derr)
	}
	_ = db.Close()
	_ = config.WriteRepoPathMarker(rt.Dir, repoRoot)
	fmt.Fprintln(out, initLine(!dbExisted, dbPath))

	// 5. Harness hooks on demand (--harness, or an existing harness dir).
	if processHooks {
		if err := ensureProcessHooks(out, repoRoot, forcedHarness); err != nil {
			return err
		}
	}

	// 6. .gitignore — add only the entries satelle needs.
	if added, gerr := ensureGitignore(repoRoot); gerr != nil {
		return gerr
	} else if len(added) > 0 {
		fmt.Fprintln(out, initLine(true, ".gitignore (added: "+strings.Join(added, ", ")+")"))
	} else {
		fmt.Fprintln(out, initLine(false, ".gitignore (satelle entries)"))
	}

	// 6b. Local workspace registry, so satelled sees this repo.
	ensureWorkspaceRegistration(out, repoRoot, noWorkspace)

	// 7. Validate: a repo with a broken configuration refuses to run.
	if verr := validateDeployment(out, dataDir); verr != nil {
		return verr
	}
	fmt.Fprintln(out, "\nReady. Try: satelle status · satelle story create --title \"…\"")
	return nil
}

// agentInstructionFiles returns the agent instruction files present at the repo
// root — CLAUDE.md / AGENTS.md, matched case-insensitively — as their on-disk names.
func agentInstructionFiles(repoRoot string) []string {
	entries, err := os.ReadDir(repoRoot)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch strings.ToLower(e.Name()) {
		case "claude.md", "agents.md":
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// remotesDirName is the directory under the data dir holding a repo's remote
// definitions, one directory per remote, each with its remote.toml.
const remotesDirName = "remotes"

// failVisibleMarker is the stable substring that identifies a fail-visible
// PreToolUse wrapper (sty_c75c73ed). Lives in the script body under
// .satelle/hooks/ (sty_adfb9862); heal paths also match legacy inline forms.
const failVisibleMarker = "#satelle-failvisible"

// satelleHookScriptRel is the single parameterized fail-visible PreToolUse
// wrapper (epic:minimal-harness-footprint): gate|commitgate × claude|grok.
const satelleHookScriptRel = config.HookWrapperRel

// legacyHookScriptRel is the pre-consolidation per-harness path (retired).
func legacyHookScriptRel(harness, sub string) string {
	return ".satelle/hooks/pretooluse-" + sub + "-" + harness + ".sh"
}

// hookScriptRel is kept as an alias for callers/tests that still pass harness+sub
// for path expectations of the *parameterized* form (settings commands embed sub+harness).
func hookScriptRel(harness, sub string) string {
	_ = harness
	_ = sub
	return satelleHookScriptRel
}

// renderHookCommand returns a $-free harness command that runs the parameterized
// wrapper with static sub and harness tokens (sty_adfb9862).
//
// When repoRoot is non-empty the script path is ABSOLUTE so a drifted shell cwd
// (agent `cd` into a subdir) cannot brick PreToolUse with "No such file" on the
// relative ".satelle/hooks/satelle-hook.sh". Absolute paths contain no "$", so
// Grok still accepts them. Empty repoRoot yields the portable relative form
// (tests / callers without a root).
func renderHookCommand(repoRoot, harness, sub string) string {
	script := satelleHookScriptRel
	if strings.TrimSpace(repoRoot) != "" {
		abs := filepath.Join(repoRoot, filepath.FromSlash(satelleHookScriptRel))
		if a, err := filepath.Abs(abs); err == nil {
			abs = a
		}
		script = filepath.ToSlash(abs)
	}
	return "sh " + script + " " + sub + " " + harness
}

// parameterizedHookScriptBody is the single script body for satelle-hook.sh.
// Usage: sh .satelle/hooks/satelle-hook.sh <gate|commitgate> <claude|grok|pi|cursor>
// (pi takes the claude-envelope branch: the generated pi extension parses the
// same permissionDecision deny). The per-harness deny encodings and the globs
// that recognise them come from agentcli (HookDenyHarnesses, PreToolUseDeny,
// PreToolUseDenyMatch); this script holds no copy of them beyond the generated
// case arms.
//
// The wrapper:
//  1. Resolves satelle from $HOME/.local/bin/satelle → $CLAUDE_PROJECT_DIR/.satelle/satelle
//     (or SATELLE_PROJECT_DIR) → relative .satelle/satelle → PATH
//  2. Runs `satelle hook <sub>`, capturing stdout and stderr separately
//  3. Passes a recognised deny through byte-for-byte + handler exit 0
//  4. On infra/malformed failure: harness-correct static deny JSON + exit 0
//  5. commitgate: non-mutating bash fails OPEN on infra failure; commit/push closed
//
// A PreToolUse exit 2 is deliberately not used here: Claude ignores
// structured stdout on that path and requires the reason on stderr. Mixing JSON
// stdout with exit 2 and discarded stderr caused the invisible-denial defect.
func parameterizedHookScriptBody() string {
	shQuote := func(s string) string { return strings.ReplaceAll(s, `'`, `'\''`) }
	// One infra arm and one deny-recognition arm per harness that has its own
	// deny shape (agentcli.HookDenyHarnesses); the encodings and globs come from
	// the adapter, never restated here. The default arm is Claude's.
	var infraArms, denyArms, warnArms strings.Builder
	for _, h := range agentcli.HookDenyHarnesses() {
		fmt.Fprintf(&infraArms, "  %s) infra='%s' ;;\n", h, shQuote(infraDenyJSON(h)))
		fmt.Fprintf(&denyArms, "    %s) case \"$1\" in %s) return 0;; esac ;;\n", h, agentcli.PreToolUseDenyMatch(h))
	}
	// A PreToolUse warn (a repo's [[gate.command_rule]] warn) is an exit-0 answer
	// that is not a deny; without a pass-through arm docase would drop it, or turn
	// a warned git commit/push into the infra deny.
	for _, h := range agentcli.HookWarnHarnesses() {
		fmt.Fprintf(&warnArms, "    %s) case \"$1\" in %s) return 0;; esac ;;\n", h, agentcli.PreToolUseWarnMatch(h))
	}
	return fmt.Sprintf(`#!/bin/sh
%s
# args: $1=gate|commitgate  $2=claude|grok|pi
sub="$1"
harness="$2"
# The shell gate is the edit gate (commitgate was merged into gate).
hsub="$sub"
[ "$sub" = "commitgate" ] && hsub="gate"
case "$harness" in
%s  *)    infra='%s' ;;
esac
# Prefer harness project pin so binary probe works even if invocation cwd drifted.
root=""
for d in "$CLAUDE_PROJECT_DIR" "$SATELLE_PROJECT_DIR"; do
  if [ -n "$d" ] && [ -d "$d" ]; then root="$d"; break; fi
done
b=""
for c in "$HOME/.local/bin/satelle" ${root:+"$root/.satelle/satelle"} ".satelle/satelle" satelle; do
  [ -z "$c" ] && continue
  if [ -x "$c" ]; then b="$c"; break; fi
  if command -v "$c" >/dev/null 2>&1; then b=$(command -v "$c"); break; fi
done
p=$(cat)
structured_deny(){
  case "$harness" in
%s    *)    case "$1" in %s) return 0;; esac ;;
  esac
  return 1
}
structured_warn(){
  case "$harness" in
%s  esac
  return 1
}
deny_infra(){ printf '%%s\n' "$infra"; exit 0; }
run_hook(){
  errfile=$(mktemp "${TMPDIR:-/tmp}/satelle-hook.XXXXXX") || errfile=""
  if [ -n "$errfile" ]; then
    trap 'rm -f "$errfile"' 0
    trap 'exit 129' HUP
    trap 'exit 130' INT
    trap 'exit 143' TERM
    o=$(printf '%%s' "$p" | "$b" hook "$hsub" --harness "$harness" 2>"$errfile")
    code=$?
  else
    # Keep merged failure output private; an unusable result becomes the static
    # infrastructure deny below rather than leaking arbitrary stderr or secrets.
    o=$(printf '%%s' "$p" | "$b" hook "$hsub" --harness "$harness" 2>&1)
    code=$?
  fi
}
if [ "$sub" = "commitgate" ]; then
  docase(){ case "$p" in *git\ commit*|*git\ push*) deny_infra;; *) exit 0;; esac; }
  if [ -z "$b" ]; then docase; fi
  run_hook
  if [ "$code" -eq 0 ]; then
    if [ -z "$o" ]; then exit 0; fi
    if structured_deny "$o"; then printf '%%s\n' "$o"; exit 0; fi
    if structured_warn "$o"; then printf '%%s\n' "$o"; exit 0; fi
    docase
  fi
  if structured_deny "$o"; then printf '%%s\n' "$o"; exit 0; fi
  docase
fi
if [ -z "$b" ]; then deny_infra; fi
run_hook
if [ "$code" -eq 0 ]; then
  if [ -z "$o" ]; then exit 0; fi
  if structured_deny "$o"; then printf '%%s\n' "$o"; exit 0; fi
  deny_infra
fi
if structured_deny "$o"; then printf '%%s\n' "$o"; exit 0; fi
deny_infra
`, failVisibleMarker, infraArms.String(), shQuote(infraDenyJSON("claude")),
		denyArms.String(), agentcli.PreToolUseDenyMatch(agentcli.HarnessClaude), warnArms.String())
}

// failVisibleScriptBody returns the canonical wrapper body. The single script
// is harness/sub parameterized; harness+sub args are accepted for call-site
// compatibility (body is identical for all pairs).
func failVisibleScriptBody(harness, sub string) string {
	_ = harness
	_ = sub
	return parameterizedHookScriptBody()
}

// writeHookScripts materialises the single parameterized fail-visible PreToolUse
// wrapper and retires legacy per-harness / kimi scripts (epic:minimal-harness-footprint).
func writeHookScripts(repoRoot string) error {
	dir := filepath.Join(repoRoot, ".satelle", "hooks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("init: mkdir %s: %w", dir, err)
	}
	path := filepath.Join(repoRoot, filepath.FromSlash(satelleHookScriptRel))
	body := parameterizedHookScriptBody()
	if prev, err := os.ReadFile(path); err != nil || string(prev) != body {
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			return fmt.Errorf("init: write %s: %w", path, err)
		}
	}
	// Retire per-harness and kimi residue (no replacement for kimi).
	for _, harness := range []string{"claude", "grok", "kimi"} {
		for _, sub := range []string{"gate", "commitgate"} {
			_ = os.Remove(filepath.Join(repoRoot, filepath.FromSlash(legacyHookScriptRel(harness, sub))))
		}
	}
	for _, rel := range []string{
		".satelle/hooks/stop-kimi.sh",
		".satelle/bin/kimi-argv.sh",
		".satelle/kimi/config.toml",
	} {
		_ = os.Remove(filepath.Join(repoRoot, filepath.FromSlash(rel)))
	}
	// Drop empty kimi dir if empty.
	_ = os.Remove(filepath.Join(repoRoot, ".satelle", "kimi"))
	return nil
}

// isScriptFormHookCommand reports whether cmd is already a $-free script form
// for this harness/sub (sty_adfb9862). Accepts the absolute form for repoRoot
// (canonical) and the legacy relative form (heal upgrades relative → absolute).
func isScriptFormHookCommand(cmd, harness, sub, repoRoot string) bool {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" || strings.Contains(cmd, "$") {
		return false
	}
	if cmd == renderHookCommand(repoRoot, harness, sub) {
		return true
	}
	// Legacy relative form.
	if cmd == renderHookCommand("", harness, sub) {
		return true
	}
	// Any absolute path to the parameterized script with the right args.
	wantSuffix := satelleHookScriptRel + " " + sub + " " + harness
	return strings.HasPrefix(cmd, "sh ") && strings.HasSuffix(cmd, wantSuffix) && strings.Contains(cmd, satelleHookScriptRel)
}

// buildClaudeHookSettings returns the .claude/settings.json scaffold bytes.
// PreToolUse gates use the fail-visible wrapper (sty_c75c73ed); SessionStart /
// prompt / stopcheck stay simple (fail open by design).
// repoRoot makes PreToolUse script paths absolute (cwd-safe).
func buildClaudeHookSettings(repoRoot string) []byte {
	hs := agentcli.HarnessHooks("claude")
	doc := map[string]any{
		"hooks": map[string]any{
			"SessionStart": []any{
				map[string]any{"hooks": []any{
					map[string]any{"type": "command", "command": "satelle reindex"},
					map[string]any{"type": "command", "command": "satelle hook context"},
				}},
			},
			"PreToolUse": []any{
				map[string]any{
					"matcher": hs.GateMatcher,
					"hooks":   []any{map[string]any{"type": "command", "command": renderHookCommand(repoRoot, "claude", "gate")}},
				},
				map[string]any{
					"matcher": hs.CommitMatcher,
					"hooks":   []any{map[string]any{"type": "command", "command": renderHookCommand(repoRoot, "claude", "commitgate")}},
				},
			},
			"UserPromptSubmit": []any{
				map[string]any{"hooks": []any{
					map[string]any{"type": "command", "command": promptHookCommand},
				}},
			},
			"Stop": []any{
				map[string]any{"hooks": []any{stopHookEntry(stopcheckHookCommand)}},
			},
		},
		// No statusLine (sty_325df80c): it is an operator preference and this file
		// is shared repo scaffold. The renderer stays; an operator who wants the
		// line puts it in their own settings — see statusLineOptInNotice.
	}
	for _, ev := range hs.TurnEndEvents {
		doc["hooks"].(map[string]any)[ev] = []any{
			map[string]any{"hooks": []any{map[string]any{"type": "command", "command": turnEndHookCommand("claude")}}},
		}
	}
	b, _ := json.MarshalIndent(doc, "", "  ")
	return append(b, '\n')
}

// withGrokHarness appends the explicit --harness grok flag to a satelle hook
// command (sty_719c4a7b AC2). The installed hook then authoritatively names
// its own harness on every invocation rather than leaning solely on the event
// sniff, which stays correct even if a future grok payload shape changes.
// "satelle reindex" is not a hook subcommand and is exempt.
func withGrokHarness(cmd string) string {
	return cmd + " --harness grok"
}

// grokSessionContextEvent is the event satelle hook context is installed on
// for this repo: the repo's [harness.grok] session_context_event, else the
// embedded default. Empty when neither declares one — the installer does not
// guess an event.
func grokSessionContextEvent(repoRoot string) string {
	var cfg config.Config
	if strings.TrimSpace(repoRoot) != "" {
		path := filepath.Join(repoRoot, config.DefaultDataDir, config.ConfigName)
		if c, _, err := config.Load(path); err == nil {
			cfg = c
		}
	}
	return cfg.SessionContextEvent(agentcli.HarnessGrok)
}

// buildGrokHookSettings returns the .grok/hooks/satelle.json scaffold bytes.
// Matchers cover Grok-native tool ids and Claude aliases Grok maps (sty_2fad11b0).
// repoRoot makes PreToolUse script paths absolute (cwd-safe). SessionStart keeps
// satelle reindex. satelle hook context is installed, matcher-less, on the event
// SessionContextEvent resolves to — beside reindex when that event is
// SessionStart, and not also on SessionStart when it is another event.
// UserPromptSubmit and Stop pass --harness grok (sty_719c4a7b AC2);
// PreToolUse already does via renderHookCommand's positional harness arg.
func buildGrokHookSettings(repoRoot string) []byte {
	hs := agentcli.HarnessHooks("grok")
	event := grokSessionContextEvent(repoRoot)
	contextCmd := withGrokHarness("satelle hook context")
	sessionHooks := []any{
		map[string]any{"type": "command", "command": "satelle reindex"},
	}
	if event == "SessionStart" {
		sessionHooks = append(sessionHooks, map[string]any{"type": "command", "command": contextCmd})
	}
	hooks := map[string]any{
		"SessionStart": []any{
			map[string]any{"hooks": sessionHooks},
		},
		"PreToolUse": []any{
			map[string]any{
				"matcher": hs.GateMatcher,
				"hooks":   []any{map[string]any{"type": "command", "command": renderHookCommand(repoRoot, "grok", "gate")}},
			},
			map[string]any{
				"matcher": hs.CommitMatcher,
				"hooks":   []any{map[string]any{"type": "command", "command": renderHookCommand(repoRoot, "grok", "commitgate")}},
			},
		},
		"UserPromptSubmit": []any{
			map[string]any{"hooks": []any{
				map[string]any{"type": "command", "command": withGrokHarness(promptHookCommand)},
			}},
		},
		"Stop": []any{
			map[string]any{"hooks": []any{stopHookEntry(withGrokHarness(stopcheckHookCommand))}},
		},
	}
	if event != "" && event != "SessionStart" {
		placeMatcherlessCommand(hooks, event, contextCmd)
	}
	doc := map[string]any{"hooks": hooks}
	b, _ := json.MarshalIndent(doc, "", "  ")
	return append(b, '\n')
}

// placeMatcherlessCommand appends a matcher-less command group on event so it
// fires for every invocation of that event, not only a tool-name match.
func placeMatcherlessCommand(hooks map[string]any, event, cmd string) {
	group := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": cmd}}}
	arr, _ := hooks[event].([]any)
	hooks[event] = append(arr, group)
}

// grokHooksRel is the repo-relative path of the satelle-owned Grok hooks file.
const grokHooksRel = ".grok/hooks/satelle.json"

// retiredHookCommands maps RETIRED satelle CLI commands to their replacements —
// the reconciliation seam for hook commands in an existing harness hook file
// (sty_6a919dff): a repo initialised before a rename otherwise invokes a removed
// command forever (observed: a SessionStart hook still running `satelle index`).
// Extend this map on every future rename/removal.
var retiredHookCommands = map[string]string{
	"satelle index": "satelle reindex",
}

// detectProcessHarnesses decides which process-hook scaffolds to apply
// (epic:minimal-harness-footprint). Signals:
//   - forced: explicit --harness list (wins when non-empty)
//   - existing harness dirs in the repo (.claude / .grok)
//
// Never PATH. Never a silent claude default (empty → install nothing).
func detectProcessHarnesses(repoRoot string, forced []string) (claude, grok bool) {
	if len(forced) > 0 {
		for _, h := range forced {
			switch strings.ToLower(strings.TrimSpace(h)) {
			case "claude":
				claude = true
			case "grok":
				grok = true
			}
		}
		return claude, grok
	}
	claude = dirExists(filepath.Join(repoRoot, ".claude"))
	grok = dirExists(filepath.Join(repoRoot, ".grok"))
	return claude, grok
}

// detectSessionHarnesses probes the process environment for harness session
// markers (never PATH); the marker names live in the agentcli adapters.
func detectSessionHarnesses() (claude, grok bool) {
	return detectSessionHarnessesFrom(os.Environ())
}

// detectSessionHarnessesFrom is the testable core (environ as KEY=VALUE entries).
func detectSessionHarnessesFrom(environ []string) (claude, grok bool) {
	found := agentcli.DetectSessionHarnesses(environ)
	return found[agentcli.HarnessClaude], found[agentcli.HarnessGrok]
}

func dirExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

// ensureLazySessionHarness installs missing harness scaffolds when a store-backed
// verb runs inside a harness session (session env markers). No-op outside an
// initialized satelle repo, when markers are absent, or when scaffolds exist.
// Best-effort: errors are ignored so a store verb still proceeds (hooks appear next session).
func ensureLazySessionHarness(repoRoot string) {
	dataDir := filepath.Join(repoRoot, config.DefaultDataDir)
	if st, err := os.Stat(dataDir); err != nil || !st.IsDir() {
		return
	}
	wantClaude, wantGrok := detectSessionHarnesses()
	if wantClaude {
		settings := filepath.Join(repoRoot, ".claude", "settings.json")
		if _, err := os.Stat(settings); os.IsNotExist(err) {
			_, _, _, _ = ensureClaudeHooks(repoRoot)
		}
	}
	if wantGrok {
		path := filepath.Join(repoRoot, filepath.FromSlash(grokHooksRel))
		if _, err := os.Stat(path); os.IsNotExist(err) {
			_, _, _, _ = ensureGrokHooks(repoRoot)
			_ = ensureGrokFolderTrust(io.Discard, repoRoot)
			_ = ensureGrokCompatConfig(io.Discard)
		}
	}
}

// ensureProcessHooks scaffolds Claude and/or Grok process hooks per detection
// and reports each outcome on out (same initLine / "~ updated" style as before).
// When Grok is detected it also disables Claude-compat hooks in ~/.grok/config.toml
// so Grok does not double-fire the same satelle gate/commitgate (sty_24b32127).
// forced is from init --harness (nil = dirs only).
func ensureProcessHooks(out io.Writer, repoRoot string, forced []string) error {
	// Always materialise/retire the shared hook script surface (even when no
	// harness settings are installed) so migrate heals legacy scripts.
	if err := writeHookScripts(repoRoot); err != nil {
		return err
	}
	if err := healPiExtension(out, repoRoot); err != nil {
		return err
	}
	wantClaude, wantGrok := detectProcessHarnesses(repoRoot, forced)
	if !wantClaude && !wantGrok {
		fmt.Fprintln(out, "  · process hooks: none (no .claude/.grok dirs and no --harness; use --harness claude,grok or open a harness session for lazy install)")
		return nil
	}
	if wantClaude {
		added, updated, incomplete, err := ensureClaudeHooks(repoRoot)
		if err != nil {
			return err
		}
		if len(updated) > 0 {
			fmt.Fprintf(out, "  ~ .claude/settings.json (hook updated: %s)\n", strings.Join(updated, "; "))
		} else {
			fmt.Fprintln(out, initLine(added, ".claude/settings.json (process hooks)"))
		}
		if len(incomplete) > 0 {
			fmt.Fprintf(out, "WARN  .claude/settings.json — incomplete satelle hooks after heal: missing %s\n",
				strings.Join(incomplete, ", "))
		}
	}
	if wantGrok {
		added, updated, incomplete, err := ensureGrokHooks(repoRoot)
		if err != nil {
			return err
		}
		if len(updated) > 0 {
			fmt.Fprintf(out, "  ~ %s (hook updated: %s)\n", grokHooksRel, strings.Join(updated, "; "))
		} else {
			fmt.Fprintln(out, initLine(added, grokHooksRel+" (process hooks)"))
		}
		if len(incomplete) > 0 {
			fmt.Fprintf(out, "WARN  %s — incomplete satelle hooks after heal: missing %s\n",
				grokHooksRel, strings.Join(incomplete, ", "))
		}
		// Project hooks load only when Grok trusts the folder; grant trust for
		// this repo root so .grok/hooks/satelle.json is not silently skipped
		// (sty_edb01f49 — same effect as /hooks-trust, automatic on init).
		if err := ensureGrokFolderTrust(out, repoRoot); err != nil {
			return err
		}
		// Grok-native .grok/hooks/satelle.json is the sole process-hook path under
		// Grok; leave skills/rules/agents/mcps alone.
		if err := ensureGrokCompatConfig(out); err != nil {
			return err
		}
	}
	return nil
}

// ensureGrokFolderTrust records repoRoot in ~/.grok/trusted_folders.toml so
// Grok loads project process hooks. Reports a line only when the store changes
// (first trust or promote from trusted=false).
func ensureGrokFolderTrust(out io.Writer, repoRoot string) error {
	changed, abs, err := config.EnsureGrokFolderTrusted(repoRoot)
	if err != nil {
		return fmt.Errorf("init: grok folder trust: %w", err)
	}
	if !changed {
		return nil
	}
	fmt.Fprintf(out, "  + ~/.grok/trusted_folders.toml (Grok project hooks trusted for %s)\n", abs)
	return nil
}

// ensureGrokCompatConfig surgically upserts [compat.claude] hooks = false into
// ~/.grok/config.toml so Grok does not also load .claude/settings.json hooks
// (double-fire with .grok/hooks/satelle.json). Creates the file when absent;
// preserves every other key/table. Idempotent: already-false is a silent no-op
// (no write, no report line).
func ensureGrokCompatConfig(out io.Writer) error {
	path, err := config.GrokConfigPath()
	if err != nil {
		return fmt.Errorf("init: grok config path: %w", err)
	}
	var before string
	raw, rerr := os.ReadFile(path)
	switch {
	case rerr == nil:
		before = string(raw)
	case os.IsNotExist(rerr):
		// empty before
	default:
		return fmt.Errorf("init: read %s: %w", path, rerr)
	}
	after := config.UpsertKey(before, "compat.claude", "hooks", "false")
	if after == before {
		return nil // already hooks = false (or surgically identical)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("init: mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(after), 0o644); err != nil {
		return fmt.Errorf("init: write %s: %w", path, err)
	}
	label := "~/.grok/config.toml ([compat.claude] hooks=false)"
	if before == "" {
		fmt.Fprintln(out, initLine(true, label))
	} else {
		fmt.Fprintf(out, "  ~ %s\n", label)
	}
	return nil
}

// reconcileHookFile surgically rewrites known-retired satelle commands inside
// an existing hook JSON — exact-command string swap (word-boundary guarded), so
// every other byte of the user-owned file is preserved. Returns the applied
// renames ("old -> new"), empty when nothing was stale. Idempotent.
func reconcileHookFile(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s := string(data)
	var changed []string
	for old, repl := range retiredHookCommands {
		re := regexp.MustCompile(regexp.QuoteMeta(old) + `\b`)
		if re.MatchString(s) {
			s = re.ReplaceAllString(s, repl)
			changed = append(changed, old+" -> "+repl)
		}
	}
	if len(changed) == 0 {
		return nil, nil
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		return nil, err
	}
	sort.Strings(changed)
	return changed, nil
}

// reconcileClaudeHooks is the Claude settings path of reconcileHookFile
// (kept for existing tests and call sites).
func reconcileClaudeHooks(path string) ([]string, error) { return reconcileHookFile(path) }

// retrofitGrokHarnessFlag appends --harness grok to an already-present
// SessionStart/UserPromptSubmit/Stop satelle hook command that predates
// sty_719c4a7b — the counterpart of ensureReinforcementHooks, which only
// carries the flag on an event it adds fresh. Without this, `satelle init`
// would never heal what doctor's driftGrokHarnessFlag reports on a repo whose
// grok file already has all three events (just without the flag), leaving the
// printed "run satelle init to heal" remediation false. Idempotent: a command
// already carrying --harness (any value) is left alone.
func retrofitGrokHarnessFlag(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s := string(data)
	var changed []string
	for _, cmd := range []string{"satelle hook context", promptHookCommand, stopcheckHookCommand} {
		re := regexp.MustCompile(`"` + regexp.QuoteMeta(cmd) + `"`)
		if re.MatchString(s) {
			s = re.ReplaceAllString(s, `"`+cmd+` --harness grok"`)
			changed = append(changed, cmd+" -> +--harness grok")
		}
	}
	if len(changed) == 0 {
		return nil, nil
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		return nil, err
	}
	sort.Strings(changed)
	return changed, nil
}

// The reinforcement hook commands (the PATH-prefixed form the scaffolds embed).
// Kept as named constants so the HEAL that adds them to an already-initialized
// repo appends exactly what a fresh init writes; the scaffold constants embed the
// same literals and a test asserts both files carry them (drift-caught).
const (
	promptHookCommand    = "PATH=$HOME/.local/bin:$PATH satelle hook prompt"
	stopcheckHookCommand = "PATH=$HOME/.local/bin:$PATH satelle hook stopcheck"
	// contextHookCommandPathPrefixed is the session-context command in the same
	// PATH-prefixed form, for a scaffold that has no `satelle reindex` beside it
	// to carry the PATH (the cursor scaffold). The Claude and grok scaffolds keep
	// their bare `satelle hook context`.
	contextHookCommandPathPrefixed = "PATH=$HOME/.local/bin:$PATH satelle hook context"
)

// stopHookEntry is the Stop hook's command entry. It carries a timeout longer
// than the wait stopcheck makes for a running gate (stopGateWaitDefault), so the
// hook answers with the verdict instead of being killed by the harness's default
// (sty_c4b92c9e).
func stopHookEntry(command string) map[string]any {
	return map[string]any{"type": "command", "command": command, "timeout": stopHookTimeoutSec}
}

// raiseStopHookTimeout raises the timeout of an installed satelle stopcheck
// command to stopHookTimeoutSec when it has none or a shorter one, and reports
// whether it changed anything. A longer operator-set timeout is left alone.
func raiseStopHookTimeout(event any) bool {
	groups, _ := event.([]any)
	changed := false
	for _, g := range groups {
		gm, _ := g.(map[string]any)
		hs, _ := gm["hooks"].([]any)
		for _, h := range hs {
			hm, _ := h.(map[string]any)
			if cmd, _ := hm["command"].(string); !strings.Contains(cmd, "satelle hook stopcheck") {
				continue
			}
			if cur, _ := hm["timeout"].(float64); cur < stopHookTimeoutSec {
				hm["timeout"] = stopHookTimeoutSec
				changed = true
			}
		}
	}
	return changed
}

// reinforcementSimpleHooks are event→command entries with a single command and
// no matcher (UserPromptSubmit, Stop). SessionStart and PreToolUse are handled
// separately because they need multi-command groups / harness matchers
// (sty_0699637c).
var reinforcementSimpleHooks = []struct{ event, marker, command string }{
	{"UserPromptSubmit", "satelle hook prompt", promptHookCommand},
	{"Stop", "satelle hook stopcheck", stopcheckHookCommand},
}

// ensureReinforcementHooks HEALS an already-initialized repo's hook file: when a
// satelle hook event is absent, it APPENDS it, preserving every other key.
// Covers UserPromptSubmit + Stop (sty_949e8739) and SessionStart + PreToolUse
// (sty_0699637c). harness is "claude" or "grok" so PreToolUse matchers/scripts
// match the scaffold for that harness. Idempotent; unparseable files are left
// untouched. Returns the events it added.
func ensureReinforcementHooks(path, harness, repoRoot string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, nil // not JSON we can safely mutate — leave it untouched
	}
	hooks, ok := root["hooks"].(map[string]any)
	if !ok {
		hooks = map[string]any{}
		root["hooks"] = hooks
	}
	var added []string

	// contextCmd carries --harness grok when healing a grok file
	// (sty_719c4a7b AC2) — a healed file must match what a fresh scaffold would
	// have written, or doctor's drift check would immediately re-flag it.
	// Grok installs that command on the event SessionContextEvent resolves to,
	// not on a compiled event name. Claude keeps it on SessionStart.
	contextCmd := "satelle hook context"
	contextEvent := "SessionStart"
	if harness == "grok" {
		contextCmd = withGrokHarness(contextCmd)
		contextEvent = grokSessionContextEvent(repoRoot)
	}

	changed := false
	if harness == "grok" && contextEvent != "" && contextEvent != "SessionStart" {
		// An older file has the context command on SessionStart. The resolved
		// event is elsewhere, so do not leave a second copy there. reindex stays.
		if next, removed := removeHookMarker(hooks["SessionStart"], "satelle hook context"); removed {
			hooks["SessionStart"] = next
			changed = true
		}
		if !hookEventHasMarker(hooks["SessionStart"], "satelle reindex") {
			group := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "satelle reindex"}}}
			arr, _ := hooks["SessionStart"].([]any)
			hooks["SessionStart"] = append(arr, group)
			added = append(added, "SessionStart")
		}
		if !hookEventHasMarker(hooks[contextEvent], "satelle hook context") {
			placeMatcherlessCommand(hooks, contextEvent, contextCmd)
			added = append(added, contextEvent)
		}
	} else if contextEvent == "SessionStart" || harness != "grok" {
		// SessionStart: need context (and reindex alongside on a full scaffold add).
		if !hookEventHasMarker(hooks["SessionStart"], "satelle hook context") &&
			!hookEventHasMarker(hooks["SessionStart"], "satelle reindex") {
			group := map[string]any{
				"hooks": []any{
					map[string]any{"type": "command", "command": "satelle reindex"},
					map[string]any{"type": "command", "command": contextCmd},
				},
			}
			arr, _ := hooks["SessionStart"].([]any)
			hooks["SessionStart"] = append(arr, group)
			added = append(added, "SessionStart")
		} else if !hookEventHasMarker(hooks["SessionStart"], "satelle hook context") {
			// reindex present but context missing — append context only.
			group := map[string]any{
				"hooks": []any{
					map[string]any{"type": "command", "command": contextCmd},
				},
			}
			arr, _ := hooks["SessionStart"].([]any)
			hooks["SessionStart"] = append(arr, group)
			added = append(added, "SessionStart")
		}
	}

	// PreToolUse: gate + commitgate with harness matchers / script-file commands.
	if harness == "" {
		harness = "claude"
	}
	gateCmd := renderHookCommand(repoRoot, harness, "gate")
	commitCmd := renderHookCommand(repoRoot, harness, "commitgate")
	// The matchers the heal appends are the BUILDER's, from the one table
	// (sty_338a53f8) — a heal that wrote a matcher no scaffold emits produced a
	// file the create path would never have written.
	hs := agentcli.HarnessHooks(harness)
	gateMatcher := hs.GateMatcher
	commitMatcher := hs.CommitMatcher
	if !hookEventHasMarker(hooks["PreToolUse"], "satelle hook gate") &&
		!hookEventHasMarker(hooks["PreToolUse"], "pretooluse-gate-") &&
		!hookEventHasMarker(hooks["PreToolUse"], "satelle-hook.sh") {
		group := map[string]any{
			"matcher": gateMatcher,
			"hooks":   []any{map[string]any{"type": "command", "command": gateCmd}},
		}
		arr, _ := hooks["PreToolUse"].([]any)
		hooks["PreToolUse"] = append(arr, group)
		added = append(added, "PreToolUse")
	}
	if !hookEventHasMarker(hooks["PreToolUse"], "satelle hook commitgate") &&
		!hookEventHasMarker(hooks["PreToolUse"], "pretooluse-commitgate-") &&
		!hookEventHasMarker(hooks["PreToolUse"], "satelle-hook.sh commitgate") &&
		!hookEventHasMarker(hooks["PreToolUse"], "commitgate "+harness) {
		group := map[string]any{
			"matcher": commitMatcher,
			"hooks":   []any{map[string]any{"type": "command", "command": commitCmd}},
		}
		arr, _ := hooks["PreToolUse"].([]any)
		hooks["PreToolUse"] = append(arr, group)
		// Only report PreToolUse once if both gate+commit were added.
		if len(added) == 0 || added[len(added)-1] != "PreToolUse" {
			added = append(added, "PreToolUse")
		}
	}

	// Reinforce only the events THIS harness's scaffold installs, as declared
	// once in agentcli.HarnessHooks rather than repeated as a literal here.
	for _, rh := range reinforcementSimpleHooks {
		if !hs.HasEvent(rh.event) {
			continue
		}
		if hookEventHasMarker(hooks[rh.event], rh.marker) {
			continue
		}
		cmd := rh.command
		if harness == "grok" {
			cmd = withGrokHarness(cmd)
		}
		entry := map[string]any{"type": "command", "command": cmd}
		if rh.event == "Stop" {
			entry = stopHookEntry(cmd)
		}
		group := map[string]any{"hooks": []any{entry}}
		arr, _ := hooks[rh.event].([]any)
		hooks[rh.event] = append(arr, group)
		added = append(added, rh.event)
	}
	// The turn-end events this harness fires (sty_7e4393fc), wired as the
	// builder wires them.
	for _, ev := range hs.TurnEndEvents {
		if hookEventHasMarker(hooks[ev], "satelle hook turnend") {
			continue
		}
		group := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": turnEndHookCommand(harness)}}}
		arr, _ := hooks[ev].([]any)
		hooks[ev] = append(arr, group)
		added = append(added, ev)
	}
	// A Stop hook installed before gate hand-off (sty_c4b92c9e) carries no
	// timeout, so the harness's default would cut its wait for a running gate
	// short. Raise it in place — never lowering one the operator set.
	if hs.HasEvent("Stop") && raiseStopHookTimeout(hooks["Stop"]) {
		added = append(added, "Stop timeout")
	}
	if len(added) == 0 && !changed {
		return nil, nil
	}
	b, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return nil, err
	}
	sort.Strings(added)
	return added, nil
}

// incompleteHookEvents returns event names still missing a satelle marker after
// heal. Empty when the full set is present or the file is unparseable (caller
// may still WARN on unparseable separately).
//
// The expected set is PER HARNESS, read from the same agentcli.HarnessHooks table the
// builder writes from (sty_338a53f8), so a complete file is never reported
// incomplete.
func incompleteHookEvents(path, harness string) []string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return []string{"(unreadable)"}
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return []string{"(unparseable)"}
	}
	hooks, _ := root["hooks"].(map[string]any)
	need := map[string][]string{
		"SessionStart":     {"satelle hook context", "satelle reindex"},
		"PreToolUse":       {"satelle hook gate", "pretooluse-gate-", "satelle-hook.sh"},
		"UserPromptSubmit": {"satelle hook prompt"},
		"Stop":             {"satelle hook stopcheck"},
	}
	var missing []string
	for _, event := range agentcli.HarnessHooks(harness).Events {
		markers := need[event]
		ok := false
		for _, m := range markers {
			if hookEventHasMarker(hooks[event], m) {
				ok = true
				break
			}
		}
		if !ok {
			missing = append(missing, event)
		}
	}
	for _, event := range agentcli.HarnessHooks(harness).TurnEndEvents {
		if !hookEventHasMarker(hooks[event], "satelle hook turnend") {
			missing = append(missing, event)
		}
	}
	// Grok's context command lives on the resolved channel, which may not be
	// one of the events the table above demands. A missing command is this
	// check's finding; drift does not report it. Only the installed grok hooks
	// path carries a repo root we can resolve the channel from.
	if harness == "grok" {
		rel := filepath.FromSlash(grokHooksRel)
		if strings.HasSuffix(path, rel) {
			repoRoot := strings.TrimSuffix(path, rel)
			repoRoot = strings.TrimSuffix(repoRoot, string(filepath.Separator))
			if ev := grokSessionContextEvent(repoRoot); ev != "" && !hookEventHasMarker(hooks[ev], "satelle hook context") {
				missing = append(missing, ev)
			}
		}
	}
	return missing
}

// removeHookMarker drops hook entries whose command contains marker, and drops
// groups that become empty. The event value is returned even when unchanged.
func removeHookMarker(event any, marker string) (any, bool) {
	groups, ok := event.([]any)
	if !ok {
		return event, false
	}
	changed := false
	var out []any
	for _, g := range groups {
		gm, ok := g.(map[string]any)
		if !ok {
			out = append(out, g)
			continue
		}
		hs, ok := gm["hooks"].([]any)
		if !ok {
			out = append(out, g)
			continue
		}
		var kept []any
		for _, h := range hs {
			hm, ok := h.(map[string]any)
			if !ok {
				kept = append(kept, h)
				continue
			}
			cmd, _ := hm["command"].(string)
			if strings.Contains(cmd, marker) {
				changed = true
				continue
			}
			kept = append(kept, h)
		}
		if len(kept) == 0 {
			changed = true
			continue
		}
		if len(kept) != len(hs) {
			gm["hooks"] = kept
		}
		out = append(out, gm)
	}
	if !changed {
		return event, false
	}
	return out, true
}

// hookEventHasMarker reports whether an event's hook groups already contain a
// command carrying the marker substring. Tolerant of the arbitrary nested shape a
// user-owned hook file may hold — any non-conforming node is simply skipped.
func hookEventHasMarker(event any, marker string) bool {
	groups, ok := event.([]any)
	if !ok {
		return false
	}
	for _, g := range groups {
		gm, ok := g.(map[string]any)
		if !ok {
			continue
		}
		hs, ok := gm["hooks"].([]any)
		if !ok {
			continue
		}
		for _, h := range hs {
			hm, ok := h.(map[string]any)
			if !ok {
				continue
			}
			if cmd, _ := hm["command"].(string); strings.Contains(cmd, marker) {
				return true
			}
		}
	}
	return false
}

// ensureClaudeHooks writes .claude/settings.json with the process hooks when
// absent, and RECONCILES known-retired satelle hook commands in an existing one
// (sty_6a919dff) — the user-owned file is otherwise preserved byte-for-byte.
// Also upgrades legacy inline / bare-exit-2 PreToolUse wrappers to the $-free
// script-file form (sty_c75c73ed, sty_adfb9862). Returns whether it created the
// file and any applied updates.
func ensureClaudeHooks(repoRoot string) (created bool, updated []string, incomplete []string, err error) {
	if err := writeHookScripts(repoRoot); err != nil {
		return false, nil, nil, err
	}
	dir := filepath.Join(repoRoot, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, nil, nil, fmt.Errorf("init: mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, "settings.json")
	if _, err := os.Stat(path); err == nil {
		updated, incomplete, herr := healExistingHookFile(path, "claude", repoRoot)
		if herr != nil {
			return false, nil, nil, herr
		}
		return false, updated, incomplete, nil
	} else if !os.IsNotExist(err) {
		return false, nil, nil, fmt.Errorf("init: stat %s: %w", path, err)
	}
	if err := os.WriteFile(path, buildClaudeHookSettings(repoRoot), 0o644); err != nil {
		return false, nil, nil, fmt.Errorf("init: write %s: %w", path, err)
	}
	return true, nil, nil, nil
}

// ensureGrokHooks writes .grok/hooks/satelle.json when absent, and reconciles
// known-retired satelle commands in an existing satelle-owned file. Other files
// under .grok/hooks/ are never touched. Returns created + applied updates.
func ensureGrokHooks(repoRoot string) (created bool, updated []string, incomplete []string, err error) {
	if err := writeHookScripts(repoRoot); err != nil {
		return false, nil, nil, err
	}
	dir := filepath.Join(repoRoot, ".grok", "hooks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, nil, nil, fmt.Errorf("init: mkdir %s: %w", dir, err)
	}
	path := filepath.Join(repoRoot, filepath.FromSlash(grokHooksRel))
	if _, err := os.Stat(path); err == nil {
		// Wholly satelle-owned path: no satelle command at all ⇒ user-owned — skip
		// (sty_9e86f407 AC1). A file with only retired `satelle index` is still
		// satelle-owned and must heal (reconcile → reindex).
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return false, nil, nil, fmt.Errorf("init: read %s: %w", path, rerr)
		}
		if !strings.Contains(string(raw), "satelle") {
			return false, []string{"skipped (not satelle-owned)"}, nil, nil
		}
		updated, incomplete, herr := healExistingHookFile(path, "grok", repoRoot)
		if herr != nil {
			return false, nil, nil, herr
		}
		return false, updated, incomplete, nil
	} else if !os.IsNotExist(err) {
		return false, nil, nil, fmt.Errorf("init: stat %s: %w", path, err)
	}
	if err := os.WriteFile(path, buildGrokHookSettings(repoRoot), 0o644); err != nil {
		return false, nil, nil, fmt.Errorf("init: write %s: %w", path, err)
	}
	return true, nil, nil, nil
}

// healExistingHookFile applies reconcile + reinforcement + fail-visible upgrade
// to an existing hook settings file. Returns the list of human-readable updates
// (empty when nothing changed). incomplete is a WARN note when satelle hooks
// remain incomplete after heal (sty_0699637c).
func healExistingHookFile(path, harness, repoRoot string) (updated []string, incomplete []string, err error) {
	if err := writeHookScripts(repoRoot); err != nil {
		return nil, nil, err
	}
	renames, err := reconcileHookFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("init: reconcile %s: %w", path, err)
	}
	updated = append(updated, renames...)
	if harness == "grok" {
		// ensureReinforcementHooks only carries --harness grok on an event it
		// ADDS fresh; a pre-sty_719c4a7b installed file already has
		// SessionStart/UserPromptSubmit/Stop present and needs the flag
		// retrofitted onto what's already there.
		flagged, ferr := retrofitGrokHarnessFlag(path)
		if ferr != nil {
			return nil, nil, fmt.Errorf("init: retrofit harness flag %s: %w", path, ferr)
		}
		updated = append(updated, flagged...)
	}
	healed, err := ensureReinforcementHooks(path, harness, repoRoot)
	if err != nil {
		return nil, nil, fmt.Errorf("init: reinforce %s: %w", path, err)
	}
	for _, e := range healed {
		updated = append(updated, "added "+e+" hook")
	}
	n, err := upgradeFailVisibleHooks(path, harness, repoRoot)
	if err != nil {
		return nil, nil, fmt.Errorf("init: fail-visible upgrade %s: %w", path, err)
	}
	if n > 0 {
		updated = append(updated, fmt.Sprintf("upgraded %d PreToolUse hook(s) to script-file form", n))
	}
	incomplete = incompleteHookEvents(path, harness)
	return updated, incomplete, nil
}

// upgradeFailVisibleHooks rewrites legacy PreToolUse gate/commitgate commands
// to the $-free absolute script-file form (sty_adfb9862 + cwd-safe absolute path).
// Upgrades:
//   - bare `… hook gate||commitgate || exit 2`
//   - inline `sh -c '…#satelle-failvisible…$c…'` wrappers
//   - relative `sh .satelle/hooks/satelle-hook.sh …` → absolute under repoRoot
//
// Idempotent when already the absolute form for this repoRoot.
func upgradeFailVisibleHooks(path, harness, repoRoot string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return 0, nil // unparseable — leave untouched
	}
	hooks, ok := root["hooks"].(map[string]any)
	if !ok {
		return 0, nil
	}
	pre, ok := hooks["PreToolUse"].([]any)
	if !ok {
		return 0, nil
	}
	n := 0
	for _, g := range pre {
		gm, ok := g.(map[string]any)
		if !ok {
			continue
		}
		hs, ok := gm["hooks"].([]any)
		if !ok {
			continue
		}
		for i, h := range hs {
			hm, ok := h.(map[string]any)
			if !ok {
				continue
			}
			cmd, _ := hm["command"].(string)
			sub := legacyHookSub(cmd)
			if sub == "" {
				continue
			}
			want := renderHookCommand(repoRoot, harness, sub)
			if strings.TrimSpace(cmd) == want {
				continue
			}
			hm["command"] = want
			hs[i] = hm
			n++
		}
		gm["hooks"] = hs
	}
	if n == 0 {
		return 0, nil
	}
	b, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return 0, err
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return 0, err
	}
	return n, nil
}

// legacyHookSub returns "gate" or "commitgate" when cmd is a satelle PreToolUse
// gate command that is not yet (or not only) an unrelated shell snippet.
func legacyHookSub(cmd string) string {
	switch {
	case strings.Contains(cmd, "hook commitgate") || strings.Contains(cmd, "pretooluse-commitgate-") ||
		(strings.Contains(cmd, "satelle-hook.sh") && strings.Contains(cmd, "commitgate")):
		return "commitgate"
	case strings.Contains(cmd, "hook gate") || strings.Contains(cmd, "pretooluse-gate-") ||
		(strings.Contains(cmd, "satelle-hook.sh") && strings.Contains(cmd, " gate ")):
		return "gate"
	default:
		return ""
	}
}

// scaffoldToml is the documented config a fresh init writes. Every key is
// commented because each has a default — the repo runs zero-config until a knob
// is uncommented. The exceptions, [gate] edit_exempt_paths and edit_exempt_globs,
// are seeded ACTIVE from the managed-list helpers so the seeded values and the
// set migrate converges on cannot drift apart — hence the literal is split
// around them.
var scaffoldToml = scaffoldTomlBeforeExempt + defaultEditExemptTOML() + scaffoldTomlBetweenExempt + defaultEditExemptGlobsTOML() + scaffoldTomlAfterExempt

const scaffoldTomlBeforeExempt = `# satelle.toml — per-repo config (committed, secret-free). Every setting has a
# default, so this file may stay fully commented; uncomment a key to override.
# Per-user overrides go in satelle.local.toml beside this file (gitignored).

# data_dir = ".satelle"          # authored substrate home under the repo (default)
# db = ""                        # override DB path; default is home-keyed
#                                # ~/.satelle/<repo-key>/satelle.db (sty_4660bbe1).
#                                # An explicit path wins; otherwise runtime state
#                                # (db, logs, backups, stories cache) lives outside
#                                # the repo. Migrate a legacy in-repo DB with
#                                # 'satelle runtime migrate'.
# log_level = "info"             # debug | info | warn | error (default info)
# logs_max_size_kb = 5120        # roll a runtime log file past this size (default 5 MiB). Logs live in ~/.satelle/<repo-key>/logs; .satelle/logs is a symlink pointer to it.
# logs_max_files = 7             # keep at most this many rotated log files (default 7)

# Archive retention for CLOSED-story attachment dirs under the runtime stories/
# cache (~/.satelle/<repo-key>/stories). Unset (default) = keep everything.
# The two compose — either triggers pruning. A NON-terminal (open/engaged)
# story's dir is ALWAYS kept. Pruned dirs are MOVED to runtime backups/stories/
# (never deleted in place).
# stories_keep_closed = 50       # keep the N most-recently-updated closed-story dirs
# stories_keep_days = 90         # drop a closed story's dir older than N days

# [review] — create gate. Default ON (sty_83782ffb): creation is where
# misclassification is cheapest to catch. gate_create runs the deterministic
# structure check plus the workflow's create_review skill (embedded default:
# satelle-story-create-review — content, alignment, and classification). Needs
# an agent CLI for the LLM half (see 'satelle agent'). Set false to opt out.
[review]
gate_create = true

# [tags.vocabulary] — controlled tag namespaces (optional). A tag in a listed
# namespace must use a declared value (case-insensitive match; stored in the
# casing declared here). Namespaces absent from the table stay free-form.
# Unset = every tag free-form (zero-config). Declare per-repo; never compiled
# into the binary (sty_034d843c).
# [tags.vocabulary]
# surface = ["ui", "cli"]

# [categories] — controlled story TYPE vocabulary (optional). Default list ships
# embedded (feature/fix/chore/…/substrate/epic-parent/parent); satelle.toml may
# extend (extra) or replace (vocabulary). enforce: off | warn | reject
# (default warn — upgrade never hard-breaks existing flows). Category is TYPE,
# not a surface (use [tags.vocabulary] surface for interfaces).
# [categories]
# enforce = "warn"
# extra = ["my-type"]
# vocabulary = ["feature", "fix", "chore"]

# [gate] — edit-gate exemptions and the single-story process rule. This is the
# ONE table seeded ACTIVE (not commented) below, because it is the source of
# truth for which paths escape the engaged-story edit gate — configuration, not
# a Go rule (the constitution: configuration over code).
# edit_exempt_paths lists repo-root-relative (or absolute) path prefixes whose
# edits are exempt from the engaged-story edit gate. It is the SOLE exemption
# source — the binary does NOT special-case the data dir or any managed path.
# ".satelle/" is seeded so this repo's authored data and documents stay editable
# without a story. A path under lock_substrate_paths (below) is never exempt,
# whatever is listed here: the seeded ".satelle/" is overridden for locked
# substrate (workflows/skills/agent bindings). See 'satelle help substrate'.
# The rest of the seeded list is the footprint SATELLE ITSELF deploys, unasked:
# the ".gitignore" managed block init/migrate write (sty_f115e6bf), and the
# harness hook scaffolds init installs under ".claude/" and ".grok/"
# — some of them lazily, mid-session, with no story engaged (sty_926cfcdc).
# Exempting them is not leniency: without it the binary writes a file and then
# its own stop gate refuses the session for the write, with no clean lane to
# commit it. Drop any default to require an engaged story for those paths, or
# add your own prefix (a harness authoring dir holding markdown rather than
# product code). "/tmp/" is seeded so out-of-tree story drafts are writable
# with no performing story; an outside-tree prefix never exempts a path
# inside this repo (a checkout that lives under /tmp still gates product
# code). Product paths stay gated — that is the point of the gate. An
# explicitly empty list is a deliberate opt-out (everything gated) and is never
# converged. Repos initialised before a path joined this list: run
# satelle migrate --yes to append the missing entries without clobbering
# operator additions.
# How MANY stories may perform at once is NOT set here — see [engagement] below.
# allow_outside_tree_edits (default false) opts INTO Bash/Edit mutations whose
# targets land in another git working tree (sty_a8454d10 / sty_aadd4d6c).
# Non-repo paths (temp, scratchpads) are never fenced. Leave false unless this
# install deliberately spans multiple repos from one session — create stories
# cross-repo stays allowed either way; progressing/mutating another tree does not.
[gate]
edit_exempt_paths = `

// scaffoldTomlBetweenExempt is the seeded [gate] edit_exempt_globs line
// (sty_fefc88cd). Basename globs match at any depth; a pattern containing /
// is repo-relative. The seeded set is narrow — any matching filename bypasses
// the edit gate. Removing an entry re-gates that name. Do not seed *.
const scaffoldTomlBetweenExempt = `
# edit_exempt_globs — filename globs (basename match at any depth; a pattern
# containing / is matched against the repo-relative path). Seeded for agent
# story-reference dumps. Narrow on purpose: a matching filename bypasses the
# edit gate. Removing an entry re-gates that name.
edit_exempt_globs = `

// scaffoldTomlAfterExempt is the remainder of the scaffold, resuming after
// the seeded edit_exempt_globs value.
const scaffoldTomlAfterExempt = `

# lock_substrate_paths — path prefixes the edit gate REFUSES to every session
# not engaged in a substrate-lane story, whether or not any story is performing
# (sty_992cffc6, sty_66abc0e3). It overrides edit_exempt_paths: without it an
# edit could rewrite the workflows, skills and agent bindings that judge the
# work, and the change would leave no trace in its engagement diff. The deny
# names the locked path and the lane out — a story in the substrate lane
# (category "substrate", judged by satelle-workflow-change-review) may change
# substrate under its own seat. See 'satelle help substrate'.
# The key is left commented on purpose: ABSENT means the default lock
# (".satelle/"), so a repo is locked without it. Uncomment to pin or widen the
# list. Never locked, whatever is listed: the process temp dir and /tmp,
# edit_exempt_globs matches, and the footprint satelle deploys itself
# (.gitignore, .claude/, .grok/, .pi/). An explicitly EMPTY list is the
# documented opt-out, and 'satelle doctor' reports it. The lock covers shell
# (Bash) mutations too.
# lock_substrate_paths = [".satelle/"]

# command_allow — OPT-IN step-scoped git policy (sty_c21490cc). Keys are git
# subcommands; values are story statuses that may run them while engaged.
# Absent/empty = no step restriction (commitgate only requires engagement).
# Example: permit git push only at the release step:
# [gate.command_allow]
# push = ["release"]
# allow_outside_tree_edits = false
# command_rule — OPT-IN repo-authored judgement of an agent's shell command line
# (sty_a74551d6). satelle ships no rule: with none authored nothing is refused or
# warned. Each [[gate.command_rule]] has a pattern (a whitespace-split argv
# prefix; a token of * matches any one token), an action ("deny" refuses the
# command before it runs; "warn" lets it run and tells the agent, where the
# harness can deliver that), allow (story statuses or workflow step names at
# which the command passes; empty = never passes) and a message. The first
# matching rule, in file order, wins. Best-effort: only the visible command line
# is judged, so wrappers, eval, aliases and scripts bypass a rule. satelle
# manages no identity or rights — the tool's own permissions and the account it
# runs as enforce what a command may do. Example:
# [[gate.command_rule]]
# pattern = "TOOL delete"
# action = "deny"
# allow = ["in_progress"]
# message = "delete changes shared state"
# handoff — whether a gate-running verb called by an agent (or with no terminal)
# returns a handle and delivers the verdict later by notification, instead of
# holding the foreground. "auto" (default) hands off only when a configured
# driver harness has a background cutoff under 1 minute (grok: 15s; claude's 120s
# is above it, so a claude repo whose edges run longer sets "on");
# "on" always hands off for such a caller; "off" always runs in the foreground.
# SATELLE_GATE_MODE=interactive|agent overrides it. See satelle help agent-dispatch.
# handoff = "auto"

# [engagement] — SEAT CONCURRENCY: how many stories may perform at once.
# "none" (default, and what you get with no section) is single occupancy: one
# performing story, everything else refused. "epic" keys the seat on the story's
# PARENT, so children of one parent may engage concurrently — each from its OWN
# git working tree (a second engagement from an already-leased tree is refused,
# and story diff is anchored to the tree its story was engaged from). A story
# under a different parent, or none, is refused while such a seat is held.
# Satelle arbitrates ENGAGEMENT only: how concurrent work converges (merge order,
# suite runs, one push) is this repo's workflow, and satelle polices no
# sibling contention. Only these two values are accepted.
# [engagement]
# parallel = "epic"

# [worktree] — what a worktree opened with 'satelle story worktree <id> --base
# <ref>' needs. git worktree add carries only tracked content, so list the
# gitignored paths (files or directories, repo-relative) this repo needs in a
# tree; each is linked from the main tree, never copied. branch and path are
# templates ({id} is the story id; a relative path resolves from the main tree);
# with no template the --branch / --path flags are required. Nothing is declared
# by default. See 'satelle help worktree'.
# [worktree]
# include = [".env"]
# branch = "work/{id}"
# path = "../trees/{id}"

# substrate_roots — per-kind parent dir for authored markdown. Unset means
# <data_dir>/<kind> (e.g. .satelle/documents). Point a kind elsewhere — even
# outside .satelle/ — to author it at the repo root or another path:
# [substrate_roots]
# documents = "."                # → ./documents
# skills = "."                   # → ./skills

# [sync] — the repo's binding to a hosted project. The repo holds the binding
# alone; the sync scope comes from your account: unbound or signed out = local
# (nothing leaves the machine), a project of your personal team = personal, a
# project of a team you belong to = team. Bind with 'satelle project bind <slug>'
# (no binding is taken from the directory name); 'satelle sync status' names the
# scope and where it came from.
# Never syncs, at any scope: files with a .local segment (satelle.local.toml,
# notes.local.md). Unconditional — no setting enables it; secrets stay put.
# Team catalogs are a separate verb: 'satelle publish'. Whether .satelle process
# is git-tracked is the operator's choice — satelle does not require it.
# Keys: project, workspace, stale_after, hold_unpushed. Unset
# server = https://satelle.dev; satelle login records the machine-wide server.
# A machine that pulls but never pushes sets pull_only in satelle.local.toml
# ('satelle project pull-only on').
[sync]
stale_after = "24h"
# >>> satelle-example: bind to a hosted project (uncomment, or run satelle project bind)
# [sync]
# project = "my-project-slug"    # written by satelle project bind <slug>
# workspace = "team-name"        # per-developer; usually satelle.local.toml
# [vars]
# MODEL_BASE_URL = "https://example.invalid"  # non-secret; secrets → satelle.local.toml
# <<< satelle-example
#
# [backup] — pre-mutation substrate backup policy (sty_873a5380, sty_84f14ace).
# Local copies under .satelle/backups/ always run before init/restore/rebase
# overwrite an existing file. local_only suppresses the advisory about the
# online option. hosted = true opts into pushing pre-images into the bound
# project's personal documents partition (path backups/<rel>) — default off,
# because that partition is listed by documents pull and backups/ is a restore
# exclusion (auto-push permanently wedged sync; sty_84f14ace).
# [backup]
# local_only = true
# hosted = true

# [sync] project / workspace stay per-repo. Do not put server in this file —
# the hosted origin is machine-scope (satelle settings --global server /
# satelle login). satelle init and satelle migrate --yes re-home a leftover
# repo hosted server onto the machine file and fold leftover hosted
# project/workspace onto [sync], then drop the leftover hosted table.
#
# The local push-fed UI endpoint is machine-scope (~/.satelle/config.toml
# [service] endpoint). Unset derives http://127.0.0.1:<port>. Disable with
# SATELLE_SERVER_ENDPOINT=none. Do not put [server] endpoint in this file.
#
# [vars] — operator KV substituted into agents.toml binding env values via
# ${NAME}. NON-secret vars may live here; SECRETS go in gitignored
# satelle.local.toml (per-key overlay wins). Never pushed with substrate sync.

# [output] — compact CLI rendering for the list/diff commands an agent pulls
# through Bash(satelle:*) (sty_75b76691). A fold only ever applies when it
# round-trips to the exact original and is smaller — otherwise the command
# prints the same indented JSON it always has. Unset = off everywhere (this
# table's zero value); a repo opts a verb in by name. compact_for_agents
# makes that the default for a dispatched or in-loop agent caller
# (SATELLE_SCRATCH or SATELLE_SESSION set, or an in-loop harness's own
# session marker); --json on any command always forces
# plain JSON regardless. noise_patterns are globs (basename match unless the
# pattern contains "/") identifying generated/lockfile files a story diff
# --patch offloads whole — the binary ships none of its own.
# [output]
# compact_for_agents = true
# compact_commands = ["ledger-list", "story-list", "story-doc-list", "story-messages", "story-diff"]
# long_cell_bytes = 200
# repeat_min = 3
# noise_patterns = ["go.sum"]

# [output.diff_rank] — ranked diff compressor (sty_918e2086): the gate
# payload's patch reducer (replacing an unranked byte cut), and — when
# enabled — story diff --patch's own compact rendering. Disabled (unset, or
# enabled = false) leaves diffPayloadCeiling's byte backstop as the only
# reducer, as it always has been; the binary ships no threshold, cap, or
# pattern of its own. --full always bypasses this for the raw patch.
# [output.diff_rank]
# enabled = true
# passthrough_lines = 50           # a patch at or under this many lines rides unranked
# max_files = 20                   # keep the N files with the most changed lines
# max_hunks_per_file = 10          # per file: first, last, then top-scored up to N
# context_lines = 2                # unchanged lines kept on each side of a change
# priority_patterns = ["(?i)error", "(?i)todo|fixme|bug|fix", "(?i)security|auth|secret"]
`

// scaffoldAgentsToml is the documented agents layer a fresh init writes. The
// file is REQUIRED once a repo is initialized (sty_d0d6bb67): a missing or
// unparseable agents.toml refuses to run rather than silently falling back to
// compiled defaults — the configuration executes as defined. A repo may widen
// or rebind transparently — the override is a committed file, the operator's
// choice.
var scaffoldAgentsToml = strings.NewReplacer("MODEL_ORDER_BLOCK\n", scaffoldModelOrderToml+"\n", "REVIEWER_COMMAND_TEMPLATE", agentcli.DefaultClaudeCommand).Replace(`# workflows/agents.toml — the agents layer: how each agent runs (backend + tool
# grant). It sits beside done.toml and step.toml because it is the other half of what
# those declare: step.toml names a performer and its gates by SECTION NAME, and the
# [<name>] sections here say what those names actually run (sty_10f732ed).
# COMMITTED SUBSTRATE by product default (sty_552d2d87): this file declares the
# roles your workflows name; a clone needs it to run any gated step. Prefer
# profile= into ~/.satelle/agents.toml for shared HOW; put secrets in the
# gitignored satelle.local.toml — never here. See "satelle help global-agents".
# FULLY DEFINED by init (no hidden coded configuration, sty_892517e7): every
# value below is the ACTIVE default, written out so the operator sees exactly
# what runs. Edit freely. This file is REQUIRED in an initialized repo
# (sty_d0d6bb67): a missing or unparseable agents.toml refuses to run — delete
# it and re-run "satelle init" to reseed the default.
#
# The agent operating model (see the satelle-agent-model principle):
#   - executor  — runs IN-LOOP as the driving session (context, principles,
#     skills via the substrate). Not an isolated process.
#   - reviewer  — an ISOLATED, READ-ONLY sub-process: the rubric rides as its
#     system prompt; it judges, never mutates (the default claude template
#     denylists Write/Edit/NotebookEdit/Bash on top of the read-only grant).
#   - any OTHER top-level [<name>] is an optional named agent, always isolated;
#     a route step allocates its work to it via agent: <name>, and entering that
#     step DISPATCHES the work to this binding's command (item on stdin, the
#     step's skills: rubric as the system prompt, tools/model from the binding).
#     A step naming an agent with NO binding here REFUSES the transition. A
#     named agent that MUTATES declares its own full command template + wide
#     grant; its model key pins the step's model ({model} in the template), so
#     per-step model selection is pure configuration.
#   - Per-GATE model: name a second reviewer binding and allocate it by name
#     (a step's reviewer_agent:, or a gate section's agent:). The binding is the
#     source of command template, tools AND model, so a gate on a different
#     model is a second [<name>] section — not an attribute on the route. See
#     satelle help agent-dispatch and the satelle-route-standard principle.
# role= is the binding's declared contract (reviewer | agent); inference from
# the section name is a fallback, not the norm — declare it.
#
# Define process/step agents HERE (a [<name>] binding + an agent: <name> step),
# NEVER in a harness-specific agent dir (e.g. .claude/agents): those are invisible
# to satelle — it cannot see, validate, dispatch, or carry them repo-agnostically —
# and they silently pin the repo to one CLI vendor. See "satelle help agent-dispatch".
#
# TRANSPORT (epic:agent-dispatch-transport): interface = "command" | "acp".
# Omit interface (or set "command") for a FULL multi-token command template — any
# CLI (Claude Code, grok -p, wrappers). Optional interface = "acp" for an
# ACP-capable spawn only (e.g. command = "grok agent stdio"); system/payload ride
# the protocol, not {placeholders}. Claude has no ACP — keep it on command.
# Defaults in this scaffold stay command; do not flip [reviewer] to acp here.
#
# THE COMMAND TEMPLATE (interface=command): an isolated binding requires a FULL
# multi-token command (binary + argv). The only bare single-token value is
# "in-loop" (the driving session performs the step — no subprocess). An
# empty/omitted command on [reviewer] inherits the full default claude template.
# Bare tokens like "claude" / "grok" are rejected by satelle agent
# validate — run satelle init to expand a legacy bare preset, or write the full
# argv yourself. Placeholders — each one argv token: {system} {tools} {model}
# {effort} {settings} {payload}. The work-item body is ALWAYS also written on stdin
# (dual delivery). Empty {model}/{effort}/{settings} drop that flag; empty {payload}
# does not. effort= pins reasoning/thinking level (e.g. high). secondary= names a
# fallback binding for one retry on rate-limit/unavailable (or set [defaults]
# secondary = "…"). See satelle help agent-dispatch.
# Claude's default uses stdin only (no -p {payload}) so the prompt is not
# double-fed; argv-first CLIs (grok) opt in with -p {payload}. Example:
#   command = "grok -p {payload} --system-prompt-override {system} --tools read_file,grep,list_dir --always-approve --output-format plain --max-turns 8 --no-subagents"
# Optional ACP example (not enabled by default):
#   # interface = "acp"
#   # command   = "grok agent stdio"
#
# PER-BINDING ENV — point a step at an alternate model backend WITHOUT a wrapper
# binary. A binding may set env = { KEY = "value" }; each value may reference the
# [vars] KV via ${NAME}. The resolved env is layered onto the dispatched agent's
# process (binding keys win). Put SECRETS (API keys) under [vars] in the gitignored
# satelle.local.toml — NEVER here (this file is committed). Example — GLM via its
# Anthropic-compatible endpoint, same claude CLI, model reads naturally:
#   [planner]
#   command = "claude -p --append-system-prompt {system} --allowedTools {tools} --model {model}"
#   model   = "glm-4.6"
#   env     = { ANTHROPIC_BASE_URL = "https://api.z.ai/api/anthropic", ANTHROPIC_AUTH_TOKEN = "${GLM_API_KEY}" }
# and in satelle.local.toml (gitignored):
#   [vars]
#   GLM_API_KEY = "sk-…"
#
# REUSING A MACHINE-WIDE PROFILE (profile=) — optional. An operator working across
# several repos can define reusable EXECUTION profiles once in ~/.satelle/agents.toml
# (see "satelle agent profiles" / "satelle agent migrate") and have a binding here
# name one:
#   [reviewer]
#   profile = "claude-opus"     # explicit reference — inherits command/tools/model/…
#   effort  = "low"             # anything stated here still WINS over the profile
# The reference is always explicit: a profile that merely SHARES this section's name
# is never merged in, so a repo with no profile= resolves identically whether or not
# the machine has a catalog. Precedence: repo inline > referenced profile > an opt-in
# [defaults] use_global_roles role default > satelle's embedded fallback. The catalog
# is execution configuration ONLY — workflows and skills stay repo substrate here.
#
# DEFAULT LIVE TRANSPORT (epic:model-selection child 2) — a binding with no
# interface= that is opened as a LIVE session (a rework relay seat, a
# rework.consult binding) resolves to the first
# transport below that can actually open its AUTHORED command: stream only for
# a Claude command whose argv really parses as a live stream session (a
# one-shot command, e.g. one carrying --append-system-prompt {system}, does
# not — {system}/{payload} ride the live protocol, not argv); acp only for any
# other spawn line that parses as an ACP spawn. A binding with no command yet
# is a candidate only for a transport with a shipped default line to fill in
# (stream has one; acp has none — it needs an authored spawn line satelle
# cannot guess), so an unauthored command never resolves to acp regardless of
# this order. A one-shot dispatch (gate reviewer, planner, edge advisor)
# always resolves to command, unaffected by this order. An explicit interface=
# always wins over both.
# [defaults]
# live_interfaces = ["stream", "acp"]   # shipped default — list only "acp" (or
#                                        # only "stream") to exclude a
#                                        # transport; mechanism decides which
#                                        # one an authored command can serve,
#                                        # not list order
# idle_timeout    = "5m"                # shipped default when a binding sets
#                                        # none of its own — a dispatch is
#                                        # STALLED (and stopped) only after
#                                        # this long with NO real event (tool
#                                        # start/end, message, usage) —
#                                        # heartbeats alone never reset it. A
#                                        # binding overrides with its own
#                                        # idle_timeout = "…" below.
#
# TIMEOUT vs IDLE_TIMEOUT (sty_752c4ef2): idle_timeout is what actually bounds
# a dispatch — a progressing agent is never cut off by elapsed time alone.
# timeout= is an OPTIONAL hard ceiling on one dispatch's total wall-clock time,
# UNSET by default; set it on a binding only when you want a genuine upper
# bound regardless of progress (e.g. [coder] timeout = "30m"). A stall is
# ledgered and refused as its own outcome, "stalled" — distinct from a
# timeout= ceiling firing.

[executor]
role    = "agent"              # declared contract (agent | reviewer); do not leave inferred
command = "in-loop"            # the orchestrator/driving session itself

[reviewer]
# The FULL command template — transparent and swappable (sty_892517e7): satelle
# substitutes {system}/{tools}/{model}/{settings}/{payload} (each one argv token);
# payload is always also on stdin. An empty model drops the --model pair. Point
# this at ANY agent CLI by rewriting the multi-token command. Bare single-token
# presets (claude/grok) are not accepted — the real argv must be literal.
role    = "reviewer"           # declared contract; inference is a fallback, not the norm
command = "REVIEWER_COMMAND_TEMPLATE"
tools   = "Read,Grep,Glob"     # read-only grant — widen at your own risk (claude template default; a grok full template bakes its own grok-named read-only grant)
model   = ""                   # empty is selected deliberately, not left to the CLI's own default (sty_7069bced): binding > step/agent > inherited in-loop session > story creator's session > the first entry of this executable's [model_order] list > cli-default, each recorded as the source next to the resolved id. Each binding may still pin its own (e.g. "sonnet"). A workflow gate/node model="…" overrides this for that gate only without a second binding (sty_19456622). See "satelle help agent-dispatch" § Model selection.

MODEL_ORDER_BLOCK
# A named EXECUTOR agent for isolated mutating steps (e.g. a commit/push step),
# with an explicit full command template and a wide grant:
# [commit-agent]
# role    = "agent"
# command = "claude -p --append-system-prompt {system} --allowedTools {tools}"
# tools   = "Read,Edit,Bash(git:*),Bash(gh:*),Bash(make:*),Bash(satelle:*)"
#
# WHO PERFORMS A STEP — the driving session is the in-loop executor: the shipped
# route's coded step is agent = "executor", so that session performs in_progress,
# integration, release and the close itself. Nothing below is required.
#
# An optional ONE-SHOT coder a repo may point the coded step's agent= at (no
# interface = "stream"; it runs once per dispatch):
#
# [coder]
# role    = "agent"
# command = "claude -p --append-system-prompt {system} --allowedTools {tools} --model {model} --effort {effort}"
# tools   = "Read,Grep,Glob,Edit,Write,Bash(satelle:*)"
#
# The only live relay is satelle story rework: off unless a step sets
# rework = { consult, rounds }, and its consult binding must be live-capable
# (interface = "acp" or "stream", with an authored command= or profile=).
`)

// withModelOrder appends the scaffold [model_order] block to an existing
// agents.toml that does not declare the table, and records it in notes. Only the
// lists for executables in uses (the programs the repo's bindings spawn) are
// seeded; when none survives, nothing is appended. A file that already has the
// table is returned untouched, so an operator's edited order is never
// overwritten.
func withModelOrder(content string, uses map[string]bool, notes []string) (string, []string) {
	for _, ln := range strings.Split(content, "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "[model_order]") {
			return content, notes
		}
	}
	block, ok := renderModelOrder(func(exe string) bool { return uses[exe] })
	if !ok {
		return content, notes
	}
	sep := "\n"
	if !strings.HasSuffix(content, "\n") {
		sep = "\n\n"
	}
	return content + sep + block, append(notes, "added [model_order]")
}

// scaffoldModelOrders are the seeded [model_order] lists, one per executable, in
// the order they are written (sty_4fde0a50). They are the single source of the
// seeded lists: the names are configuration, and the ids come from captured
// fixtures under internal/agentcli/testdata/usage (grok ACP modelId) and the
// claude CLI's own aliases.
var scaffoldModelOrders = []struct{ exe, line string }{
	{"claude", `claude = ["opus", "sonnet", "haiku"]`},
	{"grok", `grok   = ["grok-4.7"]`},
}

// renderModelOrder renders the [model_order] block with the lists keep admits;
// ok is false when it admits none.
func renderModelOrder(keep func(exe string) bool) (block string, ok bool) {
	var b strings.Builder
	b.WriteString(scaffoldModelOrderHeader)
	for _, o := range scaffoldModelOrders {
		if keep(o.exe) {
			b.WriteString(o.line + "\n")
			ok = true
		}
	}
	return b.String(), ok
}

// scaffoldModelOrderToml is the full [model_order] block init writes into a
// fresh agents.toml — every seeded list, as the agent-dispatch help lists them.
var scaffoldModelOrderToml, _ = renderModelOrder(func(string) bool { return true })

const scaffoldModelOrderHeader = `# [model_order] — the order an UNSET model follows (sty_4fde0a50), one list per
# executable (claude, grok). A binding that leaves model= empty and has no
# same-executable inherited or creator session model takes the FIRST entry of ITS
# OWN executable's list; a name from one list is never applied to another
# executable. An entry is a name, or an array of names that are one rank (an
# alias and its resolved id, e.g. ["opus", "claude-opus-5-5"]); the first name is
# applied. An executable with no list falls back to the CLI's own default. A
# binding's model= pin always wins. See "satelle help agent-dispatch" § Model selection.
[model_order]
`

// scaffoldConstitution is the project-constitution template a fresh init writes to
// .satelle/constitution.md — the order-zero doc injected into every session
// (epic:session-context). It is a starting point the operator rewrites for THIS
// repo; kept short so the session budget stays lean. Re-init never clobbers it.
const scaffoldConstitution = `---
type: constitution
title: Project constitution
description: The local/repo definition the agent reads as order-zero context, injected every session. Rewrite this for your repo.
---

# Project constitution

<!-- This is your repo's order-zero context — injected into every session. Keep it
short and high-signal: what an agent must know to work in THIS repo. Replace this
placeholder. -->

- **What this repo is:** …
- **Ground rules:** …
- **Where the process lives:** authored substrate under ` + "`.satelle/`" + ` (workflows,
  principles, skills) — edited without a binary release.
`

// gitignoreMarker opens the managed block ensureGitignore maintains.
const gitignoreMarker = "# >>> satelle (managed) >>>"

// gitignoreMarkerEnd closes the managed block. satelle adds lines inside the
// markers, just before this one. The one rewrite it makes is of a block in a
// retired shape (retireManagedBlock): it drops its own former wholesale
// .satelle/ ignore and the retired header, keeping every other rule. Lines
// outside the markers are never touched.
const gitignoreMarkerEnd = "# <<< satelle (managed) <<<"

// gitignoreBlockHeader is the managed block's comment: git is the substrate's
// home, and only the machine-local state listed below it is ignored.
const gitignoreBlockHeader = `# .satelle/ is this repo's process substrate (workflows, skills, principles,
# constitution, satelle.toml, tasks, documents) and is tracked in git; only the
# machine-local state below is ignored. The re-include overrides a machine-wide
# core.excludesFile that ignores .satelle/. satelle adds only entries this file
# does not already settle; any rule written here or elsewhere is kept.
# Runtime state lives under ~/.satelle/<repo-key>/, outside the repo.
`

// gitignoreBlockWrap wraps lines in a fresh managed block. The entries come
// from gitignoreNeeded, the one statement of what satelle needs.
func gitignoreBlockWrap(lines []string) string {
	var b strings.Builder
	b.WriteString(gitignoreMarker + "\n" + gitignoreBlockHeader)
	for _, l := range lines {
		b.WriteString(l + "\n")
	}
	return b.String() + gitignoreMarkerEnd + "\n"
}

// satelleTrackedSubstrateIgnores is the tracked-substrate shape: the re-include
// of .satelle/ first, then the machine-local and runtime state under it.
var satelleTrackedSubstrateIgnores = []string{
	// re-include over a machine-wide core.excludesFile that ignores .satelle/
	"!.satelle/",
	".satelle/**/.env",
	// the symlink pointer to the home-keyed runtime logs
	".satelle/logs",
	// regenerated by init; the harness configs point at them
	".satelle/hooks",
	".satelle/deployed.version",
	// satelle.local.toml and every other machine-local override
	".satelle/*.local.toml",
	// this machine's agents override (command paths, profile= into ~/.satelle)
	".satelle/workflows/agents.local.toml",
	// the repo-local pinned binary (satelle update --local)
	".satelle/satelle",
	// task execution records and their run-output docs
	".satelle/tasks/*/exe_*.md",
	".satelle/tasks/*/output-exe_*.md",
}

// gitignoreNeeded is the one statement of what satelle needs in the repo's
// .gitignore: the lines missing from text, judged by what text already settles
// (never by byte-equality of a managed block). Every path — the first scaffold
// (text empty), re-runs, migrate's plan — goes through it, so a repo that already
// holds the needed set yields nothing and its .gitignore is left untouched.
//
//   - the tracked-substrate shape (satelleTrackedSubstrateIgnores): the
//     .satelle/ process substrate is tracked in git, so .satelle/ itself is never
//     added; each line is added only when no rule already targets its path. An
//     operator's own wholesale .satelle ignore stays as written (init and doctor
//     report it — gitignoreOwnSatelleIgnore).
//   - each harness dir satelle deploys into that is present in the repo
//     (substrateLockFootprint), as <dir>/ — unless the file already has a rule or
//     negation for it or git tracks files under it: the operator may commit it.
//   - the story-dump globs the edit gate exempts (managedEditExemptGlobs).
func gitignoreNeeded(text, repoRoot string) []string {
	var need []string
	for _, l := range satelleTrackedSubstrateIgnores {
		if target, _, _ := gitignoreRuleTarget(l); !gitignoreHasRule(text, target) {
			need = append(need, l)
		}
	}
	for _, e := range harnessFootprint() {
		d := strings.TrimSuffix(e, "/")
		if d == ".gitignore" {
			continue
		}
		if fi, err := os.Stat(filepath.Join(repoRoot, d)); err != nil || !fi.IsDir() {
			continue
		}
		if !gitignoreHasSetting(text, d) && !gitListed(repoRoot, "ls-files", "--", d) {
			need = append(need, d+"/")
		}
	}
	// agent story-reference dumps (edit-gate exempt via [gate] edit_exempt_globs)
	for _, g := range managedEditExemptGlobs {
		if !gitignoreHasRule(text, g) {
			need = append(need, g)
		}
	}
	return need
}

// ensureWorkspaceRegistration registers repoRoot in the machine-local workspace
// registry (gc.Workspace — the connected-repo list for /workspace and multi-serve).
// Non-fatal on every failure path: a machine whose global config is unreadable or
// unwritable still gets a fully initialized repo (sty_3bdbdc38). noWorkspace opts out.
// Always ends with a greppable membership line (sty_805bee9c AC3):
//
//	workspace: member (N repos registered)
//	workspace: not-member — join with `satelle workspace add`
func ensureWorkspaceRegistration(out io.Writer, repoRoot string, noWorkspace bool) {
	// Identity is the MAIN working tree: running `satelle init` in a linked
	// worktree must not add a second project row for a repository that is already
	// registered, and must not relabel its mirror partition after the worktree's
	// directory name (sty_cd219594).
	abs, absErr := filepath.Abs(config.CanonicalRepoRoot(repoRoot))
	if noWorkspace {
		fmt.Fprintln(out, "  = workspace registry (skipped: --no-workspace)")
		printWorkspaceMembership(out, abs, absErr)
		return
	}
	if absErr != nil {
		fmt.Fprintf(out, "  ! workspace registry (skipped: resolve path: %v)\n", absErr)
		printWorkspaceMembership(out, "", absErr)
		return
	}
	// A repo inside the scratch tree is reaped with it; registering it would
	// leave a dangling registry entry behind (sty_e3a1c1d5).
	if config.IsScratchPath(abs) {
		fmt.Fprintf(out, "  = workspace registry (skipped: scratch repo under %s)\n", config.ScratchRoot())
		printWorkspaceMembership(out, "", nil)
		return
	}
	gc, err := config.LoadGlobal()
	if err != nil {
		fmt.Fprintf(out, "  ! workspace registry (skipped: %v)\n", err)
		printWorkspaceMembership(out, abs, err)
		return
	}
	if !gc.Workspace.AddRepo(abs) {
		fmt.Fprintf(out, "  = workspace registry (already registered: %s)\n", abs)
		printWorkspaceMembership(out, abs, nil)
		return
	}
	if err := config.SaveGlobal(gc); err != nil {
		fmt.Fprintf(out, "  ! workspace registry (registration skipped: %v)\n", err)
		printWorkspaceMembership(out, abs, err)
		return
	}
	fmt.Fprintf(out, "  + workspace registry (registered %s)\n", abs)
	printWorkspaceMembership(out, abs, nil)
}

// printWorkspaceMembership re-reads the registry and prints the stable
// member / not-member line (agent-readable; sty_805bee9c). When the repo is
// registered but the machine endpoint is disabled (SATELLE_SERVER_ENDPOINT=none),
// the line still greps as "workspace: member" but qualifies that registry-only
// is not a landing join (sty_0122610a AC3) — init stays offline (no probe).
func printWorkspaceMembership(out io.Writer, abs string, priorErr error) {
	if priorErr != nil || abs == "" {
		fmt.Fprintln(out, "workspace: not-member — join with `satelle workspace add`")
		return
	}
	gc, err := config.LoadGlobal()
	if err != nil {
		fmt.Fprintln(out, "workspace: not-member — join with `satelle workspace add`")
		return
	}
	for _, r := range gc.Workspace.Repos {
		if r == abs {
			if gc.Service.ResolveEndpoint() == "" {
				fmt.Fprintf(out, "workspace: member (%d repos registered) — registry only; seed the mirror with `satelle workspace add`\n", len(gc.Workspace.Repos))
				return
			}
			fmt.Fprintf(out, "workspace: member (%d repos registered)\n", len(gc.Workspace.Repos))
			return
		}
	}
	fmt.Fprintln(out, "workspace: not-member — join with `satelle workspace add`")
}

// ensureGitignore adds to the repo's .gitignore the entries satelle needs that
// the file does not already settle (gitignoreNeeded) and returns the lines it
// added; nil means no line was added (sty_dbc0be85). It never rewrites,
// removes or reorders a line outside the managed block; inside it, only a
// retired block is rewritten (retireManagedBlock).
//   - no file → create it with a managed block of the needed lines
//   - no markers → append a managed block of the needed lines
//   - markers present → insert the needed lines just before the end marker
func ensureGitignore(repoRoot string) ([]string, error) {
	path := filepath.Join(repoRoot, ".gitignore")
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("init: read %s: %w", path, err)
	}
	next, added, changed := convergeGitignore(string(raw), repoRoot)
	if !changed {
		return nil, nil
	}
	if werr := os.WriteFile(path, []byte(next), 0o644); werr != nil {
		return nil, fmt.Errorf("init: write %s: %w", path, werr)
	}
	return added, nil
}

// convergeGitignore returns in with a retired managed block refreshed
// (retireManagedBlock) and the missing needed lines added. Pure over in apart
// from the git probes gitignoreNeeded makes in repoRoot. changed is false when
// the block is in the current shape and nothing is missing: the wording of a
// current managed block is never compared.
func convergeGitignore(in, repoRoot string) (next string, added []string, changed bool) {
	base, _ := retireManagedBlock(in)
	missing := gitignoreNeeded(base, repoRoot)
	if len(missing) == 0 {
		return base, nil, base != in
	}
	add := strings.Join(missing, "\n") + "\n"
	start := strings.Index(base, gitignoreMarker)
	if start < 0 {
		body := base
		if body != "" && !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		if body != "" {
			body += "\n"
		}
		return body + gitignoreBlockWrap(missing), missing, true
	}
	if endRel := strings.Index(base[start:], gitignoreMarkerEnd); endRel >= 0 {
		end := start + endRel
		head := base[:end]
		if !strings.HasSuffix(head, "\n") {
			head += "\n"
		}
		return head + add + base[end:], missing, true
	}
	// A start marker with no end: extend the block to EOF and close it.
	body := base
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return body + add + gitignoreMarkerEnd + "\n", missing, true
}

// retiredGitignoreWording is header wording of an earlier managed block, which
// called .satelle git-optional with hosted rehydrate as its recovery path.
var retiredGitignoreWording = []string{"git-optional", "rehydrate"}

// retireManagedBlock rewrites a managed block in a retired shape — one that
// ignores .satelle wholesale or whose comments carry retiredGitignoreWording —
// to the current header over the rules it held, minus the wholesale .satelle
// ignore. It returns in unchanged when the block is current or absent. removed
// lists the dropped wholesale ignore lines. Only the block is rewritten; the
// block is satelle's, every line outside it is the operator's.
func retireManagedBlock(in string) (out string, removed []string) {
	start := strings.Index(in, gitignoreMarker)
	if start < 0 {
		return in, nil
	}
	bodyStart := start + len(gitignoreMarker)
	end, tail := len(in), ""
	if endRel := strings.Index(in[bodyStart:], gitignoreMarkerEnd); endRel >= 0 {
		end = bodyStart + endRel
		tail = strings.TrimPrefix(in[end+len(gitignoreMarkerEnd):], "\n")
	}
	retired := false
	var kept []string
	for _, l := range strings.Split(in[bodyStart:end], "\n") {
		t := strings.TrimSpace(l)
		switch {
		case isSatelleWholesaleIgnore(t):
			removed = append(removed, t)
			retired = true
		case strings.HasPrefix(t, "#"):
			for _, w := range retiredGitignoreWording {
				retired = retired || strings.Contains(t, w)
			}
		case t != "":
			kept = append(kept, t)
		}
	}
	if !retired {
		return in, nil
	}
	return in[:start] + gitignoreBlockWrap(kept) + tail, removed
}

// gitignoreNeedsConverge reports whether .gitignore lacks an entry satelle
// needs (a missing file lacks them all) or holds a retired managed block. Used
// by migrate plan reporting and by init --all to list a repo STALE.
func gitignoreNeedsConverge(repoRoot string) bool {
	raw, _ := os.ReadFile(filepath.Join(repoRoot, ".gitignore"))
	_, _, changed := convergeGitignore(string(raw), repoRoot)
	return changed
}

// dirReadme describes what each authored dir should contain — written as the
// dir's README.md so the skeleton is self-documenting (and the README keeps the
// otherwise-empty dir tracked).
var dirReadme = map[string]string{
	"documents":  "# documents\n\nFree-form knowledge documents in the Open Knowledge Format (OKF):\nplain markdown with YAML frontmatter carrying a required `type`. Drop reference\nnotes, designs, and commit summaries here; `index.md`/`log.md` are reserved.\n",
	"workflows":  "# workflows\n\nThe lifecycle, as a DERIVED ROUTE in two halves. `done.toml` declares what DONE\nmeans: one table per story category (`*` governs the rest), its obligations in\norder, and the park/cancel states. `step.toml` is the step catalogue: each table\nis keyed by the OBLIGATION it discharges and declares the `status` an item holds\nthere, what it `requires`, its `agent`, `skills`, and the `reviewers` gating\nENTRY to it; `[[gate]]` entries are always-on reviewers.\n\nAn obligation names a step by that step's TABLE KEY, never by its `status` — a\nstatus is a stage name, and steps deliberately share them. That is the one rule\nthe format itself does not make obvious.\n\nThe binary owns ORDER (a topological sort of the prerequisites) and topology\n(cancel, park, backward movement) — they are never authored.\n\n`agents.toml` sits here too: it binds the agent and gate names those files use to\nwhat actually runs them.\n\nOptional on a performing step: `rework = { consult = \"<binding>\", rounds = N }`\nopens a bounded coder↔consultant relay via `satelle story rework`. Absent means\noff — a step without the key behaves exactly as today. The gate reviewer still\nruns cold and one-shot; the consultant's READY is a signal, never a verdict.\n\nThe shipped route is order zero: edit these two files rather than authoring a\nlifecycle from scratch. Run `satelle workflow validate` after an edit — it parses\nboth halves and prints the effective gate/model table, which is the fastest way\nto see what your change did. See also `satelle help workflows` and\n`satelle story route <id>`.\n",
	"principles": "# principles\n\nAuthored principles (markdown, `type: principle`). They are resolvable on demand;\nthe single always-resident operating principle is injected at session start.\n",
	"skills":     "# skills\n\nAuthored skills (`type: skill`): executor rubrics, reviewer rubrics, or a\nself-contained functional check (a fenced ```check block or a `check:` key).\nEverything a reviewer needs lives inside the skill.\n",
	"stories":    "# stories\n\nPer-story attachments live here under `<id>/…` (typed documents attached to a\nstory). The per-repo database is the sole story store — there is no markdown\nmirror of the backlog.\n",
	"tasks":      "# tasks\n\nAuthored task HEADERS (`tsk_*.md`, `type: task`): re-runnable work-definitions\nthat declare an ACTION and how success is VERIFIED. The file is the source of\ntruth; the DB indexes it. Each RUN is an execution under a per-task folder\n`<tsk_id>/exe_*.md`; create one with `satelle execution create --parent <tsk_id>`.\nDispose of a superseded header with `satelle task archive <tsk_id>`: it marks the\nrecord archived (dropped from the default `task list`, still readable via `task\nget`) and MOVES the header + its executions to `.satelle/backups/tasks/<ts>/<id>/`\n— archive is record disposition, distinct from workflow status.\n",
}

// ensureReadme writes a dir's README.md (describing its contents) when absent.
func ensureReadme(dir, kind string) (bool, error) {
	path := filepath.Join(dir, "README.md")
	if fileExists(path) {
		return false, nil
	}
	body := dirReadme[kind]
	if body == "" {
		body = "# " + kind + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return false, fmt.Errorf("init: write %s: %w", path, err)
	}
	return true, nil
}

// defaultSolutionWorkflows is the embedded default solution rebase deploys into
// a repo as EDITABLE substrate: the two halves of the shipped DERIVED ROUTE
// (sty_3795e7f6). done.toml declares the obligations per category — one working
// lane, the parent/epic container close, and the task-execution run — and
// step.toml declares the steps and always-on gates that discharge them. A repo
// edits its lifecycle FROM them rather than authoring one from scratch.
//
// The two are seeded BOTH-OR-NEITHER: one half is not a route
// (wfgovern.RouteSource.Present), and half a route on disk would shadow the
// shipped pair with something that does not resolve. Seeding is collision-safe —
// the overlappingCategory guard below skips the pair when an authored workflow
// already claims a category the route declares — and the embedded copies still
// govern as order zero through the doc index's read-time overlay when a repo has
// no workflow on disk at all.
var defaultSolutionWorkflows = []string{
	wfgovern.RouteSourceDone,
	wfgovern.RouteSourceStep,
}

// referencedSkills returns every skill a workflow names — node prompts and edge
// gates — deduped.
func referencedSkills(spec wfdot.Spec) []string {
	set := map[string]bool{}
	for _, s := range spec.States {
		if s.Skill != "" {
			set[s.Skill] = true
		}
	}
	for _, tr := range spec.Transitions {
		// Full CSV list (sty_814ad29a / multi-reviewer edges) — not only first.
		if len(tr.Skills) > 0 {
			for _, sk := range tr.Skills {
				if sk != "" {
					set[sk] = true
				}
			}
			continue
		}
		if tr.Skill != "" {
			set[tr.Skill] = true
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	return out
}

// authoredWorkflowCategories returns the set of applies_to categories declared
// by the on-disk (authored) workflows in wfDir. Used to skip seeding a default
// workflow that would duplicate a category an authored workflow already claims
// at the same precedence (sty_f6bd6f84).
func authoredWorkflowCategories(wfDir string) map[string]bool {
	claimed := map[string]bool{}
	entries, err := os.ReadDir(wfDir)
	if err != nil {
		return claimed
	}
	for _, e := range entries {
		// The route source is TOML (sty_81bb0dde); an authored graph, if any is
		// left, is markdown. Both must be seen here or a converted repo looks like
		// it claims nothing.
		if e.IsDir() || !docindex.Indexable(e.Name()) ||
			strings.EqualFold(e.Name(), "README.md") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(wfDir, e.Name()))
		if err != nil {
			continue
		}
		// A DERIVED route claims its categories through done.toml's `[[category]]`
		// entries, not through applies_to — which it deliberately does not carry.
		// Without this, converting a repo would make it look like it claimed
		// NOTHING, and init would re-seed the embedded DOT defaults on top of the
		// route that replaced them (sty_9835070d).
		name := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		if name == wfgovern.RouteSourceDone {
			for _, c := range wfgovern.RouteCategories(string(body)) {
				claimed[c] = true
			}
			continue
		}
		if name == wfgovern.RouteSourceStep {
			continue
		}
		for _, c := range appliesToCategories(string(body)) {
			claimed[c] = true
		}
	}
	return claimed
}

// overlappingCategory returns the first category the workflow body declares that
// is already present in claimed, or "" when none overlaps. A DERIVED route
// declares its categories through done.toml's `## <category>` sections rather than
// applies_to, so both forms are read — otherwise the shipped route would look
// like it claimed nothing and would seed straight on top of an authored graph
// (sty_3795e7f6).
func overlappingCategory(body string, claimed map[string]bool) string {
	cats := appliesToCategories(body)
	if len(cats) == 0 {
		cats = wfgovern.RouteCategories(body)
	}
	for _, c := range cats {
		if claimed[c] {
			return c
		}
	}
	return ""
}

// appliesToCategories extracts the applies_to categories from a workflow's
// frontmatter, handling the inline flow form (applies_to: ["*", "web"]) and the
// block list form (applies_to: then subsequent "- web" lines).
func appliesToCategories(body string) []string {
	lines := strings.Split(body, "\n")
	var out []string
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if !strings.HasPrefix(t, "applies_to:") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(t, "applies_to:"))
		if rest != "" {
			for _, part := range strings.Split(strings.Trim(rest, "[]"), ",") {
				if c := strings.Trim(strings.TrimSpace(part), `"'`); c != "" {
					out = append(out, c)
				}
			}
			break
		}
		// Block list form: consume following "- item" lines.
		for _, bl := range lines[i+1:] {
			bt := strings.TrimSpace(bl)
			if strings.HasPrefix(bt, "- ") {
				if c := strings.Trim(strings.TrimSpace(bt[2:]), `"'`); c != "" {
					out = append(out, c)
				}
				continue
			}
			if bt == "" {
				continue
			}
			break
		}
		break
	}
	return out
}

// embeddedDefault returns the body of the embedded canonical artifact for
// (kind, name), if any.
func embeddedDefault(kind, name string) (string, bool) {
	for _, d := range config.EmbeddedDefaults() {
		if d.Kind == kind && d.Name == name {
			return d.Body, true
		}
	}
	return "", false
}

// initLine renders a one-line report: "+ created" or "= present".
func initLine(created bool, what string) string {
	if created {
		return "  + " + what
	}
	return "  = " + what + " (already present)"
}

// ensureDir creates dir (and parents) if absent. Returns whether it created it.
func ensureDir(dir string) (bool, error) {
	switch _, err := os.Stat(dir); {
	case err == nil:
		return false, nil
	case os.IsNotExist(err):
		if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
			return false, fmt.Errorf("init: create %s: %w", dir, mkErr)
		}
		return true, nil
	default:
		return false, fmt.Errorf("init: stat %s: %w", dir, err)
	}
}

// fileExists reports whether path exists.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// routeSourceSkills harvests every skill a route source names — a step's
// executor rubrics and entry reviewers, an always-on gate, a park/cancel gate.
// It is tolerant on purpose: a body that is not a route source parses to
// nothing, so one call covers both halves and every other workflow file
// (sty_9835070d).
func routeSourceSkills(body string) []string {
	// A body carrying a fenced graph is not a route source. The guard is textual
	// because there is no DOT parser left to ask (sty_d953c5d8); it keeps a
	// leftover graph's prose from being read as route grammar by accident.
	if strings.Contains(body, "```dot") {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(names ...string) {
		for _, n := range names {
			if n != "" && !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	if lists, err := wfdot.ParseDone(body); err == nil {
		for _, l := range lists {
			add(l.ParkGate, l.CancelGate)
		}
	}
	if cat, err := wfdot.ParseSteps(body); err == nil {
		for _, st := range cat.Steps {
			add(st.Skills...)
			add(st.Reviewers...)
		}
		for _, g := range cat.Gates {
			add(g.Skill)
		}
	}
	return out
}

// embeddedExt is a shipped default's file extension, defaulting to markdown for
// a default authored before Ext existed. The route source is TOML
// (sty_81bb0dde), so a hardcoded ".md" here would silently stop converging an
// authored copy of it.
func embeddedExt(ext string) string {
	if strings.TrimSpace(ext) == "" {
		return ".md"
	}
	return ext
}
