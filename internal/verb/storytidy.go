package verb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/fsmove"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// story-tidy / story-untidy (sty_d74e9b1b): a dispatched coder can CREATE files
// but has no grant to delete them, and the scope gate rejects untracked debris
// before the first step at which anyone may delete. These verbs break that
// deadlock by MOVING — never deleting — files the story itself created into a
// story-level tidy area, one ledger row per file, so every move can be undone.
//
// They are mechanism only: which paths are debris and whether the gate accepts
// afterwards stays with the reviewer skill. The eligibility rules below are the
// safety boundary, not a policy: a path is only ever moved when it is provably
// the story's own untracked creation.

func init() {
	Register(&Verb{
		Name:        "story-tidy",
		Description: "Move untracked files the story created into a reversible tidy area (never deletes)",
		Invoke:      storyTidy,
	})
	Register(&Verb{
		Name:        "story-untidy",
		Description: "Restore files previously moved by story-tidy",
		Invoke:      storyUntidy,
	})
}

type tidyReq struct {
	ID    string   `json:"id"`
	Paths []string `json:"paths,omitempty"`
	All   bool     `json:"all,omitempty"` // untidy only: restore every unrestored move
	// RepoRoot keys the scratch parent, matching the dispatch layout. The CLI
	// supplies the configured repo root; empty falls back to the worktree.
	RepoRoot string `json:"repo_root,omitempty"`
}

// tidyMove is one file moved (or restored), the payload of its ledger row.
type tidyMove struct {
	Src    string `json:"src"`
	Dst    string `json:"dst"`
	Action string `json:"action"`
}

// TidyRefusal names a path tidy declined to touch, and why.
type TidyRefusal struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// TidyResult is the verb response. Refused is non-empty only on a refusal, in
// which case nothing moved (the verb is all-or-nothing).
type TidyResult struct {
	StoryID string        `json:"story_id"`
	Moved   []tidyMove    `json:"moved,omitempty"`
	Refused []TidyRefusal `json:"refused,omitempty"`
}

// tidyContext is what both verbs need resolved before touching anything.
type tidyContext struct {
	item     workitem.Item
	worktree string
	baseHead string
	baseAt   time.Time
	scratch  string // repo root keying the tidy area
	cwd      string
}

