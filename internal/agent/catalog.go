package agent

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

type Binding struct {
	Name      string   `toml:"-"`
	Command   string   `toml:"command"`
	Model     string   `toml:"model"`
	Effort    string   `toml:"effort"`
	Tools     []string `toml:"tools"`
	Deny      []string `toml:"deny"`
	Placement string   `toml:"placement"`
}

type Catalog map[string]Binding

func (c Catalog) Has(name string) bool { _, ok := c[name]; return ok }

func (c Catalog) Names() []string {
	out := make([]string, 0, len(c))
	for n := range c {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func decodeAgents(path string) (map[string]Binding, toml.MetaData, error) {
	var file struct {
		Agents map[string]Binding `toml:"agents"`
	}
	md, err := toml.DecodeFile(path, &file)
	if err != nil {
		return nil, md, fmt.Errorf("%s: %w", path, err)
	}
	if un := md.Undecoded(); len(un) > 0 {
		keys := make([]string, len(un))
		for i, k := range un {
			keys[i] = k.String()
		}
		return nil, md, fmt.Errorf("%s: unknown key %s", path, strings.Join(keys, ", "))
	}
	return file.Agents, md, nil
}

func LoadCatalog(path string) (Catalog, error) {
	agents, _, err := decodeAgents(path)
	if err != nil {
		return nil, err
	}
	file := struct{ Agents map[string]Binding }{agents}
	c := Catalog{}
	for name, b := range file.Agents {
		b.Name = name
		c[name] = b
	}
	return c, nil
}

func (c Catalog) Validate() []error {
	var errs []error
	for _, name := range c.Names() {
		for _, err := range c[name].Problems() {
			errs = append(errs, fmt.Errorf("agents.%s: %w", name, err))
		}
	}
	return errs
}

func (b Binding) Problems() []error {
	var errs []error
	if a, err := AdapterFor(b); err != nil {
		errs = append(errs, err)
	} else if !IsSession(b) {
		if _, err := a.Argv(Request{Binding: b}); err != nil {
			errs = append(errs, err)
		}
	}
	for _, t := range append(slices.Clone(b.Tools), b.Deny...) {
		if err := ValidTool(t); err != nil {
			errs = append(errs, err)
		}
	}
	if err := validPlacement(b.Placement); err != nil {
		errs = append(errs, err)
	}
	return errs
}

type Override struct {
	Name    string
	Fields  []string
	Tracked Binding
}

var overridable = []struct {
	key  string
	copy func(dst *Binding, src Binding)
}{
	{"command", func(d *Binding, s Binding) { d.Command = s.Command }},
	{"model", func(d *Binding, s Binding) { d.Model = s.Model }},
	{"effort", func(d *Binding, s Binding) { d.Effort = s.Effort }},
	{"tools", func(d *Binding, s Binding) { d.Tools = slices.Clone(s.Tools) }},
	{"deny", func(d *Binding, s Binding) { d.Deny = slices.Clone(s.Deny) }},
	{"placement", func(d *Binding, s Binding) { d.Placement = s.Placement }},
}

func (c Catalog) Override(path string) (Catalog, []Override, error) {
	local, md, err := decodeAgents(path)
	if err != nil {
		return nil, nil, err
	}
	var errs []error
	names := make([]string, 0, len(local))
	for name := range local {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !c.Has(name) {
			errs = append(errs, fmt.Errorf("agents.%s: agents.toml defines no such agent (it defines: %s)", name, strings.Join(c.Names(), ", ")))
		}
	}
	if len(errs) > 0 {
		return nil, nil, errors.Join(errs...)
	}
	out := make(Catalog, len(c))
	for n, b := range c {
		out[n] = b
	}
	var ovs []Override
	for _, name := range names {
		tracked := c[name]
		merged := tracked
		var fields []string
		for _, f := range overridable {
			if md.IsDefined("agents", name, f.key) {
				f.copy(&merged, local[name])
				fields = append(fields, f.key)
			}
		}
		if len(fields) == 0 {
			continue
		}
		out[name] = merged
		ovs = append(ovs, Override{Name: name, Fields: fields, Tracked: tracked})
	}
	return out, ovs, nil
}

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

func ValidTool(t string) error {
	if slices.Contains(plainTools, t) || scopedBash.MatchString(t) {
		return nil
	}
	return fmt.Errorf("tool %q is not one of %s or Bash(<prefix>:*)", t, strings.Join(plainTools, " "))
}

func IsScoped(t string) bool { return scopedBash.MatchString(t) }

func IsReadOnly(grant []string) bool {
	for _, t := range grant {
		if !slices.Contains(readTools, t) {
			return false
		}
	}
	return true
}

func grantsBash(grant []string) bool {
	return slices.ContainsFunc(grant, func(t string) bool { return t == ToolBash || IsScoped(t) })
}

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

func commandFields(b Binding) (bin string, args []string, err error) {
	f := strings.Fields(b.Command)
	if len(f) == 0 {
		return "", nil, fmt.Errorf("binding %q has no command", b.Name)
	}
	return f[0], f[1:], nil
}

func Preset(b Binding) string {
	bin, _, err := commandFields(b)
	if err != nil {
		return ""
	}
	return preset(bin)
}

func preset(bin string) string {
	return strings.TrimSuffix(strings.ToLower(filepath.Base(bin)), ".exe")
}
