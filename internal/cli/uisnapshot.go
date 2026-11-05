// Snapshot build + POST helpers for the push-fed local UI mirror
// (sty_1dde0d47 / sty_805bee9c). The operator verb is `satelle workspace add`
// (register + seed); mutating verbs drain via uiDrain. The retired `ui push`
// spelling points at workspace add (retiredNames).
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/lease"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// uiSnapshotBudget bounds one workspace add snapshot POST (a package var so
// tests can shorten it).
var uiSnapshotBudget = 30 * time.Second

// postUISnapshot POSTs snap to endpoint/ingest/snapshot under uiSnapshotBudget.
// Used by workspace add (fail-visible; large snapshots OK).
func postUISnapshot(endpoint string, snap *mirror.Snapshot) error {
	ctx, cancel := context.WithTimeout(context.Background(), uiSnapshotBudget)
	defer cancel()
	return postUISnapshotContext(ctx, endpoint, snap)
}

// postUISnapshotContext POSTs snap under the caller's deadline. The end-of-
// invocation drain (sty_9ba3d709) passes a short shared budget so a black-holed
// endpoint cannot hang the verb.
func postUISnapshotContext(ctx context.Context, endpoint string, snap *mirror.Snapshot) error {
	body, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	url := strings.TrimRight(endpoint, "/") + "/ingest/snapshot"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("ui push: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		// Surface a bounded body so slug-conflict 409 (sty_57d5ce25) and other
		// ingest rejections print a clear remedy through workspace add.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		msg := strings.TrimSpace(string(body))
		if resp.StatusCode == http.StatusConflict && strings.HasPrefix(msg, mirror.CodePartitionUnknown) {
			return fmt.Errorf("ui push: %w: %s", errPartitionUnknown, msg)
		}
		if msg != "" {
			return fmt.Errorf("ui push: server returned %s: %s", resp.Status, msg)
		}
		return fmt.Errorf("ui push: server returned %s", resp.Status)
	}
	return nil
}

// errPartitionUnknown is satelled refusing a delta drain because the repo's
// partition has never had a full snapshot applied; the drain posts one.
var errPartitionUnknown = errors.New(mirror.CodePartitionUnknown)

// drainLedgerLimit is how many newest ledger events a light drain ships via
// MergeKinds. Full snapshots still use 10k (sty_c5065d05).
const drainLedgerLimit = 200

// deltaSlack widens a delta drain's since bound so a row stamped in the same
// clock tick as the invocation start is still sent.
const deltaSlack = time.Second

func buildUISnapshot(ctx context.Context, a *app.App) (*mirror.Snapshot, error) {
	return buildUISnapshotOpts(ctx, a, false, time.Time{})
}

// buildUIDrainSnapshot is the end-of-verb light push (sty_3562c820): stories /
// tasks / executions / seats / identity plus a merge of recent ledger events.
// Doc and story-doc bodies are omitted so the payload fits uiDrainBudget; the
// 5-minute reconcile (full snapshot via workspace add) remains the repair path.
func buildUIDrainSnapshot(ctx context.Context, a *app.App) (*mirror.Snapshot, error) {
	return buildUISnapshotOpts(ctx, a, true, time.Time{})
}

// partitionSlug is the landing slug a snapshot carries for a's partition. It names
// the PROJECT, so it comes from the canonical repo root — a worktree is the same
// project as its parent, and deriving the slug from the invoking directory renamed
// the parent's partition to `sty_<id>` and took /r/<parent> to 404 on every
// worktree dispatch (sty_cd219594, sty_dfc9b100). EVERY snapshot builder must use
// this: the mirror overwrites a partition's slug from whichever snapshot arrives
// last, so one builder on the raw root undoes the others.
func partitionSlug(a *app.App) string {
	return config.RepoName(a.RepoRoot)
}

