package agentcli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// Reviewer tool isolation (sty_ef3efb51): the tools a reviewer is offered equal
// its grant, every out-of-grant class is denied on claude and grok (command and
// acp), and a binding no adapter can keep inside its grant runs with a reported
// gap (sty_2d5e583a) rather than being refused.

const roGrant = "Read,Grep,Glob"

// scopedGrant scopes Bash to satelle commands; only a harness that enforces the
// specifier may offer Bash under it.
const scopedGrant = "Read,Grep,Glob,Bash(satelle:*)"

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func TestClassifyTool_EveryClassOnBothProviders(t *testing.T) {
	cases := map[string]ToolClass{
		"Read": ClassRead, "Grep": ClassRead, "Glob": ClassRead, "read_file": ClassRead, "grep": ClassRead, "list_dir": ClassRead,
		"Write": ClassWrite, "write": ClassWrite, "write_file": ClassWrite,
		"Edit": ClassEdit, "MultiEdit": ClassEdit, "NotebookEdit": ClassEdit, "search_replace": ClassEdit,
		"Bash": ClassShell, "Bash(satelle:*)": ClassShell, "run_terminal_command": ClassShell, "run_terminal_cmd": ClassShell,
		"Task": ClassSubprocess, "spawn_subagent": ClassSubprocess,
		"WebFetch": ClassNetwork, "WebSearch": ClassNetwork, "web_search": ClassNetwork, "image_gen": ClassNetwork,
		"mcp__srv__tool": ClassMCP, "mcp_srv_tool": ClassMCP,
		"Frobnicate": ClassUnknown, "": ClassUnknown,
	}
	for name, want := range cases {
		if got := ClassifyTool(name); got != want {
			t.Errorf("ClassifyTool(%q) = %q, want %q", name, got, want)
		}
	}
}

// sty_7d098d50: cursor's hook tool names. Delete is its file-delete tool and an
// edit (testdata/cursor 10a/11a); Write and Shell classify through the tables
// that already existed; Read and Grep are reads; a name nothing knows fails
// closed as unknown.
func TestClassifyTool_CursorToolNames(t *testing.T) {
	for name, want := range map[string]ToolClass{
		"Write": ClassWrite, "Delete": ClassEdit, "Shell": ClassShell,
		"Read": ClassRead, "Grep": ClassRead, "Glob": ClassRead,
		"FutureTool": ClassUnknown,
	} {
		if got := ClassifyTool(name); got != want {
			t.Errorf("ClassifyTool(%q) = %q, want %q", name, got, want)
		}
	}
}

// outOfGrantTools is one tool name per denied class, per provider, plus a name
// no table knows.
var outOfGrantTools = map[ToolClass][]string{
	ClassWrite:      {"Write", "write", "write_file"},
	ClassEdit:       {"Edit", "MultiEdit", "NotebookEdit", "search_replace"},
	ClassShell:      {"Bash", "run_terminal_command"},
	ClassSubprocess: {"Task", "spawn_subagent"},
	ClassNetwork:    {"WebFetch", "WebSearch", "web_search", "image_gen"},
	ClassMCP:        {"mcp__srv__tool"},
	ClassUnknown:    {"Frobnicate"},
}

// AC1 (claude command + stream): the tools OFFERED equal the grant — the
// rendered --tools value is the grant's tool names, --strict-mcp-config is
// present (no MCP servers), and no out-of-grant class is offered.
func TestReviewerOfferedTools_Claude(t *testing.T) {
	for _, tc := range []struct {
		name, iface, command string
	}{
		{"claude command", InterfaceCommand, DefaultClaudeCommand},
		{"claude stream", InterfaceStream, DefaultClaudeStreamCommand},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := RunnerFromBinding(tc.iface, tc.command)
			if err != nil {
				t.Fatal(err)
			}
			req := Request{ReadOnly: true, AllowedTools: roGrant, SystemPrompt: "s", Payload: "{}"}
			var args []string
			switch v := r.(type) {
			case templateRunner:
				args = reviewerArgs(v.binary, buildArgs(v.argTemplate, req), req)
			case streamRunner:
				args = reviewerArgs(v.binary, buildArgs(v.args, req), req)
			}
			tools, ok := flagValue(args, "--tools")
			if !ok {
				t.Fatalf("no --tools in %v", args)
			}
			if got, want := sortedCopy(strings.Split(tools, ",")), sortedCopy(strings.Split(roGrant, ",")); !reflect.DeepEqual(got, want) {
				t.Errorf("--tools = %v, want the grant %v", got, want)
			}
			if !hasFlag(args, "--strict-mcp-config") {
				t.Errorf("--strict-mcp-config missing: %v", args)
			}
			iso := DescribeReviewer(r, req)
			if iso.OfferedSource != OfferedSourceFlag || !reflect.DeepEqual(sortedCopy(iso.OfferedTools), sortedCopy(strings.Split(roGrant, ","))) {
				t.Errorf("offered = %v (%s), want the grant via flag", iso.OfferedTools, iso.OfferedSource)
			}
			offered := map[string]bool{}
			for _, n := range iso.OfferedTools {
				offered[n] = true
			}
			for class, names := range outOfGrantTools {
				for _, n := range names {
					if offered[n] {
						t.Errorf("%s tool %q (%s) is offered under a read-only grant", tc.name, n, class)
					}
				}
			}
			// A performer request is untouched.
			plain := buildArgs(templateOrStreamArgs(r), Request{AllowedTools: roGrant})
			if hasFlag(reviewerArgs("claude", plain, Request{AllowedTools: roGrant}), "--strict-mcp-config") {
				t.Error("a non-reviewer request must not be trimmed")
			}
		})
	}
}

