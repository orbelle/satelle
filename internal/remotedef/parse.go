package remotedef

import (
	"fmt"
	"strings"
)

// Parse reads a schema v1 remote.toml and refuses it when Validate finds any
// error: the node acts on no definition the server would refuse, and a
// definition in the earlier shape is refused naming each old key's v1
// replacement. v1 is closed (the server's schema is additionalProperties false),
// so a key v1 does not carry is a finding too; a schema change bumps version.
// The error lists every error finding as "<key>: <message>", in check order,
// and never carries a submitted value.
func Parse(data []byte) (Definition, error) {
	def, findings := check(data)
	if errs := Errors(findings); len(errs) > 0 {
		return Definition{}, fmt.Errorf("remote.toml is not a valid v1 definition: %s", FormatFindings(errs))
	}
	return def, nil
}

// FormatFindings is fs as "<key>: <message>" joined by "; ".
func FormatFindings(fs []Finding) string {
	parts := make([]string, len(fs))
	for i, f := range fs {
		parts[i] = f.Key + ": " + f.Message
	}
	return strings.Join(parts, "; ")
}
