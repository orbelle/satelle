package cli

import (
	"fmt"
	"strings"

	"github.com/bobmcallan/satelle/internal/hook"
	"github.com/bobmcallan/satelle/internal/workflow"
)

const (
	contextInline = "inline"
	contextIndex  = "index"
)

type injectDoc struct {
	Path        string
	Description string
	body        string
	err         error
}

func loadInjectDocs(r repo, inject []string) []injectDoc {
	docs := make([]injectDoc, 0, len(inject))
	for _, f := range inject {
		d := injectDoc{Path: f}
		d.body, d.err = contextFile(r, f)
		if d.err != nil {
			d.Description = fmt.Sprintf("unreadable: %v", d.err)
		} else {
			d.Description = docDescription(f, d.body)
		}
		docs = append(docs, d)
	}
	return docs
}

func docDescription(path, text string) string {
	sk, _ := workflow.ParseSkill(path, text)
	if d := strings.TrimSpace(sk.Meta["description"]); d != "" {
		return d
	}
	for _, line := range strings.Split(sk.Body, "\n") {
		if t := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#")); t != "" {
			return t
		}
	}
	return ""
}

func emittedBytes(harness, text, notice string) int {
	return len(hook.Context(harness, hook.EventSessionStart, text, notice)) + 1
}

func sessionStartText(head, block string, docs []injectDoc, tree, harness, notice string, limit int) (text, form string) {
	var inline strings.Builder
	inline.WriteString(head)
	inline.WriteString(block)
	for _, d := range docs {
		if d.err != nil {
			fmt.Fprintf(&inline, "\nsatelle: [hook] inject file %s: %v\n", d.Path, d.err)
			continue
		}
		fmt.Fprintf(&inline, "\n%s\n", d.body)
	}
	if limit <= 0 || len(docs) == 0 || emittedBytes(harness, inline.String(), notice) <= limit {
		return inline.String(), contextInline
	}
	var index strings.Builder
	index.WriteString(head)
	index.WriteString(block)
	fmt.Fprintf(&index, "\nRead each of these files in full before acting (paths are relative to %s); inlined they are over %s's configured session-start limit, so only their descriptions are printed here:\n", tree, harness)
	for _, d := range docs {
		if d.Description == "" {
			fmt.Fprintf(&index, "  %s\n", d.Path)
			continue
		}
		fmt.Fprintf(&index, "  %s — %s\n", d.Path, d.Description)
	}
	return index.String(), contextIndex
}
