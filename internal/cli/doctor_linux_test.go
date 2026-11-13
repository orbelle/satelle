//go:build linux

package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/buildinfo"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/gittest"
	"github.com/bobmcallan/satelle/internal/syncstate"
)

// doctorFixture is a repo and machine on which satelle doctor finds nothing:
// a fresh init, fake satelled and systemctl first on PATH, satelled's unit and
// the sync timer installed and active, /healthz answering the installed
// build, the installed CLI recorded at the same version, a successful
// automatic pass recorded, and a doctor.toml check that exits 0.
type doctorFixture struct {
	dir, home, bin string
	timerState     string // file the fake systemctl answers is-active from
	running        buildinfo.Info
}

var doctorBuild = buildinfo.Info{Name: "satelled", Version: "1.0.0", Commit: "aaaaaaaaaaaa", BuildTime: "2026-11-09T00:00:00Z"}

func newDoctorFixture(t *testing.T) *doctorFixture {
	t.Helper()
	f := &doctorFixture{dir: isolate(t), home: os.Getenv("HOME"), bin: t.TempDir(), running: doctorBuild}
	f.timerState = filepath.Join(f.bin, "timer-state")
	writeFile(t, f.timerState, "active\n")
	writeExec(t, filepath.Join(f.bin, "systemctl"), "#!/bin/sh\ncase \"$*\" in *is-active*) cat "+f.timerState+";; esac\n")
	satelled := filepath.Join(f.bin, "satelled")
	writeExec(t, satelled, "#!/bin/sh\necho '"+doctorBuild.String()+"'\n")
	writeExec(t, filepath.Join(f.bin, "satelle"), "cli")
	if err := buildinfo.Record(config.GlobalDir(), filepath.Join(f.bin, "satelle"), "satelle 1.0.0 (commit bbbbbbbbbbbb, built t)"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", f.bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(struct {
			Status string `json:"status"`
			buildinfo.Info
		}{"ok", f.running})
	}))
	t.Cleanup(srv.Close)
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	writeFile(t, filepath.Join(config.GlobalDir(), config.GlobalConfigName), fmt.Sprintf("[service]\nport = %d\n", port))
	units := filepath.Join(f.home, ".config", "systemd", "user")
	writeFile(t, filepath.Join(units, serviceUnitName), renderUnit(satelled, "127.0.0.1", port))
	writeFile(t, filepath.Join(units, syncTimerName), renderSyncTimer())

	// Wired for claude, so the harness check finds nothing in the clean fixture.
	must(t, "init", "--hooks=claude")
	writeFile(t, filepath.Join(f.dir, ".satelle", "skills", "fixture-check.md"), "---\nname: fixture-check\ntype: check\n---\n\n```sh\n[ -f FAIL ] && { cat FAIL; exit 1; }\nexit 0\n```\n")
	writeFile(t, filepath.Join(f.dir, ".satelle", "doctor.toml"), "[[check]]\nskill = \"fixture-check\"\nfix = \"fix what it names\"\n\n[[items]]\nname = \"drift\"\nkind = \"story\"\ntag = \"drift\"\nfix = \"satelle story get {id}\"\n")
	r, err := findRepo("")
	if err != nil {
		t.Fatal(err)
	}
	if err := syncstate.RecordPass(config.GlobalDir(), uiRepoRoot(r), syncstate.Pass{Store: "satelle.dev project p", Pushed: 2, Pulled: 1}, time.Now()); err != nil {
		t.Fatal(err)
	}
	return f
}

func writeFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeExec(t *testing.T, p, body string) {
	t.Helper()
	writeFile(t, p, body)
	if err := os.Chmod(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

// digest maps every file under roots to its sha256 and mode, and every
// directory to its presence: equal digests mean nothing was written.
func digest(t *testing.T, roots ...string) map[string]string {
	t.Helper()
	m := map[string]string{}
	for _, root := range roots {
		m[root] = "absent"
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			if d.IsDir() {
				m[p] = "dir"
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			sum := sha256.Sum256(b)
			m[p] = hex.EncodeToString(sum[:]) + " " + info.Mode().String()
			return nil
		})
	}
	return m
}

func sameDigest(a, b map[string]string) []string {
	var diff []string
	for k, v := range a {
		if b[k] != v {
			diff = append(diff, k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			diff = append(diff, k+" (new)")
		}
	}
	slices.Sort(diff)
	return diff
}

// doctorJSON runs satelle doctor (text, then --json) and returns the report
// and the --json run's error.
func doctorJSON(t *testing.T) (doctorReport, string, error) {
	t.Helper()
	text, _ := run(t, "", "doctor")
	out, err := run(t, "", "doctor", "--json")
	var rep doctorReport
	if jerr := json.Unmarshal([]byte(out), &rep); jerr != nil {
		t.Fatalf("doctor --json: %v\n%s", jerr, out)
	}
	return rep, text, err
}

// Each issue doctor knows is reported with its exact fix, doctor exits 1, and
// the repo (with .git), the runtime and the satelle home are byte-identical
// before and after; a clean fixture exits 0 with "no issues" (sto_3cf973ae
// AC1, AC3).
func TestDoctorReportsEachIssueAndChangesNothing(t *testing.T) {
	type want struct{ check, problem, fix string }
	cases := []struct {
		name  string
		setup func(t *testing.T, f *doctorFixture)
		want  []want
		note  string
	}{
		{name: "clean"},
		{name: "invalid config", setup: func(t *testing.T, f *doctorFixture) {
			p := filepath.Join(f.dir, ".satelle", "satelle.toml")
			b, _ := os.ReadFile(p)
			writeFile(t, p, string(b)+"\nbogus_key = 1\n")
		}, want: []want{{"config", "bogus_key", "satelle validate"}}},
		{name: "substrate drift", setup: func(t *testing.T, f *doctorFixture) {
			writeFile(t, filepath.Join(f.dir, ".satelle", "skills", "stale.md"), "---\nname: stale\ntype: skill\ndescription: Names a removed command.\n---\n\nRun `satelle story docs <id>`.\n")
		}, want: []want{{"substrate", ".satelle/skills/stale.md:7: command: satelle story docs", "satelle help"}}},
		{name: "init would add", setup: func(t *testing.T, f *doctorFixture) {
			if err := os.Remove(filepath.Join(f.dir, ".satelle", "agents.toml")); err != nil {
				t.Fatal(err)
			}
		}, want: []want{{"init", "satelle init would add .satelle/agents.toml", "satelle init"}}},
		{name: "running differs", setup: func(t *testing.T, f *doctorFixture) {
			f.running.Version = "0.9.0"
		}, want: []want{{"versions", "differs from the installed one", "satelle service restart"}}},
		{name: "CLI differs", setup: func(t *testing.T, f *doctorFixture) {
			if err := buildinfo.Record(config.GlobalDir(), filepath.Join(f.bin, "satelle"), "satelle 1.0.1 (commit bbbbbbbbbbbb, built t)"); err != nil {
				t.Fatal(err)
			}
		}, want: []want{{"versions", "satelle 1.0.1 differs from satelled 1.0.0", "satelle update"}}},
		{name: "timer missing", setup: func(t *testing.T, f *doctorFixture) {
			if err := os.Remove(filepath.Join(f.home, ".config", "systemd", "user", syncTimerName)); err != nil {
				t.Fatal(err)
			}
		}, want: []want{{"sync", "is not installed", "satelle service install"}}},
		{name: "timer inactive", setup: func(t *testing.T, f *doctorFixture) {
			writeFile(t, f.timerState, "inactive\n")
		}, want: []want{{"sync", syncTimerName + " is inactive", "satelle service install"}}},
		{name: "failed pass", setup: func(t *testing.T, f *doctorFixture) {
			r, _ := findRepo("")
			_ = syncstate.RecordPass(config.GlobalDir(), uiRepoRoot(r), syncstate.Pass{Store: "satelle.dev project p", Reason: "1 item(s) not synced: sto_x (diverged)", Pushed: 3, Forked: []string{"sto_x (diverged)"}}, time.Now())
		}, want: []want{{"sync", "the last sync pass failed", "satelle ledger status"}}, note: "pushed 3 item(s), pulled 0 item(s), forked 1: sto_x (diverged)"},
		{name: "seats", setup: func(t *testing.T, f *doctorFixture) {
			done := createID(t, "story", "create", "--title", "finished", "--criteria", "c")
			must(t, "story", "seat", done, "--session", "s-done")
			must(t, "story", "set", done, "--status", "done")
			gone := createID(t, "story", "create", "--title", "elsewhere", "--criteria", "c")
			wt := filepath.Join(t.TempDir(), "wt")
			gittest.Git(t, f.dir, "add", "-A")
			gittest.Git(t, f.dir, "commit", "-q", "-m", "fixture")
			gittest.Git(t, f.dir, "worktree", "add", "-q", wt)
			t.Chdir(wt)
			must(t, "story", "seat", gone, "--session", "s-gone")
			t.Chdir(f.dir)
			if err := os.RemoveAll(wt); err != nil {
				t.Fatal(err)
			}
		}, want: []want{
			{"seats", "a terminal state", "--session s-done"},
			{"seats", "no longer exists", "--session s-gone"},
		}},
		{name: "configured check", setup: func(t *testing.T, f *doctorFixture) {
			writeFile(t, filepath.Join(f.dir, "FAIL"), "skills/x.md:1: command: satelle nope\n")
		}, want: []want{{"check:fixture-check", "satelle nope", "fix what it names"}}},
		{name: "open items", setup: func(t *testing.T, f *doctorFixture) {
			id := createID(t, "story", "create", "--title", "a drift", "--criteria", "c", "--tag", "drift")
			writeFile(t, filepath.Join(f.dir, "want-id"), id)
		}, want: []want{{"open:drift", "a drift", "satelle story get {id}"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newDoctorFixture(t)
			if tc.setup != nil {
				tc.setup(t, f)
			}
			r, _ := findRepo("")
			before := digest(t, f.dir, os.Getenv("SATELLE_HOME"), r.Runtime)
			rep, text, err := doctorJSON(t)
			if d := sameDigest(before, digest(t, f.dir, os.Getenv("SATELLE_HOME"), r.Runtime)); len(d) != 0 {
				t.Fatalf("doctor wrote: %v", d)
			}
			if len(tc.want) == 0 {
				if err != nil || len(rep.Issues) != 0 || !strings.Contains(text, "no issues") {
					t.Fatalf("clean fixture: err %v, issues %+v\n%s", err, rep.Issues, text)
				}
				return
			}
			if ExitCodeOf(err) != 1 {
				t.Errorf("exit %d, want 1 (%v)", ExitCodeOf(err), err)
			}
			for _, w := range tc.want {
				fix := w.fix
				if strings.Contains(fix, "{id}") {
					id, _ := os.ReadFile(filepath.Join(f.dir, "want-id"))
					fix = strings.ReplaceAll(fix, "{id}", string(id))
				}
				found := false
				for _, is := range rep.Issues {
					if is.Check == w.check && strings.Contains(is.Problem, w.problem) && strings.Contains(is.Fix, fix) {
						found = true
					}
				}
				if !found {
					t.Errorf("no %s issue with %q and fix %q in %+v", w.check, w.problem, fix, rep.Issues)
				}
				if !strings.Contains(text, w.check+": ") || !strings.Contains(text, "  fix: ") {
					t.Errorf("text output lacks the %s issue and its fix:\n%s", w.check, text)
				}
			}
			for _, is := range rep.Issues {
				if !slices.ContainsFunc(tc.want, func(w want) bool { return w.check == is.Check }) {
					t.Errorf("unexpected issue %+v", is)
				}
			}
			if tc.note != "" && !slices.ContainsFunc(rep.Notes, func(n string) bool { return strings.Contains(n, tc.note) }) {
				t.Errorf("no note with %q in %v", tc.note, rep.Notes)
			}
		})
	}
}

// A repo that never ran has no runtime; doctor leaves it without one.
func TestDoctorCreatesNoRuntime(t *testing.T) {
	isolate(t)
	must(t, "init")
	r, err := findRepo("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(r.Runtime); err == nil {
		t.Skip("init created the runtime")
	}
	_, _ = run(t, "", "doctor")
	if _, err := os.Stat(r.Runtime); !os.IsNotExist(err) {
		t.Fatalf("doctor created the runtime %s: %v", r.Runtime, err)
	}
}

// hookContextText runs the session-start hook in dir and returns its
// additionalContext (the model's), its top-level systemMessage (the user's)
// and the raw stdout.
func hookContextText(t *testing.T, dir string) (ctx, msg, raw string) {
	t.Helper()
	out, err := run(t, fmt.Sprintf(`{"session_id":"s-hook","cwd":%q}`, dir), "hook", "context")
	if err != nil {
		t.Fatalf("hook context: %v\n%s", err, out)
	}
	var env struct {
		Hook struct {
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
		Message string `json:"systemMessage"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("hook output: %v\n%s", err, out)
	}
	return env.Hook.Context, env.Message, out
}

// The session-start hook runs the same checks and prints one line per check
// doctor reports, with its count and first fix (AC2).
func TestHookContextRunsTheDoctorChecks(t *testing.T) {
	f := newDoctorFixture(t)
	if err := os.Remove(filepath.Join(f.dir, ".satelle", "agents.toml")); err != nil {
		t.Fatal(err)
	}
	createID(t, "story", "create", "--title", "a drift", "--criteria", "c", "--tag", "drift")
	writeFile(t, filepath.Join(f.dir, "FAIL"), "one\ntwo\n")
	rep, _, _ := doctorJSON(t)
	ctx, msg, _ := hookContextText(t, f.dir)
	if !strings.Contains(ctx, fmt.Sprintf("satelle doctor: %d issue(s)", len(rep.Issues))) {
		t.Fatalf("no doctor block for %d issues:\n%s", len(rep.Issues), ctx)
	}
	counts := map[string]int{}
	first := map[string]string{}
	for _, is := range rep.Issues {
		if counts[is.Check] == 0 {
			first[is.Check] = is.Fix
		}
		counts[is.Check]++
	}
	for c, n := range counts {
		if !strings.Contains(ctx, fmt.Sprintf("  %s (%d): ", c, n)) || !strings.Contains(ctx, "fix: "+first[c]) {
			t.Errorf("the block lacks %s (%d) with fix %q:\n%s", c, n, first[c], ctx)
		}
	}
	// The model keeps the problem text; the user's systemMessage is the short
	// view: the issue count, then each check's count and first fix.
	if !strings.Contains(ctx, "  check:fixture-check (2): one — fix: fix what it names\n") {
		t.Errorf("the block lacks the fixture check's problem and fix:\n%s", ctx)
	}
	if !strings.HasPrefix(msg, fmt.Sprintf("satelle doctor: %d issue(s)", len(rep.Issues))) {
		t.Fatalf("no systemMessage for %d issues:\n%s", len(rep.Issues), msg)
	}
	for c, n := range counts {
		if !strings.Contains(msg, fmt.Sprintf("  %s (%d)", c, n)) || (first[c] != "" && !strings.Contains(msg, "fix: "+first[c])) {
			t.Errorf("the systemMessage lacks %s (%d) with fix %q:\n%s", c, n, first[c], msg)
		}
	}
	if !strings.Contains(msg, "  check:fixture-check (2) — fix: fix what it names\n") || strings.Contains(msg, ": one") {
		t.Errorf("the systemMessage is not the fixture check's count and fix alone:\n%s", msg)
	}
	// A clean fixture prints no block and no systemMessage.
	clean := newDoctorFixture(t)
	if ctx, _, raw := hookContextText(t, clean.dir); strings.Contains(ctx, "satelle doctor:") || strings.Contains(ctx, "not checked") || strings.Contains(raw, "systemMessage") {
		t.Errorf("a clean repo printed a doctor block or a systemMessage:\n%s", raw)
	}
}

// A slow check is cut at the hook budget, named as not checked, and the hook
// still answers promptly (AC2: never blocks).
func TestHookContextDoctorBudget(t *testing.T) {
	f := newDoctorFixture(t)
	writeFile(t, filepath.Join(f.dir, ".satelle", "skills", "fixture-check.md"), "---\nname: fixture-check\ntype: check\n---\n\n```sh\nsleep 30\n```\n")
	start := time.Now()
	ctx, _, raw := hookContextText(t, f.dir)
	if el := time.Since(start); el > 4*time.Second {
		t.Fatalf("hook context took %v", el)
	}
	if !strings.Contains(ctx, "not checked within 2s at session start: check:fixture-check") {
		t.Fatalf("the cut check is not named:\n%s", ctx)
	}
	if strings.Contains(raw, "systemMessage") {
		t.Errorf("a cut check with no issues gave a systemMessage:\n%s", raw)
	}
}

// On a machine with satelled's unit but no sync timer, doctor reports the
// sync issue; after satelle update (both binaries current) it reports none,
// because update installed and started the timer (sto_1b6a7ed3 AC4).
func TestUpdateClearsTheSyncIssue(t *testing.T) {
	f := newDoctorFixture(t)
	timer := filepath.Join(f.home, ".config", "systemd", "user", syncTimerName)
	if err := os.Remove(timer); err != nil {
		t.Fatal(err)
	}
	rep, text, _ := doctorJSON(t)
	if !slices.ContainsFunc(rep.Issues, func(i doctorIssue) bool {
		return i.Check == "sync" && strings.Contains(i.Problem, "is not installed")
	}) {
		t.Fatalf("before update doctor does not report the missing timer:\n%s", text)
	}
	t.Setenv(envNoService, "")
	prev := updateTarget
	updateTarget = func() (string, error) { return filepath.Join(f.bin, "satelle"), nil }
	t.Cleanup(func() { updateTarget = prev })
	releaseServer(t, "v"+buildinfo.Version, []string{"serve-v" + doctorBuild.Version}, nil, "")
	if out, err := run(t, "", "update"); err != nil {
		t.Fatalf("update: %v\n%s", err, out)
	}
	for _, p := range []string{timer, filepath.Join(f.home, ".config", "systemd", "user", syncServiceName)} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("update did not install %s: %v", p, err)
		}
	}
	rep, text, _ = doctorJSON(t)
	for _, i := range rep.Issues {
		if i.Check == "sync" {
			t.Errorf("after update doctor still reports a sync issue: %+v\n%s", i, text)
		}
	}
}

// AC1 (sto_0f76dd4e): doctor names each registered pre-1.0 project as needing
// upgrade. In a v1 repo the others are notes, never issues; inside a pre-1.0
// repo that repo is the one upgrade issue, no v1-only check runs, nothing
// dumps the unknown keys, and --resolve leaves it and changes nothing.
func TestDoctorNamesPreV1Repos(t *testing.T) {
	f := newDoctorFixture(t)
	oldA, _ := newPreV1Repo(t)
	oldB, mB := newPreV1Repo(t)
	registerProjects(t, f.dir, oldA, oldB)
	noDump := func(t *testing.T, rep doctorReport) {
		t.Helper()
		for _, is := range rep.Issues {
			if strings.Contains(is.Problem, "unknown key") || strings.Contains(is.Problem, "sync pass failed") {
				t.Errorf("issue dumps the configuration: %+v", is)
			}
		}
		for _, n := range rep.Notes {
			if strings.Contains(n, "unknown key") {
				t.Errorf("note dumps the configuration: %s", n)
			}
		}
	}

	rep, _, err := doctorJSON(t)
	if err != nil {
		t.Fatalf("a clean v1 repo with pre-1.0 neighbours failed doctor: %v %+v", err, rep)
	}
	var notes []string
	for _, n := range rep.Notes {
		if strings.HasPrefix(n, "upgrade: ") {
			notes = append(notes, n)
		}
	}
	if len(notes) != 2 {
		t.Fatalf("want two upgrade notes, got %q", rep.Notes)
	}
	for i, p := range []string{oldA, oldB} {
		if !strings.HasPrefix(notes[i], "upgrade: "+p+": needs upgrade") || !strings.Contains(notes[i], "satelle upgrade --archive") {
			t.Errorf("note %d = %q, want %s needing upgrade", i, notes[i], p)
		}
	}
	for _, is := range rep.Issues {
		if is.Check == "upgrade" {
			t.Errorf("a v1 repo raised an upgrade issue: %+v", is)
		}
	}
	noDump(t, rep)

	t.Chdir(oldB)
	rep, text, err := doctorJSON(t)
	if err == nil || ExitCodeOf(err) != ExitError {
		t.Fatalf("doctor in a pre-1.0 repo: want exit 1, got %v", err)
	}
	if len(rep.Issues) != 1 || rep.Issues[0].Check != "upgrade" || rep.Issues[0].Fix != "satelle upgrade --archive" ||
		rep.Issues[0].Problem != needsUpgradeLine(oldB, mB) {
		t.Fatalf("issues %+v, want the one upgrade issue", rep.Issues)
	}
	for _, n := range rep.Notes {
		if strings.Contains(n, oldB) {
			t.Errorf("the repo doctor runs in is also a note: %s", n)
		}
	}
	noDump(t, rep)
	if strings.Contains(text, "unknown key") || !strings.Contains(text, "fix: satelle upgrade --archive") {
		t.Errorf("doctor text:\n%s", text)
	}

	r, err := findRepo("")
	if err != nil {
		t.Fatal(err)
	}
	before := digest(t, oldB, os.Getenv("SATELLE_HOME"), r.Runtime)
	out, err := run(t, "", "doctor", "--resolve", "--json")
	if err == nil {
		t.Fatalf("--resolve left nothing:\n%s", out)
	}
	var res resolveReport
	if jerr := json.Unmarshal([]byte(out), &res); jerr != nil {
		t.Fatalf("doctor --resolve --json: %v\n%s", jerr, out)
	}
	if len(res.Actions) != 0 || len(res.Left) != 1 || res.Left[0].Check != "upgrade" || res.Left[0].Fix != "satelle upgrade --archive" {
		t.Errorf("--resolve = %+v, want the upgrade issue left and no action", res)
	}
	if d := sameDigest(before, digest(t, oldB, os.Getenv("SATELLE_HOME"), r.Runtime)); len(d) != 0 {
		t.Errorf("--resolve in a pre-1.0 repo changed %v", d)
	}
}

// AC3 (sto_0f76dd4e): session start in a pre-1.0 repo is the one
// needs-upgrade line, to the agent and, where the harness has a user channel,
// to the user; the prompt hook gives the agent the same line. A v1 repo's
// session start is unchanged.
func TestHookContextInAPreV1RepoIsOneLine(t *testing.T) {
	f := newDoctorFixture(t)
	dir, m := newPreV1Repo(t)
	want := needsUpgradeLine(dir, m) + "\n"
	if strings.Count(want, "\n") != 1 || !strings.Contains(want, "satelle upgrade --archive") || !strings.Contains(want, "satelle-upgrade skill") {
		t.Fatalf("the line is not one line naming the command and skill: %q", want)
	}
	noDump := func(t *testing.T, raw string) {
		t.Helper()
		for _, bad := range []string{"unknown key", "configuration is invalid", "satelle doctor"} {
			if strings.Contains(raw, bad) {
				t.Errorf("output holds %q:\n%s", bad, raw)
			}
		}
	}
	type envelope struct {
		Hook struct {
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
		Message *string `json:"systemMessage"`
	}
	hookRun := func(t *testing.T, verb, harness string) (envelope, string) {
		t.Helper()
		args := []string{"hook", verb}
		if harness != "" {
			args = append(args, "--harness", harness)
		}
		out, err := run(t, fmt.Sprintf(`{"session_id":"s-old","cwd":%q}`, dir), args...)
		if err != nil {
			t.Fatalf("hook %v: %v\n%s", args, err, out)
		}
		var env envelope
		if err := json.Unmarshal([]byte(out), &env); err != nil {
			t.Fatalf("hook %v output: %v\n%s", args, err, out)
		}
		return env, out
	}

	t.Run("claude", func(t *testing.T) {
		ctx, msg, raw := hookContextText(t, dir)
		if ctx != want || msg != want {
			t.Errorf("additionalContext %q, systemMessage %q, want both %q", ctx, msg, want)
		}
		noDump(t, raw)
	})
	t.Run("pi", func(t *testing.T) {
		env, raw := hookRun(t, "context", "pi")
		if env.Hook.Context != want || env.Message == nil || *env.Message != want {
			t.Errorf("pi:\n%s", raw)
		}
		noDump(t, raw)
	})
	t.Run("grok", func(t *testing.T) {
		env, raw := hookRun(t, "context", "grok")
		if env.Hook.Context != want || env.Message != nil {
			t.Errorf("grok:\n%s", raw)
		}
		noDump(t, raw)
	})
	t.Run("cursor", func(t *testing.T) {
		payload := `{"conversation_id":"c-old","generation_id":"c-old","session_id":"c-old","cursor_version":"2026.11.05-86047df","workspace_roots":[` + jsonString(dir) + `],"hook_event_name":"sessionStart"}`
		raw, doc := cursorAnswer(t, payload, "context")
		if len(doc) != 1 || doc["additional_context"] != want {
			t.Errorf("cursor answer %s, want only additional_context %q", raw, want)
		}
		noDump(t, raw)
	})
	t.Run("claude prompt", func(t *testing.T) {
		env, raw := hookRun(t, "prompt", "")
		if env.Hook.Context != want || env.Message != nil {
			t.Errorf("prompt:\n%s", raw)
		}
		noDump(t, raw)
	})
	t.Run("a v1 repo is unchanged", func(t *testing.T) {
		ctx, _, _ := hookContextText(t, f.dir)
		if !strings.Contains(ctx, "satelle session: s-hook") || !strings.Contains(ctx, "Edits are allowed only while this session holds a seat") {
			t.Errorf("v1 session start:\n%s", ctx)
		}
	})
}
