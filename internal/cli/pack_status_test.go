package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

type packStatusJSON struct {
	Packs []struct {
		Ref, Name, Version, Newest string
		Edited, Missing            []string
		NewestError                string `json:"newest_error"`
	}
}

func packStatus(t *testing.T, server string) (string, packStatusJSON) {
	t.Helper()
	text, err := run(t, "", "pack", "status", "--server", server)
	if err != nil {
		t.Fatalf("pack status: %v\n%s", err, text)
	}
	raw, err := run(t, "", "pack", "status", "--server", server, "--json")
	if err != nil {
		t.Fatalf("pack status --json: %v\n%s", err, raw)
	}
	var doc packStatusJSON
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("json %v:\n%s", err, raw)
	}
	if !strings.Contains(raw, `"packs":`) || doc.Packs == nil {
		t.Fatalf("packs is not an array:\n%s", raw)
	}
	return text, doc
}

func TestPackStatusListsVersionEditsAndNewest(t *testing.T) {
	dir, srv, _ := packRepo(t)
	must(t, "pack", "install", "acme/review", "--version", "1.2.3", "--server", srv.URL, "--session", "s-1")
	seedPack(srv, "acme/review", "1.3.0", reviewPack130())
	skills := filepath.Join(dir, ".satelle", "skills")
	if err := os.WriteFile(filepath.Join(skills, "review", "SKILL.md"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(skills, "review", "pack.toml")); err != nil {
		t.Fatal(err)
	}

	text, doc := packStatus(t, srv.URL)
	want := "acme/review 1.2.3: newest 1.3.0\n  edited .satelle/skills/review/SKILL.md\n  missing .satelle/skills/review/pack.toml\n"
	if text != want {
		t.Errorf("text %q, want %q", text, want)
	}
	if len(doc.Packs) != 1 {
		t.Fatalf("json %+v", doc)
	}
	p := doc.Packs[0]
	if p.Ref != "acme/review" || p.Name != "review" || p.Version != "1.2.3" || p.Newest != "1.3.0" || p.NewestError != "" ||
		strings.Join(p.Edited, ",") != ".satelle/skills/review/SKILL.md" || strings.Join(p.Missing, ",") != ".satelle/skills/review/pack.toml" {
		t.Errorf("json %+v", p)
	}
}

func TestPackStatusUnreachableMarketplace(t *testing.T) {
	_, srv, _ := packRepo(t)
	must(t, "pack", "install", "acme/review", "--version", "1.2.3", "--server", srv.URL, "--session", "s-1")
	text, doc := packStatus(t, "http://127.0.0.1:1")
	if len(doc.Packs) != 1 || doc.Packs[0].Newest != "" || !strings.HasPrefix(doc.Packs[0].NewestError, "the marketplace could not be read") {
		t.Errorf("json %+v", doc)
	}
	if !strings.HasPrefix(text, "acme/review 1.2.3: newest unknown (the marketplace could not be read") {
		t.Errorf("text %q", text)
	}
}

func TestPackStatusNoPacks(t *testing.T) {
	newMachine(t)
	text, doc := packStatus(t, "http://127.0.0.1:1")
	if text != "no packs installed (.satelle/packs.toml records none)\n" {
		t.Errorf("text %q", text)
	}
	if len(doc.Packs) != 0 {
		t.Errorf("json %+v", doc)
	}
	raw, _ := run(t, "", "pack", "status", "--json")
	if strings.Join(strings.Fields(raw), "") != `{"packs":[]}` {
		t.Errorf("json %q", raw)
	}
}

func TestPackStatusRefusesDataDirOutsideTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	m := newMachine(t)
	away := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.Rename(filepath.Join(m.dir, ".satelle"), away); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(away, filepath.Join(m.dir, ".satelle")); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "", "pack", "status")
	if err == nil || !strings.Contains(err.Error()+out, "outside the worktree") {
		t.Fatalf("pack status: %v\n%s", err, out)
	}
}

func TestNewestOf(t *testing.T) {
	latest := func(v string) *satellepb.PackVersion {
		return &satellepb.PackVersion{Pack: &satellepb.Pack{LatestVersion: v}}
	}
	const unread = "the marketplace could not be read: "
	cases := []struct {
		name          string
		resp          *satellepb.PackVersion
		err           error
		newest, wantE string
	}{
		{"latest", latest("1.3.0"), nil, "1.3.0", ""},
		{"control latest", latest("1.0\n\x1b[31mx"), nil, "", unread + "it named a latest version that is not a plain version"},
		{"flag latest", latest("--version"), nil, "", unread + "it named a latest version that is not a plain version"},
		{"empty latest", latest(""), nil, "", unread + "it named no latest version"},
		{"not found", nil, status.Error(codes.NotFound, "gone"), "", unread + "pack acme/review version 1.2.3 was not found"},
	}
	for _, c := range cases {
		n, e := newestOf("acme/review", "1.2.3", c.resp, c.err)
		if n != c.newest || e != c.wantE {
			t.Errorf("%s: newestOf = %q, %q; want %q, %q", c.name, n, e, c.newest, c.wantE)
		}
	}
	for _, err := range []error{status.Error(codes.Unavailable, "down\n\x1b[31mred\u2028x"), errors.New("plain\r\nerror\x1b")} {
		n, e := newestOf("acme/review", "1.2.3", nil, err)
		if n != "" || !strings.HasPrefix(e, unread) || strings.ContainsFunc(e, func(r rune) bool { return r < 0x20 || r == 0x7f || r == '\u2028' }) {
			t.Errorf("newestOf(%v) = %q, %q", err, n, e)
		}
	}
}
