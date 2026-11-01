// Redaction at the source (sty_bd8d963c): a secret satelle resolved into a
// performer's environment (agentcli.Request.Redact) is scrubbed from that
// performer's output where the output enters satelle — the bytes a run
// returns, its error, every event and every live Sink byte — so no reader
// downstream (logs, ledger, run-output files, progress) ever holds a raw copy.
// verb.RedactEnv is the one replacement function; this file only applies it to
// each shape output arrives in.
package agentstep

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"reflect"
	"sync"
	"time"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/verb"
)

var timeType = reflect.TypeOf(time.Time{})

// redactEvent returns a copy of ev with every text-bearing field — every
// string, every []string element, every map key and value, through pointers
// and nested structs — scrubbed of set. It walks the struct rather than naming
// fields, so a field added to agentcli.Event later is covered with no edit
// (TestRedactEventCoversEveryField fails on a kind the walk cannot reach).
// ev's maps, slices and pointers are never mutated.
func redactEvent(ev agentcli.Event, set map[string]string) agentcli.Event {
	if len(set) == 0 {
		return ev
	}
	return redactValue(reflect.ValueOf(ev), set).Interface().(agentcli.Event)
}

func redactValue(v reflect.Value, set map[string]string) reflect.Value {
	switch v.Kind() {
	case reflect.String:
		r := reflect.New(v.Type()).Elem()
		r.SetString(verb.RedactEnv(v.String(), set))
		return r
	case reflect.Struct:
		if v.Type() == timeType {
			return v
		}
		r := reflect.New(v.Type()).Elem()
		r.Set(v)
		for i := 0; i < r.NumField(); i++ {
			if f := r.Field(i); f.CanSet() {
				f.Set(redactValue(v.Field(i), set))
			}
		}
		return r
	case reflect.Pointer:
		if v.IsNil() || textFree(v.Type().Elem()) {
			return v
		}
		p := reflect.New(v.Type().Elem())
		p.Elem().Set(redactValue(v.Elem(), set))
		return p
	case reflect.Slice:
		if v.IsNil() || textFree(v.Type().Elem()) {
			return v
		}
		s := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			s.Index(i).Set(redactValue(v.Index(i), set))
		}
		return s
	case reflect.Map:
		if v.IsNil() {
			return v
		}
		m := reflect.MakeMapWithSize(v.Type(), v.Len())
		for it := v.MapRange(); it.Next(); {
			m.SetMapIndex(redactValue(it.Key(), set), redactValue(it.Value(), set))
		}
		return m
	default:
		return v
	}
}

// textFree reports a kind that can hold no text: numbers, bools and time.Time.
func textFree(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		return true
	}
	return t == timeType
}

// redactable reports why t holds text redactValue cannot reach — an
// unexported field, or a kind (interface, func, chan, array…) it does not
// walk — or nil when every byte of text in t is covered.
func redactable(t reflect.Type, path string) error {
	if textFree(t) {
		return nil
	}
	switch t.Kind() {
	case reflect.String:
		return nil
	case reflect.Pointer, reflect.Slice:
		return redactable(t.Elem(), path+"[]")
	case reflect.Map:
		if err := redactable(t.Key(), path+"{key}"); err != nil {
			return err
		}
		return redactable(t.Elem(), path+"{}")
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				return fmt.Errorf("%s.%s: unexported field the redactor cannot set", path, f.Name)
			}
			if err := redactable(f.Type, path+"."+f.Name); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("%s: kind %s is neither text the redactor walks nor text-free", path, t.Kind())
}

// redactWriter scrubs a live Sink of set. It holds bytes until a newline (or
// Close), so a value split across two writes is still caught; a line longer
// than redactWriterMax is flushed early, keeping back only a tail short enough
// to be the start of a value.
type redactWriter struct {
	mu   sync.Mutex
	w    io.Writer
	set  map[string]string
	buf  []byte
	keep int
}

const redactWriterMax = 64 << 10

