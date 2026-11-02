package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/bobmcallan/satelle/internal/buildinfo"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/health"
	"github.com/bobmcallan/satelle/internal/verb"
)

// deployedVersionName is the managed stamp of the binary version this repo was
// last init/rebase/restored against. Committed (not gitignored) so clones share
// the heal baseline.
const deployedVersionName = "deployed.version"

// writeDeployedVersion stamps dataDir/deployed.version with the running binary
// version. Returns (true, nil) when the file was created or content changed.
// Called at the end of successful init (and rebase/restore).
func writeDeployedVersion(dataDir string) (bool, error) {
	ver := strings.TrimSpace(buildinfo.Resolve().Version)
	if ver == "" || isDevVersion(ver) {
		return false, nil // never stamp a dev sentinel
	}
	path := filepath.Join(dataDir, deployedVersionName)
	body := fmt.Sprintf("satelle.version: %s\n", ver)
	if prev, err := os.ReadFile(path); err == nil && string(prev) == body {
		return false, nil
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// readDeployedVersion returns the stamped version, or "" if absent/unreadable.
func readDeployedVersion(dataDir string) string {
	b, err := os.ReadFile(filepath.Join(dataDir, deployedVersionName))
	if err != nil {
		return ""
	}
	for _, ln := range strings.Split(string(b), "\n") {
		f := strings.Fields(ln)
		if len(f) >= 2 && f[0] == "satelle.version:" {
			return strings.TrimSpace(f[1])
		}
	}
	return ""
}

// stampDataDir is the data dir whose deployed.version the drift gate, doctor
// and session advisory read. The stamp is a property of the REPOSITORY, so in a
// linked worktree it is the main working tree's .satelle (resolved through
// config.CanonicalRepoRoot — see its OWNERSHIP RULE), never the worktree's own:
// .satelle is gitignored, so a worktree never carries the stamp. In the main
// tree, or when the main tree is not initialised, it is <repoRoot>/.satelle.
func stampDataDir(repoRoot string) string {
	return filepath.Join(config.CanonicalRepoRoot(repoRoot), config.DefaultDataDir)
}

// isDevVersion reports builds that must never self-gate (local make / go run).
// scripts/build-version.sh produces the +<sha>[-dirty] form for unreleased
// make install trees so they hit the '+' rule (sty_022929ef).
func isDevVersion(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" || v == "dev" {
		return true
	}
	return strings.HasPrefix(v, "0.0.0-dev") || strings.Contains(v, "+")
}

// stampBehind reports whether a repo stamped deployed is behind binary binVer,
// so `satelle init` has a stamp to heal: an unstamped repo is behind, as the
// drift gate's unstamped refusal holds, and a dev binary never is — it never
// stamps.
func stampBehind(deployed, binVer string) bool {
	if isDevVersion(binVer) {
		return false
	}
	if strings.TrimSpace(deployed) == "" {
		return true
	}
	return verb.CmpSemverExported(deployed, binVer) < 0
}

// stampGap names what lies between a stamp stampBehind judged behind and the
// binary: the newest breaking release and whether init heals it, or none.
func stampGap(deployed, binVer string) string {
	if strings.TrimSpace(deployed) == "" {
		return "unstamped"
	}
	entries, err := verb.ChangelogRange(deployed, binVer)
	if err != nil {
		return "changelog unreadable"
	}
	healing, manual := splitBreaking(entries)
	switch {
	case len(healing) > 0:
		return "BREAKING " + healing[0].Version + ", init heals"
	case len(manual) > 0:
		return "BREAKING " + manual[0].Version + ", manual migration"
	}
	return "no breaking release"
}

// breakingWarning is the NON-refusing outcome of the drift gate: the binary is
// ahead of this repo's stamp across ### Breaking release(s) whose migrations the
// operator performs by hand, so `satelle init` cannot heal them (sty_6e143870).
// Version and Deployed are the once-per-session key; Text is what the operator
// reads, composed from the releases' own bullets.
type breakingWarning struct {
	Version  string // newest manual Breaking release in the range
	Deployed string
	Text     string
}

// checkBreakingDrift is the gatherer for the drift gate. It returns the refusal
// when the range crosses a Breaking release that declares `init-heals:` (init
// really is the heal, so stopping is safe and sufficient), and otherwise the
// warning for a range that crosses only manual-migration Breaking releases. Both
// are nil for every quiet case.
//
// The refusal (never the warning) also covers the unstamped repo: init is the
// heal there, and it is the only way the baseline gets established.
// Non-breaking version gaps do not gate. Dev builds never gate.
func checkBreakingDrift(repoRoot string) (*breakingWarning, error) {
	binVer := strings.TrimSpace(buildinfo.Resolve().Version)
	if isDevVersion(binVer) {
		return nil, nil
	}
	dataDir := stampDataDir(repoRoot)
	// Uninitialized repo: no .satelle → app.Open may still work zero-config;
	// only gate when the data dir exists (initialized).
	if st, err := os.Stat(dataDir); err != nil || !st.IsDir() {
		return nil, nil
	}
	deployed := readDeployedVersion(dataDir)
	if deployed == "" {
		// Initialized but never stamped (pre-gate repos): fail closed so the
		// operator runs init once to establish the baseline.
		return nil, fmt.Errorf(
			"satelle: this repo has no .satelle/%s stamp — run `satelle init` to align with binary %s (breaking-surface heal path)",
			deployedVersionName, binVer)
	}
	// Consult changelog for breaking entries in (deployed, binVer].
	entries, err := verb.ChangelogRange(deployed, binVer)
	if err != nil {
		// Missing changelog: do not brick — init analysis still works.
		return nil, nil
	}
	return breakingDrift(deployed, binVer, entries)
}

// breakingDrift is the DECISION half of the drift gate: given the repo's stamp,
// the running binary and the changelog entries in (deployed, binVer], return the
// warning, the refusal, or neither. Split from checkBreakingDrift so the decision
// is reachable without buildinfo — the dev short-circuit above makes every
// assertion about the whole function vacuous under `go test` (sty_b36c051c).
//
// Whether a release refuses is CONFIGURATION: it refuses only when its own
// ### Breaking section carries an `init-heals:` bullet, because only then does
// `satelle init` actually perform what the release asks. A release whose
// migrations are manual (hand edits, a jq command) cannot be healed by the
// command a refusal would send the operator to, so it warns instead and the
// operator's commands keep running. The remediation is likewise the release's
// own bullets, verbatim; there is deliberately no per-release branch here — the
// next breaking change authors its heal path in CHANGELOG.md, with no recompile.
func breakingDrift(deployed, binVer string, entries []verb.ChangelogEntry) (*breakingWarning, error) {
	if verb.CmpSemverExported(deployed, binVer) >= 0 {
		return nil, nil // binary not newer
	}
	healing, manual := splitBreaking(entries)
	switch {
	case len(healing) > 0:
		e := healing[0]
		var b strings.Builder
		fmt.Fprintf(&b,
			"satelle: binary %s is ahead of this repo's deployed stamp %s "+health.StampRefusalMarker+" %s — that release says:%s",
			binVer, deployed, e.Version, breakingBullets(e))
		for _, m := range manual {
			fmt.Fprintf(&b, "\nRelease %s also needs manual migration — it says:%s", m.Version, breakingBullets(m))
		}
		b.WriteString("\nRun `satelle init` to heal.")
		return nil, errors.New(b.String())
	case len(manual) > 0:
		var b strings.Builder
		fmt.Fprintf(&b,
			"⚠️ satelle: binary %s is ahead of this repo's deployed stamp %s "+health.StampRefusalMarker+"(s) whose migrations are manual — `satelle init` does not perform them:",
			binVer, deployed)
		for _, m := range manual {
			fmt.Fprintf(&b, "\nrelease %s says:%s", m.Version, breakingBullets(m))
		}
		b.WriteString("\nCommands still run; `satelle init` re-stamps the repo once you have migrated.")
		return &breakingWarning{Version: manual[0].Version, Deployed: deployed, Text: b.String()}, nil
	}
	return nil, nil
}

// breakingDriftFindings is the doctor's view of the same gate: one finding per
// Breaking release the repo's stamp has not caught up with, so the unacknowledged
// entry shows in `satelle doctor` whether or not a command has warned about it
// this session (sty_6e143870). Injected into doctor.Opts, like scaffoldFindings.
// Quiet for dev builds, uninitialised or unstamped repos and a missing changelog
// — the same cases checkBreakingDrift stays quiet on (unstamped refuses there).
func breakingDriftFindings(repoRoot string) health.Findings {
	binVer := strings.TrimSpace(buildinfo.Resolve().Version)
	if isDevVersion(binVer) {
		return nil
	}
	dataDir := stampDataDir(repoRoot)
	if st, err := os.Stat(dataDir); err != nil || !st.IsDir() {
		return nil
	}
	deployed := readDeployedVersion(dataDir)
	if deployed == "" {
		return nil
	}
	entries, err := verb.ChangelogRange(deployed, binVer)
	if err != nil {
		return nil
	}
	return breakingFindings(deployed, binVer, entries)
}

// breakingFindings is the pure classification behind breakingDriftFindings: an
// Error for a release that declares `init-heals:` (commands refuse until init
// runs) and a Warn for one whose migrations are manual (commands warn). The
// Detail carries the release's own bullets verbatim, as the gate's messages do.
func breakingFindings(deployed, binVer string, entries []verb.ChangelogEntry) health.Findings {
	if verb.CmpSemverExported(deployed, binVer) >= 0 {
		return nil
	}
	healing, manual := splitBreaking(entries)
	var out health.Findings
	add := func(e verb.ChangelogEntry, mk func(id, title, detail string) health.Finding, title, effect, remediation string) {
		detail := fmt.Sprintf("binary %s is ahead of deployed stamp %s "+health.StampRefusalMarker+" %s — %s; that release says: %s",
			binVer, deployed, e.Version, effect, strings.Join(e.Sections["Breaking"], "; "))
		out = append(out, mk(health.IDBreakingUnacknowledged, title, detail).About(e.Version).WithRemediation(remediation))
	}
	for _, e := range healing {
		add(e, health.Error, "Breaking release not applied", "commands refuse until `satelle init` runs",
			"run `satelle init` to heal")
	}
	for _, e := range manual {
		add(e, health.Warn, "Breaking release needs manual migration", "commands warn, `satelle init` does not migrate it",
			"perform the migration the release describes, then run `satelle init` to re-stamp")
	}
	return out
}

// breakingBullets renders an entry's ### Breaking bullets as an indented list,
// or a pointer at the changelog when the entry says nothing specific.
func breakingBullets(e verb.ChangelogEntry) string {
	bullets := e.Sections["Breaking"]
	if len(bullets) == 0 {
		return " see CHANGELOG.md ### Breaking (`satelle changelog --from <stamp>`)"
	}
	var b strings.Builder
	for _, ln := range bullets {
		b.WriteString("\n  - ")
		b.WriteString(ln)
	}
	return b.String()
}

// splitBreaking partitions the Breaking entries of a range into those that
// declare `init-heals:` (a refusal — init is the heal) and those that do not (a
// warning — the migration is manual). Entries arrive newest-first, so index 0 of
// each list is the NEWEST release of its kind — the one the refusal or warning
// names. ONE definition of "this gap crosses a breaking release", shared by the
// gate (breakingDrift) and the session-start advisory (versionDriftLine) so the
// two can never disagree about the same range.
func splitBreaking(entries []verb.ChangelogEntry) (healing, manual []verb.ChangelogEntry) {
	for _, e := range entries {
		switch {
		case !e.Breaking:
		case e.InitHeals:
			healing = append(healing, e)
		default:
			manual = append(manual, e)
		}
	}
	return healing, manual
}

// breakingWarnedName is the per-repo runtime file holding the key of the last
// Breaking warning printed, so a session sees it once, not on every command.
const breakingWarnedName = "breaking-warned"

// warnBreakingOnce prints the Breaking warning on w (stderr) the first time a
// session runs a command against this repo, and stays silent afterwards. The
// marker lives on the home-keyed runtime plane, never in the repo tree, and is
// keyed by session + stamp + release, so a new session, a re-stamp or a newer
// release warns again. One file per repo, overwritten — nothing accumulates.
//
// With no resolvable session there is nothing to scope "once" to, so it prints
// every time: repeating a warning costs a line, silencing it costs the
// operator's only notice. A marker that cannot be read or written likewise
// means the warning may repeat; it never fails the command.
func warnBreakingOnce(runtimeDir string, bw *breakingWarning, w io.Writer) {
	if bw == nil {
		return
	}
	session := config.ResolveSession()
	if session == "" {
		fmt.Fprintln(w, bw.Text)
		return
	}
	key := session + "|" + bw.Deployed + "|" + bw.Version
	path := filepath.Join(runtimeDir, breakingWarnedName)
	if prev, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(prev)) == key {
		return
	}
	fmt.Fprintln(w, bw.Text)
	if err := os.MkdirAll(runtimeDir, 0o755); err == nil {
		_ = os.WriteFile(path, []byte(key+"\n"), 0o644)
	}
}

