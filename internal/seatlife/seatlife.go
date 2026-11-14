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
// (another machine, or a remote or cloud location), gone (this machine, its
// settled owner exited) or unverified (no machine or no record, no settled
// owner, or the owner cannot be proven either way). Hook processes that exited
// before an owner was settled never make a session gone. Doctor and satelled both call Standing; neither
// re-derives it.
//
// Beside a session's record, <satelle home>/sessions/<session>.stop marks that
// the session's last stop was blocked (Stop), so the stop straight after it is
// let through; it needs no record and no process chain.
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

// hookSpawnDepth is how far above a hook command its harness can be: claude,
// grok and cursor-agent run a hook through a shell, so the harness is the hook's
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
	// Hooks are the processes within hookSpawnDepth of each hook call since
	// the owner was last alive: the latest call's first, nearest first, then
	// earlier calls' processes, kept only while they have not provably
	// exited. They are the candidates a later call settles the owner from.
	Hooks []procgroup.Proc `json:"hooks,omitempty"`
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

// observe applies one hook call's process chain to r, the single rule for
// every hook call (session start, prompt, seat command). A live owner is kept
// whatever chain holds. Otherwise the owner is cleared (a hook call for the
// session runs now, so an owner that is not alive is not its owner) and
// settled as the first of r's candidates that is also in chain: two calls of
// one harness share it, and nearest-first order picks it over a deeper
// common ancestor. Then chain's processes within hookSpawnDepth lead the
// candidates, followed by each earlier candidate not provably exited. It
// reports whether r changed.
func observe(r *Record, chain []procgroup.Proc) bool {
	if r.Owner != nil && procgroup.Probe(*r.Owner) == procgroup.Alive {
		return false
	}
	changed := false
	prevOwner := r.Owner
	r.Owner = nil
	for _, h := range r.Hooks {
		if contains(chain, h) {
			o := h
			r.Owner = &o
			break
		}
	}
	if (prevOwner == nil) != (r.Owner == nil) || (prevOwner != nil && *prevOwner != *r.Owner) {
		changed = true
	}
	hooks := slices.Clone(chain[:min(len(chain), hookSpawnDepth)])
	for _, h := range r.Hooks {
		if !contains(hooks, h) && procgroup.Probe(h) != procgroup.Gone {
			hooks = append(hooks, h)
		}
	}
	if !slices.Equal(hooks, r.Hooks) {
		changed = true
	}
	r.Hooks = hooks
	if changed {
		r.At = time.Now().UTC()
	}
	return changed
}

// Start records a SessionStart hook call whose process chain is chain. It
// keeps a live owner whether or not it is in the new chain (a dispatched
// run's satelle process, or the same harness re-reading its context through
// another shell), clears one that is not alive (a resumed session runs in a
// new process), and settles a missing owner from the process it shares with
// an earlier call. It always writes.
func Start(home, session string, chain []procgroup.Proc) error {
	r, ok := Load(home, session)
	if !ok {
		r = Record{Session: session}
	}
	observe(&r, chain)
	r.At = time.Now().UTC()
	return write(home, r)
}

// Resolve records a later hook call in the same session (a prompt hook, a
// seat command) whose process chain is chain: it settles a missing owner from
// the process it shares with an earlier call, else adds its chain's
// processes as candidates for the next call. It writes only when the record
// changes, and nothing when the session has no record.
func Resolve(home, session string, chain []procgroup.Proc) error {
	r, ok := Load(home, session)
	if !ok {
		return nil
	}
	if !observe(&r, chain) {
		return nil
	}
	return write(home, r)
}

// Own records p as session's owner (a dispatched run's satelle process).
func Own(home, session string, p procgroup.Proc) error {
	return write(home, Record{Session: session, Owner: &p, At: time.Now().UTC()})
}

