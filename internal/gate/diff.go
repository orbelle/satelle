package gate

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

const DefaultDiffMaxBytes = 64_000

type diffSection struct {
	path string
	text string
}

func formatPromptDiff(raw string, bound int) string {
	if raw == "" {
		return ""
	}
	rawSections := splitDiffSections(raw)
	sections := make([]diffSection, 0, len(rawSections))
	for _, s := range rawSections {
		sections = append(sections, parseDiffSection(s))
	}

	var assembled strings.Builder
	ends := make([]int, len(sections))
	for i, sec := range sections {
		assembled.WriteString(sec.text)
		ends[i] = assembled.Len()
	}
	full := assembled.String()
	if len(full) <= bound {
		return full
	}

	cut := bound
	if cut < 0 {
		cut = 0
	}
	for cut > 0 && !utf8.RuneStart(full[cut]) {
		cut--
	}

	var cutFiles []string
	for i, sec := range sections {
		if ends[i] > cut && sec.path != "" && !slices.Contains(cutFiles, sec.path) {
			cutFiles = append(cutFiles, sec.path)
		}
	}

	var out strings.Builder
	prefix := full[:cut]
	out.WriteString(prefix)
	if prefix != "" && !strings.HasSuffix(prefix, "\n") {
		out.WriteByte('\n')
	}
	if len(cutFiles) == 0 {
		fmt.Fprintf(&out, "… diff truncated at %d bytes …\n", bound)
		return out.String()
	}
	fmt.Fprintf(&out, "… diff truncated at %d bytes; cut files:\n", bound)
	for _, p := range cutFiles {
		fmt.Fprintf(&out, "- %s\n", p)
	}
	return out.String()
}

func splitDiffSections(raw string) []string {
	var sections []string
	start := 0
	for i := 0; i < len(raw); {
		if i > start && strings.HasPrefix(raw[i:], "diff --git ") {
			sections = append(sections, raw[start:i])
			start = i
		}
		nl := strings.IndexByte(raw[i:], '\n')
		if nl < 0 {
			break
		}
		i += nl + 1
	}
	sections = append(sections, raw[start:])
	return sections
}

func parseDiffSection(sec string) diffSection {
	if !strings.HasPrefix(sec, "diff --git ") {
		return diffSection{text: sec}
	}
	var (
		deleted  bool
		binary   bool
		inHunk   bool
		removed  int
		renameTo string
		copyTo   string
		toPath   string
		fromPath string
		header   string
	)
	for i := 0; i < len(sec); {
		nl := strings.IndexByte(sec[i:], '\n')
		var line string
		if nl < 0 {
			line = sec[i:]
			i = len(sec)
		} else {
			end := i + nl
			line = sec[i:end]
			i = end + 1
		}
		line = strings.TrimSuffix(line, "\r")
		if !inHunk {
			if strings.HasPrefix(line, "@@ ") {
				inHunk = true
				continue
			}
			switch {
			case header == "" && strings.HasPrefix(line, "diff --git "):
				header = line
			case strings.HasPrefix(line, "deleted file mode "):
				deleted = true
			case strings.HasPrefix(line, "Binary files ") || strings.HasPrefix(line, "GIT binary patch"):
				binary = true
			case strings.HasPrefix(line, "rename to "):
				renameTo = unquoteGitPath(strings.TrimPrefix(line, "rename to "))
			case strings.HasPrefix(line, "copy to "):
				copyTo = unquoteGitPath(strings.TrimPrefix(line, "copy to "))
			case strings.HasPrefix(line, "+++ "):
				toPath = stripDiffPrefix(strings.TrimPrefix(line, "+++ "), "b/")
			case strings.HasPrefix(line, "--- "):
				fromPath = stripDiffPrefix(strings.TrimPrefix(line, "--- "), "a/")
			}
			continue
		}
		if strings.HasPrefix(line, "-") {
			removed++
		}
	}

	path := sectionPath(renameTo, copyTo, toPath, fromPath, header)
	if !deleted {
		return diffSection{path: path, text: sec}
	}
	if binary {
		return diffSection{path: path, text: fmt.Sprintf("deleted file: %s (binary)\n", path)}
	}
	if removed == 1 {
		return diffSection{path: path, text: fmt.Sprintf("deleted file: %s (1 line)\n", path)}
	}
	return diffSection{path: path, text: fmt.Sprintf("deleted file: %s (%d lines)\n", path, removed)}
}

func sectionPath(renameTo, copyTo, toPath, fromPath, header string) string {
	switch {
	case renameTo != "":
		return renameTo
	case copyTo != "":
		return copyTo
	case toPath != "":
		return toPath
	case fromPath != "":
		return fromPath
	default:
		return parseDiffGitHeader(header)
	}
}

func stripDiffPrefix(raw, prefix string) string {
	if raw == "/dev/null" {
		return ""
	}
	if !strings.HasPrefix(raw, "\"") {
		raw = strings.TrimSuffix(raw, "\t")
	}
	p := unquoteGitPath(raw)
	if after, ok := strings.CutPrefix(p, prefix); ok {
		return after
	}
	return p
}

func parseDiffGitHeader(line string) string {
	rest, ok := strings.CutPrefix(line, "diff --git ")
	if !ok {
		return ""
	}
	if strings.HasPrefix(rest, "\"") {
		src, dst, ok := splitQuotedToken(rest)
		if !ok {
			return ""
		}
		if dst != "" {
			if p := stripDiffPrefix(dst, "b/"); p != "" {
				return p
			}
		}
		return stripDiffPrefix(src, "a/")
	}
	if strings.HasSuffix(rest, "\"") {
		if idx := strings.Index(rest, " \"b/"); idx >= 0 {
			return stripDiffPrefix(rest[idx+1:], "b/")
		}
	}
	if len(rest) >= 5 && strings.HasPrefix(rest, "a/") && (len(rest)-5)%2 == 0 {
		half := (len(rest) - 5) / 2
		mid := 2 + half
		sep := mid + 3
		if rest[mid:sep] == " b/" && rest[2:mid] == rest[sep:] {
			return rest[2:mid]
		}
	}
	if _, after, ok := strings.Cut(rest, " b/"); ok {
		return after
	}
	return ""
}

func splitQuotedToken(s string) (string, string, bool) {
	if len(s) < 2 || s[0] != '"' {
		return "", "", false
	}
	for i := 1; i < len(s); i++ {
		if s[i] == '\\' {
			i++
			continue
		}
		if s[i] == '"' {
			return s[:i+1], strings.TrimPrefix(s[i+1:], " "), true
		}
	}
	return "", "", false
}

func unquoteGitPath(s string) string {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return s
	}
	var out []byte
	for i := 1; i < len(s)-1; i++ {
		if s[i] != '\\' || i+1 >= len(s)-1 {
			out = append(out, s[i])
			continue
		}
		i++
		switch s[i] {
		case '\\', '"':
			out = append(out, s[i])
		case 'a':
			out = append(out, '\a')
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'v':
			out = append(out, '\v')
		default:
			if i+2 < len(s)-1 && isOctal(s[i]) && isOctal(s[i+1]) && isOctal(s[i+2]) {
				b := (s[i]-'0')<<6 | (s[i+1]-'0')<<3 | (s[i+2] - '0')
				out = append(out, b)
				i += 2
			} else {
				out = append(out, '\\', s[i])
			}
		}
	}
	return string(out)
}

func isOctal(b byte) bool { return b >= '0' && b <= '7' }
