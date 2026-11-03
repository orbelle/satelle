package mirror

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// IngestOrigin says where the store change an ingest body reports came from.
// The mirror applies every origin alike; only the hosted push trigger reads it,
// so a change materialised from the hosted project is never pushed back to it.
type IngestOrigin string

const (
	// OriginLocal is a change made on this machine (and every body from an
	// older CLI, which carries no origin).
	OriginLocal IngestOrigin = ""
	// OriginHosted is a store change materialised from the hosted project: a
	// pull, snapshot, rehydrate or align.
	OriginHosted IngestOrigin = "hosted"
)

// EnvIngestOrigin, set to OriginHosted, stamps a CLI verb's ingest hosted
// whatever the verb: satelled sets it on its reseed child, whose
// `workspace add` is local when an operator runs it (sty_0367d20c).
const EnvIngestOrigin = "SATELLE_INGEST_ORIGIN"

// ChangeEvent is the body POSTed by the CLI push publisher (sty_126228b2).
type ChangeEvent struct {
	RepoKey string       `json:"repo_key"`
	Topic   string       `json:"topic"`
	Entity  string       `json:"entity"`
	At      string       `json:"at"`
	Origin  IngestOrigin `json:"origin,omitempty"`
}

// RemoveEvent is POSTed to /ingest/remove to purge a partition (sty_eb61be02).
// Same trust model as snapshot ingest (localhost push-fed; CLI sole writer).
type RemoveEvent struct {
	RepoKey string `json:"repo_key"`
}

// Snapshot is a full-state push for one partition (order:4 reconcile).
// Every kind the UI reads must arrive here so serve never opens a repo DB.
//
// Partial drains (sty_3562c820) set Kinds / MergeKinds so only those partitions
// are written; kinds named in neither are left untouched. Absent both fields,
// behaviour is today's full replace of every kind (workspace add + reconcile).
type Snapshot struct {
	RepoKey      string            `json:"repo_key"`
	Slug         string            `json:"slug,omitempty"`
	Stories      []json.RawMessage `json:"stories,omitempty"`
	Tasks        []json.RawMessage `json:"tasks,omitempty"`
	Executions   []json.RawMessage `json:"executions,omitempty"`
	Docs         []json.RawMessage `json:"docs,omitempty"`
	LedgerEvents []json.RawMessage `json:"ledger_events,omitempty"`
	StoryDocs    []json.RawMessage `json:"story_docs,omitempty"` // id = story_id/name
	Seats        []json.RawMessage `json:"seats,omitempty"`
	// Waves carries, per epic-parent, the story-wave verb's own JSON response (or
	// {"container": id, "error": text} when the verb refuses) — id = container.
	// The web renders it and never recomputes the wave (sty_a42f4add).
	Waves    []json.RawMessage `json:"waves,omitempty"`
	Settings json.RawMessage   `json:"settings,omitempty"` // single settings blob
	// Identity is the per-partition meta blob (project name, repo path, footer email).
	// Ingested as kind "identity" id "meta" (sty_400c022b / epic:mirror-ui-parity).
	Identity json.RawMessage `json:"identity,omitempty"`
	// Kinds is the explicit set of partitions this body authoritatively replaces
	// (delete+insert). Empty means full snapshot: all standard kinds.
	Kinds []string `json:"kinds,omitempty"`
	// MergeKinds are applied as upsert-without-delete (ledger is append-only).
	MergeKinds []string `json:"merge_kinds,omitempty"`
	// Origin is where the reported store change came from; see IngestOrigin.
	Origin IngestOrigin `json:"origin,omitempty"`
}

// IdentityMeta is the JSON shape of Snapshot.Identity — what the mirror footer
// and account strip render without opening a repo DB or calling git.
type IdentityMeta struct {
	ProjectName string `json:"project_name"`
	RepoRoot    string `json:"repo_root"`
	FooterEmail string `json:"footer_email"`
	// Hosted is set when the server is the repo's store of record (sty_0dc600e1):
	// the page then lists this machine's stories and links the rest to the server.
	Hosted *HostedMeta `json:"hosted,omitempty"`
}

// HostedMeta names the hosted project a partition syncs with and the stories this
// machine engaged.
type HostedMeta struct {
	Server         string   `json:"server"`
	Project        string   `json:"project"`
	MachineStories []string `json:"machine_stories"`
}

// IngestHandler serves POST /ingest/change and POST /ingest/snapshot.
// OnChange is optional (SSE doorbell). OnIngest is optional (hosted push
// trigger); it receives the body's origin so it can ignore hosted-origin changes.
type IngestHandler struct {
	Store    *Store
	OnChange func(topic string)
	OnIngest func(repoKey string, origin IngestOrigin)
}

