package hosted

// Checkout location identity (epic:story-checkout, sty_e88d77ce).
//
// A location is one machine × repo-root checkout. The id is derived, persisted
// per-user outside any repo (beside credentials.toml), registered once per
// hosted server, and sent as x-satelle-location on authenticated sync/story
// calls. Unbound repos never touch this file.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/gitfacts"
)

// LocationHeader is the REST header / gRPC metadata key a bound checkout sends.
// Same string on both transports (satelle-server LocationHeader).
const LocationHeader = "x-satelle-location"

// LocationStatePathOverride, when non-empty, replaces LocationStatePath (tests).
var LocationStatePathOverride string

var locationMu sync.Mutex

type locationEntry struct {
	ID         string          `json:"id"`
	Registered map[string]bool `json:"registered,omitempty"`
	// Labels is the label last sent for each server in Registered, so a location
	// registered under an older label is registered again when the label changes.
	Labels map[string]string `json:"labels,omitempty"`
}

type locationStateFile struct {
	Locations       map[string]locationEntry `json:"locations"`
	MachineFallback string                   `json:"machine_fallback,omitempty"`
}

// LocationStatePath is the per-user JSON file holding checkout ids.
// Same directory as credentials.toml; file location-state.json.
func LocationStatePath() (string, error) {
	if p := strings.TrimSpace(LocationStatePathOverride); p != "" {
		return p, nil
	}
	cred, err := CredentialsPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(cred), "location-state.json"), nil
}

// ValidLocationID reports whether id meets the server contract: 8–128 chars of
// [A-Za-z0-9._:-] after trim.
func ValidLocationID(id string) bool {
	id = strings.TrimSpace(id)
	if len(id) < 8 || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') ||
			r == '.' || r == '_' || r == ':' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func locationRepoKey(repoRoot string) string {
	abs, err := filepath.Abs(repoRoot)
	if err != nil {
		abs = repoRoot
	}
	return filepath.Clean(abs)
}

func deriveLocationID(machine, repoRoot string) string {
	sum := sha256.Sum256([]byte(machine + "\x00" + locationRepoKey(repoRoot)))
	return "loc_" + hex.EncodeToString(sum[:])[:32]
}

func readMachineID() string {
	for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if s := strings.TrimSpace(string(b)); s != "" {
			return s
		}
	}
	if h, err := os.Hostname(); err == nil {
		if s := strings.TrimSpace(h); s != "" {
			return s
		}
	}
	return ""
}

func randomFallbackID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("hosted: location fallback: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func loadLocationState() (locationStateFile, error) {
	path, err := LocationStatePath()
	if err != nil {
		return locationStateFile{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return locationStateFile{Locations: map[string]locationEntry{}}, nil
		}
		return locationStateFile{}, fmt.Errorf("hosted: read location state: %w", err)
	}
	var state locationStateFile
	if err := json.Unmarshal(raw, &state); err != nil {
		return locationStateFile{}, fmt.Errorf("hosted: decode location state: %w", err)
	}
	if state.Locations == nil {
		state.Locations = map[string]locationEntry{}
	}
	return state, nil
}

func writeLocationState(state locationStateFile) error {
	path, err := LocationStatePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("hosted: create location state dir: %w", err)
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("hosted: encode location state: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("hosted: write location state: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("hosted: rename location state: %w", err)
	}
	return nil
}

func machineIDLocked(state *locationStateFile) (string, error) {
	if m := readMachineID(); m != "" {
		return m, nil
	}
	if s := strings.TrimSpace(state.MachineFallback); s != "" {
		return s, nil
	}
	fb, err := randomFallbackID()
	if err != nil {
		return "", err
	}
	state.MachineFallback = fb
	return fb, nil
}

