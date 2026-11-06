package upgrade

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/bobmcallan/satelle/internal/workflow"
)

// Target is where a mapped status puts an item.
type Target struct {
	Workflow string `toml:"workflow"`
	State    string `toml:"state"`
}

// StatusMap is the operator-confirmed map from old status to v1 workflow and
// state. A "<kind>/<status>" key wins over a bare "<status>" key.
type StatusMap struct {
	Status map[string]Target `toml:"status"`
	Path   string            `toml:"-"`
	SHA256 string            `toml:"-"`
}

// LoadMap reads a status map strictly (an unknown key refuses).
func LoadMap(path string) (*StatusMap, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	m := &StatusMap{}
	if err := workflow.DecodeStrict(path, m); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	m.Path, m.SHA256 = path, hex.EncodeToString(sum[:])
	return m, nil
}

// Resolve gives the target for an item's kind and old status, and the rule
// key that decided it.
func (m *StatusMap) Resolve(kind, status string) (Target, string, bool) {
	if t, ok := m.Status[kind+"/"+status]; ok {
		return t, kind + "/" + status, true
	}
	t, ok := m.Status[status]
	return t, status, ok
}

// check reports every map entry whose workflow or state the repo lacks.
func (m *StatusMap) check(workflows map[string]*workflow.Workflow) []string {
	var bad []string
	keys := make([]string, 0, len(m.Status))
	for k := range m.Status {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t := m.Status[k]
		w, ok := workflows[t.Workflow]
		switch {
		case !ok:
			bad = append(bad, fmt.Sprintf("%s -> %s/%s: no workflow %q in this .satelle", k, t.Workflow, t.State, t.Workflow))
		case !hasState(w, t.State):
			bad = append(bad, fmt.Sprintf("%s -> %s/%s: workflow %s has no state %q", k, t.Workflow, t.State, t.Workflow, t.State))
		}
	}
	return bad
}

// unmapped lists the kind/status pairs the map does not name, with counts.
func (m *StatusMap) unmapped(items []OldItem) []string {
	count := map[string]int{}
	for _, it := range items {
		if _, _, ok := m.Resolve(it.Kind, it.Status); !ok {
			count[it.Kind+" "+it.Status]++
		}
	}
	var out []string
	for k, n := range count {
		out = append(out, fmt.Sprintf("%s: %d item(s)", k, n))
	}
	sort.Strings(out)
	return out
}

// RuleCount is one mapping rule's line in a plan.
type RuleCount struct {
	Key, Kind, Status string
	Target            Target
	Items             int
}

func (r RuleCount) String() string {
	return fmt.Sprintf("%s %s -> %s/%s  %d item(s)", r.Kind, r.Status, r.Target.Workflow, r.Target.State, r.Items)
}

func ruleCounts(m *StatusMap, items []OldItem) []RuleCount {
	idx := map[string]*RuleCount{}
	var keys []string
	for _, it := range items {
		t, key, ok := m.Resolve(it.Kind, it.Status)
		if !ok {
			continue
		}
		k := it.Kind + "\x00" + it.Status
		if idx[k] == nil {
			idx[k] = &RuleCount{Key: key, Kind: it.Kind, Status: it.Status, Target: t}
			keys = append(keys, k)
		}
		idx[k].Items++
	}
	sort.Strings(keys)
	out := make([]RuleCount, 0, len(keys))
	for _, k := range keys {
		out = append(out, *idx[k])
	}
	return out
}

func joinLines(prefix string, ss []string) string {
	return prefix + strings.Join(ss, "\n"+prefix)
}

func hasState(w *workflow.Workflow, s string) bool {
	_, ok := w.States[s]
	return ok
}
