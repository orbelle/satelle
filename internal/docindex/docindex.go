// Package docindex is satelle's directory monitor for authored markdown.
//
// The architecture splits the system of record: stories/tasks/ledger are
// dynamic sqlite primitives, while authored artifacts (documents, workflows,
// principles, skills) are MARKDOWN ON DISK — the files are the source of truth.
// This package syncs those files into a sqlite index so the CLI and web can
// query them without the markdown becoming a hand-managed store.
//
// Sync is the core: walk the configured per-kind dirs, upsert changed files
// (detected by size+mtime), and prune rows whose file disappeared. Watch wraps
// Sync in a poll loop — a dependency-free monitor (satellites indexes by
// scanning, not fsnotify), so the static no-cgo binary stays dependency-light.
// SQL is libSQL-compatible.
package docindex

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Doc is one indexed authored file.
type Doc struct {
	Kind string `json:"kind"`
	Name string `json:"name"` // filename without its extension
	Path string `json:"path"` // absolute path on disk, or embedded:<kind>/<name><ext>
	// Ext is the source extension (".md" or ".toml"). Set on embedded defaults so
	// SetDefaults can synthesise an honest provenance path; on-disk docs carry it
	// in Path already (sty_81bb0dde).
	Ext       string    `json:"ext,omitempty"`
	Headline  string    `json:"headline,omitempty"`
	Body      string    `json:"body"`
	Hash      string    `json:"hash"` // sha256 of body, hex
	Size      int64     `json:"size"`
	ModTime   time.Time `json:"mod_time"`
	IndexedAt time.Time `json:"indexed_at"`
	Embedded  bool      `json:"embedded,omitempty"` // a binary-shipped canonical default, not an on-disk file
	// Unreadable is set only on the UnreadableSentinel doc: the reason the kind's
	// authored dir exists but cannot be read (sty_d6e209aa).
	Unreadable string `json:"unreadable,omitempty"`
}

// schema is the authored-docs index, keyed by (kind, path). Self-migrating.
const schema = `
CREATE TABLE IF NOT EXISTS authored_docs (
    kind       TEXT NOT NULL,
    name       TEXT NOT NULL,
    path       TEXT NOT NULL,
    headline   TEXT NOT NULL DEFAULT '',
    body       TEXT NOT NULL DEFAULT '',
    hash       TEXT NOT NULL DEFAULT '',
    size       INTEGER NOT NULL DEFAULT 0,
    mod_time   TEXT NOT NULL,
    indexed_at TEXT NOT NULL,
    PRIMARY KEY (kind, path)
);
CREATE INDEX IF NOT EXISTS idx_authored_docs_kind ON authored_docs(kind, name);`

// Migrate creates the authored_docs table on db. Idempotent.
func Migrate(db *sql.DB) error {
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("docindex: migrate: %w", err)
	}
	return nil
}

// Store indexes authored markdown into the authored_docs table. It also carries
// the binary's embedded canonical defaults, consulted ONLY as a by-name fallback in
// Get (sty_94da9ac9): List and Count enumerate just the on-disk .satelle docs, so a
// default is never shown as a project doc — it resolves by name (the gating baseline,
// on-demand principles) but is otherwise materialised onto disk by init. The disk
// index itself (Sync) stays purely file-driven.
type Store struct {
	db       *sql.DB
	defaults []Doc // embedded canonical defaults, normalised; keyed by (Kind, Name)
	// roots is where each authored kind is read from. When set, List("workflows")
	// best-effort Syncs that kind first so a corrected file is visible to
	// gating without a manual reindex (sty_5b88aa1b).
	roots Roots
}

// New returns a Store bound to db.
func New(db *sql.DB) *Store { return &Store{db: db} }

// SetRoots records the authored-dir map used by List to refresh workflows.
func (s *Store) SetRoots(dirs map[string]string) { s.SetAuthoredRoots(DirRoots(dirs)) }

