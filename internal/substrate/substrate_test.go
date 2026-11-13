package substrate

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fixtureTree is a small binary: satelle story {get,list}, ledger list,
// execution seat, validate, help (a leaf whose words are topics), with flags.
func fixtureTree() Tree {
	t := NewTree()
	for _, s := range []string{"story", "ledger", "execution", "validate", "help", "task"} {
		t.AddSub("", s)
	}
	t.AddSub("story", "get")
	t.AddSub("story", "list")
	t.AddSub("ledger", "list")
	t.AddSub("execution", "seat")
	t.AddSub("task", "get")
	for _, p := range []string{"", "story", "story get", "story list", "ledger", "ledger list", "execution", "execution seat", "validate", "help", "task", "task get"} {
		t.AddFlag(p, "--help")
	}
	t.AddFlag("story get", "--json")
	t.AddFlag("story list", "--json")
	t.AddFlag("story list", "--open")
	t.AddFlag("ledger list", "--kind")
	t.AddFlag("ledger list", "--input-file")
	t.AddFlag("execution seat", "--base")
	t.HelpPath = "help"
	t.Topics["workflows"] = true
	return t
}

func lines(fs []Finding) []string {
	out := []string{}
	for _, f := range fs {
		out = append(out, f.String())
	}
	return out
}

// Each rule of the drift check, ported: a clean text yields nothing; one
// planted miss of each kind yields one finding in drift-check's line form;
// elided flags, quoted first words, for-loop word lists and 4-space list items
// are not misses (sto_a1681009 AC2).
func TestCheckText(t *testing.T) {
	tree := fixtureTree()
	links := map[string]bool{"good": true, "helper": true}
	clean := "---\nname: good\n---\n\n" +
		"Read a story with `satelle story get <id> --json`; satelle runs them, and\n" +
		"`satelle <command>` is any command. See [[helper]] and [[good]].\n\n" +
		"    satelle help workflows\n\n" +
		"A TOML table such as `[[transition]]` is not a link.\n\n" +
		"```sh\nsatelle story list --open --json | jq length\nsatelle execution seat \"$exe\" --base \"v$prev\"\n" +
		"satelle ledger list --input-… x\n" +
		"echo \"the satelle app is down\"\n" +
		"for tool in git satelle bash; do :; done\n```\n"
	if got := CheckText("p.md", "md", clean, tree, links); len(got) != 0 {
		t.Fatalf("clean text: %v", lines(got))
	}
	// A 4-space-indented list item is prose, not code.
	if got := CheckText("p.md", "md", "    - satelle nosuch in prose\n", tree, links); len(got) != 0 {
		t.Fatalf("indented list item: %v", lines(got))
	}

	cases := []struct {
		file, ext, text, want string
	}{
		{".satelle/tasks/README.md", "md", "Archive with `satelle task archive <id>`.\n",
			".satelle/tasks/README.md:1: command: satelle task archive — archive is not a subcommand of satelle task"},
		{".satelle/skills/flag.md", "md", "x\n\nList with `satelle ledger list --story <id>`.\n",
			".satelle/skills/flag.md:3: flag: satelle ledger list --story — --story is not a flag of satelle ledger list"},
		{".satelle/skills/topic.md", "md", "See `satelle help no-such-topic`.\n",
			".satelle/skills/topic.md:1: topic: satelle help no-such-topic — no-such-topic is not a help topic or command of satelle"},
		{".satelle/principles/dead.md", "md", "Ship small ([[satelle-start-small]]).\n",
			".satelle/principles/dead.md:1: link: satelle-start-small — no .satelle/principles/satelle-start-small.md or .satelle/skills/satelle-start-small.md"},
		{"x.md", "md", "`satelle nosuch`\n", "x.md:1: command: satelle nosuch — nosuch is not a command of satelle"},
		{"x.md", "md", "`satelle ledger list --output-… y`\n", "x.md:1: flag: satelle ledger list --output — --output-… begins no flag of satelle ledger list"},
		{"x.sh", "sh", "satelle story nope # comment\n", "x.sh:1: command: satelle story nope — nope is not a subcommand of satelle story"},
		{"x.toml", "toml", "fix = \"run `satelle bogus`\"\nnot = \"satelle bogus\"\n", "x.toml:1: command: satelle bogus — bogus is not a command of satelle"},
		{"x.md", "md", "```\nsatelle story list | satelle nope\n```\n", "x.md:2: command: satelle nope — nope is not a command of satelle"},
	}
	for _, c := range cases {
		got := lines(CheckText(c.file, c.ext, c.text, tree, links))
		if !reflect.DeepEqual(got, []string{c.want}) {
			t.Errorf("%s %q:\n got %q\nwant %q", c.file, c.text, got, c.want)
		}
	}
	// A quoted first word that is no command is prose; a quoted real command is
	// still checked.
	if got := lines(CheckText("q.sh", "sh", "echo \"satelle story nope now\"\n", tree, links)); len(got) != 1 {
		t.Errorf("quoted real command: %q", got)
	}
}

