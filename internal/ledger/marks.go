package ledger

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// marksSchema holds per-story facts this machine keeps about its own copy of a
// story's chain. They never leave the machine: a mark describes the local store,
// not the story (sty_0dc600e1).
const marksSchema = `
CREATE TABLE IF NOT EXISTS story_marks (
    story_id TEXT NOT NULL,
    mark     TEXT NOT NULL,
    PRIMARY KEY (story_id, mark)
);`

const (
	// MarkItemPending is set on a story by every local append and never by Upsert,
	// so it names the stories whose item row this machine authored and has not yet
	// had confirmed by an Apply. A push sends those rows and clears the mark once
	// the Apply carrying the row is accepted.
	MarkItemPending = "item_pending"
	// MarkHistoryRemote is set on a story whose history was dropped from the local
	// store because the server holds it; the history is fetched again on demand.
	MarkHistoryRemote = "history_remote"
)

// write runs fn in one transaction, or on the transaction the store is already
// bound to.
func (s *Store) write(ctx context.Context, fn func(execer) error) error {
	db, ok := s.db.(*sql.DB)
	if !ok {
		return fn(s.db)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func setMark(ctx context.Context, ex execer, storyID, mark string) error {
	_, err := ex.ExecContext(ctx, `INSERT OR IGNORE INTO story_marks (story_id, mark) VALUES (?, ?)`, storyID, mark)
	return err
}

// SetMarks sets mark on every story in ids.
func (s *Store) SetMarks(ctx context.Context, mark string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return s.write(ctx, func(ex execer) error {
		for _, id := range ids {
			if strings.TrimSpace(id) == "" {
				continue
			}
			if err := setMark(ctx, ex, id, mark); err != nil {
				return fmt.Errorf("ledger: mark %s %s: %w", id, mark, err)
			}
		}
		return nil
	})
}

// ClearMarks removes mark from every story in ids.
func (s *Store) ClearMarks(ctx context.Context, mark string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return s.write(ctx, func(ex execer) error {
		for _, id := range ids {
			if _, err := ex.ExecContext(ctx, `DELETE FROM story_marks WHERE story_id = ? AND mark = ?`, id, mark); err != nil {
				return fmt.Errorf("ledger: clear %s %s: %w", id, mark, err)
			}
		}
		return nil
	})
}

// Marked returns the stories carrying mark, as a set.
func (s *Store) Marked(ctx context.Context, mark string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT story_id FROM story_marks WHERE mark = ?`, mark)
	if err != nil {
		return nil, fmt.Errorf("ledger: marked %s: %w", mark, err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("ledger: marked %s: %w", mark, err)
		}
		out[id] = true
	}
	return out, rows.Err()
}

// HasMark reports whether storyID carries mark.
func (s *Store) HasMark(ctx context.Context, storyID, mark string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM story_marks WHERE story_id = ? AND mark = ?`, storyID, mark).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("ledger: has mark %s: %w", mark, err)
	}
	return true, nil
}