// SetAuthoredRoots records the authored roots, project tier included, used by
// List to refresh workflows.
func (s *Store) SetAuthoredRoots(r Roots) {
	if s == nil {
		return
	}
	s.roots = r
}

// SetDefaults installs the embedded canonical defaults overlaid under the disk
// index. Each input needs only Kind, Name, and Body; the rest (Headline, Hash,
// synthetic Path, Embedded flag) is filled here. Replaces any prior defaults.
func (s *Store) SetDefaults(defs []Doc) {
	out := make([]Doc, 0, len(defs))
	for _, d := range defs {
		if d.Headline == "" {
			d.Headline = headline(d.Body)
		}
		if d.Hash == "" {
			sum := sha256.Sum256([]byte(d.Body))
			d.Hash = hex.EncodeToString(sum[:])
		}
		if d.Path == "" {
			// Ext defaults to .md, the DOCUMENT form; a caller supplying a TOML
			// default (the route source) sets it so provenance names the real file
			// rather than a markdown one that does not exist (sty_81bb0dde).
			ext := d.Ext
			if ext == "" {
				ext = ".md"
			}
			d.Path = "embedded:" + d.Kind + "/" + d.Name + ext
		}
		d.Size = int64(len(d.Body))
		d.Embedded = true
		out = append(out, d)
	}
	s.defaults = out
}

// SyncResult reports what a Sync pass changed.
type SyncResult struct {
	Indexed int      `json:"indexed"`           // files inserted or updated
	Pruned  int      `json:"pruned"`            // index rows whose file no longer exists
	Scanned int      `json:"scanned"`           // .md files seen on disk
	Changed []DocRef `json:"changed,omitempty"` // the (kind, name) upserted this pass
}

// DocRef identifies an authored doc by kind and name.
type DocRef struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// Sync brings the index in line with the markdown on disk for the given
// kind→dir map. For each kind it walks the dir (recursively), upserts every
// .md file whose size+mtime differs from the index, and prunes rows for files
// that disappeared. A missing dir is not an error — its rows are pruned (the
// kind simply has no authored content yet).
func (s *Store) Sync(ctx context.Context, dirs map[string]string, now time.Time) (SyncResult, error) {
	return s.SyncRoots(ctx, DirRoots(dirs), now)
}

// SyncRoots is Sync over Roots: for each kind it indexes the repo files no team
// path owns and the project tier's files one does, and prunes every other row —
// so a repo copy of a team-owned file never enters the index.
func (s *Store) SyncRoots(ctx context.Context, roots Roots, now time.Time) (SyncResult, error) {
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	dirs := roots.Dirs
	var res SyncResult
	for _, kind := range sortedKeys(dirs) {
		dir := dirs[kind]
		tierDir := roots.TierDir(kind)
		// Documents are normalised to the OKF standard BEFORE the skip-unchanged
		// check, so a frontmatter-less or type-less concept file is back-filled
		// with OKF frontmatter (required `type` + recommended fields) even when it
		// was indexed by an earlier build. Idempotent; reserved index.md/log.md and
		// already-conformant docs are left untouched.
		for _, d := range []string{dir, tierDir} {
			if d == "" {
				continue
			}
			if kind == "documents" {
				normalizeOKFDir(d)
			} else if t := authoredType(kind); t != "" {
				// Authored substrate (skills/workflows/principles) is normalised to the
				// OKF `type` key BEFORE the skip-unchanged check, migrating a legacy
				// `kind:` and back-filling a missing one — idempotently, in place.
				normalizeTypeDir(d, t)
			}
		}
		onDisk, err := roots.kindFiles(kind)
		if err != nil {
			return res, fmt.Errorf("docindex: scan %s: %w", dir, err)
		}
		res.Scanned += len(onDisk)

		indexed, err := s.indexedPaths(ctx, kind)
		if err != nil {
			return res, err
		}
		seen := make(map[string]struct{}, len(onDisk))
		for _, fileInfo := range onDisk {
			seen[fileInfo.path] = struct{}{}
			prev, ok := indexed[fileInfo.path]
			if ok && prev.size == fileInfo.size && prev.mod.Equal(fileInfo.mod) {
				continue // unchanged — skip the read+write
			}
			if err := s.upsert(ctx, kind, fileInfo, now); err != nil {
				return res, err
			}
			res.Indexed++
			res.Changed = append(res.Changed, DocRef{
				Kind: kind,
				Name: strings.TrimSuffix(filepath.Base(fileInfo.path), filepath.Ext(fileInfo.path)),
			})
		}
		for path := range indexed {
			if _, ok := seen[path]; !ok {
				if err := s.delete(ctx, kind, path); err != nil {
					return res, err
				}
				res.Pruned++
			}
		}
	}
	// Regenerate the documents bundle-root index.md (OKF progressive disclosure)
	// from the now-current index. Best-effort: an index write failure must not
	// fail the whole sync.
	if dir, ok := dirs["documents"]; ok {
		if docs, err := s.List(ctx, "documents"); err == nil {
			_ = s.writeOKFIndex(dir, docs)
		}
	}
	return res, nil
}

