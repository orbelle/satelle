// Package ledgerstore is satelle's client of the ledger store adapter
// contract: the [ledger] store setting, the external store command that
// implements put-if-absent, get and list, the key and body limits, and the
// conformance checks `satelle ledger store-test` runs. `satelle help
// ledger-store` is the contract's text.
package ledgerstore

import (
	"fmt"
	"sort"
	"strings"
)

// The built-in store kinds a string value of [ledger] store may name. They are
// satelle's own stores, not vendors; any other store is an external command.
const (
	KindSatelleDev = "satelle.dev"
	KindLocal      = "local"
)

// Spec is the [ledger] store value: a built-in kind (a string) or an external
// store command (a table with command and args). The zero value is "not
// configured".
type Spec struct {
	Kind    string
	Command string
	Args    []string
}

// External reports whether the spec names an external store command, the only
// form that implements the adapter operations.
func (s Spec) External() bool { return s.Command != "" }

// UnmarshalTOML reads a string naming a built-in kind, or a table
// { command = "...", args = ["..."] }. Any other value, an unknown table key,
// or an empty command is refused, naming it.
func (s *Spec) UnmarshalTOML(v any) error {
	*s = Spec{}
	switch x := v.(type) {
	case string:
		if x != KindSatelleDev && x != KindLocal {
			return fmt.Errorf("[ledger] store = %q: a string names a built-in store, %q or %q; any other store is a command: store = { command = \"...\", args = [\"...\"] }",
				x, KindSatelleDev, KindLocal)
		}
		s.Kind = x
		return nil
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			switch k {
			case "command":
				c, ok := x[k].(string)
				if !ok {
					return fmt.Errorf("[ledger] store: command must be a string, not %T", x[k])
				}
				s.Command = c
			case "args":
				list, ok := x[k].([]any)
				if !ok {
					return fmt.Errorf("[ledger] store: args must be an array of strings, not %T", x[k])
				}
				for _, e := range list {
					a, ok := e.(string)
					if !ok {
						return fmt.Errorf("[ledger] store: args must be an array of strings; %v is %T", e, e)
					}
					s.Args = append(s.Args, a)
				}
			default:
				return fmt.Errorf("[ledger] store: unknown key %s (a store command takes command and args)", k)
			}
		}
		if strings.TrimSpace(s.Command) == "" {
			*s = Spec{}
			return fmt.Errorf("[ledger] store: command is required and must not be empty")
		}
		return nil
	}
	return fmt.Errorf("[ledger] store: %v is neither a built-in store name (%q, %q) nor a command table { command = \"...\", args = [\"...\"] }",
		v, KindSatelleDev, KindLocal)
}
