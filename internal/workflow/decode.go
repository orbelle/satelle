package workflow

import (
	"cmp"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

type UnknownKey struct {
	File  string `json:"file"`
	Table string `json:"table,omitempty"`
	Key   string `json:"key"`
	Value string `json:"value"`
}

func (u UnknownKey) String() string {
	if u.Table == "" {
		return fmt.Sprintf("%s: %s = %s", u.File, u.Key, u.Value)
	}
	return fmt.Sprintf("%s: [%s] %s = %s", u.File, u.Table, u.Key, u.Value)
}

func Decode(path string, v any) ([]UnknownKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return DecodeData(path, data, v)
}

func DecodeData(path string, data []byte, v any) ([]UnknownKey, error) {
	md, err := toml.Decode(string(data), v)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	un := md.Undecoded()
	if len(un) == 0 {
		return nil, nil
	}
	leaves := leafKeys(un)
	var raw map[string]any
	if _, err := toml.Decode(string(data), &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var out []UnknownKey
	for _, k := range leaves {
		out = append(out, collectUnknown(raw, nil, k, path)...)
	}
	slices.SortFunc(out, func(a, b UnknownKey) int {
		return cmp.Or(
			cmp.Compare(a.File, b.File),
			cmp.Compare(a.Table, b.Table),
			cmp.Compare(a.Key, b.Key),
			cmp.Compare(a.Value, b.Value),
		)
	})
	return slices.Compact(out), nil
}

func leafKeys(keys []toml.Key) []toml.Key {
	var out []toml.Key
	seen := map[string]bool{}
	for i, k := range keys {
		if len(k) == 0 {
			continue
		}
		prefix := false
		for j, other := range keys {
			if i != j && isStrictPrefix(k, other) {
				prefix = true
				break
			}
		}
		if prefix {
			continue
		}
		sig := keySignature(k)
		if seen[sig] {
			continue
		}
		seen[sig] = true
		out = append(out, k)
	}
	return out
}

func isStrictPrefix(a, b toml.Key) bool {
	if len(a) >= len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func keySignature(k toml.Key) string {
	parts := make([]string, len(k))
	for i, seg := range k {
		parts[i] = formatSegment(seg)
	}
	return strings.Join(parts, ".")
}

func collectUnknown(cur any, prefix []string, path toml.Key, file string) []UnknownKey {
	m, ok := cur.(map[string]any)
	if !ok || len(path) == 0 {
		return nil
	}
	val, exists := m[path[0]]
	if !exists {
		return nil
	}
	seg := formatSegment(path[0])
	if len(path) == 1 {
		return []UnknownKey{{
			File:  file,
			Table: strings.Join(prefix, "."),
			Key:   seg,
			Value: formatTOMLValue(val),
		}}
	}
	rest := path[1:]
	switch x := val.(type) {
	case map[string]any:
		return collectUnknown(x, appendSeg(prefix, seg), rest, file)
	case []map[string]any:
		if len(x) == 1 {
			return collectUnknown(x[0], appendSeg(prefix, seg), rest, file)
		}
		var out []UnknownKey
		for i, elem := range x {
			out = append(out, collectUnknown(elem, appendSeg(prefix, fmt.Sprintf("%s[%d]", seg, i)), rest, file)...)
		}
		return out
	case []any:
		if len(x) == 1 {
			return collectUnknown(x[0], appendSeg(prefix, seg), rest, file)
		}
		var out []UnknownKey
		for i, elem := range x {
			out = append(out, collectUnknown(elem, appendSeg(prefix, fmt.Sprintf("%s[%d]", seg, i)), rest, file)...)
		}
		return out
	}
	return nil
}

func appendSeg(prefix []string, seg string) []string {
	next := make([]string, len(prefix)+1)
	copy(next, prefix)
	next[len(prefix)] = seg
	return next
}

func isBareKey(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func formatSegment(s string) string {
	if isBareKey(s) {
		return s
	}
	return strconv.Quote(s)
}

func formatTOMLValue(v any) string {
	switch x := v.(type) {
	case string:
		return strconv.Quote(x)
	case bool:
		return strconv.FormatBool(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case int:
		return strconv.Itoa(x)
	case uint64:
		return strconv.FormatUint(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case time.Time:
		return x.Format(time.RFC3339Nano)
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = formatTOMLValue(e)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case []map[string]any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = formatTOMLValue(e)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		if len(x) == 0 {
			return "{}"
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = formatSegment(k) + " = " + formatTOMLValue(x[k])
		}
		return "{" + strings.Join(parts, ", ") + "}"
	default:
		return fmt.Sprint(v)
	}
}
