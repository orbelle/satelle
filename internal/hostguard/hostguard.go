// Package hostguard is the one owner of what the test guards watch on the
// operator's real machine (sty_1b739a74): where the host roots are, how the top
// level of the real ~/.satelle is captured and diffed, which names a live
// satelled legitimately churns, and how the installed binaries are fingerprinted.
//
// Two guards consume it the way scripts/credfp consumes internal/hosted:
// scripts/credguard.sh (via scripts/credfp) wraps `make test` and `make
// integration`, and the integration suite's TestMain host-surface guard. Because
// both call this package they cannot disagree on a before/after pair.
//
// The package is pure: paths in, values out. It never mutates the host and never
// reads the process's isolation env (see ResolveRoots).
package hostguard

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// HostHomeEnv names the variable a test wrapper exports to say where the real
// operator home is when it has replaced HOME for the suite (sty_ec30f859).
const HostHomeEnv = "SATELLE_TEST_HOST_HOME"

// BinNames are the installed binaries under Roots.BinDir the guards fingerprint.
var BinNames = []string{"satelle", "satelled"}

// Roots are the real host paths the guards watch.
type Roots struct {
	Home        string // the operator's home directory
	SatelleHome string // <Home>/.satelle
	BinDir      string // <Home>/.local/bin
}

// ResolveRoots resolves the host roots from HostHomeEnv when it is set, otherwise
// from os.UserHomeDir. SATELLE_HOME and XDG_* are deliberately ignored: inside a
// suite they point at a sandbox, and a guard watching a sandbox proves nothing
// about the real host. This is the single root-resolution rule; sty_ec30f859's
// wrapper exports HostHomeEnv so the guards keep watching the real host after it
// replaces HOME. An unresolvable home yields empty roots, which capture as empty.
func ResolveRoots() Roots {
	home := strings.TrimSpace(os.Getenv(HostHomeEnv))
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if home == "" {
		return Roots{}
	}
	return Roots{
		Home:        home,
		SatelleHome: filepath.Join(home, ".satelle"),
		BinDir:      filepath.Join(home, ".local", "bin"),
	}
}

// runtimeKey matches a RepoKey-shaped directory name (<basename>-<8hex>, e.g.
// satelle-16882c39), without importing internal/config.
var runtimeKey = regexp.MustCompile(`^[^/]+-[0-9a-f]{8}$`)

// IsRuntimeKey reports whether name is a home-keyed project runtime directory
// name. A live satelle service legitimately mutates the contents of these.
func IsRuntimeKey(name string) bool { return runtimeKey.MatchString(name) }

// Ignored reports whether a top-level name under ~/.satelle is outside the
// guard: a live satelle process outside the suite rewrites it on its own, so
// fingerprinting it would fail every clean run on a machine in use.
//
// A name joins this list only with evidence that a live satelle process writes
// it on its own, recorded here beside the list. Never add a suffix or mtime rule.
//
//   - document-sync-state.json: the live document-sync cursor.
//   - serve: the push-fed serve plane (mirror.db and its WAL, server.log)
//     (sty_80233c10).
//   - run: the cross-process /healthz cache (cli.healthzCachePath, with its
//     .healthz-* temp sibling) that every host satelle CLI rewrites within its
//     TTL (sty_a003a459). run/ holds only that cache; durable state stored
//     there would go unguarded, so revisit this entry before adding any.
func Ignored(name string) bool {
	return name == "document-sync-state.json" || name == "serve" || name == "run"
}

// FingerprintFile returns "sha256:<hex>:<size>" for the file's content, or ""
// when it is absent, a directory or unreadable. The mtime is deliberately not
// part of it: a no-op touch is not a change.
func FingerprintFile(path string) string {
	fp, _ := fingerprint(path)
	return fp
}

