package web

import (
	"encoding/json"
	"html/template"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/workflow"
)

// The story detail page: the item's route drawn from its pushed workflow, its
// verdicts grouped by transition presentation, and its attached documents.
// Every name comes from the pushed rows; none is known here.

// routeVM is an item's workflow drawn for the item.
type routeVM struct {
	Workflow    string
	Missing     bool // the item names a workflow the partition holds no doc of
	States      []stateVM
	Transitions []transitionVM
}

// stateVM is one state of a route.
type stateVM struct {
	mirror.WorkflowState
	Current bool
	Visited bool
}

// transitionVM is one explicit transition of a route.
type transitionVM struct {
	From   []string
	To     string
	Groups []mirror.WorkflowGroup
	Taken  int  // times this item made the move
	Next   bool // leaves the item's current state
}

// presentationVM is one presentation of a transition and the gate verdicts it
// drew. Outcome is "moved", "refused" or "open" (presented, not moved).
type presentationVM struct {
	From, To string
	At       time.Time
	Outcome  string
	Verdicts []verdictVM
}

// verdictVM is one gate.run result.
type verdictVM struct {
	mirror.GateRun
	At       time.Time
	Duration string
}

// attachmentVM is one attached document with its body rendered.
type attachmentVM struct {
	mirror.Attachment
	Anchor string
	Short  string // the sha256's first 12 digits
	HTML   template.HTML
	Text   string // a non-markdown text body, shown preformatted
}

// Presentation outcomes.
const (
	outcomeMoved   = "moved"
	outcomeRefused = "refused"
	outcomeOpen    = "open"
)

// stateClass reports a state's class for list badges: start, terminal, park
// or active ("" when the workflow is unknown or lacks the state).
func stateClass(w *mirror.Workflow, status string) string {
	s, ok := workflowState(w, status)
	switch {
	case !ok:
		return ""
	case s.Terminal:
		return "terminal"
	case s.Park:
		return "park"
	case s.Start:
		return "start"
	}
	return "active"
}

// workflowState is the state named status in w; ok is false when the workflow
// is unknown or lacks it.
func workflowState(w *mirror.Workflow, status string) (mirror.WorkflowState, bool) {
	if w == nil {
		return mirror.WorkflowState{}, false
	}
	for _, s := range w.States {
		if s.Name == status {
			return s, true
		}
	}
	return mirror.WorkflowState{}, false
}

// transitionOf is an item.transitioned event's move; ok is false for any other
// event or an unreadable payload.
func transitionOf(e Entry) (from, to string, ok bool) {
	if e.Kind != mirror.EventTransitioned {
		return "", "", false
	}
	var m struct{ From, To string }
	if json.Unmarshal(e.Payload, &m) != nil {
		return "", "", false
	}
	return m.From, m.To, true
}

// moves returns the item's transitions from its events, oldest first.
func moves(events []Entry) [][2]string {
	var out [][2]string
	for _, e := range events {
		if from, to, ok := transitionOf(e); ok {
			out = append(out, [2]string{from, to})
		}
	}
	return out
}

// buildRoute draws w for an item at status with the given events.
func buildRoute(w *mirror.Workflow, name, status string, events []Entry) routeVM {
	r := routeVM{Workflow: name}
	if w == nil {
		r.Missing = true
		return r
	}
	byName := map[string]mirror.WorkflowState{}
	for _, s := range w.States {
		byName[s.Name] = s
	}
	open := func(s string) bool { st := byName[s]; return !st.Terminal && !st.Park }
	from := func(t mirror.WorkflowTransition, s string) bool {
		return slices.Contains(t.From, s) || (slices.Contains(t.From, workflow.AnyState) && open(s))
	}
	// Order: breadth first from the start states along the transitions, then
	// any state not reached, park states last.
	var names []string
	for _, s := range w.States {
		names = append(names, s.Name)
	}
	sort.Strings(names)
	seen := map[string]bool{}
	var order, queue []string
	for _, n := range names {
		if byName[n].Start {
			queue = append(queue, n)
			seen[n] = true
		}
	}
	for len(queue) > 0 {
		s := queue[0]
		queue = queue[1:]
		order = append(order, s)
		for _, t := range w.Transitions {
			if _, ok := byName[t.To]; !ok || seen[t.To] || !from(t, s) {
				continue
			}
			seen[t.To] = true
			queue = append(queue, t.To)
		}
	}
	var parks []string
	for _, n := range names {
		if seen[n] {
			continue
		}
		if byName[n].Park {
			parks = append(parks, n)
			continue
		}
		order = append(order, n)
	}
	order = append(order, parks...)
	// Park states reached breadth first still go last.
	sort.SliceStable(order, func(i, j int) bool { return !byName[order[i]].Park && byName[order[j]].Park })

	taken := moves(events)
	visited := map[string]bool{status: true}
	for _, m := range taken {
		visited[m[0]], visited[m[1]] = true, true
	}
	for _, n := range order {
		r.States = append(r.States, stateVM{WorkflowState: byName[n], Current: n == status, Visited: visited[n]})
	}
	for _, t := range w.Transitions {
		vm := transitionVM{From: t.From, To: t.To, Groups: t.Groups, Next: from(t, status)}
		for _, m := range taken {
			if (m[1] == t.To || t.To == workflow.Prior) && from(t, m[0]) {
				vm.Taken++
			}
		}
		r.Transitions = append(r.Transitions, vm)
	}
	return r
}

