package upgrade

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/ledger"
)

const (
	// KindImported carries one old history row, uninterpreted.
	KindImported = "upgrade.imported"
	// KindMapped records the mapping applied to an imported item; it is
	// written last, so its presence marks the item as fully imported.
	KindMapped = "upgrade.mapped"
	// importActor is the actor of the events rebuilt from the old store.
	importActor = "upgrade"
)

// Imported is the upgrade.imported payload: an old history row as it was.
type Imported struct {
	ID           string          `json:"id"`
	Kind         string          `json:"kind"`
	Actor        string          `json:"actor,omitempty"`
	Body         string          `json:"body,omitempty"`
	Payload      json.RawMessage `json:"payload,omitempty"`
	PayloadText  string          `json:"payload_text,omitempty"` // a payload that was not JSON
	Refs         json.RawMessage `json:"refs,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
	BaseID       string          `json:"base_id,omitempty"`
	WorkflowHash string          `json:"workflow_hash,omitempty"`
}

// Mapped is the upgrade.mapped payload.
type Mapped struct {
	Source    string            `json:"source"`
	Map       string            `json:"map"`
	MapSHA256 string            `json:"map_sha256"`
	Rule      string            `json:"rule"`
	OldStatus string            `json:"old_status"`
	Workflow  string            `json:"workflow"`
	State     string            `json:"state"`
	Old       map[string]string `json:"old,omitempty"`
}

// itemPlan is one old item's planned chain.
type itemPlan struct {
	old    OldItem
	target Target
	rule   string
	events []ledger.Event
	docs   []OldDoc
	state  string // "import", "resume" or "already"
}

// Plan is everything an import would do, checked before any write.
type Plan struct {
	Source   *Source
	Map      *StatusMap
	Rules    []RuleCount
	items    []itemPlan
	Import   int // items to write (new or resumed)
	Already  int // items fully imported by an earlier run
	History  int // history rows to import
	Docs     int // documents to import
	Warnings []string
}

// Refusal is a plan that cannot be applied; nothing was written.
type Refusal struct{ Reasons []string }

func (r *Refusal) Error() string {
	return "satelle upgrade refused; nothing was written:\n" + joinLines("  ", r.Reasons)
}

// eventID is deterministic, so a re-run rebuilds identical events.
func eventID(itemID, key string) string {
	h := sha256.Sum256([]byte("satelle-upgrade\x00" + itemID + "\x00" + key))
	return hex.EncodeToString(h[:12])
}

// PlanImport reads the source and the map and builds every item's chain,
// refusing (before any write) unmapped statuses, map targets the repo lacks,
// unreadable rows and chains that conflict with the ledger.
func PlanImport(k *kernel.Kernel, src *Source, m *StatusMap) (*Plan, error) {
	var reasons []string
	reasons = append(reasons, src.Problems...)
	if u := m.unmapped(src.Items); len(u) > 0 {
		reasons = append(reasons, "the status map names no target for:")
		for _, s := range u {
			reasons = append(reasons, "  "+s)
		}
	}
	reasons = append(reasons, m.check(k.Workflows)...)
	if len(reasons) > 0 {
		return nil, &Refusal{Reasons: reasons}
	}
	p := &Plan{Source: src, Map: m, Rules: ruleCounts(m, src.Items)}
	ids := map[string]bool{}
	for _, it := range src.Items {
		ids[it.ID] = true
	}
	var conflicts []string
	for _, it := range src.Items {
		ip, err := planItem(it, src, m)
		if err != nil {
			return nil, err
		}
		if it.Parent != "" && !ids[it.Parent] {
			p.Warnings = append(p.Warnings, fmt.Sprintf("%s: parent %s is not in the old store; kept as written", it.ID, it.Parent))
		}
		have, err := k.Ledger.Read(it.ID)
		if err != nil {
			return nil, err
		}
		switch {
		case hasMapped(have):
			ip.state = "already"
			p.Already++
		case len(have) == 0:
			ip.state = "import"
		case isPrefix(have, ip.events):
			ip.state = "resume"
		default:
			conflicts = append(conflicts, fmt.Sprintf("%s: the ledger already holds a different chain for this id", it.ID))
		}
		if ip.state != "already" {
			p.Import++
			p.History += len(src.Events[it.ID])
			p.Docs += len(src.Docs[it.ID])
		}
		p.items = append(p.items, ip)
	}
	if len(conflicts) > 0 {
		return nil, &Refusal{Reasons: conflicts}
	}
	return p, nil
}

func hasMapped(evs []ledger.Event) bool {
	for _, e := range evs {
		if e.Kind == KindMapped {
			return true
		}
	}
	return false
}

func isPrefix(have, planned []ledger.Event) bool {
	if len(have) > len(planned) {
		return false
	}
	for i := range have {
		if have[i].Hash != planned[i].Hash {
			return false
		}
	}
	return true
}

// planItem builds an item's chain: item.created at its old time, then its
// history rows and documents in time order.
func planItem(it OldItem, src *Source, m *StatusMap) (itemPlan, error) {
	t, rule, _ := m.Resolve(it.Kind, it.Status)
	ip := itemPlan{old: it, target: t, rule: rule, docs: src.Docs[it.ID]}
	crit := SplitCriteria(it.Criteria)
	f := item.Fields{Kind: &it.Kind, Title: &it.Title, Body: &it.Body, Status: &t.State, Workflow: &t.Workflow}
	if len(crit) > 0 {
		f.Criteria = &crit
	}
	if len(it.Tags) > 0 {
		f.Tags = &it.Tags
	}
	for _, s := range []struct {
		v   string
		dst **string
	}{{it.Parent, &f.Parent}, {it.Category, &f.Category}} {
		if s.v != "" {
			v := s.v
			*s.dst = &v
		}
	}
	if it.Assignee != "" {
		// The old store's single assignee is the list's one entry.
		f.Assignees = &[]string{it.Assignee}
	}
	type step struct {
		at      time.Time
		key     string
		kind    string
		actor   string
		payload any
	}
	steps := []step{{at: it.Created, key: "created", kind: item.KindCreated, actor: importActor, payload: f}}
	var rest []step
	for _, e := range src.Events[it.ID] {
		pl := Imported{ID: e.ID, Kind: e.Kind, Actor: e.Actor, Body: e.Body, CreatedAt: e.At, BaseID: e.BaseID, WorkflowHash: e.WorkflowHash}
		if raw := strings.TrimSpace(e.Payload); raw != "" && raw != "{}" {
			if json.Valid([]byte(raw)) {
				pl.Payload = json.RawMessage(raw)
			} else {
				pl.PayloadText = e.Payload
			}
		}
		if refs := strings.TrimSpace(e.Refs); refs != "" && refs != "[]" && json.Valid([]byte(refs)) {
			pl.Refs = json.RawMessage(refs)
		}
		actor := e.Actor
		if actor == "" {
			actor = importActor
		}
		rest = append(rest, step{at: e.At, key: "evidence:" + e.ID, kind: KindImported, actor: actor, payload: pl})
	}
	for _, d := range src.Docs[it.ID] {
		b, err := os.ReadFile(d.Path)
		if err != nil {
			return ip, err
		}
		sum := sha256.Sum256(b)
		a := item.Attachment{Name: d.Name, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(b)), MediaType: d.MediaType}
		rest = append(rest, step{at: d.At, key: "doc:" + d.Name, kind: item.KindAttachmentAdd, actor: importActor, payload: a})
	}
	sort.SliceStable(rest, func(i, j int) bool { return rest[i].at.Before(rest[j].at) })
	steps = append(steps, rest...)
	prev := ""
	for _, s := range steps {
		e, err := ledger.NewEventWith(eventID(it.ID, s.key), s.at, it.ID, s.kind, s.actor, s.payload, prev)
		if err != nil {
			return ip, fmt.Errorf("%s: %w", it.ID, err)
		}
		ip.events = append(ip.events, e)
		prev = e.Hash
	}
	return ip, nil
}

// Summary is what an import plan or run reports.
func (p *Plan) Summary(applied bool) string {
	var b strings.Builder
	for _, r := range p.Rules {
		fmt.Fprintln(&b, r.String())
	}
	verb := "to import"
	if applied {
		verb = "imported"
	}
	fmt.Fprintf(&b, "%d item(s) %s, %d already imported; %d history row(s), %d document(s); %d regenerated view(s) skipped, %d history row(s) naming no item skipped\n",
		p.Import, verb, p.Already, p.History, p.Docs, p.Source.Skipped, p.Source.Orphans)
	fmt.Fprintf(&b, "map %s sha256 %s\n", p.Map.Path, p.Map.SHA256)
	for _, w := range p.Warnings {
		fmt.Fprintln(&b, "warning: "+w)
	}
	return b.String()
}

// Apply writes the plan: each document's blob, then each item's chain, then
// its upgrade.mapped marker last (so an interrupted run resumes), then the
// cache catches up. actor records who ran the import.
func (p *Plan) Apply(k *kernel.Kernel, actor string) error {
	for _, ip := range p.items {
		if ip.state == "already" {
			continue
		}
		for _, d := range ip.docs {
			b, err := os.ReadFile(d.Path)
			if err != nil {
				return err
			}
			sha, err := k.Ledger.PutBlob(b)
			if err != nil {
				return err
			}
			back, err := k.Ledger.GetBlob(sha)
			if err != nil || string(back) != string(b) {
				return fmt.Errorf("%s: blob %s did not read back as %s", ip.old.ID, sha, d.Path)
			}
		}
		if _, err := k.Ledger.Import(ip.events); err != nil {
			return fmt.Errorf("%s: %w", ip.old.ID, err)
		}
		old := map[string]string{}
		for c, v := range ip.old.Extra {
			old[c] = v
		}
		for c, v := range map[string]string{"priority": ip.old.Priority, "acceptance_criteria": ip.old.Criteria} {
			if v != "" {
				old[c] = v
			}
		}
		if ip.old.Archived {
			old["archived"] = "1"
		}
		if !ip.old.Updated.IsZero() {
			old["updated_at"] = ip.old.Updated.Format(time.RFC3339Nano)
		}
		mp := Mapped{Source: p.Source.Dir, Map: p.Map.Path, MapSHA256: p.Map.SHA256, Rule: ip.rule, OldStatus: ip.old.Status,
			Workflow: ip.target.Workflow, State: ip.target.State, Old: old}
		if _, err := k.Ledger.Append(ip.old.ID, KindMapped, actor, mp); err != nil {
			return err
		}
	}
	return k.Cache.Sync(k.Ledger)
}

// IsRefusal reports whether err is a pre-write refusal.
func IsRefusal(err error) bool {
	var r *Refusal
	return errors.As(err, &r)
}
