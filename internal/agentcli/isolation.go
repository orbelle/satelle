package agentcli

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// Reviewer tool isolation (sty_ef3efb51). A reviewer judges; it must never write,
// edit, run a shell, spawn a subprocess, fetch over the network or call an MCP
// tool beyond what its binding grants — whatever permission mode the harness runs
// in. This file owns the provider-neutral vocabulary (ToolClass), the tables that
// map each supported harness's own tool names onto it, the preflight that warns of
// a dispatch no adapter can keep inside its grant, and the reviewer policies the
// live transports apply. Tool names and flags live here and nowhere else
// ([[satelle-agent-agnostic]] §1).

// ToolClass is a provider-neutral category of tool capability.
type ToolClass string

// The classes a read-only grant must be able to deny. ClassUnknown is any tool
// no table names — denied unless the grant lists that exact name.
const (
	ClassRead       ToolClass = "read"
	ClassWrite      ToolClass = "write"
	ClassEdit       ToolClass = "edit"
	ClassShell      ToolClass = "shell"
	ClassSubprocess ToolClass = "subprocess"
	ClassNetwork    ToolClass = "network"
	ClassMCP        ToolClass = "mcp"
	ClassUnknown    ToolClass = "unknown"
)

// toolClassByName maps a lower-cased tool name (any supported harness) to its
// class. claude names first, then grok's.
var toolClassByName = map[string]ToolClass{
	// claude
	"read": ClassRead, "grep": ClassRead, "glob": ClassRead, "ls": ClassRead, "notebookread": ClassRead,
	// find is pi's file search (a built-in beside read, grep, ls); it only reads.
	"find":  ClassRead,
	"write": ClassWrite, "edit": ClassEdit, "multiedit": ClassEdit, "notebookedit": ClassEdit,
	"bash": ClassShell, "bashoutput": ClassShell, "killshell": ClassShell,
	"task": ClassSubprocess, "agent": ClassSubprocess,
	"webfetch": ClassNetwork, "websearch": ClassNetwork,
	// grok
	"read_file": ClassRead, "list_dir": ClassRead,
	"write_file":     ClassWrite,
	"search_replace": ClassEdit, "strreplace": ClassEdit, "edit_file": ClassEdit,
	"run_terminal_command": ClassShell, "run_terminal_cmd": ClassShell, "run_terminal": ClassShell, "shell": ClassShell,
	"spawn_subagent": ClassSubprocess,
	"web_fetch":      ClassNetwork, "web_search": ClassNetwork, "image_gen": ClassNetwork,
	// cursor: its file-delete tool (Delete in a preToolUse payload, testdata/cursor/
	// 10a, 11a) is an edit. Write and Shell are cursor's too and already classify
	// above; no Claude name maps to Delete, so a Claude matcher never forwards it.
	"delete": ClassEdit,
}

// grantTool is one entry of a tools grant.
type grantTool struct {
	Name   string // as written, e.g. Bash(satelle:*)
	Base   string // Name without its specifier, e.g. Bash
	Scoped bool   // carries a (specifier) the harness must enforce
}

