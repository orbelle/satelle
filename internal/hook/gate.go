package hook

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/bobmcallan/satelle/internal/gate"
)

type Gate struct {
	Root      string
	Exempt    []string
	Locked    []string
	Grant     func(tree string) Grant
	TempRoots []string
	SameRepo  func(root, tree string) bool
	IsFile    func(string) bool
}

type Grant struct {
	Tree        bool
	AttachOnly  bool
	Item, State string
	Unread      string
}

type Verdict struct {
	Allow      bool
	Path       string
	Reason     string
	AttachOnly bool
}

var allow = Verdict{Allow: true}

const OperatorRecordDenied = "satelle: recording the operator's word is the operator's alone; an agent session may not run `satelle <kind> record`. " +
	"Ask the operator to run it in their own terminal. (Prose is read as code only when a shell or interpreter would run it, or it is passed to a command the scan does not know as data; pass such prose with -F <file>.)"

func (g Gate) Edit(target string) Verdict {
	return g.judge(resolvePath(g.Root, target), false)
}

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
	var grant Grant
	if g.Grant != nil {
		grant = g.Grant(tree)
	}
	if grant.AttachOnly {
		return Verdict{Path: abs, AttachOnly: true, Reason: attachOnlyReason(rel, "local:"+tree, grant)}
	}
	if matchAny(g.Locked, rel) {
		return Verdict{Path: abs, Reason: fmt.Sprintf(
			"satelle: %s is a locked path ([gate] locked_paths); no seat allows editing it.", rel)}
	}
	if matchAny(g.Exempt, rel) {
		return allow
	}
	if grant.Tree {
		return allow
	}
	return Verdict{Path: abs, Reason: fmt.Sprintf(
		"satelle: edit to %s blocked. Edits are allowed only while this session holds a seat at %s on an item whose current state's performer may change the tree. "+
			"Seat one with `satelle story seat <id> --session <session>` (see `satelle status --session <session>` and `satelle help edit-gate --session <session>`).", rel, "local:"+tree)}
}

func attachOnlyReason(rel, loc string, g Grant) string {
	const record = "Record the work with `satelle <kind> attach <id> --name <name> --file <file> --session <session>` (a file under a temp dir, or --file - for stdin). " +
		"See `satelle help edit-gate --session <session>`."
	if g.Item == "" {
		return fmt.Sprintf("satelle: edit to %s blocked. This session's seats at %s cannot be read (%s), so they are treated as seats on a state whose performer produces an attachment, not a change to the tree; "+
			"no path of this tree is editable, [gate] edit_exempt_paths and locked_paths included. %s", rel, loc, g.Unread, record)
	}
	why := "whose performer produces an attachment, not a change to the tree"
	if g.Unread != "" {
		why = fmt.Sprintf("whose edit scope cannot be read (%s), so it is treated as a state whose performer produces an attachment, not a change to the tree", g.Unread)
	}
	return fmt.Sprintf("satelle: edit to %s blocked. This session's seat at %s is on %s in state \"%s\", %s; "+
		"no path of this tree is editable from that seat, [gate] edit_exempt_paths and locked_paths included. %s", rel, loc, g.Item, g.State, why, record)
}

func (g Gate) underTemp(abs string) bool {
	return UnderTemp(g.TempRoots, abs)
}

func UnderTemp(roots []string, path string) bool {
	if roots == nil {
		roots = TempRoots()
	}
	paths := []string{filepath.Clean(path)}
	if p, err := filepath.EvalSymlinks(path); err == nil && p != paths[0] {
		paths = append(paths, p)
	}
	for _, r := range roots {
		forms := []string{filepath.Clean(r)}
		if rr, err := filepath.EvalSymlinks(r); err == nil && rr != forms[0] {
			forms = append(forms, rr)
		}
		for _, f := range forms {
			for _, p := range paths {
				if within(f, p) {
					return true
				}
			}
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
