package agentcli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// A dispatched pi binding (sty_58a9bdc8). pi is a governed harness whose
// command transport prints plain text, so its usage, model and cost are not on
// stdout: pi writes them per call to its own session record
// (~/.pi/agent/sessions/<dir>/<ts>_<sessionId>.jsonl, read by driver_usage_pi.go).
// This file owns what satelle knows about a pi seat beyond the driving-session
// reader: attributing one run to its session, pi's tool names and restriction
// flags, and the tool pi reads material with. pi literals live here and in
// driver_usage_pi.go, nowhere else ([[satelle-agent-agnostic]] §1).

// piReadTool is pi's file-read tool: how a dispatched pi performer opens the
// story files under ~/.satelle/<repo-key>/stories/<id>/.
const piReadTool = "read"

// piBuiltinTools is the tool set pi offers when no --tools / --exclude-tools
// narrows it (pi's `--tools` help lists read, bash, edit, write, grep, find,
// ls). A restriction by --exclude-tools is judged against this set.
var piBuiltinTools = []string{"read", "bash", "edit", "write", "grep", "find", "ls"}

// piEffectiveTools is the one owner of which tools a pi command offers. restricted
// is false when the args carry neither a non-blank --tools nor a non-blank
// --exclude-tools, in which case pi offers everything and tools is nil. A
// {tools} placeholder in --tools stands for the grant itself and adds nothing
// beyond it, so it is dropped from the list.
func piEffectiveTools(args []string) (tools []string, restricted bool) {
	if v, ok := flagValue(args, "--tools"); ok && strings.TrimSpace(v) != "" {
		for _, t := range toolList(v) {
			if t != "{tools}" {
				tools = append(tools, t)
			}
		}
		return tools, true
	}
	if v, ok := flagValue(args, "--exclude-tools"); ok && strings.TrimSpace(v) != "" {
		excluded := map[string]bool{}
		for _, t := range toolList(v) {
			excluded[strings.ToLower(t)] = true
		}
		for _, t := range piBuiltinTools {
			if !excluded[t] {
				tools = append(tools, t)
			}
		}
		return tools, true
	}
	return nil, false
}

// piPreflight is preflight's pi case: pi is trimmed by its own --tools /
// --exclude-tools flags, so the effective set (piEffectiveTools) is judged against
// the grant exactly as the other adapters' --tools list is, on the command and
// pi-rpc transports alike. The gaps name pi.
func piPreflight(iface, label string, args []string, grant string) []IsolationGap {
	if iface != InterfaceCommand && iface != InterfacePiRPC {
		return []IsolationGap{{Adapter: label,
			What: fmt.Sprintf("tools not held to the grant (satelle has no pi adapter for the %s transport, so pi's --tools / --exclude-tools restriction is not read)", iface),
			Fix:  "use the pi command transport with --tools equal to the grant"}}
	}
	tools, restricted := piEffectiveTools(args)
	if !restricted {
		return []IsolationGap{{Adapter: label,
			What: "tools not restricted (the pi command carries no --tools or --exclude-tools, so every pi tool is offered)",
			Fix:  "add --tools <read-only pi tools> equal to the grant"}}
	}
	if bad := unadmittedTools(tools, admitsFromGrant(grant, false)); len(bad) > 0 {
		return []IsolationGap{{Adapter: label,
			What: fmt.Sprintf("tools not held to the grant (pi offers %s, which the grant %q does not admit)", strings.Join(bad, ","), grant),
			Fix:  "set --tools to the grant"}}
	}
	return nil
}

// ContextReadTools names the tools whose presence in a binding's grant gives a
// dispatched agent of adapter a way to read the story files from disk: the disk-read
// tool that adapter actually offers. Claude's is `Read`, pi's is `read`; every other
// adapter keeps the grok-native `read_file`, which is also what an adapter satelle
// has no name for has always been judged by.
func ContextReadTools(adapter string) []string {
	switch adapter {
	case HarnessPi:
		return []string{piReadTool}
	case HarnessClaude:
		return []string{"Read"}
	}
	return []string{"read_file"}
}

// ContextChannelHint is the fragment a refusal or validation message uses to tell
// the operator which disk-read tool gives adapter a context channel.
func ContextChannelHint(adapter string) string {
	names := ContextReadTools(adapter)
	for i, n := range names {
		names[i] = "`" + n + "`"
	}
	return strings.Join(names, " or ") + " for disk reads"
}

// piUsageUnavailable is the explicit pi usage result for a run whose session
// record cannot be found or attributed. Its reason starts "pi adapter:".
func piUsageUnavailable(why string) UsageResult {
	u := unavailableUsage(HarnessPi, strings.TrimPrefix(why, "pi: "))
	u.Adapter = HarnessPi
	u.ModelResolved = noModelReport("pi command")
	return u
}

// piTransportUnsupported is the usage of a pi binding on a transport satelle has
// no pi protocol for (stream, acp): never a silent zero.
func piTransportUnsupported(iface string) UsageResult {
	u := unavailableUsage(HarnessPi, fmt.Sprintf("transport %s not supported for pi usage; use the command transport", iface))
	u.Adapter = HarnessPi
	u.ModelResolved = noModelReport("pi " + iface)
	return u
}

