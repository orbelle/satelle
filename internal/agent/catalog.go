package agent

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/bobmcallan/satelle/internal/integration/common"
)

type Binding = common.Binding

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

func (c Catalog) Overlay(top Catalog) Catalog {
	if len(top) == 0 {
		return c
	}
	out := make(Catalog, len(c)+len(top))
	for n, b := range c {
		out[n] = b
	}
	for n, b := range top {
		out[n] = b
	}
	return out
}

func decodeAgents(path string) (map[string]Binding, toml.MetaData, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, toml.MetaData{}, fmt.Errorf("%s: %w", path, err)
	}
	return decodeAgentsData(path, data)
}

func decodeAgentsData(path string, data []byte) (map[string]Binding, toml.MetaData, error) {
	var file struct {
		Agents map[string]Binding `toml:"agents"`
	}
	md, err := toml.Decode(string(data), &file)
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
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return ParseCatalog(path, data)
}

func ParseCatalog(path string, data []byte) (Catalog, error) {
	agents, _, err := decodeAgentsData(path, data)
	if err != nil {
		return nil, err
	}
	c := Catalog{}
	for name, b := range agents {
		b.Name = name
		c[name] = b
	}
	return c, nil
}

func (c Catalog) Validate() []error {
	var errs []error
	for _, name := range c.Names() {
		for _, err := range Problems(c[name]) {
			errs = append(errs, fmt.Errorf("agents.%s: %w", name, err))
		}
		for _, err := range c.FallbackProblems(name) {
			errs = append(errs, fmt.Errorf("agents.%s: %w", name, err))
		}
	}
	return errs
}

func (c Catalog) FallbackProblems(name string) []error {
	f := c[name].Fallback
	switch {
	case f == "":
		return nil
	case f == name:
		return []error{fmt.Errorf("fallback names the agent itself")}
	case !c.Has(f):
		return []error{fmt.Errorf("fallback %q is not in the agents catalog", f)}
	case c[f].Fallback != "":
		return []error{fmt.Errorf("fallback %q names its own fallback %q; a fallback is one level", f, c[f].Fallback)}
	}
	return nil
}

func Problems(b Binding) []error {
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
	{"brief", func(d *Binding, s Binding) { d.Brief = s.Brief }},
	{"fallback", func(d *Binding, s Binding) { d.Fallback = s.Fallback }},
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

func ValidTool(t string) error {
	if slices.Contains(common.PlainTools, t) || common.IsScoped(t) {
		return nil
	}
	return fmt.Errorf("tool %q is not one of %s or Bash(<prefix>:*)", t, strings.Join(common.PlainTools, " "))
}

func IsReadOnly(grant []string) bool { return common.IsReadOnly(grant) }

func Preset(b Binding) string {
	bin, _, err := common.CommandFields(b)
	if err != nil {
		return ""
	}
	return common.Preset(bin)
}
