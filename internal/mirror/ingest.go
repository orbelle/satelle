package mirror

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

type IngestOrigin string

const (
	OriginLocal  IngestOrigin = ""
	OriginHosted IngestOrigin = "hosted"
)

const CodePartitionUnknown = "partition_unknown"

const EnvIngestOrigin = "SATELLE_INGEST_ORIGIN"

type ChangeEvent struct {
	RepoKey string       `json:"repo_key"`
	Topic   string       `json:"topic"`
	Entity  string       `json:"entity"`
	At      string       `json:"at"`
	Origin  IngestOrigin `json:"origin,omitempty"`
}

type ConfirmEvent struct {
	RepoKey string `json:"repo_key"`
	Digest  string `json:"digest"`
}

type RemoveEvent struct {
	RepoKey string `json:"repo_key"`
}

type Snapshot struct {
	RepoKey      string            `json:"repo_key"`
	Slug         string            `json:"slug,omitempty"`
	Stories      []json.RawMessage `json:"stories,omitempty"`
	Tasks        []json.RawMessage `json:"tasks,omitempty"`
	Executions   []json.RawMessage `json:"executions,omitempty"`
	Queues       []json.RawMessage `json:"queues,omitempty"`
	Docs         []json.RawMessage `json:"docs,omitempty"`
	LedgerEvents []json.RawMessage `json:"ledger_events,omitempty"`
	StoryDocs    []json.RawMessage `json:"story_docs,omitempty"`
	Seats        []json.RawMessage `json:"seats,omitempty"`
	Waves        []json.RawMessage `json:"waves,omitempty"`
	Settings     json.RawMessage   `json:"settings,omitempty"`
	Identity     json.RawMessage   `json:"identity,omitempty"`
	Kinds        []string          `json:"kinds,omitempty"`
	MergeKinds   []string          `json:"merge_kinds,omitempty"`
	Origin       IngestOrigin      `json:"origin,omitempty"`
}

type User struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

type IdentityMeta struct {
	ProjectName string      `json:"project_name"`
	RepoRoot    string      `json:"repo_root"`
	User        *User       `json:"user,omitempty"`
	Hosted      *HostedMeta `json:"hosted,omitempty"`
}

type HostedMeta struct {
	Server  string `json:"server"`
	Project string `json:"project"`
}

type IngestHandler struct {
	Store    *Store
	OnChange func(topic string)
	OnIngest func(repoKey string, origin IngestOrigin)
	Log      func(format string, args ...any)

	apply func(ctx context.Context, repoKey string, replace, merge []KindRows, now time.Time) error
}

const slowIngest = time.Second

type ingestTiming struct {
	start                 time.Time
	bytes                 int
	snap                  Snapshot
	read, apply, onIngest time.Duration
	err                   string
}

func (h *IngestHandler) logIngest(t *ingestTiming) {
	if h.Log == nil {
		return
	}
	total := time.Since(t.start)
	if t.err == "" && total <= slowIngest {
		return
	}
	s := t.snap
	line := fmt.Sprintf("ingest snapshot %s: bytes=%d stories=%d tasks=%d executions=%d docs=%d ledger=%d story_docs=%d seats=%d read=%s apply=%s on_ingest=%s total=%s",
		s.RepoKey, t.bytes, len(s.Stories), len(s.Tasks), len(s.Executions), len(s.Docs), len(s.LedgerEvents), len(s.StoryDocs), len(s.Seats),
		t.read, t.apply, t.onIngest, total)
	if t.err != "" {
		line += " err=" + t.err
	}
	h.Log("%s", line)
}

func (h *IngestHandler) applySnapshot(ctx context.Context, repoKey string, replace, merge []KindRows, now time.Time) error {
	if h.apply != nil {
		return h.apply(ctx, repoKey, replace, merge, now)
	}
	return h.Store.ApplySnapshot(ctx, repoKey, replace, merge, now)
}

func (h *IngestHandler) Mount(mux *http.ServeMux) {
	mux.HandleFunc("/ingest/change", h.handleChange)
	mux.HandleFunc("/ingest/snapshot", h.handleSnapshot)
	mux.HandleFunc("/ingest/remove", h.handleRemove)
	mux.HandleFunc("/ingest/confirm", h.handleConfirm)
}

func (h *IngestHandler) confirmFresh(ctx context.Context, repoKey, digest string, now time.Time) (bool, error) {
	prev, err := h.Store.SnapshotHash(ctx, repoKey)
	if err != nil {
		return false, err
	}
	if prev == "" || prev != digest {
		return false, nil
	}
	p, ok, err := h.Store.GetPartition(ctx, repoKey)
	if err != nil {
		return false, err
	}
	wasStale := ok && p.Stale(now)
	if err := h.Store.MarkFresh(ctx, repoKey, now); err != nil {
		return false, err
	}
	if err := h.Store.MarkSeeded(ctx, repoKey); err != nil {
		return false, err
	}
	if wasStale && h.OnChange != nil {
		h.OnChange("projects")
	}
	return true, nil
}

