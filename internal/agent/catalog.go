// Package agent is the agents catalog (agents.toml), satelle's tool-grant
// vocabulary, and the thin adapters that run a binding: claude, pi, grok and
// cursor-agent, plus session (the interactive session performs; nothing is dispatched).
package agent

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Binding is one [agents.<name>] entry.
type Binding struct {
	Name      string   `toml:"-"`
	Command   string   `toml:"command"`
	Model     string   `toml:"model"`
	Effort    string   `toml:"effort"`
	Tools     []string `toml:"tools"`
	Deny      []string `toml:"deny"`
	Placement string   `toml:"placement"`
}

// Catalog maps binding names to bindings.
type Catalog map[string]Binding

// Has reports whether name is in the catalog.
func (c Catalog) Has(name string) bool { _, ok := c[name]; return ok }

// Names returns the binding names, sorted.
func (c Catalog) Names() []string {
	out := make([]string, 0, len(c))
	for n := range c {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// LoadCatalog reads agents.toml strictly.
func LoadCatalog(path string) (Catalog, error) {
	var file struct {
		Agents map[string]Binding `toml:"agents"`
	}
	md, err := toml.DecodeFile(path, &file)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if un := md.Undecoded(); len(un) > 0 {
		keys := make([]string, len(un))
		for i, k := range un {
			keys[i] = k.String()
		}
		return nil, fmt.Errorf("%s: unknown key %s", path, strings.Join(keys, ", "))
	}
	c := Catalog{}
	for name, b := range file.Agents {
		b.Name = name
		c[name] = b
	}
	return c, nil
}

// Validate returns every problem with the catalog's bindings.
func (c Catalog) Validate() []error {
	var errs []error
	for _, name := range c.Names() {
		b := c[name]
		if _, err := AdapterFor(b); err != nil {
			errs = append(errs, fmt.Errorf("agents.%s: %w", name, err))
		}
		for _, t := range append(slices.Clone(b.Tools), b.Deny...) {
			if err := ValidTool(t); err != nil {
				errs = append(errs, fmt.Errorf("agents.%s: %w", name, err))
			}
		}
		if err := validPlacement(b.Placement); err != nil {
			errs = append(errs, fmt.Errorf("agents.%s: %w", name, err))
		}
	}
	return errs
}

// Placement values.
const (
	PlaceLocal  = "local"
	PlaceCloud  = "cloud"
	PlaceRemote = "remote:"
)

func validPlacement(p string) error {
	switch {
	case p == "" || p == PlaceLocal || p == PlaceCloud:
		return nil
	case strings.HasPrefix(p, PlaceRemote) && len(p) > len(PlaceRemote):
		return nil
	}
	return fmt.Errorf("placement %q is not %s, %s or %s<id>", p, PlaceLocal, PlaceCloud, PlaceRemote)
}

// The grant vocabulary: satelle's names for tools, translated by each adapter.
const (
	ToolRead         = "Read"
	ToolGrep         = "Grep"
	ToolGlob         = "Glob"
	ToolEdit         = "Edit"
	ToolWrite        = "Write"
	ToolNotebookEdit = "NotebookEdit"
	ToolBash         = "Bash"
)

var (
	plainTools = []string{ToolRead, ToolGrep, ToolGlob, ToolEdit, ToolWrite, ToolNotebookEdit, ToolBash}
	readTools  = []string{ToolRead, ToolGrep, ToolGlob}
	scopedBash = regexp.MustCompile(`^Bash\(([^()]+):\*\)$`)
)

// ValidTool checks one grant or deny entry against the vocabulary.
func ValidTool(t string) error {
	if slices.Contains(plainTools, t) || scopedBash.MatchString(t) {
		return nil
	}
	return fmt.Errorf("tool %q is not one of %s or Bash(<prefix>:*)", t, strings.Join(plainTools, " "))
}

// IsScoped reports whether t is a Bash(<prefix>:*) entry.
func IsScoped(t string) bool { return scopedBash.MatchString(t) }

// IsReadOnly reports whether a grant names only read tools. An empty grant is
// read-only.
func IsReadOnly(grant []string) bool {
	for _, t := range grant {
		if !slices.Contains(readTools, t) {
			return false
		}
	}
	return true
}

// grantsBash reports whether the grant can run any shell command.
func grantsBash(grant []string) bool {
	return slices.ContainsFunc(grant, func(t string) bool { return t == ToolBash || IsScoped(t) })
}

// splitDenies drops scoped denies that cannot apply (the grant runs no shell)
// and splits the rest into plain tool names and scoped Bash patterns.
func splitDenies(grant, denies []string) (plain, scoped []string) {
	for _, d := range denies {
		switch {
		case IsScoped(d):
			if grantsBash(grant) && !slices.Contains(scoped, d) {
				scoped = append(scoped, d)
			}
		case !slices.Contains(plain, d):
			plain = append(plain, d)
		}
	}
	return plain, scoped
}

// commandFields splits a binding's command into its binary and extra args.
func commandFields(b Binding) (bin string, args []string, err error) {
	f := strings.Fields(b.Command)
	if len(f) == 0 {
		return "", nil, fmt.Errorf("binding %q has no command", b.Name)
	}
	return f[0], f[1:], nil
}

// preset returns the adapter preset a command's first token names.
func preset(bin string) string {
	return strings.TrimSuffix(strings.ToLower(filepath.Base(bin)), ".exe")
}
