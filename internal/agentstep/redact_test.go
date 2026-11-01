package agentstep

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/bobmcallan/satelle/internal/agentcli"
)

var redactSet = map[string]string{secretRefText: secretValue}

// fillText sets every text-bearing leaf of v (a settable value) to s, through
// pointers, slices, maps and nested structs — the same shapes redactValue
// walks, built independently so the test does not trust the walker.
func fillText(v reflect.Value, s string) {
	switch v.Kind() {
	case reflect.String:
		v.SetString(s)
	case reflect.Struct:
		if v.Type() == timeType {
			return
		}
		for i := 0; i < v.NumField(); i++ {
			fillText(v.Field(i), s)
		}
	case reflect.Pointer:
		if textFree(v.Type().Elem()) {
			return
		}
		p := reflect.New(v.Type().Elem())
		fillText(p.Elem(), s)
		v.Set(p)
	case reflect.Slice:
		if textFree(v.Type().Elem()) {
			return
		}
		sl := reflect.MakeSlice(v.Type(), 2, 2)
		for i := 0; i < 2; i++ {
			fillText(sl.Index(i), s)
		}
		v.Set(sl)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		k := reflect.New(v.Type().Key()).Elem()
		e := reflect.New(v.Type().Elem()).Elem()
		fillText(k, s)
		fillText(e, s)
		m.SetMapIndex(k, e)
		v.Set(m)
	}
}

// AC2: every text-bearing field of agentcli.Event — today Text, Tool, Status,
// Error, Command, Model, Meta, Tools and the Usage struct's strings — is
// redacted, and a field of a kind the walker cannot reach fails here, so a
// field added later cannot ship uncovered.
func TestRedactEventCoversEveryField(t *testing.T) {
	typ := reflect.TypeOf(agentcli.Event{})
	if err := redactable(typ, "Event"); err != nil {
		t.Fatalf("agentcli.Event has text the redactor cannot reach: %v", err)
	}
	var ev agentcli.Event
	fillText(reflect.ValueOf(&ev).Elem(), "pre "+secretValue+" post")
	orig := fmt.Sprintf("%+v %+v", ev, *ev.Usage)
	if !strings.Contains(orig, secretValue) || ev.Command == "" || len(ev.Tools) == 0 || len(ev.Meta) == 0 {
		t.Fatalf("fixture did not fill the event: %s", orig)
	}
	got := redactEvent(ev, redactSet)
	if dump := fmt.Sprintf("%+v %+v %v", got, *got.Usage, got.Usage.Models); strings.Contains(dump, secretValue) {
		t.Errorf("redacted event still holds the value:\n%s", dump)
	}
	if got.Command != "pre "+secretMarker+" post" || got.Tools[0] != got.Command {
		t.Errorf("Command/Tools = %q / %q", got.Command, got.Tools)
	}
	// The caller's event is never mutated through a shared map/slice/pointer.
	if now := fmt.Sprintf("%+v %+v", ev, *ev.Usage); now != orig {
		t.Errorf("redactEvent mutated its input:\n%s\nwas\n%s", now, orig)
	}
	// The coverage check itself rejects what the walker cannot reach.
	type hidden struct{ secret string }
	type opaque struct{ V any }
	for _, bad := range []reflect.Type{reflect.TypeOf(hidden{}), reflect.TypeOf(opaque{}), reflect.TypeOf([2]string{})} {
		if redactable(bad, "x") == nil {
			t.Errorf("redactable accepted %v", bad)
		}
	}
}

// secretRunner is a transport whose child echoes the value everywhere: split
// across two Sink writes, in an event, in its returned bytes and in an error
// marked as a deadline.
type secretRunner struct{ fail bool }

func (secretRunner) Name() string    { return "secret" }
func (secretRunner) Command() string { return "secret -p {system}" }
func (r secretRunner) Run(_ context.Context, req agentcli.Request) ([]byte, error) {
	if req.Sink != nil {
		half := len(secretValue) / 2
		_, _ = req.Sink.Write([]byte("sink " + secretValue[:half]))
		_, _ = req.Sink.Write([]byte(secretValue[half:] + " end\nno newline " + secretValue))
	}
	if req.OnEvent != nil {
		req.OnEvent(agentcli.Event{Kind: agentcli.EventToolStart, Tool: "Bash", Command: "echo " + secretValue,
			Error: "e " + secretValue, Meta: map[string]string{"k": secretValue}})
	}
	var err error
	if r.fail {
		err = agentcli.Mark(errors.New("child said "+secretValue), context.DeadlineExceeded)
	}
	return []byte("answer " + secretValue), err
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func runSecret(t *testing.T, set map[string]string, fail bool) (out []byte, err error, sink string, events []agentcli.Event) {
	t.Helper()
	g := New(nil, fakeDocs{}, t.TempDir(), "")
	var buf syncBuf
	var mu sync.Mutex
	req := agentcli.Request{Redact: set, Sink: &buf, OnEvent: func(ev agentcli.Event) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	}}
	out, _, err = g.runOnceBusy(context.Background(), secretRunner{fail: fail}, req, 0, 1<<40, 0)
	return out, err, buf.b.String(), events
}

