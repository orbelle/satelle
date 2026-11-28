package hook

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/reporeg"
)

type Gate struct {
	Root      string
	Exempt    []string
	Locked    []string
	Grant     func(tree string) Grant
	TempRoots []string
	SameRepo  func(root, tree string) bool
	IsFile    func(string) bool
	Push      func(branch string) Verdict
	Targets   func() []Target
}

type Target struct {
	Names               []string
	Item, Tree, Problem string
	Grant               Grant
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

const CdWriteDenied = "satelle: refused under the edit gate's cd rule: this line holds cd, pushd or popd together with a write " +
	"(a redirect, a path a command writes, a -C directory, an inline program, or a git command that is not read-only), " +
	"and the gate does not follow a change of directory for writes. Use an absolute path or git -C <dir> instead. " +
	"See `satelle help edit-gate --session <session>`."

func unreadReason(what string) string {
	return "satelle: refused under the edit gate's fail-closed rule: the gate reads a shell line only when every token is a plain literal word or one of &&, ||, ; and |, " +
		"with git as a command word after an optional cd to one literal path; this line holds " + what + " and names git, " +
		"so the gate cannot tell which tree it changes or whether it only reads. " +
		"Rewrite it in plain form: git -C <dir> or cd <dir> with a literal path, a commit message with -m '<text>' or -F <file>, " +
		"no redirections, no other builtins, or separate commands. See `satelle help edit-gate --session <session>`."
}

func (g Gate) Edit(target string) Verdict {
	return g.judgePath(JoinLiteral(g.Root, target), false)
}

func (g Gate) judgePath(raw string, inline bool) Verdict {
	lex := filepath.Clean(raw)
	forms := []string{lex}
	for _, p := range []string{EntryPath(raw), CanonicalPath(raw), EntryPath(lex), CanonicalPath(lex)} {
		if !slices.Contains(forms, p) {
			forms = append(forms, p)
		}
	}
	for i, p := range forms {
		v := g.judge(p, inline)
		if v.Allow {
			continue
		}
		if len(forms) > 1 {
			named := p
			if i == 0 {
				named = forms[len(forms)-1]
			}
			v.Reason = "satelle: " + raw + " resolves through a symlink to " + named + ". " + v.Reason
		}
		return v
	}
	return allow
}

func nativePath(p string, windows bool) string {
	if !windows {
		return p
	}
	if len(p) >= 2 && p[0] == '/' && isASCIILetter(p[1]) && (len(p) == 2 || p[2] == '/') {
		rest := strings.TrimPrefix(p[2:], "/")
		p = strings.ToUpper(p[1:2]) + ":/" + rest
	}
	return strings.ReplaceAll(p, "/", "\\")
}

func isASCIILetter(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }

func native(p string) string { return nativePath(p, runtime.GOOS == "windows") }

func (g Gate) Bash(command, cwd string) Verdict {
	if strings.TrimSpace(cwd) == "" {
		cwd = g.Root
	}
	isFile := g.IsFile
	if isFile == nil {
		isFile = isRegularFile
	}
	t := ScanBash(command, cwd, g.Root, func(p string) bool { return isFile(native(p)) })
	if t.OperatorRecord {
		return Verdict{Reason: OperatorRecordDenied}
	}
	if t.Unreadable != "" {
		return Verdict{Reason: unreadReason(t.Unreadable)}
	}
	if t.CdWrite {
		return Verdict{Reason: CdWriteDenied}
	}
	for _, p := range t.Raw {
		if v := g.judgePath(native(p), false); !v.Allow {
			return v
		}
	}
	for _, d := range t.InlineRaw {
		d = native(d)
		if v := g.judgePath(d, true); !v.Allow {
			v.Reason = "an inline program would run in " + d + ", which it may write anywhere under. " + v.Reason
			return v
		}
	}
	if g.Push == nil {
		return allow
	}
	for _, d := range t.Pushes {
		v := Verdict{Reason: fmt.Sprintf(pushUnread, d.Unread)}
		if d.Unread == "" {
			v = g.Push(d.Branch)
		}
		if !v.Allow {
			return v
		}
	}
	return allow
}

const pushUnread = "satelle: git push blocked: %s, so the gate cannot tell which branch it pushes to. " +
	"Write one literal branch destination: git push origin HEAD:refs/heads/<branch>. See satelle help edit-gate --session <session>."

func (g Gate) judge(abs string, inline bool) Verdict {
	root := filepath.Clean(g.Root)
	if !within(root, abs) && g.underTemp(abs) {
		return allow
	}
	var targets []Target
	if g.Targets != nil {
		targets = g.Targets()
	}
	if v, ok := problemVerdict(abs, targets); ok {
		return v
	}
	tree := gitRootOf(abs)
	if tree == "" && within(root, abs) {
		tree = root
	}
	if tree == "" {
		return allow
	}
	if tree != root && !g.sameRepo(root, tree) {
		if v, ok := g.targetVerdict(abs, tree, targets); ok {
			return v
		}
		if inline {
			return allow
		}
		if len(targets) == 0 {
			return Verdict{Path: abs, Reason: fmt.Sprintf(
				"satelle: %s is in another repository's working tree (%s). This session works in %s; edit another repository from a session there.",
				abs, tree, root)}
		}
		return Verdict{Path: abs, Reason: fmt.Sprintf(
			"satelle: %s is in another repository's working tree (%s). This session's seats allow edits in %s; "+
				"edit another repository from a seat on an item that targets it (satelle story create --repo <name> --session <session>, then seat it). See satelle help edit-gate --session <session>.",
			abs, tree, g.editable(root, targets))}
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
			"Seat one with `satelle story seat <id> --session <session>` (see `satelle status --session <session>` and `satelle help edit-gate --session <session>`).%s", rel, "local:"+tree, elsewhere(targets))}
}

