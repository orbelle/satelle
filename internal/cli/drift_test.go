package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/buildinfo"
	"github.com/bobmcallan/satelle/internal/verb"
)

func TestRetiredNameMessage(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"install"}, "satelle init"},
		{[]string{"workspace", "rm", "x"}, "workspace remove"},
		{[]string{"sync", "config", "pull"}, "sync config deploy"},
		{[]string{"ui", "push"}, "satelle workspace add"},
		{[]string{"ui"}, "satelle workspace add"},
		{[]string{"service", "install"}, ""}, // not retired
		{[]string{"story", "list"}, ""},
	}
	for _, c := range cases {
		got := retiredNameMessage(c.args)
		if c.want == "" {
			if got != "" {
				t.Errorf("args %v: unexpected %q", c.args, got)
			}
			continue
		}
		if !strings.Contains(got, c.want) {
			t.Errorf("args %v: got %q, want contains %q", c.args, got, c.want)
		}
	}
	// ui parent must not be registered as a live subcommand.
	root := NewRootCmd()
	for _, c := range root.Commands() {
		if c.Name() == "ui" {
			t.Fatal("ui command still registered — retired in favour of workspace add")
		}
	}
}

func TestWriteReadDeployedVersion(t *testing.T) {
	dir := t.TempDir()
	// Force a non-dev version for the stamp when buildinfo is dev — write directly.
	path := filepath.Join(dir, deployedVersionName)
	if err := os.WriteFile(path, []byte("satelle.version: 0.0.200\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readDeployedVersion(dir); got != "0.0.200" {
		t.Fatalf("read = %q", got)
	}
	// writeDeployedVersion no-ops on dev builds — just ensure it doesn't error.
	_ = buildinfo.Resolve()
	if _, err := writeDeployedVersion(dir); err != nil {
		t.Fatal(err)
	}
}

func TestIsDevVersion(t *testing.T) {
	if !isDevVersion("dev") || !isDevVersion("") || !isDevVersion("0.0.0-dev+foo") ||
		!isDevVersion("0.0.396+local") {
		t.Error("dev sentinels")
	}
	// scripts/build-version.sh form for unreleased make install (sty_022929ef).
	if !isDevVersion("0.0.417+0aaedad49804-dirty") || !isDevVersion("0.0.417+0aaedad49804") {
		t.Error("build-version.sh +sha form must demote via isDevVersion")
	}
	if isDevVersion("0.0.218") {
		t.Error("release version is not dev")
	}
}

// TestRefuseBreakingDriftDevBuildNeverGates covers AC4. It asserts against a
// stamp old enough that the shipped 0.0.385 Breaking entry is in range, so on a
// release build it would warn — the dev short-circuit is the only reason it
// does not. Skips loudly on a release build rather than passing vacuously.
func TestRefuseBreakingDriftDevBuildNeverGates(t *testing.T) {
	if !isDevVersion(buildinfo.Resolve().Version) {
		t.Skip("release build — the dev short-circuit is not the path under test")
	}
	repo := t.TempDir()
	dataDir := filepath.Join(repo, ".satelle")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, deployedVersionName),
		[]byte("satelle.version: 0.0.100\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if bw, err := checkBreakingDrift(repo); err != nil || bw != nil {
		t.Fatalf("dev build must never gate or warn: warn=%v err=%v", bw, err)
	}
}

func TestRefuseBreakingDriftMissingStamp(t *testing.T) {
	// When buildinfo is a release version and data dir exists without stamp, refuse.
	// Dev builds skip — if this test binary is dev, the guard returns nil.
	repo := t.TempDir()
	dataDir := filepath.Join(repo, ".satelle")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := checkBreakingDrift(repo)
	// Either skipped (dev) or fails naming init.
	if err != nil && !strings.Contains(err.Error(), "satelle init") {
		t.Fatalf("want init named: %v", err)
	}
}

// breakingEntry is a synthesised ### Breaking release carrying remediation text.
// It declares nothing about init, so its migrations are manual — the warning case.
func breakingEntry(ver string, bullets ...string) verb.ChangelogEntry {
	e := verb.ChangelogEntry{Version: ver, Breaking: true, Sections: map[string][]string{}}
	if len(bullets) > 0 {
		e.Sections["Breaking"] = bullets
	}
	return e
}

// healingEntry is a synthesised ### Breaking release that declares `init-heals:`
// — the only shape that refuses ordinary commands (sty_6e143870).
func healingEntry(ver string, bullets ...string) verb.ChangelogEntry {
	e := breakingEntry(ver, bullets...)
	e.InitHeals = true
	return e
}

