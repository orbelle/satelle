package help

import (
	"embed"
	"io/fs"
	"path"
	"sort"
	"strings"
)

//go:embed topics
var topicsFS embed.FS

type Topic struct {
	Name  string
	Title string
	Body  string
}

func List() []Topic {
	var out []Topic
	entries, _ := fs.ReadDir(topicsFS, "topics")
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(path.Ext(e.Name()), ".md") {
			continue
		}
		body, err := topicsFS.ReadFile(path.Join("topics", e.Name()))
		if err != nil {
			continue
		}
		name := strings.TrimSuffix(e.Name(), path.Ext(e.Name()))
		out = append(out, Topic{Name: name, Title: title(string(body)), Body: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func Get(name string) (Topic, bool) {
	for _, t := range List() {
		if t.Name == name {
			return t, true
		}
	}
	return Topic{}, false
}

func title(body string) string {
	for _, ln := range strings.Split(body, "\n") {
		t := strings.TrimSpace(ln)
		if t == "" {
			continue
		}
		return strings.TrimSpace(strings.TrimLeft(t, "#"))
	}
	return ""
}