func problemVerdict(abs string, targets []Target) (Verdict, bool) {
	for _, t := range targets {
		if t.Problem == "" {
			continue
		}
		if len(t.Names) == 0 {
			return Verdict{Path: abs, Reason: fmt.Sprintf(
				"satelle: edit to %s blocked: %s. No path is editable from this session until its seats can be read. See satelle help edit-gate --session <session>.",
				abs, t.Problem)}, true
		}
		return Verdict{Path: abs, Reason: fmt.Sprintf(
			"satelle: edit to %s blocked: this session's seat on %s targets repo %s, and %s. No path is editable from this session until the registry resolves; "+
				"satelle doctor --session <session> reports it and names the fix. See satelle help edit-gate --session <session>.",
			abs, t.Item, reporeg.Literals(t.Names), t.Problem)}, true
	}
	return Verdict{}, false
}

func (g Gate) targetVerdict(abs, tree string, targets []Target) (Verdict, bool) {
	var matched []Target
	for _, t := range targets {
		if t.Tree != "" && t.Tree == tree {
			matched = append(matched, t)
		}
	}
	if len(matched) == 0 {
		return Verdict{}, false
	}
	rel := relSlash(tree, abs)
	for _, t := range matched {
		if t.Grant.AttachOnly {
			return Verdict{Path: abs, AttachOnly: true, Reason: attachOnlyReason(rel, "local:"+tree, t.Grant)}, true
		}
	}
	if matchAny(g.Locked, rel) {
		return Verdict{Path: abs, Reason: fmt.Sprintf(
			"satelle: %s is a locked path ([gate] locked_paths); no seat allows editing it.", rel)}, true
	}
	for _, t := range matched {
		if t.Grant.Tree {
			return allow, true
		}
	}
	return Verdict{}, false
}

func (g Gate) editable(root string, targets []Target) string {
	var parts []string
	if g.Grant != nil && g.Grant(root).Tree {
		parts = append(parts, "self at "+root)
	}
	for _, t := range targets {
		if t.Tree != "" {
			parts = append(parts, fmt.Sprintf("repo %s at %s (seat on %s)", reporeg.Literals(t.Names), t.Tree, t.Item))
		}
	}
	if len(parts) == 0 {
		return "no repository"
	}
	return strings.Join(parts, "; ")
}

func elsewhere(targets []Target) string {
	var b strings.Builder
	for _, t := range targets {
		if t.Tree != "" {
			fmt.Fprintf(&b, " This session's seat on %s targets repo %s, so its edits land in %s, not here.", t.Item, reporeg.Literals(t.Names), t.Tree)
		}
	}
	return b.String()
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

func Within(root, target string) bool { return within(root, target) }

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