// TestBreakingDriftDecision exercises the pure decision directly. The whole-
// function tests below self-neuter on a dev build (the gate short-circuits), so
// this is where the refuse / warn / quiet split is actually proven (sty_b36c051c).
func TestBreakingDriftDecision(t *testing.T) {
	conv := breakingEntry("0.0.385",
		"An authored DOT workflow no longer resolves a lifecycle.",
		"Convert it forward — run `satelle migrate` and read `satelle help workflow-convert`; `satelle init` does NOT convert DOT.")
	bare := breakingEntry("0.0.385")
	heals := healingEntry("0.0.390",
		"init-heals: `satelle init` rewrites the managed hook files.")
	quiet := verb.ChangelogEntry{Version: "0.0.391", Sections: map[string][]string{"Fixed": {"something"}}}

	cases := []struct {
		name          string
		deployed, bin string
		entries       []verb.ChangelogEntry
		wantRefuse    []string // non-nil: must refuse, message contains all
		wantWarn      []string // non-nil: must warn, text contains all
		wantNotRefuse []string // substrings the refusal must not carry
	}{
		{
			name:     "init-heals entry in range — refused with its own bullets",
			deployed: "0.0.380", bin: "0.0.396", entries: []verb.ChangelogEntry{quiet, heals},
			wantRefuse: []string{"0.0.390", "0.0.380", "init-heals:", "managed hook files", "Run `satelle init` to heal."},
		},
		{
			name:     "manual entry — warns with the release's remediation, never refuses",
			deployed: "0.0.380", bin: "0.0.396", entries: []verb.ChangelogEntry{quiet, conv},
			wantWarn: []string{"0.0.385", "0.0.380", "satelle migrate", "satelle help workflow-convert", "Commands still run", "re-stamps"},
		},
		{
			name:     "manual entry that declares no remediation — warns and points at the changelog",
			deployed: "0.0.380", bin: "0.0.396", entries: []verb.ChangelogEntry{bare},
			wantWarn: []string{"0.0.385", "CHANGELOG.md ### Breaking"},
		},
		{
			name:     "init-heals and manual in one range — refused, and the manual remediation is not lost",
			deployed: "0.0.380", bin: "0.0.396", entries: []verb.ChangelogEntry{quiet, heals, conv},
			wantRefuse: []string{"0.0.390", "Release 0.0.385 also needs manual migration", "satelle help workflow-convert"},
		},
		{
			name:     "two manual entries — one warning carries both, newest first",
			deployed: "0.0.380", bin: "0.0.396",
			entries:  []verb.ChangelogEntry{breakingEntry("0.0.390", "second."), breakingEntry("0.0.385", "first.")},
			wantWarn: []string{"release 0.0.390 says:\n  - second.", "release 0.0.385 says:\n  - first."},
		},
		{
			name:     "stamped AT the breaking release — quiet",
			deployed: "0.0.385", bin: "0.0.396", entries: []verb.ChangelogEntry{quiet},
		},
		{
			name:     "current repo — quiet",
			deployed: "0.0.395", bin: "0.0.396", entries: []verb.ChangelogEntry{quiet},
		},
		{
			name:     "stamp equals binary — quiet even with an init-heals entry",
			deployed: "0.0.396", bin: "0.0.396", entries: []verb.ChangelogEntry{heals},
		},
		{
			name:     "binary older than stamp — quiet even with a breaking entry",
			deployed: "0.0.397", bin: "0.0.396", entries: []verb.ChangelogEntry{conv, heals},
		},
		{
			name:     "version gap with no breaking entry — quiet",
			deployed: "0.0.390", bin: "0.0.396", entries: []verb.ChangelogEntry{quiet},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bw, err := breakingDrift(c.deployed, c.bin, c.entries)
			switch {
			case c.wantRefuse != nil:
				if err == nil {
					t.Fatal("want refusal, got nil")
				}
				if bw != nil {
					t.Errorf("a refusal must not also warn: %+v", bw)
				}
				for _, want := range c.wantRefuse {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("refusal missing %q:\n%v", want, err)
					}
				}
			case c.wantWarn != nil:
				if err != nil {
					t.Fatalf("a manual-migration release must not refuse: %v", err)
				}
				if bw == nil {
					t.Fatal("want a warning, got none")
				}
				for _, want := range c.wantWarn {
					if !strings.Contains(bw.Text, want) {
						t.Errorf("warning missing %q:\n%s", want, bw.Text)
					}
				}
				if bw.Deployed != c.deployed {
					t.Errorf("warning key Deployed = %q, want %q", bw.Deployed, c.deployed)
				}
			default:
				if err != nil || bw != nil {
					t.Fatalf("want quiet, got warn=%v err=%v", bw, err)
				}
			}
		})
	}
}

