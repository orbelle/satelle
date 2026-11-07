package hook

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/bobmcallan/satelle/internal/gate"
)

// Gate is the edit gate for one session working in one tree. An edit is
// allowed only while the session holds a seat, at this tree's location, on an
// item whose current state has a performer. Configured exemptions are always
// allowed, locked paths never; temp dirs and paths in no working tree are
// outside its concern; another repository's tree is refused (the fence).
type Gate struct {
	Root   string   // the session's working tree, absolute
	Exempt []string // [gate] edit_exempt_paths, globs relative to a tree
	Locked []string // [gate] locked_paths
	// Seated reports whether the session holds a seat at local:<tree> on an
	// item whose current state has a performer.
	Seated func(tree string) bool
	// TempRoots are dirs whose contents are always editable; nil means
	// TempRoots().
	TempRoots []string
	// SameRepo reports whether tree is a working tree of Root's repository;
	// nil compares git common dirs.
	SameRepo func(root, tree string) bool
	// IsFile reports an existing regular file (a named script, not inline
	// program text); nil stats the filesystem.
	IsFile func(string) bool
}

// Verdict is the gate's answer for one tool call.
type Verdict struct {
	Allow  bool
	Path   string // the path a deny is about
	Reason string
}

var allow = Verdict{Allow: true}

// OperatorRecordDenied is the gate's refusal of satelle's operator record verb in
// an agent tool call: every call the gate judges is one.
const OperatorRecordDenied = "satelle: recording the operator's word is the operator's alone; an agent session may not run `satelle <kind> record`. " +
	"Ask the operator to run it in their own terminal. (If this is prose such as a commit message, reword it or pass it with -F <file>.)"

// Edit judges an edit of target (absolute, or relative to Root).
func (g Gate) Edit(target string) Verdict {
	return g.judge(resolvePath(g.Root, target), false)
}

// Bash judges a shell command run in cwd by the same predicate, applied to
// every path it would write and to the working dir of any inline program.
func (g Gate) Bash(command, cwd string) Verdict {
	if strings.TrimSpace(cwd) == "" {
		cwd = g.Root
	}
	isFile := g.IsFile
	if isFile == nil {
		isFile = isRegularFile
	}
	t := ScanBash(command, cwd, g.Root, isFile)
	if t.OperatorRecord {
		return Verdict{Reason: OperatorRecordDenied}
	}
	for _, p := range t.Paths {
		if v := g.judge(p, false); !v.Allow {
			return v
		}
	}
	for _, d := range t.Inline {
		if v := g.judge(d, true); !v.Allow {
			v.Reason = "an inline program would run in " + d + ", which it may write anywhere under. " + v.Reason
			return v
		}
	}
	return allow
}

// judge applies the predicate to one absolute path. inline marks the working
// dir of an inline program: outside the session's repository it is no write
// the gate can attribute, so the fence does not apply to it.
func (g Gate) judge(abs string, inline bool) Verdict {
	root := filepath.Clean(g.Root)
	if !within(root, abs) && g.underTemp(abs) {
		return allow
	}
	tree := gitRootOf(abs)
	if tree == "" && within(root, abs) {
		tree = root
	}
	if tree == "" {
		return allow
	}
	if tree != root && !g.sameRepo(root, tree) {
		if inline {
			return allow
		}
		return Verdict{Path: abs, Reason: fmt.Sprintf(
			"satelle: %s is in another repository's working tree (%s). This session works in %s; edit another repository from a session there.",
			abs, tree, root)}
	}
	rel := relSlash(tree, abs)
	if matchAny(g.Locked, rel) {
		return Verdict{Path: abs, Reason: fmt.Sprintf(
			"satelle: %s is a locked path ([gate] locked_paths); no seat allows editing it.", rel)}
	}
	if matchAny(g.Exempt, rel) {
		return allow
	}
	if g.Seated != nil && g.Seated(tree) {
		return allow
	}
	return Verdict{Path: abs, Reason: fmt.Sprintf(
		"satelle: edit to %s blocked. Edits are allowed only while this session holds a seat at %s on an item whose current state has a performer. "+
			"Seat one with `satelle story seat <id>` (see `satelle status` and `satelle help edit-gate`).", rel, "local:"+tree)}
}

func (g Gate) underTemp(abs string) bool {
	roots := g.TempRoots
	if roots == nil {
		roots = TempRoots()
	}
	for _, r := range roots {
		if within(r, abs) {
			return true
		}
	}
	return false
}

func (g Gate) sameRepo(root, tree string) bool {
	if g.SameRepo != nil {
		return g.SameRepo(root, tree)
	}
	return sameRepo(root, tree)
}

// TempRoots is the process temp dir (os.TempDir honours TMPDIR) and /tmp.
func TempRoots() []string {
	var out []string
	for _, p := range []string{os.TempDir(), "/tmp"} {
		p = filepath.Clean(p)
		if p != "." && (len(out) == 0 || out[0] != p) {
			out = append(out, p)
		}
	}
	return out
}

// matchAny reports whether rel matches a glob. A glob without a slash also
// matches the path's base name.
func matchAny(globs []string, rel string) bool {
	for _, g := range globs {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		if gate.Match(g, rel) {
			return true
		}
		if !strings.Contains(g, "/") {
			if ok, _ := path.Match(g, path.Base(rel)); ok {
				return true
			}
		}
	}
	return false
}

func relSlash(root, abs string) string {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return filepath.ToSlash(abs)
	}
	return filepath.ToSlash(rel)
}

// within reports whether target is root or under it. Both are absolute.
func within(root, target string) bool {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(target) == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func isRegularFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}
