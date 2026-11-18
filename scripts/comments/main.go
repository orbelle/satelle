package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const usage = "usage: comments strip|check [path...]"

type kind int

const (
	kindNone kind = iota
	kindGo
	kindShell
	kindMake
	kindTOML
	kindMarkdown
	kindHashLines
	kindJS
	kindCSS
)

type finding struct {
	line int
	text string
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 || (args[0] != "strip" && args[0] != "check") {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	mode := args[0]
	paths := args[1:]
	explicit := len(paths) > 0
	if !explicit {
		var err error
		paths, err = trackedFiles()
		if err != nil {
			fmt.Fprintln(os.Stderr, "comments:", err)
			return 2
		}
	}
	sort.Strings(paths)
	failed := false
	total := 0
	for _, p := range paths {
		k := classify(p, explicit)
		if k == kindNone {
			continue
		}
		src, err := os.ReadFile(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", p, err)
			failed = true
			continue
		}
		if k == kindGo && isGenerated(src) {
			continue
		}
		out, found, err := process(k, src)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", p, err)
			failed = true
			continue
		}
		if len(found) == 0 {
			continue
		}
		total += len(found)
		switch mode {
		case "check":
			for _, f := range found {
				fmt.Printf("%s:%d: %s\n", p, f.line, f.text)
			}
		case "strip":
			info, err := os.Stat(p)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: %v\n", p, err)
				failed = true
				continue
			}
			if err := os.WriteFile(p, out, info.Mode().Perm()); err != nil {
				fmt.Fprintf(os.Stderr, "%s: %v\n", p, err)
				failed = true
				continue
			}
			fmt.Printf("%s: %d removed\n", p, len(found))
		}
	}
	if failed {
		return 2
	}
	if mode == "check" && total > 0 {
		fmt.Fprintf(os.Stderr, "%d comment(s) found\n", total)
		return 1
	}
	if mode == "strip" {
		fmt.Printf("%d comment(s) removed\n", total)
	}
	return 0
}

func process(k kind, src []byte) ([]byte, []finding, error) {
	switch k {
	case kindGo:
		return stripGo(src)
	case kindShell:
		return stripLines(src, shellComments)
	case kindMake:
		return stripLines(src, makeComments)
	case kindTOML:
		return stripLines(src, tomlComments)
	case kindMarkdown:
		return stripMarkdown(src)
	case kindHashLines:
		return stripLines(src, hashLineComments)
	case kindJS:
		return stripWeb(src, jsComments)
	case kindCSS:
		return stripWeb(src, cssComments)
	}
	return nil, nil, errors.New("unknown file kind")
}

func trackedFiles() ([]string, error) {
	out, err := exec.Command("git", "ls-files", "-z").Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w", err)
	}
	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

const templateDir = "internal/kernel/template/"

func classify(p string, explicit bool) kind {
	p = filepath.ToSlash(p)
	if !explicit && strings.HasPrefix(p, ".satelle/") {
		return kindNone
	}
	base := filepath.Base(p)
	switch {
	case strings.HasSuffix(p, ".go"):
		return kindGo
	case strings.HasSuffix(p, ".sh"):
		return kindShell
	case base == "Makefile":
		return kindMake
	case strings.HasSuffix(p, ".js"):
		return kindJS
	case strings.HasSuffix(p, ".css"):
		return kindCSS
	}
	if explicit || strings.HasPrefix(p, templateDir) {
		switch {
		case strings.HasSuffix(p, ".toml"):
			return kindTOML
		case strings.HasSuffix(p, ".md"):
			return kindMarkdown
		case base == ".gitignore":
			return kindHashLines
		}
	}
	if hasShellShebang(p) {
		return kindShell
	}
	return kindNone
}

func hasShellShebang(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 128)
	n, _ := f.Read(buf)
	line, _, _ := bytes.Cut(buf[:n], []byte("\n"))
	if !bytes.HasPrefix(line, []byte("#!")) {
		return false
	}
	fields := strings.Fields(string(line[2:]))
	if len(fields) == 0 {
		return false
	}
	interp := filepath.Base(fields[0])
	if interp == "env" && len(fields) > 1 {
		interp = fields[1]
	}
	switch interp {
	case "sh", "bash", "dash", "ksh", "zsh":
		return true
	}
	return false
}
