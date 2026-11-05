package agentcli

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Dispatched pi binding (sty_58a9bdc8): usage from pi's own session record, tool
// restriction judged by the grant owner, and pi's read tool as a context channel.

const piReadOnlyGrant = "read,grep,find,ls"

// installPiSession places body as one more pi session record for repo, under the
// PI_CODING_AGENT_DIR the caller already set.
func installPiSession(t *testing.T, repo, id string, body []byte) {
	t.Helper()
	writeAt(t, filepath.Join(piHomeDir(), "sessions", piSessionDirName(repo), "2026-09-30T09-59-00-000Z_"+id+".jsonl"), body)
}

// multistoryCreatedAt is the multistory capture re-headed as a session pi created at
// hhmmss, so a test can place the session's creation relative to a run's start.
func multistoryCreatedAt(t *testing.T, hhmmss string) []byte {
	t.Helper()
	const header = `"timestamp":"2026-09-30T09:59:00.000Z"`
	body := string(readFixture(t, "pi_session_multistory.jsonl"))
	if !strings.Contains(body, header) {
		t.Fatalf("fixture header %s not found", header)
	}
	return []byte(strings.Replace(body, header, `"timestamp":"2026-09-30T`+hhmmss+`Z"`, 1))
}

// installMultistoryCreated installs the multistory capture as the only pi session of
// piWindowRepo, created at hhmmss.
func installMultistoryCreated(t *testing.T, hhmmss string) {
	t.Helper()
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	installPiSession(t, piWindowRepo, "01a0f01e-fb15-7095-8798-9b7ab9d3d092", multistoryCreatedAt(t, hhmmss))
}

func TestPiRunUsage_AttributesTheSessionInTheWindow(t *testing.T) {
	installMultistoryCreated(t, "10:20:00.000")
	u := piRunUsage(piWindowRepo, piAt(t, "10:20:00.000"), piAt(t, "10:30:00.000"))
	if !u.Available || !u.CacheSplitAvailable {
		t.Fatalf("usage = %+v, want available with a cache split", u)
	}
	if u.FreshInputTokens != 4000 || u.OutputTokens != 400 || u.CacheReadInputTokens != 9000 || u.CacheCreationInputTokens != 0 {
		t.Fatalf("tokens fresh=%d out=%d read=%d write=%d, want 4000/400/9000/0", u.FreshInputTokens, u.OutputTokens, u.CacheReadInputTokens, u.CacheCreationInputTokens)
	}
	if u.InputTokens != 13000 || u.TotalTokens != 13400 {
		t.Fatalf("input=%d total=%d, want the disjoint components summed (13000/13400)", u.InputTokens, u.TotalTokens)
	}
	if u.ModelResolved != "priced/model-one" || len(u.Models) != 1 || u.Models[0].ID != "priced/model-one" {
		t.Fatalf("model = %q models = %+v, want priced/model-one", u.ModelResolved, u.Models)
	}
	if u.CostUSD == nil || math.Abs(*u.CostUSD-0.0323) > 1e-9 || u.CostUnavailableReason != "" {
		t.Fatalf("cost = %v (%q), want pi's own 0.0323", u.CostUSD, u.CostUnavailableReason)
	}
	if u.UnavailableReason != "" {
		t.Fatalf("available usage carries reason %q", u.UnavailableReason)
	}
	if u.Adapter != HarnessPi {
		t.Fatalf("available usage adapter = %q, want %q", u.Adapter, HarnessPi)
	}
}

// pi prices some models at zero beside real tokens; that is an unpriced cost named
// for pi, not a measured $0, and the tokens and model are still recorded.
func TestPiRunUsage_UnpricedCostIsNamed(t *testing.T) {
	installMultistoryCreated(t, "10:00:00.000")
	u := piRunUsage(piWindowRepo, piAt(t, "10:00:00.000"), piAt(t, "10:04:00.000"))
	if !u.Available || u.FreshInputTokens != 1000 || u.ModelResolved != "stealth/space-bunny-alpha" {
		t.Fatalf("usage = %+v", u)
	}
	if u.CostUSD != nil || u.CostUnavailableReason != piUnpricedCostReason {
		t.Fatalf("cost = %v (%q), want nil with %q", u.CostUSD, u.CostUnavailableReason, piUnpricedCostReason)
	}
}