func resolveTidyContext(ctx context.Context, req tidyReq) (tidyContext, error) {
	var tc tidyContext
	store, err := requireWorkItem()
	if err != nil {
		return tc, err
	}
	if strings.TrimSpace(req.ID) == "" {
		return tc, errors.New("story tidy: id required")
	}
	it, err := store.Get(ctx, req.ID)
	if err != nil {
		return tc, err
	}
	if it.Kind != workitem.KindStory {
		return tc, fmt.Errorf("story tidy: %s is not a story", req.ID)
	}
	// Any performing step. A terminal or unengaged story has nothing left to
	// tidy for; the verb itself takes no position on WHICH performing state.
	if engaging, ok := storyStatusIsEngaging(ctx, it, it.Status); ok && !engaging {
		return tc, fmt.Errorf("story tidy: %s is %s — not in a performing state", it.ID, it.Status)
	}
	base, _, at, err := firstEngagementBaseline(ctx, it.ID)
	if err != nil {
		return tc, fmt.Errorf("story tidy: story not engaged: %w", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return tc, err
	}
	wt := base.Worktree
	if wt == "" {
		wt = gitToplevel(cwd)
	}
	if wt == "" {
		return tc, errors.New("story tidy: cannot resolve the git worktree")
	}
	if real, err := filepath.EvalSymlinks(wt); err == nil {
		wt = real
	}
	root := req.RepoRoot
	if root == "" {
		root = wt
	}
	return tidyContext{item: it, worktree: wt, baseHead: base.HeadSHA, baseAt: at, scratch: root, cwd: cwd}, nil
}

func storyTidy(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var req tidyReq
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	if len(req.Paths) == 0 {
		return nil, errors.New("story tidy: at least one path required")
	}
	tc, err := resolveTidyContext(ctx, req)
	if err != nil {
		return nil, err
	}

	type plan struct{ abs, rel string }
	var plans []plan
	var refused []TidyRefusal
	for _, p := range req.Paths {
		abs, rel, reason := tc.eligible(p)
		if reason != "" {
			refused = append(refused, TidyRefusal{Path: p, Reason: reason})
			continue
		}
		plans = append(plans, plan{abs, rel})
	}
	if len(refused) > 0 {
		return refusal(tc.item.ID, refused)
	}

	res := TidyResult{StoryID: tc.item.ID}
	tidyRoot := config.TidyDir(tc.scratch, tc.item.ID)
	for _, pl := range plans {
		dst := uniqueDst(filepath.Join(tidyRoot, pl.rel))
		if err := fsmove.Move(pl.abs, dst); err != nil {
			return nil, fmt.Errorf("story tidy: move %s: %w (moved so far: %d)", pl.rel, err, len(res.Moved))
		}
		mv := tidyMove{Src: pl.abs, Dst: dst, Action: "tidy"}
		res.Moved = append(res.Moved, mv)
		recordTidy(ctx, tc.item.ID, ledger.KindTidy, mv)
	}
	return json.Marshal(res)
}

func storyUntidy(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var req tidyReq
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	if !req.All && len(req.Paths) == 0 {
		return nil, errors.New("story untidy: name the path(s) to restore, or pass --all")
	}
	tc, err := resolveTidyContext(ctx, req)
	if err != nil {
		return nil, err
	}
	pending, err := pendingTidy(ctx, tc.item.ID)
	if err != nil {
		return nil, err
	}

	var chosen []tidyMove
	var refused []TidyRefusal
	if req.All {
		chosen = pending
	} else {
		for _, p := range req.Paths {
			abs := tc.absPath(p)
			var hit *tidyMove
			for i := range pending {
				if pending[i].Src == abs {
					hit = &pending[i]
					break
				}
			}
			if hit == nil {
				refused = append(refused, TidyRefusal{Path: p, Reason: "not tidied (or already restored)"})
				continue
			}
			chosen = append(chosen, *hit)
		}
	}
	for _, mv := range chosen {
		if _, err := os.Lstat(mv.Src); err == nil {
			refused = append(refused, TidyRefusal{Path: mv.Src, Reason: "destination exists — will not overwrite"})
		} else if _, err := os.Lstat(mv.Dst); err != nil {
			refused = append(refused, TidyRefusal{Path: mv.Src, Reason: "tidied copy is gone: " + mv.Dst})
		}
	}
	if len(refused) > 0 {
		return refusal(tc.item.ID, refused)
	}

	res := TidyResult{StoryID: tc.item.ID}
	for _, mv := range chosen {
		if err := fsmove.Move(mv.Dst, mv.Src); err != nil {
			return nil, fmt.Errorf("story untidy: restore %s: %w (restored so far: %d)", mv.Src, err, len(res.Moved))
		}
		back := tidyMove{Src: mv.Src, Dst: mv.Dst, Action: "restore"}
		res.Moved = append(res.Moved, back)
		recordTidy(ctx, tc.item.ID, ledger.KindTidyRestore, back)
	}
	return json.Marshal(res)
}

// refusal renders every refused path in the error text, so a CLI caller sees
// the reasons without needing the JSON body.
func refusal(id string, refused []TidyRefusal) (json.RawMessage, error) {
	var parts []string
	for _, r := range refused {
		parts = append(parts, fmt.Sprintf("%s: %s", r.Path, r.Reason))
	}
	return nil, fmt.Errorf("story tidy: refused, nothing moved for %s — %s", id, strings.Join(parts, "; "))
}

// absPath makes p absolute against the caller's cwd and resolves symlinks in
// its PARENT only — the path itself is moved as a link, never followed — so it
// compares equal to the worktree however the caller spelled it.
func (tc tidyContext) absPath(p string) string {
	abs := p
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(tc.cwd, p)
	}
	abs = filepath.Clean(abs)
	if parent, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		abs = filepath.Join(parent, filepath.Base(abs))
	}
	return abs
}