func templateOrStreamArgs(r Runner) []string {
	switch v := r.(type) {
	case templateRunner:
		return v.argTemplate
	case streamRunner:
		return v.args
	}
	return nil
}

// A scoped grant entry keeps its tool offered by base name, so claude can enforce
// the specifier: Bash is offered once, with --allowedTools pre-approving only
// Bash(satelle:*).
func TestReviewerOfferedTools_ClaudeScopedGrant(t *testing.T) {
	r, err := RunnerFromBinding(InterfaceCommand, DefaultClaudeCommand)
	if err != nil {
		t.Fatal(err)
	}
	iso := DescribeReviewer(r, Request{ReadOnly: true, AllowedTools: "Read,Grep,Glob,Bash(satelle:*)"})
	if got, want := sortedCopy(iso.OfferedTools), []string{"Bash", "Glob", "Grep", "Read"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("offered = %v, want %v", got, want)
	}
	if OfferedToolCount(iso.OfferedTools) != 4 {
		t.Errorf("count = %d, want 4", OfferedToolCount(iso.OfferedTools))
	}
}

// An empty grant offers no tool at all (--tools ""), never "default".
func TestReviewerOfferedTools_EmptyGrantOffersNone(t *testing.T) {
	r, _ := RunnerFromBinding(InterfaceCommand, DefaultClaudeCommand)
	iso := DescribeReviewer(r, Request{ReadOnly: true})
	if iso.OfferedTools == nil || len(iso.OfferedTools) != 0 || iso.OfferedSource != OfferedSourceFlag {
		t.Fatalf("offered = %#v (%s), want an empty flag-sourced set", iso.OfferedTools, iso.OfferedSource)
	}
}

// AC1 (grok command): the stock preset's --tools is the read-only set, and no
// out-of-grant class is in it.
func TestReviewerOfferedTools_GrokCommand(t *testing.T) {
	r, err := RunnerFromBinding(InterfaceCommand, DefaultGrokCommand)
	if err != nil {
		t.Fatal(err)
	}
	iso := DescribeReviewer(r, Request{ReadOnly: true, AllowedTools: roGrant})
	want := []string{"grep", "list_dir", "read_file"}
	if got := sortedCopy(iso.OfferedTools); !reflect.DeepEqual(got, want) || iso.OfferedSource != OfferedSourceFlag {
		t.Fatalf("offered = %v (%s), want %v via flag", got, iso.OfferedSource, want)
	}
	for _, n := range iso.OfferedTools {
		if ClassifyTool(n) != ClassRead {
			t.Errorf("grok --tools offers %q, class %s", n, ClassifyTool(n))
		}
	}
}

// The capability table's tool cells match what DescribeReviewer reports.
func TestCapabilityTable_ToolCellsMatchCode(t *testing.T) {
	spawn := map[string][2]string{
		"claude command": {InterfaceCommand, DefaultClaudeCommand},
		"claude stream":  {InterfaceStream, DefaultClaudeStreamCommand},
		"grok command":   {InterfaceCommand, DefaultGrokCommand},
		"pi pi-rpc":      {InterfacePiRPC, PiRPCCommand},
	}
	for _, row := range CapabilityTable() {
		r, err := RunnerFromBinding(spawn[row.Adapter][0], spawn[row.Adapter][1])
		if err != nil {
			t.Fatal(err)
		}
		iso := DescribeReviewer(r, Request{ReadOnly: true, AllowedTools: roGrant})
		if got := iso.OfferedTools != nil; got != row.OfferedTools.Available {
			t.Errorf("%s: table offered=%v, code reports a count=%v", row.Adapter, row.OfferedTools.Available, got)
		}
		if !row.OfferedTools.Available && iso.OfferedSource != "unavailable: "+row.OfferedTools.Reason {
			t.Errorf("%s: source %q != table reason %q", row.Adapter, iso.OfferedSource, row.OfferedTools.Reason)
		}
		if got := iso.Limitation == ""; got != row.ToolTrim.Available {
			t.Errorf("%s: table trim=%v, limitation=%q", row.Adapter, row.ToolTrim.Available, iso.Limitation)
		}
	}
}

func TestStreamInitTools(t *testing.T) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(`{"type":"system","subtype":"init","tools":["Read","Grep","Glob"]}`), &raw); err != nil {
		t.Fatal(err)
	}
	if got := streamInitTools(raw); !reflect.DeepEqual(got, []string{"Read", "Grep", "Glob"}) {
		t.Errorf("tools = %v", got)
	}
	if err := json.Unmarshal([]byte(`{"type":"system","subtype":"init","tools":[]}`), &raw); err != nil {
		t.Fatal(err)
	}
	if got := streamInitTools(raw); got == nil || len(got) != 0 {
		t.Errorf("empty tools array = %#v, want non-nil empty", got)
	}
	if got := streamInitTools(map[string]any{"type": "system"}); got != nil {
		t.Errorf("absent tools = %v, want nil", got)
	}
}

