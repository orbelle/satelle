// Package herdr is satelle's host-app adapter for herdr-app (herdr.dev): it
// knows where herdr-app keeps its mods and layouts, and installs and refreshes
// satelle's stories panel there. The panel's files ship inside the binary.
package herdr

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

//go:embed files
var shipped embed.FS

// EnvHome names herdr-app's home when it is not the default.
const EnvHome = "HERDR_APP_HOME"

// RecordName is the file under satelle's home that records what satelle wrote
// into each herdr home. It is satelle's runtime state, never a herdr file.
const RecordName = "herdr-panel.json"

// ErrNoHome is returned by Install when herdr's home does not exist.
var ErrNoHome = errors.New("herdr's home does not exist")

// File is one file of the panel: its slash-separated path under herdr's home
// and the content this binary ships.
type File struct {
	Rel  string
	Body []byte
}

var order = []string{"mods/satelle/main.js", "mods/satelle/mod.json", "layouts/satelle.json", "layouts/satelle.svg"}

// Files returns the panel's files in a fixed order.
func Files() []File {
	out := make([]File, len(order))
	for i, rel := range order {
		b, err := shipped.ReadFile(path.Join("files", rel))
		if err != nil {
			panic(fmt.Sprintf("herdr: embedded %s: %v", rel, err))
		}
		out[i] = File{Rel: rel, Body: b}
	}
	return out
}

// Source says where a resolved home came from.
type Source int

const (
	SourceDefault Source = iota
	SourceEnv
	SourceSetting
)

func (s Source) String() string {
	switch s {
	case SourceEnv:
		return "$" + EnvHome
	case SourceSetting:
		return "the setting herdr.home"
	}
	return "herdr's default"
}

// DefaultHome is herdr-app's documented home for goos: ~/.config/herdr-app on
// Linux, macOS and Windows alike. A pure join, so it answers for any goos.
func DefaultHome(goos, userHome string) string {
	sep := "/"
	if goos == "windows" {
		sep = `\`
	}
	return strings.TrimRight(userHome, `/\`) + sep + ".config" + sep + "herdr-app"
}

// Resolve picks herdr's home: the setting over $HERDR_APP_HOME over the default.
func Resolve(goos, userHome, env, setting string) (string, Source) {
	if s := strings.TrimSpace(setting); s != "" {
		return s, SourceSetting
	}
	if e := strings.TrimSpace(env); e != "" {
		return e, SourceEnv
	}
	return DefaultHome(goos, userHome), SourceDefault
}

// Home resolves herdr's home on this machine.
func Home(setting string) (string, Source) {
	userHome, _ := os.UserHomeDir()
	return Resolve(runtime.GOOS, userHome, os.Getenv(EnvHome), setting)
}

// LocalDefault is herdr's default home on this machine.
func LocalDefault() string {
	userHome, _ := os.UserHomeDir()
	return DefaultHome(runtime.GOOS, userHome)
}

// Kept is a panel file satelle left as it found it.
type Kept struct {
	Path, Reason string
}

// Result says what an install or refresh did, with absolute paths.
type Result struct {
	Written, Current []string
	Kept             []Kept
	NotInstalled     bool
	HomeGone         bool
}

// Reasons a file is kept.
const (
	ReasonChanged = "changed outside satelle"
	ReasonMissing = "removed outside satelle"
)

type record struct {
	Homes map[string]map[string]string `json:"homes"`
}

func recordPath(satelleHome string) string { return filepath.Join(satelleHome, RecordName) }

func loadRecord(satelleHome string) (record, error) {
	rec := record{Homes: map[string]map[string]string{}}
	b, err := os.ReadFile(recordPath(satelleHome))
	if errors.Is(err, fs.ErrNotExist) {
		return rec, nil
	}
	if err != nil {
		return rec, err
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		return rec, fmt.Errorf("read %s: %w", recordPath(satelleHome), err)
	}
	if rec.Homes == nil {
		rec.Homes = map[string]map[string]string{}
	}
	return rec, nil
}

func saveRecord(satelleHome string, rec record) error {
	if err := os.MkdirAll(satelleHome, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(recordPath(satelleHome), append(b, '\n'), 0o644)
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func homeKey(home string) string {
	if abs, err := filepath.Abs(home); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(home)
}

// Install writes the panel into home. A file satelle wrote and nobody changed
// since is replaced; a file changed outside satelle, or a taken name satelle
// never wrote, is kept unless force.
func Install(home, satelleHome string, force bool) (Result, error) {
	if st, err := os.Stat(home); err != nil || !st.IsDir() {
		return Result{}, ErrNoHome
	}
	return apply(home, satelleHome, force, false)
}

// Refresh brings the panel up to date where Install put it, and touches
// nothing in a home satelle never installed into.
func Refresh(home, satelleHome string) (Result, error) {
	rec, err := loadRecord(satelleHome)
	if err != nil {
		return Result{}, err
	}
	if _, ok := rec.Homes[homeKey(home)]; !ok {
		return Result{NotInstalled: true}, nil
	}
	if st, err := os.Stat(home); err != nil || !st.IsDir() {
		return Result{HomeGone: true}, nil
	}
	return apply(home, satelleHome, false, true)
}

func apply(home, satelleHome string, force, refresh bool) (Result, error) {
	rec, err := loadRecord(satelleHome)
	if err != nil {
		return Result{}, err
	}
	key := homeKey(home)
	was := rec.Homes[key]
	now := map[string]string{}
	var res Result
	var errs []error
	for _, f := range Files() {
		p := filepath.Join(key, filepath.FromSlash(f.Rel))
		want := sum(f.Body)
		have, err := os.ReadFile(p)
		switch {
		case err == nil && bytes.Equal(have, f.Body):
			now[f.Rel] = want
			res.Current = append(res.Current, p)
			continue
		case err != nil && !errors.Is(err, fs.ErrNotExist):
			errs = append(errs, err)
			keep(now, was, f.Rel)
			continue
		case err != nil && refresh && !force:
			res.Kept = append(res.Kept, Kept{p, ReasonMissing})
			keep(now, was, f.Rel)
			continue
		case err == nil && !force && sum(have) != was[f.Rel]:
			res.Kept = append(res.Kept, Kept{p, ReasonChanged})
			keep(now, was, f.Rel)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			errs = append(errs, err)
			keep(now, was, f.Rel)
			continue
		}
		if err := os.WriteFile(p, f.Body, 0o644); err != nil {
			errs = append(errs, err)
			keep(now, was, f.Rel)
			continue
		}
		now[f.Rel] = want
		res.Written = append(res.Written, p)
	}
	rec.Homes[key] = now
	if err := saveRecord(satelleHome, rec); err != nil {
		errs = append(errs, fmt.Errorf("record %s: %w", recordPath(satelleHome), err))
	}
	return res, errors.Join(errs...)
}

func keep(now, was map[string]string, rel string) {
	if h, ok := was[rel]; ok {
		now[rel] = h
	}
}