// eligible applies the safety rules to one path. It returns the absolute and
// worktree-relative path, or a non-empty reason the path must not be touched.
// Only a path that is untracked, absent from HEAD and from the engagement
// baseline, and created after that baseline is ever eligible.
func (tc tidyContext) eligible(p string) (abs, rel, reason string) {
	abs = tc.absPath(p)
	if _, err := os.Lstat(abs); err != nil {
		return "", "", "not found"
	}
	rel, err := filepath.Rel(tc.worktree, abs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", "outside worktree"
	}
	if first := strings.SplitN(rel, string(filepath.Separator), 2)[0]; first == ".git" {
		return "", "", "outside worktree (inside .git)"
	}
	if GitListed(tc.worktree, "ls-files", "--", rel) {
		return "", "", "tracked"
	}
	if GitListed(tc.worktree, "ls-tree", "-r", "--name-only", "HEAD", "--", rel) {
		return "", "", "exists in HEAD"
	}
	if tc.baseHead != "" && GitListed(tc.worktree, "ls-tree", "-r", "--name-only", tc.baseHead, "--", rel) {
		return "", "", "exists in HEAD (engagement baseline)"
	}
	old := ""
	_ = filepath.WalkDir(abs, func(q string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil && info.ModTime().Before(tc.baseAt) {
			old = q
			return fs.SkipAll
		}
		return nil
	})
	if old != "" {
		return "", "", "predates engagement baseline"
	}
	return abs, rel, ""
}

// GitListed reports whether a git listing command names anything for its
// pathspec. A git error (e.g. no commits yet, or not a git repo) counts as
// "names nothing".
func GitListed(worktree string, args ...string) bool {
	cmd := exec.Command("git", append([]string{"-C", worktree}, args...)...)
	out, err := cmd.Output()
	return err == nil && len(strings.TrimSpace(string(out))) > 0
}

// GitCheckIgnoreSource reports whether git ignores path in worktree under its
// full rule set — the repo's .gitignore files, .git/info/exclude and the
// machine-wide core.excludesFile — whether or not the path is tracked. When it
// is ignored, source is the file holding the deciding rule as git names it
// (repo-relative for a .gitignore, absolute for an excludes file) and pattern
// is that rule as written.
func GitCheckIgnoreSource(worktree, path string) (ignored bool, source, pattern string) {
	out, err := exec.Command("git", "-C", worktree, "check-ignore", "-v", "--no-index", "--", path).Output()
	if err != nil {
		return false, "", ""
	}
	// <source>:<linenum>:<pattern>\t<pathname>
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\t")
	src, rest, ok := strings.Cut(line, ":")
	if !ok {
		return true, "", ""
	}
	_, pattern, _ = strings.Cut(rest, ":")
	return true, src, pattern
}

// uniqueDst avoids clobbering an earlier tidy of the same relative path.
func uniqueDst(dst string) string {
	if _, err := os.Lstat(dst); err != nil {
		return dst
	}
	for i := 1; ; i++ {
		cand := fmt.Sprintf("%s.%d", dst, i)
		if _, err := os.Lstat(cand); err != nil {
			return cand
		}
	}
}

func recordTidy(ctx context.Context, storyID, kind string, mv tidyMove) {
	payload, _ := json.Marshal(mv)
	appendLedgerEntry(ctx, storyID, kind, "executor", fmt.Sprintf("%s %s -> %s", mv.Action, mv.Src, mv.Dst), payload, time.Now().UTC())
}

// pendingTidy returns the tidy moves not yet restored, oldest first.
func pendingTidy(ctx context.Context, storyID string) ([]tidyMove, error) {
	ls, err := requireLedger()
	if err != nil {
		return nil, err
	}
	tidied, err := ls.ListByStory(ctx, storyID, ledger.KindTidy)
	if err != nil {
		return nil, err
	}
	restored, err := ls.ListByStory(ctx, storyID, ledger.KindTidyRestore)
	if err != nil {
		return nil, err
	}
	done := map[string]bool{}
	for _, e := range restored {
		var mv tidyMove
		if json.Unmarshal(e.Payload, &mv) == nil {
			done[mv.Dst] = true
		}
	}
	var out []tidyMove
	for _, e := range tidied {
		var mv tidyMove
		if json.Unmarshal(e.Payload, &mv) == nil && !done[mv.Dst] {
			out = append(out, mv)
		}
	}
	return out, nil
}