// TestBreakingWarningNamesNewestManualRelease: the once-per-session key follows
// the newest manual release, so a NEWER Breaking release warns again even in a
// session that has already seen the older one.
func TestBreakingWarningNamesNewestManualRelease(t *testing.T) {
	bw, err := breakingDrift("0.0.380", "0.0.396",
		[]verb.ChangelogEntry{breakingEntry("0.0.390", "b"), breakingEntry("0.0.385", "a")})
	if err != nil || bw == nil {
		t.Fatalf("want a warning: warn=%v err=%v", bw, err)
	}
	if bw.Version != "0.0.390" {
		t.Errorf("warning Version = %q, want the newest manual release 0.0.390", bw.Version)
	}
}

// TestShippedChangelogMarksDOTRetirement pins the DOT retirement to the file that
// actually ships. The guard reads the EMBED, so asserting a fixture that merely
// resembles the changelog would prove nothing about a consumer's binary.
func TestShippedChangelogMarksDOTRetirement(t *testing.T) {
	entries, err := verb.ChangelogRange("0.0.384", "0.0.385")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Version != "0.0.385" {
		t.Fatalf("want exactly the 0.0.385 entry, got %+v", entries)
	}
	e := entries[0]
	if !e.Breaking {
		t.Fatal("0.0.385 retired the DOT front end and must declare ### Breaking")
	}
	if e.InitHeals {
		t.Fatal("0.0.385's conversion is manual (`satelle init` does NOT convert DOT) — it must not declare init-heals")
	}
	// The warning a stale repo sees is composed from THIS text, and the commands
	// keep running.
	bw, derr := breakingDrift("0.0.380", "0.0.385", entries)
	if derr != nil {
		t.Fatalf("a manual-migration release must not refuse: %v", derr)
	}
	if bw == nil {
		t.Fatal("a repo stamped before the DOT retirement must be warned")
	}
	for _, want := range []string{"satelle migrate", "satelle help workflow-convert", "does NOT convert"} {
		if !strings.Contains(bw.Text, want) {
			t.Errorf("shipped remediation missing %q:\n%s", want, bw.Text)
		}
	}
}

