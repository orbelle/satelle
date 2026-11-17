package gate

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/bobmcallan/satelle/internal/agent"
	"github.com/bobmcallan/satelle/internal/workflow"
)

type notes struct {
	mu    sync.Mutex
	calls []string
}

func (n *notes) add(s string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = append(n.calls, s)
}

func (n *notes) list() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.calls)
}

func notify(r *Run, n *notes, failAt int) {
	r.Started = func(seq int, name string) error {
		if seq == failAt {
			n.add(fmt.Sprintf("refused %d %s", seq, name))
			return errors.New("refused")
		}
		n.add(fmt.Sprintf("start %d %s", seq, name))
		return nil
	}
	r.Finished = func(seq int, res Result) { n.add(fmt.Sprintf("finish %d %s", seq, res.Gate)) }
}

func TestExecuteNotifiesStartAndFinishInDeclaredPosition(t *testing.T) {
	gates := map[string]workflow.Gate{"a": {Skill: "judge", Agent: "y1"}, "b": {Skill: "judge", Agent: "y2"}}
	r, _ := setup([]workflow.Group{{Mode: workflow.Sequential, Gates: []string{"a", "b"}}}, gates)
	var n notes
	notify(&r, &n, 0)
	if _, pass := r.Execute(context.Background()); !pass {
		t.Fatal("sequential: want pass")
	}
	if got, want := n.list(), []string{"start 1 a", "finish 1 a", "start 2 b", "finish 2 b"}; !slices.Equal(got, want) {
		t.Errorf("sequential calls %v, want %v", got, want)
	}

	r, d := setup([]workflow.Group{{Mode: workflow.Sequential, Gates: []string{"a", "b"}}}, gates)
	n = notes{}
	notify(&r, &n, 2)
	rs, pass := r.Execute(context.Background())
	if pass || results(rs) != "a=accept" || !slices.Equal(d.calls, []string{"y1"}) {
		t.Errorf("a refused start: pass %v, results %s, judges %v; want false, a=accept, [y1]", pass, results(rs), d.calls)
	}

	two := []workflow.Group{{Mode: workflow.Sequential, Gates: []string{"a", "b"}}, {Mode: workflow.Sequential, Gates: []string{"b", "a"}}}
	r, _ = setup(two, gates)
	n = notes{}
	notify(&r, &n, 0)
	r.Execute(context.Background())
	var starts []string
	for _, c := range n.list() {
		if c[:5] == "start" {
			starts = append(starts, c)
		}
	}
	if want := []string{"start 1 a", "start 2 b", "start 3 b", "start 4 a"}; !slices.Equal(starts, want) {
		t.Errorf("positions across groups %v, want %v", starts, want)
	}
}

func TestExecuteParallelStopsAtARefusedStart(t *testing.T) {
	gates := map[string]workflow.Gate{"a": {Skill: "judge", Agent: "y1"}, "b": {Skill: "judge", Agent: "y2"}, "c": {Skill: "judge", Agent: "n1"}}
	r, d := setup([]workflow.Group{{Mode: workflow.Parallel, Gates: []string{"a", "b", "c"}}}, gates)
	var n notes
	notify(&r, &n, 2)
	rs, pass := r.Execute(context.Background())
	if pass || results(rs) != "a=accept" {
		t.Errorf("pass %v results %s, want false and a=accept", pass, results(rs))
	}
	if !slices.Equal(d.calls, []string{"y1"}) {
		t.Errorf("judges %v, want only a's (y1)", d.calls)
	}
	got := n.list()
	if !slices.Contains(got, "finish 1 a") || slices.ContainsFunc(got, func(s string) bool { return s == "start 3 c" || s == "refused 3 c" }) {
		t.Errorf("calls %v: want a finished and c never started", got)
	}
}

type holdFirst struct {
	mu       sync.Mutex
	first    bool
	finished chan struct{}
}

func (h *holdFirst) Dispatch(ctx context.Context, role agent.Role, req agent.Request) (agent.Result, error) {
	h.mu.Lock()
	wait := !h.first
	h.first = true
	h.mu.Unlock()
	if wait {
		<-h.finished
	}
	return agent.Result{Text: yes}, nil
}

func TestExecuteParallelReportsEachSameNamedGateAsItFinishes(t *testing.T) {
	gates := map[string]workflow.Gate{"g": {Skill: "judge", Agent: "y1"}}
	r, _ := setup([]workflow.Group{{Mode: workflow.Parallel, Gates: []string{"g", "g"}}}, gates)
	h := &holdFirst{finished: make(chan struct{})}
	r.Dispatcher = h
	var once sync.Once
	var seqs []int
	var mu sync.Mutex
	r.Finished = func(seq int, res Result) {
		mu.Lock()
		seqs = append(seqs, seq)
		mu.Unlock()
		once.Do(func() { close(h.finished) })
	}
	rs, pass := r.Execute(context.Background())
	if !pass || len(rs) != 2 {
		t.Fatalf("pass %v, %d results", pass, len(rs))
	}
	slices.Sort(seqs)
	if !slices.Equal(seqs, []int{1, 2}) {
		t.Errorf("finished seqs %v, want [1 2]", seqs)
	}
}