// buildPresentations groups an item's gate.run events by the presentation of
// a transition they belong to, oldest first: a presentation ends at the move it
// made, at a verdict on another transition, or when a gate it judged is judged
// again. A move made with no gate is a presentation with no verdicts.
func buildPresentations(events []Entry) []presentationVM {
	var out []presentationVM
	cur := -1 // index of the presentation still open, -1 when none
	key := ""
	for _, e := range events {
		switch e.Kind {
		case mirror.EventGateRun:
			var g mirror.GateRun
			if json.Unmarshal(e.Payload, &g) != nil {
				continue
			}
			// A gate judged again is the transition presented again.
			if cur < 0 || key != g.Transition || judged(out[cur], g.Gate) {
				closeOpen(out, cur)
				from, to, _ := strings.Cut(g.Transition, mirror.TransitionSep)
				out = append(out, presentationVM{From: from, To: to, At: e.At})
				cur, key = len(out)-1, g.Transition
			}
			out[cur].Verdicts = append(out[cur].Verdicts, verdictVM{GateRun: g, At: e.At, Duration: humanMs(g.DurationMS)})
		case mirror.EventTransitioned:
			from, to, ok := transitionOf(e)
			if !ok {
				continue
			}
			if cur >= 0 && key == from+mirror.TransitionSep+to {
				out[cur].Outcome = outcomeMoved
			} else {
				closeOpen(out, cur)
				out = append(out, presentationVM{From: from, To: to, At: e.At, Outcome: outcomeMoved})
			}
			cur, key = -1, ""
		}
	}
	closeOpen(out, cur)
	return out
}

// judged reports whether gate already has a verdict in p.
func judged(p presentationVM, gate string) bool {
	for _, v := range p.Verdicts {
		if v.Gate == gate {
			return true
		}
	}
	return false
}

// closeOpen settles the outcome of the open presentation at i: refused when a
// gate rejected, else open.
func closeOpen(ps []presentationVM, i int) {
	if i < 0 || ps[i].Outcome != "" {
		return
	}
	ps[i].Outcome = outcomeOpen
	for _, v := range ps[i].Verdicts {
		if v.Result == mirror.GateReject {
			ps[i].Outcome = outcomeRefused
		}
	}
}

// attachmentView renders one attachment: markdown as HTML, other text as
// preformatted text, anything else by its metadata only.
func attachmentView(a mirror.Attachment) attachmentVM {
	if a.MediaType == "" {
		a.MediaType = a.Type
	}
	vm := attachmentVM{Attachment: a, Anchor: docAnchor(a.Name), Short: a.SHA256}
	if len(vm.Short) > 12 {
		vm.Short = vm.Short[:12]
	}
	if a.Body == "" {
		return vm
	}
	mt := strings.ToLower(a.MediaType)
	if strings.Contains(mt, "markdown") || strings.HasSuffix(strings.ToLower(a.Name), ".md") {
		vm.HTML = renderMarkdown(stripDocFrontmatter(a.Body))
	} else {
		vm.Text = a.Body
	}
	return vm
}

// childLink is a child item of the detail page's item.
type childLink struct {
	ID, Kind, Title, Status string
}
