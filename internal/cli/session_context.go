package cli

import (
	"fmt"
	"strings"

	"github.com/bobmcallan/satelle/internal/hook"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/workflow"
)

type sessionSource struct {
	Session  string
	Dir      string
	Settings kernel.Settings
	Harness  map[string]kernel.HarnessSettings
	Seats    func(session string) []seatView
	Invalid  error
}

type indexedContext struct {
	InlineBytes int
	Limit       int
	Entries     []indexEntry
}

type indexEntry struct {
	Path        string
	Description string
}

func injectIndex(r repo, inject []string) []indexEntry {
	entries := make([]indexEntry, 0, len(inject))
	for _, f := range inject {
		e := indexEntry{Path: f}
		if body, err := contextFile(r, f); err != nil {
			e.Description = fmt.Sprintf("unreadable: %v", err)
		} else {
			e.Description = docDescription(f, body)
		}
		entries = append(entries, e)
	}
	return entries
}

func framingLines(r repo, inject []string) string {
	var b strings.Builder
	for _, f := range inject {
		body, err := contextFile(r, f)
		if err != nil {
			continue
		}
		sk, err := workflow.ParseSkill(f, body)
		if err != nil || !sk.IsFraming() {
			continue
		}
		if d := strings.TrimSpace(sk.Meta["description"]); d != "" {
			b.WriteString(d)
			b.WriteString("\n")
		}
	}
	return b.String()
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

func overLimit(harness, text, notice string, limit, files int) bool {
	return limit > 0 && files > 0 && emittedBytes(harness, text, notice) > limit
}

func indexForm(preface string, entries []indexEntry, tree, harness string) string {
	var b strings.Builder
	b.WriteString(preface)
	fmt.Fprintf(&b, "\nRead each of these files in full before acting (paths are relative to %s); inlined they are over %s's configured session-start limit, so only their descriptions are printed here:\n", tree, harness)
	for _, e := range entries {
		if e.Description == "" {
			fmt.Fprintf(&b, "  %s\n", e.Path)
			continue
		}
		fmt.Fprintf(&b, "  %s — %s\n", e.Path, e.Description)
	}
	return b.String()
}
