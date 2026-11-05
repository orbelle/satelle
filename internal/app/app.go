// Package app is satelle's local bootstrap (build order step 3). It loads the
// config, resolves the repo root, opens the per-repo database (home-keyed under
// ~/.satelle/<repo-key>/ by default — sty_4660bbe1), and wires the dynamic
// stores + authored-doc index onto it — the in-process path every CLI command
// (and the local web server) reaches data through.
//
// The OSS tier is always local, so there is no remote-dispatch branch: Open is
// the whole "backend". Zero-config works — a repo with no satelle.toml (but
// WITH a .satelle/ directory) falls back to defaults against the current
// directory. A repo with no .satelle/ at all is not governed by satelle and
// Open refuses it with ErrNotInitialised rather than materialising a runtime
// plane for it (sty_20a7824c).
//
// A linked worktree with no data dir of its own is governed when the main tree
// has one (sty_ddbe2669). Open keeps the invoking tree as RepoRoot — edits and
// tests stay there — and reads workflows, skills, principles, documents, the
// constitution and the agents layer from the data dir the main tree's loaded
// config names. That data dir is never copied into the worktree.
package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/store"
)

// App is the wired local runtime: resolved config + the open per-repo database.
//
// Config and DataDir are the location plane: the config loaded from the
// invoking tree, and the data dir that config names under RepoRoot. Sync,
// migrate, init, and runtime write that plane. ProcessConfig, ProcessRoot,
// and ProcessDataDir are the process of record: the main tree's loaded
// config and the data dir it names. They differ for a linked worktree. The
// worktree is where code is edited and tested; the main tree is the process
// the worktree executes.
//
// RuntimeDir holds the DB, logs, backups, and stories cache
// (~/.satelle/<repo-key>/, or legacy data_dir until `satelle runtime migrate`).
type App struct {
	Config         config.Config // location config: config.Load of the invoking tree
	ProcessConfig  config.Config // process of record
	RepoRoot       string        // invoking tree: edits, tests, harness, drift
	ProcessRoot    string        // process of record; Open always sets it
	DataDir        string        // location data dir: Config.ResolveDataDir(RepoRoot)
	ProcessDataDir string        // process read-plane
	RuntimeDir     string        // runtime plane: satelle.db, logs/, backups/, stories/
	DBPath         string
	Store          *store.DB
}

// PlaneConfig is the process of record. Open always sets ProcessRoot and
// stores that config on ProcessConfig, including a zero config when the main
// tree has a data dir and no satelle.toml. A hand-built App leaves ProcessRoot
// empty and has only Config.
func (a *App) PlaneConfig() config.Config {
	if a == nil {
		return config.Config{}
	}
	if strings.TrimSpace(a.ProcessRoot) != "" {
		return a.ProcessConfig
	}
	return a.Config
}

// PlaneDir is the process read-plane data dir. Open sets ProcessDataDir.
// A hand-built App that only set DataDir keeps that directory.
func (a *App) PlaneDir() string {
	if a == nil {
		return ""
	}
	if strings.TrimSpace(a.ProcessRoot) != "" {
		if strings.TrimSpace(a.ProcessDataDir) != "" {
			return a.ProcessDataDir
		}
		return config.ResolveProcessDataDir(a.ProcessConfig, a.ProcessRoot)
	}
	if strings.TrimSpace(a.DataDir) != "" {
		return a.DataDir
	}
	return a.Config.ResolveDataDir(a.RepoRoot)
}

// PlaneConstitution is the process constitution path.
func (a *App) PlaneConstitution() string {
	if a == nil {
		return ""
	}
	if strings.TrimSpace(a.ProcessRoot) != "" {
		return config.ResolveProcessConstitution(a.ProcessConfig, a.ProcessRoot)
	}
	return a.Config.ResolveConstitution(a.RepoRoot)
}

// ErrNotInitialised reports that the working directory is not inside a satelle
// repo — no `.satelle/` directory here or in any parent. Callers compare with
// errors.Is and translate it into their own operator-facing message: the CLI
// says "run satelle init", the session hooks treat it as "not governed" and go
// inert. It carries the repo root that was checked.
var ErrNotInitialised = errors.New("not a satelle repo (no .satelle/ here or in any parent) — run `satelle init`")

// NotInitialisedError is the concrete ErrNotInitialised carrying the root that
// was checked, so a caller can name the path it refused.
type NotInitialisedError struct{ RepoRoot string }

func (e *NotInitialisedError) Error() string {
	return fmt.Sprintf("not a satelle repo (no .satelle/ in %s or any parent) — run `satelle init`", e.RepoRoot)
}
func (e *NotInitialisedError) Is(target error) bool { return target == ErrNotInitialised }
func (e *NotInitialisedError) Unwrap() error        { return ErrNotInitialised }