// AC1 (claude stream, harness source): the system/init tools array reaches the
// session as EventSessionInit.Tools, and equals the grant.
func TestStreamSession_InitReportsOfferedTools(t *testing.T) {
	skipWithoutPython3(t)
	peer := writeFakeStreamPeer(t)
	r, err := newStreamRunner(peer + " --output-format stream-json")
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan []string, 1)
	sess, err := openStreamSession(context.Background(), r.(streamRunner), Request{
		ReadOnly: true, AllowedTools: roGrant,
		Env: map[string]string{"STREAM_INIT_TOOLS": roGrant},
		OnEvent: func(ev Event) {
			if ev.Kind == EventSessionInit {
				select {
				case got <- ev.Tools:
				default:
				}
			}
		},
	}, ReviewerPermissionPolicy(roGrant))
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	select {
	case tools := <-got:
		if !reflect.DeepEqual(sortedCopy(tools), sortedCopy(strings.Split(roGrant, ","))) {
			t.Fatalf("init tools = %v, want the grant", tools)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no session_init with tools")
	}
}

// --- preflight ------------------------------------------------------------------

func TestPreflightReviewer(t *testing.T) {
	cases := []struct {
		name, iface, command, grant string
		warns                       bool // false = no gap at all
		wantAdapter                 string
	}{
		{name: "stock claude command", iface: "command", command: DefaultClaudeCommand, grant: roGrant},
		{name: "stock claude stream", iface: "stream", command: DefaultClaudeStreamCommand, grant: roGrant},
		{name: "stock grok command preset (always-approve + --tools)", iface: "command", command: DefaultGrokCommand, grant: roGrant},
		{name: "in-loop starts nothing", iface: "command", command: "in-loop", grant: roGrant},
		{name: "claude --tools {tools} placeholder is the grant", iface: "command", command: "claude -p --tools {tools} --dangerously-skip-permissions", grant: roGrant},
		{name: "claude offers Bash, grant scopes it", iface: "command", command: "claude -p --tools Read,Bash", grant: "Read,Bash(satelle:*)"},

		{name: "grok always-approve without --tools", iface: "command",
			command: "grok -p {payload} --always-approve --output-format plain",
			grant:   roGrant,
			warns:   true, wantAdapter: "grok/command"},
		{name: "grok always-approve with a wider --tools", iface: "command",
			command: "grok -p {payload} --tools read_file,grep,list_dir,run_terminal_command --always-approve",
			grant:   roGrant,
			warns:   true, wantAdapter: "grok/command"},
		{name: "grok command with no allow-list", iface: "command", command: "grok -p {payload} --output-format plain", grant: roGrant, warns: true, wantAdapter: "grok/command"},
		// A blank grok --tools is not an allow-list.
		{name: "grok always-approve with --tools= (blank)", iface: "command",
			command: "grok -p {payload} --always-approve --tools=",
			grant:   roGrant,
			warns:   true, wantAdapter: "grok/command"},
		{name: "grok always-approve with --tools before another flag", iface: "command",
			command: "grok -p {payload} --tools --always-approve",
			grant:   roGrant,
			warns:   true, wantAdapter: "grok/command"},
		{name: "grok always-approve with --tools {tools} rendered from an empty grant", iface: "command",
			command: "grok -p {payload} --tools {tools} --always-approve",
			grant:   "",
			warns:   true, wantAdapter: "grok/command"},
		{name: "grok --tools {tools} with a real grant still counts as an allow-list", iface: "command",
			command: "grok -p {payload} --tools {tools} --always-approve",
			grant:   roGrant},
		{name: "claude bypass without --tools", iface: "command", command: "claude -p --dangerously-skip-permissions", grant: roGrant, warns: true, wantAdapter: "claude/command"},
		{name: "claude bypassPermissions mode", iface: "stream", command: "claude -p --permission-mode bypassPermissions", grant: roGrant, warns: true, wantAdapter: "claude/stream"},
		{name: "claude --tools wider than the grant", iface: "command", command: "claude -p --tools Read,Bash,Task", grant: roGrant, warns: true, wantAdapter: "claude/command"},
		{name: "unrecognised harness", iface: "command", command: "mystery-cli -p {payload}", grant: roGrant, warns: true, wantAdapter: "unknown/command"},

		// A scoped grant is not enforced when permissions are skipped.
		{name: "claude command skip-permissions + scoped grant + --tools {tools}", iface: "command",
			command: "claude -p --tools {tools} --dangerously-skip-permissions", grant: scopedGrant,
			warns: true, wantAdapter: "claude/command"},
		{name: "claude stream bypassPermissions + scoped grant + --tools {tools}", iface: "stream",
			command: "claude -p --tools {tools} --permission-mode bypassPermissions", grant: scopedGrant,
			warns: true, wantAdapter: "claude/stream"},
		{name: "claude command skip-permissions + scoped grant + explicit --tools", iface: "command",
			command: "claude -p --tools Read,Bash --dangerously-skip-permissions", grant: scopedGrant,
			warns: true, wantAdapter: "claude/command"},
		{name: "claude stream bypass + scoped grant, no --tools", iface: "stream",
			command: "claude -p --permission-mode=bypassPermissions", grant: scopedGrant,
			warns: true, wantAdapter: "claude/stream"},
		{name: "claude acceptEdits + scoped grant", iface: "command",
			command: "claude -p --tools {tools} --permission-mode acceptEdits", grant: scopedGrant,
			warns: true, wantAdapter: "claude/command"},
		{name: "claude skip-permissions, --tools omits the scoped tool", iface: "command",
			command: "claude -p --tools Read,Grep --dangerously-skip-permissions", grant: scopedGrant},
		{name: "claude default mode + scoped grant is enforced", iface: "stream",
			command: "claude -p --tools {tools} --permission-mode default", grant: scopedGrant},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gaps := PreflightReviewer(tc.iface, tc.command, tc.grant)
			if !tc.warns {
				if len(gaps) != 0 {
					t.Fatalf("unexpected gap: %+v", gaps)
				}
				return
			}
			if len(gaps) == 0 {
				t.Fatal("want a gap (a warning, never a refusal), got none")
			}
			for _, g := range gaps {
				if g.Adapter != tc.wantAdapter {
					t.Errorf("adapter = %q, want %q", g.Adapter, tc.wantAdapter)
				}
				if g.What == "" || g.Fix == "" {
					t.Errorf("gap %+v must state the gap and its fix", g)
				}
			}
		})
	}
}