// Watch runs Sync immediately, then on every interval tick until ctx is
// cancelled. onSync, if non-nil, is called with each pass's result (and any
// error) so callers can log progress. It returns ctx.Err() when cancelled.
// This is the "directory monitor": a poll loop, dependency-free.
func (s *Store) Watch(ctx context.Context, dirs map[string]string, interval time.Duration, onSync func(SyncResult, error)) error {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	run := func() {
		res, err := s.Sync(ctx, dirs, time.Now())
		if onSync != nil {
			onSync(res, err)
		}
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			run()
		}
	}
}

// List returns the effective docs for a kind, name-sorted: on-disk rows plus
// embedded defaults whose (kind,name) is not present on disk (sty_29e5a9a5 /
// epic:substrate-planes — virtual sparse defaults). Disk always wins. Empty kind
// returns every kind. Sync stays file-driven; the overlay is READ-TIME only.
func (s *Store) List(ctx context.Context, kind string) ([]Doc, error) {
	// Best-effort: a workflows List is what every gating path reads. Refresh
	// that kind from disk so a corrected done.toml is visible without reindex.
	if strings.TrimSpace(kind) == "workflows" && s.roots.Dirs != nil {
		if dir := strings.TrimSpace(s.roots.Dirs["workflows"]); dir != "" {
			// An unreadable workflows dir is an authored process that exists and
			// cannot be read — never an absent one. Return ONLY the sentinel: no
			// stale index rows and no embedded overlay, so no precedence rule can
			// silently govern by the binary's default (sty_d6e209aa).
			if bad, perr, ok := s.roots.probeUnreadable("workflows"); ok {
				return []Doc{UnreadableDoc("workflows", bad, perr)}, nil
			}
			// A file or subdirectory inside a readable dir that cannot be read is
			// the same fact one level down: the authored process cannot be seen,
			// so it must not be silently replaced by the embedded half of that
			// name. Any other Sync error (the index itself) stays best-effort.
			_, serr := s.SyncRoots(ctx, s.roots.Only("workflows"), time.Now())
			var rerr *ReadError
			if errors.As(serr, &rerr) {
				return []Doc{UnreadableDoc("workflows", rerr.Path, rerr.Err)}, nil
			}
		}
	}
	if k := strings.TrimSpace(kind); k != "" && k != "workflows" {
		s.refreshKind(ctx, k)
	}
	return s.ListIndexed(ctx, s.db, kind)
}

// refreshKind re-indexes one authored kind from disk, best-effort, so a read
// sees the files as they are now. With the reindex command gone (B1) every
// read keeps the index current itself.
func (s *Store) refreshKind(ctx context.Context, kind string) {
	if s.roots.Dirs == nil || strings.TrimSpace(s.roots.Dirs[kind]) == "" {
		return
	}
	_, _ = s.SyncRoots(ctx, s.roots.Only(kind), time.Now())
}

