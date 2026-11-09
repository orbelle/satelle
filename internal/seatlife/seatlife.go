// Package seatlife knows whether a seat's session still lives, and where.
//
// A seat records the machine it was bound on (item.Seat.Machine). Whether its
// session still runs is known only on that machine, from a session record at
// <satelle home>/sessions/<session>.json naming the session's owner process:
// the harness (claude, pi, grok, cursor) found from its SessionStart and
// UserPromptSubmit hooks or its seat command, or the satelle process of a
// dispatched run. Records name local pids, so they are never replicated.
//
// Standing classes a seat as here (this machine, owner alive), elsewhere
// (another machine, or a remote or cloud location), gone (this machine, owner
// exited) or unverified (no machine or no record, or the owner cannot be
// proven either way). Doctor and satelled both call Standing; neither
// re-derives it.
package seatlife

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/procgroup"
)

// Dir is the directory under the satelle home holding session records.
const Dir = "sessions"

// hookSpawnDepth is how far above a hook command its harness can be: claude
// and grok run a hook through a shell, so the harness is the hook's
// grandparent; pi spawns satelle directly, so its parent. Two hook calls of one
// harness share it; their shells differ. A deeper common ancestor (a terminal,
// a multiplexer) is never taken as the owner.
const hookSpawnDepth = 2

// locationLocal is the prefix of a seat at a working tree on the machine that
// bound it (kernel.LocationLocal; kernel imports this package's caller, not
// this package).
const locationLocal = "local:"

// Record is one session's record on this machine.
type Record struct {
	Session string `json:"session"`
	// Ancestors is the SessionStart hook's process chain, nearest first.
	Ancestors []procgroup.Proc `json:"ancestors,omitempty"`
	// Owner is the process the session runs in, once known.
	Owner *procgroup.Proc `json:"owner"`
	At    time.Time       `json:"at"`
}

var validSession = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func recordPath(home, session string) (string, error) {
	if strings.TrimSpace(home) == "" {
		return "", errors.New("no satelle home")
	}
	if !validSession.MatchString(session) || session == "." || session == ".." {
		return "", fmt.Errorf("session %q cannot name a record", session)
	}
	return filepath.Join(home, Dir, session+".json"), nil
}

// Load reads session's record; false when there is none.
func Load(home, session string) (Record, bool) {
	p, err := recordPath(home, session)
	if err != nil {
		return Record{}, false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return Record{}, false
	}
	var r Record
	if json.Unmarshal(b, &r) != nil || r.Session != session {
		return Record{}, false
	}
	return r, true
}

// write stores r atomically: a temp file renamed over the record.
func write(home string, r Record) error {
	p, err := recordPath(home, r.Session)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), r.Session+".*.tmp")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(b)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), p)
	}
	if werr != nil {
		_ = os.Remove(tmp.Name())
	}
	return werr
}

func contains(chain []procgroup.Proc, p procgroup.Proc) bool {
	return slices.Contains(chain, p)
}

// Start records session's SessionStart chain. A previous owner is kept only
// when it still runs and appears in the new chain (a dispatched run's satelle
// process, or the same harness after a clear); otherwise it is cleared, since
// a resumed session runs in a new process.
func Start(home, session string, ancestors []procgroup.Proc) error {
	r := Record{Session: session, Ancestors: ancestors, At: time.Now().UTC()}
	if prev, ok := Load(home, session); ok && prev.Owner != nil &&
		procgroup.Probe(*prev.Owner) == procgroup.Alive && contains(ancestors, *prev.Owner) {
		r.Owner = prev.Owner
	}
	return write(home, r)
}

// Resolve settles session's owner from a later call in the same session (a
// prompt hook, a seat command) whose process chain is ancestors: when the
// record has no live owner, the owner becomes the first of the record's first
// hookSpawnDepth ancestors that is also in ancestors. It writes only when the
// owner changes, and nothing when the session has no record.
func Resolve(home, session string, ancestors []procgroup.Proc) error {
	r, ok := Load(home, session)
	if !ok {
		return nil
	}
	if r.Owner != nil && procgroup.Probe(*r.Owner) == procgroup.Alive {
		return nil
	}
	for i, a := range r.Ancestors {
		if i >= hookSpawnDepth {
			break
		}
		if contains(ancestors, a) {
			if r.Owner != nil && *r.Owner == a {
				return nil
			}
			owner := a
			r.Owner, r.At = &owner, time.Now().UTC()
			return write(home, r)
		}
	}
	return nil
}

