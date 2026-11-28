package agent

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/integration"
	"github.com/bobmcallan/satelle/internal/integration/common"
	"github.com/bobmcallan/satelle/internal/reporeg"
)

type Target = common.Target

func ConfineTarget(a Adapter, req Request) (Request, error) {
	if req.Target == nil || req.Target.Confined {
		return req, nil
	}
	t := *req.Target
	t.ReadDirs = slices.Clone(t.ReadDirs)
	b := req.Binding
	if t.Worktree == "" || !filepath.IsAbs(t.Worktree) || filepath.Clean(req.Dir) != filepath.Clean(t.Worktree) {
		return req, errors.New("target dispatch has no valid work tree")
	}
	if t.Scratch == "" || !filepath.IsAbs(t.Scratch) || filepath.Clean(t.Scratch) == filepath.Clean(t.Worktree) {
		return req, errors.New("target dispatch has no scratch folder")
	}
	for _, p := range integration.ProjectFiles() {
		path := filepath.Join(t.Worktree, filepath.FromSlash(p))
		_, err := os.Lstat(path)
		switch {
		case err == nil:
			return req, fmt.Errorf("satelle will not dispatch agent %s for target %s: its checkout %s holds %s, a harness settings, hook or MCP file that would configure the agent; remove it from the checkout or work on the item from an interactive session",
				b.Name, reporeg.Literal(t.Repo), reporeg.PathLiteral(t.Worktree), p)
		case errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR):
		default:
			return req, fmt.Errorf("satelle will not dispatch agent %s for target %s: it cannot tell whether %s exists (%v)",
				b.Name, reporeg.Literal(t.Repo), reporeg.PathLiteral(path), err)
		}
	}
	c := a.Confinement()
	narrow := func(reason string) {
		nb := b
		nb.Tools = nil
		for _, tool := range b.Tools {
			if slices.Contains(common.ReadTools, tool) {
				nb.Tools = append(nb.Tools, tool)
			}
		}
		b = nb
		t.Narrowed = reason
		req.Prompt += "\nYour grant in this target is read-only for this run: " + reason + ".\n"
	}
	if !common.IsReadOnly(b.Tools) && t.Protected {
		narrow(reporeg.Literal(t.Repo) + " is a protected repository: no dispatched performer holds a write grant there")
	}
	if !common.IsReadOnly(b.Tools) && !c.Context {
		return req, fmt.Errorf("satelle will not dispatch agent %s with a write grant into target %s on %s: %s; bind an agent whose harness records write confinement for a target, or a read-only grant",
			b.Name, reporeg.Literal(t.Repo), a.Name(), c.Refuse)
	}
	if !common.IsReadOnly(b.Tools) && !(c.FileTools && (c.Shell || !common.GrantsBash(b.Tools))) {
		narrow("the " + a.Name() + " harness record shows no confinement that keeps this grant's writes inside the work tree")
	}
	if !common.IsReadOnly(b.Tools) {
		rel, err := nestedContext(t.Worktree, c.OnRead)
		switch {
		case err != nil:
			return req, fmt.Errorf("satelle will not dispatch agent %s with a write grant into target %s: it cannot walk its checkout %s for instruction files (%v)",
				b.Name, reporeg.Literal(t.Repo), reporeg.PathLiteral(t.Worktree), err)
		case rel != "":
			return req, fmt.Errorf("satelle will not dispatch agent %s with a write grant into target %s: its checkout %s holds %s, which %s loads as instructions when a file under it is read or edited; remove it from the checkout, or dispatch the item with a read-only grant",
				b.Name, reporeg.Literal(t.Repo), reporeg.PathLiteral(t.Worktree), rel, a.Name())
		}
	}
	req.Binding = b
	if common.IsReadOnly(b.Tools) {
		t.Outside = true
		req.Dir = t.Scratch
		t.ReadDirs = append([]string{t.Worktree}, t.ReadDirs...)
		req.Prompt += "\nYour working directory " + reporeg.PathLiteral(t.Scratch) + " is an empty scratch folder. Read the target work tree at " +
			reporeg.PathLiteral(t.Worktree) + " by absolute path with your read tools; the changed paths above are relative to it.\n"
	} else {
		req.Prompt += "\nYour working directory is the target work tree.\n"
	}
	env, secrets := config.TargetAgentEnv(os.Environ(), c.AuthEnv, t.EnvNames)
	t.Environ = env
	req.Redact = common.Chain(req.Redact, common.ValueRedactor(secrets))
	mode := "inside"
	if t.Outside {
		mode = "outside"
	}
	req.Trace.Line("target: ", []byte(t.Repo+" "+t.Worktree+" "+mode+" "+req.Dir))
	if t.Narrowed != "" {
		req.Trace.Line("target grant narrowed: ", []byte(t.Narrowed))
	}
	t.Confined = true
	req.Target = &t
	return req, nil
}

var errFound = errors.New("found")

func nestedContext(root string, on common.OnRead) (string, error) {
	for _, p := range on.Paths {
		_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(p)))
		switch {
		case err == nil:
			return p, nil
		case errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR):
		default:
			return "", err
		}
	}
	if len(on.Nested) == 0 && len(on.Anywhere) == 0 {
		return "", nil
	}
	var found string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		if d.Name() == ".git" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		nested := filepath.Dir(rel) != "."
		if slices.Contains(on.Anywhere, d.Name()) || (nested && slices.Contains(on.Nested, d.Name())) {
			found = filepath.ToSlash(rel)
			return errFound
		}
		return nil
	})
	if errors.Is(err, errFound) {
		return found, nil
	}
	return "", err
}