func newRedactWriter(w io.Writer, set map[string]string) *redactWriter {
	keep := 0
	for _, v := range set {
		if len(v) > keep {
			keep = len(v)
		}
	}
	return &redactWriter{w: w, set: set, keep: keep}
}

func (r *redactWriter) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if nl := bytes.LastIndexByte(r.buf, '\n'); nl >= 0 {
		r.emit(r.buf[:nl+1])
		r.buf = append(r.buf[:0], r.buf[nl+1:]...)
	}
	if len(r.buf) > redactWriterMax {
		// Every whole value in buf is scrubbed; only a tail shorter than the
		// longest value can still be the start of one.
		red := []byte(verb.RedactEnv(string(r.buf), r.set))
		cut := len(red) - (r.keep - 1)
		if cut > 0 {
			_, _ = r.w.Write(red[:cut])
			red = red[cut:]
		}
		r.buf = append(r.buf[:0], red...)
	}
	return len(p), nil
}

func (r *redactWriter) emit(b []byte) {
	_, _ = r.w.Write([]byte(verb.RedactEnv(string(b), r.set)))
}

// Close flushes what is still held. It never closes the underlying writer.
func (r *redactWriter) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.buf) > 0 {
		r.emit(r.buf)
		r.buf = r.buf[:0]
	}
	return nil
}

// redactRequest wraps req's OnEvent and Sink so neither sees a value in
// req.Redact. It is the outermost layer next to the transport: install it after
// every other handler wrap. flush must run once the run has returned.
func redactRequest(req agentcli.Request) (agentcli.Request, func()) {
	set := req.Redact
	if len(set) == 0 {
		return req, func() {}
	}
	if next := req.OnEvent; next != nil {
		req.OnEvent = func(ev agentcli.Event) { next(redactEvent(ev, set)) }
	}
	flush := func() {}
	if req.Sink != nil {
		rw := newRedactWriter(req.Sink, set)
		req.Sink = rw
		flush = func() { _ = rw.Close() }
	}
	return req, flush
}

// redactedError is err with its message scrubbed. Unwrap keeps errors.Is and
// errors.As (a deadline, a *StallError) working through it.
type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }

// redactErr returns err unchanged unless its message carries a value in set.
func redactErr(err error, set map[string]string) error {
	if err == nil || len(set) == 0 {
		return err
	}
	msg := err.Error()
	if red := verb.RedactEnv(msg, set); red != msg {
		return &redactedError{msg: red, err: err}
	}
	return err
}

// redactBytes scrubs a run's returned output of set.
func redactBytes(b []byte, set map[string]string) []byte {
	if len(b) == 0 || len(set) == 0 {
		return b
	}
	return []byte(verb.RedactEnv(string(b), set))
}

// redactedSession scrubs a live session's channel and capture of set, the two
// ways its output leaves the transport other than OnEvent and Sink (which
// redactRequest wraps before the session opens).
type redactedSession struct {
	agentcli.Session
	set    map[string]string
	once   sync.Once
	events chan agentcli.Event
}

func (s *redactedSession) Events() <-chan agentcli.Event {
	s.once.Do(func() {
		src := s.Session.Events()
		if src == nil {
			return
		}
		s.events = make(chan agentcli.Event, cap(src))
		go func() {
			defer close(s.events)
			for ev := range src {
				s.events <- redactEvent(ev, s.set)
			}
		}()
	})
	return s.events
}

func (s *redactedSession) Captured() []byte {
	return redactBytes(s.Session.Captured(), s.set)
}

func (s *redactedSession) Send(ctx context.Context, turn agentcli.Turn) error {
	return redactErr(s.Session.Send(ctx, turn), s.set)
}

func (s *redactedSession) Cancel() error { return redactErr(s.Session.Cancel(), s.set) }
func (s *redactedSession) Close() error  { return redactErr(s.Session.Close(), s.set) }

// redactSession wraps sess when set is non-empty.
func redactSession(sess agentcli.Session, set map[string]string) agentcli.Session {
	if len(set) == 0 || sess == nil {
		return sess
	}
	return &redactedSession{Session: sess, set: set}
}