// A run that called several models records them all; the primary is the one that
// produced the most output.
func TestPiRunUsage_SeveralModels(t *testing.T) {
	installMultistoryCreated(t, "10:00:00.000")
	u := piRunUsage(piWindowRepo, piAt(t, "10:00:00.000"), piAt(t, "10:30:00.000"))
	if !u.Available || len(u.Models) != 2 {
		t.Fatalf("usage = %+v, want two models", u)
	}
	if u.ModelResolved != "priced/model-one" {
		t.Fatalf("primary = %q, want the model with the most output (priced/model-one)", u.ModelResolved)
	}
	if u.CostUSD == nil || math.Abs(*u.CostUSD-0.0323) > 1e-9 {
		t.Fatalf("cost = %v, want the priced model's 0.0323", u.CostUSD)
	}
	for _, m := range u.Models {
		if m.ID == "stealth/space-bunny-alpha" && m.CostUSD != nil {
			t.Errorf("unpriced model carries a cost %v", *m.CostUSD)
		}
	}
}

func TestPiRunUsage_UnavailableCases(t *testing.T) {
	multistory := multistoryCreatedAt(t, "10:20:00.000")
	untimed := []byte(`{"type":"session","version":3,"id":"u1","timestamp":"2026-09-30T10:00:00.000Z","cwd":"/x"}` + "\n" +
		`{"type":"message","id":"a1","message":{"role":"assistant","model":"m/x","usage":{"input":5,"output":6,"cacheRead":0,"cacheWrite":0,"totalTokens":11,"cost":{"total":0.1}}}}` + "\n")
	cases := []struct {
		name  string
		setup func(t *testing.T)
		from  string
		to    string
		want  string
	}{
		{"no session directory", func(t *testing.T) { t.Setenv("PI_CODING_AGENT_DIR", t.TempDir()) }, "10:00:00.000", "10:04:00.000", "no session record written in the run window"},
		{"window with no assistant usage", func(t *testing.T) { installMultistory(t) }, "12:00:00.000", "12:10:00.000", "no session record written in the run window"},
		{"overlapping sessions", func(t *testing.T) {
			installMultistoryCreated(t, "10:20:00.000")
			installPiSession(t, piWindowRepo, "02b1f01e-fb15-7095-8798-9b7ab9d3d093", multistory)
		}, "10:20:00.000", "10:30:00.000", "2 session records overlap the run window; cannot attribute"},
		{"untimed rows", func(t *testing.T) {
			t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
			installPiSession(t, piWindowRepo, "u1", untimed)
		}, "10:00:00.000", "10:04:00.000", "1 assistant rows in the session record carry no parseable timestamp"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.setup(t)
			u := piRunUsage(piWindowRepo, piAt(t, c.from), piAt(t, c.to))
			if u.Available || u.InputTokens != 0 || u.OutputTokens != 0 || u.CostUSD != nil {
				t.Fatalf("usage = %+v, want an unavailable that carries no figures", u)
			}
			if !strings.HasPrefix(u.UnavailableReason, "pi adapter: ") || !strings.Contains(u.UnavailableReason, c.want) {
				t.Fatalf("reason = %q, want it to start %q and say %q", u.UnavailableReason, "pi adapter: ", c.want)
			}
			if strings.Contains(u.UnavailableReason, "transport reported no usage") {
				t.Fatalf("reason %q is the generic one", u.UnavailableReason)
			}
			if u.Adapter != HarnessPi {
				t.Errorf("adapter = %q, want %q", u.Adapter, HarnessPi)
			}
			if u.CostUnavailableReason != u.UnavailableReason {
				t.Errorf("cost reason = %q, want it to mirror the usage reason", u.CostUnavailableReason)
			}
			if !IsModelUnavailable(u.ModelResolved) || !strings.Contains(u.ModelResolved, "pi") {
				t.Errorf("model = %q, want a pi-named unavailable", u.ModelResolved)
			}
		})
	}
}