func (h *IngestHandler) handleConfirm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var ev ConfirmEvent
	if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(ev.RepoKey) == "" || strings.TrimSpace(ev.Digest) == "" {
		http.Error(w, "repo_key and digest required", http.StatusBadRequest)
		return
	}
	fresh, err := h.confirmFresh(r.Context(), ev.RepoKey, ev.Digest, time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "fresh": fresh})
}

func (h *IngestHandler) handleRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var ev RemoveEvent
	if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(ev.RepoKey) == "" {
		http.Error(w, "repo_key required", http.StatusBadRequest)
		return
	}
	if err := h.Store.DeletePartition(r.Context(), ev.RepoKey); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if h.OnChange != nil {
		h.OnChange("projects")
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (h *IngestHandler) handleChange(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var ev ChangeEvent
	if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(ev.RepoKey) == "" {
		http.Error(w, "repo_key required", http.StatusBadRequest)
		return
	}
	seq, err := h.Store.ApplyChange(r.Context(), ev.RepoKey, ev.Topic, time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if h.OnChange != nil {
		h.OnChange(ev.Topic)
		h.OnChange("projects")
	}
	if h.OnIngest != nil {
		h.OnIngest(ev.RepoKey, ev.Origin)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "seq": seq})
}

const applySnapshotTimeout = 30 * time.Second

func (h *IngestHandler) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	tm := &ingestTiming{start: time.Now()}
	defer h.logIngest(tm)
	phase := tm.start
	lap := func(d *time.Duration) {
		now := time.Now()
		*d += now.Sub(phase)
		phase = now
	}
	fail := func(msg string, code int) {
		tm.err = msg
		http.Error(w, msg, code)
	}
	body, err := io.ReadAll(r.Body)
	tm.bytes = len(body)
	if err != nil {
		lap(&tm.read)
		fail("read body", http.StatusBadRequest)
		return
	}
	var snap Snapshot
	err = json.Unmarshal(body, &snap)
	lap(&tm.read)
	if err != nil {
		fail("bad json", http.StatusBadRequest)
		return
	}
	tm.snap = snap
	if strings.TrimSpace(snap.RepoKey) == "" {
		fail("repo_key required", http.StatusBadRequest)
		return
	}
	now := time.Now()
	ctx := r.Context()
	partial := len(snap.Kinds) > 0 || len(snap.MergeKinds) > 0
	digest := SnapshotDigest(body)
	if !partial {
		fresh, err := h.confirmFresh(ctx, snap.RepoKey, digest, now)
		if err != nil {
			lap(&tm.apply)
			fail(err.Error(), http.StatusInternalServerError)
			return
		}
		if fresh {
			lap(&tm.apply)
			if h.OnIngest != nil {
				h.OnIngest(snap.RepoKey, snap.Origin)
				lap(&tm.onIngest)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "unchanged": true})
			return
		}
	}
	if partial && len(snap.MergeKinds) > 0 && !slices.Contains(snap.Kinds, "story") {
		seeded, err := h.Store.PartitionSeeded(ctx, snap.RepoKey)
		if err != nil {
			lap(&tm.apply)
			fail(err.Error(), http.StatusInternalServerError)
			return
		}
		if !seeded {
			lap(&tm.apply)
			fail(CodePartitionUnknown+": partition "+snap.RepoKey+" has no full snapshot; post one", http.StatusConflict)
			return
		}
	}
	if slug := strings.TrimSpace(snap.Slug); slug != "" {
		if existing, ok, err := h.Store.FindBySlug(ctx, slug); err != nil {
			lap(&tm.apply)
			fail(err.Error(), http.StatusInternalServerError)
			return
		} else if ok && existing.RepoKey != snap.RepoKey {
			lap(&tm.apply)
			msg := slugConflictMessage(h.Store, ctx, slug, existing.RepoKey, snap.RepoKey)
			fail(msg, http.StatusConflict)
			return
		}
	}
	if _, err := h.Store.TouchPartition(ctx, snap.RepoKey, snap.Slug, now); err != nil {
		lap(&tm.apply)
		fail(err.Error(), http.StatusInternalServerError)
		return
	}

	replace, merge := snapshotKindRows(snap)
	applyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), applySnapshotTimeout)
	defer cancel()
	if err := h.applySnapshot(applyCtx, snap.RepoKey, replace, merge, now); err != nil {
		lap(&tm.apply)
		fail(err.Error(), http.StatusInternalServerError)
		return
	}
	hash := digest
	if partial {
		hash = ""
	}
	if err := h.Store.SetSnapshotHash(ctx, snap.RepoKey, hash); err != nil {
		lap(&tm.apply)
		fail(err.Error(), http.StatusInternalServerError)
		return
	}
	err = h.Store.MarkFresh(ctx, snap.RepoKey, now)
	lap(&tm.apply)
	if err != nil {
		fail(err.Error(), http.StatusInternalServerError)
		return
	}
	if h.OnChange != nil {
		h.OnChange("stories")
		h.OnChange("tasks")
		h.OnChange("queues")
		h.OnChange("docs")
		h.OnChange("projects")
	}
	if h.OnIngest != nil {
		h.OnIngest(snap.RepoKey, snap.Origin)
		lap(&tm.onIngest)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func snapshotKindRows(snap Snapshot) (replace, merge []KindRows) {
	kindOK := func(kind string) bool {
		if len(snap.Kinds) == 0 && len(snap.MergeKinds) == 0 {
			return true
		}
		for _, k := range snap.Kinds {
			if k == kind {
				return true
			}
		}
		return false
	}
	mergeOK := func(kind string) bool {
		for _, k := range snap.MergeKinds {
			if k == kind {
				return true
			}
		}
		return false
	}
	full := len(snap.Kinds) == 0 && len(snap.MergeKinds) == 0
	merges := func(kind string) bool { return !full && mergeOK(kind) }
	candidates := []struct {
		kind  string
		rows  []ItemRow
		merge bool
	}{
		{"story", rawToRows(snap.Stories, "id"), merges("story")},
		{"task", rawToRows(snap.Tasks, "id"), merges("task")},
		{"execution", rawToRows(snap.Executions, "id"), merges("execution")},
		{"queue", rawToRows(snap.Queues, "id"), merges("queue")},
		{"doc", docRows(snap.Docs), false},
		{"ledger_event", rawToRows(snap.LedgerEvents, "id"), merges("ledger_event")},
		{"story_doc", rawToRows(snap.StoryDocs, "id"), merges("story_doc")},
		{"seat", rawToRows(snap.Seats, "id"), false},
	}
	for _, c := range candidates {
		if c.merge {
			merge = append(merge, KindRows{Kind: c.kind, Items: c.rows})
			continue
		}
		if full {
			if c.kind == "ledger_event" || kindOK(c.kind) {
				replace = append(replace, KindRows{Kind: c.kind, Items: c.rows})
			}
			continue
		}
		if kindOK(c.kind) {
			replace = append(replace, KindRows{Kind: c.kind, Items: c.rows})
		}
	}
	if full || kindOK("settings") {
		if len(snap.Settings) > 0 {
			replace = append(replace, KindRows{Kind: "settings", Items: []ItemRow{{ID: "config", Payload: string(snap.Settings)}}})
		} else if full {
		}
	}
	if full || kindOK("identity") {
		if len(snap.Identity) > 0 {
			replace = append(replace, KindRows{Kind: "identity", Items: []ItemRow{{ID: "meta", Payload: string(snap.Identity)}}})
		}
	}
	return replace, merge
}

func SnapshotDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func slugConflictMessage(store *Store, ctx context.Context, slug, existingKey, incomingKey string) string {
	pathNote := ""
	if payload, err := store.GetItem(ctx, existingKey, "identity", "meta"); err == nil {
		var meta IdentityMeta
		if json.Unmarshal([]byte(payload), &meta) == nil && strings.TrimSpace(meta.RepoRoot) != "" {
			pathNote = fmt.Sprintf(" (path %s)", meta.RepoRoot)
		}
	}
	return fmt.Sprintf(
		"slug %q already used by partition %s%s; incoming repo_key %s collides. "+
			"Rename this repo's directory so its basename is unique, then re-run `satelle project add`. "+
			"To drop the existing landing card, remove that partition from the serve mirror.",
		slug, existingKey, pathNote, incomingKey,
	)
}

func rawToRows(raw []json.RawMessage, idKey string) []ItemRow {
	var out []ItemRow
	for _, r := range raw {
		var m map[string]any
		id := ""
		if json.Unmarshal(r, &m) == nil {
			if v, ok := m[idKey].(string); ok {
				id = v
			}
		}
		if id == "" {
			continue
		}
		out = append(out, ItemRow{ID: id, Payload: string(r)})
	}
	return out
}

func docRows(raw []json.RawMessage) []ItemRow {
	var out []ItemRow
	for _, r := range raw {
		var m map[string]any
		if json.Unmarshal(r, &m) != nil {
			continue
		}
		id := ""
		if v, ok := m["path"].(string); ok {
			id = strings.TrimSpace(v)
		}
		if id == "" {
			name, _ := m["name"].(string)
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			kind, _ := m["kind"].(string)
			kind = strings.TrimSpace(kind)
			if kind == "" {
				kind = "documents"
			}
			id = kind + "/" + name
		}
		out = append(out, ItemRow{ID: id, Payload: string(r)})
	}
	return out
}