// Own records p as session's owner (a dispatched run's satelle process).
func Own(home, session string, p procgroup.Proc) error {
	return write(home, Record{Session: session, Owner: &p, At: time.Now().UTC()})
}

// Forget removes session's record.
func Forget(home, session string) error {
	p, err := recordPath(home, session)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Recorder is kernel.SessionOwner over the records in Home: the calling
// process owns the session.
type Recorder struct{ Home string }

// Own records the calling process as session's owner.
func (r Recorder) Own(session string) error { return Own(r.Home, session, procgroup.Self()) }

// Forget removes session's record.
func (r Recorder) Forget(session string) error { return Forget(r.Home, session) }

// Standing is where a seat's session stands, seen from this machine.
type Standing string

const (
	// Here: bound on this machine, and its session's owner runs.
	Here Standing = "here"
	// Elsewhere: bound on another machine, or at a remote or cloud location.
	Elsewhere Standing = "elsewhere"
	// Gone: bound on this machine, and its session's owner has exited.
	Gone Standing = "gone"
	// Unverified: neither proven here nor gone.
	Unverified Standing = "unverified"
)

// Evidence says why a seat has its standing, in words a person can check.
type Evidence string

// liveness is the record's session liveness: its owner's, else whether any
// hook-depth ancestor still runs.
func (r Record) liveness() (procgroup.Liveness, Evidence) {
	if r.Owner != nil {
		o := *r.Owner
		switch procgroup.Probe(o) {
		case procgroup.Alive:
			return procgroup.Alive, Evidence(fmt.Sprintf("pid %d", o.PID))
		case procgroup.Gone:
			return procgroup.Gone, Evidence(fmt.Sprintf("pid %d started %s has exited", o.PID, o.Start))
		default:
			return procgroup.Unknown, Evidence(fmt.Sprintf("pid %d cannot be probed", o.PID))
		}
	}
	if len(r.Ancestors) == 0 {
		return procgroup.Unknown, "its owner is not known"
	}
	var pids []string
	for i, a := range r.Ancestors {
		if i >= hookSpawnDepth {
			break
		}
		if procgroup.Probe(a) != procgroup.Gone {
			return procgroup.Unknown, Evidence(fmt.Sprintf("its owner is not resolved yet; pid %d still runs", a.PID))
		}
		pids = append(pids, fmt.Sprintf("%d", a.PID))
	}
	return procgroup.Gone, Evidence("its session start's processes " + strings.Join(pids, ", ") + " have exited")
}

// Gone reports whether r's session has provably ended.
func (r Record) Gone() bool {
	l, _ := r.liveness()
	return l == procgroup.Gone
}

// Classify is Standing as a pure function of the seat, this machine's id
// (here, "" when this machine has none) and the session's record (ok false
// when there is none). Rules, in order:
//  1. a location other than local: is elsewhere;
//  2. no machine recorded is unverified;
//  3. another machine is elsewhere;
//  4. no session record here is unverified;
//  5. an owner alive is here, exited is gone, unprobeable unverified;
//  6. no owner: a hook-depth ancestor still running is unverified; all of
//     them exited is gone.
func Classify(here string, seat item.Seat, rec Record, ok bool) (Standing, Evidence) {
	if !strings.HasPrefix(seat.Location, locationLocal) {
		return Elsewhere, Evidence("at " + seat.Location)
	}
	if seat.Machine == "" {
		return Unverified, "no machine recorded"
	}
	if seat.Machine != here {
		host := seat.Host
		if host == "" {
			host = "an unnamed host"
		}
		return Elsewhere, Evidence(fmt.Sprintf("on %s (%s)", host, seat.Machine))
	}
	if !ok {
		return Unverified, "no session record on this machine"
	}
	switch l, ev := rec.liveness(); l {
	case procgroup.Alive:
		return Here, ev
	case procgroup.Gone:
		return Gone, ev
	default:
		return Unverified, ev
	}
}

// StandingOf classes seat from this machine (here is its id, "" when it has
// none) by the session records in home. It never writes.
func StandingOf(home, here string, seat item.Seat) (Standing, Evidence) {
	rec, ok := Load(home, seat.Session)
	return Classify(here, seat, rec, ok)
}

// Prune removes each record whose session is gone and that keep rejects.
func Prune(home string, keep func(session string) bool) error {
	entries, err := os.ReadDir(filepath.Join(home, Dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		session, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || e.IsDir() {
			continue
		}
		r, ok := Load(home, session)
		if !ok || !r.Gone() || keep(session) {
			continue
		}
		errs = append(errs, Forget(home, session))
	}
	return errors.Join(errs...)
}
