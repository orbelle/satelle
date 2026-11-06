//go:build integration

package tests

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// oldDDL is the pre-1.0 store's schema (work_items and evidence, as the
// 0.0.x binary created and migrated them).
const oldDDL = `
CREATE TABLE work_items (
    id TEXT PRIMARY KEY, kind TEXT NOT NULL, title TEXT NOT NULL DEFAULT '', body TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'backlog', priority TEXT NOT NULL DEFAULT '', category TEXT NOT NULL DEFAULT '',
    parent_id TEXT NOT NULL DEFAULT '', acceptance_criteria TEXT NOT NULL DEFAULT '', tags TEXT NOT NULL DEFAULT '[]',
    created_at TEXT NOT NULL, updated_at TEXT NOT NULL, archived INTEGER NOT NULL DEFAULT 0, assignee TEXT NOT NULL DEFAULT '',
    park_origin TEXT NOT NULL DEFAULT '', input_decls TEXT NOT NULL DEFAULT '', input_values TEXT NOT NULL DEFAULT '', triggers TEXT NOT NULL DEFAULT '');
CREATE TABLE evidence (
    id TEXT PRIMARY KEY, story_id TEXT NOT NULL DEFAULT '', project_id TEXT NOT NULL DEFAULT '', kind TEXT NOT NULL,
    actor TEXT NOT NULL DEFAULT '', body TEXT NOT NULL DEFAULT '', payload TEXT NOT NULL DEFAULT '{}', refs TEXT NOT NULL DEFAULT '[]',
    created_at TEXT NOT NULL, base_id TEXT NOT NULL DEFAULT '', workflow_hash TEXT NOT NULL DEFAULT '');
`

// oldRow is one fixture work_items row.
type oldRow struct {
	id, kind, title, body, status, priority, category, parent, criteria, tags, created, assignee string
	archived                                                                                     int
}

var oldRows = []oldRow{
	{"sty_a1000001", "story", "Story one", "the body", "backlog", "high", "feature", "", "1. first criterion\n2. second\n   continued", `["area:x","k:v"]`, "2026-07-12T00:00:00Z", "ann", 0},
	{"sty_a1000002", "story", "Story two", "", "done", "", "", "sty_a1000001", "", `[]`, "2026-07-13T10:20:30.123456789Z", "", 1},
	{"tsk_t1000001", "task", "A task", "", "active", "", "", "", "", `[]`, "2026-07-14T00:00:00Z", "", 0},
	{"exe_e1000001", "execution", "A run", "", "done", "", "", "tsk_t1000001", "", `[]`, "2026-07-15T00:00:00Z", "", 0},
}

// oldEvidence: three JSON rows and one non-JSON row for story one, and one
// row naming no item.
var oldEvidence = [][]string{
	{"ev1", "sty_a1000001", "note", "ann", "first", `{"a":1}`, "2026-07-12T01:00:00Z"},
	{"ev2", "sty_a1000001", "review", "bob", "second", `{"verdict":"accept"}`, "2026-07-12T02:00:00Z"},
	{"ev3", "sty_a1000001", "status", "ann", "third", `{"to":"backlog"}`, "2026-07-12T03:00:00Z"},
	{"ev4", "sty_a1000001", "log", "ann", "fourth", "not json", "2026-07-12T04:00:00Z"},
	{"ev5", "", "project", "ann", "project-level", `{}`, "2026-07-12T05:00:00Z"},
}

// oldStore writes a pre-1.0 store under dir: satelle.db, story one's
// documents (a markdown file and a png with its sidecar) and a regenerated
// stories/<id>.md view.
func oldStore(t *testing.T, dir string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "satelle.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(oldDDL); err != nil {
		t.Fatal(err)
	}
	for _, r := range oldRows {
		if _, err := db.Exec(`INSERT INTO work_items (id,kind,title,body,status,priority,category,parent_id,acceptance_criteria,tags,created_at,updated_at,archived,assignee,park_origin)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, r.id, r.kind, r.title, r.body, r.status, r.priority, r.category, r.parent, r.criteria, r.tags, r.created, r.created, r.archived, r.assignee, "x"); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range oldEvidence {
		if _, err := db.Exec(`INSERT INTO evidence (id,story_id,kind,actor,body,payload,created_at) VALUES (?,?,?,?,?,?,?)`, e[0], e[1], e[2], e[3], e[4], e[5], e[6]); err != nil {
			t.Fatal(err)
		}
	}
	docs := filepath.Join(dir, "stories", "sty_a1000001")
	writeFile(t, filepath.Join(docs, "plan.md"), "# Plan\n\nthe old plan\n", 0o644)
	writeFile(t, filepath.Join(docs, "shot.png"), "\x89PNG\r\n\x1a\nnot really", 0o644)
	writeFile(t, filepath.Join(docs, "shot.png.satelle.json"), `{"content_type":"image/png","attached_at":"2026-07-12T06:00:00Z"}`, 0o644)
	writeFile(t, filepath.Join(dir, "stories", "sty_a1000001.md"), "regenerated view", 0o644)
}

// oldFormat writes a pre-1.0 .satelle into the sandbox and commits it.
func oldFormat(t *testing.T, s *sandbox) {
	t.Helper()
	s.write(".satelle/satelle.toml", "[review]\ngate_create = true\n\n[gate]\nedit_exempt_globs = [\"x\"]\n")
	s.write(".satelle/deployed.version", "satelle.version: 0.0.879\n")
	s.write(".satelle/workflows/step.toml", "[meta]\nname = \"step\"\n")
	s.write(".satelle/workflows/done.toml", "[meta]\nname = \"done\"\n")
	s.write(".satelle/workflows/agents.toml", "[coder]\nrole = \"agent\"\n")
	s.write(".satelle/skills/plan.md", "---\nname: plan\n---\nPlan it.\n")
	writeFile(t, filepath.Join(s.dir, ".satelle", "hooks", "satelle-hook.sh"), "#!/bin/sh\n", 0o755)
	if err := os.Symlink(filepath.Join(s.home, "logs"), filepath.Join(s.dir, ".satelle", "logs")); err != nil {
		t.Fatal(err)
	}
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "a pre-1.0 .satelle")
}
