package agentcli

import (
	"context"
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
