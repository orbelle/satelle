// Package substrate checks a repo's substrate (its .satelle skills,
// principles, tasks, workflows and queues) against the running binary: every
// `satelle <command> [<subcommand>] [--flag]` it names in code, every
// `satelle help <topic>`, and every [[name]] link must exist. It is the one
// owner of these rules; `satelle validate` and `satelle doctor` report its
// findings, and scripts/drift-check.sh prints them.
package substrate

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// The finding kinds this package reports.
const (
	KindCommand = "command"
	KindFlag    = "flag"
	KindTopic   = "topic"
	KindLink    = "link"
)

// Field names these findings wherever they are reported: the key of
// `satelle validate --json`'s list of them and the name of satelle doctor's
// check that reports them.
const Field = "substrate"

// Dirs are the directories under the data dir whose files are substrate.
var Dirs = []string{"skills", "principles", "tasks", "workflows", "queues"}

// Finding is one name the substrate uses that the binary lacks.
type Finding struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Kind    string `json:"kind"`
	Subject string `json:"subject"`
	Why     string `json:"why"`
}

// String is the finding's line: `<file>:<line>: <kind>: <subject> — <why>`.
func (f Finding) String() string {
	return fmt.Sprintf("%s:%d: %s: %s — %s", f.File, f.Line, f.Kind, f.Subject, f.Why)
}

// Tree is the binary's command tree as the checks read it. Paths are command
// paths below the root joined by single spaces ("" is the root).
type Tree struct {
	Subs   map[string]map[string]bool // path → its subcommands
	Flags  map[string]map[string]bool // path → its flags (--name), own and inherited
	Topics map[string]bool            // help topics
	// HelpPath is the path of the command whose words are topics.
	HelpPath string
}

// NewTree returns an empty tree.
func NewTree() Tree {
	return Tree{Subs: map[string]map[string]bool{}, Flags: map[string]map[string]bool{}, Topics: map[string]bool{}}
}

// AddSub records sub as a subcommand of path.
func (t Tree) AddSub(path, sub string) {
	if t.Subs[path] == nil {
		t.Subs[path] = map[string]bool{}
	}
	t.Subs[path][sub] = true
}

// AddFlag records flag (--name) on path.
func (t Tree) AddFlag(path, flag string) {
	if t.Flags[path] == nil {
		t.Flags[path] = map[string]bool{}
	}
	t.Flags[path][flag] = true
}

var (
	reMention   = regexp.MustCompile(`(^|[^A-Za-z0-9_./-])satelle[ \t]+[a-z]`)
	reCut       = regexp.MustCompile(`[|;)]|&&|[ \t]#`)
	reFlag      = regexp.MustCompile(`^--[a-z][a-z0-9-]*`)
	reWord      = regexp.MustCompile(`^[a-z][a-z-]*$`)
	reForList   = regexp.MustCompile(`(^|[ \t;])for[ \t]+[A-Za-z_]+[ \t]+in[ \t][^;]*$`)
	reSpan      = regexp.MustCompile("`[^`]+`")
	reSpanAny   = regexp.MustCompile("`[^`]*`")
	reFence     = regexp.MustCompile("^[ \t]*(```|~~~)")
	reIndented  = regexp.MustCompile(`^    [ \t]*[^ \t]`)
	reListItem  = regexp.MustCompile(`^[ \t]*([-*+]|[0-9]+\.)[ \t]`)
	reLink      = regexp.MustCompile(`\[\[[a-z0-9][a-z0-9-]*\]\]`)
	reFlagStart = regexp.MustCompile(`^--[a-z]`)
)

// notCommand is why word is no command at path: "<word> is not a command of
// satelle" at the root, else "<word> is not a subcommand of satelle <path>".
func notCommand(path, word string) string {
	if path == "" {
		return word + " is not a command of satelle"
	}
	return word + " is not a subcommand of satelle " + path
}

// Lacks reports whether the command path words (the words after "satelle")
// names a command the tree lacks. It descends from the root and stops, not
// lacking, at a word that is not a plain command word or at a path with no
// subcommands; a word that is not a subcommand of a path that has some is
// lacking: subject is "satelle <path> <word>" and why says which. Flags are
// not judged.
func (t Tree) Lacks(words []string) (subject, why string, lacks bool) {
	path := ""
	for _, w := range words {
		if !reWord.MatchString(w) || len(t.Subs[path]) == 0 {
			return "", "", false
		}
		if !t.Subs[path][w] {
			return "satelle" + pathSuffix(path) + " " + w, notCommand(path, w), true
		}
		path = strings.TrimSpace(path + " " + w)
	}
	return "", "", false
}

// checker walks one file's text.
type checker struct {
	file  string
	tree  Tree
	links map[string]bool
	line  int
	out   []Finding
}

func (c *checker) miss(kind, subject, why string) {
	c.out = append(c.out, Finding{File: c.file, Line: c.line, Kind: kind, Subject: subject, Why: why})
}

func pathSuffix(path string) string {
	if path == "" {
		return ""
	}
	return " " + path
}