// Forget removes session's record and its stop marker.
func Forget(home, session string) error {
	p, err := recordPath(home, session)
	if err != nil {
		return err
	}
	var errs []error
	for _, f := range []string{p, stopPath(p)} {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// stopSuffix names a session's stop marker: <session>.stop beside its record.
const stopSuffix = ".stop"

// stopPath is the stop marker beside the record at p.
func stopPath(record string) string {
	return strings.TrimSuffix(record, ".json") + stopSuffix
}

// Stop answers a turn's end for session: whether to block it. owed is
// whether the session's seated items owe a move. A stop that owes nothing
// clears the marker and passes; the first stop that owes a move sets the
// marker and blocks; the stop straight after a blocked one clears it and
// passes, so a session is never trapped. A session id that cannot name a
// file, or a marker that cannot be written, lets the stop through.
func Stop(home, session string, owed bool) bool {
	p, err := recordPath(home, session)
	if err != nil {
		return false
	}
	marker := stopPath(p)
	_, statErr := os.Stat(marker)
	present := statErr == nil
	if !owed || present {
		if present {
			_ = os.Remove(marker)
		}
		return false
	}
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		return false
	}
	f, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return false
	}
	return f.Close() == nil
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
	// Gone: bound on this machine, and its session's settled owner has
	// exited.
	Gone Standing = "gone"
	// Unverified: neither proven here nor gone.
	Unverified Standing = "unverified"
)

// Evidence says why a seat has its standing, in words a person can check.
type Evidence string

// liveness is the record's session liveness: its settled owner's. With no
// owner it is never gone, whatever its hook processes show: they are a hook's
// shell or a harness not yet settled, and their exit proves nothing about the
// session.
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
	if len(r.Hooks) == 0 {
		return procgroup.Unknown, "its owner is not known"
	}
	var pids []string
	for _, h := range r.Hooks {
		if procgroup.Probe(h) != procgroup.Gone {
			return procgroup.Unknown, Evidence(fmt.Sprintf("its owner is not settled yet; pid %d still runs", h.PID))
		}
		pids = append(pids, fmt.Sprintf("%d", h.PID))
	}
	return procgroup.Unknown, Evidence("its owner was never settled; its hook processes " + strings.Join(pids, ", ") + " have exited")
}

// Gone reports whether r's session has provably ended: its settled owner has
// exited.
func (r Record) Gone() bool {
	l, _ := r.liveness()
	return l == procgroup.Gone
}

// Lapsed reports whether r never settled an owner and none of its hook
// processes may still run (vacuously so when it has none). Its session is
// unverified, not gone: doctor asks about its seats, and an unseated lapsed
// record is pruned.
func (r Record) Lapsed() bool {
	if r.Owner != nil {
		return false
	}
	for _, h := range r.Hooks {
		if procgroup.Probe(h) != procgroup.Gone {
			return false
		}
	}
	return true
}

// Classify is Standing as a pure function of the seat, this machine's id
// (here, "" when this machine has none) and the session's record (ok false
// when there is none). Rules, in order:
//  1. a location other than local: is elsewhere;
//  2. no machine recorded is unverified;
//  3. another machine is elsewhere;
//  4. no session record here is unverified;
//  5. an owner alive is here, exited is gone, unprobeable unverified;
//  6. no owner is unverified, whatever its hook processes show.
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

// Prune removes each record that keep rejects whose session is gone (its
// settled owner exited) or lapsed (no owner, and its hook processes exited),
// with its stop marker, and each stop marker keep rejects that has no record.
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
		if e.IsDir() {
			continue
		}
		if session, ok := strings.CutSuffix(e.Name(), stopSuffix); ok {
			if _, has := Load(home, session); !has && !keep(session) {
				errs = append(errs, Forget(home, session))
			}
			continue
		}
		session, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok {
			continue
		}
		r, ok := Load(home, session)
		if !ok || !(r.Gone() || r.Lapsed()) || keep(session) {
			continue
		}
		errs = append(errs, Forget(home, session))
	}
	return errors.Join(errs...)
}
