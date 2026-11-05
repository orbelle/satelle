// Package store opens the per-repo sqlite database (.satelle/satelle.db) and
// wires the dynamic-primitive stores plus the authored-doc index onto the one
// shared handle.
//
// It follows satellites' internal/workstate opener verbatim — pure-Go
// modernc.org/sqlite (no cgo), WAL, busy_timeout, _txlock=immediate, and
// SetMaxOpenConns(1) so all writes funnel through a single writer (sqlite
// permits exactly one). Each domain package owns its own schema and self-
// migrates; Open orchestrates them so a fresh repo needs no setup step.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/lease"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/storyfold"
	"github.com/bobmcallan/satelle/internal/wfgovern"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// DB is the opened per-repo database with its domain stores attached. Callers
// reach data through the sub-stores; the raw handle stays encapsulated.
type DB struct {
	db       *sql.DB
	Ledger   *ledger.Store
	Stories  *workitem.Store // stories and tasks share one store; filter by Kind
	DocIndex *docindex.Store
	Leases   *lease.Store // engagement seats (sty_8426b9c0)
}

// Open opens (creating if absent) the sqlite database at path, migrates every
// dynamic-primitive and index schema, and returns the wired DB. Parent dirs
// are created as needed, so Open(".satelle/satelle.db") works on a fresh repo.
func Open(path string) (*DB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("store: empty database path")
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("store: mkdir %s: %w", dir, err)
		}
	}
	// WAL + busy_timeout for concurrent readers and a contended single writer;
	// _txlock=immediate so a read-then-write txn takes its write lock at BEGIN
	// (where busy_timeout applies) instead of erroring on lock upgrade.
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_txlock=immediate"
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	// sqlite permits exactly one writer; cap the pool to one connection so
	// concurrent writers serialise (wait) under busy_timeout rather than racing
	// the lock and surfacing SQLITE_BUSY. The per-repo file is low-volume, so a
	// single connection is not a throughput concern.
	sqldb.SetMaxOpenConns(1)

	for _, migrate := range []func(*sql.DB) error{
		ledger.Migrate,
		workitem.Migrate,
		docindex.Migrate,
		lease.Migrate,
	} {
		if err := migrate(sqldb); err != nil {
			sqldb.Close()
			return nil, err
		}
	}

	// Every migration has settled the rows, so a story whose chain predates the
	// fold is seeded from its row now (sty_d84be8bc). Not in wire, which
	// OpenReadOnly shares, and not in ledger.Migrate, which runs before the rows
	// are migrated.
	d := wire(sqldb)
	if _, err := storyfold.Seed(context.Background(), sqldb, d.Ledger, time.Time{}); err != nil {
		sqldb.Close()
		return nil, err
	}
	return d, nil
}

// wire attaches the domain stores to an opened handle. It runs no migration and
// writes nothing, so Open and OpenReadOnly share it.
func wire(sqldb *sql.DB) *DB {
	di := docindex.New(sqldb)
	di.SetDefaults(EmbeddedDefaultDocs())
	led := ledger.New(sqldb)
	led.SetWorkflowSource(workflowVersion(di))
	return &DB{
		db:       sqldb,
		Ledger:   led,
		Stories:  workitem.New(sqldb),
		DocIndex: di,
		Leases:   lease.New(sqldb),
	}
}

// workflowVersion is the ledger's resolver for the workflow a story is judged
// under (sty_41f61669): the content hash of the workflow wfgovern says governs the
// story, "" when the story is unknown or no workflow applies, with the source that
// hashes to it (sty_d84be8bc). It reads through the handle the append runs on, so
// it is safe inside a transaction.
func workflowVersion(di *docindex.Store) ledger.WorkflowSourceFunc {
	return func(ctx context.Context, c ledger.Conn, storyID string) ledger.WorkflowSource {
		item, err := workitem.NewOn(c).Get(ctx, storyID)
		if err != nil {
			return ledger.WorkflowSource{}
		}
		docs, err := di.ListIndexed(ctx, c, "workflows")
		if err != nil {
			return ledger.WorkflowSource{}
		}
		v := wfgovern.RouteVersionSource(docs, item)
		return ledger.WorkflowSource{Hash: v.Hash, Done: v.Done, Step: v.Step, Legacy: v.Legacy}
	}
}