// The claude bypass-with-scoped-grant gap names the transport and the reason.
func TestPreflightReviewer_ClaudeBypassScopedGrantReason(t *testing.T) {
	for _, tc := range []struct{ iface, command, adapter string }{
		{"command", "claude -p --tools {tools} --dangerously-skip-permissions", "claude/command"},
		{"stream", "claude -p --tools {tools} --permission-mode bypassPermissions", "claude/stream"},
	} {
		gaps := PreflightReviewer(tc.iface, tc.command, scopedGrant)
		if len(gaps) != 1 {
			t.Fatalf("%s: want one gap, got %+v", tc.adapter, gaps)
		}
		for _, want := range []string{"scoped grant not enforced", "the specifier is not applied when permissions are skipped"} {
			if !strings.Contains(gaps[0].What, want) {
				t.Errorf("%s: gap %q must contain %q", tc.adapter, gaps[0].What, want)
			}
		}
		if gaps[0].Adapter != tc.adapter {
			t.Errorf("adapter = %q, want %q", gaps[0].Adapter, tc.adapter)
		}
	}
}

// A claude reviewer spawn pins --permission-mode default unless the template sets
// a mode, so a user-settings defaultMode of bypassPermissions cannot switch the
// Bash(satelle:*) specifier off.
func TestReviewerArgs_PinsPermissionModeDefault(t *testing.T) {
	req := Request{ReadOnly: true, AllowedTools: scopedGrant}
	for _, tc := range []struct{ name, iface, command string }{
		{"stock claude command", InterfaceCommand, DefaultClaudeCommand},
		{"stock claude stream", InterfaceStream, DefaultClaudeStreamCommand},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := RunnerFromBinding(tc.iface, tc.command)
			if err != nil {
				t.Fatal(err)
			}
			args := reviewerArgs("claude", buildArgs(templateOrStreamArgs(r), req), req)
			if v, ok := flagValue(args, "--permission-mode"); !ok || v != "default" {
				t.Errorf("--permission-mode = %q (present %v), want default in %v", v, ok, args)
			}
			if DescribeReviewer(r, req).OfferedSource != OfferedSourceFlag {
				t.Error("describe must render the same argv")
			}
		})
	}

	// A template-set mode is left alone; so is a skip flag (preflight judges it).
	for _, tmpl := range [][]string{
		{"-p", "--permission-mode", "plan"},
		{"-p", "--permission-mode=plan"},
		{"-p", "--dangerously-skip-permissions"},
	} {
		args := reviewerArgs("claude", tmpl, req)
		for i := range tmpl {
			if args[i] != tmpl[i] {
				t.Fatalf("template %v rewritten to %v", tmpl, args)
			}
		}
		if n := strings.Count(strings.Join(args, " "), "--permission-mode"); n > 1 {
			t.Errorf("%v: --permission-mode appears %d times", args, n)
		}
		if strings.Contains(strings.Join(args, " "), "--permission-mode default") {
			t.Errorf("%v: a template-set mode/skip flag must not be overridden with default", args)
		}
	}

	// Not a reviewer request, or not claude: untouched.
	perf := Request{AllowedTools: scopedGrant}
	if got := reviewerArgs("claude", []string{"-p"}, perf); hasFlag(got, "--permission-mode") {
		t.Errorf("performer request gained a permission mode: %v", got)
	}
	if got := reviewerArgs("grok", []string{"-p"}, req); hasFlag(got, "--permission-mode") {
		t.Errorf("grok spawn gained a permission mode: %v", got)
	}
}

// Every configuration the isolation warning covers is a gap whatever the operator
// declares: attestation only changes how the gap is reported, so the preflight
// takes no attested argument and an unrecognised harness is a gap too.
func TestPreflightReviewer_UnrecognisedHarnessIsAGap(t *testing.T) {
	const unknownCmd = "verdict.sh -p {payload}"
	for _, iface := range []string{"command", "stream", "acp"} {
		gaps := PreflightReviewer(iface, unknownCmd, roGrant)
		if len(gaps) != 1 || !strings.Contains(gaps[0].What, "tools not held to the grant") {
			t.Errorf("%s: gaps = %+v", iface, gaps)
		}
	}
	if !UnrecognisedCommand(unknownCmd) || UnrecognisedCommand("claude -p") || UnrecognisedCommand("grok agent stdio") || UnrecognisedCommand("in-loop") {
		t.Error("UnrecognisedCommand must be true only for a harness no adapter knows")
	}
}

// --- permission policy: the deny half, every class, every live transport ----------

