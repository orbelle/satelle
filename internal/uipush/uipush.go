package uipush

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/bobmcallan/satelle/internal/cache"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/workflow"
)

const (
	UIFile    = "ui.toml"
	uiSection = "ui"
)

const MaxBody = 256 << 10

type Source struct {
	Ledger    *ledger.Store
	Cache     *cache.Cache
	Workflows map[string]*workflow.Workflow
	DataDir   string
	RepoRoot  string
	RepoKey   string
	Slug      string
	User      *mirror.User
	Queues    map[string]*workflow.Queue
}

func Full(src Source) (mirror.Snapshot, error) {
	items, err := src.Cache.Items(cache.Query{})
	if err != nil {
		return mirror.Snapshot{}, err
	}
	snap, err := rows(src, items)
	if err != nil {
		return snap, err
	}
	if snap.Docs, err = workflowDocs(src); err != nil {
		return snap, err
	}
	if snap.Settings, err = settings(src); err != nil {
		return snap, err
	}
	snap.Identity = identity(src)
	return snap, nil
}

func identity(src Source) json.RawMessage {
	return mustJSON(mirror.IdentityMeta{ProjectName: src.Slug, RepoRoot: src.RepoRoot, User: src.User})
}

func Delta(src Source, ids []string) (mirror.Snapshot, error) {
	var items []item.Item
	for _, id := range ids {
		it, ok, err := src.Cache.Item(id)
		if err != nil {
			return mirror.Snapshot{}, err
		}
		if ok {
			items = append(items, it)
		}
	}
	snap, err := rows(src, items)
	if err != nil {
		return snap, err
	}
	if snap.Docs, err = workflowDocs(src); err != nil {
		return snap, err
	}
	snap.Kinds = []string{"seat", "identity", "doc"}
	snap.Identity = identity(src)
	snap.MergeKinds = []string{"story", "task", "execution", "queue", "ledger_event", "story_doc"}
	return snap, nil
}

func rows(src Source, items []item.Item) (mirror.Snapshot, error) {
	snap := mirror.Snapshot{RepoKey: src.RepoKey, Slug: src.Slug}
	for _, it := range items {
		if def := src.Queues[it.ID]; def != nil && it.Kind == item.QueueKind {
			def.Apply(&it)
		}
		row := mustJSON(itemRow(it))
		switch it.Kind {
		case "task":
			snap.Tasks = append(snap.Tasks, row)
		case "execution":
			snap.Executions = append(snap.Executions, row)
		case item.QueueKind:
			snap.Queues = append(snap.Queues, row)
		default:
			snap.Stories = append(snap.Stories, row)
		}
		events, err := src.Cache.Events(it.ID, "")
		if err != nil {
			return snap, err
		}
		for _, e := range events {
			snap.LedgerEvents = append(snap.LedgerEvents, mustJSON(mirror.Event{
				ID: e.ID, Item: e.Item, Kind: e.Kind, At: e.At, Actor: e.Actor, Payload: e.Payload, Prev: e.Prev, Hash: e.Hash,
			}))
		}
		atts, err := src.Cache.Attachments(it.ID)
		if err != nil {
			return snap, err
		}
		latest, versions := item.LatestAttachments(atts)
		for _, a := range latest {
			snap.StoryDocs = append(snap.StoryDocs, mustJSON(attachmentRow(src.Ledger, a, versions[a.Name])))
		}
	}
	seats, err := src.Cache.Seats("", "")
	if err != nil {
		return snap, err
	}
	for _, s := range seats {
		snap.Seats = append(snap.Seats, mustJSON(mirror.Seat{
			ID: s.Item + "/" + s.Session, Item: s.Item, Session: s.Session, Location: s.Location, Base: s.Base, BoundAt: s.BoundAt,
			Machine: s.Machine, Host: s.Host, User: s.User,
		}))
	}
	return snap, nil
}

func itemRow(it item.Item) mirror.Item {
	tags := it.Tags
	if tags == nil {
		tags = []string{}
	}
	return mirror.Item{
		ID: it.ID, Kind: it.Kind, Title: it.Title, Body: it.Body, Status: it.Status, Criteria: it.Criteria,
		Tags: tags, ParentID: it.Parent, Category: it.Category, Workflow: it.Workflow, Assignees: it.Assignees,
		Inputs: it.Inputs, Trigger: it.Trigger, Repo: it.Repo, CreatedAt: it.CreatedAt, UpdatedAt: it.UpdatedAt,
	}
}

func attachmentRow(l *ledger.Store, a item.Attachment, versions int) mirror.Attachment {
	row := mirror.Attachment{
		ID: a.Item + "/" + a.Name, Item: a.Item, Name: a.Name, MediaType: a.MediaType,
		Size: a.Size, SHA256: a.SHA256, At: a.At, Actor: a.Actor, Versions: versions,
	}
	if a.Size == 0 {
		return row
	}
	row.BodyOmitted = true
	if a.Size > MaxBody || !textual(a.MediaType, a.Name) {
		return row
	}
	if b, err := l.GetBlob(a.SHA256); err == nil {
		row.Body, row.BodyOmitted = string(b), false
	}
	return row
}

