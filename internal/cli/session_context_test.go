package cli

import (
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/hook"
)

func TestSessionStartTextFormsByLimit(t *testing.T) {
	const preface, notice = "satelle session: s\nseats     none\nguidance\nsatelle doctor: 1 issue(s)\n  check (1): one\n", "satelle doctor: 1 issue(s)"
	const inline = preface + "\nbody-a\n\n<b> & \"quoted\"\n\nsatelle: [hook] inject file gone.md: no such file\n\nbody-bare\n"
	entries := []indexEntry{
		{Path: "a.md", Description: "héllo — wörld"},
		{Path: "b.md", Description: "angles"},
		{Path: "gone.md", Description: "unreadable: no such file"},
		{Path: "bare.md"},
	}
	for _, h := range hook.Harnesses {
		size := emittedBytes(h, inline, notice)
		if size != len(hook.Context(h, hook.EventSessionStart, inline, notice))+1 {
			t.Fatalf("%s: emittedBytes is not the envelope and its newline", h)
		}
		if size <= len(inline) {
			t.Fatalf("%s: emittedBytes %d does not count the envelope around %d bytes of text", h, size, len(inline))
		}
		if overLimit(h, inline, notice, 0, len(entries)) {
			t.Fatalf("%s: no limit is over the limit", h)
		}
		for limit := 1; limit <= size+1; limit++ {
			if got, want := overLimit(h, inline, notice, limit, len(entries)), limit < size; got != want {
				t.Fatalf("%s: limit %d of inline size %d: over %v, want %v", h, limit, size, got, want)
			}
			if overLimit(h, inline, notice, limit, 0) {
				t.Fatalf("%s: no inject file is over limit %d", h, limit)
			}
		}
		index := indexForm(preface, entries, "/tree", h)
		if !strings.HasPrefix(index, preface+"\nRead each of these files in full before acting (paths are relative to /tree); inlined they are over "+h+"'s configured session-start limit") {
			t.Fatalf("%s: the index form does not open with the preface and the read line:\n%s", h, index)
		}
		if !strings.HasSuffix(index, ":\n  a.md — héllo — wörld\n  b.md — angles\n  gone.md — unreadable: no such file\n  bare.md\n") {
			t.Fatalf("%s: the index form does not end with one line per file:\n%s", h, index)
		}
		for _, body := range []string{"body-a", "quoted", "body-bare"} {
			if strings.Contains(index, body) {
				t.Fatalf("%s: the index form carries a file body (%s):\n%s", h, body, index)
			}
		}
	}
}

func TestDocDescription(t *testing.T) {
	for name, tc := range map[string]struct{ text, want string }{
		"frontmatter description": {"---\nname: x\ndescription: what it is\n---\n\n# Title\n", "what it is"},
		"first heading":           {"\n\n## Plain title\n\nbody\n", "Plain title"},
		"first line":              {"first line\nsecond\n", "first line"},
		"frontmatter alone":       {"---\nname: x\n---\n", ""},
		"empty":                   {"", ""},
		"unterminated":            {"---\ndescription: lost\nbody\n", "---"},
	} {
		if got := docDescription("f.md", tc.text); got != tc.want {
			t.Errorf("%s: description %q, want %q", name, got, tc.want)
		}
	}
}