// versionDriftAdvisory gathers the inputs versionDriftLine decides on: the
// running binary, this repo's stamp, and the changelog entries between them.
// The gatherer/decision split mirrors checkBreakingDrift/breakingDrift —
// the dev short-circuit below makes assertions about the whole function vacuous
// under `go test`, so the decision has to be reachable without buildinfo.
//
// Returns "" for every quiet case. It has NO error channel by construction:
// this feeds `satelle hook context`, and a hook that errors breaks the session
// instead of informing it.
func versionDriftAdvisory(repoRoot string) string {
	binVer := strings.TrimSpace(buildinfo.Resolve().Version)
	if isDevVersion(binVer) {
		return ""
	}
	dataDir := stampDataDir(repoRoot)
	// Uninitialised repo: nothing was ever deployed here, so there is no gap to
	// report — same guard checkBreakingDrift applies.
	if st, err := os.Stat(dataDir); err != nil || !st.IsDir() {
		return ""
	}
	deployed := readDeployedVersion(dataDir)
	// A missing/unreadable CHANGELOG diverges DELIBERATELY from the refusal path:
	// there it silences the gate (never brick a repo over a missing file), here it
	// only costs the breaking classification. The gap itself is still real and
	// still worth naming, so advise with no entries rather than going quiet.
	entries, err := verb.ChangelogRange(deployed, binVer)
	if err != nil {
		entries = nil
	}
	return versionDriftLine(deployed, binVer, entries)
}

