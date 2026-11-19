package substrate

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/bobmcallan/satelle/internal/workflow"
)

var RuleDirs = []string{"principles", "skills"}

const (
	commandsKey = "commands"
	binaryWord  = "satelle"
)

type Reference struct {
	Name        string
	Description string
	Path        string
	Line        int
	Commands    []string
}

func SplitCommands(value string) []string {
	v := strings.TrimSpace(value)
	v = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(v, "["), "]"))
	if v == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(v, ",") {
		words := strings.Fields(strings.Trim(strings.TrimSpace(part), "\"'"))
		if len(words) > 0 && words[0] == binaryWord {
			words = words[1:]
		}
		out = append(out, strings.Join(words, " "))
	}
	return out
}

func References(dataDir string) ([]Reference, error) {
	base := filepath.Dir(dataDir)
	var out []Reference
	for _, d := range RuleDirs {
		ents, err := os.ReadDir(filepath.Join(dataDir, d))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, e := range ents {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			p := filepath.Join(dataDir, d, e.Name())
			b, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			stem := strings.TrimSuffix(e.Name(), ".md")
			sk, err := workflow.ParseSkill(stem, string(b))
			if err != nil {
				continue
			}
			commands := SplitCommands(sk.Meta[commandsKey])
			if len(commands) == 0 {
				continue
			}
			rel, err := filepath.Rel(base, p)
			if err != nil {
				continue
			}
			name := sk.Meta["name"]
			if name == "" {
				name = stem
			}
			out = append(out, Reference{Name: name, Description: sk.Meta["description"], Path: filepath.ToSlash(rel), Line: sk.Lines[commandsKey], Commands: commands})
		}
	}
	return out, nil
}
