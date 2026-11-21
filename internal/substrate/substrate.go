package substrate

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/bobmcallan/satelle/internal/workflow"
)

const (
	KindCommand = "command"
	KindFlag    = "flag"
	KindTopic   = "topic"
	KindLink    = "link"
)

const Field = "substrate"

var Dirs = []string{"skills", "principles", "tasks", "workflows", "queues"}

type Finding struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Kind    string `json:"kind"`
	Subject string `json:"subject"`
	Why     string `json:"why"`
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d: %s: %s — %s", f.File, f.Line, f.Kind, f.Subject, f.Why)
}

type Tree struct {
	Subs     map[string]map[string]bool
	Flags    map[string]map[string]bool
	Topics   map[string]bool
	HelpPath string
}

func NewTree() Tree {
	return Tree{Subs: map[string]map[string]bool{}, Flags: map[string]map[string]bool{}, Topics: map[string]bool{}}
}

func (t Tree) AddSub(path, sub string) {
	if t.Subs[path] == nil {
		t.Subs[path] = map[string]bool{}
	}
	t.Subs[path][sub] = true
}

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

func notCommand(path, word string) string {
	if path == "" {
		return word + " is not a command of satelle"
	}
	return word + " is not a subcommand of satelle " + path
}

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

func (t Tree) Command(words []string) (why string, ok bool) {
	if len(words) == 0 {
		return "names no command", false
	}
	path := ""
	for _, w := range words {
		if !t.Subs[path][w] {
			return notCommand(path, w), false
		}
		path = strings.TrimSpace(path + " " + w)
	}
	return "", true
}

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

func (c *checker) check(rest string, quoted bool) {
	if loc := reCut.FindStringIndex(rest); loc != nil {
		rest = rest[:loc[0]]
	}
	path, words, descend := "", "satelle", true
	for _, t := range strings.FieldsFunc(rest, func(r rune) bool { return r == ' ' || r == '\t' }) {
		if reFlagStart.MatchString(t) {
			f := reFlag.FindString(t)
			if strings.HasSuffix(f, "-") {
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

func (c *checker) spans(s string) {
	for _, m := range reSpan.FindAllString(s, -1) {
		c.mentions(m[1 : len(m)-1])
	}
}

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
					c.miss(KindLink, name, "no .satelle/principles/"+name+".md, .satelle/skills/"+name+".md or .satelle/skills/"+name+"/"+workflow.SkillFile)
				}
			}
		}
	}
	return c.out
}

func Check(dataDir string, tree Tree) ([]Finding, error) {
	links := map[string]bool{}
	for _, d := range RuleDirs {
		ents, err := os.ReadDir(filepath.Join(dataDir, d))
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		for _, e := range ents {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
				links[strings.TrimSuffix(e.Name(), ".md")] = true
				continue
			}
			if d == "skills" && e.IsDir() && e.Name() != "testdata" {
				p := filepath.Join(dataDir, d, e.Name(), workflow.SkillFile)
				if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
					links[e.Name()] = true
				}
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
	refs, err := References(dataDir)
	if err != nil {
		return nil, err
	}
	for _, ref := range refs {
		for _, entry := range ref.Commands {
			if why, ok := tree.Command(strings.Fields(entry)); !ok {
				out = append(out, Finding{File: ref.Path, Line: ref.Line, Kind: KindCommand, Subject: strings.TrimSpace(binaryWord + " " + entry), Why: why})
			}
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