// parseGrant splits a comma/space separated grant, keeping a (specifier) whole.
func parseGrant(grant string) []grantTool {
	var out []grantTool
	var cur strings.Builder
	depth := 0
	flush := func() {
		name := strings.TrimSpace(cur.String())
		cur.Reset()
		if name == "" {
			return
		}
		base, scoped := name, false
		if i := strings.IndexByte(name, '('); i >= 0 {
			base, scoped = strings.TrimSpace(name[:i]), true
		}
		out = append(out, grantTool{Name: name, Base: base, Scoped: scoped})
	}
	for _, r := range grant {
		switch {
		case r == '(':
			depth++
			cur.WriteRune(r)
		case r == ')':
			if depth > 0 {
				depth--
			}
			cur.WriteRune(r)
		case (r == ',' || r == ' ' || r == '\t') && depth == 0:
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// ClassifyTool maps a harness tool name onto its class. A name no table knows is
// ClassUnknown. A specifier — Bash(satelle:*) — is ignored.
func ClassifyTool(name string) ToolClass {
	n := strings.ToLower(strings.TrimSpace(name))
	if i := strings.IndexByte(n, '('); i >= 0 {
		n = strings.TrimSpace(n[:i])
	}
	if n == "" {
		return ClassUnknown
	}
	if strings.HasPrefix(n, "mcp__") || strings.HasPrefix(n, "mcp_") {
		return ClassMCP
	}
	if c, ok := toolClassByName[n]; ok {
		return c
	}
	return ClassUnknown
}

// classifyKind maps an ACP ToolKind onto a class.
func classifyKind(kind string) ToolClass {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "read", "search", "think":
		return ClassRead
	case "edit", "delete", "move":
		return ClassEdit
	case "execute":
		return ClassShell
	case "fetch":
		return ClassNetwork
	default:
		return ClassUnknown
	}
}

// grantAdmits is what a grant admits: the classes of its tool names plus the
// exact names listed. A reviewer's live permission policy counts only UNSCOPED
// names (a scoped one — Bash(satelle:*) — is pre-approved by the harness itself;
// a live ask for the same tool is by definition outside it). Only a harness that
// enforces specifiers (claude) may offer a scoped tool in its allow-list.
type grantAdmits struct {
	classes map[ToolClass]bool
	names   map[string]bool
}

func admitsFromGrant(grant string, includeScoped bool) grantAdmits {
	a := grantAdmits{classes: map[ToolClass]bool{}, names: map[string]bool{}}
	for _, t := range parseGrant(grant) {
		if t.Scoped && !includeScoped {
			continue
		}
		a.names[strings.ToLower(t.Base)] = true
		if c := ClassifyTool(t.Base); c != ClassUnknown {
			a.classes[c] = true
		}
	}
	return a
}

// GrantAdmitsRead reports whether grant lists an unscoped read-class tool.
// A scoped name does not count: opening a file needs a read tool the grant
// itself names. ClassRead is Read, Grep, Glob, ls, notebookread, read_file,
// and list_dir. find is ClassUnknown and does not admit a read.
func GrantAdmitsRead(grant string) bool {
	return admitsFromGrant(grant, false).classes[ClassRead]
}

// GrantAdmitsShell reports whether grant lists a shell-class tool, including
// a scoped one such as Bash(satelle:*). The judging briefing keeps its CLI
// lines only when this is true.
func GrantAdmitsShell(grant string) bool {
	return admitsFromGrant(grant, true).classes[ClassShell]
}

// ReadOnlyProblem is one reason a binding cannot be held to read-only access.
// Class names the tool class a grant admits beyond reading; it is empty when the
// cause is the adapter (it cannot trim or deny tools) rather than the grant.
type ReadOnlyProblem struct {
	Class ToolClass
	Cause string
}

// readOnlyDenied are the classes a read-only grant must not admit, in report
// order: everything dangerOrder names, then any tool no table knows.
var readOnlyDenied = append(append([]ToolClass{}, dangerOrder...), ClassUnknown)

// ReadOnlyGrantProblems is the ONE answer to whether a binding (its command, its
// interface and its tool grant) is held to read-only access, for a step whose
// input is untrusted text. It returns one problem per cause, none when the
// binding is read-only:
//
//   - an adapter that holds read-only itself (ReadOnlyEnforced: cursor's forced
//     ask mode) passes without reading the grant, which is advisory there; a
//     cursor performer is only held when its template asks for ask mode, because
//     satelle forces that for reviewers alone;
//   - otherwise preflight must find no gap — the adapter must trim the offered
//     tools to the grant, so an adapter no table names is refused;
//   - and the grant itself must admit no shell, subprocess, network, mcp, write,
//     edit or unknown tool, scoped entries (Bash(satelle:*)) included.
//
// It is built on the classifier above, so no provider tool name is judged
// anywhere else. Whether a performer also has a context channel is not decided
// here: config.GrantsContextChannel owns that.
func ReadOnlyGrantProblems(command, iface, grant string, performer bool) []ReadOnlyProblem {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return []ReadOnlyProblem{{Cause: "the binding has no command, so nothing holds it read-only"}}
	}
	if len(fields) == 1 && strings.EqualFold(fields[0], "in-loop") {
		return []ReadOnlyProblem{{Cause: "command in-loop is performed by the driving session with its own tools"}}
	}
	iface = strings.ToLower(strings.TrimSpace(iface))
	if iface == "" {
		iface = InterfaceCommand
	}
	adapter, args := adapterOf(fields[0], fields[1:]), fields[1:]
	if adapter == HarnessCursor {
		if cause := cursorReadOnlyProblem(args, iface, performer); cause != "" {
			return []ReadOnlyProblem{{Cause: cause}}
		}
		return nil
	}
	var out []ReadOnlyProblem
	for _, g := range preflight(iface, fields[0], args, grant) {
		out = append(out, ReadOnlyProblem{Cause: fmt.Sprintf("adapter %s cannot hold it read-only: %s", g.Adapter, g.What)})
	}
	byClass := map[ToolClass][]string{}
	for _, t := range parseGrant(grant) {
		if c := ClassifyTool(t.Base); c != ClassRead {
			byClass[c] = append(byClass[c], t.Name)
		}
	}
	for _, c := range readOnlyDenied {
		if names := byClass[c]; len(names) > 0 {
			out = append(out, ReadOnlyProblem{Class: c, Cause: fmt.Sprintf("grant admits %s tool %s", c, strings.Join(names, ", "))})
		}
	}
	return out
}

// cursorReadOnlyProblem is ReadOnlyGrantProblems' cursor case: "" when the seat is
// held read-only, else why not. A reviewer is held by ReadOnlyEnforced (satelle
// forces ask mode); a performer is not forced, so it must ask for ask mode itself
// on the command transport, and the ACP transport never holds it.
func cursorReadOnlyProblem(args []string, iface string, performer bool) string {
	if !performer {
		if _, err := ReadOnlyEnforced(HarnessCursor, args, iface); err != nil {
			return strings.TrimPrefix(err.Error(), "agentcli: ")
		}
		return ""
	}
	if iface != InterfaceCommand {
		return "cursor: satelle forces read-only mode for a reviewer only, so a performer on the " + iface + " transport is not held read-only"
	}
	modes := flagValues(args, "--mode")
	for _, m := range modes {
		if strings.ToLower(strings.TrimSpace(m)) != cursorReadOnlyMode {
			return errCursorReadOnlyMode(m).Error()
		}
	}
	if len(modes) == 0 {
		return "cursor: satelle forces ask mode for a reviewer only, so a performer template must carry --mode " + cursorReadOnlyMode + " itself"
	}
	return ""
}

// allows reports whether a tool of class c named name is inside the grant. An
// unknown class is admitted only by an exact name listed in the grant.
func (a grantAdmits) allows(c ToolClass, name string) bool {
	if c == ClassUnknown {
		return name != "" && a.names[strings.ToLower(name)]
	}
	return a.classes[c]
}

// dangerOrder ranks classes when one call shows several (worst first).
var dangerOrder = []ToolClass{ClassShell, ClassSubprocess, ClassNetwork, ClassMCP, ClassWrite, ClassEdit}

// acpToolCall is what an ACP toolCall carries that names the tool.
type acpToolCall struct {
	Kind     string
	Title    string
	RawInput json.RawMessage
	// Names are explicit tool-name fields the peer put on the call (toolName,
	// name, _meta.toolName) — first-class evidence, unlike a free-form title.
	Names []string
}

// classifyACPCall resolves the class of an ACP tool call from every signal it
// carries. The kind alone is never trusted: a peer may label a shell tool
// kind=read (the VIRE run_terminal_command hole, sty_ef3efb51). Any signal
// showing a dangerous class wins; read needs positive evidence.
func classifyACPCall(call acpToolCall) (ToolClass, string) {
	var seen []ToolClass
	firstName := ""
	note := func(c ToolClass, name string) {
		seen = append(seen, c)
		if firstName == "" && name != "" {
			firstName = name
		}
	}
	explicitUnknown := false
	for _, n := range call.Names {
		if strings.TrimSpace(n) != "" {
			c := ClassifyTool(n)
			explicitUnknown = explicitUnknown || c == ClassUnknown
			note(c, n)
		}
	}
	head, _, _ := strings.Cut(call.Title, ":")
	head = strings.TrimSpace(head)
	// A title is free prose: only a name a table knows counts, and a lone word
	// counts only when it is the whole title head (so "Read edit.go" is not a
	// write). Underscore names (run_terminal_command) are unambiguous tokens.
	if c := ClassifyTool(head); c != ClassUnknown {
		note(c, head)
	}
	for _, tok := range strings.FieldsFunc(call.Title, func(r rune) bool {
		return !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
	}) {
		if strings.Contains(tok, "_") {
			if c := ClassifyTool(tok); c != ClassUnknown {
				note(c, tok)
			}
		}
	}
	if len(call.RawInput) > 0 {
		var in map[string]json.RawMessage
		if json.Unmarshal(call.RawInput, &in) == nil {
			if _, ok := in["command"]; ok {
				note(ClassShell, "command")
			}
		}
	}
	if k := classifyKind(call.Kind); k != ClassUnknown {
		note(k, "")
	}
	class := worstClass(seen...)
	if class == ClassRead && explicitUnknown {
		// A peer that names a tool no table knows has an unknown tool, whatever
		// benign kind it labels it with.
		return ClassUnknown, firstName
	}
	return class, firstName
}

// worstClass picks the most dangerous class among cs; read only when read is
// positively shown and nothing worse is; otherwise unknown.
func worstClass(cs ...ToolClass) ToolClass {
	for _, d := range dangerOrder {
		for _, c := range cs {
			if c == d {
				return d
			}
		}
	}
	for _, c := range cs {
		if c == ClassRead {
			return ClassRead
		}
	}
	return ClassUnknown
}

// ReviewerPermissionPolicy is the live-session permission policy of a reviewer
// (stream and ACP): a tool is allowed only when its class is inside the grant.
// Unknown, empty-kind and mislabelled tools are denied — never approved by
// default.
func ReviewerPermissionPolicy(grant string) PermissionPolicy {
	admits := admitsFromGrant(grant, false)
	return func(req PermissionRequest) PermissionDecision {
		byName, byKind := ClassifyTool(req.ToolName), classifyKind(req.Kind)
		if byName == ClassUnknown && req.ToolName != "" && byKind != ClassShell && byKind != ClassEdit && byKind != ClassNetwork {
			// A named tool no table knows is unknown whatever a benign kind says.
			return PermissionDecision{Allow: admits.allows(ClassUnknown, req.ToolName)}
		}
		// A dangerous kind beats a benign-looking name and vice versa; a request
		// naming nothing and no kind is unknown, denied.
		return PermissionDecision{Allow: admits.allows(worstClass(byName, byKind), req.ToolName)}
	}
}

// ReviewerSessionPolicy narrows a live reviewer session's own policy (the rework
// consultant's mutator ceiling, say) by its grant: a tool runs only when both
// allow it, so the by-name denial holds on stream sessions too. A nil caller
// leaves the grant policy alone.
func ReviewerSessionPolicy(grant string, caller PermissionPolicy) PermissionPolicy {
	byGrant := ReviewerPermissionPolicy(grant)
	if caller == nil {
		return byGrant
	}
	return func(req PermissionRequest) PermissionDecision {
		if d := caller(req); !d.Allow {
			return d
		}
		return byGrant(req)
	}
}

// --- adapter identity and flag tables -------------------------------------

// AdapterName is adapterOf for a caller outside the package that holds a binding's
// command line rather than a spawn — the label a report prints beside a seat.
// The first field is the binary, the rest its args; an empty command is
// HarnessUnknown.
func AdapterName(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return HarnessUnknown
	}
	return adapterOf(fields[0], fields[1:])
}