// ReadOnlyDSN is the one definition of a read-only sqlite connection string: the
// file opened mode=ro with a busy timeout. It deliberately carries no query_only
// pragma — VACUUM INTO, which cli.vacuumInto runs through it, is a read of the
// source that query_only could refuse — so OpenReadOnly adds that itself.
func ReadOnlyDSN(path string) string {
	return "file:" + filepath.ToSlash(path) + "?mode=ro&_pragma=busy_timeout(5000)"
}

// OpenReadOnly opens an EXISTING database at path for reading only, for a caller
// that must see another repo's stories without touching that repo: mode=ro and
// query_only, no mkdir, no create, no migration, no marker check, no journal_mode
// change. An absent path is an error and nothing is created. A database whose
// schema this build does not know fails at the read, not here, so the caller
// reports it rather than treating the data as present.
//
// immutable=1 is not used: the other repo may be live, and an immutable open
// would serve a stale page set. Under mode=ro SQLite may create transient
// -wal/-shm sidecars beside a WAL database; they carry no story data and the
// main database file is not written.
func OpenReadOnly(path string) (*DB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("store: empty database path")
	}
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("store: open read-only %s: %w", path, err)
	}
	sqldb, err := sql.Open("sqlite", ReadOnlyDSN(path)+"&_pragma=query_only(1)")
	if err != nil {
		return nil, fmt.Errorf("store: open read-only %s: %w", path, err)
	}
	sqldb.SetMaxOpenConns(1)
	// sql.Open is lazy; a file that is not a database fails here, not at first read.
	if err := sqldb.Ping(); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("store: open read-only %s: %w", path, err)
	}
	return wire(sqldb), nil
}

func init() {
	wfgovern.EmbeddedRoute = embeddedWorkflowBody
}

// embeddedWorkflowBody is the shipped body of a workflow default by name, the
// baseline wfgovern overlays a repo's route halves onto.
func embeddedWorkflowBody(name string) string {
	for _, d := range config.EmbeddedDefaults() {
		if d.Kind == "workflows" && d.Name == name {
			return d.Body
		}
	}
	return ""
}

// EmbeddedDefaultDocs maps the binary's canonical default substrate into
// docindex.Doc seeds (Kind/Name/Body) for the read-time overlay. config is the
// single source of those bytes; the doc index fills in headline/hash/path.
func EmbeddedDefaultDocs() []docindex.Doc {
	defs := config.EmbeddedDefaults()
	out := make([]docindex.Doc, 0, len(defs))
	for _, d := range defs {
		if d.Kind == "tasks" {
			// TASKS are workitem-store items ingested by SyncTasks, not OKF docs —
			// keep this Get-fallback overlay to OKF kinds or `doc get` would surface
			// a task as a doc (sty_d4360e90).
			continue
		}
		out = append(out, docindex.Doc{Kind: d.Kind, Name: d.Name, Body: d.Body, Ext: d.Ext})
	}
	return out
}

// SQL exposes the raw handle for callers that need it (tests, future stores).
func (d *DB) SQL() *sql.DB { return d.db }

// InTx runs fn inside a single sqlite transaction on the shared handle.
// Rollback on any error (or panic); commit only when fn returns nil.
func (d *DB) InTx(ctx context.Context, fn func(*sql.Tx) error) error {
	if d == nil || d.db == nil {
		return fmt.Errorf("store: InTx on a nil database")
	}
	if fn == nil {
		return fmt.Errorf("store: InTx missing func")
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit tx: %w", err)
	}
	return nil
}

// Close releases the underlying database handle.
func (d *DB) Close() error { return d.db.Close() }
