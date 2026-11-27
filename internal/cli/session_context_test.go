package cli

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
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

func TestLedgerSeatsEqualTheCachesSeats(t *testing.T) {
	isolate(t)
	must(t, "init")
	first := createID(t, "story", "create", "--title", "seated first")
	second := createID(t, "story", "create", "--title", "seated second")
	other := createID(t, "story", "create", "--title", "seated by another")
	must(t, "story", "seat", second, "--session", "s-two")
	must(t, "story", "seat", other, "--session", "s-other")
	must(t, "story", "seat", first, "--session", "s-two")
	k, _, err := openKernel()
	if err != nil {
		t.Fatal(err)
	}
	want, err := seatsOf(k, "s-two")
	k.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(want) != 2 || want[0].Item != second || want[1].Item != first {
		t.Fatalf("the cache's seats are not the two of s-two in bound order: %+v", want)
	}
	in := mustDoctorInput(t, "")
	if got := ledgerSeats(in, "s-two"); !reflect.DeepEqual(got, want) {
		t.Errorf("ledgerSeats:\n got %+v\nwant %+v", got, want)
	}
	if got := ledgerSeats(in, "s-none"); len(got) != 0 {
		t.Errorf("a session with no seat: %+v", got)
	}
}

func TestSessionDoctorIgnoresNodeHint(t *testing.T) {
	dir := isolate(t)
	must(t, "init")
	sub := namedSubstrate(t)
	marker := filepath.Join(t.TempDir(), "node-check-ran")
	writeFile(t, filepath.Join(sub, "skills", "node-probe.md"), "---\nname: node-probe\ntype: check\n---\n\n```sh\ntouch '"+marker+"'\nexit 0\n```\n")
	cur, err := os.ReadFile(filepath.Join(sub, "doctor.toml"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sub, "doctor.toml"), string(cur)+"\n[[check]]\nskill = \"node-probe\"\nfix = \"x\"\n")
	stampNodeRun(t, sub, dir)
	if in := mustDoctorInput(t, dir); in.r.DataDir != sub {
		t.Fatalf("the hint is not live: satelle doctor resolved %s, want the node's %s", in.r.DataDir, sub)
	}
	sessionDoctor(dir, "claude")
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the session-start doctor ran a check of the hinted node substrate")
	}
	t.Setenv(config.NodeRunEnv, t.TempDir())
	if _, err := loadDoctorInput(dir); !errors.Is(err, config.ErrNodeHint) {
		t.Fatalf("satelle doctor under a refused hint = %v, want the refusal", err)
	}
	if in, err := doctorInputFor(findOwnRepo(dir)); err != nil || in.r.DataDir != filepath.Join(dir, ".satelle") {
		t.Fatalf("the session-start doctor under a refused hint resolved %+v, %v; want the own substrate", in, err)
	}
	sessionDoctor(dir, "claude")
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the session-start doctor ran a check of the hinted node substrate under a refused hint")
	}
}