// fingerprint is FingerprintFile with the error kept so Capture can tell a file
// that vanished (skip) from one it cannot read (fail closed).
func fingerprint(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	if fi.IsDir() {
		return "", nil
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256:%s:%d", hex.EncodeToString(h.Sum(nil)), n), nil
}

// Entry kinds recorded in Snapshot.Entries.
const (
	kindDir   = "dir"
	kindFile  = "file"
	kindOther = "other"
)

// Snapshot is the host surface at one moment.
type Snapshot struct {
	// Entries maps each top-level name under ~/.satelle (Ignored names removed)
	// to its kind: "dir", "file" or "other".
	Entries map[string]string
	// Files maps each top-level regular file to its content fingerprint.
	Files map[string]string
	// Bins maps each of BinNames to its fingerprint; "" when absent.
	Bins map[string]string
}

// KeyDirs returns the runtime key directories present at capture time.
func (s Snapshot) KeyDirs() map[string]struct{} {
	out := map[string]struct{}{}
	for name, kind := range s.Entries {
		if kind == kindDir && IsRuntimeKey(name) {
			out[name] = struct{}{}
		}
	}
	return out
}

// Capture snapshots the host surface. A missing root captures as empty (fresh CI
// with no install); an unreadable root, or a top-level file that exists but
// cannot be read, is an error so the caller fails closed. It does not descend
// into directories: serve/ and the key directories are recorded as entries only.
func Capture(r Roots) (Snapshot, error) {
	s := Snapshot{
		Entries: map[string]string{},
		Files:   map[string]string{},
		Bins:    map[string]string{},
	}
	if r.SatelleHome != "" {
		ents, err := os.ReadDir(r.SatelleHome)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return Snapshot{}, fmt.Errorf("hostguard: read %s: %w", r.SatelleHome, err)
		}
		for _, e := range ents {
			name := e.Name()
			if Ignored(name) {
				continue
			}
			switch {
			case e.IsDir():
				s.Entries[name] = kindDir
			case e.Type().IsRegular():
				fp, err := fingerprint(filepath.Join(r.SatelleHome, name))
				if err != nil {
					return Snapshot{}, fmt.Errorf("hostguard: fingerprint %s: %w", name, err)
				}
				s.Entries[name] = kindFile
				s.Files[name] = fp
			default:
				s.Entries[name] = kindOther
			}
		}
	}
	for _, name := range BinNames {
		fp := ""
		if r.BinDir != "" {
			var err error
			if fp, err = fingerprint(filepath.Join(r.BinDir, name)); err != nil {
				return Snapshot{}, fmt.Errorf("hostguard: fingerprint %s: %w", name, err)
			}
		}
		s.Bins[name] = fp
	}
	return s, nil
}

// Diff returns one line per change between two snapshots, sorted; empty when
// identical. A line names the entry, file or binary that changed.
func Diff(before, after Snapshot) []string {
	var out []string
	names := map[string]struct{}{}
	for n := range before.Entries {
		names[n] = struct{}{}
	}
	for n := range after.Entries {
		names[n] = struct{}{}
	}
	for _, n := range sortedKeys(names) {
		b, bok := before.Entries[n]
		a, aok := after.Entries[n]
		switch {
		case bok && !aok:
			out = append(out, "removed entry "+n)
		case !bok && aok:
			out = append(out, "added entry "+n)
		case b != a:
			out = append(out, "changed entry "+n+" ("+b+" -> "+a+")")
		case a == kindFile && before.Files[n] != after.Files[n]:
			out = append(out, "changed file "+n)
		}
	}
	return append(out, DiffBins(before.Bins, after.Bins)...)
}

// DiffBins returns one line per installed binary that appeared, was removed or
// changed content. It is split out so a guard that walks the trees itself (the
// integration suite's hashTree) can use the binary rule without the top-level one.
func DiffBins(before, after map[string]string) []string {
	var out []string
	names := map[string]struct{}{}
	for n := range before {
		names[n] = struct{}{}
	}
	for n := range after {
		names[n] = struct{}{}
	}
	for _, n := range sortedKeys(names) {
		b, a := before[n], after[n]
		switch {
		case b == a:
		case b == "":
			out = append(out, "appeared bin "+n)
		case a == "":
			out = append(out, "removed bin "+n)
		default:
			out = append(out, "changed bin "+n)
		}
	}
	return out
}

func sortedKeys(m map[string]struct{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Marshal renders s as one line per record — `entry "<name>" "<kind>"`, `file
// "<name>" "<fingerprint>"`, `bin "<name>" "<fingerprint>"` — so a shell guard
// can keep a snapshot in a temp file. Output is sorted and deterministic.
func (s Snapshot) Marshal() []byte {
	var b strings.Builder
	write := func(kind string, m map[string]string) {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "%s %s %s\n", kind, strconv.Quote(k), strconv.Quote(m[k]))
		}
	}
	write("entry", s.Entries)
	write("file", s.Files)
	write("bin", s.Bins)
	return []byte(b.String())
}

// Parse reads what Marshal wrote.
func Parse(text []byte) (Snapshot, error) {
	s := Snapshot{
		Entries: map[string]string{},
		Files:   map[string]string{},
		Bins:    map[string]string{},
	}
	for i, line := range strings.Split(string(text), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		kind, rest, _ := strings.Cut(line, " ")
		name, rest, err := unquotePrefix(rest)
		if err != nil {
			return Snapshot{}, fmt.Errorf("hostguard: line %d: %w", i+1, err)
		}
		val, rest, err := unquotePrefix(strings.TrimPrefix(rest, " "))
		if err != nil || rest != "" {
			return Snapshot{}, fmt.Errorf("hostguard: line %d: malformed record", i+1)
		}
		switch kind {
		case "entry":
			s.Entries[name] = val
		case "file":
			s.Files[name] = val
		case "bin":
			s.Bins[name] = val
		default:
			return Snapshot{}, fmt.Errorf("hostguard: line %d: unknown record %q", i+1, kind)
		}
	}
	return s, nil
}

// unquotePrefix reads one Go-quoted string off the front of s.
func unquotePrefix(s string) (value, rest string, err error) {
	q, err := strconv.QuotedPrefix(s)
	if err != nil {
		return "", "", err
	}
	value, err = strconv.Unquote(q)
	return value, s[len(q):], err
}