// adapterOf names the provider behind a spawn from its binary and argv:
// HarnessClaude, HarnessGrok, HarnessPi, HarnessCursor or HarnessUnknown. Nothing
// unrecognised is assumed to be Claude ([[satelle-agent-agnostic]] §3). cursor
// is the executable named exactly cursor-agent (or cursor-agent-*), and pi the
// executable named exactly pi (or pi-*, which excludes pip and the like); both are
// decided on the binary alone and first: a cursor or pi spawn routinely carries
// `--model grok-…` or `claude-…`, which must not read as another provider.
//
// This is the one classifier: HarnessOf delegates to it. sty_f141c77f kept pi
// HarnessUnknown here so the no-trim gap and the operator-attested path would not
// turn silent; sty_58a9bdc8 reverses that on purpose, because preflight now reports
// pi's own gap (piPreflight) instead of the unknown-adapter one.
func adapterOf(binary string, args []string) string {
	base := strings.ToLower(filepath.Base(binary))
	switch {
	case base == "cursor-agent" || strings.HasPrefix(base, "cursor-agent-"):
		return HarnessCursor
	case base == "pi" || strings.HasPrefix(base, "pi-"):
		return HarnessPi
	case strings.Contains(base, "claude"):
		return HarnessClaude
	case strings.Contains(base, "grok"):
		return HarnessGrok
	}
	for _, a := range args {
		if strings.Contains(strings.ToLower(a), "grok") {
			return HarnessGrok
		}
	}
	return HarnessUnknown
}