// AC4 (claude stream): under a read-only grant every out-of-grant class asks
// and is denied; the granted read tools are allowed.
func TestReviewerPermissionPolicy_DeniesEveryOutOfGrantClass(t *testing.T) {
	pol := ReviewerPermissionPolicy(roGrant)
	for class, names := range outOfGrantTools {
		for _, n := range names {
			if pol(PermissionRequest{ToolName: n, Kind: toolNameKind(n)}).Allow {
				t.Errorf("%s tool %q was allowed under a read-only grant", class, n)
			}
			// kind=read must not launder a named tool (the run_terminal_command hole).
			if pol(PermissionRequest{ToolName: n, Kind: "read"}).Allow {
				t.Errorf("%s tool %q with kind=read was allowed", class, n)
			}
		}
	}
	if pol(PermissionRequest{}).Allow {
		t.Error("an unnamed, kindless request must be denied")
	}
	for _, n := range []string{"Read", "Grep", "Glob", "read_file", "list_dir"} {
		if !pol(PermissionRequest{ToolName: n, Kind: "read"}).Allow {
			t.Errorf("granted read tool %q was denied", n)
		}
	}
	// A scoped grant is pre-approved by the harness; a live ask for the bare tool is outside it.
	if ReviewerPermissionPolicy("Read,Bash(satelle:*)")(PermissionRequest{ToolName: "Bash"}).Allow {
		t.Error("an unscoped Bash ask must be denied when only Bash(satelle:*) is granted")
	}
	// A tool the grant names exactly is allowed even when no table knows it.
	if !ReviewerPermissionPolicy("Read,Frobnicate")(PermissionRequest{ToolName: "Frobnicate"}).Allow {
		t.Error("an exactly granted unknown tool must be allowed")
	}
}

func TestStreamSession_ReviewerDeniesEveryOutOfGrantClass(t *testing.T) {
	skipWithoutPython3(t)
	peer := writeFakeStreamPeer(t)
	for class, names := range outOfGrantTools {
		for _, n := range names {
			t.Run(string(class)+"/"+n, func(t *testing.T) {
				permOut := filepath.Join(t.TempDir(), "perm.txt")
				r, err := RunnerFromBinding(InterfaceStream, peer+" --output-format stream-json")
				if err != nil {
					t.Fatal(err)
				}
				_, err = r.Run(context.Background(), Request{
					SystemPrompt: "r", Payload: "{}", AllowedTools: roGrant, ReadOnly: true,
					Env: map[string]string{"STREAM_PERM": "1", "STREAM_PERM_TOOL": n, "PERM_OUT": permOut, "STREAM_RESULT": `{"decision":"accept","notes":"ok"}`},
				})
				if err != nil {
					t.Fatalf("Run: %v", err)
				}
				got, _ := os.ReadFile(permOut)
				if strings.TrimSpace(string(got)) != "deny" {
					t.Fatalf("%s tool %q behaviour = %q, want deny", class, n, got)
				}
			})
		}
	}
}

// permissionExtra is the peer snippet that asks permission for toolCall and
// insists on the answer wantOption.
func permissionExtra(t *testing.T, toolCall map[string]any, wantOption string) string {
	t.Helper()
	tc, err := json.Marshal(toolCall)
	if err != nil {
		t.Fatal(err)
	}
	return `
        send({"jsonrpc":"2.0","id":99,"method":"session/request_permission","params":{"sessionId":"sess_test","toolCall":` + string(tc) + `,"options":[{"optionId":"allow-once","name":"Allow","kind":"allow_once"},{"optionId":"reject-once","name":"Reject","kind":"reject_once"}]}})
        resp = read()
        if resp is None or resp.get("result",{}).get("outcome",{}).get("optionId") != "` + wantOption + `":
            send({"jsonrpc":"2.0","id":mid,"error":{"code":1,"message":"expected ` + wantOption + `, got %s" % (resp,)}})
            continue
`
}

func runACPReviewer(t *testing.T, peer string) ([]byte, error) {
	t.Helper()
	out, _, err := runACPReviewerNotes(t, peer)
	return out, err
}

// runACPReviewerNotes is runACPReviewer that also returns every runtime isolation
// note (gap or breach) the session reported.
func runACPReviewerNotes(t *testing.T, peer string) ([]byte, []IsolationNote, error) {
	t.Helper()
	r, err := RunnerFromBinding(InterfaceACP, peer+" stdio")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var mu sync.Mutex
	var notes []IsolationNote
	out, err := r.Run(ctx, Request{SystemPrompt: "r", Payload: "{}", AllowedTools: roGrant, ReadOnly: true, Capture: CaptureFull,
		OnIsolation: func(n IsolationNote) {
			mu.Lock()
			notes = append(notes, n)
			mu.Unlock()
		}})
	mu.Lock()
	defer mu.Unlock()
	return out, append([]IsolationNote(nil), notes...), err
}

// wantNote asserts exactly one note of the given kind whose detail contains want.
func wantNote(t *testing.T, notes []IsolationNote, breach bool, want string) {
	t.Helper()
	var got []IsolationNote
	for _, n := range notes {
		if n.Breach == breach {
			got = append(got, n)
		}
	}
	if len(got) != 1 || !strings.Contains(got[0].Detail, want) || got[0].Adapter == "" {
		t.Fatalf("want one breach=%v note containing %q, got %+v", breach, want, notes)
	}
}

