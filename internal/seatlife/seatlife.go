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

const Dir = "sessions"

const hookSpawnDepth = 2

type Record struct {
	Session string           `json:"session"`
	Hooks   []procgroup.Proc `json:"hooks,omitempty"`
	Owner   *procgroup.Proc  `json:"owner"`
	Harness string           `json:"harness,omitempty"`
	Model   string           `json:"model,omitempty"`
	At      time.Time        `json:"at"`
}

type Meta struct {
	Harness string
	Model   string
}

func (m Meta) clean() Meta {
	return Meta{Harness: strings.TrimSpace(m.Harness), Model: strings.TrimSpace(m.Model)}
}

func (m Meta) empty() bool { return m.Harness == "" && m.Model == "" }

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

func applyMeta(r *Record, meta Meta, overwrite bool) bool {
	meta = meta.clean()
	changed := false
	if meta.Harness != "" && (overwrite || r.Harness == "") && r.Harness != meta.Harness {
		r.Harness = meta.Harness
		changed = true
	}
	if meta.Model != "" && (overwrite || r.Model == "") && r.Model != meta.Model {
		r.Model = meta.Model
		changed = true
	}
	return changed
}

func Start(home, session string, chain []procgroup.Proc, meta Meta) error {
	r, ok := Load(home, session)
	if !ok {
		r = Record{Session: session}
	}
	if len(chain) > 0 {
		observe(&r, chain)
	}
	applyMeta(&r, meta, true)
	r.At = time.Now().UTC()
	return write(home, r)
}

func Resolve(home, session string, chain []procgroup.Proc, meta Meta) error {
	meta = meta.clean()
	r, ok := Load(home, session)
	if !ok {
		if meta.empty() {
			return nil
		}
		r = Record{Session: session}
	}
	changed := !ok
	if len(chain) > 0 && observe(&r, chain) {
		changed = true
	}
	if applyMeta(&r, meta, true) {
		changed = true
	}
	if !changed {
		return nil
	}
	r.At = time.Now().UTC()
	return write(home, r)
}

func ObserveMeta(home, session string, meta Meta) error {
	meta = meta.clean()
	if meta.empty() {
		return nil
	}
	r, ok := Load(home, session)
	if !ok {
		r = Record{Session: session}
	}
	if !applyMeta(&r, meta, false) {
		return nil
	}
	r.At = time.Now().UTC()
	return write(home, r)
}

func Own(home, session string, p procgroup.Proc) error {
	return write(home, Record{Session: session, Owner: &p, At: time.Now().UTC()})
}

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

const stopSuffix = ".stop"

func stopPath(record string) string {
	return strings.TrimSuffix(record, ".json") + stopSuffix
}

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

type Recorder struct{ Home string }

func (r Recorder) Own(session string) error { return Own(r.Home, session, procgroup.Self()) }

func (r Recorder) Forget(session string) error { return Forget(r.Home, session) }

type Standing string

const (
	Here       Standing = "here"
	Elsewhere  Standing = "elsewhere"
	Gone       Standing = "gone"
	Unverified Standing = "unverified"
)

type Evidence string

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

func (r Record) Gone() bool {
	l, _ := r.liveness()
	return l == procgroup.Gone
}

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

func Classify(here string, seat item.Seat, rec Record, ok bool) (Standing, Evidence) {
	if !strings.HasPrefix(seat.Location, item.LocationLocal) {
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

func StandingOf(home, here string, seat item.Seat) (Standing, Evidence) {
	rec, ok := Load(home, seat.Session)
	return Classify(here, seat, rec, ok)
}

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