// RowQuerier is the one method ListIndexed needs: the plain handle or the open
// transaction a caller is already inside.
type RowQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// ListIndexed is List's read without the workflows refresh from disk: the indexed
// rows plus the embedded defaults, read through q. A caller already inside a
// transaction passes it (the pool holds one connection, so a read on the plain
// handle would wait on the transaction forever); it sees the index as of the last
// Sync, which every gating read of workflows has just run.
func (s *Store) ListIndexed(ctx context.Context, q RowQuerier, kind string) ([]Doc, error) {
	query := `SELECT kind, name, path, headline, body, hash, size, mod_time, indexed_at FROM authored_docs`
	var args []any
	if strings.TrimSpace(kind) != "" {
		query += ` WHERE kind = ?`
		args = append(args, kind)
	}
	query += ` ORDER BY kind ASC, name ASC`
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("docindex: list: %w", err)
	}
	defer rows.Close()
	out := []Doc{}
	for rows.Next() {
		var (
			d              Doc
			modS, indexedS string
		)
		if err := rows.Scan(&d.Kind, &d.Name, &d.Path, &d.Headline, &d.Body,
			&d.Hash, &d.Size, &modS, &indexedS); err != nil {
			return nil, fmt.Errorf("docindex: scan: %w", err)
		}
		d.ModTime = parseTime(modS)
		d.IndexedAt = parseTime(indexedS)
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return overlayDefaults(out, s.defaults, kind), nil
}

