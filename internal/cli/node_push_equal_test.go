package cli

import (
	"sort"

	"github.com/bobmcallan/satelle/internal/hosted"
)

// nodeRun is one task run in the store: its execution (with a recorded run
// output), the subject story it ran about, the subject's execution_link and a
// second row on the subject that is neither an execution's nor a link (the
// evt_8544ab66 shape).
type nodeRun struct{ exec, subject, extra string }

// nodeClient is a client acting under the node token, as the node builds it.
func nodeClient(server, token string) *hosted.Client {
	return hosted.NewClient(server, hosted.NodeTokenStore{Server: server, Token: token}, nil)
}

// sentRow is one item or ledger row as an Apply carried it.
type sentRow struct{ ID, Record string }

// sentOutput is one run output as an Apply carried it.
type sentOutput struct{ Execution, Name, Body string }

// sentEvent is one event as an Append carried it.
type sentEvent struct{ Project, Story, Base, ID, Workflow, Payload string }

// sent is every payload a server received, apart from its metadata.
type sent struct {
	Items, Ledger []sentRow
	Outputs       []sentOutput
	Events        []sentEvent
	Projects      []string
}

func sentTo(s *nodeSync) sent {
	var out sent
	for _, req := range s.applies() {
		out.Projects = append(out.Projects, req.GetProject())
		for _, it := range req.GetItems() {
			out.Items = append(out.Items, sentRow{it.GetId(), string(it.GetRecord())})
		}
		for _, e := range req.GetLedger() {
			out.Ledger = append(out.Ledger, sentRow{e.GetId(), string(e.GetRecord())})
		}
		for _, o := range req.GetOutputs() {
			out.Outputs = append(out.Outputs, sentOutput{o.GetExecution(), o.GetName(), string(o.GetBody())})
		}
	}
	for _, req := range s.appends() {
		for _, e := range req.GetEvents() {
			out.Events = append(out.Events, sentEvent{req.GetProject(), req.GetStoryId(), e.GetBaseId(), e.GetId(), e.GetWorkflowVersion(), string(e.GetPayload())})
		}
	}
	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].ID < out.Items[j].ID })
	sort.Slice(out.Ledger, func(i, j int) bool { return out.Ledger[i].ID < out.Ledger[j].ID })
	sort.Slice(out.Outputs, func(i, j int) bool { return out.Outputs[i].Execution < out.Outputs[j].Execution })
	sort.SliceStable(out.Events, func(i, j int) bool { return out.Events[i].Story < out.Events[j].Story })
	return out
}

// bearers is the distinct authorization each server's calls carried.
func bearers(s *nodeSync) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range s.rec.all() {
		if !seen[c.Auth] {
			seen[c.Auth] = true
			out = append(out, c.Auth)
		}
	}
	return out
}
