package workflow

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func decodeTemp(t *testing.T, body string, v any) (string, []UnknownKey, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "sample.toml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	un, err := Decode(p, v)
	return p, un, err
}

func TestDecodeCleanReturnsNoUnknownKeys(t *testing.T) {
	type cfg struct {
		Name string `toml:"name"`
	}
	var got cfg
	_, un, err := decodeTemp(t, "name = \"ok\"\n", &got)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "ok" || len(un) != 0 {
		t.Fatalf("got %+v, unknown %+v", got, un)
	}
}

func TestDecodeCollectsLeavesTablesArraysAndFormatting(t *testing.T) {
	type itemEntry struct {
		Name string `toml:"name"`
	}
	type cfg struct {
		Known string      `toml:"known"`
		Items []itemEntry `toml:"items"`
	}
	body := `
known = "yes"
top_str = "hi"
top_bool = false
top_int = 42
top_float = 3.5
top_time = 2026-11-21T01:02:03Z
top_list = ["a", 2, true]
"dotted.key" = "q"

[extra]
a = 1
b = "two"

[extra.nested]
flag = true

[empty_table]

[inline_parent]
tbl = { z = 2, a = 1 }
empty_inline = {}

[[items]]
name = "one"
key_prefix = "sto"

[[items]]
name = "two"
key_prefix = "epi"
owner_tag = "ops"
`
	var got cfg
	p, un, err := decodeTemp(t, body, &got)
	if err != nil {
		t.Fatal(err)
	}
	if got.Known != "yes" || len(got.Items) != 2 || got.Items[0].Name != "one" || got.Items[1].Name != "two" {
		t.Fatalf("decoded known fields wrong: %+v", got)
	}
	want := []UnknownKey{
		{File: p, Table: "", Key: `"dotted.key"`, Value: `"q"`},
		{File: p, Table: "", Key: "empty_table", Value: "{}"},
		{File: p, Table: "", Key: "top_bool", Value: "false"},
		{File: p, Table: "", Key: "top_float", Value: "3.5"},
		{File: p, Table: "", Key: "top_int", Value: "42"},
		{File: p, Table: "", Key: "top_list", Value: `["a", 2, true]`},
		{File: p, Table: "", Key: "top_str", Value: `"hi"`},
		{File: p, Table: "", Key: "top_time", Value: "2026-11-21T01:02:03Z"},
		{File: p, Table: "extra", Key: "a", Value: "1"},
		{File: p, Table: "extra", Key: "b", Value: `"two"`},
		{File: p, Table: "extra.nested", Key: "flag", Value: "true"},
		{File: p, Table: "inline_parent", Key: "empty_inline", Value: "{}"},
		{File: p, Table: "inline_parent.tbl", Key: "a", Value: "1"},
		{File: p, Table: "inline_parent.tbl", Key: "z", Value: "2"},
		{File: p, Table: "items[0]", Key: "key_prefix", Value: `"sto"`},
		{File: p, Table: "items[1]", Key: "key_prefix", Value: `"epi"`},
		{File: p, Table: "items[1]", Key: "owner_tag", Value: `"ops"`},
	}
	if !slices.Equal(un, want) {
		t.Fatalf("unknown keys:\ngot:  %+v\nwant: %+v", un, want)
	}
	if s := (UnknownKey{File: ".satelle/init.toml", Key: "bogus", Value: "1"}).String(); s != ".satelle/init.toml: bogus = 1" {
		t.Errorf("top-level String() = %q", s)
	}
	if s := (UnknownKey{File: ".satelle/doctor.toml", Table: "items", Key: "key_prefix", Value: `"sto"`}).String(); s != `.satelle/doctor.toml: [items] key_prefix = "sto"` {
		t.Errorf("table String() = %q", s)
	}
}

func TestDecodeSingleArrayElementOmitsIndex(t *testing.T) {
	type itemEntry struct {
		Name string `toml:"name"`
	}
	type cfg struct {
		Items []itemEntry `toml:"items"`
	}
	var got cfg
	p, un, err := decodeTemp(t, "[[items]]\nname = \"only\"\nkey_prefix = \"sto\"\n", &got)
	if err != nil {
		t.Fatal(err)
	}
	want := []UnknownKey{{File: p, Table: "items", Key: "key_prefix", Value: `"sto"`}}
	if !slices.Equal(un, want) {
		t.Fatalf("got %+v, want %+v", un, want)
	}
}

func TestDecodeQuotedTableName(t *testing.T) {
	type cfg struct{}
	var got cfg
	p, un, err := decodeTemp(t, "[\"hook.extra\"]\nx = 1\n", &got)
	if err != nil {
		t.Fatal(err)
	}
	want := []UnknownKey{{File: p, Table: `"hook.extra"`, Key: "x", Value: "1"}}
	if !slices.Equal(un, want) {
		t.Fatalf("got %+v, want %+v", un, want)
	}
}

func TestDecodeRefusesSyntaxAndTypeErrors(t *testing.T) {
	type cfg struct {
		Count int `toml:"count"`
	}
	var got cfg
	if _, _, err := decodeTemp(t, "count = [broken\n", &got); err == nil || !strings.Contains(err.Error(), "sample.toml") {
		t.Errorf("syntax error: %v", err)
	}
	if _, _, err := decodeTemp(t, "count = \"not-an-int\"\n", &got); err == nil || !strings.Contains(err.Error(), "sample.toml") {
		t.Errorf("type error: %v", err)
	}
}