// check walks one mention: rest is the text after "satelle"; quoted is set
// when the mention sits inside a "..." string, where a first word that is no
// command is prose, not a call.
func (c *checker) check(rest string, quoted bool) {
	if loc := reCut.FindStringIndex(rest); loc != nil {
		rest = rest[:loc[0]]
	}
	path, words, descend := "", "satelle", true
	for _, t := range strings.FieldsFunc(rest, func(r rune) bool { return r == ' ' || r == '\t' }) {
		if reFlagStart.MatchString(t) {
			f := reFlag.FindString(t)
			if strings.HasSuffix(f, "-") { // an elided flag (--input-…): any flag it begins
				f = strings.TrimRight(f, "-")
				known := false
				for k := range c.tree.Flags[path] {
					if strings.HasPrefix(k, f+"-") {
						known = true
						break
					}
				}
				if !known {
					c.miss(KindFlag, words+" "+f, f+"-… begins no flag of satelle"+pathSuffix(path))
				}
				continue
			}
			if !c.tree.Flags[path][f] {
				c.miss(KindFlag, words+" "+f, f+" is not a flag of satelle"+pathSuffix(path))
			}
			continue
		}
		if strings.HasPrefix(t, "-") || !descend {
			continue
		}
		t = strings.TrimRight(t, ".,:")
		if !reWord.MatchString(t) {
			descend = false
			continue
		}
		if c.tree.HelpPath != "" && path == c.tree.HelpPath {
			if !c.tree.Topics[t] && !c.tree.Subs[""][t] {
				c.miss(KindTopic, "satelle "+c.tree.HelpPath+" "+t, t+" is not a help topic or command of satelle")
			}
			descend = false
			continue
		}
		if len(c.tree.Subs[path]) == 0 {
			descend = false
			continue
		}
		if !c.tree.Subs[path][t] {
			if path == "" && quoted {
				return
			}
			c.miss(KindCommand, words+" "+t, notCommand(path, t))
			return
		}
		if path == "" {
			path = t
		} else {
			path += " " + t
		}
		words += " " + t
	}
}

// mentions checks every mention in code text s, skipping a word list
// (for tool in git satelle bash).
func (c *checker) mentions(s string) {
	q := 0
	for {
		loc := reMention.FindStringIndex(s)
		if loc == nil {
			return
		}
		i := loc[0] + strings.Index(s[loc[0]:], "satelle")
		before := s[:i]
		q = (q + strings.Count(before, `"`)) % 2
		if !reForList.MatchString(before) {
			c.check(s[i+len("satelle"):], q == 1)
		}
		s = s[i+len("satelle"):]
	}
}

// spans checks the mentions in each backtick span of s.
func (c *checker) spans(s string) {
	for _, m := range reSpan.FindAllString(s, -1) {
		c.mentions(m[1 : len(m)-1])
	}
}

// CheckText checks one file's text. file is the path findings name; ext is
// its extension without the dot (md, toml or sh): a .sh file is read on every
// line, a .toml file in backtick spans, and a .md file in fenced blocks,
// 4-space-indented lines that are not list items and backtick spans, with
// its [[name]] links read outside code. links holds the names a link may
// name.
func CheckText(file, ext, text string, tree Tree, links map[string]bool) []Finding {
	c := &checker{file: file, tree: tree, links: links}
	fence := false
	for n, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		c.line = n + 1
		switch ext {
		case "sh":
			c.mentions(line)
		case "toml":
			c.spans(line)
		case "md":
			if reFence.MatchString(line) {
				fence = !fence
				continue
			}
			if fence {
				c.mentions(line)
				continue
			}
			if reIndented.MatchString(line) && !reListItem.MatchString(line) {
				c.mentions(line)
				continue
			}
			c.spans(line)
			for _, m := range reLink.FindAllString(reSpanAny.ReplaceAllString(line, ""), -1) {
				name := m[2 : len(m)-2]
				if !links[name] {
					c.miss(KindLink, name, "no .satelle/principles/"+name+".md or .satelle/skills/"+name+".md")
				}
			}
		}
	}
	return c.out
}

// Check walks dataDir's substrate directories (Dirs) for *.md, *.toml and
// *.sh files outside any testdata directory and checks each against tree.
// Link targets are the principles and skills on disk. Findings name files
// relative to dataDir's parent, slash-separated, sorted by file then line.
func Check(dataDir string, tree Tree) ([]Finding, error) {
	links := map[string]bool{}
	for _, d := range []string{"principles", "skills"} {
		ents, err := os.ReadDir(filepath.Join(dataDir, d))
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		for _, e := range ents {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
				links[strings.TrimSuffix(e.Name(), ".md")] = true
			}
		}
	}
	base := filepath.Dir(dataDir)
	var out []Finding
	for _, d := range Dirs {
		root := filepath.Join(dataDir, d)
		err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) && p == root {
					return nil
				}
				return err
			}
			if e.IsDir() {
				if e.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			ext := strings.TrimPrefix(filepath.Ext(p), ".")
			if ext != "md" && ext != "toml" && ext != "sh" {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(base, p)
			if err != nil {
				return err
			}
			out = append(out, CheckText(filepath.ToSlash(rel), ext, string(b), tree, links)...)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out, nil
}