func TestPiRunUsage_UnreadableSessionDirIsNamed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", home)
	// A file where the sessions directory should be makes it unreadable, not absent.
	writeAt(t, filepath.Join(home, "sessions"), []byte("not a directory"))
	u := piRunUsage(piWindowRepo, piAt(t, "10:00:00.000"), piAt(t, "10:04:00.000"))
	if u.Available || !strings.HasPrefix(u.UnavailableReason, "pi adapter: ") {
		t.Fatalf("usage = %+v, want a pi adapter unavailable", u)
	}
}

// A pi driving session in the same repo, created before the run started, writes rows
// inside the run's window. With no session of its own the run must not be billed the
// driver's tokens, cost and model as available pi usage (sty_58a9bdc8).
func TestPiRunUsage_DriverSessionIsNotTheRuns(t *testing.T) {
	// The multistory capture is the driver: created 09:59, writing at 10:20-10:30.
	installMultistoryCreated(t, "09:59:00.000")
	u := piRunUsage(piWindowRepo, piAt(t, "10:20:00.000"), piAt(t, "10:30:00.000"))
	if u.Available || u.InputTokens != 0 || u.OutputTokens != 0 || u.CostUSD != nil || len(u.Models) != 0 {
		t.Fatalf("usage = %+v, want an unavailable carrying none of the driver's figures", u)
	}
	if u.Adapter != HarnessPi || !strings.HasPrefix(u.UnavailableReason, "pi adapter: ") ||
		!strings.Contains(u.UnavailableReason, "created at or after the run start") {
		t.Fatalf("usage = %+v, want a pi-named unavailable saying no session was created at or after the run start", u)
	}
	if !IsModelUnavailable(u.ModelResolved) {
		t.Fatalf("model = %q, want unavailable, not the driver's model", u.ModelResolved)
	}
}

// The run's own session is attributed even though a driver session is also writing
// inside the window: the earlier session is not a competing "overlap".
func TestPiRunUsage_OwnSessionBesideDriver(t *testing.T) {
	installMultistoryCreated(t, "09:59:00.000") // the driver
	own := `{"type":"session","version":3,"id":"own-1","timestamp":"2026-09-30T10:20:00.000Z","cwd":"/x"}` + "\n" +
		`{"type":"message","id":"a1","timestamp":"2026-09-30T10:25:00.000Z","message":{"role":"assistant","model":"own/model","usage":{"input":7,"output":3,"cacheRead":0,"cacheWrite":0,"totalTokens":10,"cost":{"total":0.25}}}}` + "\n"
	installPiSession(t, piWindowRepo, "own-1", []byte(own))
	u := piRunUsage(piWindowRepo, piAt(t, "10:20:00.000"), piAt(t, "10:30:00.000"))
	if !u.Available || u.Adapter != HarnessPi || u.FreshInputTokens != 7 || u.OutputTokens != 3 || u.ModelResolved != "own/model" {
		t.Fatalf("usage = %+v, want the run's own session (7/3, own/model), not the driver's", u)
	}
	if u.CostUSD == nil || *u.CostUSD != 0.25 {
		t.Fatalf("cost = %v, want the run's own 0.25", u.CostUSD)
	}
}