func textual(mediaType, name string) bool {
	mt := strings.ToLower(mediaType)
	if strings.HasPrefix(mt, "text/") {
		return true
	}
	for _, s := range []string{"json", "markdown", "yaml", "toml", "xml"} {
		if strings.Contains(mt, s) {
			return true
		}
	}
	if mt == "" {
		switch strings.ToLower(filepath.Ext(name)) {
		case ".md", ".txt", ".json", ".yaml", ".yml", ".toml", ".log", ".diff", ".patch":
			return true
		}
	}
	return false
}

func workflowDocs(src Source) ([]json.RawMessage, error) {
	names := make([]string, 0, len(src.Workflows))
	for n := range src.Workflows {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []json.RawMessage
	for _, n := range names {
		w := src.Workflows[n]
		doc := mirror.Doc{Kind: mirror.DocKindWorkflows, Name: n, Workflow: wireWorkflow(w)}
		if w.File != "" {
			if w.Part != "" {
				doc.Body = string(w.Source)
				if st, err := os.Lstat(w.File); err == nil {
					doc.ModTime = st.ModTime().UTC()
				}
			} else {
				if b, err := os.ReadFile(w.File); err == nil {
					doc.Body = string(b)
				}
				if st, err := os.Stat(w.File); err == nil {
					doc.ModTime = st.ModTime().UTC()
				}
			}
			doc.Source = w.File
			if rel, err := filepath.Rel(src.RepoRoot, w.File); err == nil && !strings.HasPrefix(rel, "..") {
				doc.Source = filepath.ToSlash(rel)
			}
		}
		out = append(out, mustJSON(doc))
	}
	return out, nil
}

func wireWorkflow(w *workflow.Workflow) *mirror.Workflow {
	out := &mirror.Workflow{Name: w.Name, Part: w.Part, Panel: w.PanelName(), States: []mirror.WorkflowState{}, Transitions: []mirror.WorkflowTransition{}}
	for _, n := range sortedKeys(w.States) {
		s := w.States[n]
		ws := mirror.WorkflowState{Name: n, Start: s.Start, Terminal: s.Terminal, Park: s.Park, Resumes: s.Resumes}
		if _, ok := s.Operated(); ok {
			ws.PerformerAgent = "operator"
		} else if p := s.Agent(); p != nil {
			ws.PerformerAgent, ws.PerformerSkill = p.Agent, p.Skill
		}
		out.States = append(out.States, ws)
	}
	for _, n := range sortedKeys(w.Gates) {
		g := w.Gates[n]
		out.Gates = append(out.Gates, mirror.WorkflowGate{
			Name: n, Skill: g.Skill, Judges: g.Judges(), Combine: g.Combine, Paths: g.Paths, Trigger: g.Trigger, Carry: g.Carry,
		})
	}
	for _, t := range w.Transitions {
		wt := mirror.WorkflowTransition{From: t.From, To: t.To}
		for _, g := range t.Groups {
			wt.Groups = append(wt.Groups, mirror.WorkflowGroup{Mode: g.Mode, Gates: g.Gates})
		}
		out.Transitions = append(out.Transitions, wt)
	}
	return out
}

func settings(src Source) (json.RawMessage, error) {
	blob := mirror.Settings{RepoRoot: src.RepoRoot, Rows: []mirror.SettingRow{}}
	var m map[string]any
	if _, err := toml.DecodeFile(filepath.Join(src.DataDir, "satelle.toml"), &m); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("uipush: settings: %w", err)
	}
	var ui map[string]any
	if _, err := toml.DecodeFile(filepath.Join(src.DataDir, UIFile), &ui); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("uipush: settings: %w", err)
	}
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		if sub, ok := v.(map[string]any); ok {
			for _, k := range sortedKeys(sub) {
				walk(strings.TrimPrefix(prefix+"."+k, "."), sub[k])
			}
			return
		}
		section, label := "", prefix
		if i := strings.IndexByte(prefix, '.'); i > 0 {
			section = prefix[:i]
		}
		if i := strings.LastIndexByte(prefix, '.'); i >= 0 {
			label = prefix[i+1:]
		}
		value := fmt.Sprint(v)
		if _, scalar := v.(string); !scalar {
			if b, err := json.Marshal(v); err == nil {
				value = string(b)
			}
		}
		blob.Rows = append(blob.Rows, mirror.SettingRow{FieldID: prefix, Label: label, Value: value, Section: section})
	}
	walk("", m)
	if len(ui) > 0 {
		walk(uiSection, ui)
	}
	return mustJSON(blob), nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
