package agentcli

import (
	"fmt"
	"strings"
)

// Per-adapter capability table (sty_52a8cb4a, epic:agent-parity). One row per
// adapter satelle drives, one column per thing a mechanism reads off agent
// output or session. A capability is either available or carries an
// adapter-named reason it is not — never a silent zero. `satelle help
// agent-dispatch` embeds the table rendered by CapabilityTableMarkdown, and
// capabilities_test.go checks every cell against the code that produces it, so
// the help, this table and the mappers cannot drift apart.

// Capability is one cell: available, or unavailable with the reason.
type Capability struct {
	Available bool
	// Reason is empty when Available and non-empty (and names the adapter's
	// behaviour) when not.
	Reason string
}

func yes() Capability { return Capability{Available: true} }

func no(reason string) Capability { return Capability{Reason: reason} }

// Cell is the help-table spelling: "yes" or "unavailable: <reason>".
func (c Capability) Cell() string {
	if c.Available {
		return "yes"
	}
	return "unavailable: " + c.Reason
}

// AdapterCapabilities is one adapter's row.
type AdapterCapabilities struct {
	// Adapter is "<provider> <transport>", e.g. "claude stream".
	Adapter string
	// Usage: token counts read off the adapter's output.
	Usage Capability
	// CacheSplit: fresh / cache-read / cache-write reported separately.
	CacheSplit Capability
	// ResolvedModel: the model id the run actually used.
	ResolvedModel Capability
	// ModelInheritance: a session model of this provider can be inherited into
	// a dispatch of this adapter (config.SelectModel's in-loop tier).
	ModelInheritance Capability
	// LiveSession: the adapter can be opened as a live session
	// (OpenerFromBinding).
	LiveSession Capability
	// ToolTrim: the harness can be launched offering only the reviewer's granted
	// tools (sty_ef3efb51). Where it cannot, every out-of-grant tool is denied by
	// permission instead, and the adapter names why.
	ToolTrim Capability
	// OfferedTools: the harness or the rendered allow-list reports the tools it
	// offers, so a ledger row can record a real offered-tool count. Where it
	// cannot, the row records this adapter-named reason and no number.
	OfferedTools Capability
	// CloudLaunch: a session of the repository can be started on the provider's
	// cloud from a local worktree and told the branch to push (sty_82cffd60).
	// Where it cannot, the adapter names that it has no cloud runner.
	CloudLaunch Capability
}

// CapabilityTable returns the table in the order help prints it.
func CapabilityTable() []AdapterCapabilities {
	const notLive = "interface=command is one-shot only"
	const noGrokHookModel = "grok's hook payload carries no model, so the in-loop tier is unknown"
	rows := []AdapterCapabilities{
		{
			Adapter: "claude command", Usage: yes(), CacheSplit: yes(), ResolvedModel: yes(),
			ModelInheritance: yes(), LiveSession: no(notLive), ToolTrim: yes(), OfferedTools: yes(),
		},
		{
			Adapter: "claude stream", Usage: yes(), CacheSplit: yes(), ResolvedModel: yes(),
			ModelInheritance: yes(), LiveSession: yes(), ToolTrim: yes(), OfferedTools: yes(),
		},
		{
			Adapter: "grok command", Usage: yes(), CacheSplit: yes(), ResolvedModel: yes(),
			ModelInheritance: no(noGrokHookModel), LiveSession: no(notLive), ToolTrim: yes(), OfferedTools: yes(),
		},
		{
			Adapter: piRPCLabel, Usage: yes(), CacheSplit: yes(), ResolvedModel: yes(),
			ModelInheritance: no("pi's hook payload carries no model, so the in-loop tier is unknown"),
			LiveSession:      no("interface=" + InterfacePiRPC + " runs one-shot dispatches only"),
			ToolTrim:         yes(), OfferedTools: yes(),
		},
	}
	for i := range rows {
		rows[i].CloudLaunch = cloudLaunchCapability(rows[i].Adapter)
	}
	return rows
}

// ReasonForNoModel names why a hook-time in-loop publish found no model to
// report for harness — the ModelInheritance reason of the first CapabilityTable
// row whose adapter is this harness (any transport), since a hook payload only
// carries the harness token, never the transport (sty_719c4a7b AC6). A harness
// with every row available, or with no row at all, still gets an adapter-named
// fallback — never a silent "unknown" with nothing behind it
// (satelle-agent-agnostic §2).
func ReasonForNoModel(harness string) string {
	h := strings.TrimSpace(harness)
	if h == "" {
		h = HarnessUnknown
	}
	for _, a := range CapabilityTable() {
		if !strings.HasPrefix(a.Adapter, h+" ") {
			continue
		}
		if !a.ModelInheritance.Available {
			return a.ModelInheritance.Reason
		}
	}
	return h + ": model not reported by hook payload"
}

// capabilityColumns are the table headings, in cell order.
var capabilityColumns = []string{"usage", "cache split", "resolved model", "model inheritance", "live session", "tool trim", "offered tools", "cloud launch"}

// cells returns the row's cells in capabilityColumns order.
func (a AdapterCapabilities) cells() []Capability {
	return []Capability{a.Usage, a.CacheSplit, a.ResolvedModel, a.ModelInheritance, a.LiveSession, a.ToolTrim, a.OfferedTools, a.CloudLaunch}
}

// CapabilityTableMarkdown renders CapabilityTable as the markdown table
// `satelle help agent-dispatch` carries.
func CapabilityTableMarkdown() string {
	var b strings.Builder
	b.WriteString("| adapter | " + strings.Join(capabilityColumns, " | ") + " |\n")
	b.WriteString("| --- |" + strings.Repeat(" --- |", len(capabilityColumns)) + "\n")
	for _, a := range CapabilityTable() {
		fmt.Fprintf(&b, "| %s |", a.Adapter)
		for _, c := range a.cells() {
			fmt.Fprintf(&b, " %s |", c.Cell())
		}
		b.WriteString("\n")
	}
	return b.String()
}