// AC4 (grok acp): every out-of-grant class is rejected by permission under a
// read-only grant — including the VIRE hole (run_terminal_command labelled
// kind=read) — and unknown, empty-kind and MCP calls are never approved by
// default.
func TestACPReviewer_PermissionDeniesEveryOutOfGrantClass(t *testing.T) {
	skipWithoutPython3(t)
	deny := []struct {
		name string
		call map[string]any
	}{
		{"write", map[string]any{"toolCallId": "c1", "kind": "edit", "title": "write"}},
		{"edit", map[string]any{"toolCallId": "c1", "kind": "edit", "title": "search_replace"}},
		{"edit named, kind other", map[string]any{"toolCallId": "c1", "kind": "other", "toolName": "search_replace"}},
		{"shell", map[string]any{"toolCallId": "c1", "kind": "execute", "title": "Run: satelle story get sty_1"}},
		{"shell labelled kind=read (VIRE)", map[string]any{"toolCallId": "c1", "kind": "read", "title": "run_terminal_command"}},
		{"shell labelled kind=read, command input", map[string]any{"toolCallId": "c1", "kind": "read", "title": "satelle story get sty_1", "rawInput": map[string]any{"command": "satelle story get sty_1"}}},
		{"shell named in title tail", map[string]any{"toolCallId": "c1", "kind": "read", "title": "run_terminal_command: satelle ledger list"}},
		{"subprocess", map[string]any{"toolCallId": "c1", "kind": "other", "title": "spawn_subagent"}},
		{"network", map[string]any{"toolCallId": "c1", "kind": "fetch", "title": "web_search"}},
		{"network named", map[string]any{"toolCallId": "c1", "kind": "other", "title": "image_gen"}},
		{"mcp", map[string]any{"toolCallId": "c1", "kind": "other", "title": "mcp__srv__tool"}},
		{"unknown named", map[string]any{"toolCallId": "c1", "kind": "read", "toolName": "frobnicate"}},
		{"unknown, kind other", map[string]any{"toolCallId": "c1", "kind": "other", "title": "Frobnicate"}},
		{"empty kind and title", map[string]any{"toolCallId": "c1", "kind": "", "title": ""}},
	}
	for _, tc := range deny {
		t.Run("deny/"+tc.name, func(t *testing.T) {
			out, err := runACPReviewer(t, writeFakeACPPeer(t, permissionExtra(t, tc.call, "reject-once")))
			if err != nil || !strings.Contains(string(out), "accept") {
				t.Fatalf("Run = %q, %v (the peer reports any answer but reject-once as an error)", out, err)
			}
		})
	}
	allow := []struct {
		name string
		call map[string]any
	}{
		{"read_file", map[string]any{"toolCallId": "c1", "kind": "read", "title": "read_file"}},
		{"grep by kind search", map[string]any{"toolCallId": "c1", "kind": "search", "title": "grep"}},
		{"list_dir", map[string]any{"toolCallId": "c1", "kind": "read", "title": "list_dir"}},
	}
	for _, tc := range allow {
		t.Run("allow/"+tc.name, func(t *testing.T) {
			out, err := runACPReviewer(t, writeFakeACPPeer(t, permissionExtra(t, tc.call, "allow-once")))
			if err != nil || !strings.Contains(string(out), "accept") {
				t.Fatalf("Run = %q, %v", out, err)
			}
		})
	}
}

// writeIsoACPPeer is an ACP peer whose session/new result and mid-prompt traffic
// the test controls. It appends every method it receives to log.
func writeIsoACPPeer(t *testing.T, log, sessionNewResult, extra string, setModeFails bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "iso-acp-peer")
	logEsc := strings.ReplaceAll(log, `\`, `\\`)
	setMode := `send({"jsonrpc":"2.0","id":mid,"result":{}})`
	if setModeFails {
		setMode = `send({"jsonrpc":"2.0","id":mid,"error":{"code":-32000,"message":"mode is locked"}})`
	}
	script := `#!/usr/bin/env python3
import json, sys
LOG = "` + logEsc + `"

def send(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()

def read():
    line = sys.stdin.readline()
    if not line:
        return None
    return json.loads(line)

while True:
    msg = read()
    if msg is None:
        break
    mid = msg.get("id")
    method = msg.get("method")
    with open(LOG, "a") as f:
        f.write("%s %s\n" % (method, json.dumps(msg.get("params") or {})))
    if method == "initialize":
        send({"jsonrpc":"2.0","id":mid,"result":{"protocolVersion":1,"agentCapabilities":{},"authMethods":[]}})
    elif method == "session/new":
        send({"jsonrpc":"2.0","id":mid,"result":` + sessionNewResult + `})
    elif method == "session/set_mode":
        ` + setMode + `
    elif method == "session/prompt":
` + extra + `
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess_test","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"{\"decision\":\"accept\",\"notes\":\"ok\"}"}}}})
        send({"jsonrpc":"2.0","id":mid,"result":{"stopReason":"end_turn"}})
    elif method == "session/cancel":
        pass
    else:
        if mid is not None:
            send({"jsonrpc":"2.0","id":mid,"result":{}})
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func methodsLogged(t *testing.T, log string) string {
	t.Helper()
	b, _ := os.ReadFile(log)
	return string(b)
}

// AC3/AC7: a grok acp peer that opens in yolo is switched to an ask mode when it
// offers one; the prompt is only sent after that.
func TestACPReviewer_YoloPeerIsMovedToAskMode(t *testing.T) {
	skipWithoutPython3(t)
	log := filepath.Join(t.TempDir(), "log")
	modes := `{"sessionId":"sess_test","modes":{"currentModeId":"yolo","availableModes":[{"id":"yolo"},{"id":"default"}]}}`
	out, err := runACPReviewer(t, writeIsoACPPeer(t, log, modes, "", false))
	if err != nil || !strings.Contains(string(out), "accept") {
		t.Fatalf("Run = %q, %v", out, err)
	}
	l := methodsLogged(t, log)
	if !strings.Contains(l, `session/set_mode {"modeId": "default"`) {
		t.Fatalf("the peer was never moved to the default mode:\n%s", l)
	}
	if strings.Index(l, "session/set_mode") > strings.Index(l, "session/prompt") {
		t.Fatalf("session/prompt was sent before the ask mode was selected:\n%s", l)
	}
}