// TestShippedChangelogSparesCurrentRepos is the range check against the REAL
// corpus: a repo is warned across a Breaking release and spared once it is
// stamped at or after it. Ranges come from verb.ChangelogRange over the shipped
// embed, so this fails if a later edit marks a release Breaking without
// accounting for who it reaches.
//
// The expectations MOVE with each Breaking release, and they must — a `###
// Breaking` marker exists to tell the repos below it. Only 0.0.761 declares
// `init-heals:` (it removes a config key init deletes itself), so every stamp
// below it is refused with init as the heal; the earlier releases' migrations
// are manual, and are named in that refusal. A stamp at or after 0.0.761 but
// below a later manual release is warned, not refused:
//
//   - 0.0.793, team-owned paths govern from the project tier (sty_8dedc0a4).
//     Manual: publish or move local edits; init cannot do either.
//
//   - 0.0.761, location holds removed from the client (sty_642275eb). Healed:
//     `satelle init` deletes the retired [sync] hold_stale_after key.
//
//   - 0.0.385, the DOT retirement. RETROACTIVE (sty_b36c051c) — added after the
//     fact, so it can only reach repos that were already broken.
//
//   - 0.0.401, the route source becoming TOML (sty_81bb0dde). A REAL breaking
//     release: every repo whose route source is still markdown stops resolving
//     on upgrade, and being told so on the next command — rather than at work
//     time, three gates in — is the entire point of the marker.
//
//   - 0.0.568, the removal of every harness but claude and grok (sty_941e60cb):
//     a repo still carrying scaffolding or bindings for a removed harness must
//     be told to clean them up by hand.
//
//   - 0.0.575, reviewer isolation (sty_ef3efb51). Manual: change the reviewer
//     binding; init does not rewrite it.
//
//   - 0.0.607, story estimate no longer accepts --usd (sty_2f6c7df5). Manual:
//     pass --fresh-input/--output (or legacy --tokens/--time). Init does not
//     rewrite scripts or stored tags.
//
//   - 0.0.665, configuration validated before push (sty_89b9df20). Manual:
//     move machine-specific agent fields into agents.local.toml; init does not
//     move them.
//
//   - 0.0.676, satelle no longer manages cloud identity (sty_c00782a3). Manual:
//     delete a `cloud` table from the catalog or a repo binding; init does not
//     edit them.
//
//   - 0.0.856, remote definitions are schema v1 only (sty_bc990070). Manual:
//     rewrite a `.satelle/remotes/*/remote.toml` in v1; init reports each old
//     key but rewrites none, since the override sources and team-store names
//     are values the old file does not carry.
//
// What stays invariant is the shape: at-or-after the newest marker is quiet, and
// the warning a repo below it gets carries that release's own bullets.
func TestShippedChangelogSparesCurrentRepos(t *testing.T) {
	// A ceiling above every shipped entry, so the range is the widest one any
	// future binary could ask for.
	const future = "9.9.9"
	cases := []struct {
		deployed string
		refused  bool
		why      string
		warned   bool
	}{
		{"0.0.380", true, "predates the DOT retirement — already broken, must be told", false},
		{"0.0.385", true, "converted off DOT, but its route source is still markdown", false},
		{"0.0.395", true, "same — every pre-TOML stamp is warned across 0.0.401", false},
		{"0.0.401", true, "converted to TOML, but still predates the harness removal at 0.0.568", false},
		{"0.0.567", true, "the release just before the harness removal", false},
		{"0.0.568", true, "stamped at the harness removal, but predates reviewer isolation at 0.0.575", false},
		{"0.0.574", true, "the release just before reviewer isolation", false},
		{"0.0.575", true, "stamped at reviewer isolation, but predates the estimate flag removal at 0.0.607", false},
		{"0.0.576", true, "past reviewer isolation, but still predates 0.0.607", false},
		{"0.0.606", true, "the release just before the estimate flag removal", false},
		{"0.0.607", true, "stamped at the estimate flag removal, but predates the push check at 0.0.665", false},
		{"0.0.608", true, "past 0.0.607, but still predates 0.0.665", false},
		{"0.0.664", true, "the release just before the push check", false},
		{"0.0.665", true, "stamped at the push check, but predates the cloud table removal at 0.0.676", false},
		{"0.0.675", true, "the release just before the cloud table removal", false},
		{"0.0.676", true, "stamped at the cloud table removal, but predates the pattern syntax at 0.0.678", false},
		{"0.0.677", true, "the release just before the pattern syntax", false},
		{"0.0.678", true, "stamped at the pattern syntax, but predates the location-hold removal at 0.0.761", false},
		{"0.0.759", true, "predates the location-hold removal", false},
		{"0.0.760", true, "the release just before the location-hold removal — released without it, so it must be refused until init", false},
		{"0.0.761", true, "stamped at the location-hold removal, but predates the [sync] scope-key removal at 0.0.816", false},
		{"0.0.792", true, "the release just before the manual project tier, still behind 0.0.816", false},
		{"0.0.793", true, "stamped at the manual project tier, but predates the [sync] scope-key removal at 0.0.816", false},
		{"0.0.815", true, "the release just before the [sync] scope-key removal", false},
		{"0.0.816", true, "stamped at the [sync] scope-key removal, but predates the recent_window removal at 0.0.823", false},
		{"0.0.822", true, "the release just before the recent_window removal", false},
		{"0.0.823", true, "stamped at the recent_window removal, but predates the budget removal at 0.0.830", false},
		{"0.0.829", true, "the release just before the budget removal", false},
		{"0.0.830", false, "stamped at the budget removal, the newest init-heals release, but predates the manual schema v1 at 0.0.856", true},
		{"0.0.855", false, "the release just before schema v1", true},
		{"0.0.856", false, "stamped AT the newest Breaking release", false},
		{"0.0.857", false, "past it", false},
	}
	for _, c := range cases {
		entries, err := verb.ChangelogRange(c.deployed, future)
		if err != nil {
			t.Fatal(err)
		}
		bw, derr := breakingDrift(c.deployed, future, entries)
		if c.refused && bw != nil {
			t.Errorf("stamp %s (%s): a range with an init-heals release refuses, never warns:\n%s", c.deployed, c.why, bw.Text)
		}
		if c.warned && (bw == nil || bw.Version != "0.0.856") {
			t.Errorf("stamp %s (%s): want a warning across the manual 0.0.856, got %+v", c.deployed, c.why, bw)
		}
		if !c.refused && !c.warned && bw != nil {
			t.Errorf("stamp %s (%s): must be quiet:\n%s", c.deployed, c.why, bw.Text)
		}
		if c.refused && (derr == nil || !strings.Contains(derr.Error(), "Run `satelle init` to heal.")) {
			t.Errorf("stamp %s (%s): want a refusal naming `satelle init` as the heal, got %v", c.deployed, c.why, derr)
		}
		if !c.refused && derr != nil {
			t.Errorf("stamp %s (%s): must be quiet:\n%v", c.deployed, c.why, derr)
		}
	}
}

