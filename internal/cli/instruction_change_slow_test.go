//go:build clislow

package cli

import (
	"strings"
	"testing"
)

func TestSlowInstructionTriggerDecidesByChangedWords(t *testing.T) {
	extra := "[instruction_review]\nextra_paths = [\"internal/config/substrate/skills/**\"]\n"
	cases := []struct {
		name     string
		toml     string
		baseline bool
		edit     func(t *testing.T, dir string)
		files    []string
		want     int
	}{
		{"whitespace-only edit skips", "", true, func(t *testing.T, d string) {
			writeFile(t, d, ".satelle/skills/a.md", "# A\n\n\n"+strings.ReplaceAll(triggerParagraph, " ", "   ")+"   \n\n")
		}, []string{".satelle/skills/a.md"}, 1},
		{"reflowed paragraph skips", "", true, func(t *testing.T, d string) {
			writeFile(t, d, ".satelle/skills/a.md", "# A\n\n"+strings.ReplaceAll(triggerParagraph, " because ", "\nbecause ")+"\n")
		}, []string{".satelle/skills/a.md"}, 1},
		{"punctuation-only edit skips", "", true, func(t *testing.T, d string) {
			writeFile(t, d, ".satelle/skills/a.md", "# A\n\n"+strings.ReplaceAll(triggerParagraph, "done because", "done, because")+" ;\n")
		}, []string{".satelle/skills/a.md"}, 1},
		{"one-word typo fix skips", "", true, func(t *testing.T, d string) {
			writeFile(t, d, ".satelle/skills/a.md", "# A\n\n"+strings.Replace(triggerParagraph, "ledger", "ledgre", 1)+"\n")
		}, []string{".satelle/skills/a.md"}, 1},
		{"new paragraph runs", "", true, func(t *testing.T, d string) {
			writeFile(t, d, ".satelle/skills/a.md", "# A\n\n"+triggerParagraph+"\n\nNever lower an acceptance criterion to make a gate accept, since the definition is frozen once work starts.\n")
		}, []string{".satelle/skills/a.md"}, 0},
		{"new file runs", "", true, func(t *testing.T, d string) {
			writeFile(t, d, ".satelle/principles/new.md", "# New\n\nx\n")
		}, []string{".satelle/principles/new.md"}, 0},
		{"changes below the threshold add up across files", "", true, func(t *testing.T, d string) {
			// Three changed words each: below the threshold alone, above it together.
			writeFile(t, d, ".satelle/skills/a.md", "# A\n\n"+strings.Replace(triggerParagraph, "before you claim", "first then say", 1)+"\n")
			writeFile(t, d, ".satelle/skills/b.md", "# B\n\n"+strings.Replace(triggerParagraph, "proves nothing about", "shows little regarding", 1)+"\n")
		}, []string{".satelle/skills/a.md", ".satelle/skills/b.md"}, 0},
		{"a big change outside the instruction paths skips", "", true, func(t *testing.T, d string) {
			writeFile(t, d, "README.md", "# readme\n\ncompletely different words that add up to well over five changed tokens here\n")
		}, []string{"README.md"}, 1},
		{"extra_paths file is ignored while the key is unset", "", true, func(t *testing.T, d string) {
			writeFile(t, d, "internal/config/substrate/skills/e.md", "# E\n\nNever lower an acceptance criterion to make a gate accept, since the definition is frozen.\n")
		}, []string{"internal/config/substrate/skills/e.md"}, 1},
		{"extra_paths file counts once the key is set", extra, true, func(t *testing.T, d string) {
			writeFile(t, d, "internal/config/substrate/skills/e.md", "# E\n\nNever lower an acceptance criterion to make a gate accept, since the definition is frozen.\n")
		}, []string{"internal/config/substrate/skills/e.md"}, 0},
		{"extra_paths typo fix still skips", extra, true, func(t *testing.T, d string) {
			writeFile(t, d, "internal/config/substrate/skills/e.md", "# E\n\n"+strings.Replace(triggerParagraph, "ledger", "ledgre", 1)+"\n")
		}, []string{"internal/config/substrate/skills/e.md"}, 1},
		{"no instruction file changed skips", "", true, func(t *testing.T, d string) {}, nil, 1},
		{"instruction change with no baseline runs", "", false, func(t *testing.T, d string) {
			writeFile(t, d, ".satelle/skills/a.md", "# A\n\ntiny\n")
		}, []string{".satelle/skills/a.md"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newTriggerRepo(t, tc.toml)
			tc.edit(t, r.dir)
			if got, out := runTrigger(t, r, tc.baseline, tc.files...); got != tc.want {
				t.Errorf("exit %d, want %d\n%s", got, tc.want, out)
			}
		})
	}
}