// sty_2d5e583a AC1: a peer that reports yolo and offers no ask mode — or refuses
// the switch — still gets its prompt and returns its verdict; the gap is reported.
func TestACPReviewer_YoloPeerWithNoAskModeRunsWithGap(t *testing.T) {
	skipWithoutPython3(t)
	cases := map[string]struct {
		modes string
		fails bool
	}{
		"no ask mode offered": {`{"sessionId":"sess_test","modes":{"currentModeId":"yolo","availableModes":[{"id":"yolo"}]}}`, false},
		"switch refused":      {`{"sessionId":"sess_test","modes":{"currentModeId":"yolo","availableModes":[{"id":"yolo"},{"id":"default"}]}}`, true},
		"bypass mode":         {`{"sessionId":"sess_test","modes":{"currentModeId":"bypassPermissions","availableModes":[{"id":"bypassPermissions"}]}}`, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			log := filepath.Join(t.TempDir(), "log")
			out, notes, err := runACPReviewerNotes(t, writeIsoACPPeer(t, log, tc.modes, "", tc.fails))
			if err != nil || !strings.Contains(string(out), "accept") {
				t.Fatalf("Run = %q, %v; a never-ask peer must still return its verdict", out, err)
			}
			wantNote(t, notes, false, "never asks permission")
			if l := methodsLogged(t, log); !strings.Contains(l, "session/prompt") {
				t.Fatalf("no turn was sent to the never-ask peer:\n%s", l)
			}
		})
	}
}

// askModes is a session/new result of a peer that advertises an ask mode.
const askModes = `{"sessionId":"sess_test","modes":{"currentModeId":"default","availableModes":[{"id":"default"}]}}`

// pyJSONLiteral renders a JSON document as a Python expression (json.loads of a
// string), so a peer script can embed a captured result whose true/false/null
// are not Python literals.
func pyJSONLiteral(t *testing.T, doc []byte) string {
	t.Helper()
	q, err := json.Marshal(string(doc))
	if err != nil {
		t.Fatal(err)
	}
	return "json.loads(" + string(q) + ")"
}

// A reviewer peer that reports no permission mode cannot be confirmed or forced
// to ask: the gap is reported and the turn is still sent.
func TestACPReviewer_PeerWithoutModesRunsWithGap(t *testing.T) {
	skipWithoutPython3(t)
	log := filepath.Join(t.TempDir(), "log")
	out, notes, err := runACPReviewerNotes(t, writeIsoACPPeer(t, log, `{"sessionId":"sess_test"}`, "", false))
	if err != nil || !strings.Contains(string(out), "accept") {
		t.Fatalf("Run = %q, %v", out, err)
	}
	wantNote(t, notes, false, "the peer reports no permission mode and cannot be forced to ask")
	if l := methodsLogged(t, log); !strings.Contains(l, "session/prompt") {
		t.Fatalf("no turn was sent to a peer with no permission mode:\n%s", l)
	}
}

