package agy

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// initTools is init.tools from the captured stream (agy 1.5.14).
func initTools(t *testing.T) []string {
	t.Helper()
	line := strings.SplitN(string(fixture(t, "agy-stream.jsonl")), "\n", 2)[0]
	var e struct {
		Init struct {
			Tools []string `json:"tools"`
		} `json:"init"`
	}
	if err := json.Unmarshal([]byte(line), &e); err != nil || len(e.Init.Tools) < 60 {
		t.Fatalf("init tools %v: %v", e.Init.Tools, err)
	}
	return e.Init.Tools
}

func matcherMatches(t *testing.T, matcher, name string) bool {
	t.Helper()
	for _, b := range strings.Split(matcher, "|") {
		if regexp.MustCompile(b).MatchString(name) {
			return true
		}
	}
	return false
}

func TestComplementMatcher(t *testing.T) {
	const probe9 = `^$|^[^r].*$|^r$|^r[^e].*$|^re$|^re[^a].*$|^rea$|^rea[^d].*$|^read$|^read[^_].*$|^read_$|^read_[^f].*$|^read_f$|^read_f[^i].*$|^read_fi$|^read_fi[^l].*$|^read_fil$|^read_fil[^e].*$|^read_file.+$`
	if got := complementMatcher([]string{"read_file"}); got != probe9 {
		t.Fatalf("matcher for read_file:\n%s\nwant\n%s", got, probe9)
	}
	tools := initTools(t)
	for _, allowed := range [][]string{{"read_file"}, {"read_file", "write_file", "edit_file"}} {
		m := complementMatcher(allowed)
		if strings.ContainsAny(m, "()") {
			t.Errorf("matcher has a parenthesis: %s", m)
		}
		for _, a := range allowed {
			if matcherMatches(t, m, a) {
				t.Errorf("%v: the allowed %s is denied", allowed, a)
			}
		}
		for _, tool := range tools {
			if !slices.Contains(allowed, tool) && !matcherMatches(t, m, tool) {
				t.Errorf("%v: %s is not denied", allowed, tool)
			}
		}
		denied := []string{"", "r", "read_", "read_files", "READ_FILE", "x_read_file"}
		if len(allowed) > 1 {
			denied = append(denied, "edit_fil", "write_file2", "e", "w", "write_")
		}
		for _, name := range denied {
			if !matcherMatches(t, m, name) {
				t.Errorf("%v: %q is not denied", allowed, name)
			}
		}
	}
}

func TestGuardHooks(t *testing.T) {
	b, err := guardHooks([]string{"read_file"})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]map[string][]struct {
		Matcher string `json:"matcher"`
		Hooks   []struct {
			Type    string `json:"type"`
			Command string `json:"command"`
			Timeout int    `json:"timeout"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	pre := doc[guardOwner]["PreToolUse"]
	if len(doc) != 1 || len(doc[guardOwner]) != 1 || len(pre) != 1 || len(pre[0].Hooks) != 1 {
		t.Fatalf("guard hooks:\n%s", b)
	}
	h := pre[0].Hooks[0]
	if pre[0].Matcher != complementMatcher([]string{"read_file"}) || h.Type != "command" || h.Timeout != 10 ||
		!strings.Contains(h.Command, `{"decision":"deny","reason":"denied by satelle dispatch grant"}`) {
		t.Fatalf("guard hook:\n%s", b)
	}
}
