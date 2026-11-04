package verb

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/epicset"
	"github.com/bobmcallan/satelle/internal/workitem"
	"github.com/bobmcallan/satelle/internal/workspace"
)

// A cross-repo dependency is written depends-on:<repo>/<story id>, where <repo> is
// the name the workspace registry knows the other repo by (config.RepoName). Write
// time checks only that the tag is well formed and names exactly one other
// registered repo; whether the story exists, and whether it is finished, is wave's
// question, because both change after the tag is written.

// Seams a test replaces: the registry lookup, the invoking repo's root, and the
// read-only read of a sibling repo. Production uses the real ones.
var (
	findRegistered  = workspace.FindRegistered
	currentRepoRoot = changeRecordRepoRoot
	loadSibling     = workspace.LoadRepoReadOnly
)

// splitDependsOnTarget reports whether a depends-on target names a story in another
// repo — exactly one "/" — and splits it. Any other shape is a same-repo target.
func splitDependsOnTarget(target string) (repo, id string, cross bool) {
	if strings.Count(target, "/") != 1 {
		return "", "", false
	}
	repo, id, _ = strings.Cut(target, "/")
	return repo, id, true
}

// localDependsOnTargets is dependsOnTargets without the cross-repo ones: the
// targets the epic-set and cycle checks apply to.
func localDependsOnTargets(tags []string) []string {
	var out []string
	for _, t := range dependsOnTargets(tags) {
		if _, _, cross := splitDependsOnTarget(t); !cross {
			out = append(out, t)
		}
	}
	return out
}

// resolveSiblingRepo finds the one registered repo called name that is not the
// invoking repo (current, compared by canonical root so a worktree is its main
// tree). Zero, several, or this repo itself is an error saying which.
func resolveSiblingRepo(name, current string) (string, error) {
	roots, err := findRegistered(name)
	if err != nil {
		return "", fmt.Errorf("read the workspace registry: %w", err)
	}
	switch len(roots) {
	case 0:
		return "", fmt.Errorf("repo %s is not in the workspace registry (satelle workspace list)", name)
	case 1:
	default:
		return "", fmt.Errorf("ambiguous repo name %s, registered as %s", name, strings.Join(roots, ", "))
	}
	if roots[0] == config.CanonicalRepoRoot(current) {
		return "", fmt.Errorf("repo %s is this repo — name a story in it with depends-on:<story id>", name)
	}
	return roots[0], nil
}

// checkSiblingTarget is the write-time judgement of one cross-repo target.
func checkSiblingTarget(target, repo, id string) error {
	if repo == "" || !strings.HasPrefix(id, "sty_") {
		return fmt.Errorf("%s%s: does not name a story id (want %s<repo>/sty_<id>)", DependsOnPrefix, target, DependsOnPrefix)
	}
	if _, err := resolveSiblingRepo(repo, currentRepoRoot()); err != nil {
		return fmt.Errorf("%s%s: %w", DependsOnPrefix, target, err)
	}
	return nil
}

// unresolvedPrefix starts the status of a cross-repo dependency wave could not
// judge. It is a blocker, never a satisfied edge.
const unresolvedPrefix = "unresolved: "

func unresolved(format string, args ...any) waveState {
	return waveState{Status: unresolvedPrefix + fmt.Sprintf(format, args...)}
}

// siblingReader answers wave's question about stories in other repos. It reads
// each target once per wave call — the story and its containers, by id —
// in-process and read-only.
type siblingReader struct {
	views map[string]siblingView
}

type siblingView struct {
	rv  workspace.RepoView
	err error
}

func (r *siblingReader) view(ctx context.Context, repo, id string) siblingView {
	key := repo + "/" + id
	if v, ok := r.views[key]; ok {
		return v
	}
	var v siblingView
	if root, err := resolveSiblingRepo(repo, currentRepoRoot()); err != nil {
		v.err = err
	} else if v.rv, err = loadSibling(ctx, root, id); err != nil {
		v.err = fmt.Errorf("repo %s: %w", repo, err)
	}
	if r.views == nil {
		r.views = map[string]siblingView{}
	}
	r.views[key] = v
	return v
}

// laneTrunkTag marks a story that releases to trunk itself rather than through its
// epic's branch — the literal the release skills key on.
const laneTrunkTag = "lane:trunk"

// state is the wave state of story id in repo, judged on that repo's own route. A
// finished story satisfies the edge only when every container above it is
// finished too — a child's done does not carry the release, the epic's close does —
// unless it is tagged lane:trunk, which carries its own release to trunk.
// Anything that cannot be read is unresolved, which blocks.
func (r *siblingReader) state(ctx context.Context, repo, id string) waveState {
	v := r.view(ctx, repo, id)
	if v.err != nil {
		return unresolved("%v", v.err)
	}
	item, ok := findStory(v.rv.Stories, id)
	if !ok {
		return unresolved("story %s not found in repo %s", id, repo)
	}
	own := waveStateOf(v.rv.Workflows, item)
	if !own.Known {
		return unresolved("no route governs %s/%s (status %s)", repo, id, item.Status)
	}
	if !own.Done || slices.Contains(item.Tags, laneTrunkTag) {
		return own
	}
	open, reason := openContainerAbove(v.rv, item)
	if reason != "" {
		return unresolved("%s", reason)
	}
	if open != "" {
		own.Done = false
		own.Status = fmt.Sprintf("%s; container %s open", item.Status, open)
	}
	return own
}

func findStory(stories []workitem.Item, id string) (workitem.Item, bool) {
	for _, s := range stories {
		if s.ID == id {
			return s, true
		}
	}
	return workitem.Item{}, false
}

// openContainerAbove walks every container above item — its parent_id chain and
// the epic-parent of each epic:<theme> tag it carries, at every level — and
// returns the first that is not finished on its own route. reason is non-empty
// when a container cannot be judged (missing, no route, or an undetermined epic set).
func openContainerAbove(rv workspace.RepoView, item workitem.Item) (open, reason string) {
	visited := map[string]bool{item.ID: true}
	queue := []workitem.Item{item}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		parents, why := containersOf(rv.Stories, cur)
		if why != "" {
			return "", why
		}
		for _, p := range parents {
			if visited[p.ID] {
				continue
			}
			visited[p.ID] = true
			st := waveStateOf(rv.Workflows, p)
			if !st.Known {
				return "", fmt.Sprintf("no route governs container %s (status %s)", p.ID, p.Status)
			}
			if !st.Done {
				return p.ID, ""
			}
			queue = append(queue, p)
		}
	}
	return "", ""
}

// containersOf is the containers directly above item among stories: its parent_id
// parent and the one epic-parent per epic:<theme> tag. A parent_id that names no
// story read, or a theme with several epic-parents, cannot say whether item's
// container is finished, so it is a reason, not a guess.
func containersOf(stories []workitem.Item, item workitem.Item) (parents []workitem.Item, reason string) {
	if item.ParentID != "" {
		p, ok := findStory(stories, item.ParentID)
		if !ok {
			return nil, fmt.Sprintf("container %s of %s not found", item.ParentID, item.ID)
		}
		parents = append(parents, p)
	}
	for _, theme := range epicset.Themes(item) {
		var found []workitem.Item
		for _, s := range stories {
			if s.ID != item.ID && epicset.IsEpicParent(s) && slices.Contains(s.Tags, theme) {
				found = append(found, s)
			}
		}
		if len(found) > 1 {
			return nil, fmt.Sprintf("%d epic-parents carry %s above %s", len(found), theme, item.ID)
		}
		parents = append(parents, found...)
	}
	return parents, ""
}