// flagValue returns the value of --name in args ("--name v" or "--name=v") and
// whether the flag is present.
func flagValue(args []string, name string) (string, bool) {
	for i, a := range args {
		if a == name {
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				return args[i+1], true
			}
			return "", true
		}
		if strings.HasPrefix(a, name+"=") {
			return strings.TrimPrefix(a, name+"="), true
		}
	}
	return "", false
}

func hasFlag(args []string, name string) bool {
	_, ok := flagValue(args, name)
	return ok
}

// skipsPermission reports whether the spawn args switch the harness's permission
// requests off (yolo / always-approve / bypass): the harness then never asks, so
// a permission deny never runs.
func skipsPermission(adapter string, args []string) bool {
	switch adapter {
	case HarnessClaude:
		if hasFlag(args, "--dangerously-skip-permissions") {
			return true
		}
		if v, ok := flagValue(args, "--permission-mode"); ok && skipModeName(v) {
			return true
		}
	case HarnessGrok:
		if hasFlag(args, "--always-approve") || hasFlag(args, "--yolo") {
			return true
		}
		for _, f := range []string{"--permission-mode", "--mode"} {
			if v, ok := flagValue(args, f); ok && skipModeName(v) {
				return true
			}
		}
	}
	return false
}

// skipModeName reports whether a peer permission-mode id means "never ask".
func skipModeName(id string) bool {
	m := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(id, "_", ""), "-", ""))
	for _, s := range []string{"yolo", "bypass", "alwaysapprove", "autoapprove", "dontask", "acceptall", "acceptedits"} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}

