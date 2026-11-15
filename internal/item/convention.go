package item

import (
	"slices"
	"strings"
)

var ChipColours = []string{"fail", "warn", "ok", "accent", "muted", "note"}

type Convention struct {
	Key     string
	Values  []string
	Colours map[string]string
	Colour  string
}

var Conventions = []Convention{
	{Key: "priority", Values: []string{"high", "medium", "low"}, Colours: map[string]string{"high": "fail", "medium": "warn", "low": "ok"}},
	{Key: "surface", Colour: "accent"},
	{Key: "reporter", Colour: "muted"},
	{Key: "source", Colour: "note"},
}

func TagKV(tag string) (key, value string, ok bool) {
	i := strings.IndexByte(tag, ':')
	if i <= 0 || i >= len(tag)-1 {
		return "", "", false
	}
	return tag[:i], tag[i+1:], true
}

func conventionOf(key string) (Convention, bool) {
	for _, c := range Conventions {
		if c.Key == key {
			return c, true
		}
	}
	return Convention{}, false
}

func ChipColour(tag string, overrides map[string]string) string {
	if c := overrides[tag]; c != "" {
		return c
	}
	key, value, ok := TagKV(tag)
	if !ok {
		return ""
	}
	if c := overrides[key]; c != "" {
		return c
	}
	conv, ok := conventionOf(key)
	if !ok {
		return ""
	}
	if c := conv.Colours[value]; c != "" {
		return c
	}
	return conv.Colour
}

func FacetKeys() []string {
	var out []string
	for _, c := range Conventions {
		if len(c.Values) > 0 {
			out = append(out, c.Key)
		}
	}
	return out
}

func OutsideValues(tag string) (named []string, outside bool) {
	key, value, ok := TagKV(tag)
	if !ok {
		return nil, false
	}
	conv, ok := conventionOf(key)
	if !ok || len(conv.Values) == 0 || slices.Contains(conv.Values, value) {
		return nil, false
	}
	named = make([]string, len(conv.Values))
	for i, v := range conv.Values {
		named[i] = key + ":" + v
	}
	return named, true
}