// overlayDefaults appends the embedded defaults whose (kind,name) has no doc in
// disk — disk always wins — honouring a kind filter (empty = every kind), and
// re-sorts so SessionStart injection order matches pure-disk ordering (AC5). It
// is the one overlay List and ReadWorkflowDocs share.
func overlayDefaults(disk, defaults []Doc, kind string) []Doc {
	seen := make(map[string]struct{}, len(disk))
	for _, d := range disk {
		seen[d.Kind+"\x00"+d.Name] = struct{}{}
	}
	kindFilter := strings.TrimSpace(kind)
	out := disk
	for _, def := range defaults {
		if kindFilter != "" && def.Kind != kindFilter {
			continue
		}
		if _, ok := seen[def.Kind+"\x00"+def.Name]; ok {
			continue
		}
		out = append(out, def)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// ReadWorkflowDocs is the read-only twin of Store.List("workflows"): it reads the
// authored workflows in dir straight from disk and overlays defaults, with no
// index and no write of any kind — the way to read ANOTHER repo's process without
// touching its database. It keeps List's unreadable semantics: an unreadable dir,
// or a file or subdirectory inside it that cannot be read, yields ONLY the
// UnreadableDoc sentinel (no embedded overlay), so a process that exists and
// cannot be seen is never silently replaced by the binary's default
// (sty_d6e209aa). An absent or empty dir yields the defaults alone.
func ReadWorkflowDocs(dir string, defaults []Doc) []Doc {
	return ReadWorkflowDocsIn(DirRoots(map[string]string{"workflows": dir}), defaults)
}

// ReadWorkflowDocsIn is ReadWorkflowDocs over Roots, so a project tier's
// workflows replace the repo's for every path the team owns.
func ReadWorkflowDocsIn(roots Roots, defaults []Doc) []Doc {
	dir := strings.TrimSpace(roots.Dirs["workflows"])
	disk := []Doc{}
	if dir != "" {
		if bad, perr, ok := roots.probeUnreadable("workflows"); ok {
			return []Doc{UnreadableDoc("workflows", bad, perr)}
		}
		files, err := roots.kindFiles("workflows")
		if err != nil {
			return []Doc{unreadableFrom(dir, err)}
		}
		for _, fi := range files {
			d, err := docFromFile("workflows", fi)
			if err != nil {
				return []Doc{unreadableFrom(fi.path, err)}
			}
			disk = append(disk, d)
		}
	}
	return overlayDefaults(disk, defaults, "workflows")
}

// unreadableFrom builds the sentinel for err, naming the path a *ReadError carries
// and falling back to path for any other failure.
func unreadableFrom(path string, err error) Doc {
	var rerr *ReadError
	if errors.As(err, &rerr) {
		return UnreadableDoc("workflows", rerr.Path, rerr.Err)
	}
	return UnreadableDoc("workflows", path, err)
}

// Count returns the effective doc count for a kind (empty kind = all kinds),
// matching List's disk+virtual overlay (sty_29e5a9a5).
func (s *Store) Count(ctx context.Context, kind string) (int, error) {
	list, err := s.List(ctx, kind)
	if err != nil {
		return 0, err
	}
	return len(list), nil
}

// Fingerprint returns a cheap change-signal for the index — count plus the
// latest indexed_at — so a poller can detect mutations without loading bodies.
func (s *Store) Fingerprint(ctx context.Context) (string, error) {
	var (
		n   int
		max string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(MAX(indexed_at), '') FROM authored_docs`).Scan(&n, &max)
	if err != nil {
		return "", fmt.Errorf("docindex: fingerprint: %w", err)
	}
	return fmt.Sprintf("%d:%s", n, max), nil
}

// Get returns one indexed doc by (kind, name), or ErrNotFound.
func (s *Store) Get(ctx context.Context, kind, name string) (Doc, error) {
	s.refreshKind(ctx, kind)
	row := s.db.QueryRowContext(ctx,
		`SELECT kind, name, path, headline, body, hash, size, mod_time, indexed_at
		   FROM authored_docs WHERE kind = ? AND name = ?`, kind, name)
	var (
		d              Doc
		modS, indexedS string
	)
	err := row.Scan(&d.Kind, &d.Name, &d.Path, &d.Headline, &d.Body,
		&d.Hash, &d.Size, &modS, &indexedS)
	if errors.Is(err, sql.ErrNoRows) {
		// No on-disk doc — fall through to an embedded default, if any.
		for _, def := range s.defaults {
			if def.Kind == kind && def.Name == name {
				return def, nil
			}
		}
		return Doc{}, ErrNotFound
	}
	if err != nil {
		return Doc{}, fmt.Errorf("docindex: get: %w", err)
	}
	d.ModTime = parseTime(modS)
	d.IndexedAt = parseTime(indexedS)
	return d, nil
}

// ErrNotFound is returned when a Get misses.
var ErrNotFound = errors.New("docindex: not found")

// --- internals ---

type fileInfo struct {
	path string
	size int64
	mod  time.Time
}

type indexedMeta struct {
	size int64
	mod  time.Time
}

// walkMarkdown returns every .md file under dir (recursively). A non-existent
// dir yields an empty set (not an error) so an unconfigured kind is benign.
func walkMarkdown(dir string) ([]fileInfo, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, nil
	}
	if state, err := ProbeDir(dir); state == DirAbsent {
		return nil, nil
	} else if state == DirUnreadable {
		return nil, err
	}
	var out []fileInfo
	walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Skip testdata/ subtrees — by Go convention they hold fixtures, not
		// authored substrate, so they must not be indexed or structure-checked.
		if d.IsDir() {
			if d.Name() == "testdata" {
				return fs.SkipDir
			}
			// A non-root subdirectory carrying its own index.md is a self-managed OKF
			// SUB-BUNDLE (e.g. documents/story-implementation-summary/): it owns its
			// index/log and is surfaced as ONE entry in the parent, so its concept
			// files are NOT flattened into the parent kind's index (progressive
			// disclosure). Skip its subtree.
			if path != dir {
				if _, err := os.Stat(filepath.Join(path, "index.md")); err == nil {
					return fs.SkipDir
				}
			}
			return nil
		}
		if !Indexable(path) {
			return nil
		}
		// README.md is a dir-descriptor (what the dir should contain), not authored
		// substrate — skip it so it is never indexed, validated, or normalised.
		if strings.EqualFold(filepath.Base(path), "README.md") {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			abs = path
		}
		out = append(out, fileInfo{path: abs, size: fi.Size(), mod: fi.ModTime().UTC()})
		return nil
	})
	if walkErr != nil {
		return nil, &ReadError{Path: dir, Err: walkErr}
	}
	return out, nil
}

func (s *Store) indexedPaths(ctx context.Context, kind string) (map[string]indexedMeta, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT path, size, mod_time FROM authored_docs WHERE kind = ?`, kind)
	if err != nil {
		return nil, fmt.Errorf("docindex: indexed paths: %w", err)
	}
	defer rows.Close()
	out := map[string]indexedMeta{}
	for rows.Next() {
		var (
			path, modS string
			size       int64
		)
		if err := rows.Scan(&path, &size, &modS); err != nil {
			return nil, err
		}
		out[path] = indexedMeta{size: size, mod: parseTime(modS)}
	}
	return out, rows.Err()
}

// docFromFile reads one authored file into a Doc, exactly as authored. A read
// failure carries a *ReadError, so a caller can tell an unreadable file from any
// other failure.
func docFromFile(kind string, fi fileInfo) (Doc, error) {
	body, err := os.ReadFile(fi.path)
	if err != nil {
		return Doc{}, fmt.Errorf("docindex: read %s: %w", fi.path, &ReadError{Path: fi.path, Err: err})
	}
	// Workflows are indexed exactly as authored. The DOT normalisation that used
	// to run here existed only to feed the DOT parser; with that front end retired
	// a lifecycle is done.toml + step.toml, which the index stores verbatim
	// (sty_d953c5d8).
	sum := sha256.Sum256(body)
	return Doc{
		Kind:     kind,
		Name:     strings.TrimSuffix(filepath.Base(fi.path), filepath.Ext(fi.path)),
		Path:     fi.path,
		Headline: headline(string(body)),
		Body:     string(body),
		Hash:     hex.EncodeToString(sum[:]),
		Size:     int64(len(body)),
		ModTime:  fi.mod,
	}, nil
}

func (s *Store) upsert(ctx context.Context, kind string, fi fileInfo, now time.Time) error {
	d, err := docFromFile(kind, fi)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
        INSERT INTO authored_docs (kind, name, path, headline, body, hash, size, mod_time, indexed_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(kind, path) DO UPDATE SET
            name=excluded.name, headline=excluded.headline, body=excluded.body,
            hash=excluded.hash, size=excluded.size, mod_time=excluded.mod_time,
            indexed_at=excluded.indexed_at`,
		d.Kind, d.Name, d.Path, d.Headline, d.Body, d.Hash, d.Size,
		fi.mod.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("docindex: upsert %s: %w", fi.path, err)
	}
	return nil
}

func (s *Store) delete(ctx context.Context, kind, path string) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM authored_docs WHERE kind = ? AND path = ?`, kind, path); err != nil {
		return fmt.Errorf("docindex: delete %s: %w", path, err)
	}
	return nil
}

// headline returns the one-line summary a list surface shows beside a doc's
// name: the first non-blank line after any YAML frontmatter, with a leading
// "# " heading marker stripped. Empty for an empty body.
//
// A TOML doc has no prose to take a first line FROM — its first line after the
// header is another table — so it uses its frontmatter `description` instead.
// Without this the route source listed itself as "[meta]" on `workflow list`
// and in the web panel (sty_81bb0dde).
func headline(body string) string {
	lines := strings.Split(body, "\n")
	i := 0
	// Skip a leading YAML frontmatter block (--- … ---).
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		for j := 1; j < len(lines); j++ {
			if strings.TrimSpace(lines[j]) == "---" {
				i = j + 1
				break
			}
		}
	} else if fm, ok := tomlMetaLines(body); ok {
		for _, ln := range fm {
			if rest, hit := strings.CutPrefix(ln, "description:"); hit {
				return strings.TrimSpace(rest)
			}
		}
		return ""
	}
	for ; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if t == "" {
			continue
		}
		return strings.TrimSpace(strings.TrimLeft(t, "#"))
	}
	return ""
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func parseTime(s string) time.Time {
	if strings.TrimSpace(s) == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