// piSessionIDs lists the session ids of the pi session records pi wrote for dir
// (<ts>_<sessionId>.jsonl), or "" and a reason when the directory cannot be read.
func piSessionIDs(dir string) ([]string, string) {
	sessions := filepath.Join(piHomeDir(), "sessions", piSessionDirName(dir))
	entries, err := os.ReadDir(sessions)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ""
		}
		return nil, fmt.Sprintf("session directory %s unreadable: %v", sessions, err)
	}
	var ids []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		if _, id, ok := strings.Cut(strings.TrimSuffix(name, ".jsonl"), "_"); ok && id != "" {
			ids = append(ids, id)
		}
	}
	return ids, ""
}

// piRunUsage attributes one dispatched pi run to pi's own session record: the
// session under dir (the run's working directory) that pi created at or after start
// and that has assistant rows timestamped inside [start, end]. A session created
// earlier (a pi driving session in the same directory) is never the run's, however
// its rows fall. Exactly one such session is the run's; none, or several
// (concurrent pi processes in one directory share it), is a pi-named unavailable —
// never a zero and never the generic "transport reported no usage".
func piRunUsage(dir string, start, end time.Time) UsageResult {
	if strings.TrimSpace(dir) == "" {
		if wd, err := os.Getwd(); err == nil {
			dir = wd
		}
	}
	ids, why := piSessionIDs(dir)
	if why != "" {
		return piUsageUnavailable(why)
	}
	var hits [][]piUsageRow
	untimedHit, untimedOther, earlier := 0, 0, 0
	var readFail string
	for _, id := range ids {
		rec, reason := readPiSession(id, dir)
		if reason != "" {
			readFail = reason
			continue
		}
		rows, n := piWindowRows(rec, start, end)
		if rec.created.IsZero() || rec.created.Before(start) {
			// Not this run's: a session pi created before the run started (a pi
			// driving session in the same directory) can write rows inside the
			// window, and its figures are the driver's, not the run's.
			if len(rows) > 0 {
				earlier++
			}
			continue
		}
		if len(rows) > 0 {
			hits = append(hits, rows)
			untimedHit += n
		} else {
			untimedOther += n
		}
	}
	untimedWhy := func(n int) string {
		return fmt.Sprintf("%d assistant rows in the session record carry no parseable timestamp; the run window cannot be attributed", n)
	}
	switch {
	case len(hits) > 1:
		return piUsageUnavailable(fmt.Sprintf("%d session records overlap the run window; cannot attribute", len(hits)))
	case len(hits) == 1 && untimedHit > 0:
		return piUsageUnavailable(untimedWhy(untimedHit))
	case len(hits) == 1:
		return piRowsUsage(hits[0])
	case untimedOther > 0:
		return piUsageUnavailable(untimedWhy(untimedOther))
	case readFail != "":
		return piUsageUnavailable(readFail)
	case earlier > 0:
		return piUsageUnavailable(fmt.Sprintf("no session record created at or after the run start; %d earlier session record(s) wrote in the run window and are not this run's", earlier))
	}
	return piUsageUnavailable("no session record written in the run window")
}

// piRowsUsage folds the run's assistant rows into a UsageResult: the disjoint
// token split, the dollar figure pi priced (or a pi-named unpriced reason), and
// every model the run called, the primary being the one that produced most output.
func piRowsUsage(rows []piUsageRow) UsageResult {
	u := UsageResult{Adapter: HarnessPi}
	byModel := map[string]*ModelUsage{}
	var cost float64
	for _, r := range rows {
		u.FreshInputTokens += r.input
		u.CacheCreationInputTokens += r.cacheWrite
		u.CacheReadInputTokens += r.cacheRead
		u.OutputTokens += r.output
		cost += r.cost
		if r.model == "" {
			continue
		}
		m := byModel[r.model]
		if m == nil {
			m = &ModelUsage{ID: r.model}
			byModel[r.model] = m
		}
		m.InputTokens += r.input + r.cacheWrite + r.cacheRead
		m.OutputTokens += r.output
		m.CacheCreationInputTokens += r.cacheWrite
		m.CacheReadInputTokens += r.cacheRead
		if r.cost > 0 {
			c := 0.0
			if m.CostUSD != nil {
				c = *m.CostUSD
			}
			c += r.cost
			m.CostUSD = &c
		}
	}
	u.Available, u.CacheSplitAvailable = true, true
	u.InputTokens = u.FreshInputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
	u.TotalTokens = u.InputTokens + u.OutputTokens
	u.ModelResolved = noModelReport("pi command")
	if len(byModel) > 0 {
		for _, m := range byModel {
			u.Models = append(u.Models, *m)
		}
		sort.Slice(u.Models, func(i, j int) bool { return u.Models[i].ID < u.Models[j].ID })
		u.ModelResolved = pickPrimary(u.Models)
	}
	if cost > 0 {
		u.CostUSD = &cost
	} else {
		u.CostUnavailableReason = piUnpricedCostReason
	}
	return u
}
