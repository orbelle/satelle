//go:build linux

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/hook"
	"github.com/bobmcallan/satelle/internal/kernel"
)

const (
	framingPrefix  = "framing-nonce-a\nframing-nonce-c\nsatelle session: s-hook\n"
	framingSession = "satelle session: s-hook"
)

var (
	framingOthers = []string{"skill-nonce", "plain-nonce", "body-first-nonce", "broken-nonce"}
	framingBodies = []string{"body-pa", "body-s", "body-plain", "body-nodesc", "body-broken", "body-pc"}
)

func framingFixture(t *testing.T) *doctorFixture {
	t.Helper()
	f := newDoctorFixture(t)
	files := []struct{ name, text string }{
		{"p-a.md", "---\ntype: principle\nframing: true\ndescription: framing-nonce-a\n---\n\nbody-pa\n"},
		{"s.md", "---\ntype: skill\nframing: true\ndescription: skill-nonce\n---\n\nbody-s\n"},
		{"p-plain.md", "---\ntype: principle\ndescription: plain-nonce\n---\n\nbody-plain\n"},
		{"p-nodesc.md", "---\ntype: principle\nframing: true\n---\n\nbody-first-nonce\nbody-nodesc\n"},
		{"p-broken.md", "---\ntype: principle\nframing: true\ndescription: broken-nonce\nbody-broken\n"},
		{"p-c.md", "---\ntype: principle\nframing: true\ndescription: framing-nonce-c\n---\n\nbody-pc\n"},
	}
	var inject []string
	for _, file := range files {
		writeFile(t, filepath.Join(f.dir, "ctx", file.name), file.text)
		inject = append(inject, strconv.Quote("ctx/"+file.name))
	}
	writeHookSettings(t, f.dir, "inject = ["+strings.Join(inject, ", ")+"]\n")
	return f
}

func (f *doctorFixture) noLimit(t *testing.T) {
	t.Helper()
	if err := os.Remove(filepath.Join(f.dir, ".satelle", kernel.HarnessFile)); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestContextOpensWithFramingPrincipleDescriptions(t *testing.T) {
	f := framingFixture(t)
	for _, h := range hook.Harnesses {
		f.noLimit(t)
		ctx, inline := f.context(t, h)
		if !strings.HasPrefix(ctx, framingPrefix) {
			t.Fatalf("%s: the context does not open with the two framing descriptions, then the session line:\n%s", h, ctx)
		}
		session := strings.Index(ctx, framingSession)
		for _, other := range framingOthers {
			if i := strings.Index(ctx, other); i < session {
				t.Errorf("%s: %s is at %d, not after the session line at %d", h, other, i, session)
			}
		}
		hasAll(t, h+" inline", ctx, framingBodies...)
		if n := strings.Count(ctx[:session], "\n"); n != 2 {
			t.Errorf("%s: %d lines before the session line, want the two framing lines", h, n)
		}
		f.limit(t, h, len(inline)-1)
		ctx, raw := f.context(t, h)
		if !strings.HasPrefix(ctx, framingPrefix) {
			t.Fatalf("%s: the index form does not open with the two framing descriptions, then the session line:\n%s", h, ctx)
		}
		hasAll(t, h+" index", ctx, indexReadLine)
		hasNone(t, h+" index", raw, framingBodies...)
	}
	raw, err := run(t, fmt.Sprintf(`{"session_id":"s-hook","cwd":%q}`, f.dir), "hook", "prompt", "--harness", hook.Claude)
	if err != nil {
		t.Fatalf("hook prompt: %v\n%s", err, raw)
	}
	hasAll(t, "prompt", raw, framingSession)
	hasNone(t, "prompt", raw, "framing-nonce-a", "framing-nonce-c")
}

func TestFramingLineIsWithinTheHarnessLimit(t *testing.T) {
	const last = "framing-nonce-c"
	f := framingFixture(t)
	for _, h := range hook.Harnesses {
		f.noLimit(t)
		_, inline := f.context(t, h)
		end := strings.Index(inline, last) + len(last)
		if end < len(last) {
			t.Fatalf("%s: no framing line in the output:\n%s", h, inline)
		}
		f.limit(t, h, end)
		ctx, raw := f.context(t, h)
		hasAll(t, h+" at the framing line's end", ctx, indexReadLine)
		if got := strings.Index(raw, last) + len(last); got > end || !strings.HasPrefix(ctx, framingPrefix) {
			t.Errorf("%s: with the limit at byte %d the framing lines end at byte %d:\n%s", h, end, got, raw)
		}
		f.limit(t, h, len(inline))
		if _, raw := f.context(t, h); raw != inline || end > len(inline) {
			t.Errorf("%s: with the limit at the inline size the output changed, or the framing lines end past it:\n%s", h, raw)
		}
	}
	f.noLimit(t)
	_, raw := f.context(t, hook.Claude)
	_, notes, _ := contextReport(t)
	want := fmt.Sprintf("context: %s: session-start context is %d bytes, unbudgeted", hook.Claude, len(raw))
	if len(notes) != 1 || !strings.HasPrefix(notes[0], want) {
		t.Errorf("doctor does not measure the framing lines: notes %q, want one starting %q", notes, want)
	}
}