// AC2 at the source: runOnceBusy never returns or forwards the value — its
// bytes, its error, every event and every Sink byte (even a value split across
// writes) are scrubbed, and a marked deadline still reads as one.
func TestRunOnceBusyRedactsAtTheSource(t *testing.T) {
	out, err, sink, events := runSecret(t, redactSet, true)
	if string(out) != "answer "+secretMarker {
		t.Errorf("out = %q", out)
	}
	if err == nil || strings.Contains(err.Error(), secretValue) || !strings.Contains(err.Error(), secretMarker) {
		t.Errorf("err = %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("errors.Is(deadline) lost through the redacted error: %v", err)
	}
	if want := "sink " + secretMarker + " end\nno newline " + secretMarker; sink != want {
		t.Errorf("sink = %q, want %q", sink, want)
	}
	if len(events) != 1 || strings.Contains(fmt.Sprintf("%+v", events), secretValue) {
		t.Errorf("events = %+v", events)
	}

	// Negative control: with no resolved set nothing is scrubbed, so the
	// assertions above are sensitive to the redactor.
	out, err, sink, events = runSecret(t, nil, true)
	if !strings.Contains(string(out), secretValue) || !strings.Contains(err.Error(), secretValue) ||
		!strings.Contains(sink, secretValue) || !strings.Contains(events[0].Command, secretValue) {
		t.Errorf("negative control: an empty set must change nothing: %q %v %q %+v", out, err, sink, events)
	}
}

// A line longer than the writer's hold is flushed early, never holding back
// more than a value's length and never cutting a value in two.
func TestRedactWriterFlushesLongLines(t *testing.T) {
	var buf bytes.Buffer
	w := newRedactWriter(&buf, redactSet)
	long := strings.Repeat("x", redactWriterMax) + secretValue[:5]
	_, _ = w.Write([]byte(long))
	if buf.Len() == 0 {
		t.Fatal("an over-long line was not flushed early")
	}
	_, _ = w.Write([]byte(secretValue[5:] + strings.Repeat("y", 10)))
	_ = w.Close()
	if s := buf.String(); strings.Contains(s, secretValue) || !strings.Contains(s, secretMarker) {
		t.Errorf("long-line flush leaked or lost the value (len %d)", len(s))
	}
}

type fakeSession struct {
	events chan agentcli.Event
}

func (f *fakeSession) Send(context.Context, agentcli.Turn) error {
	return errors.New("send " + secretValue)
}
func (f *fakeSession) Events() <-chan agentcli.Event { return f.events }
func (f *fakeSession) Cancel() error                 { return nil }
func (f *fakeSession) Close() error                  { return errors.New("close " + secretValue) }
func (f *fakeSession) Captured() []byte              { return []byte("cap " + secretValue) }

// AC2 for a live session: its channel, capture and errors are scrubbed.
func TestRedactSessionScrubsChannelCaptureAndErrors(t *testing.T) {
	src := make(chan agentcli.Event, 1)
	src <- agentcli.Event{Kind: agentcli.EventMessage, Text: "msg " + secretValue}
	close(src)
	s := redactSession(&fakeSession{events: src}, redactSet)
	var got []agentcli.Event
	for ev := range s.Events() {
		got = append(got, ev)
	}
	if len(got) != 1 || got[0].Text != "msg "+secretMarker {
		t.Errorf("events = %+v", got)
	}
	if c := string(s.Captured()); c != "cap "+secretMarker {
		t.Errorf("captured = %q", c)
	}
	for _, err := range []error{s.Send(context.Background(), agentcli.Turn{}), s.Close()} {
		if err == nil || strings.Contains(err.Error(), secretValue) {
			t.Errorf("session error leaks the value: %v", err)
		}
	}
	if same := redactSession(&fakeSession{}, nil); reflect.TypeOf(same) != reflect.TypeOf(&fakeSession{}) {
		t.Error("an empty set must not wrap the session")
	}
}
