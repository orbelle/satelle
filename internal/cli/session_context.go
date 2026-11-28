package cli

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bobmcallan/satelle/internal/hook"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/reporeg"
	"github.com/bobmcallan/satelle/internal/workflow"
)

type sessionSource struct {
	Session    string
	Dir        string
	Settings   kernel.Settings
	Harness    map[string]kernel.HarnessSettings
	Seats      func(session string) []seatView
	Inject     []string
	InjectNote string
	Invalid    error
	NoDoctor   bool
}

func (k *sessionSource) injectFiles() []string {
	if k.Inject != nil {
		return k.Inject
	}
	return k.Settings.Hook.Inject
}

const injectNotePrefix = "satelle: [hook] inject: "

func noInjectNote(cause string) string {
	return injectNotePrefix + "no files are given: " + cause
}

func mixedSeatsNote(targets []string) string {
	t := slices.Clone(targets)
	slices.Sort(t)
	t = slices.Compact(t)
	return fmt.Sprintf("%sthis session's seats target several repositories (%s), so the shared files are given", injectNotePrefix, reporeg.Literals(t))
}

func partInjectNote(dataDir, x string) string {
	return fmt.Sprintf("%srepository %s's part names its own files in %s/%s/%s/%s; they are given in place of the shared [hook] inject files",
		injectNotePrefix, reporeg.Literal(x), filepath.Base(dataDir), kernel.PartsDir, reporeg.Literal(x), kernel.SettingsFile)
}

func seatedInject(k *kernel.Kernel, r repo, session string) (files []string, note string) {
	if session == "" {
		return nil, ""
	}
	seats, err := k.Cache.Seats(session, "")
	if err != nil {
		return []string{}, noInjectNote("this session's seats cannot be read: " + reporeg.Literal(err.Error()))
	}
	var items []item.Item
	for _, s := range seats {
		it, ok, err := k.Cache.Item(s.Item)
		if err != nil {
			return []string{}, noInjectNote("this session's seats cannot be read: " + reporeg.Literal(err.Error()))
		}
		if ok {
			items = append(items, it)
		}
	}
	var targets []string
	var owner string
	for _, it := range items {
		f, err := k.InjectFor(it)
		if err != nil {
			return []string{}, noInjectNote(reporeg.Literal(err.Error()))
		}
		if f != nil {
			files, owner = f, it.Target()
		}
		targets = append(targets, it.Target())
	}
	if files == nil {
		return nil, ""
	}
	if slices.ContainsFunc(targets, func(t string) bool { return t != owner }) {
		return nil, mixedSeatsNote(targets)
	}
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f
		if rel, err := filepath.Rel(r.Tree, f); err == nil && filepath.IsLocal(rel) {
			out[i] = rel
		}
	}
	return out, partInjectNote(r.DataDir, owner)
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
		if d := strings.TrimSpace(sk.Meta[workflow.KeyDescription]); d != "" {
			b.WriteString(d)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func docDescription(path, text string) string {
	sk, _ := workflow.ParseSkill(path, text)
	if d := strings.TrimSpace(sk.Meta[workflow.KeyDescription]); d != "" {
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
