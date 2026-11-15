package web

import (
	"strings"
	"testing"
)

func TestRenderMarkdownFormatsCommonElements(t *testing.T) {
	src := "---\nname: x\nkind: doc\n---\n# Title\n\nIntro **bold** and `code` and a [link](https://example.com).\n\n- one\n- two\n\n```\nplain block\n```\n"
	got := string(renderMarkdown(src))

	for _, want := range []string{
		"<h1>Title</h1>",
		"<strong>bold</strong>",
		"<code>code</code>",
		`<a href="https://example.com">link</a>`,
		"<ul>",
		"<li>one</li>",
		"<pre><code>",
		"plain block",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered markdown missing %q\n--- got ---\n%s", want, got)
		}
	}
	if strings.Contains(got, "kind: doc") {
		t.Errorf("frontmatter leaked into rendered output:\n%s", got)
	}
}

func TestRenderMarkdownEscapesRawHTMLAndScript(t *testing.T) {
	src := "intro <script>alert('xss')</script> and <img src=x onerror=alert(1)> text"
	got := string(renderMarkdown(src))
	if strings.Contains(got, "<script>") || strings.Contains(got, "<img") {
		t.Errorf("raw HTML/script reached the output (injection):\n%s", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("script tag should be escaped, got:\n%s", got)
	}
}