// The real grok 1.0.41 `session/new` result (testdata/grok_acp_session_new_1.0.41.jsonl):
// no modes block, configOptions with only model and reasoning_effort. It runs —
// the prompt is sent and a verdict returns — and reports the no-ask-mode gap,
// whatever the user's grok config sets.
func TestACPReviewer_CapturedGrokSessionNewRunsWithGap(t *testing.T) {
	skipWithoutPython3(t)
	raw, err := os.ReadFile(filepath.Join("testdata", "grok_acp_session_new_1.0.41.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var msg struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil || len(msg.Result) == 0 {
		t.Fatalf("fixture is not a session/new response: %v", err)
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(msg.Result, &probe); err != nil {
		t.Fatal(err)
	}
	if _, ok := probe["modes"]; ok {
		t.Fatal("the captured grok session/new result must carry no modes block")
	}
	log := filepath.Join(t.TempDir(), "log")
	out, notes, err := runACPReviewerNotes(t, writeIsoACPPeer(t, log, pyJSONLiteral(t, msg.Result), "", false))
	if err != nil || !strings.Contains(string(out), "accept") {
		t.Fatalf("Run = %q, %v; grok's captured session/new must still return a verdict", out, err)
	}
	wantNote(t, notes, false, "no permission mode")
	if l := methodsLogged(t, log); !strings.Contains(l, "session/prompt") {
		t.Fatalf("no turn was sent to grok:\n%s", l)
	}
}

// A peer that runs an out-of-grant tool WITHOUT asking (yolo behind a mode that
// never showed) is recorded as a breach — one note per call — and the run
// continues to its verdict (sty_2d5e583a): a breach is warned, never aborted.
func TestACPReviewer_ToolRunningWithoutAskIsReportedNotAborted(t *testing.T) {
	skipWithoutPython3(t)
	cases := map[string]struct{ call, want string }{
		"shell": {`{"sessionUpdate":"tool_call","toolCallId":"c1","title":"run_terminal_command","kind":"read","status":"pending"}`, "run_terminal_command"},
		"write": {`{"sessionUpdate":"tool_call","toolCallId":"c1","title":"write","kind":"edit","status":"pending"}`, "write"},
		"mcp":   {`{"sessionUpdate":"tool_call","toolCallId":"c1","title":"mcp__srv__tool","kind":"other","status":"pending"}`, "mcp__srv__tool"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			extra := `
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess_test","update":` + tc.call + `}})
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess_test","update":{"sessionUpdate":"tool_call_update","toolCallId":"c1","status":"in_progress"}}})
`
			log := filepath.Join(t.TempDir(), "log")
			out, notes, err := runACPReviewerNotes(t, writeIsoACPPeer(t, log, askModes, extra, false))
			if err != nil || !strings.Contains(string(out), "accept") {
				t.Fatalf("Run = %q, %v; a breach must not abort the run", out, err)
			}
			wantNote(t, notes, true, tc.want)
		})
	}
}

// A tool the peer ran AFTER satelle denied it is a breach too — reported, not aborted.
func TestACPReviewer_ToolRunningAfterDenyIsReported(t *testing.T) {
	skipWithoutPython3(t)
	extra := permissionExtra(t, map[string]any{"toolCallId": "c1", "kind": "execute", "title": "Bash"}, "reject-once") + `
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess_test","update":{"sessionUpdate":"tool_call_update","toolCallId":"c1","status":"completed","kind":"execute","title":"Bash"}}})
`
	log := filepath.Join(t.TempDir(), "log")
	out, notes, err := runACPReviewerNotes(t, writeIsoACPPeer(t, log, askModes, extra, false))
	if err != nil || !strings.Contains(string(out), "accept") {
		t.Fatalf("Run = %q, %v", out, err)
	}
	wantNote(t, notes, true, "ran after permission was denied")
}

// A peer that switches into a never-ask mode mid-session is a reported breach.
func TestACPReviewer_MidSessionYoloIsReported(t *testing.T) {
	skipWithoutPython3(t)
	extra := `
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess_test","update":{"sessionUpdate":"current_mode_update","currentModeId":"yolo"}}})
`
	out, notes, err := runACPReviewerNotes(t, writeIsoACPPeer(t, filepath.Join(t.TempDir(), "log"), askModes, extra, false))
	if err != nil || !strings.Contains(string(out), "accept") {
		t.Fatalf("Run = %q, %v", out, err)
	}
	wantNote(t, notes, true, "mid-session")
}

// A granted read tool that runs without a permission ask is normal (grok does
// not ask for reads), never a breach.
func TestACPReviewer_UnaskedReadToolIsNotABreach(t *testing.T) {
	skipWithoutPython3(t)
	extra := `
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess_test","update":{"sessionUpdate":"tool_call","toolCallId":"c1","title":"list_dir","kind":"read","status":"pending"}}})
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess_test","update":{"sessionUpdate":"tool_call_update","toolCallId":"c1","status":"completed"}}})
`
	out, notes, err := runACPReviewerNotes(t, writeIsoACPPeer(t, filepath.Join(t.TempDir(), "log"), askModes, extra, false))
	if err != nil || !strings.Contains(string(out), "accept") {
		t.Fatalf("Run = %q, %v", out, err)
	}
	if len(notes) != 0 {
		t.Fatalf("an unasked granted read tool is not a breach, got %+v", notes)
	}
}

// A tool no signal identifies, running without a permission ask, is reported as a
// breach: only a positively identified granted tool may run unasked.
func TestACPReviewer_UnidentifiedToolRunningWithoutAskIsReported(t *testing.T) {
	skipWithoutPython3(t)
	cases := map[string]string{
		"no title, kind or name": `{"sessionUpdate":"tool_call","toolCallId":"c1","status":"in_progress"}`,
		"kind other only":        `{"sessionUpdate":"tool_call","toolCallId":"c1","kind":"other","status":"in_progress"}`,
		"unknown title":          `{"sessionUpdate":"tool_call","toolCallId":"c1","title":"Frobnicate","kind":"other","status":"in_progress"}`,
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			extra := `
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess_test","update":` + call + `}})
`
			out, notes, err := runACPReviewerNotes(t, writeIsoACPPeer(t, filepath.Join(t.TempDir(), "log"), askModes, extra, false))
			if err != nil || !strings.Contains(string(out), "accept") {
				t.Fatalf("Run = %q, %v", out, err)
			}
			wantNote(t, notes, true, "unknown tool call")
		})
	}
}

// A performer (ReadOnly unset) is unchanged: it neither switches modes nor
// breaches on an unasked tool.
func TestACPPerformer_IsolationDoesNotApply(t *testing.T) {
	skipWithoutPython3(t)
	log := filepath.Join(t.TempDir(), "log")
	modes := `{"sessionId":"sess_test","modes":{"currentModeId":"yolo","availableModes":[{"id":"yolo"}]}}`
	extra := `
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess_test","update":{"sessionUpdate":"tool_call","toolCallId":"c1","title":"run_terminal_command","kind":"execute","status":"in_progress"}}})
`
	r, err := RunnerFromBinding(InterfaceACP, writeIsoACPPeer(t, log, modes, extra, false)+" stdio")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background(), Request{SystemPrompt: "r", Payload: "{}", AllowedTools: "Read,Bash,Edit,Write"}); err != nil {
		t.Fatalf("performer Run: %v", err)
	}
}

// The reviewer policy is applied per call under load: concurrent reviewer runs
// share no isolation state.
func TestACPReviewer_ConcurrentRunsAreIndependent(t *testing.T) {
	skipWithoutPython3(t)
	var wg sync.WaitGroup
	errs := make([]error, 4)
	extra := permissionExtra(t, map[string]any{"toolCallId": "c1", "kind": "execute", "title": "Bash"}, "reject-once")
	// Write every peer script before any run starts: writing and exec'ing scripts
	// concurrently races on ETXTBSY.
	peers := make([]string, len(errs))
	for i := range peers {
		peers[i] = writeFakeACPPeer(t, extra)
	}
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = runACPReviewer(t, peers[i])
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("run %d: %v", i, err)
		}
	}
}
