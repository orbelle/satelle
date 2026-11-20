package gate

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func makeModifiedSection(path string, lines int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\n", path, path)
	b.WriteString("index 1111111..2222222 100644\n")
	fmt.Fprintf(&b, "--- a/%s\n", path)
	fmt.Fprintf(&b, "+++ b/%s\n", path)
	fmt.Fprintf(&b, "@@ -1 +1,%d @@\n", lines)
	b.WriteString("-old\n")
	for i := range lines {
		fmt.Fprintf(&b, "+line-%04d-abcdefghijklmnopqrstuvwxyz\n", i)
	}
	return b.String()
}

func makeDeletedSection(path string, lines []string, noTrailingNewline bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\n", path, path)
	b.WriteString("deleted file mode 100644\n")
	b.WriteString("index 1111111..0000000\n")
	if len(lines) == 0 {
		return b.String()
	}
	fmt.Fprintf(&b, "--- a/%s\n", path)
	b.WriteString("+++ /dev/null\n")
	if len(lines) == 1 {
		b.WriteString("@@ -1 +0,0 @@\n")
	} else {
		fmt.Fprintf(&b, "@@ -1,%d +0,0 @@\n", len(lines))
	}
	for _, l := range lines {
		fmt.Fprintf(&b, "-%s\n", l)
	}
	if noTrailingNewline {
		b.WriteString("\\ No newline at end of file\n")
	}
	return b.String()
}

func TestFormatPromptDiffDeletedFiles(t *testing.T) {
	t.Run("multi-line deleted text file", func(t *testing.T) {
		raw := makeDeletedSection("old/file.go", []string{"package old", "", "func Gone() {}", "-- literal dashes"}, false)
		got := formatPromptDiff(raw, DefaultDiffMaxBytes)
		want := "deleted file: old/file.go (4 lines)\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
		if strings.Contains(got, "func Gone") {
			t.Fatalf("removed line leaked into summary: %q", got)
		}
	})

	t.Run("single-line without trailing newline", func(t *testing.T) {
		raw := makeDeletedSection("one.txt", []string{"only line"}, true)
		got := formatPromptDiff(raw, DefaultDiffMaxBytes)
		want := "deleted file: one.txt (1 line)\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("empty deleted file", func(t *testing.T) {
		raw := makeDeletedSection("empty.txt", nil, false)
		got := formatPromptDiff(raw, DefaultDiffMaxBytes)
		want := "deleted file: empty.txt (0 lines)\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("deleted symlink", func(t *testing.T) {
		raw := "diff --git a/link b/link\ndeleted file mode 120000\nindex 1111111..0000000\n--- a/link\n+++ /dev/null\n@@ -1 +0,0 @@\n-target/path\n\\ No newline at end of file\n"
		got := formatPromptDiff(raw, DefaultDiffMaxBytes)
		want := "deleted file: link (1 line)\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("deleted binary files", func(t *testing.T) {
		for _, raw := range []string{
			"diff --git a/assets/logo.png b/assets/logo.png\ndeleted file mode 100644\nindex 1111111..0000000\nBinary files a/assets/logo.png and /dev/null differ\n",
			"diff --git a/assets/logo.png b/assets/logo.png\ndeleted file mode 100644\nindex 1111111..0000000\nGIT binary patch\nliteral 0\nHcmV?d00001\n",
		} {
			got := formatPromptDiff(raw, DefaultDiffMaxBytes)
			want := "deleted file: assets/logo.png (binary)\n"
			if got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		}
	})

	t.Run("large deletion summarised before bound keeps modification uncut", func(t *testing.T) {
		var removed []string
		for i := range 2000 {
			removed = append(removed, fmt.Sprintf("deleted-body-%04d-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", i))
		}
		deleted := makeDeletedSection("big_gone.go", removed, false)
		modified := makeModifiedSection("kept.go", 3)
		if len(deleted) <= DefaultDiffMaxBytes {
			t.Fatalf("fixture deleted diff %d bytes must exceed default bound %d", len(deleted), DefaultDiffMaxBytes)
		}
		got := formatPromptDiff(deleted+modified, DefaultDiffMaxBytes)
		if strings.Contains(got, "diff truncated") {
			t.Fatalf("diff was truncated even though deletion summary shrinks it:\n%s", got)
		}
		if !strings.Contains(got, "deleted file: big_gone.go (2000 lines)\n") {
			t.Fatalf("missing deletion summary:\n%s", got)
		}
		if strings.Contains(got, "deleted-body-") {
			t.Fatalf("removed lines were not stripped:\n%s", got)
		}
		if !strings.Contains(got, "+line-0002-abcdefghijklmnopqrstuvwxyz") {
			t.Fatalf("modified file hunk was lost:\n%s", got)
		}
	})

	t.Run("hunk content spoofing is not treated as deleted or split", func(t *testing.T) {
		raw := "diff --git a/internal/gate/diff_test.go b/internal/gate/diff_test.go\n" +
			"index 1111111..2222222 100644\n" +
			"--- a/internal/gate/diff_test.go\n" +
			"+++ b/internal/gate/diff_test.go\n" +
			"@@ -1,2 +1,5 @@\n" +
			" package gate\n" +
			"+diff --git a/fake.go b/fake.go\n" +
			"+deleted file mode 100644\n" +
			"+-removed\n" +
			" var x = 1\n"
		got := formatPromptDiff(raw, DefaultDiffMaxBytes)
		if got != raw {
			t.Fatalf("modified file with hunk spoofing changed:\ngot:\n%s\nwant:\n%s", got, raw)
		}
	})

	t.Run("renamed file is not treated as deleted", func(t *testing.T) {
		raw := "diff --git a/old_name.go b/new_name.go\n" +
			"similarity index 100%\n" +
			"rename from old_name.go\n" +
			"rename to new_name.go\n"
		got := formatPromptDiff(raw, DefaultDiffMaxBytes)
		if got != raw {
			t.Fatalf("renamed file changed:\ngot:\n%s\nwant:\n%s", got, raw)
		}
	})
}