// toolList splits a --tools value into names (comma or space separated, a
// specifier kept whole).
func toolList(v string) []string {
	var out []string
	for _, t := range parseGrant(v) {
		out = append(out, t.Name)
	}
	return out
}

// baseNames returns the deduplicated, order-preserving base names of a grant —
// what a harness allow-list (claude --tools) names, specifiers stripped.
func baseNames(grant string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range parseGrant(grant) {
		k := strings.ToLower(t.Base)
		if !seen[k] {
			seen[k] = true
			out = append(out, t.Base)
		}
	}
	return out
}

// --- preflight -------------------------------------------------------------

// IsolationGap is one way a reviewer binding cannot be held to its tool grant.
// Any gap refuses the reviewer dispatch (ReviewerProblem). The wording is
// provider-local; the engine and doctor only render it.
type IsolationGap struct {
	// Adapter is "<provider>/<transport>", e.g. "grok/acp".
	Adapter string
	// What is the specific gap, e.g. "tools not held to the grant (...)".
	What string
	// Fix is how the operator closes it.
	Fix string
}

// IsolationNote is one runtime observation, delivered through Request.OnIsolation
// while a reviewer session runs: a peer that offered no ask mode (a gap) or ran a
// tool outside the grant without asking (a breach). It is recorded and warned;
// it never aborts the run.
type IsolationNote struct {
	Adapter string
	// Breach is true for an observed out-of-grant tool call or mode switch, false
	// for a runtime gap (the peer offers no ask mode).
	Breach bool
	Detail string
}

// GapSummary joins the gaps' What text for one line ("a; b").
func GapSummary(gaps []IsolationGap) string {
	var parts []string
	for _, g := range gaps {
		parts = append(parts, g.What)
	}
	return strings.Join(parts, "; ")
}