// A session created exactly at the run start is the run's; an older one is not, and
// a session whose creation time cannot be read is never assumed to be the run's.
func TestPiRunUsage_SessionCreationBoundary(t *testing.T) {
	at := func(created string) UsageResult {
		installMultistoryCreated(t, created)
		return piRunUsage(piWindowRepo, piAt(t, "10:20:00.000"), piAt(t, "10:30:00.000"))
	}
	if u := at("10:20:00.000"); !u.Available {
		t.Errorf("created at the run start: usage = %+v, want available", u)
	}
	if u := at("10:19:59.999"); u.Available {
		t.Errorf("created just before the run start: usage = %+v, want unavailable", u)
	}
	noHeader := `{"type":"message","id":"a1","timestamp":"2026-09-30T10:25:00.000Z","message":{"role":"assistant","model":"m/x","usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"totalTokens":2,"cost":{"total":0.1}}}}` + "\n"
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	writeAt(t, filepath.Join(piHomeDir(), "sessions", piSessionDirName(piWindowRepo), "not-a-time_x1.jsonl"), []byte(noHeader))
	if u := piRunUsage(piWindowRepo, piAt(t, "10:20:00.000"), piAt(t, "10:30:00.000")); u.Available {
		t.Errorf("unknown creation time: usage = %+v, want unavailable", u)
	}
}