// buildUISnapshotOpts builds the full body (drain false), the light drain
// (drain true, since zero) or, with since set, the delta drain: only the story,
// task and execution rows updated at or after since, and the waves of their
// epics, as merge kinds (sty_a003a459). Rows are never deleted by a delta; the
// reconcile reseed's full body is the repair.
func buildUISnapshotOpts(ctx context.Context, a *app.App, drain bool, since time.Time) (*mirror.Snapshot, error) {
	repoKey := config.RepoKey(a.RepoRoot)
	snap := &mirror.Snapshot{RepoKey: repoKey, Slug: partitionSlug(a)}

	items, err := a.Store.Stories.List(ctx, workitem.ListFilter{})
	if err != nil {
		return nil, err
	}
	delta := drain && !since.IsZero()
	sent := items
	if delta {
		sent = changedSince(items, since.Add(-deltaSlack))
	}
	for _, it := range sent {
		b, err := json.Marshal(it)
		if err != nil {
			return nil, err
		}
		switch it.Kind {
		case workitem.KindTask:
			snap.Tasks = append(snap.Tasks, b)
		case workitem.KindExecution:
			snap.Executions = append(snap.Executions, b)
		default:
			snap.Stories = append(snap.Stories, b)
		}
	}

	if !drain {
		// Provenance/source for process docs (workflows/skills/principles).
		prov, src := map[string]string{}, map[string]string{}
		if a.Store.DocIndex != nil {
			docs, err := a.Store.DocIndex.List(ctx, "")
			if err != nil {
				return nil, err
			}
			// Path-keyed source for every indexed doc (not only substrate.List
			// process rows). Same-name docs in different dirs keep distinct
			// sources; kind+name last-wins would share one.
			for _, d := range docs {
				key := d.Path
				if key == "" {
					key = d.Kind + "\x00" + d.Name
				}
				if d.Path != "" {
					src[key] = d.Path
				}
			}
			for _, d := range docs {
				key := d.Path
				if key == "" {
					key = d.Kind + "\x00" + d.Name
				}
				row := map[string]any{
					"name":       d.Name,
					"kind":       d.Kind,
					"body":       d.Body,
					"headline":   d.Headline,
					"path":       d.Path,
					"hash":       d.Hash,
					"size":       d.Size,
					"mod_time":   d.ModTime,
					"indexed_at": d.IndexedAt,
					"embedded":   d.Embedded,
					"provenance": prov[key],
					"source":     src[key],
				}
				b, _ := json.Marshal(row)
				snap.Docs = append(snap.Docs, b)
			}
		}
	}

	if a.Store.Ledger != nil {
		limit := 10000
		if drain {
			limit = drainLedgerLimit
		}
		led, have, err := listLedgerJSONLimit(ctx, a, limit)
		if err == nil {
			snap.LedgerEvents = led
		}
		if !drain {
			// A full push replaces the mirror's ledger set, so the window alone would
			// drop the progress of every story whose rows are older than it.
			if prog, err := listProgressJSON(ctx, a, items, have); err == nil {
				snap.LedgerEvents = append(snap.LedgerEvents, prog...)
			}
		}
	}

	if a.Store.Leases != nil {
		if seats, err := listSeatsJSON(ctx, a); err == nil {
			snap.Seats = seats
		}
	}

	if !drain {
		// Settings blob — display rows the RO settings page renders.
		if cfgBytes, err := marshalSettingsBlob(a); err == nil {
			snap.Settings = cfgBytes
		}
	}

	// Per-partition identity/meta for footer + account strip.
	//
	// IDENTITY sinks go through CanonicalRepoRoot; LOCATION uses keep the
	// invoking root. The rule (sty_cd219594): anything that NAMES the repository
	// — registry path, partition slug, partition path, ProjectName, the runtime
	// path marker — is identity and must not move per worktree. Anything that
	// READS the repository's own data — data dir, substrate, docs, runtime-dir
	// resolution via RepoKey — is location and stays per worktree. Canonicalising
	// only some sinks is how the partition's slug said `satelle` while its path
	// still said the worktree.
	canon := config.CanonicalRepoRoot(a.RepoRoot)
	id := mirror.IdentityMeta{
		ProjectName: config.RepoName(a.RepoRoot),
		RepoRoot:    canon,
		FooterEmail: resolveUser(a.Config, a.RepoRoot).Display(),
		Hosted:      hostedIdentity(ctx, a),
	}
	if b, err := json.Marshal(id); err == nil {
		snap.Identity = b
	}

	if !drain {
		// Story/task attachment docs (plans, step-summaries, …) under the runtime stories dir.
		if docs, err := listStoryDocsJSON(a); err == nil {
			snap.StoryDocs = docs
		}
	}

	switch {
	case delta:
		// Seats replace: they are few and a released seat must vanish.
		snap.Kinds = []string{"seat", "identity"}
		snap.MergeKinds = []string{"story", "task", "execution", "wave", "ledger_event"}
	case drain:
		// Explicit partition set: omitted kinds stay untouched on the mirror.
		snap.Kinds = []string{"story", "task", "execution", "seat", "wave", "identity"}
		snap.MergeKinds = []string{"ledger_event"}
	}

	return snap, nil
}