// Check reads the five substrate directories, skips testdata/ and remotes/,
// names files relative to the repo root and sorts its findings.
func TestCheckWalksSubstrate(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, ".satelle")
	write := func(rel, body string) {
		p := filepath.Join(data, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("skills/b.md", "a\n`satelle nope`\n")
	write("skills/a.md", "`satelle nope`\n\n[[b]] [[gone]]\n")
	write("principles/p.md", "`satelle nope`\n")
	write("tasks/t.sh", "satelle nope\n")
	write("workflows/w.toml", "# `satelle nope`\n")
	write("queues/q.toml", "# `satelle nope`\n")
	write("queues/notes.txt", "`satelle nope`\n")
	write("skills/testdata/x.md", "`satelle nope`\n")
	write("remotes/r.md", "`satelle nope`\n")
	write("satelle.toml", "# `satelle nope`\n")
	got, err := Check(data, fixtureTree())
	if err != nil {
		t.Fatal(err)
	}
	var where []string
	for _, f := range got {
		where = append(where, f.File+":"+f.Kind+":"+strings.SplitN(f.Subject, " ", 2)[0])
	}
	want := []string{
		".satelle/principles/p.md:command:satelle",
		".satelle/queues/q.toml:command:satelle",
		".satelle/skills/a.md:command:satelle",
		".satelle/skills/a.md:link:gone",
		".satelle/skills/b.md:command:satelle",
		".satelle/tasks/t.sh:command:satelle",
		".satelle/workflows/w.toml:command:satelle",
	}
	if !reflect.DeepEqual(where, want) {
		t.Fatalf("findings:\n got %q\nwant %q", where, want)
	}
	if got[3].Line != 3 || got[4].Line != 2 {
		t.Errorf("lines: %v", lines(got))
	}
	// A data dir with none of the directories is clean.
	if got, err := Check(t.TempDir(), fixtureTree()); err != nil || len(got) != 0 {
		t.Fatalf("empty data dir: %v %v", got, err)
	}
}

// Lacks judges a hook's command path: a word that is no subcommand of a path
// with subcommands is lacking; a word that is not a plain command word, or a
// path with no subcommands, stops the walk, not lacking.
func TestTreeLacks(t *testing.T) {
	tree := fixtureTree()
	tree.AddSub("", "hook")
	tree.AddSub("hook", "gate")
	tree.AddSub("", "doctor")
	for _, c := range []struct {
		words        []string
		subject, why string
	}{
		{[]string{"reindex"}, "satelle reindex", "reindex is not a command of satelle"},
		{[]string{"story", "nosuch"}, "satelle story nosuch", "nosuch is not a subcommand of satelle story"},
		{[]string{"reindex", "&&", "echo", "done"}, "satelle reindex", "reindex is not a command of satelle"},
	} {
		subject, why, lacks := tree.Lacks(c.words)
		if !lacks || subject != c.subject || why != c.why {
			t.Errorf("Lacks(%q) = %q, %q, %v; want %q, %q, true", c.words, subject, why, lacks, c.subject, c.why)
		}
	}
	for _, words := range [][]string{
		{"hook", "gate"}, {"doctor"}, {}, {"doctor", "||", "true"}, {"story", "sto_123"}, {"reindex;"},
	} {
		if subject, _, lacks := tree.Lacks(words); lacks {
			t.Errorf("Lacks(%q) lacks %q; want not", words, subject)
		}
	}
}
