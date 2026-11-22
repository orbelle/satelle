package agy

import (
	"encoding/json"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

// complementMatcher returns a flat, |-separated list of anchored RE2
// branches, with no parentheses, that matches every tool name except the
// allowed ones. agy splits a PreToolUse matcher on | before it compiles each
// branch, so nested alternation would not match.
func complementMatcher(allowed []string) string {
	root := &trieNode{children: map[byte]*trieNode{}}
	for _, name := range allowed {
		n := root
		for i := 0; i < len(name); i++ {
			c := name[i]
			next, ok := n.children[c]
			if !ok {
				next = &trieNode{children: map[byte]*trieNode{}}
				n.children[c] = next
			}
			n = next
		}
		n.allowed = true
	}
	var branches []string
	var walk func(path string, n *trieNode)
	walk = func(path string, n *trieNode) {
		p := regexp.QuoteMeta(path)
		if !n.allowed {
			branches = append(branches, "^"+p+"$")
		}
		keys := make([]byte, 0, len(n.children))
		for c := range n.children {
			keys = append(keys, c)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
		if len(keys) == 0 {
			branches = append(branches, "^"+p+".+$")
			return
		}
		var class strings.Builder
		for _, c := range keys {
			class.WriteString(classChar(c))
		}
		branches = append(branches, "^"+p+"[^"+class.String()+"].*$")
		for _, c := range keys {
			walk(path+string(c), n.children[c])
		}
	}
	walk("", root)
	return strings.Join(branches, "|")
}

type trieNode struct {
	allowed  bool
	children map[byte]*trieNode
}

func classChar(c byte) string {
	switch c {
	case '\\', ']', '^', '-', '[':
		return `\` + string(c)
	}
	return string(c)
}

func denyCommand() string {
	body := `{"decision":"deny","reason":"` + denyReason + `"}`
	if runtime.GOOS == "windows" {
		return "cmd /c echo " + body
	}
	return "printf '" + body + "'"
}

// guardHooks is the hook file a run's guard directory carries: one PreToolUse
// entry that denies every tool outside tools. agy matches the matcher against
// the tool name only, so neither the prompt nor tool arguments reach it, and
// the command reads and interpolates nothing.
func guardHooks(tools []string) ([]byte, error) {
	doc := map[string]any{
		guardOwner: map[string]any{
			"PreToolUse": []any{map[string]any{
				"matcher": complementMatcher(tools),
				"hooks":   []any{map[string]any{"type": "command", "command": denyCommand(), "timeout": 10}},
			}},
		},
	}
	return json.MarshalIndent(doc, "", "  ")
}
