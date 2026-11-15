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

type routeVM struct {
	Workflow    string
	Missing     bool
	States      []stateVM
	Transitions []transitionVM
}

type stateVM struct {
	mirror.WorkflowState
	Current bool
	Visited bool
}

type transitionVM struct {
	From   []string
	To     string
	Groups []mirror.WorkflowGroup
	Taken  int
	Next   bool
}

type presentationVM struct {
	From, To string
	At       time.Time
	Outcome  string
	Verdicts []verdictVM
	Attempt  string
}

type verdictVM struct {
	mirror.GateRun
	At       time.Time
	Duration string
}

type attachmentVM struct {
	mirror.Attachment
	Anchor string
	Short  string
	HTML   template.HTML
	Text   string
}

const (
	outcomeMoved   = "moved"
	outcomeRefused = "refused"
	outcomeOpen    = "open"
)

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

func moves(events []Entry) [][2]string {
	var out [][2]string
	for _, e := range events {
		if from, to, ok := transitionOf(e); ok {
			out = append(out, [2]string{from, to})
		}
	}
	return out
}

func leavesState(byName map[string]mirror.WorkflowState, t mirror.WorkflowTransition, s string) bool {
	if slices.Contains(t.From, s) {
		return true
	}
	st := byName[s]
	return slices.Contains(t.From, workflow.AnyState) && !st.Terminal && !st.Park
}

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
	from := func(t mirror.WorkflowTransition, s string) bool { return leavesState(byName, t, s) }
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

func buildPresentations(events []Entry) []presentationVM {
	var out []presentationVM
	cur := -1
	key := ""
	for _, e := range events {
		switch e.Kind {
		case mirror.EventGateRun:
			var g mirror.GateRun
			if json.Unmarshal(e.Payload, &g) != nil {
				continue
			}
			if cur < 0 || key != g.Transition || newRound(out[cur], g) {
				closeOpen(out, cur)
				from, to, _ := strings.Cut(g.Transition, mirror.TransitionSep)
				out = append(out, presentationVM{From: from, To: to, At: e.At, Attempt: g.Attempt})
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

func newRound(p presentationVM, g mirror.GateRun) bool {
	if g.Attempt != "" || p.Attempt != "" {
		return g.Attempt != p.Attempt
	}
	return judged(p, g.Gate)
}

func judged(p presentationVM, gate string) bool {
	for _, v := range p.Verdicts {
		if v.Gate == gate {
			return true
		}
	}
	return false
}

func closeOpen(ps []presentationVM, i int) {
	if i < 0 || ps[i].Outcome != "" {
		return
	}
	ps[i].Outcome = outcomeOpen
	for _, v := range ps[i].Verdicts {
		if v.Result == mirror.GateReject || v.Result == mirror.GateError {
			ps[i].Outcome = outcomeRefused
		}
	}
}

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

type childLink struct {
	ID, Kind, Title, Status string
}