// GapFix joins the gaps' distinct Fix text for one line.
func GapFix(gaps []IsolationGap) string {
	seen := map[string]bool{}
	var parts []string
	for _, g := range gaps {
		if g.Fix != "" && !seen[g.Fix] {
			seen[g.Fix] = true
			parts = append(parts, g.Fix)
		}
	}
	return strings.Join(parts, "; or ")
}

// noAskMode is the runtime gap for an ACP peer that reports no permission mode.
const noAskMode = "the peer reports no permission mode and cannot be forced to ask, so a tool outside the grant could run without a permission request"

const fixGrokCommand = "use the grok command transport with --tools equal to the grant"

// PreflightRunner reports the ways a reviewer dispatch cannot be held inside its
// grant. It is pure over the runner's own configuration and runs before the
// process starts; a nil result means the adapter holds the grant. A
// Runner satelle did not build (a test double) launches no harness and has no
// gaps.
func PreflightRunner(r Runner, grant string) []IsolationGap {
	switch v := r.(type) {
	case templateRunner:
		return preflight(InterfaceCommand, v.binary, v.argTemplate, grant)
	case streamRunner:
		return preflight(InterfaceStream, v.binary, v.args, grant)
	case acpRunner:
		return preflight(InterfaceACP, v.binary, v.args, grant)
	case piRPCRunner:
		return preflight(InterfacePiRPC, v.binary, v.args, grant)
	}
	return nil
}

// UnrecognisedCommand is UnrecognisedRunner over a binding's raw command (what
// satelle doctor holds). An in-loop or empty command starts no process and is
// never unrecognised.
func UnrecognisedCommand(command string) bool {
	fields := strings.Fields(command)
	if len(fields) == 0 || strings.EqualFold(fields[0], "in-loop") {
		return false
	}
	return adapterOf(fields[0], fields[1:]) == HarnessUnknown
}

// PreflightReviewer is PreflightRunner over a binding's raw interface and command
// (what satelle doctor holds). An in-loop or empty command starts no process.
func PreflightReviewer(iface, command, grant string) []IsolationGap {
	fields := strings.Fields(command)
	if len(fields) == 0 || strings.EqualFold(fields[0], "in-loop") {
		return nil
	}
	iface = strings.ToLower(strings.TrimSpace(iface))
	if iface == "" {
		iface = InterfaceCommand
	}
	return preflight(iface, fields[0], fields[1:], grant)
}

