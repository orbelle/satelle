package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/bobmcallan/satelle/internal/config"
)

const logsPointerRel = ".satelle/logs"

// EnsureLogsPointer plants .satelle/logs as a symlink to the home-keyed runtime
// logs directory. It refuses to clobber a real directory.
func EnsureLogsPointer(repoRoot, runtimeLogs string) error {
	if err := os.MkdirAll(runtimeLogs, 0o755); err != nil {
		return err
	}
	link := filepath.Join(repoRoot, filepath.FromSlash(logsPointerRel))
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return err
	}
	st, err := os.Lstat(link)
	if err == nil {
		if st.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("%s exists and is not a symlink — migrate relocates a real logs dir first", logsPointerRel)
		}
		got, _ := os.Readlink(link)
		want, _ := filepath.Abs(runtimeLogs)
		if got == runtimeLogs || got == want {
			return nil
		}
		if err := os.Remove(link); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	target, err := filepath.Abs(runtimeLogs)
	if err != nil {
		target = runtimeLogs
	}
	return os.Symlink(target, link)
}

// gitignoreRuleTarget reads one .gitignore line as the path it targets: blank
// and comment lines are not rules (ok false); a leading "!" marks a negation;
// a leading "/" and a trailing "/", "/*" or "/**" are dropped, so ".claude/*"
// and "/.claude/" both target ".claude".
func gitignoreRuleTarget(line string) (target string, negated bool, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", false, false
	}
	if strings.HasPrefix(line, "!") {
		negated = true
		line = line[1:]
	}
	line = strings.TrimPrefix(line, "/")
	for _, suffix := range []string{"/**", "/*", "/"} {
		if strings.HasSuffix(line, suffix) {
			line = strings.TrimSuffix(line, suffix)
			break
		}
	}
	if line == "" {
		return "", false, false
	}
	return line, negated, true
}

// gitignoreHasSetting reports whether the operator's .gitignore already holds a
// setting for path: a rule, positive or "!" negation, that targets path or a
// path under it. satelle leaves such a path exactly as the operator chose.
func gitignoreHasSetting(content, path string) bool {
	for _, line := range strings.Split(content, "\n") {
		if t, _, ok := gitignoreRuleTarget(line); ok && (t == path || strings.HasPrefix(t, path+"/")) {
			return true
		}
	}
	return false
}

// gitignoreHasRule reports whether any rule, positive or negated, targets
// exactly path.
func gitignoreHasRule(content, path string) bool {
	for _, line := range strings.Split(content, "\n") {
		if t, _, ok := gitignoreRuleTarget(line); ok && t == path {
			return true
		}
	}
	return false
}

// gitignoreTextIgnoresSatelleDir reports a positive whole-directory ignore of
// .satelle (".satelle", ".satelle/", "/.satelle", "/.satelle/"). A ".satelle/*"
// rule is not one: it can be re-opened by a negation.
func gitignoreTextIgnoresSatelleDir(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		if isSatelleWholesaleIgnore(line) {
			return true
		}
	}
	return false
}

// isSatelleWholesaleIgnore reports whether one .gitignore line ignores the whole
// .satelle directory — the line that keeps the process substrate out of git.
func isSatelleWholesaleIgnore(line string) bool {
	switch strings.TrimSpace(line) {
	case ".satelle", ".satelle/", "/.satelle", "/.satelle/":
		return true
	}
	return false
}

// gitignoreOwnSatelleIgnore returns the first line OUTSIDE satelle's managed
// block that ignores .satelle wholesale, as written. satelle never edits such a
// line; init and doctor name it so the operator can remove it.
func gitignoreOwnSatelleIgnore(content string) (line string, ok bool) {
	inBlock := false
	for _, l := range strings.Split(content, "\n") {
		switch strings.TrimSpace(l) {
		case gitignoreMarker:
			inBlock = true
			continue
		case gitignoreMarkerEnd:
			inBlock = false
			continue
		}
		if !inBlock && isSatelleWholesaleIgnore(l) {
			return strings.TrimSpace(l), true
		}
	}
	return "", false
}

func plantLogsPointer(out io.Writer, repoRoot string) {
	cfgPath := filepath.Join(repoRoot, config.DefaultDataDir, config.ConfigName)
	cfg, _, err := config.Load(cfgPath)
	if err != nil {
		cfg, _, err = config.Load("")
	}
	if err != nil {
		fmt.Fprintf(out, "  ! %s pointer: %v\n", logsPointerRel, err)
		return
	}
	rt := cfg.ResolveLogsDir(repoRoot)
	link := filepath.Join(repoRoot, filepath.FromSlash(logsPointerRel))
	_, existed := os.Lstat(link)
	if err := EnsureLogsPointer(repoRoot, rt); err != nil {
		fmt.Fprintf(out, "  ! %s pointer: %v\n", logsPointerRel, err)
		return
	}
	fmt.Fprintln(out, initLine(existed != nil, logsPointerRel+" (pointer to runtime logs)"))
}