// Open loads config, opens the database, and returns the wired App. A missing
// config is not an error — the zero-value Config runs on defaults against the
// current directory (zero-config). The caller owns Close. A still-unmigrated
// legacy DB emits a one-line deprecation note on stderr (stdout stays clean
// for JSON).
//
// The location load is config.Load("") — the invoking tree. Open then calls
// LoadProcess, which derives that root itself. When the tree is a linked
// worktree of a governed main tree, the process of record is the main tree's
// loaded config, including a data_dir it names that is not .satelle.
// Discovery (FindDataDir, and the walk Load uses to find a config) stays on
// a literal .satelle. The data dir is never created in the worktree.
//
// The governance guard checks that the process data dir exists and is a
// directory. It does not use FindDataDir. A linked worktree with no data dir
// of its own opens when the main tree's process data dir exists. A main tree
// with no data dir still returns ErrNotInitialised, and nothing is written.
// This is the single governance guard for every store-backed verb and every
// session hook, because Open is the one seam through which they all reach the
// two writes below — store.Open (which MkdirAll's the runtime plane and
// creates+migrates the database) and WriteRepoPathMarker. Guarding it
// per-verb would leave the rest still materialising a plane for a repo
// satelle does not govern (sty_20a7824c). `satelle init` does not come through
// here — it calls store.Open and WriteRepoPathMarker directly, which is how
// creating stays possible.
//
// Runtime-dir resolution, the repo.path marker, and the database path stay
// on the location config and the invoking root. The marker itself records
// CanonicalRepoRoot. Sync, migrate, and runtime read DataDir, which stays
// the location join.
func Open() (*App, error) {
	loc, locPath, err := config.Load("")
	if err != nil && !errors.Is(err, config.ErrNotFound) {
		return nil, err
	}
	proc, procRoot, _, err := config.LoadProcess(loc, locPath)
	if err != nil {
		return nil, err
	}
	return OpenLoaded(loc, locPath, proc, procRoot)
}

// OpenLoaded is Open from a config load the caller already made: loc and locPath
// are the config.Load("") result, proc and procRoot the config.LoadProcess result
// for them. A hook that has resolved its config opens the store from it without
// loading again.
func OpenLoaded(loc config.Config, locPath string, proc config.Config, procRoot string) (*App, error) {
	// Repo root as the location load has always computed it: the directory
	// that holds the file Load found, otherwise the working directory.
	invoking := config.InvokingRoot(locPath)

	// Governance guard — before store.Open and WriteRepoPathMarker, so an
	// ungoverned repo costs zero writes. Keyed on the process data dir the
	// loaded config names, not on a literal .satelle of the invoking tree,
	// and not on satelle.toml: a zero-config main tree has a directory and
	// no file. When the main tree has no data dir, CanonicalRepoRoot stays
	// on the invoking tree, this path does not exist, and Open refuses.
	processDataDir := config.ResolveProcessDataDir(proc, procRoot)
	info, serr := os.Stat(processDataDir)
	if serr != nil || !info.IsDir() {
		root := invoking
		if strings.TrimSpace(root) == "" || root == "." {
			if cwd, e := os.Getwd(); e == nil {
				root = cwd
			} else if strings.TrimSpace(procRoot) != "" {
				root = procRoot
			}
		}
		return nil, &NotInitialisedError{RepoRoot: root}
	}

	dataDir := loc.ResolveDataDir(invoking)
	rt := loc.ResolveRuntimeDir(invoking)
	dbPath := loc.ResolveDB(invoking)
	if note := loc.LegacyRuntimeNote(invoking); note != "" {
		fmt.Fprintln(os.Stderr, note)
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return nil, err
	}
	// The marker records CanonicalRepoRoot of the invoking tree. For a linked
	// worktree of a governed main tree that is the same path LoadProcess
	// returned. A marker that was not written (legacy runtime dir) keeps
	// procRoot. SATELLE_CONFIG keeps the named file's root even when the
	// marker names main.
	_ = config.WriteRepoPathMarker(rt.Dir, invoking)
	named := procRoot
	// A home-keyed runtime dir is the one WriteRepoPathMarker just wrote.
	// Its text is CanonicalRepoRoot, symlink-resolved, which is the process
	// root when SATELLE_CONFIG is unset. Adopting that text keeps the marker
	// and ProcessRoot one string. An explicit SATELLE_CONFIG keeps the named
	// file's root instead, and a legacy runtime dir keeps procRoot.
	if strings.TrimSpace(os.Getenv("SATELLE_CONFIG")) == "" && config.IsRuntimeKeyDir(filepath.Base(rt.Dir)) {
		if marker := config.ReadRepoPathMarker(rt.Dir); marker != "" {
			named = marker
		}
	}
	if named != procRoot {
		processDataDir = config.ResolveProcessDataDir(proc, named)
	}
	// One resolution feeds both the document index and the configuration stamp,
	// so the stamp's paths are relative to the very roots the index scanned.
	authored := config.ResolveAuthoredRoots(proc, named, rt.Dir)
	st.DocIndex.SetAuthoredRoots(authored.Index())
	a := &App{
		Config:         loc,
		ProcessConfig:  proc,
		RepoRoot:       invoking,
		ProcessRoot:    named,
		DataDir:        dataDir,
		ProcessDataDir: processDataDir,
		RuntimeDir:     rt.Dir,
		DBPath:         dbPath,
		Store:          st,
	}
	return a, nil
}

// AuthoredDirs returns the authored roots the directory monitor indexes,
// anchored at the process of record, project tier included. A hand-built App
// with no ProcessRoot keeps the location resolution it was given.
func (a *App) AuthoredDirs() config.AuthoredRoots {
	if a == nil {
		return config.AuthoredRoots{}
	}
	if strings.TrimSpace(a.ProcessRoot) != "" {
		return config.ResolveAuthoredRoots(a.ProcessConfig, a.ProcessRoot, a.RuntimeDir)
	}
	return config.AuthoredRoots{Dirs: a.Config.ResolveAuthoredDirs(a.RepoRoot)}
}

// Close releases the database handle.
func (a *App) Close() error {
	if a.Store != nil {
		return a.Store.Close()
	}
	return nil
}