// LocationID returns the persisted checkout id for repoRoot, deriving and
// writing one if missing. A persisted id that fails ValidLocationID is
// re-derived. Never returns an empty id on success.
func LocationID(repoRoot string) (string, error) {
	locationMu.Lock()
	defer locationMu.Unlock()
	state, err := loadLocationState()
	if err != nil {
		return "", err
	}
	key := locationRepoKey(repoRoot)
	if e, ok := state.Locations[key]; ok && ValidLocationID(e.ID) {
		return e.ID, nil
	}
	machine, err := machineIDLocked(&state)
	if err != nil {
		return "", err
	}
	id := deriveLocationID(machine, repoRoot)
	if !ValidLocationID(id) {
		return "", fmt.Errorf("hosted: derived location id %q fails the server contract", id)
	}
	// A replacement id (missing or invalid persisted) is unregistered.
	state.Locations[key] = locationEntry{ID: id, Registered: map[string]bool{}}
	if err := writeLocationState(state); err != nil {
		return "", err
	}
	return id, nil
}

// KnownLocationID is LocationID without the side effects: the persisted id for
// repoRoot, or "" when none is stored or it cannot be read. It never derives or
// writes one, so a session-start read leaves no state behind.
func KnownLocationID(repoRoot string) string {
	locationMu.Lock()
	defer locationMu.Unlock()
	state, err := loadLocationState()
	if err != nil {
		return ""
	}
	if e, ok := state.Locations[locationRepoKey(repoRoot)]; ok && ValidLocationID(e.ID) {
		return e.ID
	}
	return ""
}

// LocationRegisteredAs reports whether repoRoot's id has been registered on
// server under exactly label. A location registered before labels were stored
// has none recorded, so it reads as not registered under any label.
func LocationRegisteredAs(repoRoot, server, label string) bool {
	locationMu.Lock()
	defer locationMu.Unlock()
	state, err := loadLocationState()
	if err != nil {
		return false
	}
	e, ok := state.Locations[locationRepoKey(repoRoot)]
	if !ok {
		return false
	}
	srv := normalizeServerURL(server)
	return e.Registered[srv] && e.Labels[srv] == label
}

// MarkLocationRegistered records a successful POST /api/v1/locations for this
// repoRoot × server pair, with the label that was sent.
func MarkLocationRegistered(repoRoot, server, label string) error {
	locationMu.Lock()
	defer locationMu.Unlock()
	state, err := loadLocationState()
	if err != nil {
		return err
	}
	key := locationRepoKey(repoRoot)
	e := state.Locations[key]
	if e.Registered == nil {
		e.Registered = map[string]bool{}
	}
	if e.Labels == nil {
		e.Labels = map[string]string{}
	}
	srv := normalizeServerURL(server)
	e.Registered[srv] = true
	e.Labels[srv] = label
	state.Locations[key] = e
	return writeLocationState(state)
}

// ShortHost is the machine's hostname up to its first dot ("unknown" when it has
// none): the one host spelling shared by the location label and the session name.
func ShortHost() string {
	host, err := os.Hostname()
	host = strings.TrimSpace(host)
	if err != nil || host == "" {
		return "unknown"
	}
	if i := strings.IndexByte(host, '.'); i > 0 {
		host = host[:i]
	}
	return host
}

// nameSeparator joins the segments of a session name.
const nameSeparator = " · "

// SessionName is the one identity an agent session goes by:
// <short host> · <repo name>[ · <worktree dir>][ · <story id>]. The repo name is
// config.RepoName (the same for a worktree and its main tree); the worktree
// segment is present only for a linked worktree; the story segment is present
// only when engagedID is non-empty and differs from the worktree segment (a
// worktree directory named after its story carries the id once).
func SessionName(repoRoot, engagedID string) string {
	parts := []string{ShortHost(), config.RepoName(repoRoot)}
	wt := ""
	if (gitfacts.Repo{Dir: repoRoot}).IsLinked() {
		wt = filepath.Base(locationRepoKey(repoRoot))
		parts = append(parts, wt)
	}
	if id := strings.TrimSpace(engagedID); id != "" && id != wt {
		parts = append(parts, id)
	}
	return strings.Join(parts, nameSeparator)
}

// LocationLabel is the human-readable register payload: the session name without
// a story (a location is a checkout, not a session).
func LocationLabel(repoRoot string) string {
	return SessionName(repoRoot, "")
}