// versionDriftLine is the DECISION half: ONE advisory line when this repo's
// stamp is behind the running binary, "" otherwise. Advisory, never a refusal.
//
// The binary is machine-wide, so `satelle update` in one repo moves every repo
// onto the new binary while each repo's stamp keeps naming the binary that last
// deployed its scaffolding. That gap is harmless until a release in the open
// range (deployed, binVer] declares ### Breaking — at which point the first
// store-backed verb fails closed, and at session start that verb is the
// SessionStart hook's own reindex. This line is the warning ahead of that
// refusal.
//
// Deliberately ONE line and no bullet list: the refusal path already prints the
// release's own ### Breaking bullets verbatim, and duplicating them here would
// be a per-session token toll in every repo. Silent when the stamp matches, for
// the same reason.
func versionDriftLine(deployed, binVer string, entries []verb.ChangelogEntry) string {
	if strings.TrimSpace(binVer) == "" {
		return ""
	}
	if deployed == "" {
		// Initialised but never stamped: the first store-backed verb REFUSES this
		// repo outright, so the unstamped case is exactly the one worth warning
		// about ahead of time.
		return fmt.Sprintf(
			"\u26a0\ufe0f satelle: binary %s — this repo has no .satelle/%s stamp, so the next store-backed verb will refuse it; run `satelle init` to establish the baseline.",
			binVer, deployedVersionName)
	}
	if verb.CmpSemverExported(deployed, binVer) >= 0 {
		return "" // stamp current (or ahead) — the common path stays silent
	}
	gap := "no breaking release in that range"
	action := "run `satelle init` to heal."
	healing, manual := splitBreaking(entries)
	switch {
	case len(healing) > 0:
		gap = "BREAKING release " + healing[0].Version + " in that range — commands refuse until init runs"
	case len(manual) > 0:
		gap = "BREAKING release " + manual[0].Version + " in that range needs manual migration — commands warn"
		action = "follow the release's ### Breaking bullets (`satelle changelog`), then run `satelle init` to re-stamp."
	}
	return fmt.Sprintf(
		"\u26a0\ufe0f satelle: binary %s is ahead of this repo's .satelle/%s stamp %s (%s) — %s",
		binVer, deployedVersionName, deployed, gap, action)
}
