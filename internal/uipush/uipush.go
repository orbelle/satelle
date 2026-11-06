// Package uipush is the CLI's side of satelled: after a kernel mutation it
// tells satelled what changed (POST /ingest/change) and pushes the rows its
// pages read (POST /ingest/snapshot): folded items from the cache, the items'
// ledger events, attachment metadata and text bodies, seats, the workflows,
// settings and identity. satelled never asks the CLI for anything.
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

// MaxBody bounds an attachment body pushed to satelled; a larger or binary
// attachment is pushed as metadata only.
const MaxBody = 256 << 10

// Source is what a push reads: one repo's opened ledger and cache, its
// workflows and where it lives.
type Source struct {
	Ledger    *ledger.Store
	Cache     *cache.Cache
	Workflows map[string]*workflow.Workflow
	DataDir   string // the repo's .satelle directory
	RepoRoot  string // the repo's canonical root
	RepoKey   string
	Slug      string
	User      *mirror.User // who the CLI is signed in as; nil when signed out
	// Queues are the queue files by the chain id each defines; a queue row
	// carries its file's title, filter and trigger.
	Queues map[string]*workflow.Queue
}

// Full is the snapshot that replaces every kind of the partition.
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

// identity is the partition's identity blob: project name, repo path and who
// the CLI is signed in as. Every push carries it, full and delta alike, so a
// sign-in or sign-out shows on satelled's next change.
func identity(src Source) json.RawMessage {
	return mustJSON(mirror.IdentityMeta{ProjectName: src.Slug, RepoRoot: src.RepoRoot, User: src.User})
}

// Delta is the snapshot that merges the rows of the named items (and their
// events and attachments) and replaces the seats and the identity.
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
	snap.Kinds = []string{"seat", "identity"}
	snap.Identity = identity(src)
	snap.MergeKinds = []string{"story", "task", "execution", "queue", "ledger_event", "story_doc"}
	return snap, nil
}

// rows fills the per-item kinds of a snapshot for items, and the seats.
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
		Tags: tags, ParentID: it.Parent, Category: it.Category, Workflow: it.Workflow, Assignee: it.Assignee,
		Inputs: it.Inputs, Trigger: it.Trigger, CreatedAt: it.CreatedAt, UpdatedAt: it.UpdatedAt,
	}
}

// attachmentRow is an attachment's metadata (the newest of versions), with its
// body when it is text within MaxBody.
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

// textual reports whether a media type (or, lacking one, a name) is text.
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

// workflowDocs is each workflow as a doc of kind mirror.DocKindWorkflows: its
// source and its parsed states, gates and transitions.
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
			if b, err := os.ReadFile(w.File); err == nil {
				doc.Body = string(b)
			}
			if st, err := os.Stat(w.File); err == nil {
				doc.ModTime = st.ModTime().UTC()
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
	out := &mirror.Workflow{Name: w.Name, States: []mirror.WorkflowState{}, Transitions: []mirror.WorkflowTransition{}}
	for _, n := range sortedKeys(w.States) {
		s := w.States[n]
		ws := mirror.WorkflowState{Name: n, Start: s.Start, Terminal: s.Terminal, Park: s.Park, Resumes: s.Resumes}
		if s.Performer != nil {
			ws.PerformerAgent, ws.PerformerSkill = s.Performer.Agent, s.Performer.Skill
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

// settings is satelle.toml flattened to one row per key, in key order.
func settings(src Source) (json.RawMessage, error) {
	blob := mirror.Settings{RepoRoot: src.RepoRoot, Rows: []mirror.SettingRow{}}
	var m map[string]any
	if _, err := toml.DecodeFile(filepath.Join(src.DataDir, "satelle.toml"), &m); err != nil && !os.IsNotExist(err) {
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
		panic(err) // every value here is plain data
	}
	return b
}
