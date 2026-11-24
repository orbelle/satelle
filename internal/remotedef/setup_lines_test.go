package remotedef

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSetupLinesReadsOnlySetupCommandsWithTheirLines(t *testing.T) {
	body := `# [setup] on_create = ["not me"]
version = 1
note = "on_create = [\"nor me\"]"

[connect]
argv = ["ssh", ".satelle/z"]

[setup]
on_create = [
  "test -f .satelle/satelle.toml", # trailing comment
  'cat C:\path',

  "printf \"%s\\n\" \u0041 x",
]
post_start = ["one", """
multi"""]

[setup.extra]
on_create = ["nested table, not setup"]

[other]
on_create = ["elsewhere"]
`
	got := SetupLines([]byte(body))
	want := []SetupLine{
		{Line: 10, Key: "on_create", Command: "test -f .satelle/satelle.toml"},
		{Line: 11, Key: "on_create", Command: `cat C:\path`},
		{Line: 13, Key: "on_create", Command: `printf "%s\n" A x`},
		{Line: 15, Key: "post_start", Command: "one"},
		{Line: 15, Key: "post_start", Command: "multi"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SetupLines =\n%+v\nwant\n%+v", got, want)
	}
}

func TestSetupLinesReadsDottedSetupKeys(t *testing.T) {
	got := SetupLines([]byte("version = 1\nsetup.post_start = [\"go\"]\n"))
	if len(got) != 1 || got[0].Line != 2 || got[0].Key != "post_start" || got[0].Command != "go" {
		t.Fatalf("SetupLines = %+v", got)
	}
}

func TestSetupLinesAgreesWithParseOnTheRepoRemotes(t *testing.T) {
	files, err := filepath.Glob("../../.satelle/remotes/*/remote.toml")
	if err != nil {
		t.Fatal(err)
	}
	fixtures, _ := filepath.Glob("testdata/*.toml")
	files = append(files, fixtures...)
	if len(files) == 0 {
		t.Fatal("no remote definitions found")
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		def, err := Parse(b)
		if err != nil {
			continue
		}
		want := append(append([]string{}, def.Setup.OnCreate...), def.Setup.PostStart...)
		var got []string
		for _, key := range []string{"on_create", "post_start"} {
			for _, l := range SetupLines(b) {
				if l.Key == key {
					got = append(got, l.Command)
				}
			}
		}
		if len(want) == 0 && len(got) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: SetupLines commands = %q, Parse = %q", f, got, want)
		}
	}
}
