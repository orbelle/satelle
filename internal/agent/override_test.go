package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func writeLocal(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "agents.local.toml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func trackedCatalog() Catalog {
	return Catalog{
		"grok":  {Name: "grok", Command: "grok", Model: "grok-4.7", Tools: []string{"Read", "Grep", "Glob"}},
		"coder": {Name: "coder", Command: "claude", Model: "opus", Tools: []string{"Read", "Edit", "Write"}},
	}
}

func TestOverrideMergesFieldByField(t *testing.T) {
	c := trackedCatalog()
	got, ovs, err := c.Override(writeLocal(t, "[agents.grok]\ncommand = \"cursor-agent\"\nmodel = \"gpt-5.6-sol-high\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	g := got["grok"]
	if g.Command != "cursor-agent" || g.Model != "gpt-5.6-sol-high" || !slices.Equal(g.Tools, []string{"Read", "Grep", "Glob"}) || g.Name != "grok" {
		t.Errorf("merged %+v, want the overriding command and model and the tracked tools", g)
	}
	if !reflect.DeepEqual(got["coder"], c["coder"]) {
		t.Errorf("an agent the file does not name changed: %+v", got["coder"])
	}
	if len(ovs) != 1 || ovs[0].Name != "grok" || !slices.Equal(ovs[0].Fields, []string{"command", "model"}) || ovs[0].Tracked.Command != "grok" {
		t.Errorf("overrides %+v", ovs)
	}
	if c["grok"].Command != "grok" {
		t.Error("Override changed the tracked catalog")
	}

	got, ovs, err = c.Override(writeLocal(t, "[agents.coder]\ntools = []\nmodel = \"\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if b := got["coder"]; len(b.Tools) != 0 || b.Model != "" || b.Command != "claude" {
		t.Errorf("an empty tools list and an empty model are overrides: %+v", b)
	}
	if !slices.Equal(ovs[0].Fields, []string{"model", "tools"}) {
		t.Errorf("fields %v, want [model tools] in schema order", ovs[0].Fields)
	}

	if _, ovs, err := c.Override(writeLocal(t, "[agents.coder]\n")); err != nil || len(ovs) != 0 {
		t.Errorf("a table with no keys: overrides %v, %v; want none", ovs, err)
	}
}

func TestOverridableCoversTheSchema(t *testing.T) {
	var keys []string
	ty := reflect.TypeOf(Binding{})
	for i := range ty.NumField() {
		if tag := ty.Field(i).Tag.Get("toml"); tag != "" && tag != "-" {
			keys = append(keys, tag)
		}
	}
	var table []string
	for _, f := range overridable {
		table = append(table, f.key)
	}
	if !slices.Equal(keys, table) {
		t.Errorf("Binding's toml keys %v and the overridable table %v differ", keys, table)
	}
}

func TestOverrideRefusesUnknownAgentsAndFields(t *testing.T) {
	c := trackedCatalog()
	_, _, err := c.Override(writeLocal(t, "[agents.ghost]\nmodel = \"m\"\n"))
	if err == nil || !strings.Contains(err.Error(), "agents.ghost") || !strings.Contains(err.Error(), "defines no such agent") {
		t.Errorf("an unknown agent: %v", err)
	}
	_, _, err = c.Override(writeLocal(t, "[agents.grok]\ncolour = 1\n"))
	if err == nil || !strings.Contains(err.Error(), "unknown key agents.grok.colour") {
		t.Errorf("an unknown field: %v", err)
	}
	_, _, err = c.Override(writeLocal(t, "stray = 1\n"))
	if err == nil || !strings.Contains(err.Error(), "unknown key stray") {
		t.Errorf("a stray top-level key: %v", err)
	}
}

func TestOverrideBrief(t *testing.T) {
	c := trackedCatalog()
	g := c["grok"]
	g.Brief = "tracked brief"
	c["grok"] = g

	got, ovs, err := c.Override(writeLocal(t, "[agents.grok]\nbrief = \"local brief\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got["grok"].Brief != "local brief" {
		t.Errorf("brief = %q, want local brief", got["grok"].Brief)
	}
	if len(ovs) != 1 || ovs[0].Name != "grok" || !slices.Equal(ovs[0].Fields, []string{"brief"}) || ovs[0].Tracked.Brief != "tracked brief" {
		t.Errorf("overrides = %+v", ovs)
	}
	if c["grok"].Brief != "tracked brief" {
		t.Error("Override mutated the tracked catalog")
	}

	got, ovs, err = c.Override(writeLocal(t, "[agents.grok]\nbrief = \"\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got["grok"].Brief != "" || len(ovs) != 1 || !slices.Equal(ovs[0].Fields, []string{"brief"}) {
		t.Errorf("cleared brief = %q, overrides = %+v", got["grok"].Brief, ovs)
	}

	got, _, err = c.Override(writeLocal(t, "[agents.grok]\nmodel = \"other\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got["grok"].Brief != "tracked brief" {
		t.Errorf("omitted brief = %q, want tracked brief preserved", got["grok"].Brief)
	}
}
