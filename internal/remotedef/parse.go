package remotedef

import (
	"fmt"
	"strings"
)

func Parse(data []byte) (Definition, error) {
	def, findings := check(data)
	if errs := Errors(findings); len(errs) > 0 {
		return Definition{}, fmt.Errorf("remote.toml is not a valid v1 definition: %s", FormatFindings(errs))
	}
	return def, nil
}

func FormatFindings(fs []Finding) string {
	parts := make([]string, len(fs))
	for i, f := range fs {
		parts[i] = f.Key + ": " + f.Message
	}
	return strings.Join(parts, "; ")
}