// changedSince keeps the items updated at or after cut.
func changedSince(items []workitem.Item, cut time.Time) []workitem.Item {
	var out []workitem.Item
	for _, it := range items {
		if !it.UpdatedAt.Before(cut) {
			out = append(out, it)
		}
	}
	return out
}

// listStoryDocsJSON walks the runtime stories/ attachment tree and emits
// story_doc rows with id "storyID/name" for mirror detail pages.
func listStoryDocsJSON(a *app.App) ([]json.RawMessage, error) {
	storiesDir := filepath.Join(a.RuntimeDir, "stories")
	if a.RuntimeDir == "" {
		// Fall back to home-keyed runtime layout.
		rt := a.Config.ResolveRuntimeDir(a.RepoRoot)
		storiesDir = filepath.Join(rt.Dir, "stories")
	}
	ents, err := os.ReadDir(storiesDir)
	if err != nil {
		return nil, nil // no attachments yet
	}
	var out []json.RawMessage
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		storyID := e.Name()
		sub := filepath.Join(storiesDir, storyID)
		files, err := os.ReadDir(sub)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".md") {
				continue
			}
			data, rerr := os.ReadFile(filepath.Join(sub, f.Name()))
			if rerr != nil {
				continue
			}
			name := strings.TrimSuffix(f.Name(), ".md")
			typ := ""
			// light frontmatter type: extract if present
			body := string(data)
			if strings.HasPrefix(body, "---\n") {
				if end := strings.Index(body[4:], "\n---"); end >= 0 {
					fm := body[4 : 4+end]
					for _, line := range strings.Split(fm, "\n") {
						if strings.HasPrefix(line, "type:") {
							typ = strings.TrimSpace(strings.TrimPrefix(line, "type:"))
							typ = strings.Trim(typ, `"'`)
						}
					}
				}
			}
			row := map[string]any{
				"id":       storyID + "/" + name,
				"story_id": storyID,
				"name":     name,
				"type":     typ,
				"body":     body,
			}
			b, _ := json.Marshal(row)
			out = append(out, b)
		}
	}
	return out, nil
}

func listLedgerJSON(ctx context.Context, a *app.App) ([]json.RawMessage, error) {
	// Full snapshot path: newest-first under the 10k budget (sty_c5065d05).
	out, _, err := listLedgerJSONLimit(ctx, a, 10000)
	return out, err
}

// listLedgerJSONLimit emits the newest limit ledger rows and the set of their ids.
func listLedgerJSONLimit(ctx context.Context, a *app.App, limit int) ([]json.RawMessage, map[string]bool, error) {
	if a.Store.Ledger == nil {
		return nil, nil, nil
	}
	entries, err := a.Store.Ledger.ListAll(ctx, limit)
	if err != nil {
		return nil, nil, err
	}
	out := make([]json.RawMessage, 0, len(entries))
	ids := make(map[string]bool, len(entries))
	for _, e := range entries {
		b, err := json.Marshal(e)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, b)
		ids[e.ID] = true
	}
	return out, ids, nil
}

