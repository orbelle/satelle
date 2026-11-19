package substrate

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSplitCommands(t *testing.T) {
	for value, want := range map[string][]string{
		"":                                    nil,
		"   ":                                 nil,
		"[]":                                  nil,
		"story get":                           {"story get"},
		" story   get ,  task get ":           {"story get", "task get"},
		"satelle story get, story list":       {"story get", "story list"},
		"[\"satelle story get\", 'task get']": {"story get", "task get"},
		"story get\", \"story list":           {"story get", "story list"},
		"satelle":                             {""},
		"story get,":                          {"story get", ""},
		"*":                                   {"*"},
		"satellex story":                      {"satellex story"},
	} {
		if got := SplitCommands(value); !reflect.DeepEqual(got, want) {
			t.Errorf("SplitCommands(%q) = %q, want %q", value, got, want)
		}
	}
}

func TestReferences(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, ".satelle")
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(data, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("principles/plain.md", "---\nname: plain-rule\ndescription: a plain list\ncommands: story get, story list\n---\n\nbody\n")
	write("principles/bracketed.md", "---\ndescription: bracketed and quoted\n\ncommands: [\"satelle story get\", 'task get']\n---\n\nbody\n")
	write("principles/nokey.md", "---\nname: nokey\ndescription: no key\n---\n\nbody\n")
	write("principles/scoped.md", "---\nname: scoped\napplies_to: [\"*\"]\n---\n\nbody\n")
	write("principles/bare.md", "commands: story get\n")
	write("principles/empty.md", "---\nname: empty\ncommands:\n---\n")
	write("skills/broken.md", "---\nname: broken\ntype: check\ncommands: story get\n---\n\nno script\n")
	write("skills/keyed.md", "---\nname: keyed-skill\ncommands: task get\n---\n\nbody\n")
	write("skills/notes.txt", "---\ncommands: story get\n---\n")
	write("skills/sub/inner.md", "---\nname: inner\ncommands: story get\n---\n")
	write("tasks/task.md", "---\nname: task\ncommands: story get\n---\n")
	got, err := References(data)
	if err != nil {
		t.Fatal(err)
	}
	want := []Reference{
		{Name: "bracketed", Description: "bracketed and quoted", Path: ".satelle/principles/bracketed.md", Line: 4, Commands: []string{"story get", "task get"}},
		{Name: "plain-rule", Description: "a plain list", Path: ".satelle/principles/plain.md", Line: 4, Commands: []string{"story get", "story list"}},
		{Name: "keyed-skill", Path: ".satelle/skills/keyed.md", Line: 3, Commands: []string{"task get"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("references:\n got %+v\nwant %+v", got, want)
	}
	if got, err := References(filepath.Join(t.TempDir(), ".satelle")); err != nil || len(got) != 0 {
		t.Fatalf("no data dir: %+v %v", got, err)
	}
}
