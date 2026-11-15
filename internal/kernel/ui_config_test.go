package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUIConfig(t *testing.T) {
	root, c := initVolatile(t)
	if len(c.UI.TagColours) != 0 {
		t.Fatalf("fresh init has tag colours %v", c.UI.TagColours)
	}
	if errs := Validate(root); len(errs) != 0 {
		t.Fatalf("no %s: %v", UIFile, errs)
	}
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, UIFile), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("[tag_colours]\n\"priority:high\" = \"ok\"\nsurface = \"warn\"\n\"epic:x\" = \"note\"\n")
	if errs := Validate(root); len(errs) != 0 {
		t.Fatalf("a good %s: %v", UIFile, errs)
	}
	c, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if c.UI.TagColours["priority:high"] != "ok" || c.UI.TagColours["surface"] != "warn" || c.UI.TagColours["epic:x"] != "note" {
		t.Errorf("loaded %v", c.UI.TagColours)
	}
	for name, body := range map[string]string{
		"a colour outside the palette": "[tag_colours]\nsurface = \"pink\"\n",
		"an empty tag":                 "[tag_colours]\n\"\" = \"ok\"\n",
		"an unknown table":             "[colours]\nsurface = \"ok\"\n",
	} {
		write(body)
		errs := Validate(root)
		if len(errs) == 0 {
			t.Errorf("%s: no problem", name)
			continue
		}
		joined := ""
		for _, e := range errs {
			joined += e.Error() + "\n"
		}
		if !strings.Contains(joined, UIFile) {
			t.Errorf("%s: the problem does not name %s:\n%s", name, UIFile, joined)
		}
	}
}