func TestFormatPromptDiffBoundAndCutFiles(t *testing.T) {
	t.Run("under or equal to bound stays unchanged", func(t *testing.T) {
		raw := makeModifiedSection("a.go", 2)
		if got := formatPromptDiff(raw, len(raw)); got != raw {
			t.Fatalf("exact bound altered diff:\ngot:\n%s\nwant:\n%s", got, raw)
		}
		if got := formatPromptDiff(raw, len(raw)+10); got != raw {
			t.Fatalf("under bound altered diff:\ngot:\n%s\nwant:\n%s", got, raw)
		}
	})

	t.Run("multi-file cut mid-second-file names file 2 and file 3 only", func(t *testing.T) {
		s1 := makeModifiedSection("a.go", 2)
		s2 := makeModifiedSection("b.go", 20)
		s3 := makeModifiedSection("c.go", 20)
		bound := len(s1) + 40
		got := formatPromptDiff(s1+s2+s3, bound)
		head, trailer, ok := strings.Cut(got, "… diff truncated at ")
		if !ok {
			t.Fatalf("missing truncation notice:\n%s", got)
		}
		if len(strings.TrimSuffix(head, "\n")) > bound {
			t.Fatalf("diff prefix %d exceeds bound %d", len(head), bound)
		}
		wantTrailer := fmt.Sprintf("%d bytes; cut files:\n- b.go\n- c.go\n", bound)
		if trailer != wantTrailer {
			t.Fatalf("trailer = %q, want %q", trailer, wantTrailer)
		}
		if strings.Contains(trailer, "- a.go") {
			t.Fatalf("intact file a.go listed in cut files: %q", trailer)
		}
	})

	t.Run("cut on exact section boundary names subsequent files only", func(t *testing.T) {
		s1 := makeModifiedSection("first.go", 2)
		s2 := makeModifiedSection("second.go", 4)
		s3 := makeModifiedSection("third.go", 4)
		got := formatPromptDiff(s1+s2+s3, len(s1))
		want := s1 + fmt.Sprintf("… diff truncated at %d bytes; cut files:\n- second.go\n- third.go\n", len(s1))
		if got != want {
			t.Fatalf("boundary cut:\ngot:\n%q\nwant:\n%q", got, want)
		}
	})

	t.Run("single oversized file names itself", func(t *testing.T) {
		s1 := makeModifiedSection("huge.go", 40)
		got := formatPromptDiff(s1, 100)
		if !strings.HasSuffix(got, "… diff truncated at 100 bytes; cut files:\n- huge.go\n") {
			t.Fatalf("single oversized file:\n%s", got)
		}
	})

	t.Run("utf8 rune boundary safety", func(t *testing.T) {
		s1 := "diff --git a/u.txt b/u.txt\nindex 1..2 100644\n--- a/u.txt\n+++ b/u.txt\n@@ -0,0 +1 @@\n+" + strings.Repeat("é", 40) + "\n"
		prefixLen := strings.Index(s1, "é")
		bound := prefixLen + 3
		got := formatPromptDiff(s1, bound)
		head, _, ok := strings.Cut(got, "… diff truncated at ")
		if !ok {
			t.Fatalf("not truncated:\n%s", got)
		}
		if !utf8.ValidString(head) {
			t.Fatalf("cut produced invalid UTF-8: %q", head)
		}
		trimmed := strings.TrimSuffix(head, "\n")
		if len(trimmed) > bound {
			t.Fatalf("prefix %d exceeds bound %d", len(trimmed), bound)
		}
	})

	t.Run("paths with spaces and git C-quoted paths", func(t *testing.T) {
		s1 := "diff --git a/dir with space/my file.txt b/dir with space/my file.txt\n" +
			"deleted file mode 100644\nindex e69de29..0000000\n"
		s2 := "diff --git \"a/caf\\303\\251.txt\" \"b/caf\\303\\251.txt\"\n" +
			"index 1..2 100644\n--- \"a/caf\\303\\251.txt\"\n+++ \"b/caf\\303\\251.txt\"\n@@ -1 +1 @@\n-a\n+b\n"
		s3 := "diff --git \"a/quote\\\"name\\\\x\\t.txt\" \"b/quote\\\"name\\\\x\\t.txt\"\n" +
			"deleted file mode 100644\nindex e69de29..0000000\n"
		gotFull := formatPromptDiff(s1+s2+s3, DefaultDiffMaxBytes)
		if !strings.Contains(gotFull, "deleted file: dir with space/my file.txt (0 lines)\n") {
			t.Fatalf("space path summary missing:\n%s", gotFull)
		}
		if !strings.Contains(gotFull, "deleted file: quote\"name\\x\t.txt (0 lines)\n") {
			t.Fatalf("quoted path summary missing:\n%s", gotFull)
		}
		gotCut := formatPromptDiff(s1+s2+s3, 10)
		for _, want := range []string{
			"- dir with space/my file.txt\n",
			"- café.txt\n",
			"- quote\"name\\x\t.txt\n",
		} {
			if !strings.Contains(gotCut, want) {
				t.Fatalf("cut list lacks %q:\n%s", want, gotCut)
			}
		}
	})

	t.Run("deleted file past bound is named in cut files", func(t *testing.T) {
		s1 := makeModifiedSection("big.go", 30)
		s2 := makeDeletedSection("gone_later.go", []string{"a", "b"}, false)
		got := formatPromptDiff(s1+s2, 120)
		wantSuffix := "… diff truncated at 120 bytes; cut files:\n- big.go\n- gone_later.go\n"
		if !strings.HasSuffix(got, wantSuffix) {
			t.Fatalf("got:\n%s\nwant suffix:\n%s", got, wantSuffix)
		}
	})

	t.Run("synthetic pathless diff falls back to generic trailer", func(t *testing.T) {
		got := formatPromptDiff(strings.Repeat("x", 100), 20)
		want := strings.Repeat("x", 20) + "\n… diff truncated at 20 bytes …\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("default bound is below 200k and cuts 100k diff", func(t *testing.T) {
		if DefaultDiffMaxBytes >= 200_000 {
			t.Fatalf("DefaultDiffMaxBytes = %d, want < 200000", DefaultDiffMaxBytes)
		}
		raw := makeModifiedSection("large.go", 3000)
		if len(raw) < 100_000 || len(raw) >= 200_000 {
			t.Fatalf("fixture size %d must be between 100k and 200k", len(raw))
		}
		got := formatPromptDiff(raw, DefaultDiffMaxBytes)
		wantSuffix := fmt.Sprintf("… diff truncated at %d bytes; cut files:\n- large.go\n", DefaultDiffMaxBytes)
		if !strings.HasSuffix(got, wantSuffix) {
			t.Fatalf("100k diff not cut at default %d", DefaultDiffMaxBytes)
		}
	})
}