// listProgressJSON emits every ledger.ProgressKinds row of the exported items
// that is not already in have, whatever its age: the rows the mirror folds a
// story's PROGRESS from, so it never depends on the recent window (sty_434d2fd9).
// Its size grows with the items' progress events, not with the ledger.
func listProgressJSON(ctx context.Context, a *app.App, items []workitem.Item, have map[string]bool) ([]json.RawMessage, error) {
	if a.Store.Ledger == nil {
		return nil, nil
	}
	exported := make(map[string]bool, len(items))
	for _, it := range items {
		exported[it.ID] = true
	}
	var out []json.RawMessage
	for _, kind := range ledger.ProgressKinds {
		err := a.Store.Ledger.ForEachKind(ctx, "", kind, func(e ledger.Entry) error {
			if !exported[e.StoryID] || have[e.ID] {
				return nil
			}
			b, err := json.Marshal(e)
			if err != nil {
				return err
			}
			out = append(out, b)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func listSeatsJSON(ctx context.Context, a *app.App) ([]json.RawMessage, error) {
	if a.Store.Leases == nil {
		return nil, nil
	}
	now := time.Now().UTC()
	_, _ = a.Store.Leases.Reap(ctx)
	all, err := a.Store.Leases.List(ctx)
	if err != nil {
		return nil, err
	}
	// Best-effort idle_timeout resolution per dispatched binding (sty_752c4ef2
	// AC6): the mirror-side render needs it to decide the warn threshold, and
	// the push-fed web has no other access to this repo's agents.toml. A
	// missing/broken agents layer must never fail a seat push — it just omits
	// idle_timeout on every row.
	dataDir := a.PlaneDir()
	agentsLayer, agentsErr := config.LoadAgents(dataDir)
	out := make([]json.RawMessage, 0, len(all))
	for _, l := range all {
		row := map[string]any{
			"id":         l.ItemID,
			"kind":       l.Kind,
			"story_seat": l.StorySeat,
			// Arbitration key + tree anchor, so the UI seat surface shows the
			// same grouping as `satelle story seat` (sty_c098dc2d).
			"seat_key":  l.SeatKey,
			"worktree":  l.Worktree,
			"owner":     l.Owner,
			"state":     l.State,
			"in_flight": lease.EffectiveInFlight(l, now),
			"stale":     !lease.Alive(l, now),
		}
		// In-flight DISPATCH metadata (sty_752c4ef2 AC6): which agent binding
		// and model are running, the OS pid, and the last real event — the web
		// story row's running indicator renders straight from these, the same
		// fields `satelle story seat` (AC7) prints.
		if d, ok := lease.EffectiveActivityDetail(l, now); ok {
			row["agent"] = d.Agent
			row["model"] = d.Model
			row["pid"] = d.Pid
			row["last_event"] = d.EventLabel
			row["last_event_at"] = d.EventAt
			row["event_count"] = d.EventCount
			if agentsErr == nil {
				if b, ok := agentsLayer.NamedBinding(d.Agent); ok {
					if idle, ierr := agentsLayer.ResolveIdleTimeout(b, config.DefaultIdleTimeout); ierr == nil {
						row["idle_timeout_ns"] = int64(idle)
					}
				}
			}
		}
		if act, elapsed, ok := lease.EffectiveActivity(l, now); ok {
			row["activity"] = act.Label
			row["activity_index"] = act.Index
			row["activity_total"] = act.Total
			row["activity_started_at"] = now.Add(-elapsed)
			// A gate round's live tally (sty_216d5205): the web's progress chip
			// reads these keys through web.seatPayload.
			row["activity_gates"] = act.Gates
			row["activity_passed"] = act.Passed
			row["activity_failed"] = act.Failed
		}
		b, err := json.Marshal(row)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

func marshalSettingsBlob(a *app.App) (json.RawMessage, error) {
	// Push the resolved config as a flat display map the mirror settings page can
	// render without reopening the repo toml.
	type row struct {
		FieldID string `json:"field_id"`
		Label   string `json:"label"`
		Help    string `json:"help"`
		Value   string `json:"value"`
		Section string `json:"section"`
	}
	var rows []row
	for _, s := range config.Settings {
		rows = append(rows, row{
			FieldID: s.FieldID(),
			Label:   s.Label,
			Help:    s.Help,
			Value:   config.SettingDisplay(a.Config, s),
			Section: s.Section,
		})
	}
	return json.Marshal(map[string]any{
		"repo_root": a.RepoRoot,
		"rows":      rows,
	})
}