// writePiShim writes an executable named pi that, like pi, answers in plain text
// and records one assistant row in its session file, the session header stamped
// created and the row stamped at (HH:MM:SS.mmm on the piAt day). An empty created
// writes no record. The file path comes from PI_TEST_SESSION_FILE so the test
// controls where "pi" writes.
func writePiShim(t *testing.T, created, at string) string {
	t.Helper()
	dir := t.TempDir()
	shim := filepath.Join(dir, "pi")
	body := "#!/bin/sh\n"
	if created != "" {
		body += `printf '%s\n' '{"type":"session","version":3,"id":"dispatch-1","timestamp":"2026-09-30T` + created + `Z","cwd":"/x"}' > "$PI_TEST_SESSION_FILE"` + "\n"
		body += `printf '%s\n' '{"type":"message","timestamp":"2026-09-30T` + at + `Z","message":{"role":"assistant","model":"minimax/minimax-m3","usage":{"input":10,"output":20,"cacheRead":30,"cacheWrite":0,"totalTokens":60,"cost":{"total":0.5}}}}' >> "$PI_TEST_SESSION_FILE"` + "\n"
	}
	body += "echo done\n"
	if err := os.WriteFile(shim, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return shim
}

// fixPiRunClock makes every pi run window open at start and close at end, so a
// run's attribution never depends on real time.
func fixPiRunClock(t *testing.T, start, end time.Time) {
	t.Helper()
	orig := piRunClock
	t.Cleanup(func() { piRunClock = orig })
	n := 0
	piRunClock = func() time.Time {
		n++
		if n%2 == 1 {
			return start
		}
		return end
	}
}

// A pi command run through the dispatch path (RunnerFromCommand → UsageRunner) is
// attributed from pi's session record, or recorded as a pi-named unavailable;
// never the generic "transport reported no usage".
func TestPiCommandDispatchRecordsPiUsage(t *testing.T) {
	repo := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	t.Setenv("PI_TEST_SESSION_FILE", filepath.Join(piHomeDir(), "sessions", piSessionDirName(repo), "2026-09-30T10-00-00-000Z_dispatch-1.jsonl"))
	if err := os.MkdirAll(filepath.Dir(os.Getenv("PI_TEST_SESSION_FILE")), 0o755); err != nil {
		t.Fatal(err)
	}
	fixPiRunClock(t, piAt(t, "10:20:00.000"), piAt(t, "10:30:00.000"))

	r, err := RunnerFromCommand(writePiShim(t, "10:20:00.000", "10:25:00.000") + " -p {payload}")
	if err != nil {
		t.Fatal(err)
	}
	ur, ok := r.(UsageRunner)
	if !ok {
		t.Fatalf("%T is not a UsageRunner: a pi command would fall back to UnwrapUsage and record the generic reason", r)
	}
	out, u, err := ur.RunUsage(context.Background(), Request{Dir: repo, Payload: "hi"})
	if err != nil || strings.TrimSpace(string(out)) != "done" {
		t.Fatalf("out = %q err = %v", out, err)
	}
	if !u.Available || u.FreshInputTokens != 10 || u.OutputTokens != 20 || u.CacheReadInputTokens != 30 || u.ModelResolved != "minimax/minimax-m3" {
		t.Fatalf("usage = %+v, want pi's session figures", u)
	}
	if u.CostUSD == nil || *u.CostUSD != 0.5 {
		t.Fatalf("cost = %v, want 0.5", u.CostUSD)
	}
	if u.Adapter != HarnessPi {
		t.Fatalf("dispatched pi usage adapter = %q, want %q", u.Adapter, HarnessPi)
	}

	// The same command with a pi that wrote no session record.
	r, err = RunnerFromCommand(writePiShim(t, "", "") + " -p {payload}")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(os.Getenv("PI_TEST_SESSION_FILE")); err != nil {
		t.Fatal(err)
	}
	_, u, err = r.(UsageRunner).RunUsage(context.Background(), Request{Dir: repo, Payload: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if u.Available || !strings.HasPrefix(u.UnavailableReason, "pi adapter: ") || strings.Contains(u.UnavailableReason, "transport reported no usage") {
		t.Fatalf("usage = %+v, want a pi adapter unavailable, never the generic reason", u)
	}
	if u.Adapter != HarnessPi {
		t.Fatalf("unavailable usage adapter = %q, want %q", u.Adapter, HarnessPi)
	}

	// A session pi created before the run started is not the run's, though its row
	// falls inside the window.
	r, err = RunnerFromCommand(writePiShim(t, "10:19:00.000", "10:25:00.000") + " -p {payload}")
	if err != nil {
		t.Fatal(err)
	}
	_, u, err = r.(UsageRunner).RunUsage(context.Background(), Request{Dir: repo, Payload: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if u.Available || !strings.HasPrefix(u.UnavailableReason, "pi adapter: ") {
		t.Fatalf("session created before the run start: usage = %+v, want a pi adapter unavailable", u)
	}
}

// Every other command adapter behaves exactly as runOnce's Run+UnwrapUsage fallback
// did before templateRunner became a UsageRunner.
func TestTemplateRunnerRunUsage_NonPiUnchanged(t *testing.T) {
	dir := t.TempDir()
	shim := filepath.Join(dir, "claude-shim")
	env := `{"result":"hello","usage":{"input_tokens":3,"output_tokens":4,"cache_creation_input_tokens":1,"cache_read_input_tokens":2},"modelUsage":{"claude-opus-5-5":{"inputTokens":3,"outputTokens":4}}}`
	if err := os.WriteFile(shim, []byte("#!/bin/sh\ncat <<'EOF'\n"+env+"\nEOF\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := RunnerFromCommand(shim + " -p {payload}")
	if err != nil {
		t.Fatal(err)
	}
	text, u, err := r.(UsageRunner).RunUsage(context.Background(), Request{Dir: dir, Payload: "x"})
	if err != nil || string(text) != "hello" {
		t.Fatalf("text = %q err = %v", text, err)
	}
	raw, err := r.Run(context.Background(), Request{Dir: dir, Payload: "x"})
	if err != nil {
		t.Fatal(err)
	}
	wantText, wantUsage := UnwrapUsage(raw)
	// The only difference from the fallback is the adapter agentcli now stamps.
	if u.Adapter != HarnessClaude {
		t.Fatalf("adapter = %q, want %q", u.Adapter, HarnessClaude)
	}
	wantUsage.Adapter = HarnessClaude
	if string(text) != string(wantText) || !reflect.DeepEqual(u, wantUsage) {
		t.Fatalf("RunUsage = %q %+v, want the UnwrapUsage fallback %q %+v", text, u, wantText, wantUsage)
	}
	if !u.Available || u.InputTokens != 6 {
		t.Fatalf("usage = %+v", u)
	}
}

func TestPiTransportUnsupportedIsNamed(t *testing.T) {
	for _, iface := range []string{InterfaceStream, InterfaceACP} {
		u := piTransportUnsupported(iface)
		if u.Available || !strings.HasPrefix(u.UnavailableReason, "pi adapter: ") || !strings.Contains(u.UnavailableReason, iface) || u.Adapter != HarnessPi {
			t.Errorf("%s: usage = %+v", iface, u)
		}
	}
}

func TestPiEffectiveTools(t *testing.T) {
	for _, tc := range []struct {
		name       string
		args       []string
		want       []string
		restricted bool
	}{
		{"no flag", []string{"-p", "{payload}"}, nil, false},
		{"--tools list", []string{"-p", "--tools", "read,grep,find,ls"}, []string{"read", "grep", "find", "ls"}, true},
		{"--tools=list", []string{"--tools=read"}, []string{"read"}, true},
		{"--tools placeholder is the grant", []string{"--tools", "{tools}"}, nil, true},
		{"--exclude-tools removes builtins", []string{"--exclude-tools", "bash,edit"}, []string{"read", "write", "grep", "find", "ls"}, true},
		{"--exclude-tools is case-insensitive", []string{"--exclude-tools", "Bash,EDIT,Write"}, []string{"read", "grep", "find", "ls"}, true},
		{"blank --tools is no restriction", []string{"--tools="}, nil, false},
		{"--tools wins over --exclude-tools", []string{"--tools", "read", "--exclude-tools", "bash"}, []string{"read"}, true},
	} {
		got, restricted := piEffectiveTools(tc.args)
		if restricted != tc.restricted || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: piEffectiveTools(%v) = %v, %v; want %v, %v", tc.name, tc.args, got, restricted, tc.want, tc.restricted)
		}
	}
}

func TestPreflightRunner_PiToolRestriction(t *testing.T) {
	gapFor := func(command, grant string) []IsolationGap {
		t.Helper()
		r, err := RunnerFromCommand(command)
		if err != nil {
			t.Fatal(err)
		}
		return PreflightRunner(r, grant)
	}
	if gaps := gapFor("pi -p --tools read,grep,find,ls {payload}", piReadOnlyGrant); len(gaps) != 0 {
		t.Errorf("restricted pi command: gaps = %+v, want none", gaps)
	}
	if gaps := gapFor("pi -p --tools {tools} {payload}", piReadOnlyGrant); len(gaps) != 0 {
		t.Errorf("--tools {tools}: gaps = %+v, want none", gaps)
	}
	if gaps := gapFor("pi -p --exclude-tools bash,edit,write {payload}", piReadOnlyGrant); len(gaps) != 0 {
		t.Errorf("--exclude-tools bash,edit,write leaves only read tools: gaps = %+v, want none", gaps)
	}

	gaps := gapFor("pi -p {payload}", piReadOnlyGrant)
	if len(gaps) != 1 || gaps[0].Adapter != "pi/command" || !strings.Contains(gaps[0].What, "not restricted") {
		t.Errorf("unrestricted pi command: gaps = %+v, want one pi/command not-restricted gap", gaps)
	}
	gaps = gapFor("pi -p --exclude-tools bash,edit {payload}", piReadOnlyGrant)
	if len(gaps) != 1 || gaps[0].Adapter != "pi/command" || !strings.Contains(gaps[0].What, "write") || strings.Contains(gaps[0].What, "bash") {
		t.Errorf("--exclude-tools leaving write: gaps = %+v, want one gap naming write only", gaps)
	}
	gaps = gapFor("pi -p --tools read,bash {payload}", piReadOnlyGrant)
	if len(gaps) != 1 || !strings.Contains(gaps[0].What, "bash") || !strings.Contains(gaps[0].Fix, "--tools") {
		t.Errorf("--tools read,bash: gaps = %+v, want a gap naming bash", gaps)
	}
	// A recognised adapter: never the unknown-adapter gap, and no attestation offer.
	for _, command := range []string{"pi -p {payload}", "pi -p --tools read,bash {payload}"} {
		for _, g := range gapFor(command, piReadOnlyGrant) {
			if strings.Contains(g.What, "no adapter knows") || strings.HasPrefix(g.Adapter, "unknown") {
				t.Errorf("%q: gap %+v is the unknown-adapter gap", command, g)
			}
		}
	}
	// Other transports have no pi restriction satelle can read.
	for _, iface := range []string{InterfaceStream, InterfaceACP} {
		g := PreflightReviewer(iface, "pi --tools read", piReadOnlyGrant)
		if len(g) != 1 || g[0].Adapter != "pi/"+iface {
			t.Errorf("%s: gaps = %+v, want one pi/%s gap", iface, g, iface)
		}
	}
}

func TestDescribeReviewer_Pi(t *testing.T) {
	describe := func(command string) ReviewerIsolation {
		t.Helper()
		r, err := RunnerFromCommand(command)
		if err != nil {
			t.Fatal(err)
		}
		return DescribeReviewer(r, Request{ReadOnly: true, AllowedTools: piReadOnlyGrant})
	}
	iso := describe("pi -p --tools read,grep,find,ls {payload}")
	if iso.Adapter != "pi command" || iso.OfferedSource != OfferedSourceFlag || !reflect.DeepEqual(iso.OfferedTools, []string{"read", "grep", "find", "ls"}) {
		t.Errorf("--tools: %+v", iso)
	}
	iso = describe("pi -p --exclude-tools bash,edit,write {payload}")
	if iso.Adapter != "pi command" || iso.OfferedSource != OfferedSourceFlag || !reflect.DeepEqual(iso.OfferedTools, []string{"read", "grep", "find", "ls"}) {
		t.Errorf("--exclude-tools reports the effective set: %+v", iso)
	}
	iso = describe("pi -p --tools {tools} {payload}")
	if iso.OfferedSource != OfferedSourceFlag || !reflect.DeepEqual(iso.OfferedTools, []string{"read", "grep", "find", "ls"}) {
		t.Errorf("--tools {tools} renders the grant: %+v", iso)
	}
	iso = describe("pi -p {payload}")
	if iso.Adapter != "pi command" || iso.OfferedTools != nil || iso.OfferedSource != "unavailable: pi command binding carries no tool restriction" {
		t.Errorf("unrestricted: %+v", iso)
	}
}

func TestContextReadTools(t *testing.T) {
	for adapter, want := range map[string][]string{
		HarnessPi:      {"read"},
		HarnessGrok:    {"read_file"},
		HarnessClaude:  {"Read"},
		HarnessUnknown: {"read_file"},
	} {
		if got := ContextReadTools(adapter); !reflect.DeepEqual(got, want) {
			t.Errorf("ContextReadTools(%q) = %v, want %v", adapter, got, want)
		}
	}
	if got := ContextChannelHint(HarnessPi); !strings.Contains(got, "`read`") || strings.Contains(got, "read_file") {
		t.Errorf("pi hint = %q, want it to name `read` and not read_file", got)
	}
	if got := ContextChannelHint(HarnessClaude); !strings.Contains(got, "`Read` for disk reads") || strings.Contains(got, "read_file") {
		t.Errorf("claude hint = %q, want it to name `Read` and not read_file", got)
	}
	if got := ContextChannelHint(HarnessGrok); !strings.Contains(got, "`read_file`") {
		t.Errorf("grok hint = %q, want it to name `read_file`", got)
	}
}

// pi's find only reads, so a read-only grant admits it, as the shared admits loop
// (admitsFromGrant + ClassifyTool) is the one place that is decided.
func TestClassifyTool_PiFind(t *testing.T) {
	if ClassifyTool("find") != ClassRead {
		t.Errorf("find = %s, want read", ClassifyTool("find"))
	}
	if bad := unadmittedTools([]string{"read", "find", "ls"}, admitsFromGrant("Read", false)); len(bad) != 0 {
		t.Errorf("unadmitted = %v", bad)
	}
	if bad := unadmittedTools([]string{"read", "bash", "write"}, admitsFromGrant("Read", false)); !reflect.DeepEqual(bad, []string{"bash", "write"}) {
		t.Errorf("unadmitted = %v, want bash,write", bad)
	}
}