// LatestEventTimes returns, per story with events, when its newest event was made.
func (s *Store) LatestEventTimes(ctx context.Context) (map[string]time.Time, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT story_id, MAX(created_at) FROM evidence WHERE story_id <> '' GROUP BY story_id`)
	if err != nil {
		return nil, fmt.Errorf("ledger: latest event times: %w", err)
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var id, at string
		if err := rows.Scan(&id, &at); err != nil {
			return nil, fmt.Errorf("ledger: latest event times: %w", err)
		}
		out[id] = parseTime(at)
	}
	return out, rows.Err()
}

// DropStoryHistory deletes every event of a story and marks it MarkHistoryRemote,
// in one transaction, but only when the chain ends at head: the last event the
// server accepted from this checkout. A story with events the server has not
// accepted, or a chain that does not end in one event, keeps everything and the
// call reports false. Nothing is lost: the server holds every event dropped.
func (s *Store) DropStoryHistory(ctx context.Context, storyID, head string) (bool, error) {
	if strings.TrimSpace(storyID) == "" || strings.TrimSpace(head) == "" {
		return false, nil
	}
	dropped := false
	err := s.write(ctx, func(ex execer) error {
		events, err := (&Store{db: ex}).storyEvents(ctx, storyID)
		if err != nil || len(events) == 0 {
			return err
		}
		if tip, ok := chainTip(events); !ok || tip != head {
			return nil
		}
		if _, err := ex.ExecContext(ctx, `DELETE FROM evidence WHERE story_id = ?`, storyID); err != nil {
			return fmt.Errorf("ledger: drop history of %s: %w", storyID, err)
		}
		dropped = true
		return setMark(ctx, ex, storyID, MarkHistoryRemote)
	})
	return dropped, err
}

// chainTip is the one event no other event of the chain builds on.
func chainTip(events []Entry) (string, bool) {
	builtOn := make(map[string]bool, len(events))
	for _, e := range events {
		builtOn[e.BaseID] = true
	}
	tip := ""
	for _, e := range events {
		if builtOn[e.ID] {
			continue
		}
		if tip != "" {
			return "", false
		}
		tip = e.ID
	}
	return tip, tip != ""
}

// RebaseEvents puts a story's unpublished events after events the server holds
// and this checkout had not seen, in one transaction: the server's events are
// inserted with the bases they carry, then the unpublished events (named in chain
// order) are written again after them, the first now building on newBase and each
// other on the one named before it. Ids, kinds, payloads and times are kept; the
// events are re-inserted rather than edited so insertion order stays chain order.
func (s *Store) RebaseEvents(ctx context.Context, storyID string, hosted []Entry, unpublished []string, newBase string) error {
	if strings.TrimSpace(storyID) == "" || len(unpublished) == 0 {
		return fmt.Errorf("%w: a story and its unpublished events are required", ErrRemoveRefused)
	}
	return s.write(ctx, func(ex execer) error {
		events, err := (&Store{db: ex}).storyEvents(ctx, storyID)
		if err != nil {
			return err
		}
		byID := make(map[string]Entry, len(events))
		for _, e := range events {
			byID[e.ID] = e
		}
		moved := make([]Entry, 0, len(unpublished))
		for _, id := range unpublished {
			e, ok := byID[id]
			if !ok {
				return fmt.Errorf("%w: %s is not an event of %s", ErrRemoveRefused, id, storyID)
			}
			moved = append(moved, e)
		}
		for _, e := range moved {
			if _, err := ex.ExecContext(ctx, `DELETE FROM evidence WHERE id = ? AND story_id = ?`, e.ID, storyID); err != nil {
				return fmt.Errorf("ledger: rebase %s: %w", e.ID, err)
			}
		}
		for _, e := range hosted {
			e.StoryID = storyID
			if err := insertKnown(ctx, ex, e); err != nil {
				return err
			}
		}
		for i := range moved {
			if i == 0 {
				moved[i].BaseID = newBase
			} else {
				moved[i].BaseID = moved[i-1].ID
			}
		}
		for _, e := range moved {
			if err := insertKnown(ctx, ex, e); err != nil {
				return err
			}
		}
		return nil
	})
}

// insertKnown writes an event with its own id and base, skipping one already held.
func insertKnown(ctx context.Context, ex execer, e Entry) error {
	payload, refs := string(e.Payload), string(e.Refs)
	if payload == "" {
		payload = "{}"
	}
	if refs == "" {
		refs = "[]"
	}
	created := e.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	if _, err := ex.ExecContext(ctx, `
        INSERT OR IGNORE INTO evidence (id, story_id, project_id, kind, actor, body, payload, refs, created_at, base_id, workflow_hash)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.StoryID, e.ProjectID, e.Kind, e.Actor, e.Body, payload, refs,
		created.UTC().Format(time.RFC3339Nano), e.BaseID, e.WorkflowHash); err != nil {
		return fmt.Errorf("ledger: insert %s: %w", e.ID, err)
	}
	return nil
}
