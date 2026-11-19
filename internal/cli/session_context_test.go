package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/hook"
)

func TestSessionStartTextFormsByLimit(t *testing.T) {
	const head, block, notice = "satelle session: s\nseats     none\nguidance\n", "satelle doctor: 1 issue(s)\n  check (1): one\n", "satelle doctor: 1 issue(s)"
	docs := []injectDoc{
		{Path: "a.md", Description: "héllo — wörld", body: "body-a"},
		{Path: "b.md", Description: "angles", body: "<b> & \"quoted\""},
		{Path: "gone.md", Description: "unreadable: no such file", err: errors.New("no such file")},
		{Path: "bare.md", body: "body-bare"},
	}
	for _, h := range hook.Harnesses {
		inline, form := sessionStartText(head, block, docs, "/tree", h, notice, 0)
		if form != contextInline {
			t.Fatalf("%s: no limit gave the %s form", h, form)
		}
		for _, want := range []string{"\nbody-a\n", "\n<b> & \"quoted\"\n", "\nsatelle: [hook] inject file gone.md: no such file\n", "\nbody-bare\n"} {
			if !strings.Contains(inline, want) {
				t.Fatalf("%s: the inline form lacks %q:\n%s", h, want, inline)
			}
		}
		size := emittedBytes(h, inline, notice)
		if size != len(hook.Context(h, hook.EventSessionStart, inline, notice))+1 {
			t.Fatalf("%s: emittedBytes is not the envelope and its newline", h)
		}
		index := ""
		for limit := 1; limit <= size+1; limit++ {
			text, form := sessionStartText(head, block, docs, "/tree", h, notice, limit)
			if want := map[bool]string{true: contextInline, false: contextIndex}[limit >= size]; form != want {
				t.Fatalf("%s: limit %d of inline size %d gave the %s form, want %s", h, limit, size, form, want)
			}
			if form == contextInline {
				if text != inline {
					t.Fatalf("%s: limit %d changed the inline form", h, limit)
				}
				continue
			}
			if index == "" {
				index = text
			}
			if text != index {
				t.Fatalf("%s: limit %d changed the index form", h, limit)
			}
		}
		if !strings.HasPrefix(index, head+block+"\nRead each of these files in full before acting (paths are relative to /tree); inlined they are over "+h+"'s configured session-start limit") {
			t.Fatalf("%s: the index form does not open with the head, the block and the read line:\n%s", h, index)
		}
		if !strings.HasSuffix(index, ":\n  a.md — héllo — wörld\n  b.md — angles\n  gone.md — unreadable: no such file\n  bare.md\n") {
			t.Fatalf("%s: the index form does not end with one line per file:\n%s", h, index)
		}
		for _, body := range []string{"body-a", "quoted", "body-bare"} {
			if strings.Contains(index, body) {
				t.Fatalf("%s: the index form carries a file body (%s):\n%s", h, body, index)
			}
		}
		if text, form := sessionStartText(head, block, nil, "/tree", h, notice, 1); form != contextInline || text != head+block {
			t.Fatalf("%s: no inject file at limit 1 gave the %s form:\n%s", h, form, text)
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
