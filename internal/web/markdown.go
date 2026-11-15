package web

import (
	"html"
	"html/template"
	"regexp"
	"strconv"
	"strings"
)

var (
	mdHeading = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	mdOrdered = regexp.MustCompile(`^\d+\.\s+(.*)$`)
	mdCode    = regexp.MustCompile("`([^`]+)`")
	mdBold    = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	mdItalic  = regexp.MustCompile(`\*([^*]+)\*`)
	mdUnder   = regexp.MustCompile(`_([^_]+)_`)
	mdLink    = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
)

func renderMarkdown(src string) template.HTML {
	lines := strings.Split(stripDocFrontmatter(src), "\n")
	var b strings.Builder
	var list string
	inCode := false
	var para []string

	flushPara := func() {
		if len(para) > 0 {
			b.WriteString("<p>" + inlineMarkdown(strings.Join(para, " ")) + "</p>\n")
			para = nil
		}
	}
	closeList := func() {
		if list != "" {
			b.WriteString("</" + list + ">\n")
			list = ""
		}
	}

	for _, ln := range lines {
		if strings.HasPrefix(strings.TrimSpace(ln), "```") {
			if inCode {
				b.WriteString("</code></pre>\n")
				inCode = false
			} else {
				flushPara()
				closeList()
				b.WriteString("<pre><code>")
				inCode = true
			}
			continue
		}
		if inCode {
			b.WriteString(html.EscapeString(ln) + "\n")
			continue
		}
		t := strings.TrimSpace(ln)
		if t == "" {
			flushPara()
			closeList()
			continue
		}
		if m := mdHeading.FindStringSubmatch(t); m != nil {
			flushPara()
			closeList()
			lvl := strconv.Itoa(len(m[1]))
			b.WriteString("<h" + lvl + ">" + inlineMarkdown(m[2]) + "</h" + lvl + ">\n")
			continue
		}
		if strings.HasPrefix(t, "> ") {
			flushPara()
			closeList()
			b.WriteString("<blockquote>" + inlineMarkdown(strings.TrimPrefix(t, "> ")) + "</blockquote>\n")
			continue
		}
		if strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ") || strings.HasPrefix(t, "+ ") {
			flushPara()
			if list != "ul" {
				closeList()
				b.WriteString("<ul>\n")
				list = "ul"
			}
			b.WriteString("<li>" + inlineMarkdown(t[2:]) + "</li>\n")
			continue
		}
		if m := mdOrdered.FindStringSubmatch(t); m != nil {
			flushPara()
			if list != "ol" {
				closeList()
				b.WriteString("<ol>\n")
				list = "ol"
			}
			b.WriteString("<li>" + inlineMarkdown(m[1]) + "</li>\n")
			continue
		}
		para = append(para, t)
	}
	if inCode {
		b.WriteString("</code></pre>\n")
	}
	flushPara()
	closeList()
	return template.HTML(b.String())
}

func inlineMarkdown(s string) string {
	s = html.EscapeString(s)
	s = mdCode.ReplaceAllStringFunc(s, func(m string) string {
		return "<code>" + mdCode.FindStringSubmatch(m)[1] + "</code>"
	})
	s = mdBold.ReplaceAllStringFunc(s, func(m string) string {
		return "<strong>" + mdBold.FindStringSubmatch(m)[1] + "</strong>"
	})
	s = mdItalic.ReplaceAllStringFunc(s, func(m string) string {
		return "<em>" + mdItalic.FindStringSubmatch(m)[1] + "</em>"
	})
	s = mdUnder.ReplaceAllStringFunc(s, func(m string) string {
		return "<em>" + mdUnder.FindStringSubmatch(m)[1] + "</em>"
	})
	s = mdLink.ReplaceAllStringFunc(s, func(m string) string {
		sub := mdLink.FindStringSubmatch(m)
		return `<a href="` + sub[2] + `">` + sub[1] + `</a>`
	})
	return s
}

func stripLeadingHeading(src string) string {
	lines := strings.Split(src, "\n")
	i := 0
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	if i >= len(lines) || mdHeading.FindStringSubmatch(strings.TrimSpace(lines[i])) == nil {
		return src
	}
	i++
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	return strings.Join(lines[i:], "\n")
}

func stripDocFrontmatter(body string) string {
	lines := strings.Split(body, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return body
	}
	for j := 1; j < len(lines); j++ {
		if strings.TrimSpace(lines[j]) == "---" {
			return strings.TrimLeft(strings.Join(lines[j+1:], "\n"), "\n")
		}
	}
	return body
}