// Mount registers ingest routes on mux.
func (h *IngestHandler) Mount(mux *http.ServeMux) {
	mux.HandleFunc("/ingest/change", h.handleChange)
	mux.HandleFunc("/ingest/snapshot", h.handleSnapshot)
	mux.HandleFunc("/ingest/remove", h.handleRemove)
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

// applySnapshotTimeout bounds server-side apply after the body is fully read.
// Detached from the client so a drain that walks away mid-apply cannot cancel
// a half-written multi-kind replace (sty_3562c820 AC2).
const applySnapshotTimeout = 30 * time.Second

func (h *IngestHandler) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	var snap Snapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(snap.RepoKey) == "" {
		http.Error(w, "repo_key required", http.StatusBadRequest)
		return
	}
	now := time.Now()
	ctx := r.Context()
	partial := len(snap.Kinds) > 0 || len(snap.MergeKinds) > 0
	// A periodic reconcile re-posts the same FULL state most of the time. When
	// the body is byte-identical to the one already applied, record freshness
	// and stop (sty_e6e467fe). Partial drains never own snap_hash — skip the
	// short-circuit so a light body cannot suppress a later full re-seed.
	digest := snapshotDigest(body)
	if !partial {
		if prev, err := h.Store.SnapshotHash(ctx, snap.RepoKey); err == nil && prev != "" && prev == digest {
			if err := h.Store.MarkFresh(ctx, snap.RepoKey, now); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			// Unchanged bytes can still coincide with a hosted-side gap; the
			// push is a cursor no-op when there is nothing to send (sty_c526753a).
			if h.OnIngest != nil {
				h.OnIngest(snap.RepoKey, snap.Origin)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "unchanged": true})
			return
		}
	}
	// Fail-closed: landing /r/<slug>/ must map to one partition (sty_57d5ce25).
	// Empty slug skips — the UI falls back to unique repo_key.
	if slug := strings.TrimSpace(snap.Slug); slug != "" {
		if existing, ok, err := h.Store.FindBySlug(ctx, slug); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		} else if ok && existing.RepoKey != snap.RepoKey {
			msg := slugConflictMessage(h.Store, ctx, slug, existing.RepoKey, snap.RepoKey)
			http.Error(w, msg, http.StatusConflict)
			return
		}
	}
	if _, err := h.Store.TouchPartition(ctx, snap.RepoKey, snap.Slug, now); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	replace, merge := snapshotKindRows(snap)
	// Detach from the client deadline: the body is already fully read; finishing
	// (or failing) the single tx must not be cut short by the drain budget.
	applyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), applySnapshotTimeout)
	defer cancel()
	if err := h.Store.ApplySnapshot(applyCtx, snap.RepoKey, replace, merge, now); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Only full bodies may set snap_hash. Partial applies clear it so the next
	// reconcile cannot short-circuit against a light digest (sty_3562c820).
	hash := digest
	if partial {
		hash = ""
	}
	if err := h.Store.SetSnapshotHash(ctx, snap.RepoKey, hash); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Doorbell only after a committed apply so viewers never refetch a failed push.
	if h.OnChange != nil {
		h.OnChange("stories")
		h.OnChange("tasks")
		h.OnChange("docs")
		h.OnChange("projects")
	}
	if h.OnIngest != nil {
		h.OnIngest(snap.RepoKey, snap.Origin)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// snapshotKindRows builds the replace/merge sets for ApplySnapshot.
// Empty Kinds+MergeKinds → full replace of every standard kind (and settings/
// identity when present). Non-empty Kinds selects which replace partitions
// apply; omitted kinds are left untouched.
func snapshotKindRows(snap Snapshot) (replace, merge []KindRows) {
	kindOK := func(kind string) bool {
		if len(snap.Kinds) == 0 && len(snap.MergeKinds) == 0 {
			return true // full snapshot
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
	// When full (no Kinds), ledger is a replace. When light, ledger is merge-only.
	full := len(snap.Kinds) == 0 && len(snap.MergeKinds) == 0
	candidates := []struct {
		kind  string
		rows  []ItemRow
		merge bool
	}{
		{"story", rawToRows(snap.Stories, "id"), false},
		{"task", rawToRows(snap.Tasks, "id"), false},
		{"execution", rawToRows(snap.Executions, "id"), false},
		{"doc", docRows(snap.Docs), false},
		{"ledger_event", rawToRows(snap.LedgerEvents, "id"), !full && mergeOK("ledger_event")},
		{"story_doc", rawToRows(snap.StoryDocs, "id"), false},
		{"seat", rawToRows(snap.Seats, "id"), false},
		{"wave", rawToRows(snap.Waves, "container"), false},
	}
	for _, c := range candidates {
		if c.merge {
			merge = append(merge, KindRows{Kind: c.kind, Items: c.rows})
			continue
		}
		if full {
			// Full path always replaces every standard kind (even empty).
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
			// Full snapshot with empty settings still clears settings when we
			// historically only wrote when present — keep that: only write if non-empty.
		}
	}
	if full || kindOK("identity") {
		if len(snap.Identity) > 0 {
			replace = append(replace, KindRows{Kind: "identity", Items: []ItemRow{{ID: "meta", Payload: string(snap.Identity)}}})
		}
	}
	return replace, merge
}

// snapshotDigest fingerprints a snapshot body. Snapshot JSON is deterministic
// for unchanged repo state (ordered queries, sorted map keys), so equal digests
// mean equal state.
func snapshotDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// slugConflictMessage builds the 409 body for a landing-slug collision: names
// the slug, existing and incoming repo_keys, best-effort path of the existing
// partition, and the rename / re-seed remedy (sty_57d5ce25).
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
			"Rename this repo's directory so its basename is unique, then re-run `satelle workspace add`. "+
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

// docRows builds ItemRows for snapshot docs. ID is path when set; otherwise
// "<kind>/<name>" with kind defaulting to "documents". Docs with neither a
// path nor a name are skipped (same as empty-id rows elsewhere).
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
