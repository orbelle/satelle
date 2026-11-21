package hook

import (
	"os"
	"slices"
	"strings"

	"github.com/bobmcallan/satelle/internal/integration"
)

func CallerFrom(environ []string) string {
	set := map[string]bool{}
	adapters := integration.Hooks()
	for _, kv := range environ {
		name, value, _ := strings.Cut(kv, "=")
		if strings.TrimSpace(value) == "" {
			continue
		}
		for _, a := range adapters {
			names, prefixes := a.CallerMarkers()
			if slices.Contains(names, name) || slices.ContainsFunc(prefixes, func(p string) bool { return strings.HasPrefix(name, p) }) {
				set[a.Name()] = true
				break
			}
		}
	}
	found, fallback := "", ""
	for _, a := range adapters {
		if a.Fallback() {
			fallback = a.Name()
			continue
		}
		if !set[a.Name()] {
			continue
		}
		if found != "" {
			return ""
		}
		found = a.Name()
	}
	if found == "" && fallback != "" && set[fallback] {
		return fallback
	}
	return found
}

func Caller() string { return CallerFrom(os.Environ()) }

func CallerMarkers() (names, prefixes []string) {
	for _, a := range integration.Hooks() {
		n, p := a.CallerMarkers()
		names = append(names, n...)
		prefixes = append(prefixes, p...)
	}
	return names, prefixes
}