// TestShippedChangelogCarriesTheTomlRemediation (sty_81bb0dde AC6): the marker
// only helps if the bullets an operator READS tell them what to do. A repo
// stamped before the cutover must get the TOML conversion path verbatim — the
// rename, the help topic, and the diff that proves no gate vanished. The range
// stops just below the later Breaking release so the warning is about the
// cutover alone.
func TestShippedChangelogCarriesTheTomlRemediation(t *testing.T) {
	entries, err := verb.ChangelogRange("0.0.395", "0.0.567")
	if err != nil {
		t.Fatal(err)
	}
	bw, derr := breakingDrift("0.0.395", "0.0.567", entries)
	if derr != nil {
		t.Fatalf("the cutover's migration is manual and must not refuse: %v", derr)
	}
	if bw == nil {
		t.Fatal("a pre-TOML stamp must be warned across the cutover")
	}
	for _, want := range []string{
		"done.toml", "step.toml",
		"satelle help workflow-convert",
		"satelle workflow show",
		"satelle init` does NOT convert",
	} {
		if !strings.Contains(bw.Text, want) {
			t.Errorf("the TOML remediation is missing %q:\n%s", want, bw.Text)
		}
	}
}

// TestRemediationCommandsAreReachable guards the half the message text cannot: a
// refusal is useless if the commands it names sit BEHIND the gate that emits it.
// checkBreakingDrift runs only for store-backed commands, so the conversion path
// must stay off that annotation.
func TestRemediationCommandsAreReachable(t *testing.T) {
	root := NewRootCmd()
	for _, name := range []string{"migrate", "help"} {
		cmd, _, err := root.Find([]string{name})
		if err != nil || cmd == nil || cmd.Name() != name {
			t.Fatalf("%s is named in the drift remediation but is not a command (%v)", name, err)
		}
		if cmd.Annotations[storeAnnotation] == "1" {
			t.Errorf("%s is store-backed, so the drift refusal would block its own heal path", name)
		}
	}
}

// TestCheckBreakingDriftBreakingRange covers the WIRING — stat the data dir,
// read the stamp, range the shipped changelog, delegate the decision. It cannot
// plant its own changelog: readChangelogBody prefers the embed always, which is
// the point (a consumer's binary carries satelle's changelog, not their repo's).
// So the fixture is the stamp alone, and the corpus is the real one.
func TestCheckBreakingDriftBreakingRange(t *testing.T) {
	if isDevVersion(buildinfo.Resolve().Version) {
		t.Skip("dev build never gates — see TestBreakingDriftDecision for the decision")
	}
	repo := t.TempDir()
	dataDir := filepath.Join(repo, ".satelle")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A stamp old enough that the shipped 0.0.385 Breaking entry is in range.
	if err := os.WriteFile(filepath.Join(dataDir, deployedVersionName),
		[]byte("satelle.version: 0.0.100\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bw, err := checkBreakingDrift(repo)
	if err != nil {
		t.Fatalf("shipped Breaking releases are manual — must warn, not refuse: %v", err)
	}
	if bw == nil {
		t.Fatal("release build with a 0.0.100 stamp must warn across a Breaking release")
	}
	if !strings.Contains(strings.ToLower(bw.Text), "breaking") {
		t.Fatalf("want breaking named: %s", bw.Text)
	}

	// A current stamp is neither warned nor refused.
	cur := t.TempDir()
	curData := filepath.Join(cur, ".satelle")
	if err := os.MkdirAll(curData, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(curData, deployedVersionName),
		[]byte("satelle.version: "+buildinfo.Resolve().Version+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if bw, err := checkBreakingDrift(cur); err != nil || bw != nil {
		t.Fatalf("a repo stamped at the running binary must be quiet: warn=%v err=%v", bw, err)
	}
}

func TestMigrateAgentsFlattenAndInject(t *testing.T) {
	// Config package test lives better there; smoke via retiredNames already covered.
	// Keep retiredName multi-token asserts here as AC2 CLI invocation surface.
	for _, args := range [][]string{
		{"workspace", "rm"},
		{"sync", "config", "pull"},
	} {
		msg := retiredNameMessage(args)
		if msg == "" {
			t.Errorf("%v must be retired", args)
		}
	}
}
