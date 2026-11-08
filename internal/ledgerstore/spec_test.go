package ledgerstore

import (
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func decode(t *testing.T, src string) (Spec, error) {
	t.Helper()
	var v struct {
		Store Spec `toml:"store"`
	}
	md, err := toml.Decode(src, &v)
	if err == nil && len(md.Undecoded()) > 0 {
		t.Errorf("undecoded keys %v", md.Undecoded())
	}
	return v.Store, err
}

func TestSpecForms(t *testing.T) {
	s, err := decode(t, `store = { command = "satelle-store-azure", args = ["--account", "a"] }`)
	if err != nil || s.Command != "satelle-store-azure" || !slices.Equal(s.Args, []string{"--account", "a"}) || !s.External() {
		t.Fatalf("table: %+v %v", s, err)
	}
	s, err = decode(t, `store = { command = "x" }`)
	if err != nil || s.Command != "x" || s.Args != nil {
		t.Fatalf("table without args: %+v %v", s, err)
	}
	for _, k := range []string{KindSatelleDev, KindLocal} {
		s, err := decode(t, `store = "`+k+`"`)
		if err != nil || s.Kind != k || s.External() {
			t.Errorf("%s: %+v %v", k, s, err)
		}
	}
	if s, err := decode(t, ``); err != nil || s.External() || s.Kind != "" {
		t.Errorf("absent: %+v %v", s, err)
	}
}

func TestSpecRefusals(t *testing.T) {
	for src, want := range map[string]string{
		`store = "s3"`: `"satelle.dev" or "local"`,
		`store = { command = "x", bucket = "b" }`: "unknown key bucket",
		`store = { command = "" }`:                "command is required",
		`store = { args = ["a"] }`:                "command is required",
		`store = { command = 3 }`:                 "command must be a string",
		`store = { command = "x", args = "a" }`:   "args must be an array of strings",
		`store = { command = "x", args = [1] }`:   "args must be an array of strings",
		`store = 7`:                               "neither a built-in store name",
	} {
		if _, err := decode(t, src); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", src, err, want)
		}
	}
}