func preflight(iface, binary string, args []string, grant string) []IsolationGap {
	adapter := adapterOf(binary, args)
	label := adapter + "/" + iface
	gap := func(what, fix string) []IsolationGap {
		return []IsolationGap{{Adapter: label, What: what, Fix: fix}}
	}
	if adapter == HarnessUnknown {
		return gap(fmt.Sprintf("tools not held to the grant (no adapter knows how to trim or deny tools for %q)", filepath.Base(binary)),
			"point the binding at claude or grok")
	}
	if adapter == HarnessCursor {
		// cursor has no tools allow-list and no permission skip that matters: the
		// forced ask mode is the ceiling (cursor_seat.go).
		return cursorPreflight(iface, args)
	}
	if adapter == HarnessPi {
		return piPreflight(iface, label, args, grant)
	}
	skips := skipsPermission(adapter, args)
	if iface == InterfaceACP {
		if adapter == HarnessGrok {
			// Captured from grok 1.0.41 (testdata/grok_acp_session_new_1.0.41.jsonl):
			// session/new carries no modes block, configOptions offers no permission
			// option and `grok agent stdio` has no ask flag, while the user's own
			// grok config may set always-approve. satelle can neither confirm nor
			// force ask mode, so no permission deny is guaranteed to run.
			fix := fixGrokCommand
			return []IsolationGap{
				{Adapter: label, What: "tools not held to the grant (grok agent stdio cannot trim the offered tools and reports no permission mode, so it cannot be forced to ask)", Fix: fix},
				{Adapter: label, What: "usage accounting not to standard (this transport reports no offered-tool figure and may report no usage)", Fix: fix},
			}
		}
		if skips {
			return gap("tools not held to the grant (the spawn skips permission requests, so the peer never asks; ACP cannot trim the offered tools)",
				"drop the yolo/always-approve flag or use a transport that can trim tools")
		}
		return nil
	}
	// command and stream: the harness trims its offered tools with an allow-list.
	list, listed := flagValue(args, "--tools")
	if adapter == HarnessGrok && listed {
		// A blank grok --tools (`--tools=`, `--tools` before another flag, or
		// `--tools {tools}` rendered from a grant naming no tool) is not an
		// allow-list: grok may read it as "offer the default tools". Claude documents
		// "" as no tools, so only grok is judged this way.
		if v := strings.TrimSpace(list); v == "" || (v == "{tools}" && len(baseNames(grant)) == 0) {
			listed = false
		}
	}
	if listed && strings.TrimSpace(list) != "{tools}" {
		if badNames := unadmittedTools(toolList(list), admitsFromGrant(grant, adapter == HarnessClaude)); len(badNames) > 0 {
			return gap(fmt.Sprintf("tools not held to the grant (--tools offers %s, which the grant %q does not admit)", strings.Join(badNames, ","), grant),
				"narrow --tools to the grant")
		}
	}
	if skips && adapter == HarnessClaude {
		// A scoped grant (Bash(satelle:*)) is enforced by the permission engine; with
		// permissions skipped the specifier is not applied and the bare tool runs
		// unbounded. The offered tools are the listed --tools, else the grant's
		// base names (reviewerArgs adds them at spawn).
		offered := baseNames(grant)
		if listed && strings.TrimSpace(list) != "" && strings.TrimSpace(list) != "{tools}" {
			offered = baseNames(list)
		}
		if names := scopedOnlyOffered(grant, offered); len(names) > 0 {
			return gap(fmt.Sprintf("scoped grant not enforced (the specifier is not applied when permissions are skipped, so %s runs unbounded under the grant %q)", strings.Join(names, ","), grant),
				"drop --dangerously-skip-permissions / the bypass permission mode")
		}
	}
	if skips && !listed {
		return gap("tools not held to the grant (the binding skips permission requests without a --tools allow-list equal to its grant, so every tool the harness offers would run)",
			"add a --tools allow-list equal to the grant, or drop always-approve/yolo/bypass")
	}
	if adapter == HarnessGrok && !listed {
		return gap("tools not held to the grant (grok's command transport carries no --tools allow-list in this binding)",
			"add --tools <read-only grok tools> so only the grant is offered")
	}
	return nil
}

// unadmittedTools returns the names in tools the grant does not admit: the one
// loop preflight's --tools branch and the pi restriction both judge a harness's
// offered set by.
func unadmittedTools(tools []string, admits grantAdmits) []string {
	var bad []string
	for _, n := range tools {
		if !admits.allows(ClassifyTool(n), n) {
			bad = append(bad, n)
		}
	}
	return bad
}

// scopedOnlyOffered returns the offered tool names the grant only SCOPES: a tool
// the grant lists solely with a specifier (Bash(satelle:*)) and never bare.
func scopedOnlyOffered(grant string, offered []string) []string {
	scoped := map[string]bool{}
	bare := map[string]bool{}
	for _, t := range parseGrant(grant) {
		k := strings.ToLower(t.Base)
		if t.Scoped {
			scoped[k] = true
		} else {
			bare[k] = true
		}
	}
	var out []string
	for _, n := range offered {
		if k := strings.ToLower(n); scoped[k] && !bare[k] {
			out = append(out, n)
		}
	}
	return out
}

// --- spawn-time trim ---------------------------------------------------------

// reviewerArgs applies the harness-side tool trim to a rendered reviewer argv. For
// claude it adds `--tools <grant base names>` (the AVAILABLE built-in tools;
// --allowedTools only pre-approves) and `--strict-mcp-config` (no MCP servers are
// configured, so no mcp__* tool is offered) unless the template already carries
// them. It also pins `--permission-mode default` when the template sets no mode
// and does not skip permissions, so a user-settings defaultMode of
// bypassPermissions cannot silently switch off the Bash(satelle:*) specifier
// (under -p an ask the allow-list does not pre-approve is denied). Any other
// request, and any other adapter, is returned unchanged.
func reviewerArgs(binary string, args []string, req Request) []string {
	args = denyArgs(binary, args, req)
	if !req.ReadOnly || adapterOf(binary, args) != HarnessClaude {
		return args
	}
	out := append([]string(nil), args...)
	if !hasFlag(out, "--permission-mode") && !skipsPermission(HarnessClaude, out) {
		out = append(out, "--permission-mode", "default")
	}
	if !hasFlag(out, "--tools") {
		out = append(out, "--tools", strings.Join(baseNames(req.AllowedTools), ","))
	}
	if !hasFlag(out, "--strict-mcp-config") {
		out = append(out, "--strict-mcp-config")
	}
	return out
}

