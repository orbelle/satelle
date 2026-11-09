package hook

import (
	"errors"
	"fmt"
	"strings"
)

// The [hook] allow setting lists command prefixes the gate hook lets an
// interactive session run without its harness's own approval. Which commands,
// and whether any, is the repo's configuration: this file only parses an entry
// and matches a command against the entries it is handed.

// ParseAllow splits one [hook] allow entry into its words. An entry is plain
// words (letters, digits and . _ / : = -), so it can name a command prefix and
// never a glob, quote, substitution, redirect or command separator.
func ParseAllow(entry string) ([]string, error) {
	words := strings.Fields(entry)
	if len(words) == 0 {
		return nil, errors.New("empty entry")
	}
	for _, w := range words {
		if !plainWord(w, false) {
			return nil, fmt.Errorf("word %q is not plain (letters, digits and . _ / : = - only)", w)
		}
	}
	return words, nil
}

// Allowed reports whether command is one the entries allow: every command
// segment (split at ; && || | and newlines) is plain unquoted words whose
// leading words are some entry's words. A quote, substitution, redirect,
// glob, leading assignment or wrapper makes it false, as does an invalid
// entry; no entries allow nothing.
func Allowed(entries []string, command string) bool {
	var prefixes [][]string
	for _, e := range entries {
		if words, err := ParseAllow(e); err == nil {
			prefixes = append(prefixes, words)
		}
	}
	if len(prefixes) == 0 || strings.TrimSpace(command) == "" {
		return false
	}
	toks := tokenizeBash(command)
	for _, t := range toks {
		if t.Kind == "op" {
			continue
		}
		if t.Kind != "word" || !plainWord(t.Value, true) {
			return false
		}
	}
	segs := splitSegments(toks)
	if len(segs) == 0 {
		return false
	}
	for _, s := range segs {
		words := wordsOnly(s.Toks)
		if len(words) != len(s.Toks) || !hasPrefix(words, prefixes) {
			return false
		}
	}
	return true
}

func hasPrefix(words []string, prefixes [][]string) bool {
	for _, p := range prefixes {
		if len(words) < len(p) {
			continue
		}
		match := true
		for i := range p {
			if words[i] != p[i] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// plainWord reports a word of letters, digits and . _ / : = - (and, in a
// command's arguments, , @ + %). Nothing a shell expands or redirects.
func plainWord(w string, arg bool) bool {
	if w == "" {
		return false
	}
	for _, r := range w {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("._/:=-", r):
		case arg && strings.ContainsRune(",@+%", r):
		default:
			return false
		}
	}
	return true
}
