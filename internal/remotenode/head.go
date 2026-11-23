package remotenode

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/bobmcallan/satelle/internal/gitfacts"
	"github.com/bobmcallan/satelle/internal/remote/provider"
)

// stageHead writes a git bundle of ref from tree into in, with the tree's
// shallow file beside it when the tree is shallow, so a node with no network
// can check the commit out (checkoutHead). It reports whether the tree is
// shallow.
func stageHead(tree, in, ref string) (bool, error) {
	if err := os.MkdirAll(in, 0o755); err != nil {
		return false, err
	}
	repo := gitfacts.Repo{Dir: tree}
	if err := repo.Bundle(filepath.Join(in, gateBundle), ref); err != nil {
		return false, err
	}
	shallow, err := repo.ShallowCommits()
	if err != nil {
		return false, err
	}
	if len(shallow) > 0 {
		if err := os.WriteFile(filepath.Join(in, gateShallow), []byte(strings.Join(shallow, "\n")+"\n"), 0o644); err != nil {
			return false, err
		}
	}
	return len(shallow) > 0, nil
}

// checkoutHead checks commit out, detached, at root/work in the node, from the
// bundle stageHead wrote and the node copied to root/in, and verifies the work
// tree is at it. On a failure it returns the failing git step's output and
// name.
func checkoutHead(ctx context.Context, h provider.Handle, root, ref, commit string, shallow bool) ([]byte, string, error) {
	work := path.Join(root, "work")
	type fetchStep struct {
		name string
		argv []string
	}
	steps := []fetchStep{{"init", []string{"git", "init", "-q", work}}}
	if shallow {
		steps = append(steps, fetchStep{"shallow", []string{"cp", path.Join(root, "in", gateShallow), path.Join(work, ".git", gateShallow)}})
	}
	steps = append(steps,
		fetchStep{"fetch", []string{"git", "-C", work, "fetch", "-q", path.Join(root, "in", gateBundle), ref}},
		fetchStep{"checkout", []string{"git", "-C", work, "checkout", "-q", "--detach", commit}},
	)
	for _, step := range steps {
		if b, err := h.Exec(ctx, step.argv...); err != nil {
			return b, step.name, fmt.Errorf("the node could not check out the head %s (git %s): %w", commit, step.name, err)
		}
	}
	got, err := h.Exec(ctx, "git", "-C", work, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(string(got)) != commit {
		return nil, "rev-parse", fmt.Errorf("the node could not check out the head %s: it is at %q (%v)", commit, strings.TrimSpace(string(got)), err)
	}
	return nil, "", nil
}