// Where an offered-tool figure came from.
const (
	OfferedSourceFlag    = "flag"    // the --tools allow-list the adapter rendered
	OfferedSourceHarness = "harness" // the harness's own report (claude system/init tools)
)

// ReviewerIsolation describes what one reviewer spawn offers, for the ledger.
type ReviewerIsolation struct {
	// Adapter is "<provider> <transport>".
	Adapter string
	// Limitation is the adapter-named limitation, empty when there is none.
	Limitation string
	// OfferedTools is the tools the harness offers; nil when unavailable.
	OfferedTools []string
	// OfferedSource is OfferedSourceFlag/OfferedSourceHarness, or
	// "unavailable: <adapter reason>" when OfferedTools is nil.
	OfferedSource string
}

// grokACPLimitation is the adapter-named limitation recorded for a grok ACP
// reviewer: grok agent stdio has no tool-list or system-prompt flag. The binding
// runs with this recorded on the ledger (sty_2d5e583a).
const grokACPLimitation = "grok/acp: grok agent stdio cannot trim the offered tools, override the system prompt or drop the machine-wide skills list, and reports no permission mode so it cannot be forced to ask; tools are not held to the grant and usage accounting is not to standard"

const grokACPOfferedUnavailable = "unavailable: grok agent stdio neither trims nor reports offered tools"

// DescribeReviewer reports what a reviewer spawn of r under req offers. It is pure:
// the flag-sourced figure is read off the argv the adapter will render.
func DescribeReviewer(r Runner, req Request) ReviewerIsolation {
	var binary string
	var args []string
	var transport string
	switch v := r.(type) {
	case templateRunner:
		binary, transport = v.binary, InterfaceCommand
		args = reviewerArgs(v.binary, buildArgs(v.argTemplate, req), req)
	case streamRunner:
		binary, transport = v.binary, InterfaceStream
		args = reviewerArgs(v.binary, buildArgs(v.args, req), req)
	case acpRunner:
		binary, transport = v.binary, InterfaceACP
		args = v.args
	case piRPCRunner:
		binary, transport = v.binary, InterfacePiRPC
		args = v.spawnArgs(req)
	default:
		return ReviewerIsolation{}
	}
	adapter := adapterOf(binary, args)
	if adapter == HarnessCursor {
		return cursorOffered(transport)
	}
	iso := ReviewerIsolation{Adapter: adapter + " " + transport}
	if transport == InterfaceACP {
		iso.OfferedSource = "unavailable: " + adapter + " agent stdio neither trims nor reports offered tools"
		if adapter == HarnessGrok {
			iso.OfferedSource = grokACPOfferedUnavailable
			iso.Limitation = grokACPLimitation
		}
		return iso
	}
	if adapter == HarnessPi {
		// pi is trimmed by --tools or --exclude-tools; piEffectiveTools is the one
		// owner of what that leaves.
		tools, restricted := piEffectiveTools(args)
		if !restricted {
			iso.OfferedSource = "unavailable: pi " + transport + " binding carries no tool restriction"
			return iso
		}
		iso.OfferedTools = append([]string{}, tools...)
		iso.OfferedSource = OfferedSourceFlag
		return iso
	}
	if v, ok := flagValue(args, "--tools"); ok {
		iso.OfferedTools = baseNames(v)
		if iso.OfferedTools == nil {
			iso.OfferedTools = []string{}
		}
		iso.OfferedSource = OfferedSourceFlag
		return iso
	}
	iso.OfferedSource = "unavailable: " + adapter + " " + transport + " binding carries no --tools allow-list"
	return iso
}

// OfferedToolCount is the number of distinct tools a report names.
func OfferedToolCount(tools []string) int {
	seen := map[string]bool{}
	for _, t := range tools {
		seen[strings.ToLower(strings.TrimSpace(t))] = true
	}
	delete(seen, "")
	return len(seen)
}
