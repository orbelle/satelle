package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/scanner"
	"go/token"
	"regexp"
	"strconv"
	"strings"
)

var goDirective = regexp.MustCompile(`^//go:(build|embed|generate)( |$)`)

func isGenerated(src []byte) bool {
	f, err := parser.ParseFile(token.NewFileSet(), "", src, parser.ParseComments|parser.PackageClauseOnly)
	if err != nil {
		return false
	}
	return ast.IsGenerated(f)
}

func keptGoComment(text string) bool {
	return goDirective.MatchString(text)
}

func exampleOutput(text string) bool {
	t := strings.TrimSpace(strings.TrimPrefix(text, "//"))
	return strings.HasPrefix(t, "Output:") || strings.HasPrefix(t, "Unordered output:")
}

func stripGo(src []byte) ([]byte, []finding, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		return nil, nil, err
	}
	tf := fset.File(f.Pos())
	keep := map[*ast.Comment]bool{}
	for _, imp := range f.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p != "C" {
			continue
		}
		for _, d := range f.Decls {
			if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.IMPORT && gd.Pos() <= imp.Pos() && imp.End() <= gd.End() {
				for _, g := range []*ast.CommentGroup{gd.Doc, imp.Doc} {
					if g != nil {
						for _, c := range g.List {
							keep[c] = true
						}
					}
				}
			}
		}
	}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Body == nil || !strings.HasPrefix(fd.Name.Name, "Example") {
			continue
		}
		for _, g := range f.Comments {
			if g.Pos() < fd.Body.Lbrace || g.End() > fd.Body.Rbrace {
				continue
			}
			on := false
			for _, c := range g.List {
				if exampleOutput(c.Text) {
					on = true
				}
				if on {
					keep[c] = true
				}
			}
		}
	}
	var spans []span
	var found []finding
	for _, g := range f.Comments {
		for _, c := range g.List {
			if keep[c] || keptGoComment(c.Text) {
				continue
			}
			start := tf.Offset(c.Pos())
			end := tf.Offset(c.End())
			spans = append(spans, span{start, end})
			found = append(found, finding{fset.Position(c.Pos()).Line, firstLine(c.Text)})
		}
	}
	if len(spans) == 0 {
		return src, nil, nil
	}
	out := removeGoSpans(src, spans)
	formatted, err := format.Source(out)
	if err != nil {
		return nil, nil, fmt.Errorf("stripped source does not format: %w", err)
	}
	if err := sameGo(src, formatted); err != nil {
		return nil, nil, err
	}
	return formatted, found, nil
}

func removeGoSpans(src []byte, spans []span) []byte {
	var out bytes.Buffer
	prev := 0
	for _, s := range spans {
		ls := lineStart(src, s.start)
		le := lineEnd(src, s.end)
		before := blank(src[max(ls, prev):s.start])
		after := blank(src[s.end:le])
		multi := bytes.IndexByte(src[s.start:s.end], '\n') >= 0
		switch {
		case before && after:
			cut := max(ls, prev)
			out.Write(src[prev:cut])
			prev = min(le+1, len(src))
		case after:
			cut := s.start
			for cut > prev && (src[cut-1] == ' ' || src[cut-1] == '\t') {
				cut--
			}
			out.Write(src[prev:cut])
			prev = s.end
		default:
			out.Write(src[prev:s.start])
			if multi {
				out.WriteByte('\n')
			} else {
				out.WriteByte(' ')
			}
			prev = s.end
		}
	}
	out.Write(src[prev:])
	return out.Bytes()
}

type goTok struct {
	tok token.Token
	lit string
}

func goTokens(src []byte) ([]goTok, []string) {
	var s scanner.Scanner
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	s.Init(file, src, nil, scanner.ScanComments)
	var toks []goTok
	var directives []string
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		switch tok {
		case token.COMMENT:
			if keptGoComment(lit) {
				directives = append(directives, lit)
			}
			continue
		case token.SEMICOLON:
			lit = ""
		}
		toks = append(toks, goTok{tok, lit})
	}
	return toks, directives
}

func sameGo(a, b []byte) error {
	ta, da := goTokens(a)
	tb, db := goTokens(b)
	if len(ta) != len(tb) {
		return fmt.Errorf("token count changed: %d to %d", len(ta), len(tb))
	}
	for i := range ta {
		if ta[i] != tb[i] {
			return fmt.Errorf("token %d changed: %v %q to %v %q", i, ta[i].tok, ta[i].lit, tb[i].tok, tb[i].lit)
		}
	}
	if strings.Join(da, "\n") != strings.Join(db, "\n") {
		return fmt.Errorf("a kept directive changed")
	}
	return nil
}
